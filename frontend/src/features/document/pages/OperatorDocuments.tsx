import { useCallback, useEffect, useRef, useState } from "react";
import { Eye, RefreshCw, Upload, Wand2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { Skeleton } from "@/shared/ui/Skeleton";
import { Spinner } from "@/shared/ui/Spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/ui/Table";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/Select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/Tooltip";
import { DocPreviewSheet } from "@/features/document/ui/DocPreviewSheet";
import { FileTypeBadge } from "@/shared/ui/FileTypeBadge";
import { PipelineStepper } from "@/features/document/ui/StatusBadge";
import { InfoHint } from "@/shared/ui/InfoHint";
import { ErrorState, PageHeader, Pagination, SearchField } from "@/shared/ui";
import { adminListDocuments } from "@/features/admin";
import {
  getDocument,
  getDocumentChunks,
  ApiDocument,
  IngestionEvent,
  UPLOAD_ACCEPT,
} from "@/features/document/api";
import { useIngestionEvents } from "@/features/document/useIngestionEvents";
import {
  docWithChunks,
  documentTitle,
  fileTypeOf,
  publishedLabel,
} from "@/features/document/docAdapter";
import { formatDateTime, humanSize } from "@/shared/lib/format";
import { uploadSmart, UploadCancelled, type UploadProgress } from "@/features/document/upload";
import { FileType, KbDoc } from "@/shared/types";

/** Строк таблицы на странице — дальше листается страничной навигацией. */
const DOCS_PAGE = 20;

/** Пауза после ввода перед серверным поиском. */
const SEARCH_DEBOUNCE_MS = 300;

// Подписи фильтров типов — в словаре document (page.typeFilters.*).
const TYPE_FILTERS = ["pdf", "docx", "xlsx", "pptx", "eml", "txt", "image"] as const;

type Phase = "loading" | "ready" | "error";

export function OperatorDocuments() {
  const { t } = useTranslation("document");

  const [docs, setDocs] = useState<ApiDocument[] | null>(null);
  const [total, setTotal] = useState(0);
  const [phase, setPhase] = useState<Phase>("loading");
  const [refreshing, setRefreshing] = useState(false);
  const [filter, setFilter] = useState("");
  // Отложенное значение поиска — уходит на сервер после паузы в наборе.
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState<"all" | FileType>("all");
  const [docsPage, setDocsPage] = useState(1);
  const [preview, setPreview] = useState<KbDoc | null>(null);
  const [previewLoadingId, setPreviewLoadingId] = useState<string | null>(null);
  const [uploading, setUploading] = useState(0);
  const [sessionStatus, setSessionStatus] = useState<Record<string, string>>({});
  const [dragOver, setDragOver] = useState(false);
  const dragDepth = useRef(0);
  // Прогресс внутри текущей стадии (страницы OCR, батчи индексации) по WS.
  const [stageProgress, setStageProgress] = useState<
    Record<string, { current: number; total: number }>
  >({});
  // Active large-file transfers — progress cards above the table.
  const [transfers, setTransfers] = useState<
    { id: string; name: string; fraction: number; totalBytes: number; cancel: () => void }[]
  >([]);
  const fileInput = useRef<HTMLInputElement>(null);

  // Стражи серверной загрузки: seq отбрасывает устаревшие ответы, loaded
  // оставляет таблицу на экране при фоновых обновлениях (без скелетона).
  const requestSeq = useRef(0);
  const loaded = useRef(false);

  // Сервер отдаёт готовую страницу: сортировка, поиск и фильтр — на бэкенде.
  const load = useCallback(async () => {
    const seq = ++requestSeq.current;
    if (!loaded.current) setPhase("loading");
    try {
      const page = await adminListDocuments({
        limit: DOCS_PAGE,
        offset: (docsPage - 1) * DOCS_PAGE,
        q: query || undefined,
        type: typeFilter === "all" ? undefined : typeFilter,
      });
      if (seq !== requestSeq.current) return;
      setDocs(page.items);
      setTotal(page.total);
      loaded.current = true;
      setPhase("ready");
    } catch (e) {
      if (seq !== requestSeq.current) return;
      if (!loaded.current) setPhase("error");
      toast.error(e instanceof Error ? e.message : t("page.loadFailed"));
    }
  }, [docsPage, query, typeFilter, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Дебаунс поиска — сервер не дёргается на каждый символ.
  useEffect(() => {
    const timer = setTimeout(() => setQuery(filter.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [filter]);

  // The «Обновить» (Refresh) button: other users' uploads don't arrive over WebSocket — refetch only.
  const refresh = async () => {
    setRefreshing(true);
    try {
      await load();
    } finally {
      setRefreshing(false);
    }
  };

  // Live indexing progress: events arrive only for the current user's
  // documents; rows are patched by document_id (WS + reconnect live in the hook).
  const { live } = useIngestionEvents(
    useCallback((ev: IngestionEvent) => {
      setSessionStatus((prev) =>
        prev[ev.document_id] === undefined ? prev : { ...prev, [ev.document_id]: ev.status },
      );
      setDocs((prev) =>
        prev === null
          ? prev
          : prev.map((d) =>
              d.id === ev.document_id
                ? { ...d, status: ev.status, status_msg: ev.message, updated_at: ev.timestamp }
                : d,
            ),
      );
      setStageProgress((prev) => {
        const stageTotal = ev.stage_total ?? 0;
        if (stageTotal > 0) {
          return {
            ...prev,
            [ev.document_id]: { current: ev.stage_current ?? 0, total: stageTotal },
          };
        }
        if (prev[ev.document_id]) {
          const next = { ...prev };
          delete next[ev.document_id];
          return next;
        }
        return prev;
      });
      // Pipeline finale: fetch fresh metadata (chunk_count, status_msg).
      if (ev.status === "indexed" || ev.status === "failed") {
        getDocument(ev.document_id)
          .then((fresh) => {
            setDocs((prev) =>
              prev === null ? prev : prev.map((d) => (d.id === fresh.id ? fresh : d)),
            );
          })
          .catch(() => undefined);
      }
    }, []),
  );

  const noteUploaded = (doc: ApiDocument) => {
    setSessionStatus((prev) => ({ ...prev, [doc.id]: doc.status }));
    setDocs((prev) => (prev === null ? [doc] : [doc, ...prev]));
    setTotal((n) => n + 1);
    // A duplicate is a success, not an error: the content is already searchable.
    if ((doc.status_msg ?? "").startsWith("дубликат")) {
      toast.info(t("page.duplicate", { name: doc.filename }));
    }
  };

  const uploadOne = (file: File) => {
    setUploading((n) => n + 1);
    const controller = new AbortController();
    const id = `${file.name}-${file.size}-${file.lastModified}`;
    setTransfers((prev) => [
      { id, name: file.name, fraction: 0, totalBytes: file.size, cancel: () => controller.abort() },
      ...prev,
    ]);
    const onProgress = (p: UploadProgress) =>
      setTransfers((prev) =>
        prev.map((tr) => (tr.id === id ? { ...tr, fraction: p.fraction } : tr)),
      );
    uploadSmart(file, onProgress, controller.signal)
      .then(noteUploaded)
      .catch((e: unknown) => {
        if (e instanceof UploadCancelled) {
          toast(t("page.uploadCancelled", { name: file.name }));
        } else {
          toast.error(
            `«${file.name}»: ${e instanceof Error ? e.message : t("page.uploadFailedGeneric")}`,
          );
        }
      })
      .finally(() => {
        setTransfers((prev) => prev.filter((tr) => tr.id !== id));
        setUploading((n) => n - 1);
      });
  };

  const onFiles = (files: FileList | null) => {
    if (!files || files.length === 0) return;
    const picked = [...files];
    const archives = picked.filter((f) => /\.(zip|rar|7z|tar|gz|tgz)$/i.test(f.name)).length;
    toast(
      archives > 0
        ? t("page.uploadingBatchArchives", { count: picked.length })
        : t("page.uploadingBatch", { count: picked.length }),
    );
    for (const file of picked) uploadOne(file);
  };

  const openPreview = async (d: ApiDocument) => {
    setPreviewLoadingId(d.id);
    try {
      const { document, chunks } = await getDocumentChunks(d.id);
      setPreview(docWithChunks(document, chunks));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("page.openFailed"));
    } finally {
      setPreviewLoadingId(null);
    }
  };

  // Смена поиска или фильтра типа возвращает таблицу на первую страницу.
  useEffect(() => {
    setDocsPage(1);
  }, [query, typeFilter]);

  const visible = docs ?? [];
  const docsPageCount = Math.max(1, Math.ceil(total / DOCS_PAGE));
  const hasFilters = query !== "" || typeFilter !== "all";

  // Страница опустела (сузился фильтр, документ удалён) — откат к последней.
  useEffect(() => {
    if (phase === "ready" && docsPage > docsPageCount) setDocsPage(docsPageCount);
  }, [phase, docsPage, docsPageCount]);

  const sessionIds = Object.keys(sessionStatus);
  const sessionDone = sessionIds.filter((id) =>
    ["indexed", "failed"].includes(sessionStatus[id] ?? ""),
  ).length;
  const sessionIndexed = sessionIds.filter((id) => sessionStatus[id] === "indexed").length;
  const sessionAllDone = sessionIds.length > 0 && sessionDone === sessionIds.length;

  return (
    <div
      className="mx-auto w-full max-w-5xl px-4 py-6 md:py-8"
      onDragEnter={(e) => {
        if (![...e.dataTransfer.types].includes("Files")) return;
        e.preventDefault();
        dragDepth.current += 1;
        setDragOver(true);
      }}
      onDragOver={(e) => {
        if ([...e.dataTransfer.types].includes("Files")) e.preventDefault();
      }}
      onDragLeave={() => {
        dragDepth.current = Math.max(0, dragDepth.current - 1);
        if (dragDepth.current === 0) setDragOver(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        dragDepth.current = 0;
        setDragOver(false);
        onFiles(e.dataTransfer.files);
      }}
    >
      {dragOver && (
        <div className="pointer-events-none fixed inset-0 z-50 flex items-center justify-center bg-background/80">
          <div className="rounded-2xl border-2 border-dashed border-brand bg-card px-10 py-8 text-center">
            <Upload className="mx-auto size-8 text-brand" aria-hidden />
            <p className="mt-3 text-lg font-medium">{t("page.dropHere")}</p>
            <p className="mt-1 text-sm text-muted-foreground">{t("page.dropHint")}</p>
          </div>
        </div>
      )}
      <PageHeader
        kicker={t("page.kicker")}
        title={t("page.title")}
        description={t("page.description")}
        actions={
          <>
            {live && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <output className="relative mr-1 flex size-2" aria-label={t("page.liveAria")}>
                    <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-ok opacity-50" />
                    <span className="relative inline-flex size-2 rounded-full bg-ok" />
                  </output>
                </TooltipTrigger>
                <TooltipContent>{t("page.liveTooltip")}</TooltipContent>
              </Tooltip>
            )}
            <Button variant="outline" onClick={() => void refresh()} disabled={refreshing}>
              <RefreshCw className={`size-4 ${refreshing ? "animate-spin" : ""}`} aria-hidden />
              {t("actions.refresh", { ns: "common" })}
            </Button>
            <Button onClick={() => fileInput.current?.click()}>
              {uploading > 0 ? <Spinner /> : <Upload className="size-4" aria-hidden />}
              {t("page.upload")}
            </Button>
            <input
              ref={fileInput}
              type="file"
              multiple
              accept={UPLOAD_ACCEPT}
              className="hidden"
              aria-label={t("page.chooseFiles")}
              onChange={(e) => {
                onFiles(e.target.files);
                e.target.value = "";
              }}
            />
          </>
        }
      />

      {sessionIds.length > 0 && (
        <div
          className={`mt-5 flex flex-wrap items-center justify-between gap-3 rounded-xl border px-4 py-3 ${
            sessionAllDone ? "border-transparent bg-ok-wash" : "bg-secondary/40"
          }`}
        >
          <div className="flex items-center gap-3">
            {sessionAllDone ? (
              <span className="text-sm font-medium text-ok">
                {t("page.sessionReady", { count: sessionIndexed })}
              </span>
            ) : (
              <>
                <Spinner />
                <span className="text-sm">
                  {t("page.sessionProgress", { done: sessionDone, total: sessionIds.length })}
                </span>
              </>
            )}
          </div>
          {sessionAllDone && sessionIndexed > 0 && (
            <Button size="sm" variant="brand" asChild>
              <Link to="/hypotheses">
                <Wand2 className="size-4" aria-hidden />
                {t("page.sessionGenerate")}
              </Link>
            </Button>
          )}
        </div>
      )}

      <div className="mt-5 flex flex-col gap-3 sm:flex-row sm:items-center">
        <SearchField
          className="flex-1"
          value={filter}
          onChange={setFilter}
          placeholder={t("page.searchPlaceholder")}
          ariaLabel={t("page.searchAria")}
        />
        <Select value={typeFilter} onValueChange={(v) => setTypeFilter(v as "all" | FileType)}>
          <SelectTrigger className="w-full sm:w-52" aria-label={t("page.typeFilterAria")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("page.allTypes")}</SelectItem>
            {TYPE_FILTERS.map((type) => (
              <SelectItem key={type} value={type}>
                {t(`page.typeFilters.${type}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {transfers.length > 0 && (
        <div className="mt-4 space-y-2">
          {transfers.map((tr) => (
            <div key={tr.id} className="rounded-lg border bg-card px-4 py-3">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">{tr.name}</p>
                  <p className="mt-0.5 text-xs text-muted-foreground">
                    {t("page.transferProgress", {
                      percent: Math.round(tr.fraction * 100),
                      size: humanSize(tr.totalBytes),
                    })}
                  </p>
                </div>
                <span className="text-sm tabular-nums text-muted-foreground">
                  {Math.round(tr.fraction * 100)}%
                </span>
                <Button variant="outline" size="sm" onClick={tr.cancel}>
                  {t("page.cancelUpload")}
                </Button>
              </div>
              <div
                className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted"
                // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role -- стилизованный индикатор, native progress не стилизуется под дизайн
                role="progressbar"
                aria-valuenow={Math.round(tr.fraction * 100)}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-label={t("page.uploadingAria", { name: tr.name })}
              >
                <div
                  className="h-full rounded-full bg-primary transition-[width] duration-300"
                  style={{ width: `${Math.round(tr.fraction * 100)}%` }}
                />
              </div>
            </div>
          ))}
        </div>
      )}

      {phase === "error" ? (
        <ErrorState message={t("page.loadErrorState")} onRetry={() => void load()} />
      ) : (
        <div className="mt-4 rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("page.columns.document")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("page.columns.size")}</TableHead>
                <TableHead className="hidden text-right md:table-cell">
                  <span className="inline-flex items-center gap-1">
                    {t("page.columns.chunks")}
                    <InfoHint>{t("page.chunksHint")}</InfoHint>
                  </span>
                </TableHead>
                <TableHead className="hidden lg:table-cell">{t("page.columns.updated")}</TableHead>
                <TableHead>{t("page.columns.status")}</TableHead>
                <TableHead className="w-16 text-right">{t("page.columns.actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {phase === "loading" &&
                [0, 1, 2, 3, 4].map((i) => (
                  <TableRow key={i}>
                    <TableCell>
                      <Skeleton className="h-4 w-48 max-w-full" />
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <Skeleton className="h-4 w-14" />
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <Skeleton className="ml-auto h-4 w-10" />
                    </TableCell>
                    <TableCell className="hidden lg:table-cell">
                      <Skeleton className="h-4 w-28" />
                    </TableCell>
                    <TableCell>
                      <Skeleton className="h-5 w-20 rounded-full" />
                    </TableCell>
                    <TableCell>
                      <Skeleton className="ml-auto size-7 rounded-md" />
                    </TableCell>
                  </TableRow>
                ))}

              {phase === "ready" &&
                visible.map((d) => {
                  const indexed = d.status === "indexed";
                  return (
                    <TableRow key={d.id}>
                      <TableCell className="max-w-72 font-medium">
                        <div className="flex min-w-0 flex-col">
                          <div className="flex min-w-0 items-center gap-2">
                            <span className="truncate" title={documentTitle(d)}>
                              {documentTitle(d)}
                            </span>
                            <span className="shrink-0">
                              <FileTypeBadge type={fileTypeOf(d.filename, d.mime_type)} />
                            </span>
                            {d.kind === "hypotheses" && (
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Badge variant="warn" className="shrink-0">
                                    {t("page.hypothesesDoc.badge")}
                                  </Badge>
                                </TooltipTrigger>
                                <TooltipContent className="max-w-xs">
                                  {t("page.hypothesesDoc.hint")}
                                </TooltipContent>
                              </Tooltip>
                            )}
                          </div>
                          {d.title?.trim() && (
                            <span
                              className="truncate text-xs font-normal text-muted-foreground"
                              title={d.filename}
                            >
                              {d.filename}
                            </span>
                          )}
                          {(d.author?.trim() || d.published_at?.trim()) && (
                            <span className="truncate text-xs font-normal text-muted-foreground">
                              {[d.author?.trim(), publishedLabel(d.published_at)]
                                .filter(Boolean)
                                .join(" · ")}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="hidden text-muted-foreground md:table-cell">
                        {humanSize(d.size)}
                      </TableCell>
                      <TableCell className="hidden text-right tabular-nums text-muted-foreground md:table-cell">
                        {d.chunk_count}
                      </TableCell>
                      <TableCell className="hidden text-muted-foreground lg:table-cell">
                        {formatDateTime(d.updated_at)}
                      </TableCell>
                      <TableCell>
                        <PipelineStepper
                          status={d.status}
                          statusMsg={d.status_msg}
                          progress={stageProgress[d.id]}
                        />
                      </TableCell>
                      <TableCell className="text-right">
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <span className="inline-flex">
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                aria-label={t("page.previewAria", { name: d.filename })}
                                disabled={!indexed || previewLoadingId === d.id}
                                onClick={() => void openPreview(d)}
                              >
                                {previewLoadingId === d.id ? (
                                  <Spinner />
                                ) : (
                                  <Eye className="size-4" aria-hidden />
                                )}
                              </Button>
                            </span>
                          </TooltipTrigger>
                          <TooltipContent>
                            {indexed ? t("page.preview") : t("page.stillIndexing")}
                          </TooltipContent>
                        </Tooltip>
                      </TableCell>
                    </TableRow>
                  );
                })}

              {phase === "ready" && visible.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={6}
                    className="py-10 text-center text-sm text-muted-foreground"
                  >
                    {hasFilters ? t("page.emptyFiltered") : t("page.emptyNoDocs")}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      {phase === "ready" && visible.length > 0 && (
        <>
          <p className="mt-2 text-center text-xs text-muted-foreground">
            {t("page.pageRange", {
              from: (docsPage - 1) * DOCS_PAGE + 1,
              to: (docsPage - 1) * DOCS_PAGE + visible.length,
              count: total,
            })}
          </p>
          <Pagination
            className="mt-2"
            page={docsPage}
            pageCount={docsPageCount}
            onPage={setDocsPage}
          />
        </>
      )}

      <DocPreviewSheet doc={preview} onClose={() => setPreview(null)} />
    </div>
  );
}
