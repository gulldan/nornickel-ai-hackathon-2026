import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { Download, ExternalLink, FileWarning } from "lucide-react";
import { useTranslation } from "react-i18next";
import { fetchDocumentContent } from "@/features/document/api";
import type { PdfHighlight } from "@/features/document/ui/PdfViewer";
import type { FileType } from "@/shared/types";
import { Spinner } from "@/shared/ui/Spinner";

// pdf.js is heavy, so the real PDF viewer loads only when a PDF is actually
// opened (and keeps out of the main bundle).
const PdfViewer = lazy(() => import("@/features/document/ui/PdfViewer"));

/** Renders the document in its original form: PDFs through pdf.js (so a citation
 *  can scroll to and highlight its exact place), images natively, and other
 *  formats through the browser's native viewer with a download fallback.
 *  Fetches the file as an object URL and revokes it on close. */
export function OriginalDocViewer({
  docId,
  fileName,
  fileType,
  highlight,
  onUnavailable,
}: {
  docId: string;
  fileName: string;
  fileType: FileType;
  /** Cited fragment to scroll to + highlight (PDF only). */
  highlight?: PdfHighlight;
  /** Оригинал недоступен (файл не отдался) — родитель может переключить на текст. */
  onUnavailable?: () => void;
}) {
  const { t } = useTranslation("document");
  const [url, setUrl] = useState<string | null>(null);
  const [mime, setMime] = useState("application/octet-stream");
  const [error, setError] = useState(false);
  // Колбэк в ref, чтобы эффект загрузки зависел только от документа.
  const onUnavailableRef = useRef(onUnavailable);
  onUnavailableRef.current = onUnavailable;

  useEffect(() => {
    let alive = true;
    let made: string | null = null;
    setUrl(null);
    setError(false);
    fetchDocumentContent(docId)
      .then(({ url: u, mime: m }) => {
        if (alive) {
          made = u;
          setUrl(u);
          setMime(m);
        } else {
          URL.revokeObjectURL(u);
        }
      })
      .catch(() => {
        if (alive) {
          setError(true);
          onUnavailableRef.current?.();
        }
      });
    return () => {
      alive = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [docId]);

  if (error) {
    return <Centered icon={<FileWarning className="h-6 w-6" />} text={t("viewer.docFailed")} />;
  }
  if (!url) {
    return <Centered icon={<Spinner size="lg" />} text={t("viewer.loadingDoc")} />;
  }

  const isPdf = fileType === "pdf" || mime.includes("pdf");

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-end gap-3 border-b bg-background px-4 py-2 text-sm">
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1.5 text-muted-foreground hover:text-foreground"
        >
          <ExternalLink className="h-4 w-4" /> {t("viewer.openInNewTab")}
        </a>
        <a
          href={url}
          download={fileName}
          className="inline-flex items-center gap-1.5 text-muted-foreground hover:text-foreground"
        >
          <Download className="h-4 w-4" /> {t("actions.download", { ns: "common" })}
        </a>
      </div>
      <div className="min-h-0 flex-1 bg-muted/30">
        {fileType === "image" ? (
          <div className="flex h-full items-center justify-center overflow-auto p-4">
            <img src={url} alt={fileName} className="max-h-full max-w-full rounded shadow-sm" />
          </div>
        ) : isPdf ? (
          <Suspense
            fallback={<Centered icon={<Spinner size="lg" />} text={t("viewer.loadingViewer")} />}
          >
            <PdfViewer url={url} highlight={highlight} />
          </Suspense>
        ) : (
          <object data={url} type={mime} className="h-full w-full" aria-label={fileName}>
            <Centered
              icon={<FileWarning className="h-6 w-6" />}
              text={t("viewer.noInlinePreview")}
            />
          </object>
        )}
      </div>
    </div>
  );
}

function Centered({ icon, text }: { icon: React.ReactNode; text: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center text-muted-foreground">
      {icon}
      <p className="text-sm">{text}</p>
    </div>
  );
}
