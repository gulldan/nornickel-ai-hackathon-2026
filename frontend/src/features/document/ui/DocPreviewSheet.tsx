import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/shared/ui/Sheet";
import { DocumentPreview } from "@/features/document/ui/DocumentPreview";
import { OriginalDocViewer } from "@/features/document/ui/OriginalDocViewer";
import type { PdfHighlight } from "@/features/document/ui/PdfViewer";
import { KbDoc } from "@/shared/types";
import { cn } from "@/shared/lib/cn";
import { publishedLabel } from "@/features/document/docAdapter";

type Tab = "original" | "text";

interface DocPreviewSheetProps {
  doc: KbDoc | null;
  query?: string;
  fragmentId?: string;
  /** Cited fragment to scroll to + highlight in the original PDF. */
  highlight?: PdfHighlight;
  /** All cited fragments of the document (see DocumentPreview) */
  citedFragmentIds?: string[];
  /** Location captions («стр. N», section) by chunk_id (see DocumentPreview) */
  fragmentLocations?: Record<string, string>;
  relevance?: number;
  /** The document's text content is still loading (the «Текст» tab) */
  loading?: boolean;
  /** The text content failed to load — an error with retry */
  error?: boolean;
  onRetry?: () => void;
  onClose: () => void;
}

export function DocPreviewSheet({
  doc,
  query,
  fragmentId,
  highlight,
  citedFragmentIds,
  fragmentLocations,
  relevance,
  loading,
  error,
  onRetry,
  onClose,
}: DocPreviewSheetProps) {
  const { t } = useTranslation("document");
  // Open on the real document by default; when a specific quote is being shown
  // (a citation from chat), start on the text view that can highlight it.
  const [tab, setTab] = useState<Tab>("original");
  // Оригинал не отдался (файла нет в хранилище) — тихо показываем текст.
  const [originalUnavailable, setOriginalUnavailable] = useState(false);
  useEffect(() => {
    // PDFs open on the real document (it now scrolls to + highlights the cited
    // fragment); other formats fall back to the highlighted reconstructed text.
    const isPdf = doc?.fileType === "pdf";
    setTab(fragmentId && !isPdf ? "text" : "original");
    setOriginalUnavailable(false);
  }, [doc?.id, doc?.fileType, fragmentId]);

  return (
    <Sheet open={doc !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        side="right"
        className="flex w-[95vw] flex-col gap-0 p-0 sm:w-[88vw] sm:max-w-5xl"
      >
        {doc && (
          <>
            <div className="flex items-center gap-3 border-b px-4 py-3 pr-12">
              <div className="min-w-0 flex-1">
                <SheetTitle className="truncate text-base">{doc.title}</SheetTitle>
                <SheetDescription className="truncate text-xs">{doc.fileName}</SheetDescription>
                {(doc.author || doc.publishedAt) && (
                  <p className="truncate text-xs text-muted-foreground">
                    {[doc.author, publishedLabel(doc.publishedAt)].filter(Boolean).join(" · ")}
                  </p>
                )}
              </div>
              <div className="flex shrink-0 rounded-md border p-0.5 text-sm">
                <TabBtn
                  active={tab === "original"}
                  disabled={originalUnavailable}
                  onClick={() => setTab("original")}
                >
                  {t("preview.tabOriginal")}
                </TabBtn>
                <TabBtn active={tab === "text"} onClick={() => setTab("text")}>
                  {t("preview.tabText")}
                </TabBtn>
              </div>
            </div>
            <div className="min-h-0 flex-1">
              {tab === "original" ? (
                <OriginalDocViewer
                  docId={doc.id}
                  fileName={doc.fileName}
                  fileType={doc.fileType}
                  highlight={highlight}
                  onUnavailable={() => {
                    setOriginalUnavailable(true);
                    setTab("text");
                  }}
                />
              ) : (
                <div className="h-full overflow-y-auto p-6">
                  {originalUnavailable && (
                    <p className="mb-4 rounded-lg bg-secondary px-3 py-2 text-sm text-muted-foreground">
                      {t("preview.originalUnavailable")}
                    </p>
                  )}
                  <DocumentPreview
                    doc={doc}
                    query={query}
                    fragmentId={fragmentId}
                    citedFragmentIds={citedFragmentIds}
                    fragmentLocations={fragmentLocations}
                    relevance={relevance}
                    loading={loading}
                    error={error}
                    onRetry={onRetry}
                  />
                </div>
              )}
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function TabBtn({
  active,
  disabled,
  onClick,
  children,
}: {
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "rounded px-3 py-1 transition-colors disabled:cursor-not-allowed disabled:opacity-45",
        active ? "bg-foreground text-background" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}
