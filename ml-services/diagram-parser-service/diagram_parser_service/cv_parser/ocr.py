"""OCR attachment for detected diagram block crops."""

from __future__ import annotations

import base64
import json
import tempfile
from pathlib import Path
from urllib import error, request

import cv2
import numpy as np

from diagram_parser_service.cv_parser.image import read_image
from diagram_parser_service.cv_parser.models import DetectedBlock, OCRBlockText


def recognize_block_crops(
    image_path: Path,
    blocks: list[DetectedBlock],
    ocr_url: str,
    model: str,
    *,
    padding: int = 8,
    timeout: float = 120.0,
) -> dict[str, OCRBlockText]:
    if not ocr_url.strip() or not blocks:
        return {}
    image = read_image(image_path)
    texts: dict[str, OCRBlockText] = {}
    for block in blocks:
        try:
            texts[block.id] = recognize_crop(crop_block(image, block, padding), ocr_url, model, timeout=timeout)
        except Exception as exc:
            texts[block.id] = OCRBlockText(
                text="",
                confidence=0.0,
                warnings=(f"ocr_failed:{type(exc).__name__}",),
            )
    return texts


def crop_block(image: np.ndarray, block: DetectedBlock, padding: int) -> np.ndarray:
    height, width = image.shape[:2]
    x0, y0, x1, y1 = block.bbox
    x0 = max(0, x0 - padding)
    y0 = max(0, y0 - padding)
    x1 = min(width, x1 + padding)
    y1 = min(height, y1 + padding)
    return image[y0:y1, x0:x1]


def recognize_crop(crop: np.ndarray, ocr_url: str, model: str, *, timeout: float) -> OCRBlockText:
    if crop.size == 0:
        return OCRBlockText(text="", confidence=0.0, warnings=("empty crop",))
    with tempfile.NamedTemporaryFile(suffix=".png") as fh:
        if not cv2.imwrite(fh.name, crop):
            return OCRBlockText(text="", confidence=0.0, warnings=("failed to encode crop",))
        payload = post_ocr(Path(fh.name), ocr_url, model, timeout=timeout)
    text = str(payload.get("text") or "").strip()
    raw_warnings = payload.get("warnings")
    warnings = raw_warnings if isinstance(raw_warnings, list) else []
    confidence = confidence_from_payload(payload)
    return OCRBlockText(text=text, confidence=confidence, warnings=tuple(str(item) for item in warnings))


def post_ocr(path: Path, ocr_url: str, model: str, *, timeout: float) -> dict[str, object]:
    body = {
        "model": model,
        "image_b64": base64.b64encode(path.read_bytes()).decode("ascii"),
        "mime": "image/png",
    }
    endpoint = ocr_url.strip()
    req = request.Request(
        endpoint,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with request.urlopen(req, timeout=timeout) as resp:
            payload = json.loads(resp.read().decode("utf-8"))
    except error.HTTPError as exc:
        raise RuntimeError(f"OCR HTTP {exc.code}: {exc.read()[:500]!r}") from exc
    except error.URLError as exc:
        raise RuntimeError(f"OCR request failed: {exc}") from exc
    if not isinstance(payload, dict):
        raise RuntimeError("OCR response must be a JSON object")
    return payload


def confidence_from_payload(payload: dict[str, object]) -> float:
    for field in ("confidence", "avg_confidence", "score"):
        value = payload.get(field)
        if isinstance(value, bool):
            continue
        if isinstance(value, (int, float)):
            return max(0.0, min(1.0, float(value)))
    return 0.0
