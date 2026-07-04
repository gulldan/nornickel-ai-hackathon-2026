export type Role = "user" | "operator" | "admin";

export type FileType = "pdf" | "docx" | "eml" | "xlsx" | "pptx" | "txt" | "image" | "db" | "other";

export interface DocFragment {
  id: string;
  text: string;
  /** Human-readable location in the document: «стр. 4», «Раздел 2.1 …», «Тело письма» */
  location: string;
  /** For PDF — the page number, to draw page separators */
  page?: number;
  /** For Word — the section heading before the fragment */
  heading?: string;
}

interface EmailMeta {
  from: string;
  to: string;
  subject: string;
  date: string;
}

export interface KbDoc {
  id: string;
  /** Document from the real backend: fragments are actual index chunks,
   *  reading mode does not add synthetic text around them. */
  real?: boolean;
  title: string;
  snippet: string;
  section: string;
  sourceId: string;
  sourceName: string;
  updatedAt: string;
  tags: string[];
  fileType: FileType;
  fileName: string;
  /** Метаданные из парсера (email-заголовки, PDF docinfo); пусто — не извлечено. */
  author?: string;
  publishedAt?: string;
  emailMeta?: EmailMeta;
  fragments: DocFragment[];
}

export interface SmartAnswerSource {
  doc: KbDoc;
  fragmentId: string;
  /** Реальный скор соответствия (0–100 %); отсутствует, если переранжирование не работало. */
  relevance?: number;
  /** Человекочитаемое место в документе: «стр. 4», раздел. */
  page?: string;
  section?: string;
}

/** Кликабельная сноска в тексте ответа: номер источника из списка ниже. */
export interface AnswerChip {
  label: number;
  docId: string;
  fragmentId: string;
}

export interface SmartAnswer {
  /** Markdown ответа; маркеры [Sn] заменены ссылками вида [n](#cite-i). */
  markdown: string;
  /** Чистый текст для копирования — без сносок. */
  plain: string;
  /** Сноски по индексу i из ссылок #cite-i. */
  chips: AnswerChip[];
  sources: SmartAnswerSource[];
}
