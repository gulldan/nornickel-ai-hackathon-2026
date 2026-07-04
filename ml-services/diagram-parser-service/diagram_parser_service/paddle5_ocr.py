"""Lightweight PP-OCRv5 label OCR for technological diagrams."""

from __future__ import annotations

import importlib
import logging
import os
import threading
from dataclasses import dataclass
from pathlib import Path
from typing import Any

LOGGER = logging.getLogger(__name__)


@dataclass(frozen=True)
class OCRLabel:
    text: str
    score: float
    bbox: tuple[int, int, int, int]


_ENGINE_LOCK = threading.Lock()
_ENGINE: Any | None = None


def render_paddle5_ocr(path: Path) -> str:
    try:
        labels = recognize_labels(path)
    except Exception as exc:
        LOGGER.exception("PP-OCRv5 label OCR failed for %s", path)
        return f"\npaddle5_ocr_status: failed:{type(exc).__name__}\n"

    if not labels:
        return "\npaddle5_ocr_status: empty\n"

    lines = [
        "\npaddle5_ocr_status: ok",
        f"paddle5_ocr_line_count: {len(labels)}",
        f"paddle5_ocr_detector: {det_model_name()}",
        f"paddle5_ocr_recognizer: {rec_model_name()}",
        "paddle5_ocr_text:",
    ]
    for label in labels:
        lines.append(f'- bbox={format_bbox(label.bbox)} score={label.score:.3f} text="{compact(label.text)}"')
    return "\n".join(lines) + "\n"


def recognize_labels(path: Path) -> list[OCRLabel]:
    result = get_engine().predict(str(path))
    labels: list[OCRLabel] = []
    for page in result:
        labels.extend(labels_from_page(page))
    return labels


def get_engine() -> Any:
    global _ENGINE
    with _ENGINE_LOCK:
        if _ENGINE is None:
            _ENGINE = create_engine()
        return _ENGINE


def create_engine() -> Any:
    paddleocr = importlib.import_module("paddleocr")

    return paddleocr.PaddleOCR(
        lang=os.getenv("DIAGRAM_PADDLE5_LANG", "ru"),
        text_detection_model_name=det_model_name(),
        text_recognition_model_name=rec_model_name(),
        text_recognition_batch_size=positive_int_env("DIAGRAM_PADDLE5_REC_BATCH_SIZE", 8),
        use_doc_orientation_classify=False,
        use_doc_unwarping=False,
        use_textline_orientation=False,
        device=os.getenv("DIAGRAM_PADDLE5_DEVICE", "cpu"),
    )


def det_model_name() -> str:
    return os.getenv("DIAGRAM_PADDLE5_DET_MODEL", "PP-OCRv5_server_det")


def rec_model_name() -> str:
    return os.getenv("DIAGRAM_PADDLE5_REC_MODEL", "eslav_PP-OCRv5_mobile_rec")


def labels_from_page(page: Any) -> list[OCRLabel]:
    data = getattr(page, "json", None)
    data = data() if callable(data) else data
    if not isinstance(data, dict):
        return []
    res = data.get("res", data)
    if not isinstance(res, dict):
        return []

    texts = list(res.get("rec_texts") or [])
    scores = list(res.get("rec_scores") or [])
    polygons = list(res.get("rec_polys") or res.get("dt_polys") or [])
    labels: list[OCRLabel] = []
    for index, text in enumerate(texts):
        normalized = compact(text)
        if not normalized:
            continue
        labels.append(
            OCRLabel(
                text=normalized,
                score=float_or_zero(scores[index] if index < len(scores) else 0.0),
                bbox=bbox_from_polygon(polygons[index] if index < len(polygons) else None),
            )
        )
    return labels


def bbox_from_polygon(value: Any) -> tuple[int, int, int, int]:
    if not isinstance(value, list | tuple):
        return (0, 0, 0, 0)
    points: list[tuple[int, int]] = []
    for point in value:
        if not isinstance(point, list | tuple) or len(point) < 2:
            continue
        points.append((int_or_zero(point[0]), int_or_zero(point[1])))
    if not points:
        return (0, 0, 0, 0)
    xs = [point[0] for point in points]
    ys = [point[1] for point in points]
    return (min(xs), min(ys), max(xs), max(ys))


def compact(value: Any) -> str:
    return " ".join(str(value).split())[:500]


def format_bbox(value: tuple[int, int, int, int]) -> str:
    return "[" + ",".join(str(item) for item in value) + "]"


def int_or_zero(value: Any) -> int:
    if isinstance(value, bool):
        return 0
    if isinstance(value, int | float):
        return int(value)
    try:
        return int(float(str(value)))
    except (TypeError, ValueError):
        return 0


def float_or_zero(value: Any) -> float:
    if isinstance(value, bool):
        return 0.0
    if isinstance(value, int | float):
        return float(value)
    try:
        return float(str(value))
    except (TypeError, ValueError):
        return 0.0


def positive_int_env(name: str, default: int) -> int:
    try:
        return max(1, int(os.getenv(name, str(default))))
    except ValueError:
        return max(1, default)
