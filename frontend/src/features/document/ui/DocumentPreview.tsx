import { ChevronDown, ChevronUp, Clock, Sparkles } from "lucide-react";
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { formatDate, formatDateShort } from "@/shared/lib/format";
import { getReaderContent, DocBlock } from "@/features/document/docContent";
import { highlightParts } from "@/shared/lib/highlight";
import { KbDoc } from "@/shared/types";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { ErrorState } from "@/shared/ui";
import { FileTypeBadge } from "@/shared/ui/FileTypeBadge";
import { Separator } from "@/shared/ui/Separator";
import { Skeleton } from "@/shared/ui/Skeleton";

interface DocumentPreviewProps {
  doc: KbDoc;
  /** User query — for highlighting words in the answering fragment */
  query?: string;
  /** Cited fragment (source chunk_id) — selected on open */
  fragmentId?: string;
  /** All cited fragments of this document: highlighted as
   *  clickable matches, with an «N of M» navigator between them. */
  citedFragmentIds?: string[];
  /** Location caption of the cited fragment by chunk_id: «стр. N», section —
   *  from source provenance (see docAdapter.sourceLocation). */
  fragmentLocations?: Record<string, string>;
  /** Relevance to the query in percent */
  relevance?: number;
  /** Show the header with title and metadata */
  showHeader?: boolean;
  /** Content (chunks) is still loading — a skeleton instead of text */
  loading?: boolean;
  /** Content failed to load — a compact error with retry */
  error?: boolean;
  onRetry?: () => void;
}

function LoadingBody() {
  const { t } = useTranslation("document");
  return (
    <output
      className="block rounded-xl border bg-card px-5 py-5"
      aria-label={t("preview.loadingAria")}
    >
      <div className="space-y-3">
        <Skeleton className="mx-auto h-5 w-2/3" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-11/12" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-4/5" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-3/5" />
      </div>
    </output>
  );
}

function HighlightedText({ text, query }: { text: string; query?: string }) {
  if (!query) return <>{text}</>;
  // Ключ части — её смещение в исходном тексте: уникально и стабильно.
  let offset = 0;
  const nodes = highlightParts(text, query).map((p) => {
    const at = offset;
    offset += p.part.length;
    return p.hit ? (
      <mark key={at} className="rounded-sm bg-primary/20 px-0.5 font-medium text-foreground">
        {p.part}
      </mark>
    ) : (
      <Fragment key={at}>{p.part}</Fragment>
    );
  });
  return <>{nodes}</>;
}

// Датозависимые ключи блоков: fragmentId, иначе «kind:текст» со счётчиком повторов.
function withBlockKeys(blocks: DocBlock[]): { block: DocBlock; key: string }[] {
  const seen = new Map<string, number>();
  return blocks.map((block) => {
    const base =
      block.kind === "paragraph" && block.fragmentId !== undefined
        ? block.fragmentId
        : `${block.kind}:${block.text.slice(0, 40)}`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    return { block, key: n === 0 ? base : `${base}#${n}` };
  });
}

export function DocumentPreview({
  doc,
  query,
  fragmentId,
  citedFragmentIds,
  fragmentLocations,
  relevance,
  showHeader = true,
  loading = false,
  error = false,
  onRetry,
}: DocumentPreviewProps) {
  const { t } = useTranslation("document");
  const highlightRef = useRef<HTMLDivElement>(null);

  // The selected match — controlled internally (clicks on matches,
  // navigator), starts from the source-cited fragment.
  const [activeFragmentId, setActiveFragmentId] = useState<string | null>(fragmentId ?? null);
  // A nonce restarts the CSS flash: changing key unmounts the highlighted block,
  // and the animation plays again (jumping back to the same place should also
  // "blink" — otherwise the eye has nothing to latch onto).
  const [flashNonce, setFlashNonce] = useState(0);
  useEffect(() => {
    setActiveFragmentId(fragmentId ?? null);
    setFlashNonce((n) => n + 1);
  }, [fragmentId, doc.id]);

  const ready = !loading && !error;
  const content = useMemo(() => getReaderContent(doc), [doc]);

  // Matches actually present in the loaded content, in their order
  // through the document — the basis of the «N of M» navigator.
  const cited = useMemo(() => {
    const wanted = new Set(citedFragmentIds ?? (fragmentId ? [fragmentId] : []));
    if (wanted.size === 0) return [];
    const ordered: string[] = [];
    const blocks = content.type === "pdf" ? content.pages.flat() : content.blocks;
    for (const b of blocks) {
      if (b.kind === "paragraph" && b.fragmentId && wanted.has(b.fragmentId)) {
        ordered.push(b.fragmentId);
      }
    }
    return ordered;
  }, [content, citedFragmentIds, fragmentId]);

  const activeIdx = activeFragmentId ? cited.indexOf(activeFragmentId) : -1;

  // Location caption of the active fragment («стр. N», section) by chunk_id — from
  // source provenance. "" / absent → the badge shows nothing.
  const activeLocation = (activeFragmentId && fragmentLocations?.[activeFragmentId]) || undefined;

  const jumpTo = (id: string) => {
    setActiveFragmentId(id);
    setFlashNonce((n) => n + 1);
  };

  // Циклический переход к соседнему совпадению.
  const jumpBy = (delta: number) => {
    const next = cited.at((activeIdx + delta + cited.length) % cited.length);
    if (next !== undefined) jumpTo(next);
  };

  // If no explicit fragment is passed (a card click without a source) or it's
  // not in the loaded content — we pick the first match in document order,
  // so the jump always works, not only from the smart answer.
  useEffect(() => {
    if (!ready) return;
    const first = cited.at(0);
    if (first === undefined) return;
    if (activeFragmentId && cited.includes(activeFragmentId)) return;
    setActiveFragmentId(first);
    setFlashNonce((n) => n + 1);
  }, [ready, cited, activeFragmentId]);

  const scrollToHighlight = () => {
    highlightRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
  };
  // Scroll to the selected place: after content loads (doc changes from
  // a stub to the full one — a separate dependency) and on every jump. The double
  // pass guards against layout shifts (fonts, panel animation).
  useEffect(() => {
    if (!activeFragmentId || !ready) return;
    const t1 = setTimeout(scrollToHighlight, 80);
    const t2 = setTimeout(scrollToHighlight, 420);
    return () => {
      clearTimeout(t1);
      clearTimeout(t2);
    };
  }, [doc, activeFragmentId, flashNonce, ready]);

  const renderBlock = (block: DocBlock, key: string) => {
    if (block.kind === "heading") {
      return (
        <h4 key={key} className="pt-1 text-sm font-semibold">
          {block.text}
        </h4>
      );
    }
    const isActive = block.fragmentId !== undefined && block.fragmentId === activeFragmentId;
    const isCited = !isActive && block.fragmentId !== undefined && cited.includes(block.fragmentId);

    if (isActive) {
      // The key with a nonce restarts hl-frag-flash on a repeated jump.
      return (
        <div key={`${key}-${flashNonce}`} ref={highlightRef}>
          <span className="mb-1.5 mr-2 inline-flex items-center gap-1 rounded-full bg-primary px-2 py-0.5 text-[11px] font-semibold text-primary-foreground">
            <Sparkles className="size-3" aria-hidden />
            {t("preview.answersQuestion")}
          </span>
          {activeLocation && (
            <span className="text-[11px] text-muted-foreground">{activeLocation}</span>
          )}
          <p className="hl-frag hl-frag-flash px-3 py-2 leading-relaxed">
            <HighlightedText text={block.text} query={query} />
          </p>
        </div>
      );
    }
    if (isCited) {
      // Настоящая кнопка: Enter/Space и фокус даёт браузер, блочная вёрстка сохранена.
      return (
        <button
          key={key}
          type="button"
          className="hl-frag-ctx block w-full px-3 py-2 text-left leading-relaxed"
          title={t("preview.alsoAnswers")}
          onClick={() => block.fragmentId && jumpTo(block.fragmentId)}
        >
          <HighlightedText text={block.text} query={query} />
        </button>
      );
    }
    return (
      <p key={key} className="px-0.5 leading-relaxed text-foreground/90">
        {block.text}
      </p>
    );
  };

  const renderBody = () => {
    if (content.type === "pdf") {
      const total = content.pages.length;
      // Номер страницы — её идентичность в документе, он же ключ.
      const pages = content.pages.map((blocks, idx) => ({
        num: idx + 1,
        entries: withBlockKeys(blocks),
      }));
      return (
        <div className="space-y-4">
          {pages.map(({ num, entries }) => {
            const first = entries.at(0);
            // Обложка: заголовок первой страницы рендерится крупнее остальных.
            const cover = num === 1 && first?.block.kind === "heading" ? first : undefined;
            const body = cover ? entries.slice(1) : entries;
            return (
              <div key={`page-${num}`} className="rounded-xl border bg-card px-5 py-5">
                <div className="space-y-3 text-sm">
                  {cover && (
                    <h3 className="pb-1 text-center text-base font-semibold">{cover.block.text}</h3>
                  )}
                  {body.map(({ block, key }) => renderBlock(block, key))}
                </div>
                <p className="mt-5 border-t pt-2 text-center text-[11px] text-muted-foreground">
                  {t("preview.pageOf", { page: num, total })}
                </p>
              </div>
            );
          })}
        </div>
      );
    }
    if (content.type === "docx") {
      return (
        <div className="rounded-xl border bg-card px-5 py-5">
          <h3 className="pb-3 text-center text-base font-semibold">{doc.title}</h3>
          <div className="space-y-3 text-sm">
            {withBlockKeys(content.blocks).map(({ block, key }) => renderBlock(block, key))}
          </div>
        </div>
      );
    }
    // eml
    return (
      <div className="rounded-xl border bg-card">
        {doc.emailMeta && (
          <div className="border-b bg-muted/40 px-5 py-3">
            <dl className="space-y-1 text-xs">
              <div className="flex gap-2">
                <dt className="w-12 shrink-0 text-muted-foreground">{t("preview.emailFrom")}</dt>
                <dd className="font-medium">{doc.emailMeta.from}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="w-12 shrink-0 text-muted-foreground">{t("preview.emailTo")}</dt>
                <dd>{doc.emailMeta.to}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="w-12 shrink-0 text-muted-foreground">{t("preview.emailSubject")}</dt>
                <dd className="font-medium">{doc.emailMeta.subject}</dd>
              </div>
              <div className="flex gap-2">
                <dt className="w-12 shrink-0 text-muted-foreground">{t("preview.emailDate")}</dt>
                <dd>{formatDateShort(doc.emailMeta.date)}</dd>
              </div>
            </dl>
          </div>
        )}
        <div className="space-y-3 px-5 py-4 text-sm">
          {withBlockKeys(content.blocks).map(({ block, key }) => renderBlock(block, key))}
        </div>
      </div>
    );
  };

  const renderError = () => {
    const snippet = (doc.snippet ?? "").trim();
    if (snippet) {
      return (
        <div className="space-y-3">
          <p className="rounded-lg bg-secondary px-3 py-2 text-sm text-muted-foreground">
            {t("preview.snippetOnly")}
          </p>
          <div className="whitespace-pre-line rounded-xl border bg-card px-5 py-5 text-sm leading-relaxed">
            {snippet}
          </div>
        </div>
      );
    }
    return <ErrorState variant="compact" message={t("preview.contentFailed")} onRetry={onRetry} />;
  };

  // Some cited fragments may not make it into the preview: the backend
  // returns an ordered prefix of very large documents.
  const wantedCount = (citedFragmentIds ?? (fragmentId ? [fragmentId] : [])).length;
  const missingCited = ready && wantedCount > 0 && cited.length < wantedCount;

  // The match navigator — a sticky panel above the text (like the toolbar in
  // the validation station): in a huge document it's always at hand.
  const fragmentNav = ready && cited.length > 0 && (
    <div className="sticky top-0 z-10 -mx-4 mb-3 flex items-center gap-1.5 border-b bg-background/95 px-4 py-2 backdrop-blur">
      <Button
        variant="outline"
        size="icon-sm"
        aria-label={t("preview.prevMatch")}
        disabled={cited.length < 2}
        onClick={() => jumpBy(-1)}
      >
        <ChevronUp className="size-3.5" aria-hidden />
      </Button>
      <Button
        variant="outline"
        size="icon-sm"
        aria-label={t("preview.nextMatch")}
        disabled={cited.length < 2}
        onClick={() => jumpBy(1)}
      >
        <ChevronDown className="size-3.5" aria-hidden />
      </Button>
      <button
        type="button"
        className="rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        onClick={() => {
          const target = activeFragmentId ?? cited.at(0);
          if (target !== undefined) jumpTo(target);
        }}
        title={t("preview.goToMatch")}
      >
        {t("preview.matchOf", { current: activeIdx >= 0 ? activeIdx + 1 : 1, total: cited.length })}
      </button>
      {missingCited && (
        <span className="ml-auto text-[11px] text-warn">{t("preview.matchesOutside")}</span>
      )}
    </div>
  );

  return (
    // break-words: длинные имена файлов/DOI не вылезают за рамки панели.
    <div className="break-words">
      {showHeader && (
        <div className="space-y-1.5 px-4 pb-3 pt-1">
          <h2 className="pr-6 text-base font-semibold leading-snug">{doc.title}</h2>
          <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            <FileTypeBadge type={doc.fileType} />
            <span className="truncate">{doc.fileName}</span>
            {relevance !== undefined && (
              <Badge variant="outline" className="font-mono text-ok">
                {t("preview.relevance", { value: relevance })}
              </Badge>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
            <Badge variant="secondary">{doc.section}</Badge>
            <span>{doc.sourceName}</span>
            {doc.updatedAt && (
              <span className="inline-flex items-center gap-1">
                <Clock className="size-3" aria-hidden />
                {formatDate(doc.updatedAt)}
              </span>
            )}
          </div>
        </div>
      )}
      <Separator />
      <div className="bg-muted/60 px-4 py-4 pb-8">
        {fragmentNav}
        {loading ? <LoadingBody /> : error ? renderError() : renderBody()}
      </div>
    </div>
  );
}
