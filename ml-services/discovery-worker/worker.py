#!/usr/bin/env python3
"""Discovery worker — turns close-KPI transfer opportunities (and, in legacy mode,
graph bridges) into candidate hypotheses on the board, hands-free.

Watches the corpus epoch in Valkey (bumped by main-service every time a document
finishes indexing). When the epoch moves and then holds steady for DEBOUNCE_SEC
(ingestion has settled), it runs discovery in one of two modes (DISCOVERY_MODE):

  * transfer (default) — the KPI layer. It embeds every KPI (title + metric +
    description) via f2llm, finds semantically CLOSE KPI pairs, and where one KPI
    has strong hypotheses while its neighbour lacks coverage, asks the LLM to
    CARRY a top donor hypothesis onto the recipient KPI as an adapted, testable
    transfer. transfer_score = kpi_similarity · source_strength · (1 −
    target_coverage). graph-compute is not used.
  * bridge (legacy) — the theme layer. It asks graph-compute to score
    cross-community "bridges" (structural holes between Leiden themes with ABC
    mediators) and turns the strongest into cross-cluster hypotheses.

Every candidate passes a mandatory anti-hype VALIDATION GATE before it is
published: grounding (it must cite real document ids — for a transfer, the donor
hypothesis's own evidence), a not-a-duplicate check against the target, and a
SciMON novelty probe (the statement, embedded and searched against the corpus,
must not merely paraphrase text that already exists). The LLM's own opinion of
novelty is never trusted. bridge mode additionally requires a convergence floor.

This worker computes NOTHING heavy itself: embeddings come from f2llm and the
legacy graph (kNN + Leiden + bridge scoring) lives in graph-compute. It only
orchestrates — generate, validate, publish — mirroring cluster-worker.
"""
import hashlib
import json
import math
import os
import sys
import time
import urllib.error
import urllib.request

import grpc
from genproto.graph.v1 import graph_pb2, graph_pb2_grpc

GW = os.environ.get("RAG_GW", "http://nginx/api/v1")
USER = os.environ.get("RAG_USER", "admin")
PASS = os.environ.get("RAG_PASS", "")
COLLECTION = os.environ.get("QDRANT_COLLECTION", "documents")
QDRANT_URL = os.environ.get("QDRANT_URL", "http://qdrant:6333")
GRAPH_COMPUTE_ADDR = os.environ.get("GRAPH_COMPUTE_ADDR", "graph-compute:9093")
GRAPH_COMPUTE_TIMEOUT = float(os.environ.get("GRAPH_COMPUTE_TIMEOUT", "600"))
VALKEY_URL = os.environ.get("VALKEY_URL", "redis://valkey:6379")
EPOCH_KEY = os.environ.get("EPOCH_KEY", "rag:corpus_epoch:shared")
LAST_KEY = os.environ.get("LAST_KEY", "rag:discovery:last_epoch")
WORKER_STATUS_KEY = "rag:worker:discovery:status"
CHECK_INTERVAL = int(os.environ.get("CHECK_INTERVAL", "15"))
DEBOUNCE_SEC = int(os.environ.get("DEBOUNCE_SEC", "45"))

# Cluster graph knobs. These MUST match cluster-worker: graph-compute rebuilds the
# same kNN + Leiden partition for bridge scoring, so identical knobs (and the
# fixed leiden/seed=42) reproduce the exact communities — and fingerprints — the
# published board was built from, letting a bridge be tied back to a real cluster.
KNN_K = int(os.environ.get("KNN_K", "6"))
KNN_BLOCK_SIZE = max(64, int(os.environ.get("KNN_BLOCK_SIZE", "512")))
RESOLUTION = float(os.environ.get("RESOLUTION", "1.0"))
MIN_SIZE = max(2, int(os.environ.get("MIN_SIZE", "2")))
SIM_THRESHOLD = max(0.0, min(1.0, float(os.environ.get("SIM_THRESHOLD", "0.0"))))
MUTUAL_KNN = os.environ.get("MUTUAL_KNN", "true").lower() not in {"0", "false", "no", "off"}
CHUNK_WEIGHTING = os.environ.get("CHUNK_WEIGHTING", "true").lower() not in {"0", "false", "no", "off"}
CHUNK_LEAD_COUNT = max(0, int(os.environ.get("CHUNK_LEAD_COUNT", "2")))
CHUNK_W_LEAD = max(0.0, float(os.environ.get("CHUNK_W_LEAD", "2.0")))
CHUNK_W_RESULTS = max(0.0, float(os.environ.get("CHUNK_W_RESULTS", "1.5")))
CHUNK_W_REFS = max(0.0, float(os.environ.get("CHUNK_W_REFS", "0.05")))
LINEAGE_OVERLAP_MIN = max(0.0, min(1.0, float(os.environ.get("LINEAGE_OVERLAP_MIN", "0.3"))))
CLUSTER_ALGORITHM = "leiden"
CLUSTER_SEED = 42

# Bridge knobs (discovery layer). Forwarded straight to graph-compute's
# BridgeConfig: how many bridges to score, the affinity floor for a candidate
# pair, ABC mediators per bridge, the convergence floor, and the composite
# weights for the maverick / bridging / vanguard signals.
BRIDGE_TOP_N = max(1, int(os.environ.get("BRIDGE_TOP_N", "50")))
BRIDGE_MIN_AFFINITY = max(0.0, min(1.0, float(os.environ.get("BRIDGE_MIN_AFFINITY", "0.15"))))
BRIDGE_MIN_CONVERGENCE = max(0, int(os.environ.get("BRIDGE_MIN_CONVERGENCE", "2")))
BRIDGE_MAX_MEDIATORS = max(1, int(os.environ.get("BRIDGE_MAX_MEDIATORS", "3")))
BRIDGE_W_MAVERICK = max(0.0, float(os.environ.get("BRIDGE_W_MAVERICK", "1.0")))
BRIDGE_W_BRIDGING = max(0.0, float(os.environ.get("BRIDGE_W_BRIDGING", "1.0")))
BRIDGE_W_VANGUARD = max(0.0, float(os.environ.get("BRIDGE_W_VANGUARD", "0.5")))

# Generation (LLM) + the anti-hype validation gate.
EMBEDDINGS_URL = os.environ.get("EMBEDDINGS_URL", "http://f2llm-service:8085/v1/embeddings")
EMBEDDINGS_MODEL = os.environ.get("EMBEDDINGS_MODEL", "codefuse-ai/F2LLM-v2-0.6B")
VLLM_URL = os.environ.get("VLLM_URL", "")
VLLM_MODEL = os.environ.get("VLLM_MODEL", "")
VLLM_API_KEY = os.environ.get("VLLM_API_KEY", "")
VLLM_RPM = max(0, int(os.environ.get("VLLM_RPM", "18")))
DISCOVERY_LIMIT = max(0, int(os.environ.get("DISCOVERY_LIMIT", "24")))
# SciMON novelty floor: a statement whose top cosine against the corpus exceeds
# this just rephrases existing text and is rejected (0.92 = near-duplicate).
NOVELTY_MAX_SIM = max(0.0, min(1.0, float(os.environ.get("NOVELTY_MAX_SIM", "0.92"))))
AUTO_DISCOVERY = os.environ.get("AUTO_DISCOVERY", "true").lower() not in {"0", "false", "no", "off"}

# Discovery mode. "transfer" (default) carries a strong hypothesis from a KPI that
# has one onto a semantically close KPI that lacks coverage — graph-compute is not
# touched. The legacy "bridge" mode scores graph-compute structural-hole bridges.
DISCOVERY_MODE = os.environ.get("DISCOVERY_MODE", "transfer").strip().lower()
# Transfer knobs. Two KPIs count as close at cosine >= TRANSFER_MIN_SIM; a pair is
# only worth transferring when the donor has at least TRANSFER_MIN_GAP more
# non-failed hypotheses than the recipient (the coverage gap) AND the recipient is
# not already covered (< TRANSFER_COVERAGE_FULL non-failed hypotheses — which also
# floors target_coverage below 1 so transfer_score stays non-zero). Up to
# TRANSFER_PER_PAIR donor hypotheses seed each pair; at most TRANSFER_MAX_PAIRS
# pairs per run (0 = no cap).
TRANSFER_MIN_SIM = max(0.0, min(1.0, float(os.environ.get("TRANSFER_MIN_SIM", "0.55"))))
TRANSFER_PER_PAIR = max(1, int(os.environ.get("TRANSFER_PER_PAIR", "2")))
TRANSFER_COVERAGE_FULL = max(1, int(os.environ.get("TRANSFER_COVERAGE_FULL", "5")))
TRANSFER_MIN_GAP = max(1, int(os.environ.get("TRANSFER_MIN_GAP", "1")))
TRANSFER_MAX_PAIRS = max(0, int(os.environ.get("TRANSFER_MAX_PAIRS", "60")))
# A hypothesis in one of these statuses does not count toward a KPI's coverage.
FAILED_STATUS = {"rejected", "archived"}
_LAST_VLLM_CALL = {"t": 0.0}
TOKEN = {"v": None}


def clamp01(value):
    return max(0.0, min(1.0, float(value)))


def first_sentence(value, limit=320):
    text = " ".join((value or "").split())
    if not text:
        return ""
    cut = len(text)
    for sep in (". ", "! ", "? "):
        idx = text.find(sep)
        if idx > 24:
            cut = min(cut, idx + len(sep))
    out = text[: min(cut, limit)].strip()
    if len(text) > len(out) and not out.endswith(("...", ".", "!", "?")):
        out = out.rstrip(" ,;:") + "..."
    return out


def as_dict(value):
    """A JSONB field as a dict — accepts a dict or a raw-JSON string (db DTOs)."""
    if isinstance(value, dict):
        return value
    if isinstance(value, str) and value.strip():
        try:
            obj = json.loads(value)
        except Exception:  # noqa: BLE001 — tolerate malformed JSONB, treat as empty
            return {}
        return obj if isinstance(obj, dict) else {}
    return {}


def as_list(value):
    """A JSONB array field as a list — accepts a list or a raw-JSON string."""
    if isinstance(value, list):
        return value
    if isinstance(value, str) and value.strip():
        try:
            obj = json.loads(value)
        except Exception:  # noqa: BLE001 — tolerate malformed JSONB, treat as empty
            return []
        return obj if isinstance(obj, list) else []
    return []


def http(url, payload=None, token=None, timeout=180, method=None):
    data = json.dumps(payload).encode() if payload is not None else None
    m = method or ("POST" if data is not None else "GET")
    req = urllib.request.Request(url, data=data, method=m)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        body = r.read().decode()
        return json.loads(body) if body else {}


def throttle_vllm():
    if VLLM_RPM <= 0:
        return
    gap = 60.0 / float(VLLM_RPM)
    elapsed = time.monotonic() - _LAST_VLLM_CALL["t"]
    if elapsed < gap:
        time.sleep(gap - elapsed)
    _LAST_VLLM_CALL["t"] = time.monotonic()


def login():
    TOKEN["v"] = http(f"{GW}/auth/login", {"username": USER, "password": PASS})["access_token"]


def gw(path, payload=None, method=None):
    """Gateway call with one re-login retry on 401."""
    url = f"{GW}{path}"
    try:
        return http(url, payload, token=TOKEN["v"], method=method)
    except urllib.error.HTTPError as e:
        if e.code == 401:
            login()
            return http(url, payload, token=TOKEN["v"], method=method)
        raise


def redis_int(r, key):
    raw = r.get(key)
    return int(raw) if raw else None


# Same helper in every worker (clusters/discovery/raptor/itc) — no shared lib yet.
_STATUS = {"r": None, "last_success_at": None, "run_started": None, "last_run_seconds": None}


def record_llm_usage(model, operation, usage):
    """Best-effort OpenRouter token/cost tally into the shared Valkey ledger
    (rag:llm:usage:{YYYYMMDD} hash + per-minute counter, read by main-service).
    Fire-and-forget like report_status — never breaks or slows the worker."""
    r = _STATUS["r"]
    if r is None or not isinstance(usage, dict):
        return
    try:
        pt = int(usage.get("prompt_tokens") or 0)
        ct = int(usage.get("completion_tokens") or 0)
        cost = round(float(usage.get("cost") or 0) * 1e9)
        day = time.strftime("%Y%m%d", time.gmtime())
        key = "rag:llm:usage:" + day
        sep = "\x1f"
        for metric, val in (("req", 1), ("pt", pt), ("ct", ct), ("cost", cost)):
            r.hincrby(key, f"{model}{sep}{operation}{sep}{metric}", int(val))
        r.expire(key, 3024000)  # 35 days
        mkey = "rag:llm:reqmin:" + str(int(time.time() // 60))
        r.incr(mkey)
        r.expire(mkey, 120)
    except Exception as e:  # noqa: BLE001 — usage telemetry, never break the worker
        print(f"llm usage record failed: {e}", file=sys.stderr, flush=True)


def report_status(state, **fields):
    """Worker status snapshot in Valkey for GET /system/activity (best-effort)."""
    now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    if state == "running":
        if _STATUS["run_started"] is None:  # progress re-reports keep the original start
            _STATUS["run_started"] = time.monotonic()
    elif state == "idle":
        _STATUS["last_success_at"] = now
        if _STATUS["run_started"] is not None:
            _STATUS["last_run_seconds"] = round(time.monotonic() - _STATUS["run_started"], 3)
        _STATUS["run_started"] = None
    else:  # error: keep the previous duration, drop the aborted run's start
        _STATUS["run_started"] = None
    status = {
        "state": state,
        "epoch": None,
        "progress_done": None,
        "progress_total": None,
        "updated_at": now,
        "last_error": None,
        "last_success_at": _STATUS["last_success_at"],
        "last_run_seconds": _STATUS["last_run_seconds"],
    }
    status.update(fields)
    if _STATUS["r"] is None:
        return
    try:
        _STATUS["r"].set(WORKER_STATUS_KEY, json.dumps(status))
    except Exception as e:  # noqa: BLE001 — status is telemetry, never break the worker
        print(f"status report failed: {e}", file=sys.stderr, flush=True)


def owner_of(docs):
    """The single owner id behind the owner-scoped /documents list, or None."""
    owners = {d.get("owner_id") for d in docs if d.get("owner_id")}
    return next(iter(owners)) if len(owners) == 1 else None


def clusters_by_fingerprint(clusters):
    """Map a community fingerprint -> its published cluster (id/label/reps).

    A bridge's community_a/community_b carry the same sha1[:16] fingerprint the
    board stores in params.fingerprint — graph-compute derives both from the one
    Leiden partition — so a bridge endpoint can be resolved back to a real cluster
    for its label, representative snippets and primary_cluster_id.
    """
    by_fp = {}
    for c in clusters:
        if not isinstance(c, dict):
            continue
        fp = str(as_dict(c.get("params")).get("fingerprint") or "").strip()
        if fp and fp not in by_fp:
            by_fp[fp] = c
    return by_fp


def existing_bridge_fingerprints():
    """Fingerprints of bridges already turned into hypotheses (dedup set)."""
    seen = set()
    listed = gw(f"/hypotheses?tags=auto_bridge&limit={max(DISCOVERY_LIMIT * 8, 500)}") or []
    for hyp in listed:
        if not isinstance(hyp, dict):
            continue
        gen = as_dict(hyp.get("generation"))
        if gen.get("kind") != "auto_bridge":
            continue
        fp = str(gen.get("bridge_fingerprint") or "").strip()
        if fp:
            seen.add(fp)
    return seen


def theme_view(cluster, member_ids, docmap):
    """Label + representative snippets for one side of a bridge.

    Prefers the published cluster's representatives (rich prose snippets); when the
    community is not on the board, falls back to the member document filenames so
    the hypothesis still grounds on real document ids.
    """
    label = (cluster.get("label") or "").strip()
    reps = []
    for r in as_list(cluster.get("representatives"))[:4]:
        if not isinstance(r, dict):
            continue
        reps.append({
            "document_id": r.get("document_id") or "",
            "filename": r.get("filename") or "",
            "snippet": first_sentence(r.get("snippet", ""), 520),
        })
    if not reps:
        for doc_id in member_ids[:4]:
            reps.append({"document_id": doc_id, "filename": docmap.get(doc_id, ""), "snippet": ""})
    return label or "тематическая группа", reps


def build_evidence(reps_a, reps_b, mediators):
    """HypothesisEvidence from theme-A reps, theme-B reps and the mediators.

    relation marks each fragment's role (theme_a/theme_b/mediator); stance is
    "context" — bridge evidence frames the connection, it does not argue for it.
    """
    evidence = []
    seen = set()

    def add(items, relation):
        for it in items:
            doc_id = it.get("document_id") or ""
            snippet = first_sentence(it.get("snippet", ""), 520)
            if not doc_id and not snippet:
                continue
            key = (doc_id, relation)
            if doc_id and key in seen:
                continue
            if doc_id:
                seen.add(key)
            evidence.append({
                "document_id": doc_id or None,
                "filename": it.get("filename") or "",
                "snippet": snippet,
                "stance": "context",
                "relation": relation,
                "score": 0.6,
                "ord": len(evidence),
            })

    add(reps_a, "theme_a")
    add(reps_b, "theme_b")
    add(mediators, "mediator")
    return evidence


def build_prompt(label_a, reps_a, label_b, reps_b, mediators, scores):
    def block(reps):
        lines = []
        for r in reps:
            head = r.get("filename") or r.get("document_id") or "документ"
            snippet = r.get("snippet") or ""
            lines.append(f"- {head}: {snippet}".rstrip(": "))
        return "\n".join(lines) or "(нет сниппетов)"

    med_block = "\n".join(
        f"- {m.get('filename') or m.get('document_id') or 'документ'}: "
        f"{first_sentence(m.get('snippet', ''), 400)}".rstrip(": ")
        for m in mediators
    ) or "(нет посредников)"
    return (
        "Ты помогаешь учёным находить неочевидные связи между разными областями "
        "исследований. Ниже две тематические группы документов и документы-"
        "посредники, которые их соединяют.\n\n"
        f"ТЕМА A: {label_a}\n{block(reps_a)}\n\n"
        f"ТЕМА B: {label_b}\n{block(reps_b)}\n\n"
        f"ДОКУМЕНТЫ-ПОСРЕДНИКИ (связывают тему A и тему B):\n{med_block}\n\n"
        f"Метрики моста: affinity={scores.affinity:.3f}, link_density={scores.link_density:.3f}, "
        f"convergence={scores.convergence}, composite={scores.composite:.3f}.\n\n"
        "Сформулируй ОДНУ проверяемую (фальсифицируемую) научную гипотезу, которая "
        "связывает тему A и тему B через конкретный механизм, опирающийся на "
        "документы-посредники. Укажи, как именно и почему темы связаны, и какой "
        "результат подтвердил бы или опроверг гипотезу. Не пересказывай аннотации "
        "и не выдавай общие слова — предложи новую содержательную связь.\n"
        "Все поля пиши ТОЛЬКО на русском языке. Верни СТРОГО JSON:\n"
        '{"title":"краткий заголовок (до 12 слов)",'
        '"statement":"гипотеза одним-двумя предложениями",'
        '"rationale":"почему связь правдоподобна — механизм и роль посредников"}'
    )


def generate_hypothesis(label_a, reps_a, label_b, reps_b, mediators, scores):
    """LLM-generated falsifiable hypothesis as {title, statement, rationale}.

    Discovery needs an LLM: when VLLM_URL is empty there is no fallback (templated
    text would be hype, not a hypothesis) — the caller logs and skips the run.
    """
    if not VLLM_URL:
        return None
    prompt = build_prompt(label_a, reps_a, label_b, reps_b, mediators, scores)
    for attempt in range(3):
        try:
            throttle_vllm()
            resp = http(
                VLLM_URL.rstrip("/") + "/v1/chat/completions",
                {"model": VLLM_MODEL, "messages": [{"role": "user", "content": prompt}], "temperature": 0.3},
                token=VLLM_API_KEY,
            )
            if isinstance(resp, dict):
                record_llm_usage(VLLM_MODEL, "discovery_transfer", resp.get("usage") or {})
            c = resp["choices"][0]["message"]["content"]
            obj = json.loads(c[c.index("{"): c.rindex("}") + 1])
            title = str(obj.get("title", "")).strip()
            statement = str(obj.get("statement", "")).strip()
            rationale = str(obj.get("rationale", "")).strip()
            if title and statement:
                return {"title": title, "statement": statement, "rationale": rationale}
            print("  generation missing title/statement; retrying", file=sys.stderr)
        except Exception as e:  # noqa: BLE001 — generation is best-effort; retried then skipped
            print(f"  generation attempt {attempt + 1}/3 failed: {e}", file=sys.stderr)
            time.sleep(1.5 * (attempt + 1))
    return None


def embed(text):
    """Embed one statement via the f2llm endpoint; vector or None on a bad shape."""
    resp = http(EMBEDDINGS_URL, {"model": EMBEDDINGS_MODEL, "input": [text]}, timeout=60)
    data = resp.get("data")
    if isinstance(data, list) and data and isinstance(data[0], dict):
        vec = data[0].get("embedding")
        if isinstance(vec, list) and vec:
            return vec
    embeddings = resp.get("embeddings")  # Triton / text-embeddings-inference shape
    if isinstance(embeddings, list) and embeddings and isinstance(embeddings[0], list):
        return embeddings[0]
    return None


def novelty_probe(statement, owner_id=""):
    """SciMON novelty probe: the statement's nearest corpus "double".

    Embeds the statement and searches Qdrant over raw chunks only (the node_type
    filter keeps RAPTOR summaries out, the owner filter keeps other corpora out —
    both would pollute the estimate). Returns {"top_sim", "nearest_doc_id",
    "nearest_filename"} for the closest hit — top_sim 0.0 when the corpus has
    nothing close (fully novel) — or None when the probe could not run (embedding
    failed) so the gate can fail closed.
    """
    vector = embed(statement)
    if not vector:
        return None
    must = [{"key": "node_type", "match": {"value": "chunk"}}]
    if owner_id:
        must.append({"key": "owner_id", "match": {"value": owner_id}})
    url = f"{QDRANT_URL.rstrip('/')}/collections/{COLLECTION}/points/search"
    resp = http(
        url,
        {"vector": vector, "limit": 5, "with_payload": True, "filter": {"must": must}},
        timeout=30,
    )
    hits = [h for h in (resp.get("result") or []) if isinstance(h, dict)]
    if not hits:
        return {"top_sim": 0.0, "nearest_doc_id": None, "nearest_filename": None}
    top = max(hits, key=lambda h: float(h.get("score", 0.0)))
    payload = top.get("payload") or {}
    return {
        "top_sim": float(top.get("score", 0.0)),
        # Chunks carry document_id/filename; source_* kept defensively for summaries.
        "nearest_doc_id": payload.get("document_id") or payload.get("source_document_id") or None,
        "nearest_filename": payload.get("filename") or payload.get("title") or payload.get("source_filename") or None,
    }


def validate(bridge, statement, evidence, owner_id=""):
    """Mandatory anti-hype gate. Returns (accepted, reason, novelty_score, gates).

    Three independent, fail-closed checks:
      * convergence — the bridge must rest on >= BRIDGE_MIN_CONVERGENCE mediating
        documents; a single shared neighbour is coincidence, not a bridge,
      * grounding — the hypothesis must cite at least one real document id,
      * SciMON novelty — the statement, embedded and searched against the corpus,
        must not exceed NOVELTY_MAX_SIM (else it just paraphrases existing text). A
        probe that cannot run rejects, never waves the candidate through.
    The LLM's own novelty opinion is deliberately never consulted.
    novelty_score = clamp(1 - top_sim) — a corpus-grounded signal, not a guess.
    gates records every check (threshold, actual, nearest corpus double) so the
    published hypothesis carries its validation provenance.
    """
    convergence = int(bridge.scores.convergence)
    gates = {
        "convergence": {
            "required": BRIDGE_MIN_CONVERGENCE,
            "actual": convergence,
            "passed": convergence >= BRIDGE_MIN_CONVERGENCE,
        },
        "grounding": {"passed": any(e.get("document_id") for e in evidence)},
        "novelty": {
            "threshold": NOVELTY_MAX_SIM,
            "top_sim": None,
            "nearest_doc_id": None,
            "nearest_filename": None,
            "passed": False,
        },
    }
    if not gates["convergence"]["passed"]:
        return False, "convergence", 0.0, gates
    if not gates["grounding"]["passed"]:
        return False, "grounding", 0.0, gates
    try:
        probe = novelty_probe(statement, owner_id)
    except Exception as e:  # noqa: BLE001 — an unverifiable novelty probe fails closed
        print(f"  novelty probe failed: {e}", file=sys.stderr)
        probe = None
    if probe is None:
        return False, "novelty_unverified", 0.0, gates
    top_sim = probe["top_sim"]
    gates["novelty"].update({
        "top_sim": round(top_sim, 4),
        "nearest_doc_id": probe["nearest_doc_id"],
        "nearest_filename": probe["nearest_filename"],
        "passed": top_sim <= NOVELTY_MAX_SIM,
    })
    if not gates["novelty"]["passed"]:
        return False, "novelty", clamp01(1.0 - top_sim), gates
    return True, "", clamp01(1.0 - top_sim), gates


def bridge_payload(bridge, label_a, label_b, gen, novelty, primary_cluster_id, evidence, epoch, gates):
    """The /hypotheses POST payload for a validated bridge (cluster-worker shape)."""
    scores = bridge.scores
    mediator_ids = [m.document_id for m in bridge.mediators if m.document_id]
    composite = clamp01(scores.composite)
    generation = {
        "kind": "auto_bridge",
        "bridge_fingerprint": bridge.fingerprint,
        "community_a": bridge.community_a,
        "community_b": bridge.community_b,
        "endpoint_a": bridge.endpoint_a,
        "endpoint_b": bridge.endpoint_b,
        "theme_a": label_a,
        "theme_b": label_b,
        "scores": {
            "affinity": round(scores.affinity, 4),
            "link_density": round(scores.link_density, 4),
            "maverick": round(scores.maverick, 4),
            "vanguard": round(scores.vanguard, 4),
            "bridging_centrality": round(scores.bridging_centrality, 4),
            "convergence": scores.convergence,
            "composite": round(scores.composite, 4),
        },
        "mediators": mediator_ids,
        # Validation provenance: every gate's threshold/actual and the nearest
        # corpus double the novelty probe found (was computed and thrown away).
        "gates": gates,
        "novelty_probe": {
            "top_sim": gates["novelty"]["top_sim"],
            "nearest_doc_id": gates["novelty"]["nearest_doc_id"],
            "nearest_filename": gates["novelty"]["nearest_filename"],
        },
        "epoch": epoch,
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    payload = {
        "run_id": f"auto-bridge-{epoch or 'unknown'}",
        "title": gen["title"],
        "statement": gen["statement"],
        "rationale": gen["rationale"],
        "method": "combination",
        "status": "generated",
        # A cross-theme scientific hypothesis: no KPI-measurable effect by default.
        "measurable": False,
        "source_type": "literature",
        "function_area": label_a,
        "novelty_score": novelty,
        # Composite blends corpus-grounded novelty with the graph bridge score; no
        # invented risk/value/confidence — discovery does not fabricate scores.
        "composite_score": round((novelty + composite) / 2.0, 4),
        "tags": ["auto_bridge", "discovery"],
        "detail": {
            "theme_a": label_a,
            "theme_b": label_b,
            "convergence": scores.convergence,
            "affinity": round(scores.affinity, 4),
            "composite": round(scores.composite, 4),
            "mediator_count": len(mediator_ids),
        },
        "generation": generation,
        "evidence": evidence,
        "revision": {"action": "created", "editor_id": "discovery-worker", "summary": "гипотеза-мост создана автоматически"},
    }
    if primary_cluster_id:
        payload["primary_cluster_id"] = primary_cluster_id
    return payload


# ---------------------------------------------------------------------------
# Transfer discovery (default mode): carry a strong hypothesis from a KPI that
# has one onto a semantically close KPI that lacks coverage. No graph-compute.
# ---------------------------------------------------------------------------


def cosine(a, b):
    """Cosine similarity of two equal-length vectors; 0.0 on a bad/degenerate shape."""
    if not a or not b or len(a) != len(b):
        return 0.0
    dot = na = nb = 0.0
    for x, y in zip(a, b):
        dot += x * y
        na += x * x
        nb += y * y
    if na <= 0.0 or nb <= 0.0:
        return 0.0
    return dot / math.sqrt(na * nb)


def kpi_text(kpi):
    """Embedding text for a KPI: title + metric + description (blank parts dropped)."""
    parts = [kpi.get("title") or "", kpi.get("metric") or "", kpi.get("description") or ""]
    return " ".join(p.strip() for p in parts if p and p.strip())


def hyps_by_kpi(hyps):
    """Group hypotheses by their kpi_id (hypotheses without a KPI are dropped)."""
    by = {}
    for h in hyps:
        kid = h.get("kpi_id")
        if kid:
            by.setdefault(kid, []).append(h)
    return by


def normalize_statement(text):
    """Lowercased, whitespace-collapsed statement for exact-duplicate matching."""
    return " ".join((text or "").lower().split())


def transfer_fingerprint(source_hyp_id, target_kpi_id):
    """Stable dedup key for a transfer: the donor hypothesis + the recipient KPI."""
    raw = f"{source_hyp_id}|{target_kpi_id}"
    return hashlib.sha1(raw.encode()).hexdigest()[:16]


def existing_transfer_fingerprints():
    """Fingerprints of transfers already published (dedup set), mirroring bridges."""
    seen = set()
    listed = gw(f"/hypotheses?tags=auto_transfer&limit={max(DISCOVERY_LIMIT * 8, 500)}") or []
    for hyp in listed:
        if not isinstance(hyp, dict):
            continue
        gen = as_dict(hyp.get("generation"))
        if gen.get("kind") != "auto_transfer":
            continue
        fp = str(gen.get("transfer_fingerprint") or "").strip()
        if fp:
            seen.add(fp)
    return seen


def fetch_evidence(hyp_id, cache):
    """Evidence rows for one hypothesis (GET /hypotheses/{id}/evidence), memoised.

    The /hypotheses list does not hydrate evidence, so a donor's grounding
    documents are pulled per hypothesis (a donor can seed several recipients).
    """
    if hyp_id in cache:
        return cache[hyp_id]
    try:
        ev = gw(f"/hypotheses/{hyp_id}/evidence")
    except Exception as e:  # noqa: BLE001 — a donor whose evidence cannot load is skipped
        print(f"  evidence fetch failed for {hyp_id}: {e}", file=sys.stderr)
        ev = None
    items = ev if isinstance(ev, list) else []
    cache[hyp_id] = items
    return items


def transfer_evidence(raw_evidence, limit=6):
    """Carry the donor hypothesis's grounded documents as transfer_source context."""
    evidence = []
    seen = set()
    for ev in raw_evidence:
        if not isinstance(ev, dict):
            continue
        doc_id = ev.get("document_id") or None
        snippet = first_sentence(ev.get("snippet", ""), 520)
        if not doc_id and not snippet:
            continue
        if doc_id and doc_id in seen:
            continue
        if doc_id:
            seen.add(doc_id)
        raw_score = ev.get("score")
        try:
            score = clamp01(raw_score) if raw_score is not None else 0.6
        except (TypeError, ValueError):
            score = 0.6
        evidence.append({
            "document_id": doc_id,
            "filename": ev.get("filename") or "",
            "snippet": snippet,
            "stance": "context",
            "relation": "transfer_source",
            "score": score or 0.6,
            "ord": len(evidence),
        })
        if len(evidence) >= limit:
            break
    return evidence


def build_transfer_prompt(src_kpi, tgt_kpi, src_hyp):
    def kpi_line(k):
        bits = [f"«{(k.get('title') or '').strip() or 'без названия'}»"]
        metric = (k.get("metric") or "").strip()
        unit = (k.get("unit") or "").strip()
        if metric:
            bits.append("метрика: " + metric + (f" ({unit})" if unit else ""))
        direction = (k.get("direction") or "").strip()
        if direction:
            bits.append(f"направление: {direction}")
        desc = first_sentence(k.get("description", ""), 320)
        if desc:
            bits.append(f"описание: {desc}")
        return "; ".join(bits)

    statement = (src_hyp.get("statement") or "").strip()
    rationale = first_sentence(src_hyp.get("rationale", ""), 360)
    return (
        "Ты помогаешь учёным переносить удачные исследовательские приёмы между "
        "близкими, но разными целями (KPI). У цели-донора уже есть сильная "
        "гипотеза; у цели-реципиента таких гипотез мало.\n\n"
        f"ЦЕЛЬ-ДОНОР: {kpi_line(src_kpi)}\n"
        f"ГИПОТЕЗА-ДОНОР (проверенный приём/механизм): {statement}\n"
        + (f"Обоснование донора: {rationale}\n" if rationale else "")
        + f"\nЦЕЛЬ-РЕЦИПИЕНТ: {kpi_line(tgt_kpi)}\n\n"
        "Сформулируй ОДНУ новую проверяемую (фальсифицируемую) гипотезу для "
        "цели-реципиента, которая ПЕРЕНОСИТ приём/механизм из гипотезы-донора на "
        "цель-реципиента С УЧЁТОМ различий между целями. Не переписывай донора "
        "дословно — адаптируй под метрику и специфику реципиента и укажи, какой "
        "результат подтвердил бы или опроверг перенос.\n"
        "Все поля пиши ТОЛЬКО на русском языке. Верни СТРОГО JSON:\n"
        '{"statement":"гипотеза для реципиента одним-двумя предложениями",'
        '"rationale":"почему приём должен сработать для реципиента — механизм",'
        '"transferability":"почему приём переносим с донора на реципиента",'
        '"risk":"чем цель-реципиент отличается и в чём риск переноса"}'
    )


def generate_transfer(src_kpi, tgt_kpi, src_hyp):
    """LLM transfer hypothesis as {statement, rationale, transferability, risk}."""
    if not VLLM_URL:
        return None
    prompt = build_transfer_prompt(src_kpi, tgt_kpi, src_hyp)
    for attempt in range(3):
        try:
            throttle_vllm()
            resp = http(
                VLLM_URL.rstrip("/") + "/v1/chat/completions",
                {"model": VLLM_MODEL, "messages": [{"role": "user", "content": prompt}], "temperature": 0.3},
                token=VLLM_API_KEY,
            )
            if isinstance(resp, dict):
                record_llm_usage(VLLM_MODEL, "discovery_transfer", resp.get("usage") or {})
            c = resp["choices"][0]["message"]["content"]
            obj = json.loads(c[c.index("{"): c.rindex("}") + 1])
            statement = str(obj.get("statement", "")).strip()
            if statement:
                return {
                    "statement": statement,
                    "rationale": str(obj.get("rationale", "")).strip(),
                    "transferability": str(obj.get("transferability", "")).strip(),
                    "risk": str(obj.get("risk", "")).strip(),
                }
            print("  transfer generation missing statement; retrying", file=sys.stderr)
        except Exception as e:  # noqa: BLE001 — generation is best-effort; retried then skipped
            print(f"  transfer generation attempt {attempt + 1}/3 failed: {e}", file=sys.stderr)
            time.sleep(1.5 * (attempt + 1))
    return None


def transfer_title(tgt_kpi, statement):
    """A short board title for a transfer, anchored on the recipient KPI."""
    tgt = (tgt_kpi.get("title") or "").strip()
    if tgt:
        return f"Перенос приёма на цель «{tgt}»"[:120]
    return (first_sentence(statement, 90) or "Перенос гипотезы между целями")[:120]


def validate_transfer(statement, evidence, target_statements, owner_id=""):
    """Anti-hype gate for a transfer. Returns (accepted, reason, novelty, gates).

    Three fail-closed checks mirroring the bridge gate, minus graph convergence:
      * grounding — the carried evidence must cite at least one real document id,
      * duplicate — the statement must not repeat an existing recipient hypothesis
        (exact match on the normalized statement),
      * SciMON novelty — embedded and searched against the corpus, the statement
        must stay at/under NOVELTY_MAX_SIM; a probe that cannot run rejects.
    novelty_score = clamp(1 - top_sim), a corpus-grounded signal, not a guess.
    """
    gates = {
        "grounding": {"passed": any(e.get("document_id") for e in evidence)},
        "duplicate": {"passed": True, "match": None},
        "novelty": {
            "threshold": NOVELTY_MAX_SIM,
            "top_sim": None,
            "nearest_doc_id": None,
            "nearest_filename": None,
            "passed": False,
        },
    }
    if not gates["grounding"]["passed"]:
        return False, "grounding", 0.0, gates
    norm = normalize_statement(statement)
    if norm and norm in target_statements:
        gates["duplicate"].update({"passed": False, "match": target_statements.get(norm)})
        return False, "duplicate", 0.0, gates
    try:
        probe = novelty_probe(statement, owner_id)
    except Exception as e:  # noqa: BLE001 — an unverifiable novelty probe fails closed
        print(f"  novelty probe failed: {e}", file=sys.stderr)
        probe = None
    if probe is None:
        return False, "novelty_unverified", 0.0, gates
    top_sim = probe["top_sim"]
    gates["novelty"].update({
        "top_sim": round(top_sim, 4),
        "nearest_doc_id": probe["nearest_doc_id"],
        "nearest_filename": probe["nearest_filename"],
        "passed": top_sim <= NOVELTY_MAX_SIM,
    })
    if not gates["novelty"]["passed"]:
        return False, "novelty", clamp01(1.0 - top_sim), gates
    return True, "", clamp01(1.0 - top_sim), gates


def transfer_payload(src_kpi, tgt_kpi, src_hyp, gen, novelty, evidence, scores, epoch, gates, fingerprint):
    """The /hypotheses POST payload for a validated cross-KPI transfer.

    composite_score is intentionally omitted — main-service ranks it (rank-v1).
    """
    generation = {
        "kind": "auto_transfer",
        "transfer_fingerprint": fingerprint,
        "source_kpi_id": src_kpi.get("id"),
        "target_kpi_id": tgt_kpi.get("id"),
        "source_hypothesis_id": src_hyp.get("id"),
        "source_kpi": src_kpi.get("title") or "",
        "target_kpi": tgt_kpi.get("title") or "",
        "kpi_similarity": scores["kpi_similarity"],
        "source_strength": scores["source_strength"],
        "target_coverage": scores["target_coverage"],
        "transfer_score": scores["transfer_score"],
        "transferability": gen.get("transferability") or "",
        "risk": gen.get("risk") or "",
        # Validation provenance: every gate's threshold/actual and the nearest
        # corpus double the novelty probe found (computed and otherwise dropped).
        "gates": gates,
        "novelty_probe": {
            "top_sim": gates["novelty"]["top_sim"],
            "nearest_doc_id": gates["novelty"]["nearest_doc_id"],
            "nearest_filename": gates["novelty"]["nearest_filename"],
        },
        "epoch": epoch,
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    return {
        "run_id": f"auto-transfer-{epoch or 'unknown'}",
        "title": gen["title"],
        "statement": gen["statement"],
        "rationale": gen.get("rationale") or "",
        "method": "transfer",
        "status": "generated",
        # A cross-KPI transfer is a research hypothesis, not a measured KPI effect.
        "measurable": False,
        "source_type": "literature",
        "function_area": tgt_kpi.get("title") or "",
        "kpi_id": tgt_kpi.get("id"),
        "novelty_score": novelty,
        "tags": ["auto_transfer", "discovery"],
        "detail": {
            "kpi_similarity": scores["kpi_similarity"],
            "transfer_score": scores["transfer_score"],
            "source_kpi": src_kpi.get("title") or "",
            "target_kpi": tgt_kpi.get("title") or "",
            "transferability": gen.get("transferability") or "",
            "risk": gen.get("risk") or "",
        },
        "generation": generation,
        "evidence": evidence,
        "revision": {"action": "created", "editor_id": "discovery-worker", "summary": "гипотеза-перенос между близкими целями создана автоматически"},
    }


def score_bridges(owner_id, document_refs):
    """graph-compute ScoreBridges over an insecure gRPC channel."""
    request = graph_pb2.BridgeRequest(
        owner_id=owner_id,
        collection=COLLECTION,
        config=graph_pb2.BridgeConfig(
            cluster=graph_pb2.ClusterConfig(
                knn_k=KNN_K,
                knn_block_size=KNN_BLOCK_SIZE,
                resolution=RESOLUTION,
                min_size=MIN_SIZE,
                sim_threshold=SIM_THRESHOLD,
                mutual_knn=MUTUAL_KNN,
                chunk_weighting=CHUNK_WEIGHTING,
                chunk_lead_count=CHUNK_LEAD_COUNT,
                chunk_w_lead=CHUNK_W_LEAD,
                chunk_w_results=CHUNK_W_RESULTS,
                chunk_w_refs=CHUNK_W_REFS,
                lineage_overlap_min=LINEAGE_OVERLAP_MIN,
                algorithm=CLUSTER_ALGORITHM,
                seed=CLUSTER_SEED,
            ),
            top_n=BRIDGE_TOP_N,
            min_affinity=BRIDGE_MIN_AFFINITY,
            max_mediators=BRIDGE_MAX_MEDIATORS,
            min_convergence=BRIDGE_MIN_CONVERGENCE,
            w_maverick=BRIDGE_W_MAVERICK,
            w_bridging=BRIDGE_W_BRIDGING,
            w_vanguard=BRIDGE_W_VANGUARD,
        ),
        documents=document_refs,
    )
    try:
        with grpc.insecure_channel(GRAPH_COMPUTE_ADDR) as channel:
            return graph_pb2_grpc.GraphComputeStub(channel).ScoreBridges(
                request, timeout=GRAPH_COMPUTE_TIMEOUT
            )
    except grpc.RpcError as e:  # surfaced to main(), which retries on the next tick
        print(f"graph-compute ScoreBridges failed: {e.code()} {e.details()}", file=sys.stderr, flush=True)
        raise


def discover_bridge(epoch=None):
    login()
    if not VLLM_URL:
        print("VLLM_URL is empty — hypothesis generation disabled; discovery needs an LLM, skipping", flush=True)
        return 0

    docs = gw("/documents")
    # /documents is owner-scoped, so discover only within this owner's corpus;
    # forward the owner + collection so graph-compute filters Qdrant server-side.
    owner_id = owner_of(docs) or ""
    document_refs = [
        graph_pb2.DocumentRef(id=d["id"], filename=d.get("filename", ""))
        for d in docs if isinstance(d, dict) and d.get("id")
    ]
    if not document_refs:
        print("no documents; skip", flush=True)
        return 0
    docmap = {d["id"]: d.get("filename", "") for d in docs if isinstance(d, dict) and d.get("id")}

    print(
        f"scoring bridges over {len(document_refs)} docs via graph-compute "
        f"({CLUSTER_ALGORITHM}) at {GRAPH_COMPUTE_ADDR}",
        flush=True,
    )
    resp = score_bridges(owner_id, document_refs)
    if not resp.bridges:
        print(f"no bridges from graph-compute (docs={resp.stats.document_count}); nothing to discover", flush=True)
        return 0

    already = existing_bridge_fingerprints()
    # The board lets a bridge resolve to real clusters (label/reps + primary id);
    # best-effort — an unloadable board just drops snippet enrichment for this run.
    try:
        by_fp = clusters_by_fingerprint(gw("/clusters") or [])
    except Exception as e:  # noqa: BLE001 — cluster lookup is enrichment, never block discovery
        print(f"  cluster board load skipped: {e}", file=sys.stderr, flush=True)
        by_fp = {}

    created = rejected_novelty = rejected_other = skipped_gen = skipped_dup = 0
    limit = DISCOVERY_LIMIT if DISCOVERY_LIMIT > 0 else len(resp.bridges)
    candidates = list(resp.bridges)[:limit]
    for done, bridge in enumerate(candidates):
        report_status("running", epoch=epoch, progress_done=done, progress_total=len(candidates))
        if bridge.fingerprint in already:
            skipped_dup += 1
            continue
        cluster_a = by_fp.get(bridge.community_a, {})
        cluster_b = by_fp.get(bridge.community_b, {})
        label_a, reps_a = theme_view(cluster_a, list(bridge.members_a), docmap)
        label_b, reps_b = theme_view(cluster_b, list(bridge.members_b), docmap)
        mediators = [
            {"document_id": m.document_id, "filename": m.filename, "snippet": m.snippet}
            for m in bridge.mediators
        ]
        evidence = build_evidence(reps_a, reps_b, mediators)

        gen = generate_hypothesis(label_a, reps_a, label_b, reps_b, mediators, bridge.scores)
        if not gen:
            skipped_gen += 1
            continue

        ok, reason, novelty, gates = validate(bridge, gen["statement"], evidence, owner_id)
        if not ok:
            if reason.startswith("novelty"):
                rejected_novelty += 1
            else:
                rejected_other += 1
            print(f"  rejected [{label_a} × {label_b}] reason={reason}", flush=True)
            continue

        payload = bridge_payload(bridge, label_a, label_b, gen, novelty, cluster_a.get("id"), evidence, epoch, gates)
        gw("/hypotheses", payload)
        already.add(bridge.fingerprint)
        created += 1
        print(
            f"  + [{gen['title']}] {label_a} × {label_b} "
            f"novelty={novelty:.2f} conv={bridge.scores.convergence}",
            flush=True,
        )

    print(
        f"discovery done: bridges={len(resp.bridges)} created={created} "
        f"rejected_novelty={rejected_novelty} rejected_other={rejected_other} "
        f"skipped_gen={skipped_gen} skipped_dup={skipped_dup}",
        flush=True,
    )
    return created


def discover_transfer(epoch=None):
    """Transfer discovery: carry strong hypotheses across close, under-covered KPIs.

    Embeds every KPI, pairs the semantically close ones, and where a donor KPI has
    strong hypotheses and its close neighbour lacks coverage, asks the LLM to adapt
    a top donor hypothesis to the recipient. Each candidate is grounded on the
    donor's evidence documents, de-duplicated against the recipient and novelty-
    probed against the corpus before it is published. graph-compute is not used.
    """
    login()
    if not VLLM_URL:
        print("VLLM_URL is empty — transfer generation disabled; discovery needs an LLM, skipping", flush=True)
        return 0

    kpis = [k for k in (gw("/kpis") or []) if isinstance(k, dict) and k.get("id")]
    if len(kpis) < 2:
        print(f"transfer discovery: fewer than 2 KPIs ({len(kpis)}); skip", flush=True)
        return 0
    hyps = [h for h in (gw("/hypotheses?limit=500") or []) if isinstance(h, dict) and h.get("id")]
    if not hyps:
        print("transfer discovery: no hypotheses to transfer; skip", flush=True)
        return 0

    # Single-owner (admin-scoped) corpus: reuse the owner for the novelty probe's
    # Qdrant filter so other corpora never pollute the estimate.
    owner_id = owner_of(kpis) or owner_of(hyps) or ""
    kpi_by_id = {k["id"]: k for k in kpis}
    by_kpi = hyps_by_kpi(hyps)
    nonfailed_count = {
        kid: sum(1 for h in hs if (h.get("status") or "") not in FAILED_STATUS)
        for kid, hs in by_kpi.items()
    }
    nonfailed_hyps = {
        kid: [h for h in hs if (h.get("status") or "") not in FAILED_STATUS]
        for kid, hs in by_kpi.items()
    }

    # Embed each KPI (title + metric + description) via f2llm; reuse the vectors.
    vecs = {}
    for k in kpis:
        text = kpi_text(k)
        if not text:
            continue
        try:
            vec = embed(text)
        except Exception as e:  # noqa: BLE001 — a KPI that fails to embed is skipped
            print(f"  KPI embed failed [{k.get('title')}]: {e}", file=sys.stderr)
            vec = None
        if vec:
            vecs[k["id"]] = vec
    ids = [k["id"] for k in kpis if k["id"] in vecs]
    if len(ids) < 2:
        print(f"transfer discovery: fewer than 2 KPIs embedded ({len(ids)}); skip", flush=True)
        return 0

    # Close KPI pairs with a real coverage gap: donor = more non-failed hypotheses,
    # recipient = fewer and not already well covered. Strongest similarity first.
    pairs = []
    for i in range(len(ids)):
        for j in range(i + 1, len(ids)):
            ia, ib = ids[i], ids[j]
            sim = cosine(vecs[ia], vecs[ib])
            if sim < TRANSFER_MIN_SIM:
                continue
            ca, cb = nonfailed_count.get(ia, 0), nonfailed_count.get(ib, 0)
            if ca == cb:
                continue
            donor, recip, dcount, rcount = (ia, ib, ca, cb) if ca > cb else (ib, ia, cb, ca)
            if dcount <= 0:
                continue  # donor has no hypotheses to transfer
            if rcount >= TRANSFER_COVERAGE_FULL:
                continue  # recipient already well covered — no gap to fill
            if (dcount - rcount) < TRANSFER_MIN_GAP:
                continue  # both dense enough; no meaningful coverage gap
            pairs.append((sim, donor, recip, rcount))
    pairs.sort(key=lambda p: p[0], reverse=True)
    if TRANSFER_MAX_PAIRS > 0:
        pairs = pairs[:TRANSFER_MAX_PAIRS]
    if not pairs:
        print("transfer discovery: no close KPI pairs with a coverage gap; nothing to do", flush=True)
        return 0

    already = existing_transfer_fingerprints()
    evidence_cache = {}
    created = rejected_novelty = rejected_dup = rejected_other = 0
    skipped_gen = skipped_dup = skipped_ungrounded = 0
    for done, (sim, donor, recip, rcount) in enumerate(pairs):
        report_status("running", epoch=epoch, progress_done=done, progress_total=len(pairs))
        src_kpi, tgt_kpi = kpi_by_id[donor], kpi_by_id[recip]
        target_statements = {}
        for h in by_kpi.get(recip, []):
            norm = normalize_statement(h.get("statement"))
            if norm:
                target_statements.setdefault(norm, h.get("id"))
        target_coverage = min(1.0, rcount / float(TRANSFER_COVERAGE_FULL))
        donor_top = sorted(
            nonfailed_hyps.get(donor, []),
            key=lambda h: float(h.get("composite_score") or 0.0),
            reverse=True,
        )[:TRANSFER_PER_PAIR]
        for src_hyp in donor_top:
            src_id = src_hyp.get("id")
            if not src_id:
                continue
            fp = transfer_fingerprint(src_id, recip)
            if fp in already:
                skipped_dup += 1
                continue
            evidence = transfer_evidence(fetch_evidence(src_id, evidence_cache))
            if not any(e.get("document_id") for e in evidence):
                skipped_ungrounded += 1  # donor has no grounded evidence to carry over
                continue
            gen = generate_transfer(src_kpi, tgt_kpi, src_hyp)
            if not gen:
                skipped_gen += 1
                continue
            gen["title"] = transfer_title(tgt_kpi, gen["statement"])
            ok, reason, novelty, gates = validate_transfer(gen["statement"], evidence, target_statements, owner_id)
            if not ok:
                if reason.startswith("novelty"):
                    rejected_novelty += 1
                elif reason == "duplicate":
                    rejected_dup += 1
                else:
                    rejected_other += 1
                print(f"  rejected transfer [{src_kpi.get('title')} → {tgt_kpi.get('title')}] reason={reason}", flush=True)
                continue
            source_strength = clamp01(src_hyp.get("composite_score") or 0.0)
            transfer_score = clamp01(sim) * source_strength * (1.0 - target_coverage)
            scores = {
                "kpi_similarity": round(clamp01(sim), 4),
                "source_strength": round(source_strength, 4),
                "target_coverage": round(clamp01(target_coverage), 4),
                "transfer_score": round(clamp01(transfer_score), 4),
            }
            payload = transfer_payload(src_kpi, tgt_kpi, src_hyp, gen, novelty, evidence, scores, epoch, gates, fp)
            gw("/hypotheses", payload)
            already.add(fp)
            created += 1
            print(
                f"  + transfer [{src_kpi.get('title')} → {tgt_kpi.get('title')}] "
                f"sim={sim:.2f} strength={source_strength:.2f} cov={target_coverage:.2f} "
                f"transfer={transfer_score:.3f} novelty={novelty:.2f}",
                flush=True,
            )

    print(
        f"transfer discovery done: pairs={len(pairs)} created={created} "
        f"rejected_novelty={rejected_novelty} rejected_dup={rejected_dup} rejected_other={rejected_other} "
        f"skipped_gen={skipped_gen} skipped_dup={skipped_dup} skipped_ungrounded={skipped_ungrounded}",
        flush=True,
    )
    return created


def discover(epoch=None):
    """Dispatch to the configured discovery mode (transfer by default)."""
    if DISCOVERY_MODE == "bridge":
        return discover_bridge(epoch)
    return discover_transfer(epoch)


def main():
    import redis  # deferred so `--selfcheck` runs without the runtime redis dependency

    r = redis.from_url(VALKEY_URL)
    _STATUS["r"] = r
    print(
        f"discovery-worker up; mode={DISCOVERY_MODE}; watching {EPOCH_KEY} "
        f"(debounce {DEBOUNCE_SEC}s, every {CHECK_INTERVAL}s)",
        flush=True,
    )
    if not AUTO_DISCOVERY:
        print("AUTO_DISCOVERY disabled; idling", flush=True)
    try:
        last = redis_int(r, LAST_KEY)
    except Exception as e:  # noqa: BLE001 — bad state should not stop the worker
        print(f"last discovery epoch read failed: {e}", file=sys.stderr, flush=True)
        last = None
    seen_epoch = last
    seen_at = time.time() if last is not None else 0.0
    if last is not None:
        print(f"last discovery epoch={last}; waiting for a newer corpus epoch", flush=True)
    while True:
        try:
            if not AUTO_DISCOVERY:
                time.sleep(CHECK_INTERVAL)
                continue
            raw = r.get(EPOCH_KEY)
            epoch = int(raw) if raw else 0
            now = time.time()
            if epoch != seen_epoch:
                seen_epoch, seen_at = epoch, now  # epoch moved -> restart debounce
                print(f"epoch moved to {epoch}; waiting {DEBOUNCE_SEC}s before discovery", flush=True)
            elif epoch != last and epoch > 0 and (now - seen_at) >= DEBOUNCE_SEC:
                try:
                    report_status("running", epoch=epoch)
                    discover(epoch)
                    last = epoch
                    try:
                        r.set(LAST_KEY, epoch)
                    except Exception as e:  # noqa: BLE001 — avoid re-running in-memory loop anyway
                        print(f"last discovery epoch write failed: {e}", file=sys.stderr, flush=True)
                    report_status("idle", epoch=epoch)
                except Exception as e:  # noqa: BLE001 — keep the loop alive
                    print(f"discovery failed: {e}", file=sys.stderr, flush=True)
                    report_status("error", epoch=epoch, last_error=str(e))
        except Exception as e:  # noqa: BLE001 — Valkey/transient
            print(f"loop error: {e}", file=sys.stderr, flush=True)
        time.sleep(CHECK_INTERVAL)


def _selfcheck():
    """Offline sanity for the pure helpers (run via `python worker.py --selfcheck`)."""
    # The proto contract this worker is built on must resolve and construct.
    assert graph_pb2_grpc.GraphComputeStub is not None
    req = graph_pb2.BridgeRequest(owner_id="o", collection="documents")
    assert req.collection == "documents"

    # JSONB coercion accepts dicts, raw-JSON strings and junk.
    assert as_dict('{"a":1}') == {"a": 1} and as_dict("nope") == {} and as_dict({"b": 2}) == {"b": 2}
    assert as_list("[1,2]") == [1, 2] and as_list("nope") == [] and as_list([3]) == [3]
    assert clamp01(1.5) == 1.0 and clamp01(-1) == 0.0
    assert first_sentence("This opening sentence is plainly long. Tail.") == "This opening sentence is plainly long."

    # A board cluster resolves by fingerprint; theme_view prefers its reps and
    # falls back to member ids (still grounded) when off the board.
    board = [{"id": "c1", "label": "Тема", "representatives": '[{"document_id":"d1","filename":"a.pdf","snippet":"Релевантный фрагмент текста."}]',
              "params": '{"fingerprint":"fp_a"}'}]
    by_fp = clusters_by_fingerprint(board)
    assert by_fp["fp_a"]["id"] == "c1"
    label_a, reps_a = theme_view(by_fp["fp_a"], ["d1"], {"d1": "a.pdf"})
    assert label_a == "Тема" and reps_a[0]["document_id"] == "d1" and reps_a[0]["snippet"]
    label_b, reps_b = theme_view({}, ["d2", "d3"], {"d2": "b.pdf"})
    assert label_b == "тематическая группа" and reps_b[0]["document_id"] == "d2"

    # Evidence carries each fragment's role and grounds on real document ids.
    mediators = [{"document_id": "m1", "filename": "m.pdf", "snippet": "Посредник связывает темы."}]
    evidence = build_evidence(reps_a, reps_b, mediators)
    relations = {e["relation"] for e in evidence}
    assert relations == {"theme_a", "theme_b", "mediator"}
    assert any(e["document_id"] == "m1" for e in evidence)

    # A fabricated bridge exercises the convergence gate (offline) and the payload.
    bridge = graph_pb2.Bridge(fingerprint="fp", community_a="fp_a", community_b="fp_b",
                              endpoint_a="d1", endpoint_b="d2")
    bridge.members_a.extend(["d1"])
    bridge.members_b.extend(["d2"])
    bridge.mediators.add(document_id="m1", filename="m.pdf", snippet="связь")
    bridge.scores.convergence = 1  # below the default floor of 2 -> rejected, no network
    bridge.scores.composite = 0.5
    ok, reason, _, gates = validate(bridge, "stmt", evidence)
    assert not ok and reason == "convergence"
    assert gates["convergence"] == {"required": BRIDGE_MIN_CONVERGENCE, "actual": 1, "passed": False}
    # Grounding gate: convergence high enough but no grounded evidence -> rejected.
    bridge.scores.convergence = 5
    ok, reason, _, gates = validate(bridge, "stmt", [{"document_id": None}])
    assert not ok and reason == "grounding"
    assert gates["convergence"]["passed"] and not gates["grounding"]["passed"]
    assert gates["novelty"]["top_sim"] is None and not gates["novelty"]["passed"]

    gen = {"title": "T", "statement": "S", "rationale": "R"}
    gates["grounding"]["passed"] = True
    gates["novelty"].update({"top_sim": 0.31, "nearest_doc_id": "d1", "nearest_filename": "a.pdf", "passed": True})
    payload = bridge_payload(bridge, label_a, label_b, gen, 0.7, "c1", evidence, 42, gates)
    assert payload["method"] == "combination" and payload["status"] == "generated"
    assert payload["generation"]["bridge_fingerprint"] == "fp"
    assert payload["generation"]["gates"]["novelty"]["passed"]
    assert payload["generation"]["novelty_probe"] == {"top_sim": 0.31, "nearest_doc_id": "d1", "nearest_filename": "a.pdf"}
    assert "auto_bridge" in payload["tags"] and payload["primary_cluster_id"] == "c1"
    assert payload["evidence"] and payload["novelty_score"] == 0.7

    # --- transfer mode (default) ---
    # Pure math + text helpers.
    assert abs(cosine([1.0, 0.0], [1.0, 0.0]) - 1.0) < 1e-9
    assert cosine([1.0, 0.0], [0.0, 1.0]) == 0.0
    assert cosine([1.0, 0.0, 0.0], [1.0, 0.0]) == 0.0  # length mismatch -> 0
    assert kpi_text({"title": "Прочность", "metric": "МПа", "description": "предел"}) == "Прочность МПа предел"
    assert normalize_statement("  A  b\nC ") == "a b c"
    grouped = hyps_by_kpi([{"id": "h1", "kpi_id": "k1"}, {"id": "h2", "kpi_id": "k1"}, {"id": "h3"}])
    assert len(grouped["k1"]) == 2 and None not in grouped and "" not in grouped
    # Fingerprint is stable and keyed on (donor hypothesis, recipient KPI).
    assert transfer_fingerprint("hypA", "kpiB") == transfer_fingerprint("hypA", "kpiB")
    assert transfer_fingerprint("hypA", "kpiB") != transfer_fingerprint("hypA", "kpiC")

    # Evidence carries the donor's grounded documents as transfer_source/context.
    tev = transfer_evidence([
        {"document_id": "d9", "filename": "src.pdf", "snippet": "Донорский приём описан здесь.", "score": 0.7},
        {"document_id": None, "snippet": ""},  # dropped: neither doc nor snippet
    ])
    assert len(tev) == 1
    assert tev[0]["document_id"] == "d9" and tev[0]["relation"] == "transfer_source" and tev[0]["stance"] == "context"

    # Validation gates (offline): grounding and duplicate short-circuit before the
    # network novelty probe, so they can be exercised without a backend.
    ok, reason, _, tgates = validate_transfer("S", [{"document_id": None, "snippet": "x"}], {}, "")
    assert not ok and reason == "grounding" and not tgates["grounding"]["passed"]
    dup_targets = {normalize_statement("Перенос гипотезы"): "h-existing"}
    ok, reason, _, tgates = validate_transfer("  перенос   ГИПОТЕЗЫ ", tev, dup_targets, "")
    assert not ok and reason == "duplicate" and tgates["duplicate"]["match"] == "h-existing"

    # Title derives from the recipient KPI; payload has the transfer contract.
    src_kpi = {"id": "kpiA", "title": "Цель A", "metric": "m1", "description": "da"}
    tgt_kpi = {"id": "kpiB", "title": "Цель B", "metric": "m2", "description": "db"}
    src_hyp = {"id": "hypA", "composite_score": 0.6}
    tgen = {
        "title": transfer_title(tgt_kpi, "S"), "statement": "S", "rationale": "R",
        "transferability": "переносимо", "risk": "различия",
    }
    assert tgen["title"] == "Перенос приёма на цель «Цель B»"
    tscores = {"kpi_similarity": 0.72, "source_strength": 0.6, "target_coverage": 0.2, "transfer_score": 0.3456}
    tgates = {
        "grounding": {"passed": True}, "duplicate": {"passed": True, "match": None},
        "novelty": {"threshold": NOVELTY_MAX_SIM, "top_sim": 0.28, "nearest_doc_id": "d9",
                    "nearest_filename": "src.pdf", "passed": True},
    }
    fp = transfer_fingerprint("hypA", "kpiB")
    tpayload = transfer_payload(src_kpi, tgt_kpi, src_hyp, tgen, 0.72, tev, tscores, 42, tgates, fp)
    assert tpayload["method"] == "transfer" and tpayload["status"] == "generated"
    assert tpayload["measurable"] is False and tpayload["kpi_id"] == "kpiB"
    assert "composite_score" not in tpayload  # main-service ranks it (rank-v1)
    assert tpayload["novelty_score"] == 0.72 and tpayload["evidence"][0]["relation"] == "transfer_source"
    assert "auto_transfer" in tpayload["tags"] and "discovery" in tpayload["tags"]
    tg = tpayload["generation"]
    assert tg["kind"] == "auto_transfer" and tg["transfer_fingerprint"] == fp
    assert tg["source_kpi_id"] == "kpiA" and tg["target_kpi_id"] == "kpiB" and tg["source_hypothesis_id"] == "hypA"
    assert tg["kpi_similarity"] == 0.72 and tg["source_strength"] == 0.6
    assert tg["target_coverage"] == 0.2 and tg["transfer_score"] == 0.3456 and tg["transferability"] == "переносимо"
    assert tpayload["detail"]["kpi_similarity"] == 0.72 and tpayload["detail"]["transfer_score"] == 0.3456
    assert tpayload["detail"]["source_kpi"] == "Цель A" and tpayload["detail"]["target_kpi"] == "Цель B"

    print("selfcheck ok", flush=True)


if __name__ == "__main__":
    if "--selfcheck" in sys.argv:
        _selfcheck()
    else:
        main()
