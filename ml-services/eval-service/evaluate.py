#!/usr/bin/env python3
"""RAG evaluation harness — standard retrieval + generation metrics.

Runs a labeled question set (dataset.jsonl) through the LIVE RAG API and reports
the metrics used across the industry and in RAG benchmarks:

  Retrieval (classic IR / BEIR-style):
    Hit@k        — relevant document present in top-k sources (a.k.a. Recall@k here)
    MRR          — mean reciprocal rank of the first relevant source
    nDCG@k       — rank-weighted retrieval quality

  Generation (RAGAS + TruLens "RAG Triad"):
    faithfulness       — answer grounded ONLY in retrieved context (no hallucination)
    answer_relevance   — answer addresses the question
    context_relevance  — retrieved context is relevant to the question
    answer_correctness — answer matches the ground-truth (LLM-judge)
    answer_similarity  — F2LLM-v2 cosine(answer, ground_truth)  [RAGAS semantic similarity]

  Operational:
    latency p50 / p95 / mean (seconds)

Judge = the configured generation model from VLLM_URL. Embeddings for semantic
similarity = the local F2LLM-v2-0.6B server. Both read from environment variables.
Network calls retry on transient failures. A metric that still cannot be scored
is recorded as null rather than aborting.

Run (descriptive):  python3 eval-service/evaluate.py
Run (regression gate): python3 eval-service/evaluate.py --gate    # or EVAL_GATE=1

Gate exit codes:
    0  PASS                — every running metric meets its floor.
    1  quality FAIL        — a metric mean is below its floor, or a judge metric
                             was expected (EVAL_SKIP_JUDGE unset) yet scored
                             nothing (null/n==0 is NOT a passing state).
    2  harness/setup error — the live RAG API / embedder is unreachable, dataset
                             missing, etc. (distinct from a quality FAIL).
Thresholds are env-overridable (EVAL_MIN_<METRIC>, e.g. EVAL_MIN_ANSWER_SIMILARITY).
A plain run always prints an advisory PASS/FAIL breakdown but exits 0.

──────────────────────────────────────────────────────────────────────────────
Hypothesis-quality eval (ADDITIVE, opt-in — P0.7 "Фабрика гипотез")
──────────────────────────────────────────────────────────────────────────────
Set EVAL_HYPOTHESES=1 to ALSO score the Hypothesis Factory against a small golden
set (dataset-hypotheses.jsonl). This is off by default, never gated, and leaves
the retrieval/generation scorecard above untouched. For each KPI it creates/finds
the KPI, calls POST /hypotheses/generate, and reports:

  schema_completeness  — fraction of generated hypotheses whose detail/assessment
                         names an intervention+material, a baseline, and a
                         validation_plan/experiment (i.e. it is falsifiable).
  counterevidence_rate — fraction with >=1 evidence item of stance `contradicts`
                         or `context` (not every item `supports`).
  separation           — best-effort: does the board's composite/ranking tend to
                         rank the falsifiable `expected_good` shapes above the
                         vague `expected_bad` ones? Compared by F2LLM-v2 text
                         similarity when the embedder is up; otherwise we just
                         report the generated composite scores so the run is still
                         informative.

dataset-hypotheses.jsonl — one JSON object per line, one KPI per line:
  {"kpi": {"title","metric","unit","direction","baseline","material_family","description"},
   "expected_good": [{"id","description","intervention","material","conditions","mechanism","target_delta"}, ...],
   "expected_bad":  [{"id","description","why_bad"}, ...]}

Results land under results.json -> "hypotheses" (the existing summary/cases keys
are unchanged). NOTE: needs the full Hypothesis Factory stack (generation + stance
classification + board ranking); until that lands the metrics may be null — that
is expected and never fails the gate.

Run (hypothesis eval just adds to a normal run):
    EVAL_HYPOTHESES=1 EVAL_SKIP_JUDGE=1 python3 eval-service/evaluate.py
"""

import json
import math
import os
import re
import statistics
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
GATEWAY = os.environ.get("RAG_GATEWAY", "http://localhost:18080").rstrip("/")
API_BASE = GATEWAY if GATEWAY.endswith("/api/v1") else GATEWAY + "/api/v1"
EMBED_URL = os.environ.get("EVAL_EMBED_URL", "http://localhost:8085/v1/embeddings")
USER = os.environ.get("RAG_USER", "admin")
PASS = os.environ.get("RAG_PASS", "")
TOPK = int(os.environ.get("EVAL_TOPK", "5"))
# Opt-in hypothesis-quality eval (P0.7). Off by default; additive and never gated.
EVAL_HYPOTHESES = os.environ.get("EVAL_HYPOTHESES") == "1"
HYP_COUNT = int(os.environ.get("EVAL_HYP_COUNT", "5"))
# Opt-in discovery/bridge eval — the contribution of the bridge layer to the
# board. Off by default; additive and never gated.
EVAL_DISCOVERY = os.environ.get("EVAL_DISCOVERY") == "1"


def env(key: str) -> str:
    if os.environ.get(key, ""):
        return os.environ[key]
    env_paths = [
        os.path.join(HERE, "..", "..", "infra", ".env"),
        os.path.join(HERE, "..", ".env"),
    ]
    for path in env_paths:
        try:
            with open(path, encoding="utf-8") as f:
                for line in f:
                    if line.startswith(key + "="):
                        return line.split("=", 1)[1].strip()
        except FileNotFoundError:
            continue
    return ""


JUDGE_BASE_URL = env("VLLM_URL").rstrip("/")
JUDGE_URL = JUDGE_BASE_URL + "/v1/chat/completions" if JUDGE_BASE_URL else ""
JUDGE_KEY = env("VLLM_API_KEY")
JUDGE_MODEL = env("VLLM_MODEL")

# ── Gate configuration ──────────────────────────────────────────────────────
# `--gate` (or EVAL_GATE=1) turns the descriptive scorecard into a pass/fail
# regression gate that exits non-zero on FAIL. Exit codes:
#   0 = PASS, 1 = quality FAIL (threshold breach / null judge metrics),
#   2 = harness/setup error (e.g. the live API is unreachable).
# Without the gate flag the run is purely descriptive and always exits 0.
GATE = "--gate" in __import__("sys").argv[1:] or os.environ.get("EVAL_GATE") == "1"

EXIT_PASS = 0
EXIT_QUALITY_FAIL = 1
EXIT_HARNESS_ERROR = 2


def _floor(key: str, default: float) -> float:
    """Per-metric minimum mean, overridable via EVAL_MIN_<KEY> env vars."""
    return float(os.environ.get("EVAL_MIN_" + key.upper(), default))


# Defaults are calibrated for the shipped synthetic gold set:
#   retrieval (hit@k / mrr / ndcg@k) should stay near 1.0, so require ≥ 0.90.
#   answer_similarity uses F2LLM-v2 cosine between a free-form answer and a terse
#     gold string; it is useful as a regression signal but should leave headroom.
#   judge metrics (faithfulness / answer_relevance / context_relevance /
#     answer_correctness) are held to a conservative RAG bar when the judge runs.
# Generated results.json is a local/staging artifact and must not be committed.
THRESHOLDS = {
    "hit@k": _floor("hit@k", 0.90),
    "mrr": _floor("mrr", 0.90),
    "ndcg@k": _floor("ndcg@k", 0.90),
    "answer_similarity": _floor("answer_similarity", 0.55),
    "faithfulness": _floor("faithfulness", 0.70),
    "answer_relevance": _floor("answer_relevance", 0.70),
    "context_relevance": _floor("context_relevance", 0.70),
    "answer_correctness": _floor("answer_correctness", 0.70),
}
# The four LLM-judge metrics. When EVAL_SKIP_JUDGE is unset these are EXPECTED
# to produce a score; a null/n==0 aggregate is a FAIL (a run that judged nothing
# must not be reported as a passing state).
JUDGE_METRICS = ("faithfulness", "answer_relevance", "context_relevance", "answer_correctness")

_TRANSIENT = (urllib.error.URLError, TimeoutError, OSError)


def http(url, body=None, headers=None, timeout=90, retries=5):
    """POST/GET JSON with retries on transient network errors and 429/5xx."""
    data = json.dumps(body).encode() if body is not None else None
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url, data=data, headers=dict(headers or {}), method="POST" if data else "GET")
            if data:
                req.add_header("Content-Type", "application/json")
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return json.loads(r.read().decode("utf-8"), strict=False)
        except urllib.error.HTTPError as e:
            last = e
            if e.code in (429, 500, 502, 503, 504):
                time.sleep(3 * (attempt + 1))
                continue
            raise
        except _TRANSIENT as e:
            last = e
            time.sleep(3 * (attempt + 1))
    raise last


def login() -> str:
    return http(f"{API_BASE}/auth/login", {"username": USER, "password": PASS})["access_token"]


def ask(token, question):
    h = {"Authorization": "Bearer " + token}
    chat = http(f"{API_BASE}/chats", {"title": "eval"}, h)["id"]
    t0 = time.time()
    resp = http(f"{API_BASE}/chats/{chat}/messages", {"content": question}, h, timeout=150)
    return resp.get("content", ""), resp.get("sources", []), time.time() - t0


def embed(texts):
    return [d["embedding"] for d in http(EMBED_URL, {"model": "F2LLM-v2-0.6B", "input": texts})["data"]]


def cosine(a, b):
    dot = sum(x * y for x, y in zip(a, b))
    na, nb = math.sqrt(sum(x * x for x in a)), math.sqrt(sum(y * y for y in b))
    return dot / (na * nb) if na and nb else 0.0


JUDGE_SUFFIX = '\n\nОтветь СТРОГО в формате JSON без пояснений: {"score": <число от 0.0 до 1.0>}'


def judge(prompt):
    """Returns a 0..1 score, or None if skipped/unreachable/unparseable."""
    if os.environ.get("EVAL_SKIP_JUDGE") or not JUDGE_URL or not JUDGE_MODEL:
        return None
    headers = {"X-Data-Logging-Enabled": "false"}
    if JUDGE_KEY:
        headers["Authorization"] = "Bearer " + JUDGE_KEY
    try:
        r = http(
            JUDGE_URL,
            {"model": JUDGE_MODEL, "messages": [{"role": "user", "content": prompt + JUDGE_SUFFIX}], "max_tokens": 40, "temperature": 0},
            headers,
        )
        txt = r["choices"][0]["message"]["content"]
    except Exception:
        return None  # error body (no choices), network failure, etc. → skip this metric
    m = re.search(r'"score"\s*:\s*([01](?:\.\d+)?)', txt)
    if m:
        return float(m.group(1))
    m = re.search(r"[01](?:\.\d+)?", txt)
    return float(m.group()) if m else None


PROMPTS = {
    "faithfulness": "Контекст (фрагменты, найденные системой):\n{ctx}\n\nОтвет системы:\n{ans}\n\nЗадача: оцени, насколько ответ подтверждается ТОЛЬКО приведённым контекстом, без домыслов. 1.0 — все утверждения ответа есть в контексте; 0.0 — ответ не следует из контекста. Верни ТОЛЬКО число от 0.0 до 1.0.",
    "answer_relevance": "Вопрос: {q}\nОтвет: {ans}\n\nНасколько ответ по существу отвечает именно на этот вопрос (без воды и ухода в сторону)? Верни ТОЛЬКО число от 0.0 до 1.0.",
    "context_relevance": "Вопрос: {q}\n\nНайденный контекст:\n{ctx}\n\nНасколько найденный контекст релевантен вопросу? 1.0 — содержит нужную информацию; 0.0 — нерелевантен. Верни ТОЛЬКО число от 0.0 до 1.0.",
    "correctness": "Вопрос: {q}\nЭталонный ответ: {gt}\nОтвет системы: {ans}\n\nНасколько ответ системы фактически совпадает с эталоном? 1.0 — полностью верно; 0.0 — неверно. Верни ТОЛЬКО число от 0.0 до 1.0.",
}


def retrieval_metrics(sources, relevant_doc, k):
    # Reduce the top-k chunk hits to document level (one entry per doc, first
    # occurrence) so the metrics are doc-level and nDCG stays in [0, 1].
    docs = []
    for s in sources[:k]:
        fn = s.get("filename", "") or ""
        if fn and fn not in docs:
            docs.append(fn)
    ranks = [i + 1 for i, fn in enumerate(docs) if relevant_doc.lower() in fn.lower()]
    if not ranks:
        return 0.0, 0.0, 0.0
    r = ranks[0]  # single relevant doc, binary relevance
    return 1.0, 1.0 / r, (1.0 / math.log2(r + 1)) / (1.0 / math.log2(2))


def prometheus_stages(metric: str, group: str):
    """Per-stage latency (p50/p95 seconds) from a Prometheus histogram, grouped
    by the given label. Used for the query pipeline (rag_stage_duration_seconds
    by stage) and the document-ingestion pipeline (rag_processing_duration_seconds
    by queue). Empty if Prometheus is unreachable or has no data."""
    import urllib.parse

    prom = os.environ.get("EVAL_PROM_URL", "http://localhost:9090")
    out: dict = {}
    for quant, label in ((0.5, "p50"), (0.95, "p95")):
        promql = f"histogram_quantile({quant}, sum by ({group}, le) ({metric}_bucket))"
        try:
            d = http(prom + "/api/v1/query?query=" + urllib.parse.quote(promql))
            for r in d.get("data", {}).get("result", []):
                k = r["metric"].get(group, "?")
                v = float(r["value"][1])
                if v == v:  # skip NaN
                    out.setdefault(k, {})[label] = round(v, 3)
        except Exception:
            pass
    return out


# ── Hypothesis-quality eval (P0.7, opt-in via EVAL_HYPOTHESES=1) ──────────────
# Scores the Hypothesis Factory: for each KPI in dataset-hypotheses.jsonl it
# creates/finds the KPI, calls POST /hypotheses/generate, then measures whether
# the generated hypotheses are falsifiable, carry counter-evidence, and rank
# above the vague "directions". All reads are tolerant of missing fields so the
# code runs (reporting null/0) before the full generation stack lands.


def find_or_create_kpi(token, spec):
    """Return a KPI id for the dataset spec, reusing an existing same-title KPI."""
    h = {"Authorization": "Bearer " + token}
    title = (spec.get("title") or "").strip()
    for k in http(f"{API_BASE}/kpis", None, h) or []:
        if (k.get("title") or "").strip() == title and title:
            return k["id"]
    body = {
        "title": title,
        "description": spec.get("description", ""),
        "metric": spec.get("metric", ""),
        "unit": spec.get("unit", ""),
        "direction": spec.get("direction", ""),
        "baseline": spec.get("baseline"),
        "target": spec.get("target"),
        "function_area": spec.get("material_family", ""),
        # material_family is a domain field; keep it on detail so it round-trips.
        "detail": {"material_family": spec.get("material_family", "")},
    }
    return http(f"{API_BASE}/kpis", body, h)["id"]


def generate_for_kpi(token, kpi_id, count):
    """POST /hypotheses/generate and return the created hypothesis views."""
    h = {"Authorization": "Bearer " + token}
    return http(f"{API_BASE}/hypotheses/generate", {"kpi_id": kpi_id, "count": count}, h, timeout=600) or []


def _hay(hyp):
    """Flatten a hypothesis' detail+assessment to lowercase text for keyword checks."""
    parts = [hyp.get("statement", ""), hyp.get("rationale", ""), hyp.get("method", "")]
    for key in ("detail", "assessment"):
        v = hyp.get(key)
        if isinstance(v, (dict, list)):
            parts.append(json.dumps(v, ensure_ascii=False))
        elif isinstance(v, str):
            parts.append(v)
    return " ".join(parts).lower()


# Bilingual (RU/EN) keyword sets so the check works whichever language the
# generator emits. A hypothesis is "complete" when it names a lever, ties to a
# baseline, and proposes a way to test it.
_KW_INTERVENTION = ("intervention", "composition_change", "process_change", "lever", "вмешательств", "легирован", "допирован", "режим", "обработк", "состав")
_KW_MATERIAL = ("material_system", "material", "alloy", "coating", "electrolyte", "материал", "сплав", "покрыти", "электролит", "систем")
_KW_BASELINE = ("baseline", "база", "базов", "исходн", "относительно", "к базе")
_KW_VALIDATION = ("validation_plan", "experiment_plan", "experiment", "test_method", "characterization", "success_criteria", "валидаци", "эксперимент", "испытан", "метод проверк", "критери")


def schema_completeness(hyp):
    """True if the hypothesis is falsifiable: it names an intervention+material,
    references a baseline, and carries a validation_plan/experiment."""
    text = _hay(hyp)

    def has(words):
        return any(w in text for w in words)

    return has(_KW_INTERVENTION) and has(_KW_MATERIAL) and has(_KW_BASELINE) and has(_KW_VALIDATION)


def counterevidence(hyp):
    """True if at least one evidence item is `contradicts` or `context` (i.e. the
    hypothesis was not just bolstered by uniformly `supports` evidence)."""
    ev = hyp.get("evidence") or []
    return any((e.get("stance") or "").lower() in ("contradicts", "context") for e in ev)


def composite_of(hyp):
    """The board's headline score: composite_score, falling back to assessment.ranking.score."""
    cs = hyp.get("composite_score")
    if isinstance(cs, (int, float)):
        return float(cs)
    rank = (hyp.get("assessment") or {}).get("ranking") or {}
    sc = rank.get("score")
    return float(sc) if isinstance(sc, (int, float)) else None


def separation(hyps, good, bad):
    """Best-effort: do the generated hypotheses align more with the falsifiable
    `expected_good` shapes than the vague `expected_bad` ones, when ranked by the
    board's composite? Returns a dict; `score` in [0,1] is the fraction of
    (good, bad) pairs correctly ordered, or null if it can't be computed (no
    scores, or the embedder is down so we can't match generated→expected)."""
    scored = [(h, composite_of(h)) for h in hyps]
    scored = [(h, s) for h, s in scored if s is not None]
    out = {"n_generated": len(hyps), "n_scored": len(scored)}
    if not scored:
        out["score"] = None
        out["note"] = "no composite/ranking scores yet (generation stack not fully up)"
        return out

    good_txt = [g.get("description", "") for g in good if g.get("description")]
    bad_txt = [b.get("description", "") for b in bad if b.get("description")]
    try:
        if not good_txt or not bad_txt:
            raise ValueError("no reference descriptions")
        vecs = embed([h.get("statement", "") or "" for h, _ in scored] + good_txt + bad_txt)
        n = len(scored)
        hv, gv, bv = vecs[:n], vecs[n:n + len(good_txt)], vecs[n + len(good_txt):]
        good_s = [s for (_, s), v in zip(scored, hv) if max(cosine(v, g) for g in gv) >= max(cosine(v, b) for b in bv)]
        bad_s = [s for (_, s), v in zip(scored, hv) if max(cosine(v, g) for g in gv) < max(cosine(v, b) for b in bv)]
        out["n_good_like"], out["n_bad_like"] = len(good_s), len(bad_s)
        if good_s and bad_s:
            pairs = [(gs, bs) for gs in good_s for bs in bad_s]
            out["score"] = round(sum(1 for gs, bs in pairs if gs > bs) / len(pairs), 3)
            out["note"] = "fraction of good>bad composite pairs (good/bad split by F2LLM-v2 similarity)"
        else:
            out["score"] = None
            out["note"] = "could not split generated into good-like vs bad-like (need both)"
    except Exception:
        # Embedder down or no references — fall back to reporting raw scores.
        vals = [s for _, s in scored]
        out["score"] = None
        out["composite_scores"] = [round(v, 3) for v in vals]
        out["composite_mean"] = round(statistics.mean(vals), 3)
        out["note"] = "embedder unavailable; reporting generated composite scores only"
    return out


def run_hypothesis_eval(token):
    """Score the Hypothesis Factory against dataset-hypotheses.jsonl. Returns the
    block stored under results.json -> "hypotheses" (additive; never gated)."""
    path = os.path.join(HERE, "dataset-hypotheses.jsonl")
    specs = [json.loads(ln) for ln in open(path, encoding="utf-8") if ln.strip()]
    print(f"\nHypothesis eval: {len(specs)} KPIs (count={HYP_COUNT} each)\n", flush=True)

    kpis_out, all_complete, all_counter = [], [], []
    for spec in specs:
        kpi = spec.get("kpi", {})
        title = kpi.get("title", "")
        try:
            kpi_id = find_or_create_kpi(token, kpi)
            hyps = generate_for_kpi(token, kpi_id, HYP_COUNT)
        except Exception as e:
            print(f"  ! KPI failed: {title[:44]} {e}", flush=True)
            kpis_out.append({"kpi": title, "error": str(e), "n_generated": 0})
            continue
        comp = [schema_completeness(h) for h in hyps]
        cnt = [counterevidence(h) for h in hyps]
        all_complete += comp
        all_counter += cnt
        sep = separation(hyps, spec.get("expected_good", []), spec.get("expected_bad", []))

        def rate(xs):
            return round(sum(xs) / len(xs), 3) if xs else None

        entry = {
            "kpi": title,
            "kpi_id": kpi_id,
            "n_generated": len(hyps),
            "schema_completeness": rate(comp),
            "counterevidence_rate": rate(cnt),
            "separation": sep,
        }
        kpis_out.append(entry)
        print(f"  {title[:44]:46s} n={len(hyps)} complete={entry['schema_completeness']} counter={entry['counterevidence_rate']} sep={sep.get('score')}", flush=True)

    def agg(xs):
        return {"mean": round(sum(xs) / len(xs), 3), "n": len(xs)} if xs else {"mean": None, "n": 0}

    return {
        "n_kpis": len(specs),
        "hyp_count": HYP_COUNT,
        "schema_completeness": agg(all_complete),
        "counterevidence_rate": agg(all_counter),
        "kpis": kpis_out,
    }


# ── Discovery / bridge eval (opt-in via EVAL_DISCOVERY=1) ─────────────────────
# Measures the contribution of the bridge layer (graph-compute ScoreBridges →
# discovery-worker) to the FINAL hypothesis board: it reads the published
# auto_bridge hypotheses and scores whether they are falsifiable, grounded,
# genuinely cross-community and corpus-novel — then compares them against the
# per-cluster (auto_cluster) baseline so the *added value of bridges* is
# explicit. Additive, never gated (like the hypothesis eval above).


def _obj(value):
    """Coerce a JSONB field (a dict already, or a raw-JSON string) to a dict."""
    if isinstance(value, dict):
        return value
    if isinstance(value, str):
        try:
            parsed = json.loads(value)
        except (ValueError, TypeError):
            return {}
        return parsed if isinstance(parsed, dict) else {}
    return {}


def _has_tag(hyp, tag):
    return tag in (hyp.get("tags") or [])


def _grounded(hyp):
    """At least one evidence item cites a real document id."""
    return any(e.get("document_id") for e in (hyp.get("evidence") or []))


def _doc_count(hyp):
    """Number of distinct documents the hypothesis cites."""
    return len({e.get("document_id") for e in (hyp.get("evidence") or []) if e.get("document_id")})


def _cross_community_span(hyp):
    """Evidence spans BOTH bridged themes (relations theme_a and theme_b, set by
    discovery-worker) — the defining property of a bridge hypothesis."""
    rels = {(e.get("relation") or "").lower() for e in (hyp.get("evidence") or [])}
    return "theme_a" in rels and "theme_b" in rels


def _novelty(hyp):
    """The corpus-grounded novelty (1 - top_sim) the discovery worker recorded."""
    ns = hyp.get("novelty_score")
    return float(ns) if isinstance(ns, (int, float)) else None


def run_discovery_eval(token):
    """Score the bridge/discovery layer and its lift over the per-cluster baseline.
    Returns the block stored under results.json -> "discovery" (additive, never gated)."""
    h = {"Authorization": "Bearer " + token}
    board = http(f"{API_BASE}/hypotheses?limit=500", None, h) or []
    bridges = [x for x in board if _has_tag(x, "auto_bridge")]
    clusters = [x for x in board if _has_tag(x, "auto_cluster")]
    print(f"\nDiscovery eval: {len(bridges)} bridge hypotheses vs {len(clusters)} cluster (baseline)\n", flush=True)

    def rate(items):
        return round(sum(1 for x in items if x) / len(items), 3) if items else None

    def meanf(items):
        vals = [x for x in items if x is not None]
        return round(statistics.mean(vals), 3) if vals else None

    scores = [_obj(b.get("generation")).get("scores", {}) for b in bridges]

    def score_mean(key):
        return meanf([s.get(key) for s in scores if isinstance(s, dict)])

    bridge_nov = meanf([_novelty(b) for b in bridges])
    cluster_nov = meanf([_novelty(c) for c in clusters])
    block = {
        "n_bridge_hypotheses": len(bridges),
        "n_cluster_hypotheses": len(clusters),
        # Bridge-layer quality (reusing the falsifiability/counter-evidence checks).
        "falsifiability": rate([schema_completeness(b) for b in bridges]),
        "grounding": rate([_grounded(b) for b in bridges]),
        "cross_community_span": rate([_cross_community_span(b) for b in bridges]),
        "counterevidence_rate": rate([counterevidence(b) for b in bridges]),
        "novelty_mean": bridge_nov,
        "bridge_scores": {
            "maverick": score_mean("maverick"),
            "bridging_centrality": score_mean("bridging_centrality"),
            "convergence": score_mean("convergence"),
            "composite": score_mean("composite"),
        },
        # The headline question: do bridges add cross-document, corpus-novel
        # connections the single-cluster path cannot?
        "contribution_vs_cluster": {
            "bridge_cross_doc_rate": rate([_doc_count(b) >= 2 for b in bridges]),
            "cluster_cross_doc_rate": rate([_doc_count(c) >= 2 for c in clusters]),
            "bridge_novelty_mean": bridge_nov,
            "cluster_novelty_mean": cluster_nov,
            "novelty_delta": (round(bridge_nov - cluster_nov, 3) if bridge_nov is not None and cluster_nov is not None else None),
            "note": "bridges add value when they surface cross-community (theme_a×theme_b) hypotheses the per-cluster baseline cannot, at comparable-or-higher corpus novelty",
        },
    }
    print(f"  bridges: falsifiable={block['falsifiability']} span={block['cross_community_span']} novelty={block['novelty_mean']} composite={block['bridge_scores']['composite']}", flush=True)
    contrib = block["contribution_vs_cluster"]
    print(f"  contribution: cross_doc bridge={contrib['bridge_cross_doc_rate']} vs cluster={contrib['cluster_cross_doc_rate']} novelty_delta={contrib['novelty_delta']}", flush=True)
    return block


def gate(summary):
    """Evaluate the summary against the gate. Returns (passed, lines) where
    `lines` is a human-readable PASS/FAIL breakdown (always populated). A metric
    fails if its computed mean is below the configured floor; the judge metrics
    additionally fail if they were expected (EVAL_SKIP_JUDGE unset) but produced
    no score (mean is null / n == 0)."""
    failures, lines = [], []
    skip_judge = bool(os.environ.get("EVAL_SKIP_JUDGE"))
    flat = {**summary["retrieval"], **summary["generation"]}

    for key in ("hit@k", "mrr", "ndcg@k", "answer_similarity", *JUDGE_METRICS):
        agg = flat.get(key, {"mean": None, "n": 0})
        floor = THRESHOLDS[key]
        mean, n = agg.get("mean"), agg.get("n", 0)
        is_judge = key in JUDGE_METRICS

        if mean is None or n == 0:
            if is_judge and not skip_judge:
                msg = f"{key}: null/n=0 — judge expected but nothing was scored (not a passing state)"
                failures.append(msg)
                lines.append("  FAIL  " + msg)
            elif is_judge and skip_judge:
                lines.append(f"  skip  {key}: judge disabled (EVAL_SKIP_JUDGE)")
            else:
                # A non-judge metric (retrieval / similarity) that scored nothing
                # means the run produced no usable data → fail the gate.
                msg = f"{key}: null/n=0 — no data scored"
                failures.append(msg)
                lines.append("  FAIL  " + msg)
            continue

        if mean < floor:
            msg = f"{key}: mean {mean:.3f} < floor {floor:.3f} (n={n})"
            failures.append(msg)
            lines.append("  FAIL  " + msg)
        else:
            lines.append(f"  ok    {key}: mean {mean:.3f} >= floor {floor:.3f} (n={n})")

    return (not failures), lines


def main():
    cases = [json.loads(ln) for ln in open(os.path.join(HERE, "dataset.jsonl"), encoding="utf-8") if ln.strip()]
    try:
        token = login()
    except Exception as e:
        # The live stack is required to evaluate anything. Treat an unreachable
        # API as a harness/setup error (exit 2), distinct from a quality FAIL.
        print(f"!! harness error: cannot reach RAG API at {GATEWAY}: {e}", flush=True)
        if GATE:
            print("\n=== GATE: ERROR (harness/setup, exit 2) ===", flush=True)
        raise SystemExit(EXIT_HARNESS_ERROR)
    rows = []
    print(f"Evaluating {len(cases)} questions (top_k={TOPK}, judge={JUDGE_MODEL})\n", flush=True)
    for c in cases:
        try:
            ans, sources, dt = ask(token, c["q"])
        except Exception as e:
            print(f"  ! case failed (RAG call): {c['q'][:40]}… {e}", flush=True)
            continue
        ctx = "\n---\n".join((s.get("snippet", "") or "") for s in sources[:TOPK])[:3000]
        hit, mrr, ndcg = retrieval_metrics(sources, c["doc"], TOPK)
        try:
            sim = cosine(*embed([ans, c["gt"]]))
        except Exception:
            sim = None
        row = {
            "q": c["q"], "doc": c["doc"], "hit": hit, "mrr": mrr, "ndcg": ndcg,
            "faithfulness": judge(PROMPTS["faithfulness"].format(ctx=ctx, ans=ans)),
            "answer_relevance": judge(PROMPTS["answer_relevance"].format(q=c["q"], ans=ans)),
            "context_relevance": judge(PROMPTS["context_relevance"].format(q=c["q"], ctx=ctx)),
            "answer_correctness": judge(PROMPTS["correctness"].format(q=c["q"], gt=c["gt"], ans=ans)),
            "answer_similarity": sim, "latency_s": dt, "answer": ans[:140],
        }
        rows.append(row)

        def s(x):
            return "—" if x is None else f"{x:.2f}"
        print(f"  {c['q'][:44]:46s} hit={hit:.0f} mrr={mrr:.2f} ndcg={ndcg:.2f} | faith={s(row['faithfulness'])} rel={s(row['answer_relevance'])} ctx={s(row['context_relevance'])} corr={s(row['answer_correctness'])} sim={s(sim)} | {dt:.1f}s", flush=True)
        time.sleep(1)

    def agg(k):
        vals = [r[k] for r in rows if r.get(k) is not None]
        return {"mean": round(statistics.mean(vals), 3), "n": len(vals)} if vals else {"mean": None, "n": 0}

    lat = sorted(r["latency_s"] for r in rows) or [0]
    summary = {
        "n_questions": len(rows), "top_k": TOPK, "judge_model": JUDGE_MODEL,
        "retrieval": {"hit@k": agg("hit"), "mrr": agg("mrr"), "ndcg@k": agg("ndcg")},
        "generation": {m: agg(m) for m in ("faithfulness", "answer_relevance", "context_relevance", "answer_correctness", "answer_similarity")},
        "latency_s": {"p50": round(lat[len(lat) // 2], 2), "p95": round(lat[min(len(lat) - 1, int(0.95 * len(lat)))], 2), "mean": round(statistics.mean(lat), 2)},
    }
    time.sleep(20)  # let Prometheus scrape the freshly recorded stage histograms
    summary["pipeline"] = prometheus_stages("rag_stage_duration_seconds", "stage")
    summary["ingestion"] = prometheus_stages("rag_processing_duration_seconds", "queue")

    payload = {"summary": summary, "cases": rows}
    # Additive, opt-in hypothesis-quality eval (P0.7). Its failures never abort
    # the RAG scorecard and never affect the gate.
    if EVAL_HYPOTHESES:
        try:
            payload["hypotheses"] = run_hypothesis_eval(token)
        except Exception as e:
            print(f"\nhypothesis eval failed (non-fatal): {e}", flush=True)
            payload["hypotheses"] = {"error": str(e)}
    # Additive, opt-in discovery/bridge eval — also never gated.
    if EVAL_DISCOVERY:
        try:
            payload["discovery"] = run_discovery_eval(token)
        except Exception as e:
            print(f"\ndiscovery eval failed (non-fatal): {e}", flush=True)
            payload["discovery"] = {"error": str(e)}

    json.dump(payload, open(os.path.join(HERE, "results.json"), "w", encoding="utf-8"), ensure_ascii=False, indent=2)
    print("\n=== SCORECARD ===")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    if "hypotheses" in payload:
        print("\n=== HYPOTHESES ===")
        print(json.dumps(payload["hypotheses"], ensure_ascii=False, indent=2))
    if "discovery" in payload:
        print("\n=== DISCOVERY (bridge contribution) ===")
        print(json.dumps(payload["discovery"], ensure_ascii=False, indent=2))

    # Publish to the backend so the admin UI ("Метрики") can display it.
    try:
        admin_user = os.environ.get("RAG_ADMIN_USER") or os.environ.get("ADMIN_USERNAME") or USER
        admin_pass = os.environ.get("RAG_ADMIN_PASS") or os.environ.get("ADMIN_PASSWORD") or os.environ.get("RAG_PASS", "")
        if not admin_pass:
            raise RuntimeError("RAG_ADMIN_PASS or ADMIN_PASSWORD is required to publish the scorecard")
        atok = http(f"{API_BASE}/auth/login", {"username": admin_user, "password": admin_pass})["access_token"]
        http(f"{API_BASE}/admin/metrics", payload, {"Authorization": "Bearer " + atok})
        print("\npublished scorecard to /api/v1/admin/metrics")
    except Exception as e:
        print("\npublish failed:", e)

    # Gate evaluation. We always print a clear PASS/FAIL breakdown so a plain
    # descriptive run still tells you whether it would pass; only `--gate`
    # (EVAL_GATE=1) actually turns a FAIL into a non-zero exit.
    passed, lines = gate(summary)
    print("\n=== GATE " + ("(enforced)" if GATE else "(advisory — pass --gate or EVAL_GATE=1 to enforce)") + " ===")
    for ln in lines:
        print(ln)
    verdict = "PASS" if passed else "FAIL"
    print(f"\nGATE: {verdict}")
    if GATE and not passed:
        raise SystemExit(EXIT_QUALITY_FAIL)
    # In enforced mode a pass returns 0 explicitly; advisory mode also returns 0.
    raise SystemExit(EXIT_PASS)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise  # honour the explicit exit codes set above (0/1/2)
    except Exception as e:
        # Any other unhandled failure is a harness/setup problem, not a quality
        # signal — surface it as exit 2 rather than a Python traceback (exit 1).
        print(f"!! harness error: {type(e).__name__}: {e}", flush=True)
        raise SystemExit(EXIT_HARNESS_ERROR)
