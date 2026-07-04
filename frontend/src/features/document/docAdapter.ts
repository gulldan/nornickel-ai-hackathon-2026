// Adapters between the backend wire types and the UI's KbDoc vocabulary. Real
// documents carry `real: true`, so the reader pane renders their actual indexed
// chunks instead of synthesising demo content.

import type { ApiSource } from "@/features/search";
import { i18n } from "@/shared/i18n";
import { formatDate } from "@/shared/lib/format";
import type { DocFragment, FileType, KbDoc } from "@/shared/types";
import type { ApiChunk, ApiDocument } from "./api";

/** Maps a filename (and MIME as fallback) onto the UI file-type badge. */
export function fileTypeOf(filename: string, mime?: string): FileType {
  // Бэкенд толерантен к отсутствующим полям — на рантайме filename может не прийти.
  const ext = (filename ?? "").toLowerCase().split(".").pop() ?? "";
  switch (ext) {
    case "pdf":
      return "pdf";
    case "doc":
    case "docx":
      return "docx";
    case "eml":
    case "msg":
      return "eml";
    case "xls":
    case "xlsx":
      return "xlsx";
    case "ppt":
    case "pptx":
      return "pptx";
    case "txt":
    case "md":
      return "txt";
    case "png":
    case "jpg":
    case "jpeg":
    case "webp":
    case "tiff":
      return "image";
    case "db":
    case "sqlite":
    case "sqlite3":
      return "db";
    default:
      break;
  }
  const mt = (mime ?? "").toLowerCase();
  if (mt.includes("pdf")) return "pdf";
  if (mt.startsWith("image/")) return "image";
  if (mt.includes("wordprocessingml") || mt.includes("msword")) return "docx";
  if (mt.includes("spreadsheetml") || mt.includes("excel")) return "xlsx";
  if (mt.includes("presentationml") || mt.includes("powerpoint")) return "pptx";
  if (mt === "message/rfc822") return "eml";
  if (mt.includes("sqlite")) return "db";
  if (mt.startsWith("text/")) return "txt";
  return "other";
}

/** Human-readable document title — the file name without its extension. */
export function titleOf(filename: string): string {
  const name = filename ?? "";
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(0, dot) : name;
}

/** Title to display for a document: the real article title extracted from the
 *  file when the backend has one, otherwise the file name without its extension. */
export function documentTitle(d: Pick<ApiDocument, "title" | "filename">): string {
  return d.title?.trim() || titleOf(d.filename);
}

/** Дата публикации для показа: локализованная, если распарсилась, иначе сырьё
 *  как есть; "" — даты нет. */
export function publishedLabel(raw?: string): string {
  const value = raw?.trim() ?? "";
  if (!value) return "";
  const formatted = formatDate(value);
  return formatted === "—" ? value : formatted;
}

/** «стр. N» or «стр. N–M» from source provenance; "" if the page is unknown
 *  (field omitted or 0). Simple human-readable format — as in search results. */
function pageLabel(s: ApiSource): string {
  const start = s.page_start ?? 0;
  if (start <= 0) return "";
  const end = s.page_end ?? 0;
  return end > start
    ? i18n.t("document:adapter.pageRangeN", { start, end })
    : i18n.t("document:adapter.pageN", { page: start });
}

/** Location of the cited fragment in the document for the source caption:
 *  «стр. N», section, both joined by «·», or the fallback «Найденный фрагмент». */
export function sourceLocation(s: ApiSource): string {
  const heading = s.section_heading?.trim() ?? "";
  const page = pageLabel(s);
  const parts = [page, heading].filter((p) => p.length > 0);
  return parts.length > 0 ? parts.join(" · ") : i18n.t("document:adapter.foundFragment");
}

function fragmentOfChunk(c: ApiChunk): DocFragment {
  return {
    id: c.id,
    text: c.text,
    location: i18n.t("document:adapter.fragmentN", { index: c.index + 1 }),
  };
}

/** KbDoc from document metadata (no content — fragments are loaded separately). */
function docFromApi(d: ApiDocument): KbDoc {
  return {
    id: d.id,
    real: true,
    title: documentTitle(d),
    snippet: "",
    section: i18n.t("document:adapter.uploadedSection"),
    sourceId: "backend",
    sourceName: i18n.t("document:adapter.corpus"),
    updatedAt: d.updated_at,
    tags: [],
    fileType: fileTypeOf(d.filename, d.mime_type),
    fileName: d.filename,
    author: d.author?.trim() || undefined,
    publishedAt: d.published_at?.trim() || undefined,
    fragments: [],
  };
}

/** KbDoc with real chunks — full reading mode. */
export function docWithChunks(d: ApiDocument, chunks: ApiChunk[]): KbDoc {
  return {
    ...docFromApi(d),
    snippet: chunks[0]?.text.slice(0, 180) ?? "",
    fragments: chunks.map(fragmentOfChunk),
  };
}

/** KbDoc from a RAG-answer source: enough for the result card;
 *  the full text is loaded when the preview is opened. */
export function docFromSource(s: ApiSource): KbDoc {
  return {
    id: s.document_id,
    real: true,
    title: titleOf(s.filename),
    snippet: s.snippet,
    section: i18n.t("document:adapter.foundInCorpus"),
    sourceId: "backend",
    sourceName: i18n.t("document:adapter.corpus"),
    updatedAt: "",
    tags: [],
    fileType: fileTypeOf(s.filename),
    fileName: s.filename,
    fragments: [
      {
        id: s.chunk_id,
        text: s.snippet,
        location: sourceLocation(s),
        page: s.page_start && s.page_start > 0 ? s.page_start : undefined,
        heading: s.section_heading?.trim() || undefined,
      },
    ],
  };
}

export interface ScoredSourceDoc {
  doc: KbDoc;
  /** Id of the cited chunk (for highlighting in the preview). */
  fragmentId: string;
  /** Реальный скор соответствия в процентах (0–100), без нормировок;
   *  отсутствует, когда скор не откалиброван (реранжирование не работало). */
  relevance?: number;
  score: number;
  /** «стр. N» / «стр. N–M» if the page is known, otherwise "" (source provenance). */
  page: string;
  /** Section heading of the cited fragment, otherwise "" (source provenance). */
  section: string;
}

function isBibliographySource(s: ApiSource): boolean {
  const heading = (s.section_heading ?? "").trim().toLowerCase();
  return /^(references?|bibliography|works cited|литература|список литературы)$/.test(heading);
}

/** Deduplicates sources by document (best score wins). Relevance — честный
 *  калиброванный скор реранкера (0..1 → проценты); вне этой шкалы (деградация
 *  до порядка слияния) процент не показывается вовсе. */
export function dedupeSources(sources: ApiSource[]): ScoredSourceDoc[] {
  const byDoc = new Map<string, ApiSource>();
  for (const s of sources) {
    const prev = byDoc.get(s.document_id);
    if (
      !prev ||
      (isBibliographySource(prev) && !isBibliographySource(s)) ||
      (isBibliographySource(prev) === isBibliographySource(s) && s.score > prev.score)
    ) {
      byDoc.set(s.document_id, s);
    }
  }
  const list = [...byDoc.values()].toSorted((a, b) => b.score - a.score);
  const nonBibliography = list.filter((s) => !isBibliographySource(s));
  const displayList = nonBibliography.length >= 3 ? nonBibliography : list;
  return displayList.map((s) => ({
    doc: docFromSource(s),
    fragmentId: s.chunk_id,
    relevance: s.score > 0 && s.score <= 1 ? Math.round(s.score * 100) : undefined,
    score: s.score,
    page: pageLabel(s),
    section: s.section_heading?.trim() ?? "",
  }));
}
