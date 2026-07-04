import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { motion, AnimatePresence } from "framer-motion";
import { X, ArrowRight, RefreshCw } from "lucide-react";
import { SearchBar } from "@/features/search/ui/SearchBar";
import { AnswerBlock } from "@/features/search/ui/SmartAnswer";
import { SourceHypotheses } from "@/features/search/ui/SourceHypotheses";
import { Button } from "@/shared/ui/Button";
import { Input } from "@/shared/ui/Input";
import { ErrorState, Segmented } from "@/shared/ui";
import {
  createChat,
  listMessages,
  sendMessage,
  parseAnswerMeta,
  type AnswerMeta,
  type ApiSource,
} from "@/features/search/api";
import { buildSmartAnswer } from "@/features/search/model";
import {
  DocPreviewSheet,
  DocumentPreview,
  OriginalDocViewer,
  docWithChunks,
  getDocumentChunks,
  sourceLocation,
  type PdfHighlight,
} from "@/features/document";
import { KbDoc, SmartAnswer } from "@/shared/types";

interface Turn {
  key: string;
  question: string;
  status: "loading" | "done" | "error";
  answer: SmartAnswer | null;
  raw: ApiSource[];
  meta?: AnswerMeta;
}

interface PreviewState {
  doc: KbDoc;
  fragmentId?: string;
  /** Сниппет цитаты для скролла и маркера в PDF-оригинале (как в гипотезах). */
  highlight?: PdfHighlight;
  full: KbDoc | null;
  failed: boolean;
  citedIds: string[];
  fragmentLocations: Record<string, string>;
  relevance?: number;
}

let turnSeq = 0;
const nextTurnKey = () => `t${++turnSeq}`;

/** ≥lg — боковая панель, иначе шит: Radix-портал игнорирует lg:hidden-обёртку. */
function useIsDesktop(): boolean {
  const [isDesktop, setIsDesktop] = useState(
    () => window.matchMedia("(min-width: 1024px)").matches,
  );
  useEffect(() => {
    const mq = window.matchMedia("(min-width: 1024px)");
    const onChange = () => setIsDesktop(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return isDesktop;
}

/** Тред «вопрос → ответ» в рамках одного чата. Новый запрос (?q=) создаёт чат
 *  и сразу заменяет URL на ?chat=<id>, поэтому перезагрузка и история
 *  открывают сохранённый ответ, а не переспрашивают модель заново. */
export function SearchResults() {
  const { t } = useTranslation("search");
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const q = params.get("q") ?? "";
  const chatParam = params.get("chat") ?? "";
  const [turns, setTurns] = useState<Turn[]>([]);
  const [loadPhase, setLoadPhase] = useState<"idle" | "loading" | "error">("idle");
  const [followUp, setFollowUp] = useState("");
  const [preview, setPreview] = useState<PreviewState | null>(null);
  const isDesktop = useIsDesktop();
  // Вид правой панели: PDF открываем «как в оригинале» (скролл к цитате),
  // остальным форматам с цитатой нужен текстовый вид с подсветкой.
  const [previewTab, setPreviewTab] = useState<"original" | "text">("original");
  const [originalUnavailable, setOriginalUnavailable] = useState(false);
  const previewDocId = preview?.doc.id;
  const previewIsPdf = preview?.doc.fileType === "pdf";
  const previewFragment = preview?.fragmentId;
  useEffect(() => {
    setPreviewTab(previewFragment && !previewIsPdf ? "text" : "original");
    setOriginalUnavailable(false);
  }, [previewDocId, previewIsPdf, previewFragment]);
  const chatIdRef = useRef<string | null>(null);
  const startedRef = useRef<string | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const askRef = useRef<(question: string) => Promise<void>>(async () => {});

  const patchTurn = (key: string, patch: Partial<Turn>) => {
    setTurns((ts) => ts.map((turn) => (turn.key === key ? { ...turn, ...patch } : turn)));
  };

  const ask = async (question: string) => {
    const key = nextTurnKey();
    setPreview(null);
    setTurns((ts) => [...ts, { key, question, status: "loading", answer: null, raw: [] }]);
    try {
      let chatId = chatIdRef.current;
      if (!chatId) {
        const chat = await createChat(question);
        chatId = chat.id;
        chatIdRef.current = chatId;
        // Помечаем чат «уже открытым» ДО замены URL: иначе эффект восстановления
        // перезатрёт живой тред (вопрос показывался бы как ошибка, а пришедший
        // ответ терялся, потому что ключ turn уже заменён).
        startedRef.current = `chat:${chatId}`;
        navigate(`/search?chat=${encodeURIComponent(chatId)}`, { replace: true });
      }
      const msg = await sendMessage(chatId, question);
      const raw = msg.sources ?? [];
      patchTurn(key, {
        status: "done",
        raw,
        answer: buildSmartAnswer(msg.content, raw),
        meta: parseAnswerMeta(msg.meta),
      });
    } catch {
      patchTurn(key, { status: "error" });
    }
  };

  askRef.current = ask;

  const retryTurn = (turn: Turn) => {
    setTurns((ts) => ts.filter((other) => other.key !== turn.key));
    void ask(turn.question);
  };

  // Открытие сохранённого чата: восстанавливаем тред из сообщений без
  // повторного обращения к модели.
  useEffect(() => {
    if (!chatParam || startedRef.current === `chat:${chatParam}`) return;
    startedRef.current = `chat:${chatParam}`;
    chatIdRef.current = chatParam;
    setPreview(null);
    setLoadPhase("loading");
    let cancelled = false;
    (async () => {
      try {
        const messages = await listMessages(chatParam);
        if (cancelled) return;
        const restored: Turn[] = [];
        for (const m of messages) {
          if (m.role === "user") {
            restored.push({
              key: nextTurnKey(),
              question: m.content,
              status: "error",
              answer: null,
              raw: [],
            });
          } else if (m.role === "assistant" && restored.length > 0) {
            const turn = restored.at(-1);
            if (turn && turn.status === "error") {
              try {
                const raw = m.sources ?? [];
                turn.answer = buildSmartAnswer(m.content, raw);
                turn.meta = parseAnswerMeta(m.meta);
                turn.raw = raw;
                turn.status = "done";
              } catch {
                // Одно битое сообщение не должно ронять весь восстановленный тред.
              }
            }
          }
        }
        setTurns(restored);
        setLoadPhase("idle");
      } catch {
        if (!cancelled) setLoadPhase("error");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [chatParam]);

  // Новый вопрос из адресной строки / поисковой строки. ask живёт в ref,
  // чтобы эффект зависел только от параметров URL.
  useEffect(() => {
    if (!q.trim() || chatParam || startedRef.current === `q:${q}`) return;
    startedRef.current = `q:${q}`;
    chatIdRef.current = null;
    setTurns([]);
    setPreview(null);
    void askRef.current(q);
  }, [q, chatParam]);

  useEffect(() => {
    const last = turns.at(-1);
    if (last?.status === "loading") {
      bottomRef.current?.scrollIntoView({ behavior: "smooth", block: "end" });
    }
  }, [turns]);

  const loadPreviewChunks = useCallback(async (docId: string) => {
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

  // Стабильный обработчик открытия источника: тред мемоизирован (AnswerBlock),
  // поэтому колбэк не должен пересоздаваться на каждый рендер.
  const turnsRef = useRef<Turn[]>([]);
  useEffect(() => {
    turnsRef.current = turns;
  }, [turns]);
  const openDocByTurn = useCallback(
    (turnKey: string, docId: string, fragmentId?: string) => {
      const turn = turnsRef.current.find((it) => it.key === turnKey);
      const source = turn?.answer?.sources.find((s) => s.doc.id === docId);
      if (!turn || !source) return;
      const citedIds = turn.raw.filter((s) => s.document_id === docId).map((s) => s.chunk_id);
      const fragmentLocations: Record<string, string> = {};
      for (const s of turn.raw) {
        if (s.document_id !== docId) continue;
        // Подпись места показываем, только когда у фрагмента есть провенанс
        // (страница или раздел) — иначе sourceLocation вернёт общий фолбэк.
        const hasLocation = (s.page_start ?? 0) > 0 || (s.section_heading?.trim() ?? "") !== "";
        if (hasLocation) fragmentLocations[s.chunk_id] = sourceLocation(s);
      }
      const targetFragment = fragmentId ?? source.fragmentId;
      // Сниппет цитируемого фрагмента — по нему PDF-оригинал скроллит к месту
      // и подсвечивает его маркером (тот же механизм, что в гипотезах).
      const rawSource =
        turn.raw.find((s) => s.document_id === docId && s.chunk_id === targetFragment) ??
        turn.raw.find((s) => s.document_id === docId);
      const snippet = (rawSource?.snippet ?? "").trim();
      setPreview({
        doc: source.doc,
        fragmentId: targetFragment,
        highlight: snippet === "" ? undefined : { snippet, page: rawSource?.page_start },
        full: null,
        failed: false,
        citedIds,
        fragmentLocations,
        relevance: source.relevance,
      });
      void loadPreviewChunks(docId);
    },
    [loadPreviewChunks],
  );

  const retryPreview = () => {
    if (!preview) return;
    setPreview({ ...preview, full: null, failed: false });
    void loadPreviewChunks(preview.doc.id);
  };

  const submitFollowUp = () => {
    const text = followUp.trim();
    if (!text || turns.at(-1)?.status === "loading") return;
    setFollowUp("");
    void ask(text);
  };

  const lastQuestion = turns.at(-1)?.question ?? q;
  const busy = turns.at(-1)?.status === "loading";

  const thread = (
    <>
      <SearchBar
        initialValue={lastQuestion}
        onSearch={(next) => navigate(`/search?q=${encodeURIComponent(next)}`)}
      />

      {loadPhase === "loading" && (
        <p className="mt-8 text-sm text-muted-foreground" aria-live="polite">
          {t("results.openingSaved")}
        </p>
      )}
      {loadPhase === "error" && (
        <ErrorState
          variant="page"
          message={t("results.openSavedError")}
          onRetry={() => {
            startedRef.current = null;
            setLoadPhase("loading");
            navigate(0);
          }}
        />
      )}

      <div className="mt-8 space-y-10">
        {turns.map((turn) => (
          <div key={turn.key}>
            {turn.status === "error" ? (
              <div>
                <h2 className="font-display text-[1.9rem] leading-[1.08]">{turn.question}</h2>
                <div className="mt-4 flex items-center gap-3 rounded-xl border bg-warn-wash/60 px-4 py-3">
                  <p className="text-sm">{t("results.turnFailed")}</p>
                  <Button size="sm" variant="outline" onClick={() => retryTurn(turn)}>
                    <RefreshCw className="size-3.5" aria-hidden />
                    {t("results.askAgain")}
                  </Button>
                </div>
              </div>
            ) : (
              <>
                <AnswerBlock
                  turnKey={turn.key}
                  question={turn.question}
                  answer={turn.answer}
                  meta={turn.meta}
                  loading={turn.status === "loading"}
                  selectedDocId={
                    preview && turn.answer?.sources.some((s) => s.doc.id === preview.doc.id)
                      ? preview.doc.id
                      : undefined
                  }
                  onOpenDoc={openDocByTurn}
                />
                {turn.status === "done" && turn.raw.length > 0 && (
                  <SourceHypotheses
                    documentIds={[...new Set(turn.raw.map((s) => s.document_id))]}
                    question={turn.question}
                  />
                )}
              </>
            )}
          </div>
        ))}
      </div>

      {turns.length > 0 && chatIdRef.current && (
        <div className="mt-8 flex items-center gap-2">
          <Input
            value={followUp}
            placeholder={t("results.followUpPlaceholder")}
            aria-label={t("results.followUpAriaLabel")}
            disabled={busy}
            onChange={(e) => setFollowUp(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") submitFollowUp();
            }}
          />
          <Button variant="brand" disabled={busy || !followUp.trim()} onClick={submitFollowUp}>
            {t("results.ask")}
            <ArrowRight className="size-4" aria-hidden />
          </Button>
        </div>
      )}
      <div ref={bottomRef} />
    </>
  );

  return (
    <div className="flex w-full">
      <div
        className={`min-w-0 px-4 py-6 md:py-8 ${
          preview ? "w-full lg:w-1/2" : "mx-auto w-full max-w-3xl"
        }`}
      >
        {thread}
      </div>

      <AnimatePresence>
        {preview && (
          <motion.aside
            key="preview-panel"
            initial={{ opacity: 0, x: 24 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: 24 }}
            transition={{ duration: 0.22, ease: "easeOut" }}
            className="sticky top-0 z-10 hidden h-screen w-1/2 shrink-0 flex-col border-l bg-background lg:flex"
            aria-label={t("results.preview.ariaLabel")}
          >
            <div className="flex items-center justify-between gap-2 border-b px-4 py-2">
              <p className="kicker text-muted-foreground">{t("results.preview.kicker")}</p>
              <div className="flex items-center gap-1.5">
                {!originalUnavailable && (
                  <Segmented
                    aria-label={t("results.preview.viewAriaLabel")}
                    value={previewTab}
                    onChange={setPreviewTab}
                    options={[
                      { value: "original", label: t("results.preview.tabOriginal") },
                      { value: "text", label: t("results.preview.tabText") },
                    ]}
                  />
                )}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t("results.preview.close")}
                  onClick={() => setPreview(null)}
                >
                  <X className="size-4" aria-hidden />
                </Button>
              </div>
            </div>
            {previewTab === "original" && !originalUnavailable ? (
              <div className="min-h-0 flex-1">
                <OriginalDocViewer
                  docId={preview.doc.id}
                  fileName={preview.doc.fileName}
                  fileType={preview.doc.fileType}
                  highlight={preview.highlight}
                  onUnavailable={() => {
                    setOriginalUnavailable(true);
                    setPreviewTab("text");
                  }}
                />
              </div>
            ) : (
              <div className="flex-1 overflow-y-auto pt-2">
                <DocumentPreview
                  doc={preview.full ?? preview.doc}
                  citedFragmentIds={preview.citedIds}
                  fragmentLocations={preview.fragmentLocations}
                  query={lastQuestion}
                  fragmentId={preview.fragmentId}
                  relevance={preview.relevance}
                  loading={!preview.full && !preview.failed}
                  error={preview.failed}
                  onRetry={retryPreview}
                />
              </div>
            )}
          </motion.aside>
        )}
      </AnimatePresence>

      <DocPreviewSheet
        doc={!isDesktop && preview ? (preview.full ?? preview.doc) : null}
        query={lastQuestion}
        fragmentId={preview?.fragmentId}
        highlight={preview?.highlight}
        citedFragmentIds={preview?.citedIds}
        fragmentLocations={preview?.fragmentLocations}
        relevance={preview?.relevance}
        loading={preview ? !preview.full && !preview.failed : false}
        error={preview?.failed ?? false}
        onRetry={retryPreview}
        onClose={() => setPreview(null)}
      />
    </div>
  );
}
