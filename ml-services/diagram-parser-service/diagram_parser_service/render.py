"""Render extracted diagram graphs into retrieval-friendly text."""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from typing import Any


def render_prediction(prediction: Mapping[str, Any]) -> str:
    diagram_id = str(prediction.get("diagram_id") or "uploaded-diagram")
    evidence = as_mapping(prediction.get("evidence"))
    nodes = list(as_iterable(prediction.get("nodes")))
    edges = list(as_iterable(prediction.get("edges")))
    text_blocks = list(as_iterable(evidence.get("text_blocks")))

    lines = [
        "diagram_parse_version: local-opencv-paddle-v1",
        f"diagram_id: {diagram_id}",
        f"image_file: {evidence.get('image_file') or ''}",
        f"node_count: {len(nodes)}",
        f"edge_count: {len(edges)}",
        f"detected_block_count: {evidence.get('block_count') or 0}",
        f"rejected_ocr_block_count: {evidence.get('rejected_ocr_block_count') or 0}",
        "",
        "nodes:",
    ]
    lines.extend(render_nodes(nodes))
    lines.extend(["", "directed_edges:"])
    lines.extend(render_edges(edges, nodes))
    lines.extend(["", "reading_order:"])
    lines.extend(render_reading_order(nodes))
    lines.extend(["", "ocr_text_blocks:"])
    lines.extend(render_text_blocks(text_blocks))
    return "\n".join(lines).strip() + "\n"


def render_nodes(nodes: list[Any]) -> list[str]:
    if not nodes:
        return ["- none"]
    lines: list[str] = []
    for node in nodes:
        item = as_mapping(node)
        node_id = str(item.get("id") or "")
        label = compact(item.get("label") or item.get("text") or "")
        bbox = format_bbox(item.get("bbox"))
        confidence = item.get("confidence")
        lines.append(f'- {node_id}: "{label}" bbox={bbox} confidence={confidence}')
    return lines


def render_edges(edges: list[Any], nodes: list[Any]) -> list[str]:
    if not edges:
        return ["- none"]
    labels_by_id = {
        str(as_mapping(node).get("id") or ""): compact(as_mapping(node).get("label") or "")
        for node in nodes
    }
    lines: list[str] = []
    for edge in edges:
        item = as_mapping(edge)
        source_id = str(item.get("source") or "")
        target_id = str(item.get("target") or "")
        source_label = labels_by_id.get(source_id, source_id)
        target_label = labels_by_id.get(target_id, target_id)
        confidence = item.get("confidence")
        lines.append(f'- "{source_label}" -> "{target_label}" confidence={confidence}')
    return lines


def render_reading_order(nodes: list[Any]) -> list[str]:
    if not nodes:
        return ["- none"]
    ordered = sorted(
        (as_mapping(node) for node in nodes),
        key=lambda node: int_or_max(node.get("order_index")),
    )
    return [
        f'- {index}. "{compact(node.get("label") or node.get("text") or "")}"'
        for index, node in enumerate(ordered, 1)
    ]


def render_text_blocks(text_blocks: list[Any]) -> list[str]:
    if not text_blocks:
        return ["- none"]
    ordered = sorted(
        (as_mapping(block) for block in text_blocks),
        key=lambda block: int_or_max(block.get("order_index")),
    )
    lines: list[str] = []
    for block in ordered:
        label = compact(block.get("label") or "")
        raw_text = compact(block.get("text") or "")
        status = block.get("label_status") or block.get("text_status") or ""
        bbox = format_bbox(block.get("bbox"))
        lines.append(f'- status={status} bbox={bbox} label="{label}" text="{raw_text}"')
    return lines


def as_mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def as_iterable(value: Any) -> Iterable[Any]:
    if isinstance(value, list):
        return value
    return []


def compact(value: Any) -> str:
    return " ".join(str(value).split())[:500]


def int_or_max(value: Any) -> int:
    if isinstance(value, bool):
        return 999_999
    if isinstance(value, int):
        return value
    try:
        return int(str(value))
    except (TypeError, ValueError):
        return 999_999


def format_bbox(value: Any) -> str:
    if not isinstance(value, list | tuple) or len(value) != 4:
        return "[]"
    return "[" + ",".join(str(int_or_zero(item)) for item in value) + "]"


def int_or_zero(value: Any) -> int:
    if isinstance(value, bool):
        return 0
    if isinstance(value, int | float):
        return int(value)
    try:
        return int(float(str(value)))
    except (TypeError, ValueError):
        return 0
