// Печатный бизнес-отчёт по портфелю: цели с базовым→целевым значением и
// ранжированные гипотезы. Свёрстан под A4 — кнопка «Сохранить в PDF» открывает
// системный диалог печати, обвязка приложения на печати скрывается. Данные и
// структура разделов общие с Word-экспортом (report/reportData.ts).
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowLeft, FileText, Printer } from "lucide-react";
import { toast } from "sonner";

import {
  displayHypothesisTitle,
  pct,
  priorityScore,
  statusMeta,
  successCriteriaLines,
} from "@/features/hypothesis/model";
import { verdictMeta } from "@/features/hypothesis/board/model";
import {
  itcBandLine,
  itcComponents,
  kpiTargetLabel,
  loadReportData,
  type ReportData,
} from "@/features/hypothesis/report/reportData";
import { Button } from "@/shared/ui/Button";
import { ErrorState } from "@/shared/ui/ErrorState";
import { Skeleton } from "@/shared/ui/Skeleton";
import { formatDate } from "@/shared/lib/format";

type Phase = "loading" | "error" | "ready";

export function HypothesisReport() {
  const { t } = useTranslation("hypothesis");
  const [phase, setPhase] = useState<Phase>("loading");
  const [data, setData] = useState<ReportData | null>(null);
  const [savingDocx, setSavingDocx] = useState(false);

  const load = useCallback(async () => {
    setPhase("loading");
    try {
      setData(await loadReportData());
      setPhase("ready");
    } catch {
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const saveDocx = async () => {
    setSavingDocx(true);
    try {
      const { downloadReportDocx } = await import("@/features/hypothesis/report/reportDocx");
      await downloadReportDocx();
    } catch {
      toast.error(t("report.docxError"));
    } finally {
      setSavingDocx(false);
    }
  };

  if (phase === "error") {
    return <ErrorState message={t("report.loadError")} onRetry={() => void load()} />;
  }
  if (phase === "loading" || data === null) {
    return <Skeleton className="mx-auto mt-10 h-[600px] w-full max-w-4xl rounded-xl" />;
  }

  const { ranked, shown, approvedTotal, kpis, kpiRows, execThemes, passports, pilots, provenance } =
    data;
  const today = formatDate(new Date().toISOString());

  let sectionCount = 0;
  const sections = {
    exec: execThemes.length > 0 ? ++sectionCount : 0,
    goals: kpiRows.length > 0 ? ++sectionCount : 0,
    ranking: ++sectionCount,
    passports: passports.length > 0 ? ++sectionCount : 0,
    pilots: pilots.length > 0 ? ++sectionCount : 0,
  };

  return (
    <div className="mx-auto w-full max-w-4xl px-4 py-6 md:py-8 print:max-w-none print:p-0">
      <style>{`@media print { @page { size: A4; margin: 14mm; } tr { break-inside: avoid; } }`}</style>

      <div className="mb-6 flex items-center justify-between gap-3 print:hidden">
        <Button variant="ghost" size="sm" asChild>
          <Link to="/hypotheses">
            <ArrowLeft className="size-4" aria-hidden />
            {t("report.back")}
          </Link>
        </Button>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void saveDocx()} disabled={savingDocx}>
            <FileText className="size-4" aria-hidden />
            {savingDocx ? t("report.savingDocx") : t("report.saveDocx")}
          </Button>
          <Button variant="brand" size="sm" onClick={() => window.print()}>
            <Printer className="size-4" aria-hidden />
            {t("report.savePdf")}
          </Button>
        </div>
      </div>

      <header className="border-b pb-5">
        <p className="kicker text-muted-foreground">{t("report.kicker", { date: today })}</p>
        <h1 className="font-display mt-1 text-3xl">{t("report.title")}</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          {t("report.summary", {
            total: ranked.length,
            approved: approvedTotal,
            goals: kpis.length,
          })}
        </p>
      </header>

      {execThemes.length > 0 && (
        <section className="mt-7">
          <h2 className="font-display text-xl">{`${sections.exec}. ${t("report.execTitle")}`}</h2>
          <p className="mt-1 text-xs text-muted-foreground">{t("report.execNote")}</p>
          <ul className="mt-3 space-y-2.5 text-sm">
            {execThemes.map(({ cluster, itc, linked }) => (
              <li key={cluster.id} className="flex gap-2">
                <span aria-hidden className="text-muted-foreground">
                  •
                </span>
                <div>
                  <p>
                    <span className="font-medium">{cluster.label}</span>
                    <span className="text-muted-foreground">
                      {" — "}
                      {t("report.execScore", {
                        techscore: itc.techscore.toFixed(2),
                        score: itc.score,
                      })}
                      {itc.band?.label ? ` · ${itc.band.label}` : ""}
                    </span>
                  </p>
                  {(cluster.summary || cluster.keywords.length > 0) && (
                    <p className="text-xs text-muted-foreground">
                      {cluster.summary || cluster.keywords.slice(0, 6).join(", ")}
                    </p>
                  )}
                  <p className="text-xs text-muted-foreground">
                    {t("report.execCounts", { docs: cluster.document_count, linked })}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      {kpiRows.length > 0 && (
        <section className="mt-7">
          <h2 className="font-display text-xl">{`${sections.goals}. ${t("report.goalsTitle")}`}</h2>
          <table className="mt-3 w-full border-collapse text-sm">
            <thead>
              <tr className="border-b text-left text-xs uppercase tracking-wide text-muted-foreground">
                <th className="py-2 pr-3 font-medium">{t("report.goalsHead.goal")}</th>
                <th className="py-2 pr-3 font-medium">{t("report.goalsHead.target")}</th>
                <th className="py-2 pr-3 text-right font-medium">
                  {t("report.goalsHead.hypotheses")}
                </th>
                <th className="py-2 pr-3 text-right font-medium">
                  {t("report.goalsHead.approved")}
                </th>
                <th className="py-2 text-right font-medium">{t("report.goalsHead.best")}</th>
              </tr>
            </thead>
            <tbody>
              {kpiRows.map(({ kpi, total, approved, best }) => (
                <tr key={kpi.id} className="border-b align-top">
                  <td className="py-2.5 pr-3">
                    <p className="font-medium">{kpi.title}</p>
                    {kpi.metric !== "" && (
                      <p className="text-xs text-muted-foreground">{kpi.metric}</p>
                    )}
                  </td>
                  <td className="py-2.5 pr-3 tabular-nums">{kpiTargetLabel(kpi)}</td>
                  <td className="py-2.5 pr-3 text-right tabular-nums">{total}</td>
                  <td className="py-2.5 pr-3 text-right tabular-nums">{approved}</td>
                  <td className="py-2.5 text-right tabular-nums">
                    {best === null ? "—" : `${best}/100`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <section className="mt-8">
        <h2 className="font-display text-xl">{`${sections.ranking}. ${t("report.rankingTitle")}`}</h2>
        <p className="mt-1 text-xs text-muted-foreground">{t("report.rankingNote")}</p>
        <table className="mt-3 w-full border-collapse text-sm">
          <thead>
            <tr className="border-b text-left text-xs uppercase tracking-wide text-muted-foreground">
              <th className="py-2 pr-2 font-medium">{t("report.rankHead.num")}</th>
              <th className="py-2 pr-3 font-medium">{t("report.rankHead.hypothesis")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("report.rankHead.priority")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("report.rankHead.novelty")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("report.rankHead.value")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("report.rankHead.risk")}</th>
              <th className="py-2 pr-3 text-right font-medium">{t("report.rankHead.trl")}</th>
              <th className="py-2 font-medium">{t("report.rankHead.status")}</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((h, i) => {
              const verdict = verdictMeta(h);
              return (
                <tr key={h.id} className="border-b align-top">
                  <td className="py-2.5 pr-2 tabular-nums text-muted-foreground">{i + 1}</td>
                  <td className="py-2.5 pr-3">
                    <p className="font-medium leading-snug">{displayHypothesisTitle(h)}</p>
                    {verdict && <p className="text-xs text-muted-foreground">{verdict.label}</p>}
                  </td>
                  <td className="py-2.5 pr-3 text-right font-medium tabular-nums">
                    {priorityScore(h) ?? "—"}
                  </td>
                  <td className="py-2.5 pr-3 text-right tabular-nums">{pct(h.novelty_score)}</td>
                  <td className="py-2.5 pr-3 text-right tabular-nums">{pct(h.value_score)}</td>
                  <td className="py-2.5 pr-3 text-right tabular-nums">{pct(h.risk_score)}</td>
                  <td className="py-2.5 pr-3 text-right tabular-nums">{h.trl ?? "—"}</td>
                  <td className="py-2.5 whitespace-nowrap">{statusMeta(h.status).label}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {ranked.length > shown.length && (
          <p className="mt-2 text-xs text-muted-foreground">
            {t("report.truncated", { shown: shown.length, total: ranked.length })}
          </p>
        )}
      </section>

      {passports.length > 0 && (
        <section className="mt-8">
          <h2 className="font-display text-xl">{`${sections.passports}. ${t("report.passportsTitle")}`}</h2>
          <div className="mt-3 space-y-5">
            {passports.map(({ cluster, itc, linked }) => (
              <div
                key={cluster.id}
                className="rounded-lg border p-4"
                style={{ breakInside: "avoid" }}
              >
                <div className="flex items-baseline justify-between gap-3">
                  <h3 className="font-medium">{cluster.label}</h3>
                  <span className="whitespace-nowrap text-sm font-medium tabular-nums">
                    {t("report.passportScore", { score: itc.score })}
                  </span>
                </div>
                {itcBandLine(itc) !== "" && (
                  <p className="mt-0.5 text-xs text-muted-foreground">{itcBandLine(itc)}</p>
                )}
                {cluster.summary && <p className="mt-2 text-sm">{cluster.summary}</p>}
                {itcComponents(itc).length > 0 && (
                  <ul className="mt-2 space-y-1 text-xs text-muted-foreground">
                    {itcComponents(itc).map((c) => (
                      <li key={c.key}>
                        <span className="font-medium text-foreground">{c.name}</span>
                        {` (${Math.round(c.norm * 100)})`}
                        {c.note ? `: ${c.note}` : ""}
                      </li>
                    ))}
                  </ul>
                )}
                <p className="mt-2 text-xs text-muted-foreground">
                  {t("report.passportSignals", {
                    pubs: itc.signals?.pub_count ?? cluster.document_count,
                    orgs: itc.signals?.org_count ?? 0,
                    linked,
                  })}
                </p>
              </div>
            ))}
          </div>
        </section>
      )}

      {pilots.length > 0 && (
        <section className="mt-8">
          <h2 className="font-display text-xl">{`${sections.pilots}. ${t("report.pilotsTitle")}`}</h2>
          <p className="mt-1 text-xs text-muted-foreground">{t("report.pilotsNote")}</p>
          <ol className="mt-3 space-y-4 text-sm">
            {pilots.map((h, i) => {
              const criteria = successCriteriaLines(h.detail?.experiment_plan?.success_criteria);
              const prov = provenance[h.id];
              return (
                <li key={h.id} className="flex gap-2" style={{ breakInside: "avoid" }}>
                  <span className="tabular-nums text-muted-foreground">
                    {sections.pilots}.{i + 1}.
                  </span>
                  <div>
                    <p className="font-medium">{displayHypothesisTitle(h)}</p>
                    <p className="mt-0.5">{h.statement}</p>
                    {h.rationale !== "" && (
                      <p className="mt-1">
                        <span className="font-medium">{t("report.pilotRationale")}. </span>
                        {h.rationale}
                      </p>
                    )}
                    {prov?.constraints && (
                      <p className="mt-1 text-xs text-muted-foreground">
                        {t("report.pilotConstraints")}: {prov.constraints}
                      </p>
                    )}
                    {criteria.length > 0 && (
                      <p className="mt-0.5 text-xs text-muted-foreground">
                        {t("report.pilotCriteria")}: {criteria.join("; ")}
                      </p>
                    )}
                    {prov !== undefined && prov.works.length > 0 && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        <p className="font-medium text-foreground">{t("report.pilotSources")}</p>
                        <ul className="mt-0.5 space-y-0.5">
                          {prov.works.map((w) => (
                            <li key={w.title}>
                              {w.doi ? (
                                <a
                                  href={`https://doi.org/${w.doi}`}
                                  target="_blank"
                                  rel="noreferrer"
                                  className="text-brand hover:underline"
                                >
                                  {w.title}
                                </a>
                              ) : (
                                w.title
                              )}
                              {w.venue ? `, ${w.venue}` : ""}
                              {w.year ? ` (${w.year})` : ""}
                              {w.doi ? ` — doi:${w.doi}` : ""}
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                  </div>
                </li>
              );
            })}
          </ol>
        </section>
      )}

      <footer className="mt-8 border-t pt-3 text-xs text-muted-foreground">
        {t("report.footer")}
      </footer>
    </div>
  );
}
