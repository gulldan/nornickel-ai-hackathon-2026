"""OpenAI/Cohere-style rerank service for local cross-encoder reranker models.

The llm-service calls POST /v1/rerank with:
  {"model": "...", "query": "...", "documents": ["..."]}

The service scores each (query, document) pair directly. Single-logit sequence
classifiers are mapped through sigmoid; multi-logit classifiers use the last
class as the positive/relevant class via softmax. Original Qwen3-Reranker models
are converted into deterministic yes/no relevance scorers. The response keeps
both shapes the client accepts:
  {"scores": [...], "results": [{"index": i, "relevance_score": score, "document": {...}}, ...]}
"""

from __future__ import annotations

import os
import sys
import threading
from typing import Union

import torch
from fastapi import FastAPI
from pydantic import BaseModel
from transformers import AutoConfig, AutoModelForSequenceClassification, AutoTokenizer


MODEL_NAME = os.environ.get("RERANKER_MODEL", "Qwen/Qwen3-Reranker-0.6B")
MAX_TOKENS = int(os.environ.get("RERANKER_MAX_TOKENS", "4096"))
BATCH_SIZE = max(1, int(os.environ.get("RERANKER_BATCH", "8")))
DEFAULT_INSTRUCTION = os.environ.get(
    "RERANKER_INSTRUCTION",
    "Given a web search query, retrieve relevant passages that answer the query",
)
QWEN_SYSTEM = (
    "Judge whether the Document meets the requirements based on the Query and "
    'the Instruct provided. Note that the answer can only be "yes" or "no".'
)


def _device() -> str:
    configured = os.environ.get("TORCH_DEVICE", "auto").strip().lower()
    if configured and configured != "auto":
        return configured
    if torch.cuda.is_available():
        return "cuda"
    if getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
        return "mps"
    return "cpu"


DEVICE = _device()
DTYPE = torch.float16 if DEVICE == "cuda" else torch.float32


def _is_qwen3_reranker(config) -> bool:
    arches = {str(a).lower() for a in getattr(config, "architectures", []) or []}
    return "qwen3-reranker" in MODEL_NAME.lower() or "qwen3forcausallm" in arches


def _ensure_pad_token(tok):
    if tok.pad_token is not None:
        return tok
    fallback_pad = tok.eos_token or tok.sep_token or tok.unk_token
    if fallback_pad is not None:
        tok.pad_token = fallback_pad
    else:
        tok.add_special_tokens({"pad_token": "[PAD]"})
    return tok


def _format_qwen_instruction(instruction: str | None, query: str, doc: str) -> str:
    return "<Instruct>: {instruction}\n<Query>: {query}\n<Document>: {doc}".format(
        instruction=instruction or DEFAULT_INSTRUCTION,
        query=query,
        doc=doc,
    )


def _score_from_logits(logits: torch.Tensor, batch_len: int) -> torch.Tensor:
    """Return exactly one 0..1 relevance score per input document."""
    logits = logits.float()
    if logits.ndim == 1:
        logits = logits.view(batch_len, -1)
    elif logits.shape[0] != batch_len:
        logits = logits.view(batch_len, -1)
    if logits.shape[1] == 1:
        return torch.sigmoid(logits[:, 0])
    return torch.softmax(logits, dim=-1)[:, -1]


def _init_qwen_yes_no_head(tok, mdl) -> None:
    """Convert original Qwen3-Reranker LM token scoring into a classifier head."""
    yes_id = tok.convert_tokens_to_ids("yes")
    no_id = tok.convert_tokens_to_ids("no")
    if yes_id is None or no_id is None or yes_id < 0 or no_id < 0:
        raise RuntimeError("Qwen reranker tokenizer must contain single-token yes/no ids")
    embeddings = mdl.get_input_embeddings().weight.detach()
    vector = (embeddings[yes_id] - embeddings[no_id]).to(dtype=mdl.score.weight.dtype, device=mdl.score.weight.device)
    with torch.no_grad():
        if mdl.score.weight.shape[0] != 1:
            raise RuntimeError(f"Qwen reranker score head has shape {tuple(mdl.score.weight.shape)}, want one label")
        mdl.score.weight.copy_(vector.view(1, -1))
        if getattr(mdl.score, "bias", None) is not None:
            mdl.score.bias.zero_()
    mdl.config.pad_token_id = tok.pad_token_id
    mdl.config.num_labels = 1


def _load_model():
    config = AutoConfig.from_pretrained(MODEL_NAME)
    qwen_reranker = _is_qwen3_reranker(config)
    tokenizer_kwargs = {"padding_side": "left"} if qwen_reranker else {}
    tok = _ensure_pad_token(AutoTokenizer.from_pretrained(MODEL_NAME, **tokenizer_kwargs))
    kwargs = {"torch_dtype": DTYPE}
    if qwen_reranker:
        kwargs.update({"num_labels": 1, "ignore_mismatched_sizes": True})
    mdl = AutoModelForSequenceClassification.from_pretrained(MODEL_NAME, **kwargs).to(DEVICE).eval()
    if tok.pad_token_id is not None:
        mdl.config.pad_token_id = tok.pad_token_id
    if len(tok) > mdl.get_input_embeddings().weight.shape[0]:
        mdl.resize_token_embeddings(len(tok))
    if qwen_reranker:
        _init_qwen_yes_no_head(tok, mdl)
    return tok, mdl


tokenizer = None
model = None
if "--selfcheck" not in sys.argv:
    tokenizer, model = _load_model()

app = FastAPI(title="reranker-service")

# HF-токенизатор не потокобезопасен ("Already borrowed" под тредпулом FastAPI).
_INFER_LOCK = threading.Lock()


class RerankRequest(BaseModel):
    query: str
    documents: list[str]
    model: Union[str, None] = None
    top_n: Union[int, None] = None
    # Accepted for API compatibility with OpenAI/Cohere/Qwen-style clients; the
    # local cross-encoder request shape ignores it.
    instruction: Union[str, None] = None


@torch.no_grad()
def _score(query: str, documents: list[str], instruction: str | None = None) -> list[float]:
    global tokenizer, model
    if tokenizer is None or model is None:
        tokenizer, model = _load_model()
    qwen_reranker = _is_qwen3_reranker(model.config)
    scores: list[float] = []
    for start in range(0, len(documents), BATCH_SIZE):
        batch_docs = documents[start : start + BATCH_SIZE]
        if qwen_reranker:
            pairs = [_format_qwen_instruction(instruction, query, doc) for doc in batch_docs]
            prefix = f"<|im_start|>system\n{QWEN_SYSTEM}<|im_end|>\n<|im_start|>user\n"
            suffix = "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
            prefix_tokens = tokenizer.encode(prefix, add_special_tokens=False)
            suffix_tokens = tokenizer.encode(suffix, add_special_tokens=False)
            body_max = max(1, MAX_TOKENS - len(prefix_tokens) - len(suffix_tokens))
            inputs = tokenizer(
                pairs,
                padding=False,
                truncation="longest_first",
                max_length=body_max,
                return_attention_mask=False,
            )
            for i, token_ids in enumerate(inputs["input_ids"]):
                inputs["input_ids"][i] = prefix_tokens + token_ids + suffix_tokens
            inputs = tokenizer.pad(inputs, padding=True, max_length=MAX_TOKENS, return_tensors="pt").to(DEVICE)
        else:
            pairs = [[query, doc] for doc in batch_docs]
            inputs = tokenizer(
                pairs,
                padding=True,
                truncation=True,
                max_length=MAX_TOKENS,
                return_tensors="pt",
            ).to(DEVICE)
        logits = model(**inputs).logits.float()
        probs = _score_from_logits(logits, len(batch_docs))
        scores.extend(float(x) for x in probs.detach().cpu())
    return scores


@app.get("/health")
def health() -> dict:
    return {
        "status": "ok",
        "model": MODEL_NAME,
        "mode": "qwen3_yes_no" if model is not None and _is_qwen3_reranker(model.config) else "sequence_classifier",
        "device": DEVICE,
        "cuda_available": torch.cuda.is_available(),
        "cuda_device_count": torch.cuda.device_count(),
        "batch": BATCH_SIZE,
        "max_tokens": MAX_TOKENS,
    }


@app.post("/v1/rerank")
def rerank(req: RerankRequest) -> dict:
    docs = list(req.documents or [])
    if docs:
        with _INFER_LOCK:
            scores = _score(req.query, docs, req.instruction)
    else:
        scores = []
    ranked = [
        {"index": i, "relevance_score": score, "document": {"text": docs[i]}}
        for i, score in sorted(enumerate(scores), key=lambda item: item[1], reverse=True)
    ]
    if req.top_n is not None and req.top_n > 0:
        ranked = ranked[: req.top_n]
    return {"model": req.model or MODEL_NAME, "scores": scores, "results": ranked}


def _selfcheck() -> None:
    one_logit = _score_from_logits(torch.tensor([[0.0], [2.0]]), 2)
    assert one_logit.shape == (2,)
    assert 0.49 < float(one_logit[0]) < 0.51
    assert float(one_logit[1]) > 0.8

    two_logits = _score_from_logits(torch.tensor([[0.0, 2.0], [2.0, 0.0]]), 2)
    assert two_logits.shape == (2,)
    assert float(two_logits[0]) > 0.8
    assert float(two_logits[1]) < 0.2

    flat_logits = _score_from_logits(torch.tensor([0.0, 2.0, 2.0, 0.0]), 2)
    assert flat_logits.shape == (2,)
    assert float(flat_logits[0]) > 0.8
    assert float(flat_logits[1]) < 0.2
    print("selfcheck ok", flush=True)


if __name__ == "__main__" and "--selfcheck" in sys.argv:
    _selfcheck()
