import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Files,
  CheckCircle2,
  Clock3,
  AlertTriangle,
  Layers,
  HardDrive,
  Layers3,
  FlaskConical,
  Target,
  ArrowRight,
} from "lucide-react";
import { Card, CardContent } from "@/shared/ui/Card";
import { Skeleton } from "@/shared/ui/Skeleton";
import { EmptyState, ErrorState, Kicker, MetricTile, PageHeader, Section } from "@/shared/ui";
import { adminDocumentsStats, type AdminDocumentsStats } from "@/features/admin/api";
import { type DocStatus, documentTitle } from "@/features/document";
import { listClusters } from "@/features/cluster";
import { listKPIs } from "@/features/kpi";
import { getHypothesisBoard } from "@/features/hypothesis";
import { formatDateShort, humanSize } from "@/shared/lib/format";
import { currentLocale } from "@/shared/i18n";
import { cn } from "@/shared/lib/cn";
import type { FileType } from "@/shared/types";

/** Document accepted, but hasn't reached the index yet (and hasn't failed). */
const PROCESSING = new Set<DocStatus>([
  "uploaded",
  "queued",
  "parsing",
  "ocr",
  "parsed",
  "chunking",
]);

const DAYS_WINDOW = 14;

/** Integers with locale-aware grouping: 12345 → «12 345» (numbers, not dates). */
function fmtInt(n: number): string {
  return new Intl.NumberFormat(currentLocale()).format(n);
}

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s;
}

/** Готовые ряды для плиток и графиков из серверных агрегатов — дашборд
 *  больше не тянет и не пересчитывает полный список документов. */
function buildStats(s: AdminDocumentsStats) {
  const indexed = s.by_status["indexed"] ?? 0;
  const failed = s.by_status["failed"] ?? 0;
  let processing = 0;
  for (const st of PROCESSING) processing += s.by_status[st] ?? 0;

  // Типы остаются ключами — подпись подставляет словарь в рендере.
  const byType = (Object.entries(s.by_file_type) as [FileType, number][])
    .map(([type, value]) => ({ type, value }))
    .toSorted((a, b) => b.value - a.value);

  // День приходит строкой "YYYY-MM-DD" (UTC); номер дня берём из неё же,
  // чтобы локальный часовой пояс не сдвигал дату.
  const byDay = s.uploads_by_day.map((d) => ({
    day: formatDateShort(`${d.day}T00:00:00`),
    dayNum: String(Number(d.day.slice(8))),
    count: d.count,
  }));

  const topDocs = s.top_by_chunks.map((d, i) => ({
    label: `${i + 1}. ${truncate(documentTitle(d), 40)}`,
    value: d.chunk_count,
  }));

  return {
    total: s.total,
    indexed,
    indexedPct: s.indexed_pct,
    processing,
    failed,
    chunks: s.total_chunks,
    bytes: s.total_size_bytes,
    byType,
    byDay,
    topDocs,
  };
}

type Status = "loading" | "done" | "error";

interface FactoryProducts {
  themes: number;
  hypotheses: number;
  goals: number;
}

export function AdminDashboard() {
  const { t } = useTranslation("admin");
  const [data, setData] = useState<AdminDocumentsStats | null>(null);
  const [products, setProducts] = useState<FactoryProducts | null>(null);
  const [status, setStatus] = useState<Status>("loading");

  const load = useCallback(async () => {
    setStatus("loading");
    try {
      setData(await adminDocumentsStats());
      setStatus("done");
    } catch {
      setStatus("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Продукты фабрики — вспомогательный ряд: при ошибке просто не показываем.
  useEffect(() => {
    let cancelled = false;
    Promise.all([listClusters(), getHypothesisBoard({ limit: 1 }), listKPIs()])
      .then(([clusters, board, kpis]) => {
        if (cancelled) return;
        setProducts({ themes: clusters.length, hypotheses: board.total, goals: kpis.length });
      })
      .catch(() => {
        if (!cancelled) setProducts(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const stats = useMemo(() => (data ? buildStats(data) : null), [data]);

  const metrics = stats
    ? [
        {
          label: t("dashboard.metrics.total"),
          value: fmtInt(stats.total),
          sub: t("dashboard.metrics.totalSub"),
          icon: Files,
        },
        {
          label: t("dashboard.metrics.indexed"),
          value: fmtInt(stats.indexed),
          sub: t("dashboard.metrics.indexedSub", { pct: stats.indexedPct }),
          icon: CheckCircle2,
        },
        {
          label: t("dashboard.metrics.processing"),
          value: fmtInt(stats.processing),
          sub: t("dashboard.metrics.processingSub"),
          icon: Clock3,
        },
        {
          label: t("dashboard.metrics.failed"),
          value: fmtInt(stats.failed),
          sub: t("dashboard.metrics.failedSub"),
          icon: AlertTriangle,
        },
        {
          label: t("dashboard.metrics.chunks"),
          value: fmtInt(stats.chunks),
          sub: t("dashboard.metrics.chunksSub"),
          icon: Layers,
        },
        {
          label: t("dashboard.metrics.size"),
          value: humanSize(stats.bytes),
          sub: t("dashboard.metrics.sizeSub"),
          icon: HardDrive,
        },
      ]
    : [];

  const byTypeRows = (stats?.byType ?? []).map((r) => ({
    label: t(`dashboard.fileTypes.${r.type}`),
    value: r.value,
  }));

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <PageHeader title={t("dashboard.title")} description={t("dashboard.description")} />

      {status === "loading" && (
        <output className="block" aria-label={t("dashboard.loadingAria")}>
          <div className="mt-5 grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
            {[0, 1, 2, 3, 4, 5].map((i) => (
              <Card key={i} size="sm">
                <CardContent className="pt-4">
                  <Skeleton className="h-4 w-24" />
                  <Skeleton className="mt-2 h-7 w-16" />
                </CardContent>
              </Card>
            ))}
          </div>
          <div className="mt-5 grid grid-cols-1 gap-4 lg:grid-cols-2">
            {[0, 1].map((i) => (
              <Skeleton key={i} className="h-56 w-full rounded-xl" />
            ))}
          </div>
        </output>
      )}

      {status === "error" && (
        <ErrorState message={t("dashboard.loadError")} onRetry={() => void load()} />
      )}

      {status === "done" && stats && (
        <>
          <div className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
            {metrics.map((m) => (
              <MetricTile key={m.label} label={m.label} value={m.value} sub={m.sub} icon={m.icon} />
            ))}
          </div>

          {products && (
            <Section title={t("dashboard.factory.title")} className="mt-8">
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                {(
                  [
                    {
                      to: "/summary",
                      icon: Layers3,
                      value: products.themes,
                      labelKey: "dashboard.factory.themes",
                      subKey: "dashboard.factory.themesSub",
                    },
                    {
                      to: "/hypotheses",
                      icon: FlaskConical,
                      value: products.hypotheses,
                      labelKey: "dashboard.factory.hypotheses",
                      subKey: "dashboard.factory.hypothesesSub",
                    },
                    {
                      to: "/kpi",
                      icon: Target,
                      value: products.goals,
                      labelKey: "dashboard.factory.goals",
                      subKey: "dashboard.factory.goalsSub",
                    },
                  ] as const
                ).map((p) => (
                  <Link
                    key={p.to}
                    to={p.to}
                    className="group rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <MetricTile
                      className="h-full transition-colors group-hover:border-brand-border"
                      icon={p.icon}
                      label={t(p.labelKey)}
                      value={fmtInt(p.value)}
                      sub={
                        <span className="flex items-center gap-1">
                          {t(p.subKey)}
                          <ArrowRight
                            className="size-3 shrink-0 opacity-0 transition-opacity group-hover:opacity-100"
                            aria-hidden
                          />
                        </span>
                      }
                    />
                  </Link>
                ))}
              </div>
            </Section>
          )}

          {stats.total === 0 ? (
            <div className="mt-10">
              <EmptyState
                icon={Files}
                title={t("dashboard.empty.title")}
                description={t("dashboard.empty.description")}
              />
            </div>
          ) : (
            <Section title={t("dashboard.dynamics.title")} className="mt-8">
              <div className="grid grid-cols-1 gap-4 lg:grid-cols-5">
                <div className="rounded-xl border bg-card p-4 lg:col-span-3">
                  <Kicker className="mb-2 block">
                    {t("dashboard.dynamics.uploadsByDay", { days: DAYS_WINDOW })}
                  </Kicker>
                  <UploadBars data={stats.byDay} />
                </div>

                <div className="rounded-xl border bg-card p-4 lg:col-span-2">
                  <Kicker className="mb-2 block">{t("dashboard.dynamics.pipeline")}</Kicker>
                  <div
                    className="flex h-2.5 w-full overflow-hidden rounded-full bg-muted"
                    // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role -- составная полоса статусов из div-ов, семантичного тега нет
                    role="img"
                    aria-label={t("dashboard.dynamics.pipelineAria", {
                      indexed: stats.indexed,
                      processing: stats.processing,
                      failed: stats.failed,
                    })}
                  >
                    {stats.indexed > 0 && (
                      <div
                        className="bg-ok"
                        style={{ width: `${(stats.indexed / stats.total) * 100}%` }}
                      />
                    )}
                    {stats.processing > 0 && (
                      <div
                        className="animate-pulse bg-brand"
                        style={{ width: `${(stats.processing / stats.total) * 100}%` }}
                      />
                    )}
                    {stats.failed > 0 && (
                      <div
                        className="bg-risk"
                        style={{ width: `${(stats.failed / stats.total) * 100}%` }}
                      />
                    )}
                  </div>
                  <ul className="mt-4 space-y-2.5 text-sm">
                    <li className="flex items-center gap-2">
                      <span className="size-2 rounded-full bg-ok" aria-hidden />
                      <span className="font-mono font-semibold">{fmtInt(stats.indexed)}</span>
                      {t("dashboard.dynamics.indexedReady", { count: stats.indexed })}
                    </li>
                    <li className="flex items-center gap-2">
                      <span className="size-2 rounded-full bg-brand" aria-hidden />
                      <span className="font-mono font-semibold">{fmtInt(stats.processing)}</span>
                      {t("dashboard.dynamics.processingNow")}
                    </li>
                    <li className="flex items-center gap-2">
                      <span className="size-2 rounded-full bg-risk" aria-hidden />
                      <span className="font-mono font-semibold">{fmtInt(stats.failed)}</span>
                      {t("dashboard.dynamics.failedRetry")}
                    </li>
                  </ul>
                </div>
              </div>

              <div className="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-5">
                <div className="lg:col-span-2">
                  <Kicker className="mb-2 block">{t("dashboard.dynamics.byType")}</Kicker>
                  <BarList rows={byTypeRows} unit={t("units.docs")} />
                </div>
                <div className="lg:col-span-3">
                  <Kicker className="mb-2 block">{t("dashboard.dynamics.topDocs")}</Kicker>
                  <BarList rows={stats.topDocs} unit={t("units.chunks")} />
                </div>
              </div>
            </Section>
          )}
        </>
      )}
    </div>
  );
}

/** Дневные столбики загрузок — тот же лёгкий идиом, что на «Метриках»
 *  (DayBars): без recharts, только div-ы с моно-подписями. */
function UploadBars({ data }: { data: { day: string; dayNum: string; count: number }[] }) {
  const max = Math.max(1, ...data.map((d) => d.count));
  return (
    <div>
      <span className="mb-1 block font-mono text-[10px] text-muted-foreground">{fmtInt(max)}</span>
      <div className="flex h-40 items-end gap-1.5 border-b">
        {data.map((d) => (
          <div
            key={d.day}
            className="flex-1 rounded-t bg-brand/70 transition-colors hover:bg-brand"
            style={{ height: `${Math.max(1.5, (d.count / max) * 100)}%` }}
            title={`${d.day}: ${fmtInt(d.count)}`}
          />
        ))}
      </div>
      <div className="mt-1.5 flex gap-1.5">
        {data.map((d) => (
          <span
            key={d.day}
            className="min-w-0 flex-1 truncate text-center font-mono text-[10px] text-muted-foreground"
          >
            {d.dayNum}
          </span>
        ))}
      </div>
    </div>
  );
}

/** Пропорциональные строки-бары (по типам, топ документов) — идиом Breakdown
 *  со страницы «Метрики». */
function BarList({ rows, unit }: { rows: { label: string; value: number }[]; unit: string }) {
  const { t } = useTranslation("admin");
  const max = Math.max(1, ...rows.map((r) => r.value));
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      {rows.length === 0 && (
        <p className="px-4 py-3 text-sm text-muted-foreground">{t("noData")}</p>
      )}
      {rows.map((r, i) => (
        <div key={r.label} className={cn("flex items-center gap-3 px-4 py-2", i > 0 && "border-t")}>
          <span className="w-40 shrink-0 truncate text-sm sm:w-48" title={r.label}>
            {r.label}
          </span>
          <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
            <div
              className="h-full bg-brand/60"
              style={{ width: `${Math.max(2, (r.value / max) * 100)}%` }}
            />
          </div>
          <span className="w-20 shrink-0 text-right font-mono text-xs">
            {fmtInt(r.value)} <span className="text-muted-foreground">{unit}</span>
          </span>
        </div>
      ))}
    </div>
  );
}
