"""High-level local OpenCV diagram parsing."""

from __future__ import annotations

from pathlib import Path

from diagram_parser_service.cv_parser.blocks import detect_blocks
from diagram_parser_service.cv_parser.connectors import detect_connectors
from diagram_parser_service.cv_parser.image import edge_mask, foreground_mask, read_image
from diagram_parser_service.cv_parser.models import DiagramParseResult, ParserConfig
from diagram_parser_service.cv_parser.ocr import recognize_block_crops


def parse_diagram(
    diagram_id: str,
    image_path: Path,
    config: ParserConfig | None = None,
    *,
    ocr_url: str = "",
    ocr_model: str = "PaddleOCR-VL",
    ocr_timeout: float = 120.0,
    crop_padding: int = 8,
) -> DiagramParseResult:
    cfg = config or ParserConfig()
    image = read_image(image_path)
    blocks = detect_blocks(foreground_mask(image), cfg)
    connectors = detect_connectors(edge_mask(image), blocks, cfg)
    ocr_texts = recognize_block_crops(
        image_path,
        blocks,
        ocr_url,
        ocr_model,
        padding=crop_padding,
        timeout=ocr_timeout,
    )
    height, width = image.shape[:2]
    return DiagramParseResult(
        diagram_id=diagram_id,
        image_path=image_path,
        width=width,
        height=height,
        blocks=blocks,
        connectors=connectors,
        ocr_texts=ocr_texts,
    )
