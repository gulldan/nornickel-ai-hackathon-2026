import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import {
  Atom,
  Beaker,
  ChartLine,
  Clock,
  Cog,
  Coins,
  Factory,
  Flag,
  Gauge,
  GitBranch,
  Layers,
  Microscope,
  Pickaxe,
  ShieldCheck,
  Sparkles,
  Target,
  TrendingUp,
  TriangleAlert,
} from "lucide-react";

import {
  type AssessmentSchema,
  type AssessmentScores,
  type HypothesisDetail,
  type Ranking,
} from "@/features/hypothesis/api";
import {
  CONFIDENCE_FACETS,
  pct,
  schemaMissingLabels,
  successCriteriaLines,
} from "@/features/hypothesis/model";
import { cn } from "@/shared/lib/cn";
import { InfoHint } from "@/shared/ui/InfoHint";

type Translate = TFunction<"hypothesisDetail">;

/** Split verification confidence (assessment.scores): belief / verdict /
 *  evidence shown as DISTINCT labelled bars — never one merged number. An
 *  «Не проверено» badge guards against reading a high score as verified. */
export function ConfidenceSplit({ scores }: { scores: AssessmentScores }) {
  const { t } = useTranslation("hypothesisDetail");
  const facets = CONFIDENCE_FACETS.map((f) => ({
    key: f.key,
    label: f.label,
    hint: f.hint,
    value: scores[f.key] as number | undefined,
  })).filter((f) => typeof f.value === "number");
  if (facets.length === 0 && !scores.unverified) {
    return null;
  }
  return (
    <div className="space-y-3">
      {scores.unverified && (
        <div className="flex items-start gap-2 rounded-md border border-transparent bg-warn-wash px-3 py-2">
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden />
          <div>
            <p className="text-xs font-medium text-warn">{t("confidence.unverified")}</p>
            <p className="text-xs text-warn">{t("confidence.unverifiedNote")}</p>
          </div>
        </div>
      )}
      {facets.map((f) => (
        <div key={f.key}>
          <div className="flex items-baseline justify-between gap-2 text-xs">
            <span className="inline-flex items-center gap-1 text-muted-foreground">
              {f.label}
              <InfoHint label={f.label}>{f.hint}</InfoHint>
            </span>
            <span className="font-medium tabular-nums text-foreground">{pct(f.value)}</span>
          </div>
          <div className="mt-1 h-2 w-full overflow-hidden rounded-full bg-muted">
            <div
              className={cn("h-full rounded-full", scores.unverified ? "bg-warn" : "bg-brand")}
              style={{ width: `${Math.round((f.value ?? 0) * 100)}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

/** Subtle "what's missing" badge for an incomplete generated hypothesis
 *  (assessment.schema.complete === false). Hidden when complete. */
export function SchemaCompleteness({ schema }: { schema: AssessmentSchema }) {
  const { t } = useTranslation("hypothesisDetail");
  if (schema.complete !== false) {
    return null;
  }
  const missing = schemaMissingLabels(schema.missing);
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-md border border-transparent bg-warn-wash px-2.5 py-1.5 text-xs">
      <span className="inline-flex items-center gap-1 font-medium text-warn">
        <TriangleAlert className="size-3.5" aria-hidden />
        {t("schema.incomplete")}
        {typeof schema.score === "number" && (
          <span className="font-normal text-warn">· {pct(schema.score)}</span>
        )}
      </span>
      {missing.length > 0 && (
        <span className="text-warn">{t("schema.missing", { list: missing.join(", ") })}</span>
      )}
    </div>
  );
}

export function MaterialsPassport({ detail }: { detail: HypothesisDetail }) {
  const { t } = useTranslation("hypothesisDetail");
  const rows = [
    { icon: Layers, label: t("materials.system"), value: detail.material_system },
    { icon: Atom, label: t("materials.composition"), value: detail.composition_change },
    { icon: Factory, label: t("materials.process"), value: detail.process_change },
    {
      icon: Microscope,
      label: t("materials.microstructure"),
      value: detail.microstructure_mechanism,
    },
    { icon: Target, label: t("materials.targetProperty"), value: detail.target_property },
  ].filter((r) => (r.value ?? "") !== "");
  const chips = [
    {
      icon: Microscope,
      label: t("materials.characterization"),
      items: detail.characterization_methods,
    },
    { icon: Beaker, label: t("materials.tests"), items: detail.test_methods },
    { icon: TriangleAlert, label: t("materials.failureRisks"), items: detail.failure_modes },
  ].filter((g) => (g.items?.length ?? 0) > 0);
  if (rows.length === 0 && chips.length === 0) {
    return null;
  }
  return (
    <div className="space-y-3">
      {rows.length > 0 && (
        <dl className="divide-y">
          {rows.map((r) => (
            <div
              key={r.label}
              className="grid grid-cols-1 gap-1 py-2.5 first:pt-0 sm:grid-cols-[11rem_minmax(0,1fr)] sm:gap-3"
            >
              <dt className="flex items-center gap-2 self-start text-xs font-medium uppercase tracking-wide text-muted-foreground">
                <r.icon className="size-3.5 shrink-0" aria-hidden />
                {r.label}
              </dt>
              <dd className="text-sm leading-snug">{r.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {chips.length > 0 && (
        <div className={cn("grid gap-3", chips.length > 1 && "sm:grid-cols-2")}>
          {chips.map((g) => (
            <div key={g.label}>
              <p className="flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                <g.icon className="size-3.5 shrink-0" aria-hidden />
                {g.label}
              </p>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {g.items?.map((item) => (
                  <span key={item} className="rounded-md border bg-muted/40 px-2 py-0.5 text-xs">
                    {item}
                  </span>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function RankingExplainer({ ranking }: { ranking: Ranking }) {
  const { t } = useTranslation("hypothesisDetail");
  const factors = ranking.factors ?? [];
  if (factors.length === 0) {
    return null;
  }
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <p className="max-w-prose text-xs leading-relaxed text-muted-foreground">
          {t("ranking.explainer")}
        </p>
        {ranking.version && (
          <span className="rounded-md border bg-background px-2 py-1 text-[11px] text-muted-foreground">
            {ranking.version}
          </span>
        )}
      </div>

      <div className="overflow-hidden rounded-md border">
        <div className="grid grid-cols-[minmax(0,1fr)_3.25rem_3rem] gap-2 bg-muted/40 px-3 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
          <span>{t("ranking.factor")}</span>
          <span className="text-right">{t("ranking.contribution")}</span>
          <span className="text-right">{t("ranking.weight")}</span>
        </div>
        {factors.map((f) => (
          <div
            key={f.key}
            className="grid grid-cols-[minmax(0,1fr)_3.25rem_3rem] gap-2 border-t px-3 py-2.5 text-sm"
          >
            <div className="min-w-0">
              <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-medium leading-snug">{f.label}</span>
                {!f.scored && (
                  <span className="rounded-md border bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                    {t("ranking.notScored")}
                  </span>
                )}
              </div>
              {f.detail && (
                <p className="mt-0.5 text-xs leading-snug text-muted-foreground">{f.detail}</p>
              )}
            </div>
            <span className="text-right font-medium tabular-nums">
              +{Math.round(f.contribution * 100)}
            </span>
            <span className="text-right text-xs tabular-nums text-muted-foreground">
              {Math.round(f.weight * 100)}%
            </span>
          </div>
        ))}
      </div>

      {ranking.expert && (
        <div
          className={cn(
            "flex items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm",
            ranking.expert.multiplier >= 1 ? "bg-ok-wash text-ok" : "bg-risk-wash text-risk",
          )}
        >
          <span className="font-medium">
            {t("ranking.expertDecision", {
              label: ranking.expert.label ?? ranking.expert.status,
            })}
          </span>
          <span className="font-mono text-xs">×{ranking.expert.multiplier.toFixed(2)}</span>
        </div>
      )}

      {ranking.formula && (
        <p className="break-words rounded-md border bg-muted/20 px-2.5 py-1.5 text-[11px] leading-relaxed text-muted-foreground">
          {t("ranking.formula", { formula: ranking.formula })}
        </p>
      )}
    </div>
  );
}

// Ключи этапов приходят из данных по-русски; подписи — из словаря.
// Стадии — свободный текст от модели: сначала точное совпадение, затем поиск
// известного ключа как подстроки (например «технологический показатель»).
const STAGE_META: Record<
  string,
  {
    icon: LucideIcon;
    labelKey:
      | "causal.composition"
      | "causal.process"
      | "causal.microstructure"
      | "causal.property"
      | "causal.feed"
      | "causal.operation"
      | "causal.regime"
      | "causal.indicator"
      | "causal.kpi";
  }
> = {
  состав: { icon: Atom, labelKey: "causal.composition" },
  процесс: { icon: Factory, labelKey: "causal.process" },
  микроструктура: { icon: Microscope, labelKey: "causal.microstructure" },
  свойство: { icon: TrendingUp, labelKey: "causal.property" },
  питание: { icon: Pickaxe, labelKey: "causal.feed" },
  сырьё: { icon: Pickaxe, labelKey: "causal.feed" },
  сырье: { icon: Pickaxe, labelKey: "causal.feed" },
  операция: { icon: Cog, labelKey: "causal.operation" },
  аппарат: { icon: Cog, labelKey: "causal.operation" },
  режим: { icon: Gauge, labelKey: "causal.regime" },
  показатель: { icon: ChartLine, labelKey: "causal.indicator" },
  kpi: { icon: Target, labelKey: "causal.kpi" },
};

function stageMeta(stage: string): (typeof STAGE_META)[string] | undefined {
  const key = stage.toLowerCase();
  const exact = STAGE_META[key];
  if (exact) return exact;
  const hit = Object.keys(STAGE_META).find((k) => key.includes(k));
  return hit ? STAGE_META[hit] : undefined;
}

export function CausalChain({ steps }: { steps: NonNullable<HypothesisDetail["causal_chain"]> }) {
  const { t } = useTranslation("hypothesisDetail");
  const clean = steps.filter((s) => (s.stage ?? "") !== "" || (s.change ?? "") !== "");
  if (clean.length === 0) {
    return null;
  }
  return (
    <ol className="flex flex-col md:flex-row md:flex-wrap md:gap-y-4">
      {clean.map((s, i) => {
        const meta = stageMeta(s.stage ?? "");
        const Icon = meta?.icon ?? GitBranch;
        const last = i === clean.length - 1;
        return (
          <li
            key={`${s.stage ?? ""}-${s.change ?? ""}`}
            className="flex gap-3 md:block md:min-w-44 md:flex-1 md:gap-0 md:pr-3"
          >
            <div className="flex flex-col items-center md:flex-row">
              <span
                className={cn(
                  "flex size-7 shrink-0 items-center justify-center rounded-full border",
                  last
                    ? "border-ok/40 bg-ok-wash text-ok"
                    : "border-brand-border bg-brand-wash text-brand",
                )}
              >
                <Icon className="size-3.5" aria-hidden />
              </span>
              <span
                className={cn(
                  "my-1 w-px flex-1 bg-border md:mx-2 md:my-0 md:h-px md:w-auto",
                  last && "invisible",
                )}
                aria-hidden
              />
            </div>
            <div className="min-w-0 flex-1 pb-4 last:pb-0 md:flex-none md:pb-0 md:pt-1.5">
              <p
                className={cn(
                  "text-[11px] font-medium uppercase tracking-wide",
                  last ? "text-ok" : "text-muted-foreground",
                )}
              >
                {s.stage || (meta ? t(meta.labelKey) : t("causal.step"))}
              </p>
              {s.change && (
                <p className="mt-0.5 max-w-56 text-sm leading-snug text-foreground">{s.change}</p>
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

export function ExperimentPlan({
  plan,
}: {
  plan: NonNullable<HypothesisDetail["experiment_plan"]>;
}) {
  const { t } = useTranslation("hypothesisDetail");
  // Process parameters arrive as {name, range} objects; flatten to readable chips.
  const paramChips = [
    ...(plan.process_parameters ?? [])
      .map((p) => [p.name, p.range].filter(Boolean).join(": "))
      .filter((s) => s !== ""),
    ...(plan.parameters ?? []),
  ];
  // Characterization + test methods (newer shape) fall back to the legacy `methods`.
  const charMethods = plan.characterization_methods ?? [];
  const testMethods = plan.test_methods ?? [];
  const legacyMethods = !charMethods.length && !testMethods.length ? (plan.methods ?? []) : [];
  const criteria = successCriteriaLines(plan.success_criteria);
  // Legacy "variables" kept as a fallback when no materials/parameters are given.
  const variables = plan.variables ?? [];
  const horizon = (plan.estimated_time ?? "") || (plan.horizon ?? "");
  const sections = (plan.sections ?? []).filter(
    (s) =>
      (s.title ?? "").trim() !== "" ||
      (s.purpose ?? "").trim() !== "" ||
      (s.items?.length ?? 0) > 0,
  );
  const experimentType = experimentTypeLabel(t, plan.experiment_type);

  const hasAnything =
    sections.length > 0 ||
    experimentType !== "" ||
    (plan.materials?.length ?? 0) > 0 ||
    paramChips.length > 0 ||
    charMethods.length > 0 ||
    testMethods.length > 0 ||
    legacyMethods.length > 0 ||
    variables.length > 0 ||
    (plan.controls?.length ?? 0) > 0 ||
    criteria.length > 0 ||
    (plan.risks?.length ?? 0) > 0 ||
    (plan.estimated_cost ?? "") !== "" ||
    horizon !== "";
  if (!hasAnything) {
    return null;
  }

  const methodsCount = charMethods.length + testMethods.length + legacyMethods.length;
  const showRoadmap = sections.length > 0;

  return (
    <div className="space-y-3 text-sm">
      {experimentType !== "" && (
        <span className="inline-flex w-fit items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-xs">
          <Sparkles className="size-3 text-muted-foreground" aria-hidden />
          {t("experiment.planType", { type: experimentType })}
        </span>
      )}
      {showRoadmap && (
        <PlanRoadmap
          sections={sections}
          criteria={criteria}
          cost={plan.estimated_cost ?? ""}
          time={horizon}
          materialsCount={plan.materials?.length ?? 0}
          methodsCount={methodsCount}
        />
      )}
      {sections.length > 0 && (
        <details className="group">
          <summary className="cursor-pointer list-none text-xs font-medium text-primary hover:underline">
            {t("experiment.stagesDetail", { count: sections.length })}
          </summary>
          <div className="mt-2 space-y-2">
            {sections.map((section, i) => (
              <div key={sectionKey(section)} className="rounded-md border bg-muted/25 px-3 py-2">
                <p className="text-sm font-medium">
                  {section.title || t("experiment.stage", { n: i + 1 })}
                </p>
                {section.purpose && (
                  <p className="mt-0.5 text-xs text-muted-foreground">{section.purpose}</p>
                )}
                {(section.items?.length ?? 0) > 0 && (
                  <ul className="mt-1 list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
                    {section.items?.map((item) => (
                      <li key={item}>{item}</li>
                    ))}
                  </ul>
                )}
              </div>
            ))}
          </div>
        </details>
      )}
      <div className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
        {(plan.materials?.length ?? 0) > 0 && (
          <InsightList title={t("experiment.materials")} items={plan.materials ?? []} icon={Atom} />
        )}
        {variables.length > 0 && (
          <InsightList title={t("experiment.variables")} items={variables} />
        )}
        {paramChips.length > 0 && (
          <InsightList title={t("experiment.processParams")} items={paramChips} icon={Factory} />
        )}
        {charMethods.length > 0 && (
          <InsightList title={t("experiment.charMethods")} items={charMethods} icon={Microscope} />
        )}
        {testMethods.length > 0 && (
          <InsightList title={t("experiment.testMethods")} items={testMethods} icon={Beaker} />
        )}
        {legacyMethods.length > 0 && (
          <InsightList title={t("experiment.methods")} items={legacyMethods} icon={Beaker} />
        )}
        {(plan.controls?.length ?? 0) > 0 && (
          <InsightList
            title={t("experiment.controls")}
            items={plan.controls ?? []}
            icon={ShieldCheck}
          />
        )}
      </div>
      {(criteria.length > 0 || (plan.risks?.length ?? 0) > 0) && (
        <div className="grid gap-2 sm:grid-cols-2">
          {criteria.length > 0 && (
            <div className="flex items-start gap-2 rounded-md border border-transparent bg-ok-wash px-3 py-2">
              <TrendingUp className="mt-0.5 size-4 shrink-0 text-ok" aria-hidden />
              <div className="min-w-0">
                <p className="text-xs font-medium text-ok">{t("experiment.successCriterion")}</p>
                {criteria.length === 1 ? (
                  <p className="text-sm text-ok">{criteria[0]}</p>
                ) : (
                  <ul className="list-disc space-y-0.5 pl-4 text-sm text-ok">
                    {criteria.map((c) => (
                      <li key={c}>{c}</li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          )}
          {(plan.risks?.length ?? 0) > 0 && (
            <div className="rounded-md border border-transparent bg-warn-wash px-3 py-2">
              <p className="flex items-center gap-1.5 text-xs font-medium text-warn">
                <TriangleAlert className="size-3.5" aria-hidden />
                {t("experiment.risks")}
              </p>
              <ul className="mt-1 list-disc space-y-0.5 pl-4 text-sm text-warn">
                {plan.risks?.map((r) => (
                  <li key={r}>{r}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
      {!showRoadmap && ((plan.estimated_cost ?? "") !== "" || horizon !== "") && (
        <div className="flex flex-wrap gap-1.5">
          {(plan.estimated_cost ?? "") !== "" && (
            <span className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-xs">
              <Coins className="size-3 text-muted-foreground" aria-hidden />
              {t("experiment.costEstimate", { value: costLabel(t, plan.estimated_cost) })}
            </span>
          )}
          {horizon !== "" && (
            <span className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-xs">
              <Clock className="size-3 text-muted-foreground" aria-hidden />
              {t("experiment.timeEstimate", { value: timeLabel(t, horizon) })}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

type PlanSection = NonNullable<
  NonNullable<HypothesisDetail["experiment_plan"]>["sections"]
>[number];

// Ключ этапа плана — из его содержимого: стабильного id у этапов нет.
function sectionKey(s: PlanSection): string {
  return `${s.title ?? ""}|${s.purpose ?? ""}|${s.items?.[0] ?? ""}`;
}

// Cost/time buckets → status accents: low/days = ok, medium/weeks = warn, high/months = risk.
const BUCKET_ACCENT: Record<string, string> = {
  low: "border-transparent bg-ok-wash text-ok",
  days: "border-transparent bg-ok-wash text-ok",
  medium: "border-transparent bg-warn-wash text-warn",
  weeks: "border-transparent bg-warn-wash text-warn",
  high: "border-transparent bg-risk-wash text-risk",
  months: "border-transparent bg-risk-wash text-risk",
};

function ResourceChip({
  icon: Icon,
  text,
  accent,
}: {
  icon: LucideIcon;
  text: string;
  accent?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs",
        accent ?? "bg-muted/40 text-muted-foreground",
      )}
    >
      <Icon className="size-3" aria-hidden />
      {text}
    </span>
  );
}

/** Visual verification roadmap: numbered stages from plan sections joined by
 *  connector lines (horizontal on desktop, vertical rail on mobile), a finish
 *  flag with the success criterion, and a compact resource strip (cost / time /
 *  materials / methods counts). */
function PlanRoadmap({
  sections,
  criteria,
  cost,
  time,
  materialsCount,
  methodsCount,
}: {
  sections: PlanSection[];
  criteria: string[];
  cost: string;
  time: string;
  materialsCount: number;
  methodsCount: number;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const hasFinish = criteria.length > 0;
  return (
    <div className="rounded-lg border bg-card px-4 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5">
        <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("roadmap.title")}
        </p>
        <div className="flex flex-wrap gap-1.5">
          {cost !== "" && (
            <ResourceChip
              icon={Coins}
              text={t("roadmap.cost", { value: costLabel(t, cost) })}
              accent={BUCKET_ACCENT[cost.trim().toLowerCase()]}
            />
          )}
          {time !== "" && (
            <ResourceChip
              icon={Clock}
              text={t("roadmap.time", { value: timeLabel(t, time) })}
              accent={BUCKET_ACCENT[time.trim().toLowerCase()]}
            />
          )}
          {materialsCount > 0 && (
            <ResourceChip icon={Atom} text={t("roadmap.materials", { count: materialsCount })} />
          )}
          {methodsCount > 0 && (
            <ResourceChip icon={Microscope} text={t("roadmap.methods", { count: methodsCount })} />
          )}
        </div>
      </div>
      <ol className="mt-4 flex flex-col md:flex-row md:flex-wrap md:gap-y-5">
        {sections.map((section, i) => {
          const items = section.items ?? [];
          const isLastNode = !hasFinish && i === sections.length - 1;
          return (
            <li
              key={sectionKey(section)}
              className="flex gap-3 md:block md:min-w-44 md:flex-1 md:gap-0 md:pr-3"
            >
              <div className="flex flex-col items-center md:flex-row">
                <span className="flex size-6 shrink-0 items-center justify-center rounded-full border border-brand-border bg-brand-wash font-mono text-[11px] font-semibold text-brand">
                  {i + 1}
                </span>
                <span
                  className={cn(
                    "my-1 w-px flex-1 bg-border md:mx-2 md:my-0 md:h-px md:w-auto",
                    isLastNode && "invisible",
                  )}
                  aria-hidden
                />
              </div>
              <div className="min-w-0 flex-1 pb-4 md:flex-none md:pb-0 md:pt-2">
                <p className="break-words text-sm font-medium leading-snug">
                  {section.title || t("experiment.stage", { n: i + 1 })}
                </p>
                {section.purpose && (
                  <p className="mt-0.5 line-clamp-2 text-xs leading-snug text-muted-foreground">
                    {section.purpose}
                  </p>
                )}
                {items.length > 0 && (
                  <p className="mt-1.5 flex flex-wrap items-center gap-1">
                    {items.slice(0, 2).map((item) => (
                      <span
                        key={item}
                        className="max-w-full truncate rounded-md border bg-muted/40 px-1.5 py-0.5 text-[11px] text-muted-foreground"
                      >
                        {item}
                      </span>
                    ))}
                    {items.length > 2 && (
                      <span className="text-[11px] text-muted-foreground">+{items.length - 2}</span>
                    )}
                  </p>
                )}
              </div>
            </li>
          );
        })}
        {hasFinish && (
          <li className="flex gap-3 md:block md:min-w-44 md:flex-1 md:gap-0">
            <div className="flex flex-col items-center md:flex-row">
              <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-ok-wash text-ok">
                <Flag className="size-3.5" aria-hidden />
              </span>
            </div>
            <div className="min-w-0 flex-1 md:flex-none md:pt-2">
              <p className="text-sm font-medium leading-snug text-ok">
                {t("experiment.successCriterion")}
              </p>
              <p className="mt-0.5 line-clamp-3 text-xs leading-snug text-muted-foreground">
                {criteria[0]}
                {criteria.length > 1 &&
                  ` · ${t("roadmap.moreCriteria", { count: criteria.length - 1 })}`}
              </p>
            </div>
          </li>
        )}
      </ol>
    </div>
  );
}

// The structured planner may emit coded cost/time buckets; show plain wording.
const COST_KEYS: Record<
  string,
  "experiment.cost.low" | "experiment.cost.medium" | "experiment.cost.high"
> = {
  low: "experiment.cost.low",
  medium: "experiment.cost.medium",
  high: "experiment.cost.high",
};
const TIME_KEYS: Record<
  string,
  "experiment.time.days" | "experiment.time.weeks" | "experiment.time.months"
> = {
  days: "experiment.time.days",
  weeks: "experiment.time.weeks",
  months: "experiment.time.months",
};
const EXPERIMENT_TYPE_KEYS: Record<
  string,
  | "experiment.type.new_alloy"
  | "experiment.type.process_route"
  | "experiment.type.coating_corrosion"
  | "experiment.type.battery_material"
  | "experiment.type.ore_beneficiation"
  | "experiment.type.metallurgy_process"
  | "experiment.type.generic"
> = {
  new_alloy: "experiment.type.new_alloy",
  process_route: "experiment.type.process_route",
  coating_corrosion: "experiment.type.coating_corrosion",
  battery_material: "experiment.type.battery_material",
  ore_beneficiation: "experiment.type.ore_beneficiation",
  metallurgy_process: "experiment.type.metallurgy_process",
  generic: "experiment.type.generic",
};
function costLabel(t: Translate, value: string | undefined): string {
  const v = (value ?? "").trim();
  const key = COST_KEYS[v.toLowerCase()];
  return key ? t(key) : v;
}
function timeLabel(t: Translate, value: string): string {
  const v = value.trim();
  const key = TIME_KEYS[v.toLowerCase()];
  return key ? t(key) : v;
}
function experimentTypeLabel(t: Translate, value: string | undefined): string {
  const v = (value ?? "").trim();
  if (v === "") return "";
  const key = EXPERIMENT_TYPE_KEYS[v.toLowerCase()];
  return key ? t(key) : v;
}

function InsightList({
  title,
  items,
  icon: Icon,
}: {
  title: string;
  items: string[];
  icon?: LucideIcon;
}) {
  return (
    <div>
      <p className="text-xs font-medium uppercase text-muted-foreground">{title}</p>
      <div className="mt-1 flex flex-wrap gap-1.5">
        {items.map((item) => (
          <span
            key={item}
            className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-xs"
          >
            {Icon && <Icon className="size-3" aria-hidden />}
            {item}
          </span>
        ))}
      </div>
    </div>
  );
}

const RISK_KEYS: Record<string, "feasibility.low" | "feasibility.medium" | "feasibility.high"> = {
  low: "feasibility.low",
  medium: "feasibility.medium",
  high: "feasibility.high",
};
const RISK_ACCENT: Record<string, string> = {
  low: "bg-ok-wash text-ok border-transparent",
  medium: "bg-warn-wash text-warn border-transparent",
  high: "bg-risk-wash text-risk border-transparent",
};
const RISK_EDGE: Record<string, string> = {
  low: "border-l-ok",
  medium: "border-l-warn",
  high: "border-l-risk",
};

export function FeasibilityCheck({
  items,
}: {
  items: NonNullable<HypothesisDetail["feasibility"]>;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const clean = items.filter((f) => (f.aspect ?? "") !== "" || (f.note ?? "") !== "");
  if (clean.length === 0) {
    return null;
  }
  return (
    <ul className="grid gap-2 sm:grid-cols-2">
      {clean.map((f) => {
        const levelKey = RISK_KEYS[f.level ?? ""];
        return (
          <li
            key={`${f.aspect ?? ""}-${f.note ?? ""}`}
            className={cn(
              "rounded-lg border border-l-4 p-3",
              RISK_EDGE[f.level ?? ""] ?? "border-l-border",
            )}
          >
            <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
              <p className="text-sm font-medium">{f.aspect}</p>
              {levelKey && (
                <span
                  className={cn(
                    "rounded-md border px-1.5 py-0.5 text-xs font-medium",
                    RISK_ACCENT[f.level ?? ""],
                  )}
                >
                  {t("feasibility.risk", { level: t(levelKey) })}
                </span>
              )}
            </div>
            {f.note && <p className="mt-1 text-sm text-muted-foreground">{f.note}</p>}
          </li>
        );
      })}
    </ul>
  );
}
