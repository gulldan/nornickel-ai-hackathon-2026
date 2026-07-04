#!/usr/bin/env python3
"""Auto-clustering worker — keeps the theme board fresh, hands-free.

Watches the corpus epoch in Valkey (bumped by main-service every time a document
finishes indexing). When the epoch moves and then holds steady for DEBOUNCE_SEC
(ingestion has settled), it RE-CLUSTERS the corpus and replaces the board — so a
user who just uploads papers and waits sees themes appear on their own, with no
manual script run.

The heavy clustering compute — Qdrant scroll, chunk-weighted document pooling,
blockwise mutual-kNN cosine graph, Leiden communities, modularity/quality metrics
and cluster lineage — lives in the graph-compute service (Rust). This worker only
orchestrates it: the knobs below are forwarded as the request config, and the
returned communities are labelled and published here.

Pipeline: poll epoch -> debounce -> graph-compute Cluster RPC (documents +
previous board + config) -> LLM theme label (keyword fallback) -> publish a
versioned cluster set -> sync auto-hypotheses -> trigger ITC refresh.
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from collections import Counter

import grpc
import redis
from genproto.graph.v1 import graph_pb2, graph_pb2_grpc

GW = os.environ.get("RAG_GW", "http://nginx/api/v1")
USER = os.environ.get("RAG_USER", "admin")
PASS = os.environ.get("RAG_PASS", "")
COLLECTION = os.environ.get("QDRANT_COLLECTION", "documents")
GRAPH_COMPUTE_ADDR = os.environ.get("GRAPH_COMPUTE_ADDR", "graph-compute:9093")
GRAPH_COMPUTE_TIMEOUT = float(os.environ.get("GRAPH_COMPUTE_TIMEOUT", "600"))
VALKEY_URL = os.environ.get("VALKEY_URL", "redis://valkey:6379")
EPOCH_KEY = os.environ.get("EPOCH_KEY", "rag:corpus_epoch:shared")
LAST_CLUSTERED_KEY = os.environ.get("LAST_CLUSTERED_KEY", "rag:clusters:last_epoch")
ITC_TRIGGER_KEY = os.environ.get("ITC_TRIGGER_KEY", "rag:itc:trigger")
WORKER_STATUS_KEY = "rag:worker:clusters:status"
CHECK_INTERVAL = int(os.environ.get("CHECK_INTERVAL", "60"))
DEBOUNCE_SEC = int(os.environ.get("DEBOUNCE_SEC", "120"))
KNN_K = int(os.environ.get("KNN_K", "6"))
KNN_BLOCK_SIZE = max(64, int(os.environ.get("KNN_BLOCK_SIZE", "512")))
RESOLUTION = float(os.environ.get("RESOLUTION", "1.0"))
MIN_SIZE = max(2, int(os.environ.get("MIN_SIZE", "2")))
# Drop graph edges whose cosine similarity is below this floor (0.0 keeps every
# mutual-kNN pair, like before; raise it to split loosely related documents).
SIM_THRESHOLD = max(0.0, min(1.0, float(os.environ.get("SIM_THRESHOLD", "0.0"))))
# true  -> mutual kNN (an edge needs both docs in each other's top-k): tight,
#          well-separated themes. false -> union kNN: denser graph, fewer/larger
#          clusters. Either way isolated nodes are re-attached to their best peer.
MUTUAL_KNN = os.environ.get("MUTUAL_KNN", "true").lower() not in {"0", "false", "no", "off"}
# Section/position weighting for document vectors (P1.2). Mean-pooling every chunk
# equally lets boilerplate (references, acknowledgements, headers, OCR noise) drag
# the document vector off-topic, so chunks are weighted before pooling:
#   * the earliest chunks (title/abstract/intro) get CHUNK_W_LEAD,
#   * chunks whose text carries results/conclusion cues get CHUNK_W_RESULTS,
#   * reference/bibliography/acknowledgement chunks get CHUNK_W_REFS (~0 to drop),
#   * every other chunk gets the implicit baseline weight 1.0.
# Set CHUNK_WEIGHTING=false to fall back to the old equal mean-pool.
CHUNK_WEIGHTING = os.environ.get("CHUNK_WEIGHTING", "true").lower() not in {"0", "false", "no", "off"}
CHUNK_LEAD_COUNT = max(0, int(os.environ.get("CHUNK_LEAD_COUNT", "2")))
CHUNK_W_LEAD = max(0.0, float(os.environ.get("CHUNK_W_LEAD", "2.0")))
CHUNK_W_RESULTS = max(0.0, float(os.environ.get("CHUNK_W_RESULTS", "1.5")))
CHUNK_W_REFS = max(0.0, float(os.environ.get("CHUNK_W_REFS", "0.05")))
# Lineage (P1.4): an old<->new membership overlap (fraction of the previous
# cluster carried into a new one) at or above this counts as a real link, so a
# previous cluster feeding >=2 new ones is a split and >=2 previous clusters
# feeding one new one is a merge.
LINEAGE_OVERLAP_MIN = max(0.0, min(1.0, float(os.environ.get("LINEAGE_OVERLAP_MIN", "0.3"))))
# Graph algorithm + seed forwarded to graph-compute (Leiden, deterministic).
CLUSTER_ALGORITHM = "leiden"
CLUSTER_SEED = 42
VLLM_URL = os.environ.get("VLLM_URL", "")
VLLM_MODEL = os.environ.get("VLLM_MODEL", "")
VLLM_API_KEY = os.environ.get("VLLM_API_KEY", "")
VLLM_RPM = max(0, int(os.environ.get("VLLM_RPM", "18")))
# Incremental labelling: a full recluster used to pay one LLM call per cluster
# EVERY run, so a settled board with ~1000 themes re-billed ~1000 calls and took
# hours. A theme whose membership is unchanged (same fingerprint) or whose lineage
# is highly stable keeps its previous label for free — only genuinely new or
# reshaped clusters go to the LLM. Set LABEL_REUSE=false for the old behaviour.
LABEL_REUSE = os.environ.get("LABEL_REUSE", "true").lower() not in {"0", "false", "no", "off"}
LABEL_REUSE_STABILITY = max(0.0, min(1.0, float(os.environ.get("LABEL_REUSE_STABILITY", "0.9"))))
# Bound labelling latency: a slow or hung LLM used to block a single cluster for up
# to 3×180s, so one bad provider stalled the whole board for hours. Cap attempts and
# per-call timeout so a slow label falls back to keywords fast and the board still
# converges — the reused good label returns on a later run when the LLM is healthy.
LABEL_LLM_ATTEMPTS = max(1, int(os.environ.get("LABEL_LLM_ATTEMPTS", "2")))
LABEL_LLM_TIMEOUT = max(5, int(os.environ.get("LABEL_LLM_TIMEOUT", "45")))
AUTO_HYPOTHESES = os.environ.get("AUTO_HYPOTHESES", "true").lower() not in {"0", "false", "no", "off"}
AUTO_HYPOTHESIS_LIMIT = max(0, int(os.environ.get("AUTO_HYPOTHESIS_LIMIT", "24")))
# Auto re-clustering. Every corpus-epoch move triggers a full recluster + LLM
# theme labelling; on a corpus whose epoch churns this floods the LLM with
# relabel calls. Set CLUSTER_AUTORUN=false to keep the worker alive but stop the
# automatic regeneration (themes freeze at the last published board, no LLM
# traffic) — a manual epoch bump past last_clustered is then the only trigger.
CLUSTER_AUTORUN = os.environ.get("CLUSTER_AUTORUN", "true").lower() not in {"0", "false", "no", "off"}
_LAST_VLLM_CALL = {"t": 0.0}

# Boilerplate words excluded from the keyword fallback label.
STOPWORDS = {
    "после", "также", "более", "может", "должны", "которые", "between", "which",
    "the", "and", "for", "from", "with", "without", "into", "onto", "over", "under",
    "that", "this", "these", "those", "there", "where", "when", "what", "how", "why",
    "have", "has", "had", "were", "was", "are", "is", "been", "being", "can", "could",
    "should", "would", "will", "may", "might", "must", "all", "any", "each", "some",
    "such", "than", "then", "also", "via", "towards", "toward", "using", "used",
    "your", "our", "their", "its", "his", "her", "along",
    "their", "based", "using", "results", "research", "paper", "study", "article",
    "abstract", "introduction", "method", "methods", "table", "figure",
    "university", "department", "school", "faculty", "institute", "center", "centre",
    "laboratory", "lab", "hospital", "clinic", "author", "authors", "published",
    "conference", "journal", "china", "germany", "denmark", "taiwan", "arizona",
    "heidelberg", "beijing", "tsinghua", "peking", "medical", "medicine", "image",
    "images", "imaging", "model", "models", "learning", "deep", "network", "networks",
    "framework", "approach", "task", "tasks", "data", "dataset", "datasets",
    "performance", "evaluation", "benchmark", "efficient",
    "название", "аннотация", "ключевые", "слова", "группа", "работ",
    "technion", "israel", "haifa", "stanford", "edu", "dept", "cse", "pes",
    "bengaluru", "india", "rwth", "aachen", "monash", "melbourne", "gmail",
    "com", "labs", "cave", "technology", "hong", "kong", "rio", "janeiro",
    "coppe", "federal", "usa", "united", "states", "massachusetts", "cambridge",
    "atlanta", "emory", "montreal", "canada", "zagreb", "croatia", "zju",
    "zhejiang", "hangzhou", "evanston", "francisco", "london", "king", "college",
    "chengdu", "cityu", "dkfz", "german", "los", "alamos", "aramco", "saudi",
    "concordia", "software", "computer", "science",
}
GENERIC_LABELS = {
    "medical", "medicine", "imaging", "image", "images", "learning", "university",
    "department", "china", "germany", "clinical", "healthcare", "multimodal",
    "vision", "model", "models", "framework", "benchmark", "technology", "research",
    "тема", "медицина", "изображения", "обучение", "университет", "модель", "модели",
}


def is_generic_label(label):
    norm = re.sub(r"[^a-zа-я0-9 ]+", " ", (label or "").lower())
    words = [w for w in norm.split() if w]
    if not words:
        return True
    if len(words) == 1 and words[0] in GENERIC_LABELS:
        return True
    generic_hits = sum(1 for w in words if w in GENERIC_LABELS or w in STOPWORDS)
    return generic_hits >= max(1, len(words) - 1)


def phrase_candidates(signals):
    text = " ".join(signals).lower()
    text = re.sub(r"[^a-zа-я0-9+ -]+", " ", text)
    tokens = [
        t for t in text.split()
        if len(t) >= 3
        and not t.isdigit()
        and not re.fullmatch(r"\d{4,}v\d+", t)
        and not re.fullmatch(r"[a-z]+\d{4,}v?\d*", t)
        and t not in STOPWORDS
    ]
    counts = Counter()
    for n in (4, 3, 2):
        for i in range(0, max(0, len(tokens) - n + 1)):
            phrase = tokens[i:i + n]
            if phrase[0] in STOPWORDS or phrase[-1] in STOPWORDS:
                continue
            if len(set(phrase)) < min(2, n):
                continue
            counts[" ".join(phrase)] += 1
    return [p for p, _ in counts.most_common(8)]


def keyword_fallback(signals):
    """Аварийный лейбл, когда LLM недоступен: строится ТОЛЬКО из данных самого
    кластера (частые фразы и слова его документов). Без доменного словаря —
    прежний regex-справочник был подогнан под конкретный датасет и на другом
    корпусе давал мусорные названия."""
    text = " ".join(signals).lower()
    phrases = phrase_candidates(signals)
    if phrases:
        label = " / ".join(p.title() for p in phrases[:2])
        return label, f"Группа работ объединена вокруг тем: {', '.join(phrases[:3])}.", phrases[:6]
    words = [w for w in re.findall(r"[А-Яа-яA-Za-z]{5,}", text) if w not in STOPWORDS]
    top = [w for w, _ in Counter(words).most_common(6)]
    label = " ".join(w.title() for w in top[:3]) if top else "Тематическая группа"
    return label, "", top


def unique_label(label, keywords, signals, used):
    if label not in used:
        used.add(label)
        return label
    for phrase in list(keywords or []) + phrase_candidates(signals):
        phrase = re.sub(r"\s+", " ", str(phrase)).strip(" .;:-")
        if len(phrase) < 8 or is_generic_label(phrase):
            continue
        suffix = readable_suffix(phrase)
        candidate = f"{label}: {suffix[:48]}"
        if candidate not in used:
            used.add(candidate)
            return candidate
    i = 2
    while f"{label} #{i}" in used:
        i += 1
    candidate = f"{label} #{i}"
    used.add(candidate)
    return candidate


def normalize_key(value):
    value = re.sub(r"[^a-zа-я0-9]+", " ", (value or "").lower())
    return re.sub(r"\s+", " ", value).strip()


def cluster_params(cluster):
    params = cluster.get("params") or {}
    if isinstance(params, str):
        try:
            params = json.loads(params)
        except Exception:  # noqa: BLE001
            params = {}
    return params if isinstance(params, dict) else {}


def stable_cluster_key(cluster):
    params = cluster_params(cluster)
    return str(params.get("fingerprint") or "").strip() or normalize_key(cluster.get("label", ""))


def first_sentence(value, limit=320):
    text = re.sub(r"\s+", " ", value or "").strip()
    if not text:
        return ""
    cut = len(text)
    for sep in (". ", "! ", "? ", "\n"):
        idx = text.find(sep)
        if idx > 24:
            cut = min(cut, idx + len(sep))
    out = text[: min(cut, limit)].strip()
    if len(text) > len(out) and not out.endswith(("...", ".", "!", "?")):
        out = out.rstrip(" ,;:") + "..."
    return out


def clamp01(value):
    return max(0.0, min(1.0, float(value)))


def corpus_stats(clusters):
    """Corpus-level stats for honest novelty scoring (pure, best-effort).

    Aggregates the whole new board so a single cluster can be weighed against it:
      * total_docs: documents across all clusters (corpus size proxy),
      * cluster_count / max_docs: spread of cluster sizes,
      * keyword_freq: how many clusters each keyword appears in (a theme shared by
        many clusters is well-trodden, a keyword unique to one cluster is rarer).
    Empty/odd input ⇒ zeroed stats, which cluster_novelty maps to a mid value.
    """
    total_docs = 0
    max_docs = 0
    count = 0
    keyword_freq = Counter()
    for c in clusters or []:
        if not isinstance(c, dict):
            continue
        count += 1
        dc = int(c.get("document_count") or 0)
        total_docs += dc
        max_docs = max(max_docs, dc)
        seen = set()
        for kw in c.get("keywords") or []:
            key = normalize_key(str(kw))
            if key and key not in seen:
                seen.add(key)
                keyword_freq[key] += 1
    return {
        "total_docs": total_docs,
        "cluster_count": count,
        "max_docs": max_docs,
        "keyword_freq": dict(keyword_freq),
    }


def cluster_novelty(cluster, stats):
    """Honest novelty in [0, 1]: rarer theme + smaller cluster ⇒ more novel.

    Replaces the old hardcoded 0.45 with a corpus-density proxy that is
    explainable, not a fake signal. Two smooth, bounded factors are averaged:

      * size_factor: 1 − (cluster docs / largest cluster's docs). A large cluster
        (a well-trodden area) drives this toward 0; the smallest clusters toward 1.
      * rarity_factor: mean over the cluster's keywords of (1 − (#clusters sharing
        that keyword / #clusters)). A theme whose keywords recur across the board
        is common (toward 0); keywords unique to this cluster are rare (toward 1).

    Defensive: a lone cluster, no stats, or no keywords collapse to 0.5 (a sane
    "unknown" mid value) rather than a misleading extreme.
    """
    stats = stats or {}
    cluster_count = int(stats.get("cluster_count") or 0)
    max_docs = int(stats.get("max_docs") or 0)
    if cluster_count <= 1 or max_docs <= 0:
        return 0.5
    doc_count = int((cluster or {}).get("document_count") or 0)
    size_factor = 1.0 - min(doc_count, max_docs) / float(max_docs)

    freq = stats.get("keyword_freq") or {}
    keys = []
    seen = set()
    for kw in (cluster or {}).get("keywords") or []:
        key = normalize_key(str(kw))
        if key and key not in seen:
            seen.add(key)
            keys.append(key)
    if keys:
        shares = [min(int(freq.get(k, 1)), cluster_count) / float(cluster_count) for k in keys]
        rarity_factor = 1.0 - sum(shares) / len(shares)
        novelty = (size_factor + rarity_factor) / 2.0
    else:
        # No keywords to judge theme rarity: lean on size, pulled toward the mid.
        novelty = (size_factor + 0.5) / 2.0
    return round(clamp01(novelty), 4)


def auto_hypothesis_payload(cluster, epoch, stats=None):
    label = cluster.get("label") or "тематический кластер"
    summary = cluster.get("summary") or f"Кластер объединяет документы по теме: {label}."
    keywords = [str(k) for k in (cluster.get("keywords") or []) if str(k).strip()]
    reps = cluster.get("representatives") or []
    if isinstance(reps, str):
        try:
            reps = json.loads(reps)
        except Exception:  # noqa: BLE001
            reps = []

    doc_count = int(cluster.get("document_count") or 0)
    chunk_count = int(cluster.get("chunk_count") or 0)
    fingerprint = str(cluster_params(cluster).get("fingerprint") or "").strip()
    label_key = normalize_key(label)
    support = first_sentence((reps[0] or {}).get("snippet", "") if reps else "", 300)
    title = label
    statement = auto_direction_statement(label, summary, keywords, support)
    rationale_parts = [
        f"Направление создано автоматически после кластеризации {doc_count} документов и {chunk_count} фрагментов.",
        summary,
    ]
    if support:
        rationale_parts.append(f"Опорный источник: {support}")

    evidence = []
    seen_docs = set()
    for rep in reps[:4]:
        doc_id = rep.get("document_id") or ""
        if doc_id and doc_id in seen_docs:
            continue
        if doc_id:
            seen_docs.add(doc_id)
        snippet = first_sentence(rep.get("snippet", ""), 520)
        if not snippet:
            continue
        evidence.append({
            "document_id": doc_id or None,
            "filename": rep.get("filename") or "",
            "snippet": snippet,
            "stance": "context",
            "score": 0.65,
            "ord": len(evidence),
        })

    confidence = clamp01(0.35 + min(doc_count, 40) / 100.0)
    value = clamp01(0.45 + min(doc_count, 30) / 100.0)
    risk = clamp01(0.65 - min(doc_count, 30) / 150.0)
    novelty = cluster_novelty(cluster, stats)
    generation = {
        "kind": "auto_cluster",
        "semantic_kind": "research_direction",
        "cluster_id": cluster.get("id", ""),
        "cluster_label": label,
        "cluster_key": fingerprint or label_key,
        "cluster_label_key": label_key,
        "cluster_fingerprint": fingerprint,
        "epoch": epoch,
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }

    return {
        "run_id": f"auto-cluster-{epoch or 'unknown'}",
        "title": title,
        "statement": statement,
        "rationale": " ".join(rationale_parts),
        "method": "cluster_kpi",
        "status": "generated",
        "primary_cluster_id": cluster.get("id"),
        "trl": 3,
        "novelty_score": novelty,
        "risk_score": risk,
        "value_score": value,
        "confidence_score": confidence,
        "composite_score": round((novelty + value + confidence + (1.0 - risk)) / 4.0, 4),
        "measurable": True,
        "function_area": label,
        "source_type": "literature",
        "tags": list(dict.fromkeys(["auto_cluster", *keywords[:6]])),
        "assessment": {
            "novelty": {"score": novelty, "rationale": "Новизна оценена по редкости темы и размеру кластера относительно корпуса."},
            "risk": {"score": risk, "rationale": "Риск снижается при большем числе источников в теме, но требует валидации."},
            "value": {"score": value, "rationale": "Ценность оценена по размеру и связности тематического кластера."},
            "confidence": {"score": confidence, "rationale": "Уверенность основана на количестве документов и representative evidence."},
            "trl": {"level": 3, "rationale": "Есть научные публикации; нужна экспериментальная проверка."},
        },
        "detail": {
            "problem_addressed": label,
            "drivers": keywords[:5] or [label],
            "cluster_document_count": doc_count,
            "cluster_chunk_count": chunk_count,
        },
        "generation": generation,
        "evidence": evidence,
        "revision": {"action": "created", "editor_id": "cluster-worker", "summary": "направление создано по кластеру"},
    }


def auto_direction_statement(label, summary, keywords, support):
    topic = f"«{label}»" if label else "этой темы"
    signal = first_sentence(summary, 260) or first_sentence(support, 260)
    clean_keywords = [k.strip() for k in keywords if k and k.strip()]
    keyword_text = ", ".join(clean_keywords[:4])
    if signal and keyword_text:
        return (
            f"Документы темы {topic} связывают признаки {keyword_text}. "
            f"Ключевой корпусный сигнал: {signal}"
        )
    if signal:
        return f"Документы темы {topic} дают корпусный сигнал: {signal}"
    if keyword_text:
        return (
            f"Тема {topic} объединяет документы по признакам {keyword_text}. "
            "Для превращения направления в гипотезу нужно выбрать KPI и проверить evidence."
        )
    return (
        f"Для темы {topic} пока недостаточно извлечённых признаков; направление требует ручной "
        "проверки перед генерацией гипотезы."
    )


def auto_hyp_key(hyp):
    try:
        generation = hyp.get("generation") or {}
        if isinstance(generation, str):
            generation = json.loads(generation)
    except Exception:  # noqa: BLE001
        generation = {}
    if generation.get("kind") != "auto_cluster":
        return ""
    return generation.get("cluster_key") or normalize_key(generation.get("cluster_label", ""))


def auto_hyp_label_key(hyp):
    try:
        generation = hyp.get("generation") or {}
        if isinstance(generation, str):
            generation = json.loads(generation)
    except Exception:  # noqa: BLE001
        generation = {}
    if generation.get("kind") != "auto_cluster":
        return ""
    return generation.get("cluster_label_key") or normalize_key(generation.get("cluster_label", ""))


def auto_hyp_rank(hyp):
    try:
        detail = hyp.get("detail") or {}
        if isinstance(detail, str):
            detail = json.loads(detail)
    except Exception:  # noqa: BLE001
        detail = {}
    return (
        int(detail.get("cluster_document_count") or 0),
        int(detail.get("cluster_chunk_count") or 0),
    )


def cluster_rank(cluster):
    return (int(cluster.get("document_count") or 0), int(cluster.get("chunk_count") or 0))


def sync_auto_hypotheses(clusters, epoch, stats=None):
    if not AUTO_HYPOTHESES or AUTO_HYPOTHESIS_LIMIT <= 0:
        return 0, 0

    clusters = sorted(clusters, key=cluster_rank, reverse=True)
    # Corpus-level stats let each hypothesis score novelty against the whole board
    # (rarer theme + smaller cluster ⇒ more novel) instead of a hardcoded constant.
    if stats is None:
        stats = corpus_stats(clusters)
    existing = gw(f"/hypotheses?limit={max(AUTO_HYPOTHESIS_LIMIT * 8, 1000)}&tags=auto_cluster")
    by_key = {}
    by_label_key = {}
    by_rank = {}
    for hyp in existing:
        key = auto_hyp_key(hyp)
        if key and key not in by_key:
            by_key[key] = hyp
        label_key = auto_hyp_label_key(hyp)
        if label_key and label_key not in by_label_key:
            by_label_key[label_key] = hyp
        rank = auto_hyp_rank(hyp)
        if rank != (0, 0) and rank not in by_rank:
            by_rank[rank] = hyp

    created = updated = 0
    used_hypotheses = set()
    fallback_existing = list(existing)
    for cluster in clusters[:AUTO_HYPOTHESIS_LIMIT]:
        key = stable_cluster_key(cluster)
        label_key = normalize_key(cluster.get("label", ""))
        if not key and not label_key:
            continue
        payload = auto_hypothesis_payload(cluster, epoch, stats)
        hyp = by_key.get(key) or by_label_key.get(label_key) or by_rank.get(cluster_rank(cluster))
        while not hyp and fallback_existing:
            candidate = fallback_existing.pop(0)
            if candidate.get("id") not in used_hypotheses:
                hyp = candidate
        if hyp and hyp.get("id") not in used_hypotheses:
            used_hypotheses.add(hyp["id"])
            payload.pop("evidence", None)
            payload.pop("revision", None)
            gw(f"/hypotheses/{hyp['id']}", payload, method="PUT")
            updated += 1
        else:
            gw("/hypotheses", payload)
            created += 1

    print(f"auto hypotheses synced: created={created} updated={updated}", flush=True)
    return created, updated


def trigger_itc_refresh(epoch):
    if not ITC_TRIGGER_KEY:
        return
    try:
        value = f"cluster-worker:{epoch or 'unknown'}:{int(time.time())}"
        redis.from_url(VALKEY_URL).set(ITC_TRIGGER_KEY, value)
        print(f"ITC refresh triggered via {ITC_TRIGGER_KEY}={value}", flush=True)
    except Exception as e:  # noqa: BLE001 — ITC is async; clustering must stay alive
        print(f"ITC trigger failed: {e}", file=sys.stderr, flush=True)


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


def readable_suffix(phrase):
    low = phrase.lower()
    suffixes = [
        (r"labels adapting|metadata|without labels", "metadata без ручной разметки"),
        (r"medcta|tool agents", "клинические tool agents"),
        (r"anomaly", "zero-shot anomaly detection"),
        (r"segmentation|u-net|sam", "сегментация SAM/U-Net"),
        (r"restoration|super-resolution|isotropic", "super-resolution и восстановление"),
        (r"compression|frugal|variable-rate", "экономный инференс и сжатие"),
    ]
    for pattern, suffix in suffixes:
        if re.search(pattern, low):
            return suffix
    return phrase


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


TOKEN = {"v": None}


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


def label_community(signals, representatives=None):
    """LLM theme label; falls back to keyword extraction when the LLM is flaky.

    `signals` are the per-document title/keywords/abstract strings from
    graph-compute. When that list is empty, the cluster representatives'
    filename+snippet are used so labelling still has something to work with; the
    keyword fallback (keyword_fallback) covers the rest.
    """
    signals = [str(s) for s in (signals or []) if str(s).strip()]
    if not signals:
        for rep in representatives or []:
            if not isinstance(rep, dict):
                continue
            text = "\n".join(p for p in (rep.get("filename", ""), rep.get("snippet", "")) if p).strip()
            if text:
                signals.append(text)
    joined = "\n\n---\n\n".join(t[:1200] for t in signals[:8])
    if VLLM_URL and VLLM_API_KEY:
        prompt = (
            "Ниже названия, ключевые слова и аннотации документов одного кластера. "
            "Сформулируй прикладную, узкую тему кластера: 4–9 слов, обязательно укажи "
            "метод/технологию и материаловедческий объект или инженерную задачу. Не используй названия "
            "университетов, стран, авторов и общие ярлыки вроде 'Medical', 'University', "
            "'Learning', 'Vision', 'Healthcare'. Резюме должно объяснять, что именно "
            "объединяет документы. Все поля пиши ТОЛЬКО на русском языке. "
            'Верни СТРОГО JSON: {"label":"","summary":"","keywords":[]}\n\n' + joined
        )
        for attempt in range(LABEL_LLM_ATTEMPTS):
            try:
                throttle_vllm()
                resp = http(
                    VLLM_URL.rstrip("/") + "/v1/chat/completions",
                    {"model": VLLM_MODEL, "messages": [{"role": "user", "content": prompt}], "temperature": 0.2},
                    token=VLLM_API_KEY,
                    timeout=LABEL_LLM_TIMEOUT,
                )
                if isinstance(resp, dict):
                    record_llm_usage(VLLM_MODEL, "cluster_label", resp.get("usage") or {})
                c = resp["choices"][0]["message"]["content"]
                obj = json.loads(c[c.index("{"): c.rindex("}") + 1])
                label = obj.get("label", "")
                if not is_generic_label(label):
                    return label, obj.get("summary", ""), obj.get("keywords", [])[:8]
                print(f"  generic label rejected: {label}", file=sys.stderr)
            except Exception as e:  # noqa: BLE001 — labelling is best-effort
                print(f"  label attempt {attempt + 1}/{LABEL_LLM_ATTEMPTS} failed: {e}", file=sys.stderr)
                time.sleep(1.5 * (attempt + 1))
    return keyword_fallback(signals)


def owner_of(docs):
    """The single owner id behind the owner-scoped /documents list, or None."""
    owners = {d.get("owner_id") for d in docs if d.get("owner_id")}
    return next(iter(owners)) if len(owners) == 1 else None


def reuse_label(cluster, prev_by_fp, prev_by_id):
    """Reuse a previous theme's label instead of paying for an LLM call, when the
    new cluster is provably the same theme: identical membership (same fingerprint)
    or a high-stability lineage link to a previous cluster. Returns
    (label, summary, keywords) to reuse, or None when a fresh label is needed.

    Conservative on purpose — a reused label must be non-generic, and lineage reuse
    needs stability >= LABEL_REUSE_STABILITY — so a reshaped cluster is re-labelled
    rather than mislabelled with a stale name."""
    if not LABEL_REUSE:
        return None
    fp = str(getattr(cluster, "fingerprint", "") or "").strip()
    prev = prev_by_fp.get(fp) if fp else None
    if prev is None and hasattr(cluster, "HasField") and cluster.HasField("lineage"):
        lin = cluster.lineage
        if lin.stability >= LABEL_REUSE_STABILITY:
            prev = prev_by_id.get(lin.previous_cluster_id)
    if not prev:
        return None
    label = prev.get("label") or ""
    if not label or is_generic_label(label):
        return None
    return label, prev.get("summary", ""), list(prev.get("keywords") or [])


def recluster(epoch=None):
    login()
    docs = gw("/documents")
    # Multi-tenant safety: /documents is owner-scoped, so cluster only within this
    # owner's corpus; forward the owner + collection so graph-compute filters Qdrant
    # server-side and the returned communities never mix tenants.
    owner_id = owner_of(docs) or ""
    document_refs = [
        graph_pb2.DocumentRef(id=d["id"], filename=d.get("filename", ""))
        for d in docs if isinstance(d, dict) and d.get("id")
    ]
    if not document_refs:
        print("no documents; skip", flush=True)
        return 0

    # Previous board feeds cluster lineage; membership comes from params["members"]
    # (written below, so the next run is exact). The load is best-effort — a failure
    # or an empty board just disables lineage for this run, as before.
    previous_clusters = []
    # Previous labels indexed for reuse: by cluster fingerprint (exact membership
    # match) and by id (lineage match). Populated from the same board load.
    prev_by_fp = {}
    prev_by_id = {}
    try:
        for c in gw("/clusters") or []:
            if not isinstance(c, dict):
                continue
            cid = str(c.get("id") or "")
            params = cluster_params(c)
            entry = {
                "label": str(c.get("label") or ""),
                "summary": str(c.get("summary") or ""),
                "keywords": [str(k) for k in (c.get("keywords") or []) if str(k).strip()],
            }
            if entry["label"]:
                fp = str(params.get("fingerprint") or "").strip()
                if fp:
                    prev_by_fp[fp] = entry
                if cid:
                    prev_by_id[cid] = entry
            members = params.get("members")
            if isinstance(members, (list, tuple)) and members:
                previous_clusters.append(graph_pb2.PreviousCluster(
                    id=cid,
                    members=[str(m) for m in members if m],
                ))
    except Exception as e:  # noqa: BLE001 — lineage is best-effort, never block publishing
        print(f"  previous board load skipped: {e}", file=sys.stderr, flush=True)

    # All clustering knobs are forwarded to graph-compute, which reproduces the old
    # in-process pipeline (Qdrant scroll -> chunk-weighted pooling -> mutual-kNN
    # graph -> Leiden -> metrics/representatives/lineage).
    request = graph_pb2.ClusterRequest(
        owner_id=owner_id,
        collection=COLLECTION,
        config=graph_pb2.ClusterConfig(
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
        documents=document_refs,
        previous_clusters=previous_clusters,
    )
    print(
        f"clustering {len(document_refs)} docs via graph-compute "
        f"({CLUSTER_ALGORITHM}) at {GRAPH_COMPUTE_ADDR}",
        flush=True,
    )
    try:
        with grpc.insecure_channel(GRAPH_COMPUTE_ADDR) as channel:
            resp = graph_pb2_grpc.GraphComputeStub(channel).Cluster(
                request, timeout=GRAPH_COMPUTE_TIMEOUT
            )
    except grpc.RpcError as e:  # surfaced to main(), which retries on the next tick
        print(f"graph-compute Cluster failed: {e.code()} {e.details()}", file=sys.stderr, flush=True)
        raise

    if not resp.clusters:
        # No community met MIN_SIZE; replacing the board now would blank it out, so
        # leave the old one up (mirrors the old "keep existing board" behavior).
        print(
            f"no clusters from graph-compute (docs={resp.stats.document_count}); keeping existing board",
            flush=True,
        )
        return 0

    cluster_payloads = []
    used_labels = set()
    total = len(resp.clusters)
    reused = relabelled = 0
    for idx, cluster in enumerate(resp.clusters):
        signals = list(cluster.signals)
        representatives = [
            {"document_id": r.document_id, "filename": r.filename, "snippet": r.snippet}
            for r in cluster.representatives
        ]
        cached = reuse_label(cluster, prev_by_fp, prev_by_id)
        if cached is not None:
            label, summary, keywords = cached
            reused += 1
        else:
            label, summary, keywords = label_community(signals, representatives)
            relabelled += 1
        label = unique_label(label, keywords, signals, used_labels)
        # Heartbeat progress so GET /system/activity shows "labelling N/total" and a
        # live updated_at — a running worker that is actually stuck stops advancing.
        if (idx + 1) % 10 == 0 or idx + 1 == total:
            report_status("running", epoch=epoch, progress_done=idx + 1, progress_total=total)
        members = list(cluster.members)
        metrics = cluster.metrics
        params = {
            "knn_k": KNN_K,
            "knn_block_size": KNN_BLOCK_SIZE,
            "resolution": RESOLUTION,
            "min_size": MIN_SIZE,
            "sim_threshold": SIM_THRESHOLD,
            "mutual_knn": MUTUAL_KNN,
            "algorithm": CLUSTER_ALGORITHM,
            "epoch": epoch,
            "fingerprint": cluster.fingerprint,
            # Full membership: enables exact Jaccard lineage on the next run.
            "members": members,
            "metrics": {
                "size": metrics.size,
                "avg_similarity": round(metrics.avg_similarity, 4),
                "modularity": round(metrics.modularity, 4),
                "modularity_contribution": round(metrics.modularity_contribution, 4),
            },
        }
        if cluster.HasField("lineage"):
            lin = cluster.lineage
            lineage = {
                "previous_cluster_id": lin.previous_cluster_id,
                "jaccard": round(lin.jaccard, 4),
                "stability": round(lin.stability, 4),
            }
            if lin.merged_from:
                lineage["merged_from"] = list(lin.merged_from)
            if lin.split_from:
                lineage["split_from"] = lin.split_from
            params["lineage"] = lineage
        cluster_payloads.append({
            "label": label, "summary": summary, "keywords": keywords, "method": "leiden-doc",
            "chunk_count": cluster.chunk_count, "document_count": len(members),
            "representatives": representatives, "params": params,
        })
        print(f"  prepared [{label}] docs={len(members)} chunks={cluster.chunk_count}", flush=True)

    print(f"labels: reused={reused} relabelled={relabelled} total={total} (LLM calls saved: {reused})", flush=True)
    created_clusters = gw("/clusters/replace", {"clusters": cluster_payloads})
    for cluster in created_clusters:
        print(
            f"  + [{cluster['label']}] docs={cluster['document_count']} chunks={cluster['chunk_count']}",
            flush=True,
        )
    print(f"created {len(created_clusters)} clusters", flush=True)
    # Stats over the whole published board feed honest novelty scoring downstream.
    sync_auto_hypotheses(created_clusters, epoch, corpus_stats(created_clusters))
    trigger_itc_refresh(epoch)
    return len(created_clusters)


def main():
    r = redis.from_url(VALKEY_URL)
    _STATUS["r"] = r
    print(f"cluster-worker up; watching {EPOCH_KEY} (debounce {DEBOUNCE_SEC}s, every {CHECK_INTERVAL}s)", flush=True)
    try:
        last_clustered = redis_int(r, LAST_CLUSTERED_KEY)
    except Exception as e:  # noqa: BLE001 — bad state should not stop the worker
        print(f"last clustered epoch read failed: {e}", file=sys.stderr, flush=True)
        last_clustered = None
    seen_epoch = last_clustered
    seen_at = time.time() if last_clustered is not None else 0.0
    if last_clustered is not None:
        print(f"last clustered epoch={last_clustered}; waiting for a newer corpus epoch", flush=True)
    while True:
        try:
            raw = r.get(EPOCH_KEY)
            epoch = int(raw) if raw else 0
            now = time.time()
            if epoch != seen_epoch:
                seen_epoch, seen_at = epoch, now  # epoch moved -> restart debounce
                print(f"epoch moved to {epoch}; waiting {DEBOUNCE_SEC}s before recluster", flush=True)
            elif epoch != last_clustered and epoch > 0 and (now - seen_at) >= DEBOUNCE_SEC and not CLUSTER_AUTORUN:
                # Auto regeneration muted: keep the service alive and idle, do not
                # recluster or call the LLM. Advance the in-memory watermark (but not
                # the persisted one, so re-enabling reclusters the latest epoch).
                print(f"auto-recluster disabled (CLUSTER_AUTORUN=false); skipping epoch {epoch}", flush=True)
                last_clustered = epoch
                report_status("idle", epoch=epoch)
            elif epoch != last_clustered and epoch > 0 and (now - seen_at) >= DEBOUNCE_SEC:
                try:
                    report_status("running", epoch=epoch)
                    recluster(epoch)
                    last_clustered = epoch
                    try:
                        r.set(LAST_CLUSTERED_KEY, epoch)
                    except Exception as e:  # noqa: BLE001 — avoid re-running in-memory loop anyway
                        print(f"last clustered epoch write failed: {e}", file=sys.stderr, flush=True)
                    report_status("idle", epoch=epoch)
                except Exception as e:  # noqa: BLE001 — keep the loop alive
                    print(f"recluster failed: {e}", file=sys.stderr, flush=True)
                    report_status("error", epoch=epoch, last_error=str(e))
        except Exception as e:  # noqa: BLE001 — Valkey/transient
            print(f"loop error: {e}", file=sys.stderr, flush=True)
        time.sleep(CHECK_INTERVAL)


def _selfcheck():
    """Offline sanity for the pure helpers (run via `python worker.py --selfcheck`)."""
    # Novelty: rarer theme + smaller cluster scores higher; defensive on edges.
    board = [
        {"document_count": 40, "keywords": ["segmentation", "ct"]},   # big, common theme
        {"document_count": 4, "keywords": ["barren plateaus"]},       # small, unique theme
        {"document_count": 20, "keywords": ["segmentation"]},         # mid, shared theme
    ]
    cs = corpus_stats(board)
    assert cs["total_docs"] == 64 and cs["cluster_count"] == 3 and cs["max_docs"] == 40
    assert cs["keyword_freq"]["segmentation"] == 2 and cs["keyword_freq"]["ct"] == 1
    nov_big, nov_small, nov_mid = (cluster_novelty(c, cs) for c in board)
    assert 0.0 <= nov_big <= nov_mid <= nov_small <= 1.0
    assert nov_small > 0.5 > nov_big  # small+rare beats large+common, around the mid
    # Single cluster / no stats / no keywords ⇒ sane mid value, never an extreme.
    assert cluster_novelty(board[0], None) == 0.5
    assert cluster_novelty(board[0], corpus_stats([board[0]])) == 0.5
    assert 0.0 <= cluster_novelty({"document_count": 4, "keywords": []}, cs) <= 1.0
    print("selfcheck ok", flush=True)


if __name__ == "__main__":
    if "--selfcheck" in sys.argv:
        _selfcheck()
    else:
        main()
