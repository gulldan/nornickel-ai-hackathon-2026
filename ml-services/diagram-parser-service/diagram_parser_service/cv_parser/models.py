"""Contracts for the local OpenCV diagram parser."""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from diagram_parser_service.cv_parser.labels import LabelCandidate, classify_diagram_label
from diagram_parser_service.cv_parser.layout import top_left_reading_order

BBox = tuple[int, int, int, int]
Point = tuple[float, float]


@dataclass(frozen=True)
class ParserConfig:
    min_block_area: int = 1_200
    min_block_width: int = 24
    min_block_height: int = 18
    max_block_area_ratio: float = 0.35
    block_merge_gap: int = 14
    max_blocks: int = 40
    max_stage_label_blocks: int = 36
    min_line_length: int = 32
    max_connection_distance: float = 95.0


@dataclass(frozen=True)
class OCRBlockText:
    text: str
    confidence: float
    warnings: tuple[str, ...] = ()


@dataclass(frozen=True)
class DetectedBlock:
    id: str
    bbox: BBox
    area: int
    confidence: float

    @property
    def center(self) -> Point:
        x0, y0, x1, y1 = self.bbox
        return ((x0 + x1) / 2.0, (y0 + y1) / 2.0)


@dataclass(frozen=True)
class DetectedConnector:
    source_id: str
    target_id: str
    line: tuple[int, int, int, int]
    confidence: float


@dataclass(frozen=True)
class DiagramParseResult:
    diagram_id: str
    image_path: Path
    width: int
    height: int
    blocks: list[DetectedBlock]
    connectors: list[DetectedConnector]
    ocr_texts: dict[str, OCRBlockText] = field(default_factory=dict)

    def to_prediction(self) -> dict[str, Any]:
        ordered_blocks = top_left_reading_order(self.blocks)
        node_blocks = self._node_blocks(ordered_blocks)
        node_ids = {block.id for block in node_blocks}
        nodes = [
            {
                "id": block.id,
                "label": self._block_label(block, index),
                "text": self._block_text(block, index),
                "text_status": self._text_status(block),
                "type": "diagram_block",
                "bbox": list(block.bbox),
                "order_index": index,
                "confidence": round(self._node_confidence(block), 3),
            }
            for index, block in enumerate(node_blocks, start=1)
        ]
        edges = [
            {
                "source": connector.source_id,
                "target": connector.target_id,
                "label": "connector",
                "confidence": round(connector.confidence, 3),
            }
            for connector in self.connectors
            if connector.source_id in node_ids and connector.target_id in node_ids
        ]
        text_blocks = [
            {
                "id": block.id,
                "order_index": index,
                "label": self._block_label(block, index),
                "label_status": self._label_candidate(block).status,
                "text": self._block_text(block, index),
                "text_status": self._text_status(block),
                "bbox": list(block.bbox),
            }
            for index, block in enumerate(ordered_blocks, start=1)
        ]
        layout_sequence_edges = [
            {
                "source": source.id,
                "target": target.id,
                "label": "layout_next",
                "policy": "top_left_to_bottom_right",
            }
            for source, target in zip(ordered_blocks, ordered_blocks[1:], strict=False)
        ]
        visual_review = self._visual_review()
        return {
            "diagram_id": self.diagram_id,
            "nodes": nodes,
            "edges": edges,
            "visual_review": visual_review,
            "evidence": {
                "method": "local-opencv-block-connector-v1",
                "image_file": self.image_path.name,
                "block_count": len(self.blocks),
                "connector_count": len(self.connectors),
                "ocr_block_count": sum(1 for text in self.ocr_texts.values() if text.text.strip()),
                "node_count": len(nodes),
                "rejected_ocr_block_count": self._rejected_ocr_block_count(),
                "text_blocks": text_blocks,
                "reading_order": [block.id for block in ordered_blocks],
                "reading_order_policy": "row-major top-left to bottom-right fallback; not a process-flow edge",
                "layout_sequence_edges": layout_sequence_edges,
                "notes": [
                    "No external VLM/LLM used.",
                    "Labels come from OCR when configured; otherwise generic block IDs are used.",
                    "Text blocks and reading order are preserved even before OCR labels are attached.",
                    "visual_review is conservative until a human or label-aware OCR check accepts the graph.",
                ],
            },
        }

    def _node_blocks(self, ordered_blocks: list[DetectedBlock]) -> list[DetectedBlock]:
        if not self.ocr_texts:
            return ordered_blocks
        return [block for block in ordered_blocks if self._block_label(block, 0)]

    def _block_label(self, block: DetectedBlock, index: int) -> str:
        candidate = self._label_candidate(block)
        if candidate.label:
            return candidate.label
        return "" if self.ocr_texts else f"block {index}"

    def _label_candidate(self, block: DetectedBlock) -> LabelCandidate:
        text = self.ocr_texts.get(block.id)
        if text and text.text.strip():
            return classify_diagram_label(text.text)
        return LabelCandidate(label="", status="pending_ocr" if text is None else "ocr_empty")

    def _rejected_ocr_block_count(self) -> int:
        return sum(
            1
            for block in self.blocks
            if self.ocr_texts.get(block.id) is not None and not self._label_candidate(block).label
        )

    def _block_text(self, block: DetectedBlock, index: int) -> str:
        text = self.ocr_texts.get(block.id)
        if text and text.text.strip():
            return single_line(text.text)
        return f"block {index}"

    def _text_status(self, block: DetectedBlock) -> str:
        text = self.ocr_texts.get(block.id)
        if text is None:
            return "pending_ocr"
        return "ocr" if text.text.strip() else "ocr_empty"

    def _node_confidence(self, block: DetectedBlock) -> float:
        text = self.ocr_texts.get(block.id)
        if text is None:
            return block.confidence
        if not text.text.strip():
            return min(block.confidence, 0.25)
        if text.confidence <= 0.0:
            return block.confidence
        return min(0.99, (block.confidence + text.confidence) / 2.0)

    def _visual_review(self) -> str:
        if not self.blocks:
            return "catastrophic"
        return "bad"


def single_line(value: str) -> str:
    return " ".join(value.split())
