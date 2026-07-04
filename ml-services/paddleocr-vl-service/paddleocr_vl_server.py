"""Structural OCR HTTP server: PaddleOCR-VL behind the rag-test ocr-service contract.

ocr-service POSTs {"model", "image_b64", "mime"} and expects {"text", "pages"},
where "pages" is the list of per-page texts in reading order (ocr-service turns
it into page_offsets provenance for chunking). PaddleOCR-VL (Apache-2.0, 0.9B
vision-language document parser) recognises layout, reading order, tables and
formulas in one pass and emits Markdown (sections, HTML tables, LaTeX formulas),
which preserves STRUCTURE for downstream chunking far better than flat OCR
text. It ships a measured Cyrillic/Russian evaluation. PDFs are recognised page
by page; the per-page Markdown is concatenated in reading order.

Run:  PADDLEOCR_VL_DEVICE=gpu .venv/bin/uvicorn paddleocr_vl_server:app --host 0.0.0.0 --port 8088
"""

import base64
import os
import tempfile
from contextlib import asynccontextmanager

from fastapi import FastAPI
from pydantic import BaseModel

_pipeline: dict = {}

# MIME → temp-file suffix. PaddleOCR-VL.predict() reads a path and dispatches the
# decoder, so incoming bytes are written out with a faithful extension.
_SUFFIX = {
    "image/png": ".png",
    "image/jpeg": ".jpg",
    "image/jpg": ".jpg",
    "image/tiff": ".tiff",
    "image/bmp": ".bmp",
    "image/webp": ".webp",
    "image/gif": ".gif",
}


def _device() -> str:
    """PaddleOCR-VL device spec, e.g. 'gpu', 'gpu:0' or 'cpu'. 'auto' (the default)
    lets Paddle pick GPU 0 with a CPU fallback."""
    configured = os.environ.get("PADDLEOCR_VL_DEVICE", "auto").strip().lower()
    return configured or "auto"


def _suffix(mime: str) -> str:
    mime = (mime or "").lower()
    if "pdf" in mime:
        return ".pdf"
    return _SUFFIX.get(mime, ".png")


def _markdown(res) -> str:
    """Reading-order Markdown of one page (HTML tables, LaTeX formulas)."""
    md = getattr(res, "markdown", None)
    if isinstance(md, dict):
        return (md.get("markdown_texts") or "").strip()
    return ("" if md is None else str(md)).strip()


def _load() -> None:
    from paddleocr import PaddleOCRVL

    device = _device()
    kwargs = {} if device == "auto" else {"device": device}
    _pipeline["vl"] = PaddleOCRVL(**kwargs)


def _recognize(path: str) -> list[str]:
    """One Markdown string per page, in input order (empties preserved for count)."""
    return [_markdown(res) for res in _pipeline["vl"].predict(input=path)]


class OCRRequest(BaseModel):
    model: str | None = None
    image_b64: str
    mime: str | None = None


@asynccontextmanager
async def lifespan(_app: FastAPI):
    _load()
    yield


app = FastAPI(lifespan=lifespan, title="paddleocr-vl-ocr-adapter")


@app.get("/health")
def health() -> dict:
    return {"status": "ok", "ready": "vl" in _pipeline, "device": _device()}


@app.post("/")
def ocr(req: OCRRequest) -> dict:
    data = base64.b64decode(req.image_b64)
    with tempfile.NamedTemporaryFile(suffix=_suffix(req.mime or ""), delete=False) as fh:
        fh.write(data)
        path = fh.name
    try:
        pages = _recognize(path)
    finally:
        os.unlink(path)
    # "pages" carries the per-page texts (empties preserved so page numbers
    # match the scan); "text" keeps the flat join for older/simple clients.
    return {"text": "\n\n".join(p for p in pages if p), "pages": pages}
