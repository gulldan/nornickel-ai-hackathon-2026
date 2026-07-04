import { useCallback, useMemo, useRef, useState } from "react";
import { uploadSmart, UploadCancelled } from "./upload";
import { useIngestionEvents } from "./useIngestionEvents";
import type { ApiDocument, IngestionEvent } from "./api";

type AttachedPhase = "uploading" | "indexing" | "indexed" | "failed";

export interface AttachedUpload {
  key: string;
  name: string;
  doc: ApiDocument | null;
  phase: AttachedPhase;
  fraction: number;
  alreadyExisted: boolean;
  error?: string;
}

interface Entry extends AttachedUpload {
  abort: AbortController;
}

function fileKey(f: File): string {
  return `${f.name}-${f.size}-${f.lastModified}`;
}

/** Файлы, приложенные в модалке цели: каждый грузится сразу (reuse-дедуп на
 *  бэке), фаза переходит uploading → indexing → indexed/failed по WS-событиям. */
export function useAttachedUploads() {
  const [items, setItems] = useState<Entry[]>([]);
  const itemsRef = useRef(items);
  itemsRef.current = items;

  const patch = useCallback((key: string, changes: Partial<Entry>) => {
    setItems((prev) => prev.map((it) => (it.key === key ? { ...it, ...changes } : it)));
  }, []);

  useIngestionEvents(
    useCallback(
      (ev: IngestionEvent) => {
        const hit = itemsRef.current.find((it) => it.doc?.id === ev.document_id);
        if (!hit || hit.phase === "indexed" || hit.phase === "failed") return;
        if (ev.status === "indexed") {
          patch(hit.key, { phase: "indexed" });
        } else if (ev.status === "failed") {
          patch(hit.key, { phase: "failed", error: ev.message });
        }
      },
      [patch],
    ),
  );

  const addFiles = useCallback(
    (files: FileList | File[]) => {
      for (const file of Array.from(files)) {
        const key = fileKey(file);
        if (itemsRef.current.some((it) => it.key === key)) continue;
        const abort = new AbortController();
        const entry: Entry = {
          key,
          name: file.name,
          doc: null,
          phase: "uploading",
          fraction: 0,
          alreadyExisted: false,
          abort,
        };
        setItems((prev) => [...prev, entry]);
        void uploadSmart(file, (p) => patch(key, { fraction: p.fraction }), abort.signal, true)
          .then((doc) => {
            const done = doc.duplicate === true || doc.status === "indexed";
            patch(key, {
              doc,
              alreadyExisted: doc.duplicate === true,
              phase: done ? "indexed" : "indexing",
              fraction: 1,
            });
          })
          .catch((err: unknown) => {
            if (err instanceof UploadCancelled) return;
            patch(key, { phase: "failed", error: err instanceof Error ? err.message : undefined });
          });
      }
    },
    [patch],
  );

  const remove = useCallback((key: string) => {
    setItems((prev) => {
      const hit = prev.find((it) => it.key === key);
      if (hit && hit.phase === "uploading") hit.abort.abort();
      return prev.filter((it) => it.key !== key);
    });
  }, []);

  const reset = useCallback(() => {
    setItems((prev) => {
      for (const it of prev) if (it.phase === "uploading") it.abort.abort();
      return [];
    });
  }, []);

  const docIds = useMemo(
    () =>
      items
        .filter((it) => it.phase !== "failed" && it.doc !== null)
        .map((it) => (it.doc as ApiDocument).id),
    [items],
  );
  const allUploaded = items.every((it) => it.doc !== null || it.phase === "failed");
  const allIndexed = items.every((it) => it.phase === "indexed" || it.phase === "failed");

  return {
    items: items as AttachedUpload[],
    addFiles,
    remove,
    reset,
    docIds,
    allUploaded,
    allIndexed,
  };
}
