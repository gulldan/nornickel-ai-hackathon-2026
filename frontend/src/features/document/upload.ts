// Large-file upload manager: the user sees a single progress bar, while under
// the hood the file goes up in parts directly to S3 via presigned URLs (bypassing
// the edge and its limits). Small files use a plain POST /documents — uploadSmart
// makes the choice, the caller need not think about it.

import { i18n } from "@/shared/i18n";
import {
  abortUpload,
  beginUpload,
  completeUpload,
  getUploadPartUrls,
  uploadDocument,
  type ApiDocument,
  type UploadPartRef,
} from "./api";

/**
 * Direct-upload threshold: files no larger than this use a plain POST /documents,
 * larger ones go multipart directly to S3 (bypassing the edge and its request-body limit).
 *
 * IMPORTANT: this threshold must stay <= the server's direct-upload body limit —
 * main-service caps POST /documents via http.MaxBytesReader at MAX_UPLOAD_MB
 * (code default 50 MiB in cmd/server/main.go; infra/docker-compose.yml overrides
 * it to 512). A threshold above the server limit creates a "dead zone": such
 * files go via direct POST and the server rejects the body. 50 MiB satisfies the
 * invariant for both defaults; larger files go multipart to S3 anyway, which
 * gives per-part retries and granular progress.
 */
const DIRECT_LIMIT_BYTES = 50 * 1024 * 1024;

/** Parts in flight at once: gives speed without choking the browser. */
const PART_CONCURRENCY = 3;
/** Retries of a single part before giving up. */
const PART_RETRIES = 3;
/** How many presigned URLs to request per round-trip. */
const URL_BATCH = 200;

export interface UploadProgress {
  /** 0..1 — fraction of bytes transferred. */
  fraction: number;
  uploadedBytes: number;
  totalBytes: number;
}

export class UploadCancelled extends Error {
  constructor() {
    super(i18n.t("document:upload.cancelled"));
  }
}

/** PUT a single part with progress (fetch can't do upload-progress — XHR). */
function putPart(
  url: string,
  blob: Blob,
  onBytes: (sent: number) => void,
  signal: AbortSignal,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let lastSent = 0;
    const onAbort = () => {
      xhr.abort();
      reject(new UploadCancelled());
    };
    signal.addEventListener("abort", onAbort, { once: true });
    xhr.upload.addEventListener("progress", (e) => {
      onBytes(e.loaded - lastSent);
      lastSent = e.loaded;
    });
    xhr.addEventListener("load", () => {
      signal.removeEventListener("abort", onAbort);
      if (xhr.status >= 200 && xhr.status < 300) {
        const etag = xhr.getResponseHeader("ETag");
        if (!etag) {
          reject(new Error(i18n.t("document:upload.noEtag")));
          return;
        }
        resolve(etag);
      } else {
        reject(new Error(i18n.t("document:upload.partHttp", { status: xhr.status })));
      }
    });
    xhr.addEventListener("error", () => {
      signal.removeEventListener("abort", onAbort);
      reject(new Error(i18n.t("document:upload.partNetwork")));
    });
    xhr.open("PUT", url);
    xhr.send(blob);
  });
}

async function putPartWithRetry(
  url: string,
  blob: Blob,
  onBytes: (sent: number) => void,
  signal: AbortSignal,
): Promise<string> {
  let lastError: unknown;
  for (let attempt = 1; attempt <= PART_RETRIES; attempt++) {
    if (signal.aborted) throw new UploadCancelled();
    try {
      return await putPart(url, blob, onBytes, signal);
    } catch (err) {
      if (err instanceof UploadCancelled) throw err;
      lastError = err;
      // We don't try to roll back the progress of a failed attempt: the next
      // attempt will start sending bytes again, onBytes will swing minus-then-plus —
      // for UX that's less noticeable noise than a progress bar jumping backwards.
      await new Promise((r) => setTimeout(r, attempt * 1000));
    }
  }
  throw lastError instanceof Error ? lastError : new Error(i18n.t("document:upload.partFailed"));
}

/** Large file: session → parts → completion. */
async function uploadLarge(
  file: File,
  onProgress: (p: UploadProgress) => void,
  signal: AbortSignal,
  reuse = false,
): Promise<ApiDocument> {
  const session = await beginUpload(file.name, file.size, file.type, reuse);
  let uploaded = 0;
  const report = (delta: number) => {
    uploaded += delta;
    onProgress({
      fraction: Math.min(1, uploaded / file.size),
      uploadedBytes: Math.min(uploaded, file.size),
      totalBytes: file.size,
    });
  };

  const etags = new Map<number, string>();
  try {
    for (let batchFrom = 1; batchFrom <= session.part_count; batchFrom += URL_BATCH) {
      const { urls } = await getUploadPartUrls(session.upload_id, batchFrom, URL_BATCH);
      // A window of PART_CONCURRENCY simultaneous parts within the batch.
      let next = 0;
      const worker = async () => {
        for (;;) {
          const part = urls[next++];
          if (part === undefined) return;
          const { part_number, url } = part;
          const start = (part_number - 1) * session.part_size;
          const blob = file.slice(start, Math.min(start + session.part_size, file.size));
          const etag = await putPartWithRetry(url, blob, report, signal);
          etags.set(part_number, etag);
        }
      };
      await Promise.all(Array.from({ length: Math.min(PART_CONCURRENCY, urls.length) }, worker));
    }

    const parts: UploadPartRef[] = [...etags.entries()]
      .toSorted((a, b) => a[0] - b[0])
      .map(([part_number, etag]) => ({ part_number, etag }));
    return await completeUpload(session.upload_id, parts);
  } catch (err) {
    // Best-effort cleanup; swallow cancellation errors — the session expires by TTL.
    void abortUpload(session.upload_id).catch(() => undefined);
    throw err;
  }
}

/** Picks the path by size; the calling interface doesn't care which one. */
export async function uploadSmart(
  file: File,
  onProgress: (p: UploadProgress) => void,
  signal: AbortSignal,
  reuse = false,
): Promise<ApiDocument> {
  if (file.size <= DIRECT_LIMIT_BYTES) {
    // Plain path: progress in a single jump — small files go up fast.
    const doc = await uploadDocument(file, reuse);
    onProgress({ fraction: 1, uploadedBytes: file.size, totalBytes: file.size });
    return doc;
  }
  return uploadLarge(file, onProgress, signal, reuse);
}
