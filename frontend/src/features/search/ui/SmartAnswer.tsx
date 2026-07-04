import {
  createContext,
  memo,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import type { Components } from "react-markdown";
import { Copy, Check, ChevronDown, BookOpen } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/shared/ui/Button";
import { Skeleton } from "@/shared/ui/Skeleton";
import { Kicker } from "@/shared/ui/Kicker";
import { FileTypeBadge } from "@/shared/ui/FileTypeBadge";
import { RichText } from "@/shared/ui/RichText";
import { SearchTrace } from "@/features/search/ui/SearchTrace";
import type { AnswerMeta } from "@/features/search/api";
import { SmartAnswer as SmartAnswerType, AnswerChip } from "@/shared/types";

/** Открытие источника внутри блока ответа. */
type OpenDoc = (docId: string, fragmentId?: string) => void;

interface AnswerBlockProps {
  /** Ключ тёрна: колбэк открытия стабильный, тёрн передаётся идентификатором. */
  turnKey: string;
  question: string;
  answer: SmartAnswerType | null;
  meta?: AnswerMeta;
  loading: boolean;
  selectedDocId?: string;
  onOpenDoc: (turnKey: string, docId: string, fragmentId?: string) => void;
}

// Пояснения отказа от ответа живут в словарях — здесь только ключи по коду причины.
const ABSTAIN_KEYS = {
  no_sources: "answer.abstain.noSources",
  weak_evidence: "answer.abstain.weakEvidence",
} as const;

/** Ссылки #cite-i из Markdown-ответа рендерим сносками-источниками. Данные
 *  сносок передаются через контекст, чтобы компонент ссылки был стабильным. */
const CITE_PREFIX = "#cite-";

const CiteContext = createContext<{
  chips: AnswerChip[];
  onOpen: OpenDoc;
} | null>(null);

function MarkdownLink({ href, children }: { href?: string; children?: ReactNode }) {
  const cite = useContext(CiteContext);
  const idx = href?.startsWith(CITE_PREFIX) ? Number(href.slice(CITE_PREFIX.length)) : NaN;
  const chip = Number.isInteger(idx) ? cite?.chips[idx] : undefined;
  if (chip && cite) return <ChipButton chip={chip} onOpen={cite.onOpen} />;
  return (
    <a href={href} target="_blank" rel="noreferrer">
      {children}
    </a>
  );
}

const CITE_COMPONENTS: Components = { a: MarkdownLink };

function ChipButton({ chip, onOpen }: { chip: AnswerChip; onOpen: OpenDoc }) {
  const { t } = useTranslation("search");
  return (
    <button
      type="button"
      onClick={() => onOpen(chip.docId, chip.fragmentId)}
      className="mx-0.5 inline-flex h-[18px] min-w-[18px] items-center justify-center rounded-[5px] bg-brand-wash px-1 align-text-top font-mono text-[11px] font-semibold text-brand transition-colors hover:bg-brand hover:text-brand-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      aria-label={t("answer.openSourceAt", { label: chip.label })}
    >
      {chip.label}
    </button>
  );
}

/** Ответ из документов: вопрос display-заголовком, текст с кликабельными
 *  сносками, честные источники с реальными процентами и трасса «Как искали».
 *  Мемоизирован: открытие/закрытие просмотра источника не должно заново
 *  парсить Markdown и KaTeX всех остальных ответов треда. */
export const AnswerBlock = memo(function AnswerBlock({
  turnKey,
  question,
  answer,
  meta,
  loading,
  selectedDocId,
  onOpenDoc,
}: AnswerBlockProps) {
  const { t } = useTranslation("search");
  const [copied, setCopied] = useState(false);
  const [traceOpen, setTraceOpen] = useState(false);
  const abstain = meta?.trace?.abstainReason ?? "";

  const openDoc = useCallback<OpenDoc>(
    (docId, fragmentId) => onOpenDoc(turnKey, docId, fragmentId),
    [onOpenDoc, turnKey],
  );
  const citeValue = useMemo(
    () => (answer ? { chips: answer.chips, onOpen: openDoc } : null),
    [answer, openDoc],
  );

  const handleCopy = async () => {
    if (!answer) return;
    await navigator.clipboard.writeText(answer.plain);
    setCopied(true);
    toast.success(t("answer.copied"));
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <section aria-label={t("answer.questionAriaLabel", { question })}>
      <Kicker className="text-brand">{t("answer.questionKicker")}</Kicker>
      <h2 className="font-display mt-2 text-balance text-[1.9rem] leading-[1.08] md:text-[2.4rem]">
        {question}
      </h2>

      <div className="mt-5 rounded-xl border bg-card">
        <div className="flex items-center justify-between border-b px-5 py-3">
          <Kicker>{t("answer.fromDocs")}</Kicker>
          {meta?.cached && <Kicker className="text-muted-foreground">{t("answer.cached")}</Kicker>}
        </div>

        <div className="px-5 py-4">
          {loading ? (
            <div className="space-y-3" aria-live="polite">
              <p className="text-sm text-muted-foreground">{t("answer.searching")}</p>
              <div className="space-y-2.5">
                <Skeleton className="h-4 w-full" />
                <Skeleton className="h-4 w-11/12" />
                <Skeleton className="h-4 w-4/5" />
              </div>
            </div>
          ) : answer ? (
            <>
              {abstain !== "" && (
                <p className="mb-3 rounded-md bg-warn-wash px-3 py-2 text-sm text-warn">
                  {t(
                    ABSTAIN_KEYS[abstain as keyof typeof ABSTAIN_KEYS] ??
                      ABSTAIN_KEYS.weak_evidence,
                  )}
                </p>
              )}
              <CiteContext.Provider value={citeValue}>
                <RichText className="text-[15px] leading-7" components={CITE_COMPONENTS}>
                  {answer.markdown}
                </RichText>
              </CiteContext.Provider>
            </>
          ) : (
            <p className="text-sm text-muted-foreground">{t("answer.failed")}</p>
          )}
        </div>

        {!loading && answer && (
          <div className="flex flex-wrap items-center gap-2 border-t px-5 py-2.5">
            <span className="inline-flex items-center gap-1.5 text-sm text-muted-foreground">
              <BookOpen className="size-3.5" aria-hidden />
              {t("answer.basedOn", { count: answer.sources.length })}
            </span>
            <div className="ml-auto flex items-center gap-1.5">
              {meta && (
                <Button variant="ghost" size="sm" onClick={() => setTraceOpen((v) => !v)}>
                  {t("trace.title")}
                  <ChevronDown
                    className={`size-3.5 transition-transform ${traceOpen ? "rotate-180" : ""}`}
                    aria-hidden
                  />
                </Button>
              )}
              <Button variant="ghost" size="sm" onClick={handleCopy}>
                {copied ? (
                  <Check className="size-3.5" aria-hidden />
                ) : (
                  <Copy className="size-3.5" aria-hidden />
                )}
                {t("answer.copy")}
              </Button>
            </div>
          </div>
        )}
      </div>

      {traceOpen && meta && <SearchTrace meta={meta} />}

      {!loading && answer && answer.sources.length > 0 && (
        <div className="mt-5">
          <div className="flex items-baseline justify-between">
            <Kicker>{t("answer.sourcesHeading", { count: answer.sources.length })}</Kicker>
          </div>
          <ol className="mt-2 space-y-2">
            {answer.sources.map((s, i) => {
              const selected = selectedDocId === s.doc.id;
              const metaLine = [s.doc.fileName, s.page, s.section].filter(Boolean).join(" · ");
              return (
                <li key={s.doc.id}>
                  <button
                    type="button"
                    onClick={() => openDoc(s.doc.id, s.fragmentId)}
                    className={`flex w-full items-start gap-3 rounded-xl border bg-card px-4 py-3 text-left transition-colors hover:border-brand-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                      selected ? "border-brand ring-1 ring-brand" : ""
                    }`}
                  >
                    <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md bg-brand-wash font-mono text-xs font-semibold text-brand">
                      {i + 1}
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium leading-snug">
                        {s.doc.title}
                      </span>
                      <span className="mt-0.5 flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
                        <FileTypeBadge type={s.doc.fileType} />
                        <span className="truncate">{metaLine}</span>
                      </span>
                    </span>
                    {s.relevance !== undefined && (
                      <span className="mt-0.5 shrink-0 font-mono text-xs font-semibold text-ok">
                        {s.relevance} %
                      </span>
                    )}
                  </button>
                </li>
              );
            })}
          </ol>
        </div>
      )}
    </section>
  );
});
