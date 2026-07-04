// Real PDF rendering (pdf.js) with citation deep-linking: jump to the cited
// page and highlight the exact fragment in the text layer. Used both on the
// Documents page (plain viewing) and in the evidence/citation flow (with a
// `highlight`). The pdf.js worker is bundled locally — the app runs in a closed
// network, so no external CDN.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Document, Page, pdfjs } from "react-pdf";
import "react-pdf/dist/Page/TextLayer.css";
import "react-pdf/dist/Page/AnnotationLayer.css";
import { FileWarning, ZoomIn, ZoomOut } from "lucide-react";

import { Spinner } from "@/shared/ui/Spinner";

pdfjs.GlobalWorkerOptions.workerSrc = new URL(
  "pdfjs-dist/build/pdf.worker.min.mjs",
  import.meta.url,
).toString();

export interface PdfHighlight {
  /** The cited fragment text to find, scroll to and highlight. */
  snippet: string;
  /** 1-based page to jump to first; 0/undefined ⇒ search every page. */
  page?: number;
}

interface PdfViewerProps {
  /** Object URL (or http URL) of the PDF. */
  url: string;
  /** Optional cited fragment to deep-link to. */
  highlight?: PdfHighlight;
}

const MAX_PAGE_WIDTH = 1000;
const MIN_SCALE = 0.6;
const MAX_SCALE = 2.4;

// Lowercase and drop every whitespace char: pdf.js splits a line into many
// spans with inconsistent (often missing) spacing, so matching on the
// whitespace-stripped text is far more robust than word-by-word.
function squash(s: string): string {
  return s.toLowerCase().replace(/\s+/g, "");
}

// Progressively shorter probes from the snippet — the whole fragment first,
// then a leading window — so a slightly different extraction (OCR, hyphenation)
// still anchors a match instead of failing outright.
function probes(snippet: string): string[] {
  const sq = squash(snippet);
  if (sq === "") return [];
  const out = [sq];
  for (const n of [140, 70, 36]) {
    if (sq.length > n) out.push(sq.slice(0, n));
  }
  return out;
}

interface SpanRange {
  el: HTMLElement;
  start: number;
  end: number;
}

// Return the text-layer spans covering the first probe that occurs in a page's
// squashed text, or [] when nothing matches.
function locate(spans: HTMLElement[], snippet: string): HTMLElement[] {
  const ranges: SpanRange[] = [];
  let acc = "";
  for (const el of spans) {
    const sq = squash(el.textContent ?? "");
    if (sq === "") continue;
    ranges.push({ el, start: acc.length, end: acc.length + sq.length });
    acc += sq;
  }
  if (acc === "") return [];
  for (const probe of probes(snippet)) {
    const at = acc.indexOf(probe);
    if (at < 0) continue;
    const end = at + probe.length;
    return ranges.filter((r) => r.start < end && r.end > at).map((r) => r.el);
  }
  return [];
}

function PdfViewer({ url, highlight }: PdfViewerProps) {
  const { t } = useTranslation("document");
  const scrollRef = useRef<HTMLDivElement>(null);
  const pageEls = useRef<Map<number, HTMLDivElement>>(new Map());
  const scrolledKey = useRef<string>("");
  const [numPages, setNumPages] = useState(0);
  const [width, setWidth] = useState(0);
  const [scale, setScale] = useState(1);
  const [error, setError] = useState(false);

  const snippet = highlight?.snippet ?? "";
  const targetPage = highlight?.page && highlight.page > 0 ? highlight.page : 0;
  const targetKey = `${url}|${targetPage}|${snippet}`;

  // Measure the column so pages render crisp at the available width.
  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const measure = () => setWidth(el.clientWidth);
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const pageWidth =
    width > 0 ? Math.round(Math.min(width - 32, MAX_PAGE_WIDTH) * scale) : undefined;

  // Find + highlight the citation across already-rendered pages. Idempotent: it
  // clears prior marks, scans the target page first (then the rest), and scrolls
  // once per distinct citation. Called as each page's text layer renders and
  // whenever the highlight target changes.
  const place = useCallback(() => {
    if (snippet === "" || numPages === 0) return;
    const container = scrollRef.current;
    if (!container) return;
    const already = scrolledKey.current === targetKey && container.querySelector(".pdf-hl");
    if (already) return;

    container.querySelectorAll(".pdf-hl").forEach((el) => el.classList.remove("pdf-hl"));

    const order: number[] = [];
    if (targetPage) order.push(targetPage);
    for (let n = 1; n <= numPages; n++) {
      if (n !== targetPage) order.push(n);
    }

    for (const n of order) {
      const pageEl = pageEls.current.get(n);
      if (!pageEl) continue;
      const spans = Array.from(
        pageEl.querySelectorAll<HTMLElement>(".react-pdf__Page__textContent span"),
      );
      if (spans.length === 0) continue; // text layer not ready yet
      const hits = locate(spans, snippet);
      const firstHit = hits[0];
      if (firstHit) {
        for (const el of hits) el.classList.add("pdf-hl");
        firstHit.scrollIntoView({ block: "center", behavior: "smooth" });
        scrolledKey.current = targetKey;
        return;
      }
    }
    // Known page but its text didn't match (e.g. a scanned page with no text
    // layer): still land the citation on the right page.
    if (targetPage && scrolledKey.current !== targetKey) {
      const pageEl = pageEls.current.get(targetPage);
      if (pageEl) {
        pageEl.scrollIntoView({ block: "start", behavior: "smooth" });
        scrolledKey.current = targetKey;
      }
    }
  }, [snippet, targetPage, targetKey, numPages]);

  useEffect(() => {
    scrolledKey.current = "";
    place();
  }, [place]);

  const registerPage = useCallback((n: number) => {
    return (el: HTMLDivElement | null) => {
      if (el) pageEls.current.set(n, el);
      else pageEls.current.delete(n);
    };
  }, []);

  if (error) {
    return <Centered icon={<FileWarning className="h-6 w-6" />} text={t("viewer.pdfFailed")} />;
  }

  return (
    <div className="relative flex h-full flex-col">
      <div className="flex items-center justify-end gap-1 border-b bg-background px-3 py-1.5">
        <button
          type="button"
          aria-label={t("viewer.zoomOut")}
          className="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
          onClick={() => setScale((s) => Math.max(MIN_SCALE, Math.round((s - 0.2) * 10) / 10))}
        >
          <ZoomOut className="h-4 w-4" />
        </button>
        <span className="w-12 text-center text-xs tabular-nums text-muted-foreground">
          {Math.round(scale * 100)}%
        </span>
        <button
          type="button"
          aria-label={t("viewer.zoomIn")}
          className="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
          onClick={() => setScale((s) => Math.min(MAX_SCALE, Math.round((s + 0.2) * 10) / 10))}
        >
          <ZoomIn className="h-4 w-4" />
        </button>
      </div>
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto bg-muted/40 px-4 py-4">
        <Document
          file={url}
          onLoadSuccess={({ numPages: n }) => setNumPages(n)}
          onLoadError={() => setError(true)}
          loading={<Centered icon={<Spinner size="lg" />} text={t("viewer.loadingPdf")} />}
          error={
            <Centered icon={<FileWarning className="h-6 w-6" />} text={t("viewer.pdfFailed")} />
          }
          className="flex flex-col items-center gap-4"
        >
          {Array.from({ length: numPages }, (_, i) => i + 1).map((n) => (
            <div
              key={n}
              ref={registerPage(n)}
              className="overflow-hidden rounded-md bg-white shadow-sm ring-1 ring-black/5"
            >
              <Page
                pageNumber={n}
                width={pageWidth}
                renderAnnotationLayer={false}
                renderTextLayer
                onRenderTextLayerSuccess={place}
                loading={
                  <div className="flex h-[400px] w-full items-center justify-center">
                    <Spinner />
                  </div>
                }
              />
            </div>
          ))}
        </Document>
      </div>
    </div>
  );
}

function Centered({ icon, text }: { icon: React.ReactNode; text: string }) {
  return (
    <div className="flex h-full min-h-[200px] flex-col items-center justify-center gap-2 p-8 text-center text-muted-foreground">
      {icon}
      <p className="text-sm">{text}</p>
    </div>
  );
}

export default PdfViewer;
