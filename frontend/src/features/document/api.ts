// Document ingestion + storage endpoints (upload, parsing pipeline, chunks,
// original-file preview, large multipart uploads).
import { ApiError, authFetch, postJSON, request } from "@/shared/api/client";

/** Форматы, которые умеет разбирать пайплайн индексации (архивы распаковывает
 *  archive-scan, содержимое становится отдельными документами). */
export const UPLOAD_ACCEPT =
  ".pdf,.doc,.docx,.eml,.msg,.xls,.xlsx,.ppt,.pptx,.txt,.md,.png,.jpg,.jpeg,.webp,.tiff,.db,.sqlite,.sqlite3,.zip,.rar,.7z,.tar,.gz,.tgz";

export type DocStatus =
  | "uploaded"
  | "queued"
  | "parsing"
  | "ocr"
  | "parsed"
  | "chunking"
  | "indexed"
  | "failed";

export interface ApiDocument {
  id: string;
  owner_id: string;
  filename: string;
  /** Настоящее название статьи, извлечённое из текста при индексации.
   *  Пустое/отсутствует — заголовок ещё не извлечён (UI показывает имя файла). */
  title?: string;
  /** Метаданные из парсера (email-заголовки, PDF docinfo); пусто — не извлечено. */
  author?: string;
  published_at?: string;
  source_ref?: string;
  /** Класс документа, определённый при индексации: "hypotheses" — готовые
   *  гипотезы/идеи (итоги мозгового штурма). Такие документы исключены из
   *  генерации и проверки гипотез. Пусто — обычный документ. */
  kind?: string;
  mime_type: string;
  size: number;
  object_key: string;
  status: DocStatus;
  status_msg?: string;
  chunk_count: number;
  created_at: string;
  updated_at: string;
  /** true — байт-в-байт такой документ уже был в базе (вернулся существующий). */
  duplicate?: boolean;
}

export interface ApiChunk {
  id: string;
  index: number;
  text: string;
}

export interface ApiDocumentChunks {
  document: ApiDocument;
  chunks: ApiChunk[];
}

/** Live ingestion progress event pushed over the /ws socket. */
export interface IngestionEvent {
  document_id: string;
  owner_id: string;
  status: DocStatus;
  message?: string;
  timestamp: string;
  /** Прогресс внутри стадии (страницы OCR, батчи индексации); 0 — неизвестен. */
  stage_current?: number;
  stage_total?: number;
}

export function getDocument(id: string): Promise<ApiDocument> {
  return request<ApiDocument>(`/documents/${id}`);
}

export function getDocumentChunks(id: string): Promise<ApiDocumentChunks> {
  return request<ApiDocumentChunks>(`/documents/${id}/chunks`);
}

export function uploadDocument(file: File, reuse = false): Promise<ApiDocument> {
  const form = new FormData();
  form.append("file", file);
  const path = reuse ? "/documents?reuse=1" : "/documents";
  return request<ApiDocument>(path, { method: "POST", body: form });
}

/** Loads the original stored file (PDF, image, …) as an object URL so the UI can
 *  preview the document in its true form. Caller must URL.revokeObjectURL(url)
 *  when the preview closes. */
export async function fetchDocumentContent(id: string): Promise<{ url: string; mime: string }> {
  const resp = await authFetch(`/documents/${id}/content`);
  if (!resp.ok) throw new ApiError(resp.status, `${resp.status} ${resp.statusText}`);
  const mime = resp.headers.get("Content-Type") ?? "application/octet-stream";
  return { url: URL.createObjectURL(await resp.blob()), mime };
}

// ---- upload sessions (large files directly to S3) ----

export interface UploadSession {
  upload_id: string;
  part_size: number;
  part_count: number;
}

export interface UploadPartRef {
  part_number: number;
  etag: string;
}

export function beginUpload(
  filename: string,
  size: number,
  mimeType: string,
  reuse = false,
): Promise<UploadSession> {
  return postJSON<UploadSession>("/uploads", { filename, size, mime_type: mimeType, reuse });
}

export function getUploadPartUrls(
  uploadId: string,
  from: number,
  count: number,
): Promise<{ urls: { part_number: number; url: string }[] }> {
  return request(`/uploads/${uploadId}/parts?from=${from}&count=${count}`);
}

export function completeUpload(uploadId: string, parts: UploadPartRef[]): Promise<ApiDocument> {
  return postJSON<ApiDocument>(`/uploads/${uploadId}/complete`, { parts });
}

export function abortUpload(uploadId: string): Promise<void> {
  return request<void>(`/uploads/${uploadId}`, { method: "DELETE" });
}
