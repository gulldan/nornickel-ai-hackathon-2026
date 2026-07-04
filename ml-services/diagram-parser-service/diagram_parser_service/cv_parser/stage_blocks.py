"""Stage-label block detection for dense process diagrams."""

from __future__ import annotations

import cv2
import numpy as np

from diagram_parser_service.cv_parser.models import BBox, DetectedBlock, ParserConfig


def detect_stage_label_blocks(mask: np.ndarray, config: ParserConfig) -> list[DetectedBlock]:
    """Find small outlined text regions that the generic block detector drops.

    Flotation regulations often draw process stages as thin horizontal boxes
    embedded in a larger line diagram. Generic contour merging tends to glue
    these boxes into one huge component, so this detector works on pre-merge
    contours and keeps only compact horizontal candidates.
    """
    height, width = mask.shape[:2]
    joined = cv2.morphologyEx(mask, cv2.MORPH_CLOSE, cv2.getStructuringElement(cv2.MORPH_RECT, (3, 3)), iterations=1)
    contours, _ = cv2.findContours(joined, cv2.RETR_LIST, cv2.CHAIN_APPROX_SIMPLE)

    image_area = width * height
    min_area = max(450, min(config.min_block_area, 900))
    max_area = min(image_area * 0.05, 16_000)
    boxes: list[BBox] = []

    for contour in contours:
        x, y, w, h = cv2.boundingRect(contour)
        area = w * h
        if not is_stage_label_shape(w, h, area, min_area, max_area):
            continue
        contour_area = cv2.contourArea(contour)
        extent = contour_area / max(area, 1)
        perimeter = cv2.arcLength(contour, True)
        approx = cv2.approxPolyDP(contour, 0.03 * perimeter, True)
        if extent < 0.45 or len(approx) > 10:
            continue
        boxes.append((x, y, x + w, y + h))

    boxes = remove_nested_boxes(sorted(boxes, key=lambda box: ((box[1], box[0]), box_area(box))))
    return [
        DetectedBlock(
            id=f"s{index}",
            bbox=box,
            area=box_area(box),
            confidence=stage_label_confidence(box, width, height),
        )
        for index, box in enumerate(boxes[: config.max_stage_label_blocks], start=1)
    ]


def is_stage_label_shape(width: int, height: int, area: int, min_area: int, max_area: float) -> bool:
    if area < min_area or area > max_area:
        return False
    if width < 35 or height < 10:
        return False
    if height > 90:
        return False
    aspect = width / max(height, 1)
    return 1.25 <= aspect <= 18.0


def remove_nested_boxes(boxes: list[BBox]) -> list[BBox]:
    kept: list[BBox] = []
    for box in boxes:
        if any(is_nested(box, existing) or high_overlap(box, existing) for existing in kept):
            continue
        kept.append(box)
    return kept


def is_nested(inner: BBox, outer: BBox) -> bool:
    return (
        inner != outer
        and inner[0] >= outer[0]
        and inner[1] >= outer[1]
        and inner[2] <= outer[2]
        and inner[3] <= outer[3]
    )


def high_overlap(a: BBox, b: BBox) -> bool:
    x0 = max(a[0], b[0])
    y0 = max(a[1], b[1])
    x1 = min(a[2], b[2])
    y1 = min(a[3], b[3])
    if x1 <= x0 or y1 <= y0:
        return False
    intersection = (x1 - x0) * (y1 - y0)
    return intersection / min(box_area(a), box_area(b)) >= 0.82


def box_area(box: BBox) -> int:
    return (box[2] - box[0]) * (box[3] - box[1])


def stage_label_confidence(box: BBox, width: int, height: int) -> float:
    box_width = box[2] - box[0]
    box_height = box[3] - box[1]
    aspect = box_width / max(box_height, 1)
    aspect_score = 1.0 if 2.0 <= aspect <= 12.0 else 0.7
    area_score = min(1.0, box_area(box) / max(width * height * 0.004, 1.0))
    return max(0.15, min(0.88, aspect_score * area_score))
