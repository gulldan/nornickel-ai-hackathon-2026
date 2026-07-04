import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Archive,
  ArchiveRestore,
  ArrowRight,
  Check,
  Lightbulb,
  Network,
  Pencil,
  Plus,
  Target,
  Trash2,
  Wand2,
} from "lucide-react";
import { toast } from "sonner";

import {
  deleteKPI,
  graphHypotheses,
  listKPIs,
  saveDirectionAsHypothesis,
  suggestKPIs,
  updateKPI,
  type ApiKPI,
  type GraphHypothesis,
  type KpiSuggestion,
} from "@/features/kpi/api";
import { DocPreviewSheet, useDocPreview } from "@/features/document";
import {
  isFallbackDraft,
  isResearchDirection,
  listHypotheses,
  type ApiHypothesis,
} from "@/features/hypothesis";
import { GoalPromptDialog } from "@/features/kpi/ui/GoalPromptDialog";
import { AiNote } from "@/shared/ui/AiNote";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { Card, CardContent } from "@/shared/ui/Card";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/shared/ui/Dialog";
import { CardGridSkeleton } from "@/shared/ui/CardGridSkeleton";
import { EmptyState } from "@/shared/ui/EmptyState";
import { ErrorState } from "@/shared/ui/ErrorState";
import { PageHeader } from "@/shared/ui/PageHeader";
import { Pagination } from "@/shared/ui/Pagination";
import { RichText } from "@/shared/ui/RichText";
import { Section } from "@/shared/ui/Section";
import { Segmented } from "@/shared/ui/Segmented";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/shared/ui/Sheet";
import { Spinner } from "@/shared/ui/Spinner";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/Tooltip";
import { InfoHint } from "@/shared/ui/InfoHint";
import { useBackgroundTasks } from "@/features/task";
import { currentLocale, i18n } from "@/shared/i18n";
import { GLOSSARY } from "@/shared/glossary";
import {
  fetchHypothesisRuntimeSettings,
  loadHypothesisRuntimeSettings,
} from "@/shared/appSettings";

type Phase = "loading" | "ready" | "error";

const KPI_PAGE = 12;

function fmtNum(n: number | null): string {
  return n === null ? "—" : new Intl.NumberFormat(currentLocale()).format(n);
}

interface KpiHypothesisCounts {
  hypotheses: number;
  directions: number;
  drafts: number;
}

function emptyKpiCounts(): KpiHypothesisCounts {
  return { hypotheses: 0, directions: 0, drafts: 0 };
}

/**
 * «Цели (KPI)» (Goals/KPIs) — target metrics that hypotheses are matched to. For
 * each goal you see the metric (from baseline to target value), the direction and
 * how many hypotheses relate to it; new ones can also be generated from here.
 */
function kpiToInput(k: ApiKPI, status: string) {
  return {
    title: k.title,
    description: k.description ?? "",
    metric: k.metric ?? "",
    unit: k.unit ?? "",
    direction: k.direction ?? "",
    baseline: k.baseline ?? null,
    target: k.target ?? null,
    function_area: k.function_area ?? "",
    status,
    detail: k.detail ?? {},
  };
}

export function KpiPage() {
  const navigate = useNavigate();
  const { t } = useTranslation("kpi");
  const [phase, setPhase] = useState<Phase>("loading");
  const [runtimeSettings, setRuntimeSettings] = useState(loadHypothesisRuntimeSettings);
  const [kpis, setKpis] = useState<ApiKPI[]>([]);
  const [kpiPage, setKpiPage] = useState(1);
  const [hyps, setHyps] = useState<ApiHypothesis[]>([]);
  // Направления из графа знаний: открываются в боковой панели по клику.
  // Кэш живёт, пока открыта страница, — повторное открытие мгновенное.
  const [graphByKpi, setGraphByKpi] = useState<Record<string, GraphHypothesis[]>>({});
  const [directionsKpi, setDirectionsKpi] = useState<ApiKPI | null>(null);
  const [graphPhase, setGraphPhase] = useState<Phase>("ready");
  const [savingDirection, setSavingDirection] = useState<string | null>(null);
  const [savedDirections, setSavedDirections] = useState<Set<string>>(new Set());
  const { isTaskRunning } = useBackgroundTasks();
  const { preview, openCitation, close: closePreview, retry: retryPreview } = useDocPreview();

  const [goalDialog, setGoalDialog] = useState<{
    open: boolean;
    kpiId?: string;
    prompt?: string;
    editKpi?: ApiKPI | null;
  }>({ open: false });
  const [view, setView] = useState<"active" | "archived">("active");
  const [toDelete, setToDelete] = useState<ApiKPI | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);

  // Corpus-mined goal recommendations, fetched on demand (the LLM pass is
  // expensive, so never on page load). "idle" ⇒ the section is hidden.
  const [suggestPhase, setSuggestPhase] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [suggestions, setSuggestions] = useState<KpiSuggestion[]>([]);

  const load = useCallback(async () => {
    setPhase("loading");
    try {
      // Целям нужны только счётчики — лёгкие ссылки вместо полных гипотез.
      const [k, h] = await Promise.all([listKPIs(), listHypotheses({ view: "ref" })]);
      setKpis(k);
      setHyps(h);
      setPhase("ready");
    } catch {
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const reloadSettings = () => setRuntimeSettings(loadHypothesisRuntimeSettings());
    let cancelled = false;
    fetchHypothesisRuntimeSettings()
      .then((next) => {
        if (!cancelled) setRuntimeSettings(next);
      })
      .catch(() => {
        reloadSettings();
      });
    window.addEventListener("storage", reloadSettings);
    window.addEventListener("hypothesis-runtime-settings:update", reloadSettings);
    return () => {
      cancelled = true;
      window.removeEventListener("storage", reloadSettings);
      window.removeEventListener("hypothesis-runtime-settings:update", reloadSettings);
    };
  }, []);

  const countByKpi = useMemo(() => {
    const m = new Map<string, KpiHypothesisCounts>();
    for (const h of hyps) {
      if (!h.kpi_id) continue;
      if (h.status === "archived" || h.status === "rejected") continue;
      const counts = m.get(h.kpi_id) ?? emptyKpiCounts();
      if (isFallbackDraft(h)) {
        counts.drafts++;
      } else if (isResearchDirection(h)) {
        counts.directions++;
      } else {
        counts.hypotheses++;
      }
      m.set(h.kpi_id, counts);
    }
    return m;
  }, [hyps]);

  const activeKpis = kpis.filter((k) => k.status !== "archived");
  const archivedKpis = kpis.filter((k) => k.status === "archived");
  const shownKpis = view === "archived" ? archivedKpis : activeKpis;
  const kpiPageCount = Math.max(1, Math.ceil(shownKpis.length / KPI_PAGE));
  const safeKpiPage = Math.min(kpiPage, kpiPageCount);
  const pagedKpis = shownKpis.slice((safeKpiPage - 1) * KPI_PAGE, safeKpiPage * KPI_PAGE);
  const setStatus = async (k: ApiKPI, status: "active" | "archived") => {
    try {
      const updated = await updateKPI(k.id, kpiToInput(k, status));
      setKpis((prev) => prev.map((x) => (x.id === k.id ? updated : x)));
      toast.success(t(status === "archived" ? "toast.archived" : "toast.unarchived"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("toast.updateError"));
    }
  };

  const confirmDelete = async () => {
    if (!toDelete) return;
    setDeleteBusy(true);
    try {
      await deleteKPI(toDelete.id);
      setKpis((prev) => prev.filter((k) => k.id !== toDelete.id));
      setToDelete(null);
      toast.success(t("toast.deleted"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("toast.deleteError"));
    } finally {
      setDeleteBusy(false);
    }
  };

  const loadGraphHypotheses = async (kpi: ApiKPI) => {
    setGraphPhase("loading");
    try {
      const items = await graphHypotheses(kpi.id);
      setGraphByKpi((prev) => ({ ...prev, [kpi.id]: items }));
      setGraphPhase("ready");
    } catch {
      setGraphPhase("error");
    }
  };

  const openDirections = (kpi: ApiKPI) => {
    setDirectionsKpi(kpi);
    if (graphByKpi[kpi.id] !== undefined) {
      setGraphPhase("ready");
    } else {
      void loadGraphHypotheses(kpi);
    }
  };

  const saveDirection = async (kpi: ApiKPI, item: GraphHypothesis, key: string) => {
    setSavingDirection(key);
    try {
      await saveDirectionAsHypothesis(kpi.id, item);
      setSavedDirections((prev) => new Set(prev).add(key));
      toast.success(t("toast.directionSaved"), {
        description: t("toast.directionSavedHint"),
      });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("toast.directionSaveError"));
    } finally {
      setSavingDirection(null);
    }
  };

  const loadSuggestions = async () => {
    setSuggestPhase("loading");
    try {
      setSuggestions(await suggestKPIs());
      setSuggestPhase("ready");
    } catch {
      setSuggestPhase("error");
    }
  };

  // Accept a recommendation: compose a plain-text prompt from the suggestion and
  // open the unified goal dialog, so the user reviews (and can add files) before
  // anything is persisted.
  const acceptSuggestion = (s: KpiSuggestion) => {
    const parts = [
      (s.title ?? "").trim(),
      (s.metric ?? "").trim() &&
        `${t("wizard.suggestionMetric")}: ${(s.metric ?? "").trim()}${s.unit ? `, ${s.unit}` : ""}`,
      (s.rationale ?? "").trim(),
    ].filter(Boolean);
    setGoalDialog({ open: true, prompt: parts.join("\n") });
  };

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <PageHeader
        kicker={t("header.kicker")}
        title={t("header.title")}
        badge={<InfoHint label={t("header.hintLabel")}>{GLOSSARY.goal}</InfoHint>}
        description={t("header.description")}
        actions={
          <>
            <Button
              variant="outline"
              size="sm"
              disabled={suggestPhase === "loading"}
              onClick={() => void loadSuggestions()}
            >
              <Lightbulb className="size-4" aria-hidden />
              {suggestPhase === "loading" ? t("actions.suggestLoading") : t("actions.suggest")}
            </Button>
            <Button variant="brand" size="sm" onClick={() => setGoalDialog({ open: true })}>
              <Plus className="size-4" aria-hidden />
              {t("actions.create")}
            </Button>
          </>
        }
      />

      {suggestPhase !== "idle" && (
        <Section
          title={t("suggestions.title")}
          description={t("suggestions.description")}
          className="mt-6"
        >
          {suggestPhase === "loading" && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner size="sm" />
              {t("suggestions.loading")}
            </div>
          )}
          {suggestPhase === "error" && (
            <ErrorState message={t("suggestions.error")} onRetry={() => void loadSuggestions()} />
          )}
          {suggestPhase === "ready" && suggestions.length === 0 && (
            <p className="text-sm text-muted-foreground">{t("suggestions.empty")}</p>
          )}
          {suggestPhase === "ready" && suggestions.length > 0 && (
            <div className="space-y-3">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {suggestions.map((s) => (
                  <SuggestionCard
                    key={`${s.title}|${s.metric ?? ""}|${s.function_area ?? ""}`}
                    item={s}
                    onAccept={() => acceptSuggestion(s)}
                  />
                ))}
              </div>
              <AiNote>{t("suggestions.aiNote")}</AiNote>
            </div>
          )}
        </Section>
      )}

      {phase === "loading" && <CardGridSkeleton />}
      {phase === "error" && <ErrorState message={t("list.error")} onRetry={() => void load()} />}
      {phase === "ready" && kpis.length === 0 && (
        <div className="mt-5 space-y-5">
          <EmptyState
            icon={Target}
            title={t("empty.title")}
            description={t("empty.description")}
            action={
              <Button size="sm" onClick={() => setGoalDialog({ open: true })}>
                <Plus className="size-4" aria-hidden />
                {t("actions.create")}
              </Button>
            }
          />
        </div>
      )}
      {phase === "ready" && kpis.length > 0 && archivedKpis.length > 0 && (
        <Segmented
          aria-label={t("list.viewAria")}
          className="mt-6"
          value={view}
          onChange={(v) => {
            setView(v);
            setKpiPage(1);
          }}
          options={[
            { value: "active", label: t("list.activeTab", { count: activeKpis.length }) },
            { value: "archived", label: t("list.archivedTab", { count: archivedKpis.length }) },
          ]}
        />
      )}
      {phase === "ready" && kpis.length > 0 && shownKpis.length === 0 && (
        <p className="mt-6 text-sm text-muted-foreground">{t("list.archiveEmpty")}</p>
      )}
      {phase === "ready" && shownKpis.length > 0 && (
        <div className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {pagedKpis.map((k) => {
            const counts = countByKpi.get(k.id) ?? emptyKpiCounts();
            const generating = isTaskRunning(`kpi:${k.id}:generate`);
            const hasScale = k.baseline !== null || k.target !== null;
            const archived = k.status === "archived";
            return (
              <Card key={k.id} className={archived ? "flex flex-col opacity-80" : "flex flex-col"}>
                <CardContent className="flex flex-1 flex-col gap-3 p-4">
                  {k.function_area && (
                    <Badge variant="secondary" className="self-start">
                      {k.function_area}
                    </Badge>
                  )}
                  <div className="flex items-start justify-between gap-1.5">
                    <h3 className="text-[15px] font-medium leading-snug">{k.title}</h3>
                    <div className="-mr-1.5 -mt-1 flex shrink-0">
                      {!archived && (
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={t("card.edit")}
                              onClick={() => setGoalDialog({ open: true, editKpi: k })}
                            >
                              <Pencil className="size-3.5" aria-hidden />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>{t("card.edit")}</TooltipContent>
                        </Tooltip>
                      )}
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={archived ? t("card.unarchive") : t("card.archive")}
                            onClick={() => void setStatus(k, archived ? "active" : "archived")}
                          >
                            {archived ? (
                              <ArchiveRestore className="size-3.5" aria-hidden />
                            ) : (
                              <Archive className="size-3.5" aria-hidden />
                            )}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>
                          {archived ? t("card.unarchive") : t("card.archiveHint")}
                        </TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t("card.delete")}
                            onClick={() => setToDelete(k)}
                          >
                            <Trash2 className="size-3.5" aria-hidden />
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>{t("card.delete")}</TooltipContent>
                      </Tooltip>
                    </div>
                  </div>

                  {hasScale && (
                    <div>
                      {k.metric && <p className="kicker mb-1">{k.metric}</p>}
                      <KpiValueLine baseline={k.baseline} target={k.target} unit={k.unit} />
                    </div>
                  )}

                  {k.description && (
                    <p className="line-clamp-2 text-sm text-muted-foreground">{k.description}</p>
                  )}

                  <div className="mt-auto space-y-3 pt-1">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t pt-3 text-sm">
                      <button
                        type="button"
                        onClick={() =>
                          navigate(`/hypotheses?kpi=${k.id}&kind=hypotheses&queue=all`)
                        }
                        className="font-medium text-foreground hover:underline"
                      >
                        {t("card.hypotheses", { count: counts.hypotheses })}
                      </button>
                      {counts.drafts > 0 && (
                        <button
                          type="button"
                          onClick={() => navigate(`/hypotheses?kpi=${k.id}&kind=drafts&queue=all`)}
                          className="text-muted-foreground hover:text-foreground hover:underline"
                        >
                          {t("card.drafts", { count: counts.drafts })}
                        </button>
                      )}
                      {counts.directions > 0 && (
                        <button
                          type="button"
                          onClick={() =>
                            navigate(`/hypotheses?kpi=${k.id}&kind=directions&queue=all`)
                          }
                          className="text-muted-foreground hover:text-foreground hover:underline"
                        >
                          {t("card.directionsCount", { count: counts.directions })}
                        </button>
                      )}
                    </div>

                    {!archived && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="brand"
                            size="sm"
                            className="w-full"
                            disabled={generating}
                            onClick={() => setGoalDialog({ open: true, kpiId: k.id })}
                          >
                            <Wand2 className="size-4" aria-hidden />
                            {generating ? t("card.generating") : t("card.generate")}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent className="max-w-64">
                          {t("card.generateHint")}
                        </TooltipContent>
                      </Tooltip>
                    )}

                    {!archived && (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button
                            variant="outline"
                            size="sm"
                            className="w-full"
                            onClick={() => openDirections(k)}
                          >
                            <Network className="size-4" aria-hidden />
                            {t("card.directions")}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent className="max-w-64">
                          {t("card.directionsHint")}
                        </TooltipContent>
                      </Tooltip>
                    )}
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
      {phase === "ready" && (
        <Pagination
          className="mt-5"
          page={safeKpiPage}
          pageCount={kpiPageCount}
          onPage={setKpiPage}
        />
      )}

      <Dialog open={toDelete !== null} onOpenChange={(open) => !open && setToDelete(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t("delete.title", { title: toDelete?.title ?? "" })}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">{t("delete.body")}</p>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              disabled={deleteBusy}
              onClick={() => setToDelete(null)}
            >
              {t("dialog.cancel")}
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={deleteBusy}
              onClick={() => void confirmDelete()}
            >
              {deleteBusy ? t("delete.deleting") : t("delete.confirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <GoalPromptDialog
        open={goalDialog.open}
        onOpenChange={(open) => setGoalDialog((prev) => ({ ...prev, open }))}
        kpis={kpis}
        initialKpiId={goalDialog.kpiId}
        initialPrompt={goalDialog.prompt}
        editKpi={goalDialog.editKpi ?? null}
        onCreated={(kpi) => setKpis((prev) => [kpi, ...prev])}
        onUpdated={(kpi) => setKpis((prev) => prev.map((x) => (x.id === kpi.id ? kpi : x)))}
        onGenerated={(created) => setHyps((prev) => [...created, ...prev])}
      />

      <DirectionsSheet
        kpi={directionsKpi}
        items={directionsKpi ? (graphByKpi[directionsKpi.id] ?? []) : []}
        phase={graphPhase}
        limit={runtimeSettings.graphDirectionLimit}
        savingKey={savingDirection}
        savedKeys={savedDirections}
        onClose={() => setDirectionsKpi(null)}
        onRetry={() => {
          if (directionsKpi) void loadGraphHypotheses(directionsKpi);
        }}
        onSave={(item, key) => {
          if (directionsKpi) void saveDirection(directionsKpi, item, key);
        }}
        onOpenDoc={(docId, filename) => openCitation({ documentId: docId, filename })}
      />

      <DocPreviewSheet
        doc={preview ? (preview.full ?? preview.doc) : null}
        fragmentId={preview?.fragmentId}
        highlight={preview?.highlight}
        loading={preview ? !preview.full && !preview.failed : false}
        error={preview?.failed ?? false}
        onRetry={retryPreview}
        onClose={closePreview}
      />
    </div>
  );
}

function KpiValueLine({
  baseline,
  target,
  unit,
}: {
  baseline: number | null;
  target: number | null;
  unit: string;
}) {
  const { t } = useTranslation("kpi");
  return (
    <p className="flex flex-wrap items-center gap-2 font-mono">
      <span className="text-lg tabular-nums">{fmtNum(baseline)}</span>
      <ArrowRight className="size-4 text-muted-foreground" aria-hidden />
      <span className="text-lg font-semibold tabular-nums text-ok">
        {target !== null ? fmtNum(target) : t("valueLine.target")}
      </span>
      {unit && <span className="text-sm text-muted-foreground">{unit}</span>}
    </p>
  );
}

function directionLabel(direction: string | undefined): string {
  switch (direction) {
    case "increase":
      return i18n.t("kpi:direction.increase");
    case "decrease":
      return i18n.t("kpi:direction.decrease");
    case "maintain":
      return i18n.t("kpi:direction.maintain");
    default:
      return "";
  }
}

/** One corpus-mined goal recommendation. Mirrors the goal-card layout (kicker
 *  area, title, mono metric line, small rationale) so it reads as the same family
 *  as the goals it sits beside. Accepting opens the shared create-goal dialog. */
function SuggestionCard({ item, onAccept }: { item: KpiSuggestion; onAccept: () => void }) {
  const { t } = useTranslation("kpi");
  const dir = directionLabel(item.direction);
  const metric = (item.metric ?? "").trim();
  return (
    <Card className="flex flex-col">
      <CardContent className="flex flex-1 flex-col gap-3 p-4">
        {item.function_area && (
          <Badge variant="secondary" className="self-start">
            {item.function_area}
          </Badge>
        )}
        <h3 className="text-[15px] font-medium leading-snug">{item.title}</h3>

        {(dir || metric) && (
          <p className="flex flex-wrap items-baseline gap-1.5 font-mono text-sm">
            {dir && <span className="font-semibold text-ok">{dir}</span>}
            {metric && <span>{metric}</span>}
            {item.unit && <span className="text-sm text-muted-foreground">{item.unit}</span>}
          </p>
        )}

        {item.rationale && (
          <p className="line-clamp-3 text-xs text-muted-foreground">{item.rationale}</p>
        )}

        <div className="mt-auto pt-1">
          <Button variant="brand" size="sm" className="w-full" onClick={onAccept}>
            <Plus className="size-4" aria-hidden />
            {t("actions.create")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/** Боковая панель «Направления из графа знаний»: мостовые цепочки
 *  процесс → свойство → цель, собранные из разных документов базы. Раньше это
 *  разворачивалось «портянкой» прямо в карточке цели и ломало сетку. */
function DirectionsSheet({
  kpi,
  items,
  phase,
  limit,
  savingKey,
  savedKeys,
  onClose,
  onRetry,
  onSave,
  onOpenDoc,
}: {
  kpi: ApiKPI | null;
  items: GraphHypothesis[];
  phase: Phase;
  limit: number;
  savingKey: string | null;
  savedKeys: Set<string>;
  onClose: () => void;
  onRetry: () => void;
  onSave: (item: GraphHypothesis, key: string) => void;
  onOpenDoc: (documentId: string, filename?: string) => void;
}) {
  const { t } = useTranslation("kpi");
  return (
    <Sheet
      open={kpi !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-xl">
        <SheetHeader className="border-b px-5 py-4 pr-12">
          <SheetTitle>{t("sheet.title")}</SheetTitle>
          <SheetDescription>{kpi?.title ?? ""}</SheetDescription>
        </SheetHeader>
        <div className="flex-1 overflow-y-auto px-5 py-4">
          <p className="text-xs leading-relaxed text-muted-foreground">{t("sheet.intro")}</p>
          {phase === "loading" && (
            <div className="mt-6 flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner size="sm" />
              {t("sheet.loading")}
            </div>
          )}
          {phase === "error" && (
            <div className="mt-6">
              <ErrorState message={t("sheet.error")} onRetry={onRetry} />
            </div>
          )}
          {phase === "ready" && items.length === 0 && (
            <div className="mt-6">
              <EmptyState
                icon={Network}
                title={t("sheet.emptyTitle")}
                description={t("sheet.emptyDescription")}
              />
            </div>
          )}
          {phase === "ready" && items.length > 0 && (
            <div className="mt-4 space-y-3">
              {items.slice(0, limit).map((item, i) => {
                const text = (item.statement ?? "").trim() || (item.title ?? "").trim();
                const key = `${kpi?.id ?? ""}:${i}:${text.slice(0, 40)}`;
                return (
                  <DirectionCard
                    key={key}
                    item={item}
                    saved={savedKeys.has(key)}
                    saving={savingKey === key}
                    disabled={savingKey !== null}
                    onSave={() => onSave(item, key)}
                    onOpenDoc={onOpenDoc}
                  />
                );
              })}
              {items.length > limit && (
                <p className="text-xs text-muted-foreground">
                  {t("sheet.shownOf", { shown: limit, total: items.length })}
                </p>
              )}
              <AiNote>{t("sheet.aiNote")}</AiNote>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function DirectionCard({
  item,
  saved,
  saving,
  disabled,
  onSave,
  onOpenDoc,
}: {
  item: GraphHypothesis;
  saved: boolean;
  saving: boolean;
  disabled: boolean;
  onSave: () => void;
  onOpenDoc: (documentId: string, filename?: string) => void;
}) {
  const { t } = useTranslation("kpi");
  const chain = [item.process, item.property, item.kpi].filter(Boolean).join(" → ");
  const text = (item.statement ?? "").trim() || (item.title ?? "").trim();
  const docs = (item.documents ?? []).filter((d) => d.id || d.filename);
  return (
    <Card className="px-4 py-3">
      {item.title && item.statement && (
        <p className="text-sm font-medium leading-snug">{item.title}</p>
      )}
      {text !== "" && <RichText className="mt-1 text-sm text-muted-foreground">{text}</RichText>}
      {chain && (
        <p className="mt-2 inline-flex items-center gap-1 rounded bg-secondary px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
          <Network className="size-3" aria-hidden />
          {chain}
        </p>
      )}
      {item.rationale && <p className="mt-1.5 text-xs text-muted-foreground">{item.rationale}</p>}
      {docs.length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-muted-foreground">{t("directionCard.sources")}</span>
          {docs.map((d) =>
            d.id ? (
              <button
                key={d.id}
                type="button"
                onClick={() => onOpenDoc(d.id ?? "", d.filename)}
                className="rounded-md border bg-background px-1.5 py-0.5 font-mono text-xs transition-colors hover:border-brand-border hover:bg-brand-wash focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {d.filename || t("directionCard.document")}
              </button>
            ) : (
              <span
                key={d.filename}
                className="rounded-md border bg-background px-1.5 py-0.5 font-mono text-xs text-muted-foreground"
              >
                {d.filename}
              </span>
            ),
          )}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-2 border-t pt-2.5">
        <Button
          variant={saved ? "ghost" : "outline"}
          size="sm"
          disabled={saved || disabled}
          onClick={onSave}
        >
          {saved ? (
            <>
              <Check className="size-3.5 text-ok" aria-hidden />
              {t("directionCard.saved")}
            </>
          ) : saving ? (
            t("directionCard.saving")
          ) : (
            <>
              <Plus className="size-3.5" aria-hidden />
              {t("directionCard.save")}
            </>
          )}
        </Button>
        {!saved && (
          <span className="text-xs text-muted-foreground">{t("directionCard.savedHint")}</span>
        )}
      </div>
    </Card>
  );
}
