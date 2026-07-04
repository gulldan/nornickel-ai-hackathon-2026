import { useCallback, useEffect, useState } from "react";
import { CheckCircle2, CircleAlert, Coins, Cpu, Gauge, Loader2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Card, CardContent } from "@/shared/ui/Card";
import { Kicker, MetricTile, PageHeader, Section, Segmented } from "@/shared/ui";
import { Skeleton } from "@/shared/ui/Skeleton";
import {
  getLlmUsage,
  getPerf,
  type HypothesisJobStat,
  type LlmProvider,
  type LlmUsage,
  type PerfResponse,
} from "@/features/admin/api";
import { JOB_TITLES, WORKER_LABELS, useBackgroundTasks, type WorkerStatus } from "@/features/task";
import { cn } from "@/shared/lib/cn";
import { currentLocale, i18n } from "@/shared/i18n";
import { formatDateTime } from "@/shared/lib/format";

// The whole document + query journey, grouped by the service that OWNS each
// stage — so the bottleneck and its responsible team are obvious at a glance.
// Stage keys are read from the merged ingestion (per-queue) + pipeline
// (per-stage) timings; phase tags which lifecycle the service belongs to.
// svc — технические имена контейнеров (не переводятся), подписи этапов — в словаре.
const SERVICES = [
  {
    svc: "main-service", // приём загрузки
    phase: "ingest",
    stages: [{ key: "upload", labelKey: "metrics.trace.stages.upload" }],
  },
  {
    svc: "archive-worker", // распаковка
    phase: "ingest",
    stages: [{ key: "parse.archive", labelKey: "metrics.trace.stages.parseArchive" }],
  },
  {
    svc: "pdf-parser", // разбор PDF
    phase: "ingest",
    stages: [{ key: "parse.pdf", labelKey: "metrics.trace.stages.parsePdf" }],
  },
  {
    svc: "office-parser", // разбор Office
    phase: "ingest",
    stages: [{ key: "parse.office", labelKey: "metrics.trace.stages.parseOffice" }],
  },
  {
    svc: "email-parser", // разбор писем
    phase: "ingest",
    stages: [{ key: "parse.email", labelKey: "metrics.trace.stages.parseEmail" }],
  },
  {
    svc: "ocr-service", // OCR
    phase: "ingest",
    stages: [{ key: "parse.ocr", labelKey: "metrics.trace.stages.parseOcr" }],
  },
  {
    svc: "chunk-splitter", // чанкинг · эмбеддинг · индексация
    phase: "ingest",
    stages: [
      { key: "chunk_split", labelKey: "metrics.trace.stages.chunkSplit" },
      { key: "chunk_embed", labelKey: "metrics.trace.stages.chunkEmbed" },
      { key: "chunk_index", labelKey: "metrics.trace.stages.chunkIndex" },
    ],
  },
  {
    svc: "llm-service", // обработка запроса (RAG)
    phase: "query",
    stages: [
      { key: "embed", labelKey: "metrics.trace.stages.embed" },
      { key: "retrieve_vector", labelKey: "metrics.trace.stages.retrieveVector" },
      { key: "retrieve_lexical", labelKey: "metrics.trace.stages.retrieveLexical" },
      { key: "rerank", labelKey: "metrics.trace.stages.rerank" },
      { key: "generate", labelKey: "metrics.trace.stages.generate" },
    ],
  },
] as const;

// ---- number formatting for the usage/budget block ----
function fmtInt(n: number): string {
  return new Intl.NumberFormat(currentLocale()).format(Math.round(n));
}

/** До одного знака после запятой в текущей локали: 1.8 → «1,8» / "1.8". */
function fmt1(n: number): string {
  return new Intl.NumberFormat(currentLocale(), { maximumFractionDigits: 1 }).format(n);
}

/** Compact token counts: 1.2 млн / 340 тыс / 512. */
function fmtCompact(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)} ${i18n.t("admin:units.mln")}`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 0 : 1)} ${i18n.t("admin:units.thousand")}`;
  return String(Math.round(n));
}

/** Cost in the provider currency — reads "$0" / "₽0" when nothing was billed. */
function fmtCost(n: number, currency: string): string {
  if (!n) return `${currency}0`;
  if (n < 0.01) return `${currency}${n.toFixed(4)}`;
  return `${currency}${n.toFixed(2)}`;
}

/** Unified spend in rubles, sign after the amount (Russian convention). */
function fmtRub(n: number): string {
  if (!n) return "0 ₽";
  if (n < 100) return `${fmt1(n)} ₽`;
  return `${fmtInt(n)} ₽`;
}

/** Rate per unit — keeps one decimal below 10 so a small per-hypothesis value
 *  reads honestly ("1,7") instead of rounding to a misleading "0". */
function fmtRate(n: number): string {
  return n >= 10 ? fmtInt(n) : fmt1(Math.round(n * 10) / 10);
}

/** "2026-07-03" → "07-03" for compact day-axis ticks. */
function shortDay(s: string): string {
  return s.length >= 10 ? s.slice(5) : s;
}

/** Секунды в компактную строку: «14 мс» / «1,8 с». */
function fmtSec(v: number | undefined): string {
  if (typeof v !== "number" || !Number.isFinite(v)) return "—";
  if (v < 1) return `${Math.round(v * 1000)} ${i18n.t("admin:units.ms")}`;
  return `${fmt1(Math.round(v * 10) / 10)} ${i18n.t("admin:units.s")}`;
}

/** Тихая ссылка-обновление — общий идиом для обеих секций. */
function RefreshButton({ onClick }: { onClick: () => void }) {
  const { t } = useTranslation("admin");
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex items-center gap-1 font-mono text-xs text-muted-foreground hover:text-foreground"
    >
      <RefreshCw className="size-3.5" aria-hidden />
      {t("actions.refresh")}
    </button>
  );
}

/** Дневные столбики — лёгкая замена recharts-графику: страница «Метрики» больше
 *  не тянет ~400 КБ бандла графиков, из-за которого она заметно тормозила при
 *  открытии. Тот же язык, что у баров ниже (цвет brand, моно-подписи). */
function DayBars({
  data,
  metric,
  format,
}: {
  data: { day: string; requests: number; tokens: number }[];
  metric: "requests" | "tokens";
  format: (n: number) => string;
}) {
  const max = Math.max(1, ...data.map((d) => d[metric]));
  return (
    <div>
      <span className="mb-1 block font-mono text-[10px] text-muted-foreground">{format(max)}</span>
      <div className="flex h-44 items-end gap-1.5 border-b">
        {data.map((d) => {
          const v = d[metric];
          const pct = Math.max(1.5, (v / max) * 100);
          return (
            <div
              key={d.day}
              className="flex-1 rounded-t bg-brand/70 transition-colors hover:bg-brand"
              style={{ height: `${pct}%` }}
              title={`${d.day}: ${format(v)}`}
            />
          );
        })}
      </div>
      <div className="mt-1.5 flex gap-1.5">
        {data.map((d) => (
          <span
            key={d.day}
            className="min-w-0 flex-1 truncate text-center font-mono text-[10px] text-muted-foreground"
          >
            {shortDay(d.day)}
          </span>
        ))}
      </div>
    </div>
  );
}

const QUOTA_BG = { ok: "bg-ok", warn: "bg-warn", risk: "bg-risk" } as const;

/** A thin fill bar coloured by how full it is — the quota gauge, page-local like
 *  the stage bars below (not a new shared component). */
function FillBar({ pct }: { pct: number }) {
  const tone = pct < 70 ? "ok" : pct < 90 ? "warn" : "risk";
  return (
    <span className="mt-1.5 block h-1.5 w-full overflow-hidden rounded-full bg-muted">
      <span
        className={cn("block h-full", QUOTA_BG[tone])}
        style={{ width: `${Math.max(2, Math.min(100, pct))}%` }}
        aria-hidden
      />
    </span>
  );
}

/** One breakdown list (by operation / by model): proportional bars, sorted by
 *  requests — same dense inline idiom as the stage trace below. */
function Breakdown({
  title,
  rows,
}: {
  title: string;
  rows: { label: string; requests: number; tokens: number }[];
}) {
  const { t } = useTranslation("admin");
  const max = Math.max(1, ...rows.map((r) => r.requests));
  return (
    <div>
      <Kicker className="mb-2">{title}</Kicker>
      <div className="overflow-hidden rounded-xl border bg-card">
        {rows.length === 0 && (
          <p className="px-4 py-3 text-sm text-muted-foreground">{t("noData")}</p>
        )}
        {rows.map((r, i) => (
          <div
            key={r.label}
            className={cn("flex items-center gap-3 px-4 py-2", i > 0 && "border-t")}
          >
            <span className="w-36 shrink-0 truncate text-sm" title={r.label}>
              {r.label}
            </span>
            <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full bg-brand/60"
                style={{ width: `${Math.max(2, (r.requests / max) * 100)}%` }}
              />
            </div>
            <span className="w-14 shrink-0 text-right font-mono text-xs">{fmtInt(r.requests)}</span>
            <span className="w-16 shrink-0 text-right font-mono text-xs text-muted-foreground">
              {fmtCompact(r.tokens)}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

const KIND_BADGE: Record<LlmProvider["kind"], string> = {
  real: "border-brand/30 bg-brand/10 text-brand",
  notional: "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
  free: "border-border bg-muted text-muted-foreground",
};

/** Расход по провайдерам: строка на провайдера в его валюте (реальные $
 *  OpenRouter, условные ₽ Yandex по ключу организаторов, бесплатная локальная),
 *  плюс ≈₽-эквивалент для валют в долларах. Валюты здесь не суммируются — единый
 *  итог в рублях показан в шапке. */
function ProviderSpend({ providers }: { providers: LlmProvider[] }) {
  const { t } = useTranslation("admin");
  if (providers.length === 0) return null;
  return (
    <div className="mt-4">
      <Kicker className="mb-2">{t("metrics.usage.byProviderTitle")}</Kicker>
      <div className="overflow-hidden rounded-xl border bg-card">
        {providers.map((p, i) => (
          <div
            key={`${p.key}:${p.label}`}
            className={cn("flex items-center gap-3 px-4 py-3", i > 0 && "border-t")}
          >
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="truncate font-medium">{p.label}</span>
                <span
                  className={cn(
                    "rounded border px-1.5 py-0.5 text-[11px] leading-none",
                    KIND_BADGE[p.kind],
                  )}
                >
                  {t(`metrics.usage.kind.${p.kind}`)}
                </span>
              </div>
              <div className="mt-0.5 font-mono text-xs text-muted-foreground">
                {t("metrics.usage.providerSub", {
                  tokens: fmtCompact(p.total_tokens),
                  count: p.requests,
                  formatted: fmtInt(p.requests),
                })}
              </div>
            </div>
            <div className="shrink-0 text-right">
              <div className="font-mono tabular-nums">
                {p.kind === "free" ? t("metrics.usage.free") : fmtCost(p.cost_usd, p.currency)}
              </div>
              {p.currency === "$" && p.cost_usd > 0 && (
                <div className="font-mono text-xs text-muted-foreground">
                  ≈ {fmtRub(p.cost_rub)}
                </div>
              )}
              {p.cost_estimated && (
                <div className="text-[11px] text-muted-foreground">
                  {t("metrics.usage.estimatedNote")}
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

/** «Бюджет и расход» — расход LLM и бюджет провайдера за выбранный период.
 *  Self-loads; degrades to a quiet empty state until data accrues. */
function UsageSection() {
  const { t } = useTranslation("admin");
  const [days, setDays] = useState<7 | 30>(7);
  const [usage, setUsage] = useState<LlmUsage | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "empty">("loading");
  const [metric, setMetric] = useState<"requests" | "tokens">("requests");

  const load = useCallback(() => {
    setState("loading");
    getLlmUsage(days)
      .then((u) => {
        setUsage(u);
        setState(u.totals.requests > 0 ? "ready" : "empty");
      })
      // Endpoint may 404 until the backend ships — treat as a soft empty state.
      .catch(() => setState("empty"));
  }, [days]);

  useEffect(() => load(), [load]);

  const controls = (
    <div className="flex items-center gap-2">
      <Segmented
        aria-label={t("metrics.usage.periodAria")}
        value={days === 30 ? "30" : "7"}
        onChange={(v) => setDays(v === "30" ? 30 : 7)}
        options={[
          { value: "7", label: t("metrics.usage.days7") },
          { value: "30", label: t("metrics.usage.days30") },
        ]}
      />
      <RefreshButton onClick={load} />
    </div>
  );

  if (state === "loading") {
    return (
      <Section title={t("metrics.usage.title")} action={controls} className="mt-6">
        <Skeleton className="h-56 w-full" />
      </Section>
    );
  }
  if (state === "empty" || !usage) {
    return (
      <Section
        title={t("metrics.usage.title")}
        description={t("metrics.usage.pendingDescription")}
        action={controls}
        className="mt-6"
      >
        <Card>
          <CardContent className="pt-6 text-sm text-muted-foreground">
            {t("metrics.usage.noData")}
          </CardContent>
        </Card>
      </Section>
    );
  }

  const totals = usage.totals;
  const q = usage.quota;
  const b = usage.budget;
  const ph = usage.per_hypothesis;
  const cur = usage.currency || "$";
  const provider = usage.provider || t("metrics.usage.providerFallback");
  const hasUsd = usage.providers.some((p) => p.currency === "$" && p.cost_usd > 0);
  const quotaPct = q.daily_limit > 0 ? (q.today_requests / q.daily_limit) * 100 : 0;
  const creditsPct = b.credits_total > 0 ? (b.credits_used / b.credits_total) * 100 : 0;

  const trend = usage.by_day.map((d) => ({
    day: d.day,
    requests: d.requests,
    tokens: d.total_tokens,
  }));
  return (
    <Section
      title={t("metrics.usage.title")}
      description={t("metrics.usage.rangeDescription", { provider, days: usage.range_days })}
      action={controls}
      className="mt-6"
    >
      {/* Hero: today's quota, tokens over the range, cost, remaining credits. */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <MetricTile
          label={t("metrics.usage.requestsToday")}
          icon={Gauge}
          value={fmtInt(q.today_requests)}
          sub={
            q.daily_limit > 0 ? (
              <>
                <span className="font-mono">
                  {t("metrics.usage.requestsTodaySub", {
                    limit: fmtInt(q.daily_limit),
                    perMinUsed: q.per_min_used,
                    perMinLimit: q.per_min_limit,
                  })}
                </span>
                <FillBar pct={quotaPct} />
              </>
            ) : (
              <span className="font-mono">{t("metrics.usage.noQuota")}</span>
            )
          }
        />
        <MetricTile
          label={t("metrics.usage.tokensForRange")}
          icon={Cpu}
          value={fmtCompact(totals.total_tokens)}
          sub={
            <span className="font-mono">
              {t("metrics.usage.requestsCount", {
                count: totals.requests,
                formatted: fmtInt(totals.requests),
              })}
            </span>
          }
        />
        <MetricTile
          label={t("metrics.usage.cost")}
          icon={Coins}
          value={fmtRub(usage.total_cost_rub)}
          sub={
            <span className="font-mono">
              {hasUsd
                ? t("metrics.usage.rubRateNote", { rate: fmtInt(usage.rub_per_usd) })
                : t("metrics.usage.modelsCount", { count: usage.by_model.length })}
            </span>
          }
        />
        <MetricTile
          label={t("metrics.usage.credits")}
          icon={Coins}
          value={b.credits_total > 0 ? fmtCost(b.credits_remaining, cur) : "—"}
          sub={
            b.credits_total > 0 ? (
              <>
                <span className="font-mono">
                  {t("metrics.usage.creditsSub", {
                    total: fmtCost(b.credits_total, cur),
                    used: fmtCost(b.credits_used, cur),
                  })}
                </span>
                <FillBar pct={creditsPct} />
              </>
            ) : (
              <span className="font-mono">{t("metrics.usage.budgetUnset")}</span>
            )
          }
        />
      </div>

      <ProviderSpend providers={usage.providers} />

      {/* Daily trend (by_day is a flat total — single series with a metric toggle). */}
      <div className="mt-4 rounded-xl border bg-card p-4">
        <div className="mb-2 flex items-center justify-between">
          <Kicker>
            {metric === "requests"
              ? t("metrics.usage.requestsByDay")
              : t("metrics.usage.tokensByDay")}
          </Kicker>
          <Segmented
            aria-label={t("metrics.usage.chartMetricAria")}
            value={metric}
            onChange={setMetric}
            options={[
              { value: "requests", label: t("metrics.usage.chartRequests") },
              { value: "tokens", label: t("metrics.usage.chartTokens") },
            ]}
          />
        </div>
        <DayBars data={trend} metric={metric} format={fmtCompact} />
      </div>

      {/* Breakdowns: by operation and by model (range totals). */}
      <div className="mt-4 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Breakdown
          title={t("metrics.usage.byOperation")}
          rows={usage.by_operation
            .map((o) => ({ label: o.operation, requests: o.requests, tokens: o.total_tokens }))
            .toSorted((a, c) => c.requests - a.requests)}
        />
        <Breakdown
          title={t("metrics.usage.byModel")}
          rows={usage.by_model
            .map((m) => ({ label: m.model, requests: m.requests, tokens: m.total_tokens }))
            .toSorted((a, c) => c.requests - a.requests)}
        />
      </div>

      {/* Signature: the cost of the product's unit — one hypothesis. */}
      <div className="mt-4 rounded-xl border border-brand-border bg-brand-wash p-5">
        <Kicker>{t("metrics.usage.perHypothesis")}</Kicker>
        <p className="mt-2 font-mono text-xl tabular-nums">
          {t("metrics.usage.perHypothesisValue", {
            requests: fmtRate(ph.requests),
            tokens: fmtCompact(ph.total_tokens),
            cost: fmtRub(ph.cost_rub),
          })}
        </p>
        <p className="mt-2 text-sm text-muted-foreground">
          {ph.operations.length > 0 && (
            <>
              {t("metrics.usage.composedOf")}{" "}
              <span className="font-mono">{ph.operations.join(" → ")}</span>
              {" · "}
            </>
          )}
          {t("metrics.usage.averagedOver", {
            count: ph.hypotheses,
            formatted: fmtInt(ph.hypotheses),
          })}
        </p>
      </div>
    </Section>
  );
}

// ServiceTrace: один плотный список, сгруппированный кикерами «Путь документа»
// и «Путь запроса». Бары в одной глобальной шкале (max p50), самые горячие
// этапы — сплошной заливкой.
function ServiceTrace({ data }: { data: Record<string, { p50?: number; p95?: number }> }) {
  const { t } = useTranslation("admin");
  const maxP50 = Math.max(
    0.0001,
    ...SERVICES.flatMap((s) => s.stages.map((st) => data[st.key]?.p50 ?? 0)),
  );
  const phases = [
    { key: "ingest", labelKey: "metrics.trace.ingestPath" },
    { key: "query", labelKey: "metrics.trace.queryPath" },
  ] as const;
  return (
    <div className="space-y-6">
      {phases.map((phase) => {
        const rows = SERVICES.filter((s) => s.phase === phase.key).flatMap((s) =>
          s.stages
            .filter((st) => data[st.key])
            .map((st) => ({ svc: s.svc, label: t(st.labelKey), p: data[st.key] })),
        );
        if (rows.length === 0) return null;
        return (
          <div key={phase.key}>
            <Kicker className="mb-2">{t(phase.labelKey)}</Kicker>
            <div className="overflow-hidden rounded-xl border bg-card">
              {rows.map((r, i) => {
                const p50 = r.p?.p50 ?? 0;
                const width = Math.min(100, (p50 / maxP50) * 100);
                const hot = p50 >= maxP50 * 0.25;
                return (
                  <div
                    key={`${r.svc}:${r.label}`}
                    className={`flex items-center gap-3 px-4 py-2 ${i > 0 ? "border-t" : ""}`}
                  >
                    <span className="w-32 shrink-0 truncate font-mono text-xs text-muted-foreground">
                      {r.svc}
                    </span>
                    <span className="w-64 shrink-0 truncate text-sm" title={r.label}>
                      {r.label}
                    </span>
                    <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
                      <div
                        className={hot ? "h-full bg-foreground" : "h-full bg-brand/60"}
                        style={{ width: `${Math.max(1.5, width)}%` }}
                      />
                    </div>
                    <span className="w-20 shrink-0 text-right font-mono text-xs">
                      {fmtSec(r.p?.p50)}
                    </span>
                    <span className="w-20 shrink-0 text-right font-mono text-xs text-muted-foreground">
                      {fmtSec(r.p?.p95)}
                    </span>
                  </div>
                );
              })}
              <div className="flex items-center justify-end gap-3 border-t bg-secondary/50 px-4 py-1.5">
                <span className="w-20 text-right font-mono text-[11px] text-muted-foreground">
                  p50
                </span>
                <span className="w-20 text-right font-mono text-[11px] text-muted-foreground">
                  p95
                </span>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

/** «Гипотезы за минуты»: полная длительность фоновых задач гипотез по видам за
 *  последние 24 часа — медиана и максимум от старта до результата. Тот же
 *  плотный список, что и трейс этапов выше. */
function HypothesisJobsCard({ stats }: { stats: HypothesisJobStat[] }) {
  const { t } = useTranslation("admin");
  if (stats.length === 0) return null;
  const max = Math.max(0.0001, ...stats.map((s) => s.p50_sec));
  return (
    <Section
      title={t("metrics.jobs.title")}
      description={t("metrics.jobs.description")}
      className="mt-8"
    >
      <div className="overflow-hidden rounded-xl border bg-card">
        {stats.map((s, i) => (
          <div
            key={s.kind}
            className={`flex items-center gap-3 px-4 py-2 ${i > 0 ? "border-t" : ""}`}
          >
            <span className="w-64 shrink-0 truncate text-sm">{JOB_TITLES[s.kind] ?? s.kind}</span>
            <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full bg-brand/60"
                style={{ width: `${Math.max(1.5, (s.p50_sec / max) * 100)}%` }}
              />
            </div>
            <span className="w-20 shrink-0 text-right font-mono text-xs">{fmtSec(s.p50_sec)}</span>
            <span className="w-20 shrink-0 text-right font-mono text-xs text-muted-foreground">
              {fmtSec(s.max_sec)}
            </span>
            <span className="w-20 shrink-0 text-right font-mono text-xs text-muted-foreground">
              {t("metrics.jobs.tasksCount", { count: s.count, formatted: fmtInt(s.count) })}
            </span>
          </div>
        ))}
        <div className="flex items-center justify-end gap-3 border-t bg-secondary/50 px-4 py-1.5">
          <span className="w-20 text-right font-mono text-[11px] text-muted-foreground">
            {t("metrics.jobs.median")}
          </span>
          <span className="w-20 text-right font-mono text-[11px] text-muted-foreground">
            {t("metrics.jobs.max")}
          </span>
          <span className="w-20 text-right font-mono text-[11px] text-muted-foreground">
            {t("metrics.jobs.last24h")}
          </span>
        </div>
      </div>
    </Section>
  );
}

// eval («Проверка качества ответов») отключён вместе с моковыми метриками —
// не показываем его как фоновый расчёт.
const HIDDEN_WORKERS = new Set(["eval"]);

/** Фоновая фабрика: задержки фоновых расчётов из статусов воркеров. Показываем
 *  все воркеры, что публикует бэкенд (не молча отбрасываем незнакомые), с
 *  подписью из WORKER_LABELS или именем как запаской. */
function BackgroundFactory({ workers }: { workers: WorkerStatus[] }) {
  const { t } = useTranslation("admin");
  const shown = workers.filter((w) => w.name && !HIDDEN_WORKERS.has(w.name));
  if (shown.length === 0) return null;
  return (
    <Section
      title={t("metrics.workers.title")}
      description={t("metrics.workers.description")}
      className="mt-8"
    >
      <div className="overflow-hidden rounded-xl border bg-card">
        {shown.map((w, i) => (
          <div
            key={w.name}
            className={`flex items-center gap-3 px-4 py-2.5 ${i > 0 ? "border-t" : ""}`}
          >
            {w.state === "running" ? (
              <Loader2 className="size-4 shrink-0 animate-spin text-brand" aria-hidden />
            ) : w.state === "error" ? (
              <CircleAlert className="size-4 shrink-0 text-risk" aria-hidden />
            ) : (
              <CheckCircle2 className="size-4 shrink-0 text-ok" aria-hidden />
            )}
            <span className="min-w-0 flex-1 truncate text-sm">
              {WORKER_LABELS[w.name] ?? w.name}
              {w.state === "error" && w.lastError && (
                <span className="ml-2 text-xs text-risk">{w.lastError}</span>
              )}
            </span>
            <span className="shrink-0 font-mono text-xs text-muted-foreground">
              {w.lastRunSeconds !== null &&
                t("metrics.workers.run", { duration: fmtSec(w.lastRunSeconds) })}
              {w.lastRunSeconds !== null && w.updatedAt && " · "}
              {w.updatedAt && t("metrics.workers.updated", { time: formatDateTime(w.updatedAt) })}
              {w.lastRunSeconds === null && !w.updatedAt && t("metrics.workers.neverRan")}
            </span>
          </div>
        ))}
      </div>
    </Section>
  );
}

/** «Производительность» — живые задержки конвейера по этапам (Prometheus, по
 *  реальному трафику) плюс фоновые воркеры. Никакого eval-набора. */
function PerformanceSection({ workers }: { workers: WorkerStatus[] }) {
  const { t } = useTranslation("admin");
  const [perf, setPerf] = useState<PerfResponse | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "empty">("loading");

  const load = useCallback(() => {
    setState("loading");
    getPerf()
      .then((p) => {
        setPerf(p);
        const has =
          Object.keys(p.pipeline).length +
            Object.keys(p.ingestion).length +
            (p.hypothesis_jobs?.length ?? 0) >
          0;
        setState(has ? "ready" : "empty");
      })
      .catch(() => setState("empty"));
  }, []);

  useEffect(() => load(), [load]);

  // One unified lookup: per-queue ingestion timings + per-stage query timings.
  const trace = perf ? { ...perf.ingestion, ...perf.pipeline } : {};

  return (
    <>
      <Section
        title={t("metrics.trace.title")}
        description={t("metrics.trace.description")}
        action={<RefreshButton onClick={load} />}
        className="mt-6"
      >
        {state === "loading" ? (
          <Skeleton className="h-64 w-full" />
        ) : Object.keys(trace).length === 0 ? (
          <Card>
            <CardContent className="pt-6 text-sm text-muted-foreground">
              {t("metrics.trace.empty")}
            </CardContent>
          </Card>
        ) : (
          <ServiceTrace data={trace} />
        )}
      </Section>

      <HypothesisJobsCard stats={perf?.hypothesis_jobs ?? []} />

      <BackgroundFactory workers={workers} />
    </>
  );
}

export function AdminMetrics() {
  const { t } = useTranslation("admin");
  const [view, setView] = useState<"budget" | "perf">("budget");
  const { activity } = useBackgroundTasks();

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <PageHeader
        kicker={t("metrics.kicker")}
        title={t("metrics.title")}
        description={t("metrics.description")}
      />

      {/* Primary switch between the two entities — budget vs. performance. */}
      <Segmented
        size="md"
        aria-label={t("metrics.viewAria")}
        className="mt-5"
        value={view}
        onChange={setView}
        options={[
          {
            value: "budget",
            label: (
              <>
                <Coins className="size-4" aria-hidden />
                {t("metrics.viewBudget")}
              </>
            ),
          },
          {
            value: "perf",
            label: (
              <>
                <Gauge className="size-4" aria-hidden />
                {t("metrics.viewPerf")}
              </>
            ),
          },
        ]}
      />

      {view === "budget" ? (
        <UsageSection />
      ) : (
        <PerformanceSection workers={activity?.workers ?? []} />
      )}
    </div>
  );
}
