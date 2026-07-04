// Shared document-preview controller: opens a KbDoc in the preview, lazily
// loads its real indexed chunks (so the cited fragment can be highlighted),
// and exposes retry/close. Reused by the citation/evidence previews so the
// "open the source and highlight where it was taken from" flow is identical
// everywhere (search results, hypothesis evidence, citation graph).

import { useCallback, useState } from "react";
import { getDocumentChunks } from "./api";
import { docWithChunks, fileTypeOf, titleOf } from "./docAdapter";
import type { PdfHighlight } from "./ui/PdfViewer";
import { i18n } from "@/shared/i18n";
import type { KbDoc } from "@/shared/types";

export interface DocPreviewState {
  /** Document for the preview header — available immediately (stub or full). */
  doc: KbDoc;
  /** Cited chunk to highlight as the answering fragment. */
  fragmentId?: string;
  /** Cited fragment to scroll to + highlight in the original PDF. */
  highlight?: PdfHighlight;
  /** Full document with real chunks (after getDocumentChunks). */
  full: KbDoc | null;
  /** Chunks failed to load — show an inline error with Retry. */
  failed: boolean;
}

export interface OpenCitationOpts {
  documentId: string;
  /** Cited chunk id — highlighted in the text view. */
  chunkId?: string;
  filename?: string;
  snippet?: string;
  /** 1-based page of the fragment in the source PDF — jumps the viewer there. */
  page?: number;
}

export function useDocPreview() {
  const [preview, setPreview] = useState<DocPreviewState | null>(null);

  const loadChunks = useCallback(async (docId: string) => {
    try {
      const { document, chunks } = await getDocumentChunks(docId);
      setPreview((p) =>
        p && p.doc.id === docId
          ? { ...p, full: docWithChunks(document, chunks), failed: false }
          : p,
      );
    } catch {
      setPreview((p) => (p && p.doc.id === docId ? { ...p, failed: true } : p));
    }
  }, []);

  const openDoc = useCallback(
    (doc: KbDoc, fragmentId?: string, highlight?: PdfHighlight) => {
      setPreview({ doc, fragmentId, highlight, full: null, failed: false });
      void loadChunks(doc.id);
    },
    [loadChunks],
  );

  // Convenience for an evidence/citation reference: show a header stub right
  // away, then swap in the real document text once its chunks load.
  const openCitation = useCallback(
    ({ documentId, chunkId, filename, snippet, page }: OpenCitationOpts) => {
      const name = filename ?? i18n.t("document:adapter.document");
      const stub: KbDoc = {
        id: documentId,
        real: true,
        title: titleOf(name),
        snippet: snippet ?? "",
        section: i18n.t("document:adapter.corpus"),
        sourceId: "backend",
        sourceName: i18n.t("document:adapter.corpus"),
        updatedAt: "",
        tags: [],
        fileType: fileTypeOf(name),
        fileName: name,
        fragments: chunkId
          ? [{ id: chunkId, text: snippet ?? "", location: i18n.t("document:adapter.quote") }]
          : [],
      };
      const trimmed = (snippet ?? "").trim();
      openDoc(stub, chunkId, trimmed === "" ? undefined : { snippet: trimmed, page });
    },
    [openDoc],
  );

  const close = useCallback(() => setPreview(null), []);

  const retry = useCallback(() => {
    setPreview((p) => {
      if (p) void loadChunks(p.doc.id);
      return p ? { ...p, full: null, failed: false } : p;
    });
  }, [loadChunks]);

  return { preview, openDoc, openCitation, close, retry };
}
