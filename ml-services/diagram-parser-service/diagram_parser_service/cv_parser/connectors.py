"""Connector detection and block linking for process diagrams."""

from __future__ import annotations

import math

import cv2
import numpy as np

from diagram_parser_service.cv_parser.models import DetectedBlock, DetectedConnector, ParserConfig, Point


def detect_connectors(edges: np.ndarray, blocks: list[DetectedBlock], config: ParserConfig) -> list[DetectedConnector]:
    if len(blocks) < 2:
        return []
    raw_lines = cv2.HoughLinesP(
        edges,
        rho=1,
        theta=np.pi / 180,
        threshold=28,
        minLineLength=config.min_line_length,
        maxLineGap=12,
    )
    if raw_lines is None:
        return []

    connectors: dict[tuple[str, str], DetectedConnector] = {}
    for raw in raw_lines.reshape(-1, 4):
        x1, y1, x2, y2 = (int(value) for value in raw)
        if line_inside_any_block((x1, y1, x2, y2), blocks):
            continue
        source_point, target_point = orient_line((x1, y1), (x2, y2))
        source = nearest_block(source_point, blocks, config.max_connection_distance)
        target = nearest_block(target_point, blocks, config.max_connection_distance)
        if source is None or target is None or source.id == target.id:
            continue
        distance = endpoint_distance(source_point, source) + endpoint_distance(target_point, target)
        confidence = max(0.1, min(0.8, 1.0 - distance / (2.0 * config.max_connection_distance)))
        key = (source.id, target.id)
        candidate = DetectedConnector(source.id, target.id, (x1, y1, x2, y2), confidence)
        previous = connectors.get(key)
        if previous is None or candidate.confidence > previous.confidence:
            connectors[key] = candidate
    return sorted(connectors.values(), key=lambda edge: (edge.source_id, edge.target_id))


def orient_line(a: Point, b: Point) -> tuple[Point, Point]:
    ax, ay = a
    bx, by = b
    dx = bx - ax
    dy = by - ay
    if abs(dx) >= abs(dy):
        return (a, b) if dx >= 0 else (b, a)
    return (a, b) if dy >= 0 else (b, a)


def nearest_block(point: Point, blocks: list[DetectedBlock], max_distance: float) -> DetectedBlock | None:
    ranked = sorted((endpoint_distance(point, block), block.id, block) for block in blocks)
    if not ranked or ranked[0][0] > max_distance:
        return None
    return ranked[0][2]


def endpoint_distance(point: Point, block: DetectedBlock) -> float:
    x, y = point
    x0, y0, x1, y1 = block.bbox
    dx = max(x0 - x, 0.0, x - x1)
    dy = max(y0 - y, 0.0, y - y1)
    return math.hypot(dx, dy)


def line_inside_any_block(line: tuple[int, int, int, int], blocks: list[DetectedBlock]) -> bool:
    x1, y1, x2, y2 = line
    return any(point_inside((x1, y1), block) and point_inside((x2, y2), block) for block in blocks)


def point_inside(point: Point, block: DetectedBlock) -> bool:
    x, y = point
    x0, y0, x1, y1 = block.bbox
    return x0 <= x <= x1 and y0 <= y <= y1
