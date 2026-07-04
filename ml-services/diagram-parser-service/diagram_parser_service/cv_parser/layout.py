"""Layout ordering helpers for diagram blocks."""

from __future__ import annotations

import statistics
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from diagram_parser_service.cv_parser.models import DetectedBlock


def top_left_reading_order(blocks: list[DetectedBlock]) -> list[DetectedBlock]:
    """Return a row-major fallback order from top-left to bottom-right."""
    if not blocks:
        return []

    heights = [block.bbox[3] - block.bbox[1] for block in blocks]
    row_tolerance = max(12.0, statistics.median(heights) * 0.6)
    rows: list[list[DetectedBlock]] = []

    for block in sorted(blocks, key=lambda item: (item.center[1], item.center[0])):
        center_y = block.center[1]
        for row in rows:
            row_center = statistics.fmean(item.center[1] for item in row)
            if abs(center_y - row_center) <= row_tolerance:
                row.append(block)
                break
        else:
            rows.append([block])

    ordered: list[DetectedBlock] = []
    for row in sorted(rows, key=lambda items: statistics.fmean(item.center[1] for item in items)):
        ordered.extend(sorted(row, key=lambda item: item.center[0]))
    return ordered
