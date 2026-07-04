// Assembling the smart answer (inline [Sn] citation markers → clickable
// footnotes) from a chat message. Подсветка совпадений — в shared/lib/highlight.
import type { AnswerChip, SmartAnswer, SmartAnswerSource } from "@/shared/types";
import type { ApiSource } from "@/features/search/api";
import { dedupeSources, docFromSource } from "@/features/document";

const MARKER = /\[\s*(S\d+(?:\s*,\s*S\d+)*)\s*\]/g;

/** Собирает «умный ответ» из текста ассистента: Markdown остаётся как есть
 *  (его рендерит RichText — GFM + формулы KaTeX), а инлайн-маркеры [Sn]
 *  заменяются ссылками [n](#cite-i) — они становятся кликабельными сносками.
 *  Номер сноски совпадает с номером карточки источника (несколько цитат одного
 *  документа получают один номер, но разные фрагменты). */
export function buildSmartAnswer(content: string, raw: ApiSource[]): SmartAnswer | null {
  const text = content.trim();
  if (!text) return null;
  const scored = dedupeSources(raw);
  const sources: SmartAnswerSource[] = scored.map((s) => ({
    doc: s.doc,
    fragmentId: s.fragmentId,
    relevance: s.relevance,
    page: s.page,
    section: s.section,
  }));
  const numberByDoc = new Map<string, number>(sources.map((s, i) => [s.doc.id, i + 1]));

  const chipFor = (marker: string): AnswerChip | null => {
    const n = Number(marker.slice(1));
    const src = raw[n - 1];
    if (!src) return null;
    let label = numberByDoc.get(src.document_id);
    if (label === undefined) {
      // Источник процитирован, но отфильтрован из витрины (например,
      // библиография) — дорисовываем его в конец списка.
      sources.push({ doc: docFromSource(src), fragmentId: src.chunk_id });
      label = sources.length;
      numberByDoc.set(src.document_id, label);
    }
    return { label, docId: src.document_id, fragmentId: src.chunk_id };
  };

  const chips: AnswerChip[] = [];
  let markdown = "";
  let last = 0;
  // Дедупликация внутри «пачки» подряд идущих маркеров: [S1][S1, S2] → 1, 2.
  let runLabels = new Set<number>();
  for (const m of text.matchAll(MARKER)) {
    const before = text
      .slice(last, m.index)
      .replace(/[ \t]+([,.;:!?])/g, "$1")
      .replace(/[ \t]+$/g, "");
    if (before !== "") runLabels = new Set();
    markdown += before;
    // Группа 1 обязательна в MARKER; «??» — только для типов.
    for (const part of (m[1] ?? "").split(",")) {
      const chip = chipFor(part.trim());
      if (!chip || runLabels.has(chip.label)) continue;
      runLabels.add(chip.label);
      markdown += `[${chip.label}](#cite-${chips.length})`;
      chips.push(chip);
    }
    last = (m.index ?? 0) + m[0].length;
  }
  markdown += text.slice(last).replace(/[ \t]+([,.;:!?])/g, "$1");

  const plain = text
    .replace(MARKER, "")
    .replace(/[ \t]+([,.;:!?])/g, "$1")
    .trim();
  return { markdown, plain, chips, sources };
}
