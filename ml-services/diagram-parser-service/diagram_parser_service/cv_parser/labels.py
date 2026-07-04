"""OCR label cleanup for process-diagram graph nodes."""

from __future__ import annotations

import html
import re
from dataclasses import dataclass

_HTML_TABLE_RE = re.compile(r"<table\b.*?</table>", flags=re.IGNORECASE | re.DOTALL)
_HTML_IMG_RE = re.compile(r"<img\b[^>]*>", flags=re.IGNORECASE)
_HTML_TAG_RE = re.compile(r"<[^>]+>")
_LATEX_RE = re.compile(r"\$[^$]*\$")


@dataclass(frozen=True)
class LabelCandidate:
    label: str
    status: str


def clean_diagram_label(raw_text: str) -> str:
    """Return a compact process-stage label or an empty string for non-node data."""
    return classify_diagram_label(raw_text).label


def classify_diagram_label(raw_text: str) -> LabelCandidate:
    text = strip_markup(raw_text)
    if not text:
        return LabelCandidate(label="", status="empty")

    canonical = canonical_stage_label(text)
    if canonical:
        return LabelCandidate(label=canonical, status="canonical")

    compact = compact_freeform_label(text)
    if is_non_node_label(compact or text):
        return LabelCandidate(label="", status="rejected_non_node")
    if is_meaningful_label(compact):
        return LabelCandidate(label=compact, status="freeform")
    return LabelCandidate(label="", status="rejected_low_signal")


def strip_markup(raw_text: str) -> str:
    with_table_headers = _HTML_TABLE_RE.sub(table_header_text, raw_text)
    without_images = _HTML_IMG_RE.sub(" ", with_table_headers)
    without_latex = _LATEX_RE.sub(" ", without_images)
    without_tags = _HTML_TAG_RE.sub(" ", without_latex)
    text = html.unescape(without_tags)
    text = text.replace("|", " ")
    text = re.sub(r"\([^)]*(?:шт|л/т|мг/л|м/ч|%)\.?\)", " ", text, flags=re.IGNORECASE)
    text = re.sub(r"\s+", " ", text)
    return normalize_ocr_confusions(text.strip(" \t\r\n-–—:;,."))


def table_header_text(match: re.Match[str]) -> str:
    table = match.group(0)
    candidates = re.findall(
        r"<t[dh]\b[^>]*(?:colspan|rowspan)[^>]*>(.*?)</t[dh]>",
        table,
        flags=re.IGNORECASE | re.DOTALL,
    )
    if not candidates:
        candidates = re.findall(r"<t[dh]\b[^>]*>(.*?)</t[dh]>", table, flags=re.IGNORECASE | re.DOTALL)[:2]
    cleaned = [strip_table_cell(candidate) for candidate in candidates]
    text_candidates = [candidate for candidate in cleaned if has_cyrillic(candidate) and not mostly_numeric(candidate)]
    return " ".join(text_candidates[:2])


def strip_table_cell(cell: str) -> str:
    text = _HTML_TAG_RE.sub(" ", _LATEX_RE.sub(" ", cell))
    return re.sub(r"\s+", " ", html.unescape(text)).strip(" \t\r\n-–—:;,.")


def normalize_ocr_confusions(text: str) -> str:
    replacements = (
        (r"\b0твальн", "отвальн"),
        (r"\bхпост", "хвост"),
        (r"основнал", "основная"),
        (r"фпотаци", "флотаци"),
        (r"первял", "первая"),
        (r"меднал", "медная"),
        (r"реакентн", "реагентн"),
    )
    normalized = text.casefold()
    for pattern, replacement in replacements:
        normalized = re.sub(pattern, replacement, normalized, flags=re.IGNORECASE)
    return normalized


def canonical_stage_label(text: str) -> str:
    folded = text.casefold()

    stage_number = search_numbered_stage(folded, "основная коллективная флотация")
    if stage_number:
        return f"{stage_number} основная коллективная флотация"

    stage_number = search_numbered_stage(folded, "основная флотация")
    if stage_number:
        return f"{stage_number} основная флотация"

    if re.search(r"\b1\s+перечистн\w+\s+флотаци", folded):
        return "1 перечистная флотация"

    crushing_stage = re.search(r"\bдробление\s+([123])\s+ст\.?", folded)
    if crushing_stage:
        return f"Дробление {crushing_stage.group(1)} ст."

    if re.search(r"\bгрохочение\s+двухситн\w+\s+грохот\s+sibra\s*2dr", folded):
        return "Грохочение Двухситный грохот Sibra 2DR"
    if re.search(r"\bгрохочение\s+колосников\w+\s+грохот", folded):
        return "Грохочение Колосниковый грохот"

    fixed_labels = (
        ("питание флотаци", "Питание флотации"),
        ("классификация", "Классификация"),
        ("гравитационное обогащение", "Гравитационное обогащение"),
        ("доизмельчение", "Доизмельчение"),
        ("пропарка", "Пропарка"),
        ("перечистка первая медная", "Перечистка первая медная"),
        ("перечистка вторая медная", "Перечистка вторая медная"),
        ("медный концентрат", "Медный концентрат"),
        ("никелевый концентрат", "Никелевый концентрат"),
        ("отвальные хвосты", "Отвальные хвосты"),
        ("в бункеры ифц", "В бункеры ИФЦ"),
        ("шламопровод в ифц", "Шламопровод в ИФЦ"),
        ("карьер", "карьер"),
        ("шахта", "шахта"),
    )
    for needle, label in fixed_labels:
        if needle in folded:
            return label
    return ""


def search_numbered_stage(folded: str, label: str) -> str:
    escaped_words = r"\s+".join(re.escape(part) for part in label.split())
    match = re.search(rf"\b([123])\s+{escaped_words}", folded)
    return match.group(1) if match else ""


def compact_freeform_label(text: str) -> str:
    text = re.sub(r"\([^)]*\)", " ", text)
    text = re.sub(r"\b\d+(?:[.,]\d+)?\b", " ", text)
    text = re.sub(r"\s+", " ", text)
    return text.strip(" \t\r\n-–—:;,.")


def is_meaningful_label(text: str) -> bool:
    if len(text) < 3 or len(text) > 80:
        return False
    letters = len(re.findall(r"[A-Za-zА-Яа-яЁё]", text))
    digits = len(re.findall(r"\d", text))
    return letters >= 3 and digits <= max(3, letters) and has_cyrillic(text)


def is_non_node_label(text: str) -> bool:
    folded = text.casefold()
    blocked_fragments = (
        "вода в операцию",
        "реагентный режим",
        "пески",
        "конц т",
        "хвосты",
    )
    return any(fragment in folded for fragment in blocked_fragments)


def has_cyrillic(text: str) -> bool:
    return bool(re.search(r"[А-Яа-яЁё]", text))


def mostly_numeric(text: str) -> bool:
    letters = len(re.findall(r"[A-Za-zА-Яа-яЁё]", text))
    digits = len(re.findall(r"\d", text))
    return digits > letters * 2
