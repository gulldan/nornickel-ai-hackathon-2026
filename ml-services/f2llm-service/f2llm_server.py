"""OpenAI-compatible embeddings service for codefuse-ai/F2LLM-v2.

The compose stack points EMBEDDINGS_URL at this service. It exposes the
OpenAI-style `/v1/embeddings` contract used by chunk-splitter, llm-service,
eval-service and itc-worker.
F2LLM-v2 is a Qwen3-based causal embedder: pooling is **last-token (EOS) + L2
normalisation**.

F2LLM-v2-0.6B has dim **1024**, so the Qdrant collection size is fixed. The
embedder defines the vector space: changing it requires RE-EMBEDDING the whole
corpus (drop the Qdrant collection / `down -v`, then re-ingest). Vectors from
different models are not comparable.

Run locally:
  cd f2llm-service
  uv venv --python 3.11 && uv pip install -r requirements.txt
  TORCH_DEVICE=auto F2LLM_MODEL=codefuse-ai/F2LLM-v2-0.6B \
    .venv/bin/uvicorn f2llm_server:app --host 0.0.0.0 --port 8085
"""

import os
from typing import Union

import torch
import torch.nn.functional as F
from fastapi import FastAPI
from pydantic import BaseModel
from transformers import AutoModel, AutoTokenizer

MODEL_NAME = os.environ.get("F2LLM_MODEL", "codefuse-ai/F2LLM-v2-0.6B")
MAX_TOKENS = int(os.environ.get("F2LLM_MAX_TOKENS", "512"))


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
DTYPE = torch.bfloat16 if DEVICE == "cuda" else torch.float32

tokenizer = AutoTokenizer.from_pretrained(MODEL_NAME, trust_remote_code=True)
model = AutoModel.from_pretrained(MODEL_NAME, torch_dtype=DTYPE, trust_remote_code=True).to(DEVICE).eval()

app = FastAPI(title="F2LLM-v2 embeddings")


class EmbeddingRequest(BaseModel):
    input: Union[str, list[str]]
    model: Union[str, None] = None


def _last_token_pool(last_hidden: torch.Tensor, attention_mask: torch.Tensor) -> torch.Tensor:
    """Last non-padding token of each sequence (handles right-padding)."""
    lengths = attention_mask.sum(dim=1) - 1
    idx = lengths.clamp(min=0)
    return last_hidden[torch.arange(last_hidden.size(0), device=last_hidden.device), idx]


@torch.no_grad()
def _embed(texts: list[str]) -> list[list[float]]:
    batch = tokenizer(
        texts, padding=True, truncation=True, max_length=MAX_TOKENS, return_tensors="pt"
    ).to(DEVICE)
    out = model(**batch)
    pooled = _last_token_pool(out.last_hidden_state, batch["attention_mask"])
    pooled = F.normalize(pooled, p=2, dim=1)
    return pooled.cpu().float().tolist()


@app.get("/health")
def health() -> dict:
    return {"status": "ok", "model": MODEL_NAME, "device": DEVICE}


@app.post("/v1/embeddings")
def embeddings(req: EmbeddingRequest) -> dict:
    texts = [req.input] if isinstance(req.input, str) else list(req.input)
    vectors = _embed(texts)
    data = [
        {"object": "embedding", "index": i, "embedding": v} for i, v in enumerate(vectors)
    ]
    return {"object": "list", "data": data, "model": MODEL_NAME}
