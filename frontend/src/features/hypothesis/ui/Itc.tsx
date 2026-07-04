// Shared presentation for the technology index (ITC) — the deterministic,
// corpus-derived technology index. Plain-language labels for a non-technical
// audience; the raw component keys (SM/NV/IP/HR) never reach the screen.
import { useTranslation } from "react-i18next";

import { type Itc, type ItcComponent } from "@/features/hypothesis/api";
import { cn } from "@/shared/lib/cn";
import { GLOSSARY } from "@/shared/glossary";
import { InfoHint } from "@/shared/ui/InfoHint";

/** Band → colour. 1–2 grey, 3–4 ochre, 5–6 ultramarine, 7–10 viridian. */
function itcBandMeta(score: number | null | undefined): {
  pill: string;
  ring: string;
} {
  if (score === null || score === undefined)
    return { pill: "bg-muted text-muted-foreground", ring: "border-muted" };
  if (score <= 2) return { pill: "bg-secondary text-secondary-foreground", ring: "border-border" };
  if (score <= 4) return { pill: "bg-warn-wash text-warn", ring: "border-warn/40" };
  if (score <= 6)
    return { pill: "bg-brand-wash text-accent-foreground", ring: "border-brand-border" };
  if (score <= 8) return { pill: "bg-ok-wash text-ok", ring: "border-ok/40" };
  return { pill: "bg-ok-wash text-ok", ring: "border-ok/60" };
}

// Friendly labels for the non-technical audience (rubric keys → dictionary keys).
const COMP_LABEL_KEY: Record<
  string,
  "itc.comp.SM" | "itc.comp.NV" | "itc.comp.IP" | "itc.comp.HR"
> = {
  SM: "itc.comp.SM",
  NV: "itc.comp.NV",
  IP: "itc.comp.IP",
  HR: "itc.comp.HR",
};

function CompRow({ c }: { c: ItcComponent }) {
  const { t } = useTranslation("hypothesisDetail");
  const labelKey = COMP_LABEL_KEY[c.key];
  const v = Math.max(0, Math.min(100, Math.round((c.norm ?? 0) * 100)));
  return (
    <div title={c.note}>
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{labelKey ? t(labelKey) : c.name}</span>
        <span className="tabular-nums font-medium text-foreground">{v}</span>
      </div>
      <div className="mt-1 h-2 w-full overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-brand" style={{ width: `${v}%` }} />
      </div>
    </div>
  );
}

/** The visual summary at the top of a hypothesis / theme: big N/10, band, the
 *  four components and the bibliometric signals behind them. */
export function ItcSummary({
  itc,
  title,
  evidenceCitationLabel,
  bare = false,
}: {
  itc: Itc;
  title?: string;
  evidenceCitationLabel?: string;
  /** Без карточной обёртки — для встраивания в раскрытую панель скорборда. */
  bare?: boolean;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const heading = title ?? t("itc.title");
  const citationLabel = evidenceCitationLabel ?? t("itc.citationsHypotheses");
  const m = itcBandMeta(itc.score);
  const comps = [
    itc.components?.SM,
    itc.components?.NV,
    itc.components?.IP,
    itc.components?.HR,
  ].filter(Boolean) as ItcComponent[];
  const s = itc.signals ?? {};
  const yspan =
    s.year_min && s.year_max
      ? s.year_min === s.year_max
        ? `${s.year_min}`
        : `${s.year_min}–${s.year_max}`
      : null;
  return (
    <div className={cn(!bare && "rounded-xl border bg-card p-5")}>
      <div className="flex items-start gap-4">
        <div className="flex shrink-0 flex-col items-center">
          <div
            className={cn(
              "flex size-16 items-center justify-center rounded-full border-4 bg-background",
              m.ring,
            )}
          >
            <span className="text-2xl font-bold leading-none tabular-nums">{itc.score}</span>
          </div>
          <span className="mt-1 text-[11px] text-muted-foreground">{t("itc.outOf10")}</span>
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <h3 className="text-sm font-semibold">{heading}</h3>
            <InfoHint label={t("itc.whatIs")}>{GLOSSARY.itc}</InfoHint>
            {itc.band?.label && (
              <span className={cn("rounded-full px-2 py-0.5 text-xs font-medium", m.pill)}>
                {itc.band.label}
              </span>
            )}
          </div>
          {itc.band?.note && (
            <p className="mt-0.5 text-xs text-muted-foreground">{itc.band.note}</p>
          )}
          {/* Position on the 1–10 scale: grey → ochre → ultramarine → viridian. */}
          <div className="relative mt-2.5">
            <div className="flex h-1.5 gap-0.5">
              <div className="flex-1 rounded-l-full bg-border" />
              <div className="flex-1 bg-warn/50" />
              <div className="flex-1 bg-brand/40" />
              <div className="flex-1 bg-ok/50" />
              <div className="flex-1 rounded-r-full bg-ok" />
            </div>
            <div
              className="absolute top-1/2 size-3 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-background bg-foreground shadow-sm"
              style={{ left: `${Math.max(2, Math.min(98, itc.score * 10 - 5))}%` }}
              aria-hidden
            />
          </div>
          <div className="mt-3 space-y-2.5">
            {comps.map((c) => (
              <CompRow key={c.key} c={c} />
            ))}
          </div>
        </div>
      </div>
      <p className="mt-3 border-t pt-2 text-[11px] text-muted-foreground">
        {t("itc.computedPrefix")}{" "}
        {[
          yspan ? t("itc.pubYears", { span: yspan }) : null,
          s.pub_count ? t("itc.pubCount", { count: s.pub_count }) : null,
          s.evidence_citations ? `${citationLabel}: ${s.evidence_citations}` : null,
        ]
          .filter(Boolean)
          .join(" · ") || t("itc.deterministic")}
      </p>
    </div>
  );
}
