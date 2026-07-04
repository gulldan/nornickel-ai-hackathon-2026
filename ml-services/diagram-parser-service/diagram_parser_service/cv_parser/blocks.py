"""Block detection for process diagrams."""

from __future__ import annotations

import cv2
import numpy as np

from diagram_parser_service.cv_parser.models import BBox, DetectedBlock, ParserConfig
from diagram_parser_service.cv_parser.stage_blocks import detect_stage_label_blocks


def detect_blocks(mask: np.ndarray, config: ParserConfig) -> list[DetectedBlock]:
    height, width = mask.shape[:2]
    # Keep connector lines from gluing neighboring boxes together. Larger text
    # grouping is a later OCR-label step; this pass should find visual regions.
    kernel = cv2.getStructuringElement(cv2.MORPH_RECT, (3, 3))
    joined = cv2.morphologyEx(mask, cv2.MORPH_CLOSE, kernel, iterations=1)
    contours, _ = cv2.findContours(joined, cv2.RETR_LIST, cv2.CHAIN_APPROX_SIMPLE)

    boxes = []
    image_area = width * height
    for contour in contours:
        x, y, w, h = cv2.boundingRect(contour)
        area = w * h
        if area < config.min_block_area:
            continue
        if w < config.min_block_width or h < config.min_block_height:
            continue
        if area > image_area * config.max_block_area_ratio:
            continue
        contour_area = cv2.contourArea(contour)
        extent = contour_area / max(area, 1)
        perimeter = cv2.arcLength(contour, True)
        approx = cv2.approxPolyDP(contour, 0.03 * perimeter, True)
        if len(approx) > 6 or extent < 0.55:
            continue
        boxes.append((x, y, x + w, y + h))

    generic_boxes = [
        box
        for box in merge_boxes(sorted(boxes, key=lambda box: (box[1], box[0])), config.block_merge_gap)
        if (box[2] - box[0]) * (box[3] - box[1]) <= image_area * config.max_block_area_ratio
    ]
    stage_blocks = detect_stage_label_blocks(mask, config)
    merged = combine_boxes(generic_boxes, [block.bbox for block in stage_blocks])
    merged = sorted(merged, key=lambda box: (box[1], box[0]))[: config.max_blocks]
    return [
        DetectedBlock(
            id=f"b{index}",
            bbox=box,
            area=(box[2] - box[0]) * (box[3] - box[1]),
            confidence=block_confidence(box, width, height),
        )
        for index, box in enumerate(merged, start=1)
    ]


def combine_boxes(generic_boxes: list[BBox], stage_boxes: list[BBox]) -> list[BBox]:
    combined = list(generic_boxes)
    for box in stage_boxes:
        if any(is_duplicate_box(existing, box) for existing in combined):
            continue
        combined.append(box)
    return combined


def is_duplicate_box(existing: BBox, candidate: BBox) -> bool:
    if contains(existing, candidate):
        return area(existing) <= area(candidate) * 2.3
    return high_overlap(existing, candidate)


def merge_boxes(boxes: list[BBox], gap: int) -> list[BBox]:
    merged: list[BBox] = []
    for box in boxes:
        next_box = box
        changed = True
        while changed:
            changed = False
            rest: list[BBox] = []
            for existing in merged:
                if near_or_overlapping(next_box, existing, gap):
                    next_box = union(next_box, existing)
                    changed = True
                else:
                    rest.append(existing)
            merged = rest
        merged.append(next_box)
    return merged


def near_or_overlapping(a: BBox, b: BBox, gap: int) -> bool:
    return not (a[2] + gap < b[0] or b[2] + gap < a[0] or a[3] + gap < b[1] or b[3] + gap < a[1])


def union(a: BBox, b: BBox) -> BBox:
    return min(a[0], b[0]), min(a[1], b[1]), max(a[2], b[2]), max(a[3], b[3])


def contains(outer: BBox, inner: BBox) -> bool:
    return inner[0] >= outer[0] and inner[1] >= outer[1] and inner[2] <= outer[2] and inner[3] <= outer[3]


def high_overlap(a: BBox, b: BBox) -> bool:
    x0 = max(a[0], b[0])
    y0 = max(a[1], b[1])
    x1 = min(a[2], b[2])
    y1 = min(a[3], b[3])
    if x1 <= x0 or y1 <= y0:
        return False
    intersection = (x1 - x0) * (y1 - y0)
    return intersection / min(area(a), area(b)) >= 0.82


def area(box: BBox) -> int:
    return (box[2] - box[0]) * (box[3] - box[1])


def block_confidence(box: BBox, width: int, height: int) -> float:
    box_width = box[2] - box[0]
    box_height = box[3] - box[1]
    area_ratio = (box_width * box_height) / max(width * height, 1)
    aspect = box_width / max(box_height, 1)
    aspect_score = 1.0 if 0.3 <= aspect <= 12.0 else 0.55
    size_score = min(1.0, max(0.2, area_ratio * 90.0))
    return max(0.05, min(0.95, aspect_score * size_score))
