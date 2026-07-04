#!/usr/bin/env python3
"""Deterministic ITC (Индекс технологичности) over the corpus.

Computes a technology-readiness index per THEME (cluster) — exactly as the source
ITC report does it, but from our own knowledge base, deterministically (no LLM
guessing): four components, a 1–10 band and a 0–1 TechScore.

  SM  научный моментум   ← свежесть и рост публикаций темы по годам (метаданные
                           published_at документов, фолбэк — год из библиошапки)
  NV  новизна / фронтир  ← семантическая удалённость центроида темы от центра корпуса
  IP  импакт             ← цитируемость документов темы в графе доказательств гипотез
  HR  диффузия / охват   ← охват темы: документы + организации

Each raw signal is normalised as a PERCENTILE RANK among the corpus themes
(доля тем корпуса с меньшим сигналом), so every axis reads «выше/ниже медианы
корпуса» and cannot collapse to zero on a small or homogeneous corpus. Document
ids come from cluster params.members (full membership) with the representative
list as fallback, so the index survives boards published without representatives.

Each hypothesis is linked to its nearest theme (primary_cluster_id) and inherits
the theme's ITC. Everything is pushed back through the public REST API:
PUT /clusters/{id} (params.itc) and POST /hypotheses/{id}/itc.

The formula is the committed rubric main-service/internal/application/itc_rubric.json
— same source of truth the API serves at GET /itc/rubric. Re-running gives
identical numbers (deterministic): "как часы".

Run locally:
    python3 itc-worker/itc.py
"""
import bisect
import datetime
import json
import os
import re
import sys
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

import numpy as np

_GW_BASE = os.environ.get("RAG_GW", "http://localhost:18080").rstrip("/")
GW = _GW_BASE if _GW_BASE.endswith("/api/v1") else _GW_BASE + "/api/v1"
EMB_URL = os.environ.get("EMB_URL", "http://localhost:8085/v1/embeddings")
USER = os.environ.get("RAG_USER", "admin")
PASS = os.environ.get("RAG_PASS", "")
EMB_BATCH = int(os.environ.get("EMB_BATCH", "64"))
REPS_PER_CLUSTER = int(os.environ.get("ITC_REPS", "5"))
DRY = os.environ.get("ITC_DRY", "") not in ("", "0", "false")
LOCAL_RUBRIC = os.path.join(os.path.dirname(__file__), "itc_rubric.json")
REPO_RUBRIC = os.path.join(
    os.path.dirname(__file__), "..", "main-service", "internal", "application", "itc_rubric.json"
)
RUBRIC_PATH = os.environ.get(
    "ITC_RUBRIC_PATH",
    LOCAL_RUBRIC if os.path.exists(LOCAL_RUBRIC) else REPO_RUBRIC,
)

TECH_W = (0.40, 0.25, 0.20, 0.15)  # SM, NV, IP, HR weights (TechScore)


def http(url, payload=None, token=None, timeout=180, method=None):
    data = json.dumps(payload).encode() if payload is not None else None
    m = method or ("POST" if data else "GET")
    req = urllib.request.Request(url, data=data, method=m)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        body = r.read().decode()
        return json.loads(body) if body.strip() else {}


TOKEN = {"v": None}


def login():
    TOKEN["v"] = http(f"{GW}/auth/login", {"username": USER, "password": PASS})["access_token"]


def gw(path, payload=None, method=None):
    """Gateway call with one re-login retry on 401 (a long run outlives the JWT)."""
    url = f"{GW}{path}"
    try:
        return http(url, payload, token=TOKEN["v"], method=method)
    except urllib.error.HTTPError as e:
        if e.code == 401:
            login()
            return http(url, payload, token=TOKEN["v"], method=method)
        raise


def embed_all(texts):
    out = []
    for start in range(0, len(texts), EMB_BATCH):
        batch = texts[start:start + EMB_BATCH]
        resp = http(EMB_URL, {"model": "F2LLM-v2-0.6B", "input": batch})
        out.extend(row["embedding"] for row in resp["data"])
    return out


# ---- bibliometric extraction (deterministic regex over the bibliographic header) ----

YEAR_RE = re.compile(r"\b(19[89]\d|20[0-2]\d)\b")
# Affiliation/publisher cues → count distinct organisations (talent_share proxy).
ORG_RE = re.compile(
    r"\b([A-ZА-Я][\w&.\-]+(?:\s+[A-ZА-Яa-zа-я&.\-]+){0,4}\s+"
    r"(?:University|Universit\w+|Institute|Institut\w+|Laboratory|Laborator\w+|"
    r"Academy|Corporation|Company|Inc\.?|Ltd\.?|GmbH|"
    r"университет\w*|институт\w*|академи\w+|лаборатори\w+|компани\w+))"
)


def extract_year(text):
    """Most likely publication year: the latest plausible year in the header region
    (first ~800 chars), where the document's own year dominates citations."""
    head = text[:800]
    years = [int(y) for y in YEAR_RE.findall(head)]
    years = [y for y in years if 1980 <= y <= 2027]
    return max(years) if years else None


def meta_year(published_at):
    m = re.match(r"\s*(\d{4})", published_at or "")
    if not m:
        return None
    y = int(m.group(1))
    return y if 1980 <= y <= 2027 else None


def extract_orgs(text):
    return {m.strip() for m in ORG_RE.findall(text[:1500])}


# ---- scoring ----

def cosine(a, b):
    na, nb = np.linalg.norm(a), np.linalg.norm(b)
    if na == 0 or nb == 0:
        return 0.0
    return float(np.dot(a, b) / (na * nb))


def clamp01(x):
    return max(0.0, min(1.0, x))


def percentile_ranks(values):
    """Rank-based norm in [0,1]: share of themes with a smaller signal (ties get
    the mid rank, None → neutral 0.5). All-equal signals map to 0.5, never 0."""
    known = sorted(v for v in values if v is not None)
    n = len(known)
    out = []
    for v in values:
        if v is None or n <= 1:
            out.append(0.5)
            continue
        lo = bisect.bisect_left(known, v)
        hi = bisect.bisect_right(known, v)
        out.append((lo + 0.5 * (hi - lo)) / n)
    return out


def band_note(rubric, score):
    for b in rubric["bands"]:
        if b["min"] <= score <= b["max"]:
            return {"label": b["label"], "note": b["note"]}
    return {"label": "", "note": ""}


def comp_note(rubric, key, norm):
    for c in rubric["components"]:
        if c["key"] == key:
            return c["interpretation"]["high" if norm >= 0.5 else "low"], c["name"]
    return "", key


def compute_components(rubric, norms, sm_known):
    vals = {k: round(100.0 * norms[k]) for k in ("SM", "NV", "IP", "HR")}
    components = {}
    for k in ("SM", "NV", "IP", "HR"):
        note, name = comp_note(rubric, k, norms[k])
        if k == "SM" and not sm_known:
            note = "год публикаций не определён по корпусу"
        components[k] = {"key": k, "name": name, "value": vals[k], "norm": round(norms[k], 3), "note": note}
    techscore = sum(w * norms[k] for w, k in zip(TECH_W, ("SM", "NV", "IP", "HR")))
    # Band — position in momentum × novelty (White Space), modulated by impact/maturity.
    g = (norms["SM"] ** 0.6) * (norms["NV"] ** 0.4)
    g2 = g * (0.8 + 0.2 * (0.5 * norms["IP"] + 0.5 * norms["HR"]))
    band = max(1, min(10, round(1 + 9 * g2)))
    return components, round(techscore, 3), band


def build_itc(rubric, components, norms, techscore, band, signals, scope):
    return {
        "score": band,
        "band": band_note(rubric, band),
        "techscore": techscore,
        "components": components,
        "axes": {
            "momentum": round(norms["SM"], 3), "novelty": round(norms["NV"], 3),
            "impact": round(norms["IP"], 3), "diffusion": round(norms["HR"], 3),
        },
        "signals": signals,
        "method": "itc-v2-rank",
        "scope": scope,
        "computed_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }


def first_chunk_text(doc_id, cache):
    if doc_id in cache:
        return cache[doc_id]
    try:
        payload = gw(f"/documents/{doc_id}/chunks")
        chunks = payload.get("chunks", [])
        txt = chunks[0]["text"] if chunks else ""
    except Exception as e:  # noqa: BLE001 — a missing doc just means no bibliometrics
        print(f"  (chunk fetch failed for {doc_id}: {e})", file=sys.stderr)
        txt = ""
    cache[doc_id] = txt
    return txt


def cluster_members(c):
    params = c.get("params") or {}
    if isinstance(params, str):
        try:
            params = json.loads(params)
        except Exception:  # noqa: BLE001
            params = {}
    members = params.get("members") if isinstance(params, dict) else None
    if isinstance(members, (list, tuple)):
        return [str(m) for m in members if m]
    return []


def probe_doc_ids(c):
    """Documents whose first chunk feeds bibliometrics/centroids: the
    representative list, or the first members when the board has no reps."""
    ids = [
        rep.get("document_id")
        for rep in (c.get("representatives") or [])[:REPS_PER_CLUSTER]
        if isinstance(rep, dict) and rep.get("document_id")
    ]
    return ids or cluster_members(c)[:REPS_PER_CLUSTER]


def rep_snippets(c):
    out = {}
    for rep in (c.get("representatives") or [])[:REPS_PER_CLUSTER]:
        if isinstance(rep, dict) and rep.get("document_id") and rep.get("snippet"):
            out[rep["document_id"]] = rep["snippet"]
    return out


def main():
    with open(RUBRIC_PATH, encoding="utf-8") as f:
        rubric = json.load(f)
    login()

    clusters = gw("/clusters")
    hyps = gw("/hypotheses")
    graph = gw("/hypotheses/graph")
    docs_meta = {d["id"]: d for d in (gw("/documents") or []) if isinstance(d, dict) and d.get("id")}
    edges = graph.get("edges", [])
    # doc_id -> #incoming evidence edges (citation count inside our graph)
    cite_by_doc = {}
    for e in edges:
        cite_by_doc[e["target"]] = cite_by_doc.get(e["target"], 0) + 1
    print(
        f"clusters={len(clusters)} hypotheses={len(hyps)} graph_edges={len(edges)} docs={len(docs_meta)}",
        file=sys.stderr,
    )

    probe_ids_by_cluster = {c["id"]: probe_doc_ids(c) for c in clusters}
    all_probe_ids = {d for ids in probe_ids_by_cluster.values() for d in ids}
    chunk_cache = {}
    with ThreadPoolExecutor(max_workers=8) as ex:
        list(ex.map(lambda d: first_chunk_text(d, chunk_cache), all_probe_ids))
    print(f"fetched first-chunk text for {len(chunk_cache)} probe docs", file=sys.stderr)

    # Embed: probe first-chunks (for centroids) + hypothesis statements.
    emb_texts, emb_index = [], {}

    def want(text):
        key = text[:512]
        if key not in emb_index:
            emb_index[key] = len(emb_texts)
            emb_texts.append(key)
        return emb_index[key]

    probe_text = {}
    for c in clusters:
        snippets = rep_snippets(c)
        for did in probe_ids_by_cluster[c["id"]]:
            t = chunk_cache.get(did, "") or snippets.get(did, "")
            if t:
                probe_text[(c["id"], did)] = t
                want(t)
    for h in hyps:
        want(h.get("statement", "") or h.get("title", ""))
    vecs = np.asarray(embed_all(emb_texts), dtype=np.float32) if emb_texts else np.zeros((0, 1))
    print(f"embedded {len(emb_texts)} texts", file=sys.stderr)

    def vec_for(text):
        return vecs[emb_index[text[:512]]]

    # Cluster centroids + corpus centroid (weighted by document_count).
    centroids, weights = {}, []
    for c in clusters:
        member_vecs = [
            vec_for(probe_text[(c["id"], did)])
            for did in probe_ids_by_cluster[c["id"]]
            if (c["id"], did) in probe_text
        ]
        if member_vecs:
            centroids[c["id"]] = np.mean(member_vecs, axis=0)
            weights.append((centroids[c["id"]], max(1, c.get("document_count", 1))))
    if weights:
        corpus_centroid = np.average([w[0] for w in weights], axis=0, weights=[w[1] for w in weights])
    else:
        corpus_centroid = np.zeros(vecs.shape[1] if vecs.size else 1)

    # ---- pass 1: raw signals per cluster ----
    raw = []
    for c in clusters:
        probe_ids = probe_ids_by_cluster[c["id"]]
        members = cluster_members(c) or probe_ids
        doc_count = c.get("document_count", 0) or len(members)
        years, orgs = [], set()
        for did in dict.fromkeys(members + probe_ids):
            y = meta_year((docs_meta.get(did) or {}).get("published_at"))
            if y is None:
                y = extract_year(chunk_cache.get(did, ""))
            if y:
                years.append(y)
        for did in probe_ids:
            orgs |= extract_orgs(chunk_cache.get(did, ""))
        nov_dist = 1.0 - cosine(centroids[c["id"]], corpus_centroid) if c["id"] in centroids else None
        cites = sum(
            cite_by_doc.get(d, 0) + cite_by_doc.get(f"doc:{d}", 0)
            for d in dict.fromkeys(members + probe_ids)
        )
        raw.append({
            "cluster": c, "years": years, "orgs": orgs, "doc_count": doc_count,
            "nov_dist": nov_dist, "cites": cites, "ip_raw": cites / max(1, doc_count),
        })

    all_years = [y for r in raw for y in r["years"]]
    ymin, ymax = (min(all_years), max(all_years)) if all_years else (0, 0)
    max_docs = max((r["doc_count"] for r in raw), default=0)
    max_orgs = max((len(r["orgs"]) for r in raw), default=0)
    for r in raw:
        years = r["years"]
        if years and ymax > ymin:
            mean_recency = sum((y - ymin) / (ymax - ymin) for y in years) / len(years)
            recent_share = sum(1 for y in years if y >= ymax - 2) / len(years)
            r["sm_raw"] = 0.6 * mean_recency + 0.4 * recent_share
        else:
            r["sm_raw"] = None
        r["hr_raw"] = (
            0.7 * (r["doc_count"] / max_docs if max_docs else 0.0)
            + 0.3 * (len(r["orgs"]) / max_orgs if max_orgs else 0.0)
        )
    print(f"corpus years: {ymin}..{ymax}; themes with years: "
          f"{sum(1 for r in raw if r['years'])}/{len(raw)}", file=sys.stderr)

    # ---- pass 2: percentile-rank axes across the board, build + store ITC ----
    sm_p = percentile_ranks([r["sm_raw"] for r in raw])
    nv_p = percentile_ranks([r["nov_dist"] for r in raw])
    ip_p = percentile_ranks([r["ip_raw"] for r in raw])
    hr_p = percentile_ranks([r["hr_raw"] for r in raw])

    cluster_itc = {}
    rows = []
    for i, r in enumerate(raw):
        c = r["cluster"]
        norms = {"SM": sm_p[i], "NV": nv_p[i], "IP": ip_p[i], "HR": hr_p[i]}
        comps, tech, band = compute_components(rubric, norms, sm_known=r["sm_raw"] is not None)
        years, orgs = r["years"], r["orgs"]
        signals = {
            "years": sorted(set(years)), "year_min": (min(years) if years else None),
            "year_max": (max(years) if years else None), "pub_count": len(years),
            "org_count": len(orgs), "orgs": sorted(orgs)[:8],
            "evidence_citations": r["cites"],
            "novelty_distance": round(r["nov_dist"], 3) if r["nov_dist"] is not None else None,
            "document_count": r["doc_count"],
        }
        itc = build_itc(rubric, comps, norms, tech, band, signals, "cluster")
        cluster_itc[c["id"]] = itc
        rows.append((band, tech, c.get("label", "")[:42], comps))
        if not DRY:
            params = c.get("params") or {}
            if not isinstance(params, dict):
                params = {}
            params["itc"] = itc
            gw(f"/clusters/{c['id']}", {
                "label": c.get("label", ""), "summary": c.get("summary", ""),
                "keywords": c.get("keywords") or [], "method": c.get("method", ""),
                "chunk_count": c.get("chunk_count", 0), "document_count": c.get("document_count", 0),
                "representatives": c.get("representatives") or [], "params": params,
                "status": c.get("status", ""),
            }, method="PUT")

    # ---- per-hypothesis: assign nearest theme, inherit its ITC ----
    assigned = 0
    cl_ids = [c["id"] for c in clusters if c["id"] in centroids]
    cl_mat = np.asarray([centroids[i] for i in cl_ids], dtype=np.float32) if cl_ids else np.zeros((0, 1))
    for h in hyps:
        stmt = h.get("statement", "") or h.get("title", "")
        if not stmt or not cl_ids:
            continue
        hv = vec_for(stmt)
        sims = cl_mat @ hv / (np.linalg.norm(cl_mat, axis=1) * (np.linalg.norm(hv) or 1.0) + 1e-9)
        best = cl_ids[int(np.argmax(sims))]
        itc = dict(cluster_itc[best])
        itc["scope"] = "hypothesis(theme)"
        if not DRY:
            # composite_score is deliberately not sent: main-service ignores it now.
            gw(f"/hypotheses/{h['id']}/itc", {"cluster_id": best, "itc": itc})
        assigned += 1

    rows.sort(reverse=True)
    print("\n=== ITC per theme (top by band) ===")
    for band, tech, label, comps in rows[:40]:
        cs = " ".join(f"{k}={comps[k]['value']:>3}" for k in ("SM", "NV", "IP", "HR"))
        print(f"  {band:>2}/10  tech={tech:.2f}  {cs}  {label}")
    print(f"\nclusters scored: {len(rows)};  hypotheses linked+scored: {assigned}"
          f"{'  (DRY RUN — nothing written)' if DRY else ''}")


def _selfcheck():
    ranks = percentile_ranks([0.0, 0.0, 1.0, 2.0, None])
    assert ranks[0] == ranks[1] == 0.25 and ranks[2] == 0.625 and ranks[3] == 0.875
    assert ranks[4] == 0.5
    assert percentile_ranks([7, 7, 7]) == [0.5, 0.5, 0.5]
    assert percentile_ranks([]) == []
    with open(LOCAL_RUBRIC, encoding="utf-8") as f:
        rubric = json.load(f)
    hi = {"SM": 0.9, "NV": 0.9, "IP": 0.9, "HR": 0.9}
    lo = {"SM": 0.1, "NV": 0.1, "IP": 0.1, "HR": 0.1}
    mid = {"SM": 0.5, "NV": 0.5, "IP": 0.5, "HR": 0.5}
    comps_hi, tech_hi, band_hi = compute_components(rubric, hi, True)
    comps_lo, tech_lo, band_lo = compute_components(rubric, lo, True)
    _, tech_mid, band_mid = compute_components(rubric, mid, True)
    assert band_lo < band_mid < band_hi and tech_lo < tech_mid < tech_hi
    assert comps_hi["SM"]["value"] == 90 and comps_lo["NV"]["norm"] == 0.1
    comps_unk, _, _ = compute_components(rubric, mid, False)
    assert comps_unk["SM"]["note"] == "год публикаций не определён по корпусу"
    assert meta_year("2024-06-01") == 2024 and meta_year("1601") is None and meta_year("") is None
    print("selfcheck ok")


if __name__ == "__main__":
    if "--selfcheck" in sys.argv:
        _selfcheck()
    else:
        main()
