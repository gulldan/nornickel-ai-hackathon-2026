import { formatDate } from "@/shared/lib/format";
import { KbDoc } from "@/shared/types";

/** A block of the document's full content for reading mode. */
export type DocBlock =
  | { kind: "heading"; text: string }
  | {
      kind: "paragraph";
      text: string;
      fragmentId?: string;
    };

export type ReaderContent =
  | { type: "pdf"; pages: DocBlock[][] }
  | {
      type: "docx";
      blocks: DocBlock[];
    }
  | { type: "eml"; blocks: DocBlock[] };

const FILLER = [
  "Фрагмент относится к научному корпусу и используется для проверки ответов, гипотез и связей между источниками.",
  "Термины и обозначения сохраняются в формулировке исходного источника, чтобы вывод можно было сопоставить с публикацией.",
  "Интерпретация результата зависит от экспериментальных условий, материала, метода измерения и ограничений набора данных.",
  "Если источник содержит несколько близких утверждений, система показывает релевантный фрагмент и соседний контекст.",
  "Выводы по корпусу требуют экспертной проверки: автоматическая оценка помогает сузить область чтения, но не заменяет эксперимент.",
  "При обновлении корпуса документ может получить новые связи с темами, гипотезами и доказательными фрагментами.",
  "Для спорных результатов стоит открыть оригинальный источник и проверить методику, размер выборки и воспроизводимость.",
  "Оценки релевантности и готовности зависят от состава загруженных публикаций и могут измениться после реиндексации.",
  "Страницы, разделы и фрагменты используются как навигация по источнику, а не как отдельная публикация.",
  "Сравнение источников помогает увидеть, где корпус поддерживает гипотезу, а где содержит ограничения или противоречия.",
];

const EMAIL_FILLER = [
  "Фрагмент рассылки сохранён как источник корпуса и доступен для поиска вместе с публикациями.",
  "Проверьте первичный источник перед тем, как использовать вывод в плане эксперимента.",
  "Подробный контекст доступен в карточке документа и связанных фрагментах.",
  "Новые источники могут изменить ранжирование гипотез и карту направлений.",
];

function fillers(pool: string[], seed: number, n: number): DocBlock[] {
  return Array.from({ length: n }, (_, i) => ({
    kind: "paragraph" as const,
    text: pool[(seed + i) % pool.length] ?? "",
  }));
}

function seedOf(doc: KbDoc): number {
  return doc.id.split("").reduce((a, ch) => a + ch.charCodeAt(0), 0);
}

function intro(doc: KbDoc): string {
  const date = formatDate(doc.updatedAt);
  return `Источник научного корпуса. Раздел: «${doc.section}». Дата обновления: ${date}. ${doc.snippet}`;
}

/** Content of a real document: its actual chunks in order, no synthetic filler. */
function realReaderContent(doc: KbDoc): ReaderContent {
  const blocks: DocBlock[] = doc.fragments.map((f) => ({
    kind: "paragraph" as const,
    text: f.text,
    fragmentId: f.id,
  }));
  if (doc.fileType === "eml") {
    return { type: "eml", blocks };
  }
  // A single "page" with a heading — a cover for any format.
  if (doc.fileType === "pdf") {
    return { type: "pdf", pages: [[{ kind: "heading", text: doc.title }, ...blocks]] };
  }
  return { type: "docx", blocks };
}

/** Full document content: the original "answering" fragments are embedded into the text at their places. */
export function getReaderContent(doc: KbDoc): ReaderContent {
  if (doc.real) {
    return realReaderContent(doc);
  }
  const seed = seedOf(doc);

  if (doc.fileType === "pdf") {
    const maxPage = Math.max(1, ...doc.fragments.map((f) => f.page ?? 1));
    const total = maxPage + 1;
    const pages: DocBlock[][] = [];
    for (let p = 1; p <= total; p++) {
      const blocks: DocBlock[] = [];
      if (p === 1) {
        blocks.push({ kind: "heading", text: doc.title });
        blocks.push({ kind: "paragraph", text: intro(doc) });
      }
      blocks.push(...fillers(FILLER, seed + p * 2, p === 1 ? 1 : 2));
      for (const f of doc.fragments.filter((fr) => (fr.page ?? 1) === p)) {
        blocks.push({ kind: "paragraph", text: f.text, fragmentId: f.id });
      }
      blocks.push(...fillers(FILLER, seed + p * 3 + 5, 1));
      if (p === total) {
        blocks.push({ kind: "heading", text: "Заключительные положения" });
        blocks.push(...fillers(FILLER, seed + 7, 2));
      }
      pages.push(blocks);
    }
    return { type: "pdf", pages };
  }

  if (doc.fileType === "docx") {
    const blocks: DocBlock[] = [];
    blocks.push({ kind: "heading", text: "Общие положения" });
    blocks.push({ kind: "paragraph", text: intro(doc) });
    blocks.push(...fillers(FILLER, seed, 1));
    doc.fragments.forEach((f, i) => {
      blocks.push({ kind: "heading", text: f.heading ?? f.location });
      blocks.push(...fillers(FILLER, seed + i * 2 + 1, 1));
      blocks.push({ kind: "paragraph", text: f.text, fragmentId: f.id });
      blocks.push(...fillers(FILLER, seed + i * 2 + 4, 1));
    });
    blocks.push({ kind: "heading", text: "Заключительные положения" });
    blocks.push(...fillers(FILLER, seed + 6, 2));
    return { type: "docx", blocks };
  }

  // eml
  const senderName = doc.emailMeta?.from.split("<")[0]?.trim() ?? "Команда поддержки";
  const blocks: DocBlock[] = [];
  blocks.push({ kind: "paragraph", text: "Коллеги, добрый день!" });
  blocks.push({ kind: "paragraph", text: doc.snippet });
  doc.fragments.forEach((f, i) => {
    blocks.push({ kind: "paragraph", text: f.text, fragmentId: f.id });
    if (i < doc.fragments.length - 1) {
      blocks.push(...fillers(EMAIL_FILLER, seed + i, 1));
    }
  });
  blocks.push(...fillers(EMAIL_FILLER, seed + 2, 1));
  blocks.push({ kind: "paragraph", text: "Хорошего дня!" });
  blocks.push({ kind: "paragraph", text: `— ${senderName}` });
  return { type: "eml", blocks };
}
