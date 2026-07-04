"""FastAPI server compatible with vlm-service native image description protocol."""

from __future__ import annotations

import base64
import binascii
import os
import tempfile
import threading
from dataclasses import replace
from pathlib import Path
from typing import Literal

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from diagram_parser_service.cv_parser import parse_diagram
from diagram_parser_service.cv_parser.models import ParserConfig
from diagram_parser_service.cv_parser.ocr import post_ocr, recognize_block_crops
from diagram_parser_service.paddle5_ocr import render_paddle5_ocr
from diagram_parser_service.render import render_prediction
from diagram_parser_service.tiles import render_tiles4_ocr, render_tiles9_ocr


def positive_int_env(name: str, default: int) -> int:
    try:
        return max(1, int(os.getenv(name, str(default))))
    except ValueError:
        return max(1, default)


def positive_float_env(name: str, default: float) -> float:
    try:
        return max(0.001, float(os.getenv(name, str(default))))
    except ValueError:
        return max(0.001, default)


app = FastAPI(title="diagram-parser-service", version="0.1.0")
_PARSE_SEMAPHORE = threading.BoundedSemaphore(positive_int_env("DIAGRAM_PARSE_CONCURRENCY", 1))


class DescribeRequest(BaseModel):
    model: str = "diagram-opencv-paddle"
    image_b64: str = Field(min_length=1)
    mime: str = "image/png"
    prompt: str = ""


class DescribeResponse(BaseModel):
    text: str


class HealthResponse(BaseModel):
    status: Literal["ok"] = "ok"


@app.get("/health")
def health() -> HealthResponse:
    return HealthResponse()


@app.post("/")
def describe(request: DescribeRequest) -> DescribeResponse:
    if not request.mime.startswith("image/"):
        raise HTTPException(status_code=415, detail=f"unsupported mime: {request.mime}")
    image = decode_image(request.image_b64)
    suffix = suffix_from_mime(request.mime)
    try:
        with tempfile.NamedTemporaryFile(suffix=suffix) as tmp:
            tmp.write(image)
            tmp.flush()
            with _PARSE_SEMAPHORE:
                ocr_mode = diagram_ocr_mode()
                ocr_url = os.getenv("DIAGRAM_OCR_URL", "http://paddleocr-vl-service:8088")
                ocr_model = os.getenv("DIAGRAM_OCR_MODEL", "PaddleOCR-VL")
                ocr_timeout = positive_float_env("DIAGRAM_OCR_TIMEOUT", 90.0)
                parse_result = parse_diagram(
                    diagram_id="uploaded-image",
                    image_path=Path(tmp.name),
                    config=parser_config(),
                    ocr_url="",
                    ocr_model=ocr_model,
                    ocr_timeout=ocr_timeout,
                    crop_padding=int(os.getenv("DIAGRAM_CROP_PADDING", "8")),
                )
                use_block_ocr = should_use_block_ocr(ocr_mode, len(parse_result.blocks))
                if use_block_ocr:
                    parse_result = replace(
                        parse_result,
                        ocr_texts=recognize_block_crops(
                            Path(tmp.name),
                            parse_result.blocks,
                            ocr_url,
                            ocr_model,
                            padding=int(os.getenv("DIAGRAM_CROP_PADDING", "8")),
                            timeout=ocr_timeout,
                        ),
                    )
                prediction = parse_result.to_prediction()
                text = render_prediction(prediction)
                if ocr_mode == "paddle5":
                    text += render_paddle5_ocr(Path(tmp.name))
                if ocr_mode in {"full-image", "hybrid"} or (ocr_mode == "adaptive" and not use_block_ocr):
                    text += render_full_image_ocr(Path(tmp.name), ocr_url, ocr_model, ocr_timeout)
                if ocr_mode in {"tiles4", "tiles9"}:
                    render_tiles = render_tiles4_ocr if ocr_mode == "tiles4" else render_tiles9_ocr
                    text += render_tiles(
                        Path(tmp.name),
                        ocr_url,
                        ocr_model,
                        min(ocr_timeout, positive_float_env("DIAGRAM_TILE_OCR_TIMEOUT", 45.0)),
                        overlap_px=int(os.getenv("DIAGRAM_TILE_OVERLAP_PX", "32")),
                    )
    except Exception as exc:
        if fail_open():
            return DescribeResponse(text=f"diagram_parse_error: {type(exc).__name__}\n")
        raise HTTPException(status_code=502, detail=f"diagram parse failed: {exc}") from exc
    return DescribeResponse(text=text)


def decode_image(value: str) -> bytes:
    try:
        return base64.b64decode(value, validate=True)
    except binascii.Error as exc:
        raise HTTPException(status_code=400, detail="image_b64 must be valid base64") from exc


def suffix_from_mime(mime: str) -> str:
    normalized = mime.split(";", 1)[0].strip().lower()
    if normalized == "image/jpeg":
        return ".jpg"
    if normalized == "image/webp":
        return ".webp"
    return ".png"


def parser_config() -> ParserConfig:
    return ParserConfig(
        min_block_area=int(os.getenv("DIAGRAM_MIN_BLOCK_AREA", str(ParserConfig.min_block_area))),
        max_blocks=int(os.getenv("DIAGRAM_MAX_BLOCKS", str(ParserConfig.max_blocks))),
        max_connection_distance=float(
            os.getenv("DIAGRAM_MAX_CONNECTION_DISTANCE", str(ParserConfig.max_connection_distance))
        ),
    )


def diagram_ocr_mode() -> str:
    mode = os.getenv("DIAGRAM_OCR_MODE", "full-image").strip().lower()
    if mode not in {"adaptive", "full-image", "blocks", "hybrid", "paddle5", "tiles4", "tiles9", "none"}:
        return "full-image"
    return mode


def should_use_block_ocr(mode: str, block_count: int) -> bool:
    if mode in {"blocks", "hybrid"}:
        return True
    if mode != "adaptive":
        return False
    max_blocks = positive_int_env("DIAGRAM_BLOCK_OCR_MAX_BLOCKS", 12)
    return 0 < block_count <= max_blocks


def render_full_image_ocr(path: Path, ocr_url: str, model: str, timeout: float) -> str:
    if not ocr_url.strip():
        return "\nfull_image_ocr_status: disabled\n"
    try:
        payload = post_ocr(path, ocr_url, model, timeout=timeout)
    except Exception as exc:
        return f"\nfull_image_ocr_status: failed:{type(exc).__name__}\n"
    text = str(payload.get("text") or "").strip()
    if not text:
        return "\nfull_image_ocr_status: empty\n"
    return "\nfull_image_ocr_status: ok\nfull_image_ocr:\n" + text + "\n"


def fail_open() -> bool:
    return os.getenv("DIAGRAM_FAIL_OPEN", "true").strip().lower() not in {"0", "false", "no"}
