#!/usr/bin/env python3
"""RAPTOR worker — builds the hierarchical summary layer over chunks, hands-free.

Watches the corpus epoch in Valkey (bumped by main-service every time a document
finishes indexing). When the epoch moves and then holds steady for DEBOUNCE_SEC
(ingestion has settled), it rebuilds the RAPTOR tree: a set of summary nodes that
sit ABOVE the raw chunks, so retrieval can pick the right altitude — a summary
that spans many chunks for a broad question, a raw chunk for a precise one
(collapsed-tree retrieval).

Pipeline:
  poll epoch -> debounce -> graph-compute Cluster(granularity="chunk") over the
  raw chunk vectors -> per chunk cluster: LLM dense factual summary of the member
  chunk texts -> embed the summary (F2LLM) -> upsert a summary POINT into the SAME
  Qdrant collection (node_type="raptor_summary", node_level=1, member_ids=[chunk
  ids], deterministic id) -> recurse: cluster the level-1 summaries, summarise
  again (level 2), up to RAPTOR_MAX_LEVEL.

The heavy raw-chunk clustering (Qdrant scroll, blockwise mutual-kNN cosine graph,
Leiden communities) runs in graph-compute (Rust) — the same engine cluster-worker
and discovery-worker call; this worker only orchestrates and summarises. The small
upper RAPTOR layers (tens–hundreds of summary nodes) are clustered in-process with
a bounded mutual-kNN + connected-components grouping, since graph-compute only
sees the raw chunk layer (summary points carry no document_id, so its
chunk-granularity scroll skips them and level-1 stays stable across reruns).

Idempotent + stateless: a node's point id is a deterministic UUIDv5 of its level
and sorted member ids, so a rerun OVERWRITES rather than duplicates, and all state
lives in Qdrant / Valkey. Needs a generation LLM (VLLM_URL); like discovery-worker
it idles when none is configured (a templated summary would be noise, not signal).
"""
import hashlib
import json
import math
import os
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
from concurrent.futures import ThreadPoolExecutor, as_completed

import grpc
import redis
from genproto.graph.v1 import graph_pb2, graph_pb2_grpc

GW = os.environ.get("RAG_GW", "http://nginx/api/v1")
USER = os.environ.get("RAG_USER", "admin")
PASS = os.environ.get("RAG_PASS", "")
COLLECTION = os.environ.get("QDRANT_COLLECTION", "documents")
QDRANT_URL = os.environ.get("QDRANT_URL", "http://qdrant:6333")
GRAPH_COMPUTE_ADDR = os.environ.get("GRAPH_COMPUTE_ADDR", "graph-compute:9093")
GRAPH_COMPUTE_TIMEOUT = float(os.environ.get("GRAPH_COMPUTE_TIMEOUT", "600"))
GRPC_MAX_RECV_MB = max(4, int(os.environ.get("GRPC_MAX_RECV_MB", "128")))
VALKEY_URL = os.environ.get("VALKEY_URL", "redis://valkey:6379")
EPOCH_KEY = os.environ.get("EPOCH_KEY", "rag:corpus_epoch:shared")
LAST_KEY = os.environ.get("RAPTOR_LAST_KEY", "rag:raptor:last_epoch")
DOC_SIG_KEY = os.environ.get("RAPTOR_DOC_SIG_KEY", "rag:raptor:doc_signatures")
DOC_META_KEY = os.environ.get("RAPTOR_DOC_META_KEY", "rag:raptor:doc_meta")
WORKER_STATUS_KEY = "rag:worker:raptor:status"
CHECK_INTERVAL = int(os.environ.get("CHECK_INTERVAL", "60"))
DEBOUNCE_SEC = int(os.environ.get("DEBOUNCE_SEC", "120"))

# Chunk-graph knobs forwarded to graph-compute. Defaults match cluster-worker so
# the raw chunk graph is built the same way; granularity="chunk" is what makes it
# cluster raw chunk vectors (one node per Qdrant point) instead of pooled docs.
KNN_K = int(os.environ.get("KNN_K", "6"))
KNN_BLOCK_SIZE = max(64, int(os.environ.get("KNN_BLOCK_SIZE", "512")))
RESOLUTION = float(os.environ.get("RESOLUTION", "1.0"))
# A cluster smaller than this is not worth summarising (a lone chunk is its own
# summary), so it is dropped — both by graph-compute (level 1) and the in-process
# upper-level grouping.
MIN_SIZE = max(2, int(os.environ.get("MIN_SIZE", "2")))
SIM_THRESHOLD = max(0.0, min(1.0, float(os.environ.get("SIM_THRESHOLD", "0.0"))))
MUTUAL_KNN = os.environ.get("MUTUAL_KNN", "true").lower() not in {"0", "false", "no", "off"}
CLUSTER_ALGORITHM = "leiden"
CLUSTER_SEED = 42

# RAPTOR knobs.
# How many summary layers to build above the chunks (1 = chunk->summary only,
# 2 = also summary-of-summaries). "recurse 1-2 levels".
RAPTOR_MAX_LEVEL = max(1, int(os.environ.get("RAPTOR_MAX_LEVEL", "2")))
# Member chunk texts fed to the summariser per cluster (bounds the prompt size).
RAPTOR_MAX_MEMBERS_FOR_SUMMARY = max(2, int(os.environ.get("RAPTOR_MAX_MEMBERS_FOR_SUMMARY", "24")))
# Detailed leaf chunks denormalised into a summary's payload for citation
# expansion (llm-service expands a retrieved summary to these real chunks).
RAPTOR_SUMMARY_MEMBERS = max(1, int(os.environ.get("RAPTOR_SUMMARY_MEMBERS", "8")))
# Per-member stored text cap (keeps summary payloads bounded; enough for both the
# generation context and the citation snippet).
RAPTOR_MEMBER_TEXT_MAX = max(200, int(os.environ.get("RAPTOR_MEMBER_TEXT_MAX", "1200")))
# Safety cap on in-process upper-level clustering (pure-Python O(n^2)); above it a
# level is skipped rather than risking a long stall in the worker loop.
RAPTOR_INPROC_MAX_NODES = max(0, int(os.environ.get("RAPTOR_INPROC_MAX_NODES", "1500")))
QDRANT_UPSERT_BATCH = max(1, int(os.environ.get("RAPTOR_UPSERT_BATCH", "64")))
RAPTOR_MODE = os.environ.get("RAPTOR_MODE", "incremental").strip().lower()
RAPTOR_INCREMENTAL_BATCH = max(1, int(os.environ.get("RAPTOR_INCREMENTAL_BATCH", "8")))
RAPTOR_INCREMENTAL_WORKERS = max(1, int(os.environ.get("RAPTOR_INCREMENTAL_WORKERS", "4")))
RAPTOR_DOC_MAX_CHUNKS = max(2, int(os.environ.get("RAPTOR_DOC_MAX_CHUNKS", "16")))
RAPTOR_SUMMARY_MAX_TOKENS = max(64, int(os.environ.get("RAPTOR_SUMMARY_MAX_TOKENS", "220")))

EMBEDDINGS_URL = os.environ.get("EMBEDDINGS_URL", "http://f2llm-service:8085/v1/embeddings")
EMBEDDINGS_MODEL = os.environ.get("EMBEDDINGS_MODEL", "codefuse-ai/F2LLM-v2-0.6B")
VLLM_URL = os.environ.get("VLLM_URL", "")
VLLM_MODEL = os.environ.get("VLLM_MODEL", "")
VLLM_API_KEY = os.environ.get("VLLM_API_KEY", "")
VLLM_RPM = max(0, int(os.environ.get("VLLM_RPM", "18")))
AUTO_RAPTOR = os.environ.get("AUTO_RAPTOR", "true").lower() not in {"0", "false", "no", "off"}

# RAPTOR summary node tagging in the Qdrant payload (mirrors chunk-splitter's
# "chunk"/0 on the leaves). retrieval reads node_type to collapse the tree.
NODE_TYPE_SUMMARY = "raptor_summary"
# Fixed namespace seeding deterministic (UUIDv5) summary point ids. It must NEVER
# change: it is what ties a summary's id to its (level, sorted member ids) so a
# rerun overwrites the same point instead of inserting a duplicate.
RAPTOR_ID_NAMESPACE = uuid.UUID("7d3f1a9c-2e6b-4c8a-bf21-0a5d9e4c7b10")

_LAST_VLLM_CALL = {"t": 0.0}
_VLLM_LOCK = threading.Lock()
TOKEN = {"v": None}


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


def qdrant_scroll(filter_body=None, with_vector=False, with_payload=True, limit=256):
    """Scroll Qdrant points; filter_body is the raw Qdrant filter object."""
    url = f"{QDRANT_URL.rstrip('/')}/collections/{COLLECTION}/points/scroll"
    points, offset = [], None
    while True:
        body = {
            "limit": max(1, int(limit)),
            "with_vector": bool(with_vector),
            "with_payload": bool(with_payload),
        }
        if filter_body:
            body["filter"] = filter_body
        if offset is not None:
            body["offset"] = offset
        resp = http(url, body, timeout=120)
        result = resp.get("result") or {}
        batch = result.get("points") or []
        if isinstance(batch, list):
            points.extend(batch)
        offset = result.get("next_page_offset")
        if offset is None:
            return points


def throttle_vllm():
    if VLLM_RPM <= 0:
        return
    with _VLLM_LOCK:
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


def fingerprint(ids):
    """Deterministic sha1[:16] of the sorted member ids (a cluster's identity)."""
    joined = "\n".join(sorted(str(i) for i in ids))
    return hashlib.sha1(joined.encode("utf-8")).hexdigest()[:16]


def summary_point_id(level, fp):
    """Stable UUIDv5 point id for a summary node from its level + fingerprint.

    Pure function of (level, fingerprint) so the same cluster membership always
    maps to the same Qdrant id — a rerun OVERWRITES the node instead of inserting
    a duplicate. Level is part of the name so a chunk cluster and a same-membership
    higher node never collide.
    """
    return str(uuid.uuid5(RAPTOR_ID_NAMESPACE, f"{level}:{fp}"))


def embed(text):
    """Embed one text via the F2LLM endpoint; vector or None on a bad shape."""
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


def summarize(texts):
    """LLM dense factual summary of a cluster's texts (Russian), or "" on failure.

    No fallback: a templated summary would just be noise that pollutes retrieval,
    so when the LLM is unavailable the node is skipped (the caller logs it).
    """
    texts = [t.strip() for t in texts if t and t.strip()]
    if not VLLM_URL or len(texts) < 2:
        return ""
    joined = "\n\n---\n\n".join(
        t[:RAPTOR_MEMBER_TEXT_MAX] for t in texts[:RAPTOR_MAX_MEMBERS_FOR_SUMMARY]
    )
    prompt = (
        "Ниже фрагменты из набора документов одного тематического кластера. "
        "Напиши ПЛОТНОЕ фактологическое резюме на русском языке (4–8 предложений), "
        "которое охватывает ключевые факты, методы, объекты и выводы всех фрагментов. "
        "Не добавляй вступлений, оценок и общих слов — только содержательная выжимка, "
        "по которой этот кластер можно найти семантическим поиском. "
        "Верни ТОЛЬКО текст резюме, без заголовков и списков.\n\n" + joined
    )
    for attempt in range(3):
        try:
            throttle_vllm()
            resp = http(
                VLLM_URL.rstrip("/") + "/v1/chat/completions",
                {
                    "model": VLLM_MODEL,
                    "messages": [{"role": "user", "content": prompt}],
                    "temperature": 0.2,
                    "max_tokens": RAPTOR_SUMMARY_MAX_TOKENS,
                },
                token=VLLM_API_KEY,
            )
            if isinstance(resp, dict):
                record_llm_usage(VLLM_MODEL, "raptor_summary", resp.get("usage") or {})
            text = str(resp["choices"][0]["message"]["content"]).strip()
            if len(text) >= 40:
                return text
            print(f"  summary too short, retrying (attempt {attempt + 1}/3)", file=sys.stderr)
        except Exception as e:  # noqa: BLE001 — summarisation is best-effort; retried then skipped
            print(f"  summary attempt {attempt + 1}/3 failed: {e}", file=sys.stderr)
            time.sleep(1.5 * (attempt + 1))
    return ""


def qdrant_retrieve(ids):
    """Fetch points (payload only) by id from Qdrant; [] for an empty/failed read."""
    ids = [i for i in ids if i]
    if not ids:
        return []
    url = f"{QDRANT_URL.rstrip('/')}/collections/{COLLECTION}/points"
    resp = http(url, {"ids": ids, "with_payload": True, "with_vector": False}, timeout=120)
    result = resp.get("result")
    return result if isinstance(result, list) else []


def qdrant_upsert(points):
    """Upsert summary points into Qdrant, waiting for the write to apply."""
    if not points:
        return
    url = f"{QDRANT_URL.rstrip('/')}/collections/{COLLECTION}/points?wait=true"
    for start in range(0, len(points), QDRANT_UPSERT_BATCH):
        batch = points[start:start + QDRANT_UPSERT_BATCH]
        http(url, {"points": batch}, method="PUT", timeout=180)


def member_view(point):
    """One leaf chunk's citation view from its Qdrant payload (skip if no text)."""
    payload = point.get("payload") or {}
    text = str(payload.get("text") or "").strip()
    if not text:
        return None
    return {
        "id": str(point.get("id") or ""),
        "document_id": str(payload.get("document_id") or ""),
        "filename": str(payload.get("filename") or ""),
        "text": text[:RAPTOR_MEMBER_TEXT_MAX],
        "chunk_index": int(payload.get("chunk_index") or 0),
        "char_start": int(payload.get("char_start") or 0),
        "char_end": int(payload.get("char_end") or 0),
        "page_start": int(payload.get("page_start") or 0),
        "page_end": int(payload.get("page_end") or 0),
        "section_heading": str(payload.get("section_heading") or ""),
    }


def chunk_sort_key(point):
    payload = point.get("payload") or {}
    try:
        idx = int(payload.get("chunk_index") or 0)
    except (TypeError, ValueError):
        idx = 1_000_000
    return idx, str(point.get("id") or "")


def fetch_doc_chunks(document_id):
    """Fetch raw chunk points for one document, ordered by chunk_index."""
    if not document_id:
        return []
    points = qdrant_scroll(
        {
            "must": [
                {"key": "document_id", "match": {"value": document_id}},
                {"key": "node_type", "match": {"value": "chunk"}},
            ]
        },
        with_vector=False,
        with_payload=True,
    )
    points = [p for p in points if str(p.get("id") or "")]
    points.sort(key=chunk_sort_key)
    return points


def doc_signature(doc, chunks):
    """Signature for deciding whether a per-document summary is still current."""
    h = hashlib.sha1()
    for value in (
        str(doc.get("id") or ""),
        str(doc.get("updated_at") or ""),
        str(doc.get("chunk_count") or ""),
        str(len(chunks)),
    ):
        h.update(value.encode("utf-8"))
        h.update(b"\0")
    for point in chunks:
        h.update(str(point.get("id") or "").encode("utf-8"))
        h.update(b"\0")
    return h.hexdigest()


def doc_meta_signature(doc):
    """Cheap document-level signature used to avoid re-scrolling unchanged chunks."""
    h = hashlib.sha1()
    for value in (
        str(doc.get("id") or ""),
        str(doc.get("updated_at") or ""),
        str(doc.get("chunk_count") or ""),
    ):
        h.update(value.encode("utf-8"))
        h.update(b"\0")
    return h.hexdigest()


def select_doc_summary_points(chunks):
    """Pick bounded, deterministic document chunks for an incremental summary."""
    if len(chunks) <= RAPTOR_DOC_MAX_CHUNKS:
        return chunks
    lead = max(1, RAPTOR_DOC_MAX_CHUNKS // 2)
    tail = RAPTOR_DOC_MAX_CHUNKS - lead
    picked = chunks[:lead] + (chunks[-tail:] if tail else [])
    seen, out = set(), []
    for point in picked:
        pid = str(point.get("id") or "")
        if pid and pid not in seen:
            seen.add(pid)
            out.append(point)
    return out


def build_doc_summary(doc, chunks, epoch):
    """Build one stable per-document summary point without global chunk clustering."""
    selected = select_doc_summary_points(chunks)
    members = [member_view(point) for point in selected]
    members = [m for m in members if m]
    if len(members) < MIN_SIZE:
        return None
    prompt_texts = [m["text"] for m in members[:RAPTOR_MAX_MEMBERS_FOR_SUMMARY]]
    summary = summarize(prompt_texts)
    if not summary:
        return None
    vector = embed(summary)
    if not vector:
        print("  embedding failed; skipping document summary", file=sys.stderr)
        return None

    child_ids = [m["id"] for m in members]
    fp = fingerprint([str(doc.get("id") or ""), *child_ids])
    point_id = summary_point_id(1, "doc:" + fp)
    owner_id = str(doc.get("owner_id") or "")
    payload = {
        "node_type": NODE_TYPE_SUMMARY,
        "node_level": 1,
        "owner_id": owner_id,
        "source_document_id": str(doc.get("id") or ""),
        "source_filename": str(doc.get("filename") or ""),
        "text": summary,
        "fingerprint": fp,
        "member_ids": child_ids,
        "member_count": len(chunks),
        "members": members[:RAPTOR_SUMMARY_MEMBERS],
        "epoch": epoch,
        "incremental": True,
    }
    return {"id": point_id, "vector": vector, "payload": payload}


def build_doc_summary_candidate(doc, chunks, sig, meta_sig, epoch):
    """Build one incremental candidate in a worker thread."""
    point = build_doc_summary(doc, chunks, epoch)
    return str(doc.get("id") or ""), sig, meta_sig, len(chunks), point


def fetch_members(leaf_ids):
    """Citation views for up to RAPTOR_SUMMARY_MEMBERS leaf chunks, in id order."""
    points = {str(p.get("id")): p for p in qdrant_retrieve(leaf_ids[:RAPTOR_SUMMARY_MEMBERS])}
    members = []
    for leaf_id in leaf_ids[:RAPTOR_SUMMARY_MEMBERS]:
        point = points.get(str(leaf_id))
        view = member_view(point) if point else None
        if view:
            members.append(view)
    return members


def build_summary_node(level, child_ids, leaf_ids, prompt_texts, owner_id, epoch):
    """Summarise one cluster into a node + its Qdrant point, or (None, None).

    child_ids — the cluster's direct children (chunk ids at level 1, level-(L-1)
    summary ids above): the structural member_ids and the node's identity.
    leaf_ids  — the real chunk leaves under this node (== child_ids at level 1, the
    union of the children's leaves above): what the node cites and what level L+1
    aggregates. prompt_texts — texts fed to the summariser; when None (level 1) the
    leaf chunk texts are fetched and used (one fetch serves both prompt + citation).
    """
    if prompt_texts is None:
        wide = max(RAPTOR_MAX_MEMBERS_FOR_SUMMARY, RAPTOR_SUMMARY_MEMBERS)
        points = {str(p.get("id")): p for p in qdrant_retrieve(leaf_ids[:wide])}
        ordered = [member_view(points[str(i)]) for i in leaf_ids[:wide] if str(i) in points]
        ordered = [m for m in ordered if m]
        prompt_texts = [m["text"] for m in ordered[:RAPTOR_MAX_MEMBERS_FOR_SUMMARY]]
        members = ordered[:RAPTOR_SUMMARY_MEMBERS]
    else:
        members = fetch_members(leaf_ids)

    summary = summarize(prompt_texts)
    if not summary:
        return None, None
    vector = embed(summary)
    if not vector:
        print("  embedding failed; skipping summary node", file=sys.stderr)
        return None, None

    fp = fingerprint(child_ids)
    point_id = summary_point_id(level, fp)
    payload = {
        "node_type": NODE_TYPE_SUMMARY,
        "node_level": level,
        "owner_id": owner_id,
        # No document_id: a summary spans many documents, and its absence is what
        # makes graph-compute's chunk-granularity scroll skip summaries (it requires
        # document_id), so level-1 stays the raw chunk layer across reruns.
        "text": summary,
        "fingerprint": fp,
        "member_ids": [str(i) for i in child_ids],
        "member_count": len(leaf_ids),
        # Denormalised leaf chunks for citation (small-to-big) — always real chunks,
        # even above level 1, so a retrieved summary expands to actual passages.
        "members": members,
        "epoch": epoch,
    }
    point = {"id": point_id, "vector": vector, "payload": payload}
    node = {"id": point_id, "vector": vector, "text": summary, "leaf_ids": list(leaf_ids)}
    return node, point


def _normalize(vec):
    norm = math.sqrt(sum(float(x) * float(x) for x in vec))
    if norm == 0.0 or not math.isfinite(norm):
        return [float(x) for x in vec]
    return [float(x) / norm for x in vec]


def _cosine(a, b):
    return sum(x * y for x, y in zip(a, b))


def cluster_vectors(nodes, k=None):
    """Bounded in-process clustering of upper-level summary nodes.

    A mutual-kNN cosine graph (the same shape graph-compute builds for the raw
    layer, but tiny here) reduced to connected components; groups below MIN_SIZE
    are dropped. Pure and deterministic. Returns index groups, largest first.
    Empty when there is nothing to merge or the node count exceeds the safety cap.
    `k` defaults to KNN_K (the neighbour count); it is overridable for testing.
    """
    n = len(nodes)
    if n < MIN_SIZE or (RAPTOR_INPROC_MAX_NODES and n > RAPTOR_INPROC_MAX_NODES):
        return []
    norm = [_normalize(node["vector"]) for node in nodes]
    k = max(1, min(KNN_K if k is None else k, n - 1))
    topk = []
    for i in range(n):
        sims = [(_cosine(norm[i], norm[j]), j) for j in range(n) if j != i]
        sims = [(s, j) for s, j in sims if s >= SIM_THRESHOLD]
        sims.sort(key=lambda pair: (-pair[0], pair[1]))
        topk.append({j for _, j in sims[:k]})

    parent = list(range(n))

    def find(x):
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    for i in range(n):
        for j in topk[i]:
            if not MUTUAL_KNN or i in topk[j]:
                ri, rj = find(i), find(j)
                if ri != rj:
                    parent[ri] = rj

    groups = {}
    for i in range(n):
        groups.setdefault(find(i), []).append(i)
    out = [sorted(g) for g in groups.values() if len(g) >= MIN_SIZE]
    out.sort(key=lambda g: (-len(g), g[0]))
    return out


def cluster_chunks(owner_id, document_refs):
    """graph-compute Cluster(granularity="chunk") over the raw chunk vectors."""
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
            algorithm=CLUSTER_ALGORITHM,
            seed=CLUSTER_SEED,
            granularity="chunk",
        ),
        documents=document_refs,
    )
    try:
        grpc_limit = GRPC_MAX_RECV_MB * 1024 * 1024
        with grpc.insecure_channel(
            GRAPH_COMPUTE_ADDR,
            options=[
                ("grpc.max_receive_message_length", grpc_limit),
                ("grpc.max_send_message_length", grpc_limit),
            ],
        ) as channel:
            return graph_pb2_grpc.GraphComputeStub(channel).Cluster(
                request, timeout=GRAPH_COMPUTE_TIMEOUT
            )
    except grpc.RpcError as e:  # surfaced to main(), which retries on the next tick
        print(f"graph-compute Cluster failed: {e.code()} {e.details()}", file=sys.stderr, flush=True)
        raise


def build_level1(clusters, owner_id, epoch):
    """Summarise each raw chunk cluster into a level-1 summary node + point."""
    nodes, points = [], []
    for cluster in clusters:
        members = [str(m) for m in cluster.members if m]
        if len(members) < MIN_SIZE:
            continue
        node, point = build_summary_node(1, members, members, None, owner_id, epoch)
        if node:
            nodes.append(node)
            points.append(point)
            print(f"  L1 summary over {len(members)} chunks -> {node['id']}", flush=True)
    return nodes, points


def build_upper_level(level, prev_nodes, owner_id, epoch):
    """Cluster the previous level's summary nodes and summarise each group."""
    groups = cluster_vectors(prev_nodes)
    nodes, points = [], []
    for group in groups:
        children = [prev_nodes[i] for i in group]
        child_ids = [c["id"] for c in children]
        leaf_ids = _dedup([leaf for c in children for leaf in c["leaf_ids"]])
        prompt_texts = [c["text"] for c in children]
        node, point = build_summary_node(level, child_ids, leaf_ids, prompt_texts, owner_id, epoch)
        if node:
            nodes.append(node)
            points.append(point)
            print(
                f"  L{level} summary over {len(child_ids)} nodes / {len(leaf_ids)} leaves "
                f"-> {node['id']}",
                flush=True,
            )
    return nodes, points


def incremental_raptor(r, epoch=None):
    """Incrementally build per-document RAPTOR summary overlay.

    This is the online-safe path: no global chunk graph, no all-pairs KNN. Each
    tick processes a bounded batch of indexed documents whose chunk signature has
    changed since the last successful summary.
    """
    if not VLLM_URL:
        print("VLLM_URL is empty — incremental RAPTOR needs an LLM; skipping", flush=True)
        return True
    login()
    docs = [
        d for d in gw("/documents")
        if isinstance(d, dict) and d.get("id") and d.get("status") == "indexed"
    ]
    # New/changed documents must jump ahead of any offline backfill backlog so
    # upload -> summary stays within the interactive SLA.
    docs.sort(key=lambda d: (str(d.get("updated_at") or ""), str(d.get("id") or "")), reverse=True)

    skipped, pending = 0, 0
    candidates = []
    for doc in docs:
        if len(candidates) >= RAPTOR_INCREMENTAL_BATCH:
            pending += 1
            continue
        doc_id = str(doc.get("id") or "")
        meta_sig = doc_meta_signature(doc)
        old = r.hget(DOC_SIG_KEY, doc_id)
        old = old.decode("utf-8") if isinstance(old, bytes) else old
        old_meta = r.hget(DOC_META_KEY, doc_id)
        old_meta = old_meta.decode("utf-8") if isinstance(old_meta, bytes) else old_meta
        if old and old_meta == meta_sig:
            skipped += 1
            continue

        chunks = fetch_doc_chunks(doc_id)
        sig = doc_signature(doc, chunks)
        if old == sig:
            r.hset(DOC_META_KEY, doc_id, meta_sig)
            skipped += 1
            continue

        candidates.append((doc, chunks, sig, meta_sig))

    processed, failed = 0, 0
    # Known outstanding work this epoch: this tick's batch + docs beyond the cap.
    backlog = len(candidates) + pending
    workers = min(RAPTOR_INCREMENTAL_WORKERS, len(candidates))
    if workers > 1:
        print(
            f"incremental raptor: building {len(candidates)} candidates with {workers} workers",
            flush=True,
        )
    if candidates:
        report_status("running", epoch=epoch, progress_done=0, progress_total=backlog)
        with ThreadPoolExecutor(max_workers=max(1, workers)) as pool:
            futures = [
                pool.submit(build_doc_summary_candidate, doc, chunks, sig, meta_sig, epoch)
                for doc, chunks, sig, meta_sig in candidates
            ]
            for future in as_completed(futures):
                try:
                    doc_id, sig, meta_sig, chunk_count, point = future.result()
                except Exception as e:  # noqa: BLE001 — leave it unsigned so the next tick retries
                    failed += 1
                    print(f"incremental summary failed: {e}", file=sys.stderr, flush=True)
                    report_status("running", epoch=epoch, progress_done=processed + failed, progress_total=backlog)
                    continue
                if point:
                    qdrant_upsert([point])
                    print(
                        f"incremental summary: {doc_id} chunks={chunk_count} -> {point['id']}",
                        flush=True,
                    )
                else:
                    print(f"incremental summary skipped: {doc_id} chunks={chunk_count}", flush=True)
                r.hset(DOC_SIG_KEY, doc_id, sig)
                r.hset(DOC_META_KEY, doc_id, meta_sig)
                processed += 1
                report_status("running", epoch=epoch, progress_done=processed + failed, progress_total=backlog)

    remaining = max(0, len(docs) - skipped - processed)
    print(
        f"incremental raptor tick: processed={processed} skipped={skipped} "
        f"failed={failed} pending={pending} remaining={remaining}",
        flush=True,
    )
    return remaining == 0


def _dedup(items):
    """Order-preserving de-duplication of a string id list."""
    seen, out = set(), []
    for item in items:
        if item not in seen:
            seen.add(item)
            out.append(item)
    return out


def raptor(epoch=None):
    if not VLLM_URL:
        print("VLLM_URL is empty — RAPTOR needs an LLM to summarise; skipping", flush=True)
        return 0
    login()
    docs = gw("/documents")
    # /documents is owner-scoped, so build the tree within this owner's corpus;
    # forward owner + the document list so graph-compute filters Qdrant the same way
    # cluster-worker does (the empty owner == the shared corpus).
    owner_id = owner_of(docs) or ""
    document_refs = [
        graph_pb2.DocumentRef(id=d["id"], filename=d.get("filename", ""))
        for d in docs if isinstance(d, dict) and d.get("id")
    ]
    if not document_refs:
        print("no documents; skip", flush=True)
        return 0

    print(
        f"raptor: clustering chunks of {len(document_refs)} docs via graph-compute "
        f"(granularity=chunk) at {GRAPH_COMPUTE_ADDR}",
        flush=True,
    )
    resp = cluster_chunks(owner_id, document_refs)
    if not resp.clusters:
        print(f"no chunk clusters (docs={resp.stats.document_count}); nothing to summarise", flush=True)
        return 0

    total = 0
    nodes, points = build_level1(list(resp.clusters), owner_id, epoch)
    qdrant_upsert(points)
    total += len(points)
    print(f"level 1: {len(points)} summary nodes", flush=True)

    level = 2
    while level <= RAPTOR_MAX_LEVEL and len(nodes) >= MIN_SIZE:
        nodes, points = build_upper_level(level, nodes, owner_id, epoch)
        if not points:
            break
        qdrant_upsert(points)
        total += len(points)
        print(f"level {level}: {len(points)} summary nodes", flush=True)
        level += 1

    print(f"raptor done: {total} summary nodes across {min(level - 1, RAPTOR_MAX_LEVEL)} levels", flush=True)
    return total


def main():
    r = redis.from_url(VALKEY_URL)
    _STATUS["r"] = r
    print(
        f"raptor-worker up; mode={RAPTOR_MODE}; watching {EPOCH_KEY} "
        f"(debounce {DEBOUNCE_SEC}s, every {CHECK_INTERVAL}s)",
        flush=True,
    )
    if not AUTO_RAPTOR:
        print("AUTO_RAPTOR disabled; idling", flush=True)
    try:
        last = redis_int(r, LAST_KEY)
    except Exception as e:  # noqa: BLE001 — bad state should not stop the worker
        print(f"last raptor epoch read failed: {e}", file=sys.stderr, flush=True)
        last = None
    seen_epoch = last
    seen_at = time.time() if last is not None else 0.0
    if last is not None:
        print(f"last raptor epoch={last}; waiting for a newer corpus epoch", flush=True)
    while True:
        try:
            if not AUTO_RAPTOR:
                time.sleep(CHECK_INTERVAL)
                continue
            raw = r.get(EPOCH_KEY)
            epoch = int(raw) if raw else 0
            now = time.time()
            if epoch != seen_epoch:
                seen_epoch, seen_at = epoch, now  # epoch moved -> restart debounce
                print(f"epoch moved to {epoch}; waiting {DEBOUNCE_SEC}s before raptor", flush=True)
            elif epoch != last and epoch > 0 and (now - seen_at) >= DEBOUNCE_SEC:
                try:
                    report_status("running", epoch=epoch)
                    if RAPTOR_MODE == "incremental":
                        complete = incremental_raptor(r, epoch)
                    else:
                        raptor(epoch)
                        complete = True
                    if complete:
                        last = epoch
                        try:
                            r.set(LAST_KEY, epoch)
                        except Exception as e:  # noqa: BLE001 — avoid re-running in-memory loop anyway
                            print(f"last raptor epoch write failed: {e}", file=sys.stderr, flush=True)
                        report_status("idle", epoch=epoch)
                except Exception as e:  # noqa: BLE001 — keep the loop alive
                    print(f"raptor failed: {e}", file=sys.stderr, flush=True)
                    report_status("error", epoch=epoch, last_error=str(e))
        except Exception as e:  # noqa: BLE001 — Valkey/transient
            print(f"loop error: {e}", file=sys.stderr, flush=True)
        time.sleep(CHECK_INTERVAL)


def _selfcheck():
    """Offline sanity for the pure helpers (run via `python worker.py --selfcheck`)."""
    # The proto contract this worker is built on must resolve and carry granularity.
    req = graph_pb2.ClusterRequest(
        owner_id="o", collection="documents",
        config=graph_pb2.ClusterConfig(granularity="chunk"),
    )
    assert req.config.granularity == "chunk"

    # Fingerprint is order-independent; the point id is deterministic and
    # level-scoped (same members at different levels never collide).
    assert fingerprint(["b", "a"]) == fingerprint(["a", "b"])
    assert fingerprint(["a"]) != fingerprint(["b"])
    pid1 = summary_point_id(1, fingerprint(["a", "b"]))
    assert pid1 == summary_point_id(1, fingerprint(["b", "a"]))
    assert pid1 != summary_point_id(2, fingerprint(["a", "b"]))
    uuid.UUID(pid1)  # a valid Qdrant point id

    # Cosine + normalize.
    assert abs(_cosine(_normalize([1.0, 0.0]), _normalize([2.0, 0.0])) - 1.0) < 1e-9
    assert abs(_cosine(_normalize([1.0, 0.0]), _normalize([0.0, 1.0]))) < 1e-9
    assert _normalize([0.0, 0.0]) == [0.0, 0.0]

    # In-process clustering: two tight groups around [1,0] and [0,1] split cleanly
    # (k=2 so each node links only to its two same-group neighbours).
    nodes = [
        {"vector": [1.0, 0.0]}, {"vector": [0.98, 0.02]}, {"vector": [0.95, 0.05]},
        {"vector": [0.0, 1.0]}, {"vector": [0.02, 0.98]}, {"vector": [0.05, 0.95]},
    ]
    groups = cluster_vectors(nodes, k=2)
    assert len(groups) == 2, groups
    assert all(len(g) == 3 for g in groups)
    assert sorted(i for g in groups for i in g) == [0, 1, 2, 3, 4, 5]
    # Below MIN_SIZE / over the cap ⇒ nothing.
    assert cluster_vectors(nodes[:1]) == []

    # member_view shapes a leaf payload and drops textless points.
    view = member_view({"id": "c1", "payload": {"text": " hello ", "document_id": "d1", "filename": "a.pdf", "chunk_index": 3}})
    assert view["id"] == "c1" and view["text"] == "hello" and view["chunk_index"] == 3
    assert member_view({"id": "c2", "payload": {"text": "  "}}) is None

    # Incremental document summaries: bounded lead/tail selection, stable
    # signatures, and no synthetic summary for a single leaf.
    chunks = [
        {"id": f"c{i}", "payload": {"text": f"text {i}", "document_id": "d1", "filename": "a.pdf", "chunk_index": i}}
        for i in range(20)
    ]
    picked = select_doc_summary_points(chunks)
    assert len(picked) == RAPTOR_DOC_MAX_CHUNKS
    assert [p["id"] for p in picked] == [f"c{i}" for i in range(8)] + [f"c{i}" for i in range(12, 20)]
    doc = {"id": "d1", "owner_id": "u1", "filename": "a.pdf", "updated_at": "t1", "chunk_count": 20}
    sig1 = doc_signature(doc, chunks)
    sig2 = doc_signature(doc, chunks[:-1] + [{"id": "cx", "payload": chunks[-1]["payload"]}])
    assert sig1 != sig2
    assert doc_meta_signature(doc) == doc_meta_signature(dict(doc))
    assert build_doc_summary(doc, chunks[:1], 1080) is None

    old_summarize, old_embed = globals()["summarize"], globals()["embed"]
    try:
        globals()["summarize"] = lambda texts: "summary text over " + str(len(texts)) + " chunks"
        globals()["embed"] = lambda text: [0.1, 0.2, 0.3]
        point = build_doc_summary(doc, chunks, 1080)
        assert point and point["vector"] == [0.1, 0.2, 0.3]
        payload = point["payload"]
        assert payload["node_type"] == NODE_TYPE_SUMMARY
        assert payload["source_document_id"] == "d1"
        assert "document_id" not in payload
        assert payload["member_count"] == 20
        assert len(payload["members"]) == min(RAPTOR_SUMMARY_MEMBERS, RAPTOR_DOC_MAX_CHUNKS)
        uuid.UUID(point["id"])
    finally:
        globals()["summarize"], globals()["embed"] = old_summarize, old_embed

    # _dedup keeps first-seen order.
    assert _dedup(["a", "b", "a", "c", "b"]) == ["a", "b", "c"]
    print("selfcheck ok", flush=True)


if __name__ == "__main__":
    if "--selfcheck" in sys.argv:
        _selfcheck()
    else:
        main()
