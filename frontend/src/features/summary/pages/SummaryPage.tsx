import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Layers, RotateCcw } from "lucide-react";

import { listClusters, type ApiCluster } from "@/features/cluster";
import { cleanInternalTitle, hasInternalNoise } from "@/features/hypothesis";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { Chip } from "@/shared/ui/Chip";
import { Pagination } from "@/shared/ui/Pagination";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/ui/Card";
import { Section } from "@/shared/ui/Section";
import { SearchField } from "@/shared/ui/SearchField";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/Select";
import { EmptyState } from "@/shared/ui/EmptyState";
import { Skeleton } from "@/shared/ui/Skeleton";
import { ErrorState } from "@/shared/ui/ErrorState";
import { PageHeader } from "@/shared/ui/PageHeader";
import { InfoHint } from "@/shared/ui/InfoHint";
import { GLOSSARY } from "@/shared/glossary";

// Colour-blind-safe palette (Okabe–Ito) so themes stay distinguishable.
const PALETTE = [
  "#0072B2",
  "#E69F00",
  "#009E73",
  "#CC79A7",
  "#56B4E9",
  "#D55E00",
  "#117733",
  "#882255",
  "#44AA99",
  "#999933",
];
const NO_THEME = "#9ca3af";

/** A 0..1 fraction as a percentage, no decimals. */
function asPct(v: number): string {
  return `${Math.round(Math.max(0, Math.min(1, v)) * 100)}%`;
}

function clamp01(v: number): number {
  return Math.max(0, Math.min(1, v));
}

function shortLabel(label: string): string {
  const clean = cleanInternalTitle(label);
  return clean.length > 34 ? `${clean.slice(0, 31)}...` : clean;
}

function cleanKeywords(keywords: string[] | undefined): string[] {
  return [...new Set((keywords ?? []).map(cleanInternalTitle).filter(Boolean))].slice(0, 6);
}

function cleanSummary(summary: string | undefined): string {
  const text = summary?.trim() ?? "";
  return text !== "" && !hasInternalNoise(text) ? text : "";
}

/** Ranks themes for the list: by technology index when scored, else by size. */
function themeScore(c: ApiCluster): number {
  return c.params?.itc?.techscore ?? c.document_count * 1000 + c.chunk_count;
}

interface ClusterMapPoint {
  id: string;
  label: string;
  shortLabel: string;
  mapLabel: string;
  /** Execution / maturity axis, 0..1. */
  x: number;
  /** Vision / opportunity axis, 0..1. */
  y: number;
  nx: number;
  ny: number;
  /** Bubble size, 0..1. */
  z: number;
  score: number | null;
  docs: number;
  chunks: number;
  keywords: string[];
  color: string;
}

function buildMap(clusters: ApiCluster[]) {
  const maxDocs = Math.max(1, ...clusters.map((c) => c.document_count));
  const maxChunks = Math.max(1, ...clusters.map((c) => c.chunk_count));
  const visible = clusters
    .filter((c) => c.document_count >= 2 && cleanInternalTitle(c.label) !== "")
    .toSorted((a, b) => b.document_count - a.document_count || b.chunk_count - a.chunk_count);
  // На карту наносятся только темы с реально посчитанным индексом
  // технологичности; остальные ждут расчёта — честность важнее заполненности.
  const scored = visible.filter((c) => c.params?.itc);
  const unscored = visible
    .filter((c) => !c.params?.itc)
    .map((c) => ({ id: c.id, label: cleanInternalTitle(c.label) }));
  const clusterMap: ClusterMapPoint[] = scored.map((c, i) => {
    const itc = c.params?.itc;
    const label = cleanInternalTitle(c.label);
    const docNorm = c.document_count / maxDocs;
    const chunkNorm = c.chunk_count / maxChunks;
    const size = clamp01(0.12 + 0.72 * (0.65 * docNorm + 0.35 * chunkNorm));
    const x = clamp01(0.65 * (itc?.axes.momentum ?? 0) + 0.35 * (itc?.axes.diffusion ?? 0));
    const y = clamp01(0.65 * (itc?.axes.novelty ?? 0) + 0.35 * (itc?.axes.impact ?? 0));

    return {
      id: c.id,
      label,
      shortLabel: shortLabel(label),
      mapLabel: i < 12 ? shortLabel(label) : "",
      x,
      y,
      nx: x,
      ny: y,
      z: size,
      score: itc?.score ?? null,
      docs: c.document_count,
      chunks: c.chunk_count,
      keywords: cleanKeywords(c.keywords),
      color: PALETTE[i % PALETTE.length] ?? NO_THEME,
    };
  });
  spreadAxis(clusterMap, "x", "nx");
  spreadAxis(clusterMap, "y", "ny");

  return { clusterMap, unscored };
}

const SPREAD_PAD = 0.06;

function spreadAxis(points: ClusterMapPoint[], from: "x" | "y", to: "nx" | "ny"): void {
  if (points.length === 0) return;
  const values = points.map((p) => p[from]);
  const min = Math.min(...values);
  const range = Math.max(...values) - min;
  for (const p of points) {
    p[to] = range < 1e-6 ? 0.5 : SPREAD_PAD + ((p[from] - min) / range) * (1 - 2 * SPREAD_PAD);
  }
}

type Phase = "loading" | "ready" | "error";

// Подписи сортировок — в словарях (неймспейс summary), здесь только ключи.
const THEME_SORTS = {
  tech: {
    labelKey: "sort.tech",
    cmp: (a: ApiCluster, b: ApiCluster) => themeScore(b) - themeScore(a),
  },
  size: {
    labelKey: "sort.size",
    cmp: (a: ApiCluster, b: ApiCluster) =>
      b.document_count - a.document_count || b.chunk_count - a.chunk_count,
  },
  fresh: {
    labelKey: "sort.fresh",
    cmp: (a: ApiCluster, b: ApiCluster) =>
      (Date.parse(b.updated_at) || 0) - (Date.parse(a.updated_at) || 0),
  },
} as const;
type ThemeSort = keyof typeof THEME_SORTS;

const THEMES_PAGE = 12;

/**
 * «Темы» (Themes) — document clusters from the knowledge base with a
 * novelty/maturity map. Each map point opens the theme behind it.
 */
export function SummaryPage() {
  const navigate = useNavigate();
  const { t } = useTranslation("summary");
  const [clusters, setClusters] = useState<ApiCluster[]>([]);
  const [phase, setPhase] = useState<Phase>("loading");

  const [themeQuery, setThemeQuery] = useState("");
  const [themeSort, setThemeSort] = useState<ThemeSort>("tech");
  const [scoredOnly, setScoredOnly] = useState(false);
  const [themePage, setThemePage] = useState(1);

  const load = useCallback(async () => {
    setPhase("loading");
    try {
      setClusters(await listClusters());
      setPhase("ready");
    } catch {
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Смена поиска/сортировки/фильтра возвращает список к первой странице.
  useEffect(() => {
    setThemePage(1);
  }, [themeQuery, themeSort, scoredOnly]);

  const { clusterMap, unscored } = useMemo(() => buildMap(clusters), [clusters]);
  const themed = useMemo(
    () => clusters.filter((c) => cleanInternalTitle(c.label) !== ""),
    [clusters],
  );

  const filteredThemes = useMemo(() => {
    const q = themeQuery.trim().toLowerCase();
    const rows = themed.filter((c) => {
      if (scoredOnly && !c.params?.itc) return false;
      if (q === "") return true;
      const hay = [cleanInternalTitle(c.label), ...(c.keywords ?? []), c.summary ?? ""]
        .join(" ")
        .toLowerCase();
      return hay.includes(q);
    });
    return rows.toSorted(THEME_SORTS[themeSort].cmp);
  }, [themed, themeQuery, themeSort, scoredOnly]);

  const count = themed.length;
  const filtersActive = themeQuery.trim() !== "" || scoredOnly;
  const themePageCount = Math.max(1, Math.ceil(filteredThemes.length / THEMES_PAGE));
  const safeThemePage = Math.min(themePage, themePageCount);
  const shownThemes = filteredThemes.slice(
    (safeThemePage - 1) * THEMES_PAGE,
    safeThemePage * THEMES_PAGE,
  );

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <PageHeader
        kicker={t("header.kicker")}
        title={t("header.title")}
        badge={<InfoHint label={t("header.hintLabel")}>{GLOSSARY.theme}</InfoHint>}
        description={count > 0 ? t("header.withCount", { count }) : t("header.empty")}
      />

      {phase === "loading" && (
        <output className="mt-6 block space-y-4" aria-label={t("loading")}>
          <Skeleton className="h-64 w-full rounded-xl" />
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-32 w-full rounded-xl" />
            ))}
          </div>
        </output>
      )}

      {phase === "error" && <ErrorState message={t("error")} onRetry={() => void load()} />}

      {phase === "ready" && (
        <>
          {clusterMap.length > 0 ? (
            <Card className="mt-6">
              <CardHeader>
                <CardTitle className="text-base">{t("map.title")}</CardTitle>
                <CardDescription>{t("map.description")}</CardDescription>
              </CardHeader>
              <CardContent>
                <ThemeMap points={clusterMap} onOpen={(id) => navigate(`/clusters/${id}`)} />
              </CardContent>
            </Card>
          ) : (
            count > 0 && (
              <div className="mt-6 rounded-xl border bg-secondary/50 px-4 py-3 text-sm text-muted-foreground">
                {t("map.pending")}
              </div>
            )
          )}

          {clusterMap.length > 0 && unscored.length > 0 && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <span className="text-xs text-muted-foreground">
                {t("map.awaiting", { count: unscored.length })}
              </span>
              {unscored.slice(0, 10).map((c) => (
                <Chip key={c.id} onClick={() => navigate(`/clusters/${c.id}`)}>
                  {c.label}
                </Chip>
              ))}
            </div>
          )}

          <Section title={t("section.all")} className="mt-10">
            {count === 0 ? (
              <EmptyState
                icon={Layers}
                title={t("empty.title")}
                description={t("empty.description")}
              />
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <SearchField
                    className="min-w-56 flex-1"
                    value={themeQuery}
                    onChange={setThemeQuery}
                    placeholder={t("filters.searchPlaceholder")}
                    ariaLabel={t("filters.searchAria")}
                  />
                  <Select value={themeSort} onValueChange={(v) => setThemeSort(v as ThemeSort)}>
                    <SelectTrigger
                      size="sm"
                      className="w-auto min-w-48"
                      aria-label={t("filters.sortAria")}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {Object.entries(THEME_SORTS).map(([key, s]) => (
                        <SelectItem key={key} value={key}>
                          {t(s.labelKey)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Button
                    variant={scoredOnly ? "secondary" : "outline"}
                    size="sm"
                    aria-pressed={scoredOnly}
                    onClick={() => setScoredOnly((v) => !v)}
                  >
                    {t("filters.scoredOnly")}
                  </Button>
                  {filtersActive && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setThemeQuery("");
                        setScoredOnly(false);
                      }}
                    >
                      <RotateCcw className="size-4" aria-hidden />
                      {t("filters.reset")}
                    </Button>
                  )}
                </div>

                {filteredThemes.length === 0 ? (
                  <div className="mt-4">
                    <EmptyState
                      icon={Layers}
                      title={t("noResults.title")}
                      description={t("noResults.description")}
                      action={
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => {
                            setThemeQuery("");
                            setScoredOnly(false);
                          }}
                        >
                          {t("noResults.resetFilters")}
                        </Button>
                      }
                    />
                  </div>
                ) : (
                  <>
                    <p className="mt-4 text-sm text-muted-foreground">
                      {t("list.range", {
                        from: (safeThemePage - 1) * THEMES_PAGE + 1,
                        to: (safeThemePage - 1) * THEMES_PAGE + shownThemes.length,
                        total: filteredThemes.length,
                      })}
                      {filtersActive ? ` ${t("list.totalNote", { count })}` : ""}
                    </p>
                    <div className="mt-3 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
                      {shownThemes.map((c) => {
                        const label = cleanInternalTitle(c.label);
                        const keywords = cleanKeywords(c.keywords);
                        const summary = cleanSummary(c.summary);
                        return (
                          <Link
                            key={c.id}
                            to={`/clusters/${c.id}`}
                            className="block h-full rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <Card interactive className="flex h-full flex-col p-4">
                              <p className="font-medium leading-snug">{label}</p>
                              <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                                <span className="font-mono">
                                  {t("stats", { docs: c.document_count, chunks: c.chunk_count })}
                                </span>
                                {c.params?.itc && (
                                  <Badge variant="brand">
                                    {t("list.techBadge", { score: c.params.itc.score })}
                                  </Badge>
                                )}
                              </div>
                              {summary && (
                                <p className="mt-2 line-clamp-3 text-sm text-muted-foreground">
                                  {summary}
                                </p>
                              )}
                              {keywords.length > 0 && (
                                <div className="mt-2 flex flex-wrap gap-1.5">
                                  {keywords.map((k) => (
                                    <Badge key={k} variant="outline">
                                      {k}
                                    </Badge>
                                  ))}
                                </div>
                              )}
                            </Card>
                          </Link>
                        );
                      })}
                    </div>
                    <Pagination
                      className="mt-5"
                      page={safeThemePage}
                      pageCount={themePageCount}
                      onPage={setThemePage}
                    />
                  </>
                )}
              </>
            )}
          </Section>
        </>
      )}
    </div>
  );
}

// ── Карта тем: лёгкий SVG вместо recharts ────────────────────────────────────
// Позиции точек смысловые (оси индекса технологичности), но перед отрисовкой
// пузыри «разводятся» итеративной релаксацией, чтобы не налезать друг на друга.

const MAP_MARGIN = { top: 26, right: 20, bottom: 44, left: 50 };
const TICKS = [0, 0.25, 0.5, 0.75, 1];

interface LaidPoint extends ClusterMapPoint {
  px: number;
  py: number;
  r: number;
}

function layoutMap(points: ClusterMapPoint[], width: number, height: number): LaidPoint[] {
  const innerW = Math.max(1, width - MAP_MARGIN.left - MAP_MARGIN.right);
  const innerH = Math.max(1, height - MAP_MARGIN.top - MAP_MARGIN.bottom);
  const laid: LaidPoint[] = points.map((p) => ({
    ...p,
    r: 9 + 17 * p.z,
    px: MAP_MARGIN.left + p.nx * innerW,
    py: MAP_MARGIN.top + (1 - p.ny) * innerH,
  }));
  const anchors = laid.map((p) => ({ x: p.px, y: p.py }));

  for (let iter = 0; iter < 160; iter++) {
    // Пружина к «честной» позиции, чтобы карта оставалась смысловой.
    for (let i = 0; i < laid.length; i++) {
      const p = laid[i];
      const a = anchors[i];
      if (!p || !a) continue;
      p.px += (a.x - p.px) * 0.05;
      p.py += (a.y - p.py) * 0.05;
    }
    // Попарное расталкивание пересекающихся кругов.
    for (let i = 0; i < laid.length; i++) {
      const a = laid[i];
      if (!a) continue;
      for (let j = i + 1; j < laid.length; j++) {
        const b = laid[j];
        if (!b) continue;
        let dx = b.px - a.px;
        let dy = b.py - a.py;
        let d = Math.hypot(dx, dy);
        const min = a.r + b.r + 3;
        if (d >= min) continue;
        if (d < 1e-3) {
          // Совпавшие точки разводим детерминированно, без Math.random.
          const angle = (((i * 37 + j * 101) % 360) * Math.PI) / 180;
          dx = Math.cos(angle);
          dy = Math.sin(angle);
          d = 1;
        }
        const push = (min - d) / 2 / d;
        a.px -= dx * push;
        a.py -= dy * push;
        b.px += dx * push;
        b.py += dy * push;
      }
    }
    for (const p of laid) {
      p.px = Math.min(MAP_MARGIN.left + innerW - p.r, Math.max(MAP_MARGIN.left + p.r, p.px));
      p.py = Math.min(MAP_MARGIN.top + innerH - p.r, Math.max(MAP_MARGIN.top + p.r, p.py));
    }
  }
  return laid;
}

function ThemeMap({ points, onOpen }: { points: ClusterMapPoint[]; onOpen: (id: string) => void }) {
  const { t } = useTranslation("summary");
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [hoverId, setHoverId] = useState<string | null>(null);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width ?? 0;
      setWidth(Math.round(w));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const height = width > 0 && width < 620 ? 440 : 560;
  const laid = useMemo(
    () => (width > 0 ? layoutMap(points, width, height) : []),
    [points, width, height],
  );
  // Большие круги рисуем первыми, чтобы маленькие оставались кликабельными.
  const drawOrder = useMemo(() => laid.toSorted((a, b) => b.r - a.r), [laid]);
  const hover = hoverId === null ? null : (laid.find((p) => p.id === hoverId) ?? null);

  const innerW = Math.max(1, width - MAP_MARGIN.left - MAP_MARGIN.right);
  const innerH = height - MAP_MARGIN.top - MAP_MARGIN.bottom;
  const midX = MAP_MARGIN.left + innerW / 2;
  const midY = MAP_MARGIN.top + innerH / 2;

  return (
    <div ref={wrapRef} className="relative w-full" style={{ height }}>
      {width > 0 && (
        // role="group", не "img": внутри интерактивные пузыри-ссылки (axe nested-interactive).
        // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role -- у SVG тега-аналога нет
        <svg width={width} height={height} role="group" aria-label={t("map.aria")}>
          {/* Квадранты */}
          <rect
            x={MAP_MARGIN.left}
            y={MAP_MARGIN.top}
            width={innerW / 2}
            height={innerH / 2}
            fill="var(--brand-wash)"
            fillOpacity={0.55}
          />
          <rect
            x={midX}
            y={MAP_MARGIN.top}
            width={innerW / 2}
            height={innerH / 2}
            fill="var(--ok-wash)"
            fillOpacity={0.55}
          />
          <rect
            x={MAP_MARGIN.left}
            y={midY}
            width={innerW / 2}
            height={innerH / 2}
            fill="var(--muted)"
            fillOpacity={0.6}
          />
          <rect
            x={midX}
            y={midY}
            width={innerW / 2}
            height={innerH / 2}
            fill="var(--warn-wash)"
            fillOpacity={0.5}
          />
          {/* Сетка и оси */}
          {TICKS.filter((tick) => tick !== 0 && tick !== 1).map((tick) => (
            <g key={tick}>
              <line
                x1={MAP_MARGIN.left + tick * innerW}
                y1={MAP_MARGIN.top}
                x2={MAP_MARGIN.left + tick * innerW}
                y2={MAP_MARGIN.top + innerH}
                stroke="var(--border)"
                strokeDasharray={tick === 0.5 ? undefined : "3 3"}
              />
              <line
                x1={MAP_MARGIN.left}
                y1={MAP_MARGIN.top + (1 - tick) * innerH}
                x2={MAP_MARGIN.left + innerW}
                y2={MAP_MARGIN.top + (1 - tick) * innerH}
                stroke="var(--border)"
                strokeDasharray={tick === 0.5 ? undefined : "3 3"}
              />
            </g>
          ))}
          <rect
            x={MAP_MARGIN.left}
            y={MAP_MARGIN.top}
            width={innerW}
            height={innerH}
            fill="none"
            stroke="var(--border)"
          />
          <text
            x={MAP_MARGIN.left + innerW / 2}
            y={height - 8}
            textAnchor="middle"
            fontSize={11}
            fill="var(--muted-foreground)"
          >
            {t("map.axisX")}
          </text>
          <text
            transform={`rotate(-90 12 ${MAP_MARGIN.top + innerH / 2})`}
            x={12}
            y={MAP_MARGIN.top + innerH / 2}
            textAnchor="middle"
            fontSize={11}
            fill="var(--muted-foreground)"
          >
            {t("map.axisY")}
          </text>
          {/* Подписи квадрантов */}
          <g fontSize={11}>
            <text x={MAP_MARGIN.left + 8} y={MAP_MARGIN.top + 16} fill="var(--brand)">
              {t("map.quadrantNew")}
            </text>
            <text
              x={MAP_MARGIN.left + innerW - 8}
              y={MAP_MARGIN.top + 16}
              textAnchor="end"
              fill="var(--ok)"
            >
              {t("map.quadrantLeaders")}
            </text>
            <text
              x={MAP_MARGIN.left + 8}
              y={MAP_MARGIN.top + innerH - 8}
              fill="var(--muted-foreground)"
            >
              {t("map.quadrantFading")}
            </text>
            <text
              x={MAP_MARGIN.left + innerW - 8}
              y={MAP_MARGIN.top + innerH - 8}
              textAnchor="end"
              fill="var(--warn)"
            >
              {t("map.quadrantMature")}
            </text>
          </g>
          {/* Пузыри */}
          {drawOrder.map((p) => {
            const active = hoverId === p.id;
            return (
              <g
                key={p.id}
                // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role -- SVG: семантичного тега нет
                role="link"
                tabIndex={0}
                aria-label={t("map.openTheme", { label: p.label })}
                className="cursor-pointer outline-none"
                onMouseEnter={() => setHoverId(p.id)}
                onMouseLeave={() => setHoverId(null)}
                onFocus={() => setHoverId(p.id)}
                onBlur={() => setHoverId(null)}
                onClick={() => onOpen(p.id)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    onOpen(p.id);
                  }
                }}
              >
                <circle
                  cx={p.px}
                  cy={p.py}
                  r={p.r + 4}
                  fill={p.color}
                  opacity={active ? 0.3 : 0.16}
                />
                <circle
                  cx={p.px}
                  cy={p.py}
                  r={p.r}
                  fill={p.color}
                  stroke={active ? "var(--foreground)" : "var(--card)"}
                  strokeWidth={active ? 2 : 2.5}
                />
                <circle
                  cx={p.px - p.r * 0.32}
                  cy={p.py - p.r * 0.32}
                  r={p.r * 0.23}
                  fill="#fff"
                  opacity={0.42}
                />
              </g>
            );
          })}
          {/* Подписи крупных тем — поверх пузырей, клики не перехватывают */}
          {laid
            .filter((p) => p.mapLabel !== "")
            .map((p) => {
              const above = p.py - p.r - 7 > MAP_MARGIN.top + 12;
              return (
                <text
                  key={p.id}
                  x={p.px}
                  y={above ? p.py - p.r - 7 : p.py + p.r + 14}
                  textAnchor="middle"
                  fontSize={10}
                  fill="var(--foreground)"
                  stroke="var(--background)"
                  strokeWidth={3}
                  paintOrder="stroke"
                  className="pointer-events-none"
                >
                  {p.mapLabel}
                </text>
              );
            })}
        </svg>
      )}

      {hover && <MapHoverCard point={hover} containerWidth={width} containerHeight={height} />}
    </div>
  );
}

/** Ховер-карточка темы: появляется у самого круга (без «переезда» из угла),
 *  метрики — мини-барами в общем стиле метрик системы. */
function MapHoverCard({
  point,
  containerWidth,
  containerHeight,
}: {
  point: LaidPoint;
  containerWidth: number;
  containerHeight: number;
}) {
  const { t } = useTranslation("summary");
  const cardW = 264;
  const cardH = 190;
  let left = point.px + point.r + 12;
  if (left + cardW > containerWidth - 8) left = point.px - point.r - 12 - cardW;
  if (left < 8) left = 8;
  const top = Math.max(8, Math.min(containerHeight - cardH - 8, point.py - cardH / 2));

  return (
    <div
      className="pointer-events-none absolute z-10 rounded-lg border bg-popover p-3 shadow-md"
      style={{ left, top, width: cardW }}
    >
      <p className="text-sm font-medium leading-snug text-foreground">{point.label}</p>
      <p className="mt-1 font-mono text-xs text-muted-foreground">
        {t("stats", { docs: point.docs, chunks: point.chunks })}
        {point.score !== null ? ` · ${t("hover.techScore", { score: point.score })}` : ""}
      </p>
      <MiniBar label={t("hover.maturity")} value={point.x} />
      <MiniBar label={t("hover.potential")} value={point.y} />
      {point.keywords.length > 0 && (
        <p className="mt-2 line-clamp-2 text-xs text-muted-foreground">
          {point.keywords.slice(0, 5).join(" · ")}
        </p>
      )}
      <p className="mt-2 text-[11px] text-muted-foreground">{t("hover.open")}</p>
    </div>
  );
}

function MiniBar({ label, value }: { label: string; value: number }) {
  return (
    <div className="mt-2">
      <div className="flex items-baseline justify-between text-[11px] text-muted-foreground">
        <span>{label}</span>
        <span className="font-mono tabular-nums">{asPct(value)}</span>
      </div>
      <div className="mt-0.5 h-1.5 overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-brand" style={{ width: asPct(value) }} />
      </div>
    </div>
  );
}
