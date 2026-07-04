// Печатный отчёт по одной гипотезе (/hypotheses/:id/report): те же блоки, что
// и Word-экспорт (report/hypothesisReport.ts), свёрстанные под A4 в серифном
// «гостовском» начертании. «Сохранить в PDF» — системный диалог печати.
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowLeft, FileText, Printer } from "lucide-react";
import { toast } from "sonner";

import {
  getHypothesis,
  listHypothesisRevisions,
  type ApiHypothesis,
  type ApiRevision,
} from "@/features/hypothesis/api";
import { listClusters } from "@/features/cluster";
import { listKPIs, type ApiKPI } from "@/features/kpi";
import { cleanInternalTitle } from "@/features/hypothesis/model";
import { useOwnerNames } from "@/features/hypothesis/owners";
import {
  buildHypothesisReport,
  type ReportBlock,
} from "@/features/hypothesis/report/hypothesisReport";
import { Button } from "@/shared/ui/Button";
import { ErrorState } from "@/shared/ui/ErrorState";
import { Skeleton } from "@/shared/ui/Skeleton";
import { cn } from "@/shared/lib/cn";

type Phase = "loading" | "error" | "ready";

const SERIF = { fontFamily: '"Times New Roman", "PT Serif", serif' } as const;

export function HypothesisReportPage() {
  const { t } = useTranslation("hypothesis");
  const { id = "" } = useParams<{ id: string }>();
  const [phase, setPhase] = useState<Phase>("loading");
  const [hyp, setHyp] = useState<ApiHypothesis | null>(null);
  const [kpi, setKpi] = useState<ApiKPI | undefined>(undefined);
  const [clusterTitle, setClusterTitle] = useState<string | undefined>(undefined);
  const [revisions, setRevisions] = useState<ApiRevision[]>([]);
  const [savingDocx, setSavingDocx] = useState(false);
  const ownerName = useOwnerNames();

  const load = useCallback(async () => {
    setPhase("loading");
    try {
      const [h, kpis, clusters, revs] = await Promise.all([
        getHypothesis(id),
        listKPIs().catch(() => [] as ApiKPI[]),
        listClusters().catch(() => []),
        listHypothesisRevisions(id).catch(() => [] as ApiRevision[]),
      ]);
      setHyp(h);
      setKpi(h.kpi_id ? kpis.find((k) => k.id === h.kpi_id) : undefined);
      setClusterTitle(
        h.primary_cluster_id
          ? cleanInternalTitle(clusters.find((c) => c.id === h.primary_cluster_id)?.label ?? "") ||
              undefined
          : undefined,
      );
      setRevisions(revs);
      setPhase("ready");
    } catch {
      setPhase("error");
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const report = useMemo(
    () =>
      hyp
        ? buildHypothesisReport({
            h: hyp,
            kpi,
            clusterTitle,
            ownerName: ownerName(hyp.owner_id),
            revisions,
          })
        : null,
    [hyp, kpi, clusterTitle, ownerName, revisions],
  );

  const saveDocx = async () => {
    if (!report) return;
    setSavingDocx(true);
    try {
      const { downloadHypothesisReportDocx } =
        await import("@/features/hypothesis/report/hypothesisDocx");
      await downloadHypothesisReportDocx(report);
    } catch {
      toast.error(t("report.docxError"));
    } finally {
      setSavingDocx(false);
    }
  };

  if (phase === "error") {
    return <ErrorState message={t("report.loadError")} onRetry={() => void load()} />;
  }
  if (phase === "loading" || report === null) {
    return <Skeleton className="mx-auto mt-10 h-[600px] w-full max-w-3xl rounded-xl" />;
  }

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-6 md:py-8 print:max-w-none print:p-0">
      <style>{`@media print { @page { size: A4; margin: 20mm 15mm 20mm 30mm; } tr { break-inside: avoid; } }`}</style>

      <div className="mb-6 flex items-center justify-between gap-3 print:hidden">
        <Button variant="ghost" size="sm" asChild>
          <Link to={`/hypotheses/${id}`}>
            <ArrowLeft className="size-4" aria-hidden />
            {t("hreport.backToHypothesis")}
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

      <article style={SERIF} className="text-[13px] leading-relaxed text-foreground">
        {withKeys(report.blocks, (b) => b.kind).map(({ item, key }) => (
          <Block key={key} b={item} />
        ))}
      </article>
    </div>
  );
}

// Блоки отчёта статичны в пределах рендера: стабильный ключ = позиция+содержимое.
function withKeys<T>(items: T[], tag: (item: T) => string): { item: T; key: string }[] {
  return items.map((item, i) => ({ item, key: `${i}-${tag(item)}` }));
}

function Block({ b }: { b: ReportBlock }) {
  switch (b.kind) {
    case "doctitle":
      return (
        <header className="border-b pb-4 text-center">
          {b.org && <p className="text-sm">{b.org}</p>}
          <p className="mt-3 text-sm font-semibold uppercase tracking-wide">{b.doctype}</p>
          <h1 className="mt-2 text-2xl font-bold leading-snug">{b.title}</h1>
          <p className="mt-2 text-xs text-muted-foreground">{b.meta.join(" · ")}</p>
          {b.lines.map((line) => (
            <p key={line} className="mt-1 text-xs text-muted-foreground">
              {line}
            </p>
          ))}
        </header>
      );
    case "heading":
      return (
        <h2
          className={cn("mt-7 text-lg font-bold", b.caps && "text-center uppercase tracking-wide")}
          style={{ breakAfter: "avoid" }}
        >
          {b.text}
        </h2>
      );
    case "subheading":
      return (
        <h3 className="mt-4 text-[15px] font-bold" style={{ breakAfter: "avoid" }}>
          {b.text}
        </h3>
      );
    case "paragraph":
      return (
        <p
          className={cn(
            "mt-2 text-justify",
            b.muted && "text-left text-xs text-muted-foreground",
            b.italic && "italic",
            b.bold && "font-semibold",
          )}
          style={b.muted ? undefined : { textIndent: "1.25cm" }}
        >
          {b.text}
        </p>
      );
    case "kv":
      return (
        <table className="mt-3 w-full border-collapse border text-[12px]">
          <tbody>
            {b.rows.map((r) => (
              <tr key={r.label} className="border-b align-top">
                <td className="w-[32%] border-r bg-muted/40 px-2 py-1.5 font-semibold">
                  {r.label}
                </td>
                <td className="px-2 py-1.5">{r.value}</td>
              </tr>
            ))}
          </tbody>
        </table>
      );
    case "table":
      return (
        <div className="mt-3">
          {b.title && <p className="mb-1 text-[12px]">{b.title}</p>}
          <table className="w-full border-collapse border text-[12px]">
            <thead>
              <tr className="bg-muted/40">
                {b.head.map((hd) => (
                  <th key={hd} className="border px-2 py-1.5 text-left font-semibold">
                    {hd}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {withKeys(b.rows, (row) => row[0] ?? "").map(({ item: row, key }) => (
                <tr key={key} className="align-top">
                  {withKeys(row, (c) => c.slice(0, 12)).map(({ item: c, key: ck }) => (
                    <td key={ck} className="border px-2 py-1.5">
                      {c}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    case "list":
      return b.ordered ? (
        <ol className="mt-2 list-decimal space-y-1 pl-8">
          {withKeys(b.items, (item) => item.slice(0, 24)).map(({ item, key }) => (
            <li key={key}>{item}</li>
          ))}
        </ol>
      ) : (
        <ul className="mt-2 list-disc space-y-1 pl-8">
          {withKeys(b.items, (item) => item.slice(0, 24)).map(({ item, key }) => (
            <li key={key}>{item}</li>
          ))}
        </ul>
      );
    case "quote":
      return (
        <figure className="mt-3 pl-4" style={{ breakInside: "avoid" }}>
          <figcaption className="text-[12px] font-semibold">{b.source}</figcaption>
          <blockquote className="mt-0.5 italic">«{b.text}»</blockquote>
          {b.note && <p className="mt-0.5 text-xs text-muted-foreground">{b.note}</p>}
        </figure>
      );
    case "references":
      return (
        <ol className="mt-3 space-y-1.5 text-[12px]">
          {b.items.map((item) => (
            <li key={item.n} className="pl-8" style={{ textIndent: "-2rem" }}>
              {item.n}. {item.text}
              {item.url && (
                <>
                  {" "}
                  <a
                    href={item.url}
                    target="_blank"
                    rel="noreferrer"
                    className="text-brand hover:underline"
                  >
                    {item.url}
                  </a>
                </>
              )}
            </li>
          ))}
        </ol>
      );
  }
}
