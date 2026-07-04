"""Tiled OCR rendering for large diagram images."""

from __future__ import annotations

import tempfile
from dataclasses import dataclass
from pathlib import Path

import cv2
import numpy as np

from diagram_parser_service.cv_parser.ocr import post_ocr


@dataclass(frozen=True)
class Tile:
    index: int
    row: int
    col: int
    bbox: tuple[int, int, int, int]
    image: np.ndarray


def render_tiles4_ocr(path: Path, ocr_url: str, model: str, timeout: float, *, overlap_px: int = 32) -> str:
    return render_tiled_ocr(path, ocr_url, model, timeout, grid_size=2, overlap_px=overlap_px)


def render_tiles9_ocr(path: Path, ocr_url: str, model: str, timeout: float, *, overlap_px: int = 32) -> str:
    return render_tiled_ocr(path, ocr_url, model, timeout, grid_size=3, overlap_px=overlap_px)


def render_tiled_ocr(
    path: Path,
    ocr_url: str,
    model: str,
    timeout: float,
    *,
    grid_size: int,
    overlap_px: int = 32,
) -> str:
    if not ocr_url.strip():
        return "\ntile_ocr_status: disabled\n"

    image = cv2.imread(str(path), cv2.IMREAD_COLOR)
    if image is None:
        return "\ntile_ocr_status: failed:image_read_error\n"

    rendered: list[str] = []
    failures: list[str] = []
    for tile in split_grid(image, grid_size=grid_size, overlap_px=max(0, overlap_px)):
        text, warning = recognize_tile(tile, ocr_url, model, timeout=timeout)
        if warning:
            failures.append(f"tile_{tile.index}:{warning}")
        if text:
            x0, y0, x1, y1 = tile.bbox
            rendered.append(
                f"tile {tile.index} row={tile.row} col={tile.col} bbox={x0},{y0},{x1},{y1}\n{text}"
            )

    if not rendered:
        detail = ",".join(failures) if failures else "empty"
        return f"\ntile_ocr_status: {detail}\n"

    status = "ok" if not failures else "partial:" + ",".join(failures)
    return "\ntile_ocr_status: " + status + "\ntile_ocr:\n" + "\n---\n".join(rendered) + "\n"


def split_tiles4(image: np.ndarray, overlap_px: int) -> list[Tile]:
    return split_grid(image, grid_size=2, overlap_px=overlap_px)


def split_grid(image: np.ndarray, *, grid_size: int, overlap_px: int) -> list[Tile]:
    height, width = image.shape[:2]
    tiles: list[Tile] = []
    index = 1
    for row in range(grid_size):
        y0_base = row * height // grid_size
        y1_base = (row + 1) * height // grid_size
        for col in range(grid_size):
            x0_base = col * width // grid_size
            x1_base = (col + 1) * width // grid_size
            x0 = max(0, min(width, x0_base - overlap_px))
            y0 = max(0, min(height, y0_base - overlap_px))
            x1 = max(x0, min(width, x1_base + overlap_px))
            y1 = max(y0, min(height, y1_base + overlap_px))
            if x1 <= x0 or y1 <= y0:
                continue
            tiles.append(
                Tile(
                    index=index,
                    row=row,
                    col=col,
                    bbox=(x0, y0, x1, y1),
                    image=image[y0:y1, x0:x1],
                )
            )
            index += 1
    return tiles


def recognize_tile(tile: Tile, ocr_url: str, model: str, *, timeout: float) -> tuple[str, str]:
    if tile.image.size == 0:
        return "", "empty_crop"
    with tempfile.NamedTemporaryFile(suffix=".png") as fh:
        if not cv2.imwrite(fh.name, tile.image):
            return "", "encode_failed"
        try:
            payload = post_ocr(Path(fh.name), ocr_url, model, timeout=timeout)
        except Exception as exc:
            return "", f"ocr_failed:{type(exc).__name__}"
    return str(payload.get("text") or "").strip(), ""
