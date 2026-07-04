import { useCallback, useEffect, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Download, FileText, Printer } from "lucide-react";
import { toast } from "sonner";

import {
  getHypothesis,
  listHypothesisRevisions,
  type ApiHypothesis,
} from "@/features/hypothesis/api";
import { listClusters } from "@/features/cluster";
import { listKPIs, type ApiKPI } from "@/features/kpi";
import { HypothesisDetailPanel } from "@/features/hypothesis/ui/HypothesisDetailPanel";
import { DirectionDetailPanel } from "@/features/hypothesis/ui/DirectionDetailPanel";
import { cleanInternalTitle, isResearchDirection } from "@/features/hypothesis/model";
import { useOwnerNames } from "@/features/hypothesis/owners";
import { buildHypothesisReport } from "@/features/hypothesis/report/hypothesisReport";
import { Button } from "@/shared/ui/Button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/shared/ui/DropdownMenu";
import { Spinner } from "@/shared/ui/Spinner";
import { ErrorState } from "@/shared/ui/ErrorState";

type Phase = "loading" | "ready" | "error";

// Фирменный синий Microsoft Word — узнаваемость формата в меню экспорта.
const WORD_BLUE = "#2B579A";

export function HypothesisPage() {
  const { t } = useTranslation("hypothesisDetail");
  const { id = "" } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const isDirectionRoute = location.pathname.startsWith("/directions/");

  const [phase, setPhase] = useState<Phase>("loading");
  const [hyp, setHyp] = useState<ApiHypothesis | null>(null);
  const [kpi, setKpi] = useState<ApiKPI | undefined>(undefined);
  const [clusterTitle, setClusterTitle] = useState<string | undefined>(undefined);
  const ownerName = useOwnerNames();

  const load = useCallback(
    async (silent = false) => {
      if (!silent) {
        setPhase("loading");
      }
      try {
        // KPIs and themes are small lookup tables; load them alongside the
        // hypothesis so the panel can show the Цель ▸ Тема chain without extra
        // state plumbing.
        const [h, kpis, clusters] = await Promise.all([
          getHypothesis(id),
          listKPIs(),
          listClusters(),
        ]);
        setHyp(h);
        setKpi(h.kpi_id ? kpis.find((k) => k.id === h.kpi_id) : undefined);
        setClusterTitle(
          h.primary_cluster_id
            ? cleanInternalTitle(clusters.find((c) => c.id === h.primary_cluster_id)?.label ?? "")
            : undefined,
        );
        setPhase("ready");
      } catch {
        if (!silent) {
          setPhase("error");
        }
      }
    },
    [id],
  );

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const needsAutoProcessing = hyp && (hyp.trl === null || !hyp.assessment?.check?.verdict);
    if (phase !== "ready" || !hyp || !needsAutoProcessing || isResearchDirection(hyp)) return;
    const intervalId = window.setInterval(() => void load(true), 12_000);
    return () => window.clearInterval(intervalId);
  }, [hyp, load, phase]);

  const exportWord = () => {
    if (!hyp) return;
    const job = (async () => {
      const revisions = await listHypothesisRevisions(hyp.id).catch(() => []);
      const { downloadHypothesisReportDocx } =
        await import("@/features/hypothesis/report/hypothesisDocx");
      await downloadHypothesisReportDocx(
        buildHypothesisReport({
          h: hyp,
          kpi,
          clusterTitle: clusterTitle || undefined,
          ownerName: ownerName(hyp.owner_id),
          revisions,
        }),
      );
    })();
    toast.promise(job, {
      loading: t("export.loading"),
      success: t("export.ready"),
      error: t("export.error"),
    });
  };

  const showExport = phase === "ready" && hyp !== null && !isResearchDirection(hyp);

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <div className="mb-4 flex items-center justify-between gap-3">
        <Button variant="ghost" size="sm" className="-ml-2" onClick={() => navigate(-1)}>
          <ArrowLeft className="size-4" aria-hidden />
          {t("actions.back")}
        </Button>
        {showExport && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm">
                <Download className="size-4" aria-hidden />
                {t("export.menu")}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={exportWord}>
                <FileText style={{ color: WORD_BLUE }} aria-hidden />
                {t("export.word")}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => navigate(`/hypotheses/${id}/report`)}>
                <Printer className="text-muted-foreground" aria-hidden />
                {t("export.pdf")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>

      {phase === "loading" && (
        <div className="flex justify-center py-14">
          <Spinner
            size="lg"
            label={t(isDirectionRoute ? "page.loadingDirection" : "page.loadingHypothesis")}
          />
        </div>
      )}

      {phase === "error" && (
        <ErrorState
          message={t(isDirectionRoute ? "page.errorDirection" : "page.errorHypothesis")}
          onRetry={() => void load()}
        />
      )}

      {phase === "ready" && hyp && isResearchDirection(hyp) && (
        <DirectionDetailPanel direction={hyp} clusterTitle={clusterTitle} />
      )}

      {phase === "ready" && hyp && !isResearchDirection(hyp) && (
        <HypothesisDetailPanel
          hypothesis={hyp}
          kpiTitle={kpi?.title}
          clusterTitle={clusterTitle}
          onChanged={setHyp}
        />
      )}
    </div>
  );
}
