import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Braces,
  Download,
  FileSpreadsheet,
  FileText,
  Lightbulb,
  Printer,
  RotateCcw,
  Sparkles,
  SquareCheckBig,
  Wand2,
  X,
} from "lucide-react";
import { toast } from "sonner";

import { type ApiCluster, listClusters } from "@/features/cluster";
import { type ApiHypothesis, getHypothesisBoard } from "@/features/hypothesis/api";
import { GoalPromptDialog, type ApiKPI, listKPIs } from "@/features/kpi";
import {
  hypothesesToCSV,
  hypothesesToDigest,
  hypothesesToJSON,
  hypothesesToJiraCSV,
  hypothesesToTasksJSON,
  downloadFile,
  isFallbackDraft,
  isResearchDirection,
  researchTypeMeta,
  researchTypeTag,
  visibleMeta,
  type ResearchTypeTag,
} from "@/features/hypothesis/model";
import {
  BOARD_LIMIT,
  KIND_TABS,
  SORTS,
  WORK_QUEUES,
  clusterLabel,
  isClosed,
  matchesQueue,
  serverOrderBy,
  verdictOf,
  type KindTab,
  type QueueKey,
  type SortKey,
} from "@/features/hypothesis/board/model";
import { HypothesisCard } from "@/features/hypothesis/board/HypothesisCard";
import { FacetMultiSelect, type FacetOption } from "@/features/hypothesis/board/FacetMultiSelect";
import { useOwnerNames } from "@/features/hypothesis/owners";
import { QueueFilter } from "@/features/hypothesis/board/QueueBar";
import { MethodologyPanel } from "@/features/hypothesis/board/MethodologyPanel";
import { DigestDialog } from "@/features/hypothesis/board/dialogs";

import { Button } from "@/shared/ui/Button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/DropdownMenu";
import { Chip } from "@/shared/ui/Chip";
import { Pagination } from "@/shared/ui/Pagination";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/Select";
import { CardGridSkeleton } from "@/shared/ui/CardGridSkeleton";
import { EmptyState } from "@/shared/ui/EmptyState";
import { ErrorState } from "@/shared/ui/ErrorState";
import { PageHeader } from "@/shared/ui/PageHeader";
import { SearchField } from "@/shared/ui/SearchField";
import { Skeleton } from "@/shared/ui/Skeleton";
import { Tabs } from "@/shared/ui/Tabs";
import { InfoHint } from "@/shared/ui/InfoHint";
import { GLOSSARY } from "@/shared/glossary";
import {
  fetchHypothesisRuntimeSettings,
  loadHypothesisRuntimeSettings,
} from "@/shared/appSettings";

type Phase = "loading" | "ready" | "error";

const HypothesisGraphView = lazy(() =>
  import("@/features/hypothesis/ui/HypothesisGraphView").then((module) => ({
    default: module.HypothesisGraphView,
  })),
);

// Осмысленные пустые состояния для каждой вкладки — вместо общего «база пуста».
// Здесь только ключи словаря (hypothesis.json); graph/methodology пустых
// состояний не показывают — им достаётся безобидный фолбэк.
const EMPTY_META = {
  hypotheses: { title: "empty.hypotheses.title", empty: "empty.hypotheses.text" },
  directions: { title: "empty.directions.title", empty: "empty.directions.text" },
  drafts: { title: "empty.drafts.title", empty: "empty.drafts.text" },
  graph: { title: "empty.hypotheses.title", empty: "empty.hypotheses.text" },
  methodology: { title: "empty.hypotheses.title", empty: "empty.hypotheses.text" },
} as const satisfies Record<KindTab, { title: string; empty: string }>;

// Пояснение активной вкладки — термины должны читаться без документации.
const TAB_HINTS = {
  hypotheses: "tabHints.hypotheses",
  directions: "tabHints.directions",
  drafts: "tabHints.drafts",
  graph: "tabHints.graph",
  methodology: "tabHints.methodology",
} as const satisfies Record<KindTab, string>;

// Фирменный синий Microsoft Word — узнаваемость формата в меню экспорта.
const WORD_BLUE = "#2B579A";

// Фасеты мультивыбора: ключи состояния, URL-параметров и полей facetRows.
const FACET_KEYS = ["company", "func", "source", "trl", "research", "kpi"] as const;
type FacetKey = (typeof FACET_KEYS)[number];

// Карточек на странице вкладки; сам портфель дозагружается с сервера целиком.
const BOARD_PAGE = 12;
// Предохранитель тихой дозагрузки: больше карточек в памяти не держим.
const BOARD_FETCH_CAP = 1000;

export function Hypotheses() {
  const { t } = useTranslation("hypothesis");
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [phase, setPhase] = useState<Phase>("loading");
  const [runtimeSettings, setRuntimeSettings] = useState(loadHypothesisRuntimeSettings);
  const [hyps, setHyps] = useState<ApiHypothesis[]>([]);
  const [clusters, setClusters] = useState<ApiCluster[]>([]);
  const [kpis, setKpis] = useState<ApiKPI[]>([]);
  const [boardTotal, setBoardTotal] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);

  const [search, setSearch] = useState("");
  // Переход «Гипотезы» из карточки цели должен показывать все гипотезы цели,
  // а не только очередь «Готовые» — иначе страница выглядит пустой. Явный
  // ?queue= в ссылке имеет приоритет.
  const [queue, setQueue] = useState<QueueKey>(() => {
    const fromUrl = searchParams.get("queue");
    if (fromUrl && WORK_QUEUES.some((q) => q.key === fromUrl)) return fromUrl as QueueKey;
    return searchParams.get("kpi") ? "all" : "ready";
  });
  // Фасеты — множественный выбор: пустой список = «все». Комбинация живёт в
  // URL (?company=a,b&trl=4,5…), так что подборку можно шарить ссылкой.
  const parseList = (key: string): string[] =>
    searchParams.get(key)?.split(",").filter(Boolean) ?? [];
  const [company, setCompany] = useState<string[]>(() => parseList("company"));
  const [func, setFunc] = useState<string[]>(() => parseList("func"));
  const [source, setSource] = useState<string[]>(() => parseList("source"));
  const [trlF, setTrlF] = useState<string[]>(() => parseList("trl"));
  const [research, setResearch] = useState<string[]>(() => parseList("research"));
  const [kpiF, setKpiF] = useState<string[]>(() => parseList("kpi"));
  const [sortBy, setSortBy] = useState<SortKey>("rank");
  const initialKind = searchParams.get("kind") as KindTab | null;
  const [kind, setKind] = useState<KindTab>(
    initialKind && KIND_TABS.has(initialKind) ? initialKind : "hypotheses",
  );

  const [boardPage, setBoardPage] = useState(1);
  const [digestOpen, setDigestOpen] = useState(false);
  const [genOpen, setGenOpen] = useState(false);
  const mounted = useRef(true);
  const ownerName = useOwnerNames();

  useEffect(() => {
    return () => {
      mounted.current = false;
    };
  }, []);

  // Мультивыбор фасетов фильтруется на клиенте (портфель и так дозагружается
  // целиком) — серверу остаются поиск и сортировка.
  const boardParams = useMemo(
    () => ({
      q: search.trim() || undefined,
      order_by: serverOrderBy(sortBy),
      limit: BOARD_LIMIT,
    }),
    [search, sortBy],
  );

  useEffect(() => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        const put = (key: string, values: string[]) => {
          if (values.length > 0) {
            next.set(key, values.join(","));
          } else {
            next.delete(key);
          }
        };
        put("company", company);
        put("func", func);
        put("source", source);
        put("trl", trlF);
        put("research", research);
        put("kpi", kpiF);
        return next;
      },
      { replace: true },
    );
  }, [company, func, source, trlF, research, kpiF, setSearchParams]);

  const loadedCount = useRef(0);
  useEffect(() => {
    loadedCount.current = hyps.length;
  }, [hyps.length]);

  // silent=true — фоновое обновление: без скелетона (иначе сетка размонтируется
  // и браузер сбрасывает скролл наверх) и без тостов, объём выданного сохраняем.
  const load = useCallback(
    async (silent = false) => {
      if (!silent) setPhase("loading");
      try {
        const board = await getHypothesisBoard({
          ...boardParams,
          limit: silent ? Math.max(BOARD_LIMIT, loadedCount.current) : BOARD_LIMIT,
          offset: 0,
        });
        setHyps(board.items);
        setBoardTotal(board.total);
        setPhase("ready");
      } catch (e) {
        if (!silent) {
          toast.error(e instanceof Error ? e.message : t("board.loadError"));
          setPhase("error");
        }
      }
    },
    [boardParams, t],
  );

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const reloadSettings = () => {
      setRuntimeSettings(loadHypothesisRuntimeSettings());
    };
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

  useEffect(() => {
    let cancelled = false;
    const loadMetadata = async () => {
      try {
        const [c, k] = await Promise.all([listClusters(), listKPIs()]);
        if (cancelled) return;
        setClusters(c);
        setKpis(k);
      } catch (e) {
        toast.error(e instanceof Error ? e.message : t("board.loadRefsError"));
      }
    };
    void loadMetadata();
    return () => {
      cancelled = true;
    };
  }, [t]);

  const loadMore = useCallback(async () => {
    if (loadingMore || hyps.length >= boardTotal) return;
    setLoadingMore(true);
    try {
      const board = await getHypothesisBoard({ ...boardParams, offset: hyps.length });
      setHyps((prev) => [...prev, ...board.items]);
      setBoardTotal(board.total);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("board.loadPageError"));
    } finally {
      setLoadingMore(false);
    }
  }, [boardParams, boardTotal, hyps.length, loadingMore, t]);

  // Тихо дозагружаем весь портфель: страничная навигация по вкладке должна
  // видеть все карточки, а не только первый серверный срез.
  useEffect(() => {
    if (
      phase !== "ready" ||
      loadingMore ||
      hyps.length >= boardTotal ||
      hyps.length >= BOARD_FETCH_CAP
    ) {
      return;
    }
    void loadMore();
  }, [phase, loadingMore, hyps.length, boardTotal, loadMore]);

  // Смена вкладки, очереди, поиска или фильтров возвращает на первую страницу.
  useEffect(() => {
    setBoardPage(1);
  }, [kind, queue, search, company, func, source, trlF, research, kpiF, sortBy]);

  const kpiById = useMemo(() => new Map(kpis.map((k) => [k.id, k.title])), [kpis]);
  const clusterById = useMemo(
    () => new Map(clusters.map((c) => [c.id, clusterLabel(c)])),
    [clusters],
  );

  // Split the board: KPI-driven hypotheses (primary) vs exploratory auto-cluster
  // "Направления" vs fallback drafts.
  const byKind = useMemo(() => {
    const directions: ApiHypothesis[] = [];
    const drafts: ApiHypothesis[] = [];
    const hypotheses: ApiHypothesis[] = [];
    for (const h of hyps) {
      if (isFallbackDraft(h)) {
        drafts.push(h);
      } else if (isResearchDirection(h)) {
        directions.push(h);
      } else {
        hypotheses.push(h);
      }
    }
    return { hypotheses, directions, drafts };
  }, [hyps]);

  const activeKindRows = useMemo(() => {
    if (kind === "directions") return byKind.directions;
    if (kind === "drafts") return byKind.drafts;
    if (kind === "methodology" || kind === "graph") return [];
    return byKind.hypotheses;
  }, [byKind, kind]);

  // Значения фасетов считаются один раз на карточку; фильтрация — на клиенте,
  // внутри фасета выбранные значения объединяются по ИЛИ, между фасетами — И.
  const facetRows = useMemo(
    () =>
      activeKindRows.map((h) => ({
        h,
        company: visibleMeta(h.organization) ?? "",
        func: visibleMeta(h.function_area) ?? "",
        source: visibleMeta(h.source_type) ?? "",
        trl: h.trl === null ? "" : String(h.trl),
        research: researchTypeTag(h.tags) ?? "",
        kpi: h.kpi_id ?? "",
      })),
    [activeKindRows],
  );
  const selectedFacets = useMemo(
    () => ({ company, func, source, trl: trlF, research, kpi: kpiF }),
    [company, func, source, trlF, research, kpiF],
  );
  const rowMatchesFacets = useCallback(
    (row: (typeof facetRows)[number], skip?: FacetKey) =>
      FACET_KEYS.every(
        (k) => k === skip || selectedFacets[k].length === 0 || selectedFacets[k].includes(row[k]),
      ),
    [selectedFacets],
  );
  const facetFiltered = useMemo(
    () => facetRows.filter((r) => rowMatchesFacets(r)).map((r) => r.h),
    [facetRows, rowMatchesFacets],
  );

  // Опции фасета со счётчиками: значения из текущей вкладки, счётчик — при
  // остальных активных фасетах (классический faceted search).
  const facetOptions = useMemo(() => {
    const make = (key: FacetKey, labelOf?: (v: string) => string): FacetOption[] => {
      const counts = new Map<string, number>();
      for (const row of facetRows) {
        if (row[key] !== "" && rowMatchesFacets(row, key)) {
          counts.set(row[key], (counts.get(row[key]) ?? 0) + 1);
        }
      }
      const all = new Set(facetRows.map((r) => r[key]).filter((v) => v !== ""));
      for (const v of selectedFacets[key]) all.add(v);
      const values = [...all].toSorted((a, b) =>
        key === "trl" ? Number(a) - Number(b) : a.localeCompare(b, "ru"),
      );
      return values.map((v) => ({ value: v, label: labelOf?.(v), count: counts.get(v) ?? 0 }));
    };
    return {
      company: make("company"),
      func: make("func"),
      source: make("source"),
      trl: make("trl", (v) => t("trl.value", { trl: v })),
      research: make("research", (v) => researchTypeMeta(v as ResearchTypeTag)?.label ?? v),
      kpi: make("kpi", (v) => kpiById.get(v) ?? v),
    };
  }, [facetRows, rowMatchesFacets, selectedFacets, kpiById, t]);

  const filtered = useMemo(
    () => facetFiltered.filter((h) => matchesQueue(h, queue, runtimeSettings)),
    [facetFiltered, queue, runtimeSettings],
  );
  const showingMethodology = kind === "methodology";
  const showingGraph = kind === "graph";

  const queueCounts = useMemo(
    () =>
      WORK_QUEUES.map((q) => ({
        key: q.key,
        label: t(q.labelKey),
        hint: t(q.hintKey),
        count: facetFiltered.filter((h) => matchesQueue(h, q.key, runtimeSettings)).length,
      })),
    [facetFiltered, runtimeSettings, t],
  );
  const autoTRLCount = useMemo(
    () => activeKindRows.filter((h) => !isClosed(h) && h.trl === null).length,
    [activeKindRows],
  );
  const autoVerifyCount = useMemo(
    () => activeKindRows.filter((h) => !isClosed(h) && verdictOf(h) === "").length,
    [activeKindRows],
  );

  useEffect(() => {
    if (phase !== "ready" || showingMethodology || (autoTRLCount === 0 && autoVerifyCount === 0)) {
      return;
    }
    const id = window.setInterval(() => void load(true), 12_000);
    return () => window.clearInterval(id);
  }, [autoTRLCount, autoVerifyCount, load, phase, showingMethodology]);

  const sorted = useMemo(() => {
    const cmp = SORTS.find((s) => s.key === sortBy) ?? SORTS[0];
    if (!cmp) return filtered;
    return filtered.toSorted((a, b) => cmp.value(b) - cmp.value(a));
  }, [filtered, sortBy]);

  const boardPageCount = Math.max(1, Math.ceil(sorted.length / BOARD_PAGE));
  const safeBoardPage = Math.min(boardPage, boardPageCount);
  const pagedRows = sorted.slice((safeBoardPage - 1) * BOARD_PAGE, safeBoardPage * BOARD_PAGE);

  const kindTabs = useMemo(
    () => [
      { id: "hypotheses", label: t("tabs.hypotheses"), badge: byKind.hypotheses.length },
      { id: "directions", label: t("tabs.directions"), badge: byKind.directions.length },
      { id: "drafts", label: t("tabs.drafts"), badge: byKind.drafts.length },
      { id: "graph", label: t("tabs.graph") },
      { id: "methodology", label: t("tabs.methodology") },
    ],
    [byKind, t],
  );

  const facetsActive = FACET_KEYS.some((k) => selectedFacets[k].length > 0);
  const filtersActive = search !== "" || queue !== "ready" || facetsActive;
  // Панель фильтров/очередей нужна, только когда есть что фильтровать. На пустой
  // вкладке (нет черновиков/направлений) показываем сразу пустое состояние.
  const showToolbar = !showingMethodology && (activeKindRows.length > 0 || filtersActive);
  const resetFilters = () => {
    setSearch("");
    setQueue("ready");
    setCompany([]);
    setFunc([]);
    setSource([]);
    setTrlF([]);
    setResearch([]);
    setKpiF([]);
  };

  // Чипы активных значений: комбинацию видно целиком, любое значение снимается
  // в один клик без открытия меню.
  const facetSetters: Record<FacetKey, (next: string[]) => void> = {
    company: setCompany,
    func: setFunc,
    source: setSource,
    trl: setTrlF,
    research: setResearch,
    kpi: setKpiF,
  };
  const facetChips = FACET_KEYS.flatMap((key) =>
    selectedFacets[key].map((value) => ({
      key,
      value,
      label: facetOptions[key].find((o) => o.value === value)?.label ?? value,
      remove: () => facetSetters[key](selectedFacets[key].filter((v) => v !== value)),
    })),
  );

  const exportCSV = () => {
    if (filtered.length === 0) {
      toast.error(t("export.empty"));
      return;
    }
    downloadFile(t("export.csvFileName"), hypothesesToCSV(filtered), "text/csv;charset=utf-8");
  };

  const exportJSON = () => {
    if (filtered.length === 0) {
      toast.error(t("export.empty"));
      return;
    }
    downloadFile(
      t("export.jsonFileName"),
      hypothesesToJSON(filtered),
      "application/json;charset=utf-8",
    );
  };

  const exportJira = () => {
    if (filtered.length === 0) {
      toast.error(t("export.empty"));
      return;
    }
    downloadFile(t("export.jiraFileName"), hypothesesToJiraCSV(filtered), "text/csv;charset=utf-8");
  };

  const exportTasks = () => {
    if (filtered.length === 0) {
      toast.error(t("export.empty"));
      return;
    }
    downloadFile(
      t("export.tasksFileName"),
      hypothesesToTasksJSON(filtered),
      "application/json;charset=utf-8",
    );
  };

  const exportDocx = () => {
    const job = import("@/features/hypothesis/report/reportDocx").then(({ downloadReportDocx }) =>
      downloadReportDocx(),
    );
    toast.promise(job, {
      loading: t("export.docxLoading"),
      success: t("export.docxReady"),
      error: t("export.docxError"),
    });
  };

  const digest = useMemo(() => hypothesesToDigest(filtered), [filtered]);
  const onGenerated = (created: ApiHypothesis[]) => {
    if (!mounted.current) return;
    setHyps((prev) => [...created, ...prev]);
    setBoardTotal((prev) => prev + created.length);
  };

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <PageHeader
        kicker={t("board.kicker")}
        title={t("board.title")}
        badge={<InfoHint label={t("board.whatIsHypothesis")}>{GLOSSARY.hypothesis}</InfoHint>}
        description={t("board.description")}
        actions={
          <>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" disabled={showingMethodology || showingGraph}>
                  <Download className="size-4" aria-hidden />
                  {t("actions.export")}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={exportCSV}>
                  <FileSpreadsheet className="text-ok" aria-hidden />
                  {t("actions.exportCsv")}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={exportJSON}>
                  <Braces className="text-warn" aria-hidden />
                  {t("actions.exportJson")}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={exportJira}>
                  <JiraIcon />
                  {t("actions.exportJira")}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={exportTasks}>
                  <SquareCheckBig className="text-brand" aria-hidden />
                  {t("actions.exportTasks")}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem onSelect={exportDocx}>
                  <FileText style={{ color: WORD_BLUE }} aria-hidden />
                  {t("actions.exportDocx")}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => navigate("/hypotheses/report")}>
                  <Printer className="text-muted-foreground" aria-hidden />
                  {t("actions.exportPdf")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDigestOpen(true)}
              disabled={showingMethodology || showingGraph || filtered.length === 0}
            >
              <Sparkles className="size-4" aria-hidden />
              {t("actions.digest")}
            </Button>
            <Button variant="brand" size="sm" onClick={() => setGenOpen(true)}>
              <Wand2 className="size-4" aria-hidden />
              {t("actions.generate")}
            </Button>
          </>
        }
      />

      <Tabs
        className="mt-6"
        tabs={kindTabs}
        active={kind}
        onChange={(id) => setKind(id as KindTab)}
      />
      <p className="mt-3 max-w-3xl text-sm text-muted-foreground">{t(TAB_HINTS[kind])}</p>

      {showingMethodology ? (
        <MethodologyPanel />
      ) : showingGraph ? (
        <Suspense fallback={<Skeleton className="mt-6 h-[680px] w-full rounded-xl" />}>
          <HypothesisGraphView />
        </Suspense>
      ) : (
        <>
          {kind !== "directions" && showToolbar && (
            <div className="mt-5 flex flex-wrap items-center justify-between gap-3">
              <QueueFilter queues={queueCounts} active={queue} onSelect={setQueue} />
              <Select value={sortBy} onValueChange={(v) => setSortBy(v as SortKey)}>
                <SelectTrigger size="sm" className="w-auto min-w-48">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SORTS.map((s) => (
                    <SelectItem key={s.key} value={s.key}>
                      {t(s.labelKey)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          {showToolbar && (
            <div className="mt-4 flex flex-wrap items-center gap-2">
              <SearchField
                className="min-w-56 flex-1"
                value={search}
                onChange={setSearch}
                placeholder={t("board.searchPlaceholder")}
                ariaLabel={t("board.searchAria")}
              />
              <FacetMultiSelect
                label={t("board.facetCompany")}
                options={facetOptions.company}
                selected={company}
                onChange={setCompany}
              />
              <FacetMultiSelect
                label={t("board.facetFunc")}
                options={facetOptions.func}
                selected={func}
                onChange={setFunc}
              />
              <FacetMultiSelect
                label={t("board.facetSource")}
                options={facetOptions.source}
                selected={source}
                onChange={setSource}
              />
              <FacetMultiSelect
                label={t("board.facetTrl")}
                options={facetOptions.trl}
                selected={trlF}
                onChange={setTrlF}
              />
              <FacetMultiSelect
                label={t("board.facetResearch")}
                options={facetOptions.research}
                selected={research}
                onChange={setResearch}
              />
              {kpis.length > 0 && (
                <FacetMultiSelect
                  label={t("board.facetGoal")}
                  options={facetOptions.kpi}
                  selected={kpiF}
                  onChange={setKpiF}
                />
              )}
              {filtersActive && (
                <Button variant="ghost" size="sm" onClick={resetFilters}>
                  <RotateCcw className="size-4" aria-hidden />
                  {t("board.reset")}
                </Button>
              )}
            </div>
          )}

          {facetChips.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-1.5">
              {facetChips.map((chip) => (
                <Chip
                  key={`${chip.key}:${chip.value}`}
                  onClick={chip.remove}
                  aria-label={t("board.chipRemove", { value: chip.label })}
                  className="gap-1 py-1 text-xs"
                >
                  {chip.label}
                  <X className="size-3 text-muted-foreground" aria-hidden />
                </Chip>
              ))}
            </div>
          )}

          {phase === "loading" && <CardGridSkeleton />}
          {phase === "error" && (
            <ErrorState message={t("board.loadError")} onRetry={() => void load()} />
          )}
          {phase === "ready" && filtered.length === 0 && (
            <EmptyState
              icon={Lightbulb}
              title={t(EMPTY_META[kind].title)}
              description={
                filtersActive
                  ? t("board.emptyFiltered")
                  : activeKindRows.length === 0
                    ? t(EMPTY_META[kind].empty)
                    : t("board.emptyQueue")
              }
              action={
                filtersActive ? (
                  <Button variant="outline" size="sm" onClick={resetFilters}>
                    {t("board.resetFilters")}
                  </Button>
                ) : kind === "hypotheses" && activeKindRows.length === 0 ? (
                  <Button size="sm" onClick={() => setGenOpen(true)}>
                    <Wand2 className="size-4" aria-hidden />
                    {t("actions.generate")}
                  </Button>
                ) : undefined
              }
            />
          )}
          {phase === "ready" && filtered.length > 0 && (
            <>
              <p className="mt-4 text-sm text-muted-foreground">
                {t("board.range", {
                  from: (safeBoardPage - 1) * BOARD_PAGE + 1,
                  to: (safeBoardPage - 1) * BOARD_PAGE + pagedRows.length,
                  total: sorted.length,
                })}
                {filtered.length < activeKindRows.length
                  ? t("board.rangeFiltered", { total: activeKindRows.length })
                  : ""}
              </p>
              <div className="mt-3 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {pagedRows.map((h) => (
                  <HypothesisCard
                    key={h.id}
                    h={h}
                    owner={ownerName(h.owner_id)}
                    kpiTitle={h.kpi_id ? kpiById.get(h.kpi_id) : undefined}
                    clusterTitle={
                      h.primary_cluster_id ? clusterById.get(h.primary_cluster_id) : undefined
                    }
                    onOpen={() =>
                      navigate(
                        isResearchDirection(h) ? `/directions/${h.id}` : `/hypotheses/${h.id}`,
                      )
                    }
                  />
                ))}
              </div>
              <Pagination
                className="mt-5"
                page={safeBoardPage}
                pageCount={boardPageCount}
                onPage={setBoardPage}
              />
            </>
          )}
        </>
      )}

      <DigestDialog
        open={digestOpen}
        onOpenChange={setDigestOpen}
        digest={digest}
        count={filtered.length}
      />
      <GoalPromptDialog
        open={genOpen}
        onOpenChange={setGenOpen}
        kpis={kpis}
        onCreated={() => {
          void listKPIs().then((next) => {
            if (mounted.current) setKpis(next);
          });
        }}
        onGenerated={onGenerated}
      />
    </div>
  );
}

// Логотип Jira (Atlassian) в фирменном синем — lucide брендовых значков не даёт.
function JiraIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="#0052CC" aria-hidden>
      <path d="M11.571 11.513H0a5.218 5.218 0 0 0 5.232 5.215h2.13v2.057A5.215 5.215 0 0 0 12.575 24V12.518a1.005 1.005 0 0 0-1.005-1.005zm5.723-5.756H5.736a5.215 5.215 0 0 0 5.215 5.214h2.129v2.058a5.218 5.218 0 0 0 5.215 5.214V6.758a1.001 1.001 0 0 0-1.001-1.001zM23.013 0H11.455a5.215 5.215 0 0 0 5.215 5.215h2.129v2.057A5.215 5.215 0 0 0 24 12.483V1.005A1.001 1.001 0 0 0 23.013 0z" />
    </svg>
  );
}
