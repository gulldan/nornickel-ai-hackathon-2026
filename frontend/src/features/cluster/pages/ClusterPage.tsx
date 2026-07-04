import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowLeft, FileText, Wand2 } from "lucide-react";

import { type ApiCluster, getCluster } from "@/features/cluster/api";
import {
  cleanInternalTitle,
  generateHypotheses,
  hasInternalNoise,
  ItcSummary,
} from "@/features/hypothesis";
import { DocPreviewSheet, useDocPreview } from "@/features/document";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { Card, CardButton } from "@/shared/ui/Card";
import { Kicker } from "@/shared/ui/Kicker";
import { RichText } from "@/shared/ui/RichText";
import { Section } from "@/shared/ui/Section";
import { InfoHint } from "@/shared/ui/InfoHint";
import { SegmentBar } from "@/shared/ui/SegmentBar";
import { Spinner } from "@/shared/ui/Spinner";
import { ErrorState } from "@/shared/ui/ErrorState";
import { useBackgroundTasks } from "@/features/task";
import { formatDateShort } from "@/shared/lib/format";
import { i18n } from "@/shared/i18n";
import {
  fetchHypothesisRuntimeSettings,
  loadHypothesisRuntimeSettings,
} from "@/shared/appSettings";

type Phase = "loading" | "ready" | "error";

type UnknownObj = Record<string, unknown>;

function isObj(v: unknown): v is UnknownObj {
  return typeof v === "object" && v !== null;
}

function numOf(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

/** Плотность темы: средняя смысловая близость её документов (params.metrics). */
function clusterDensity(c: ApiCluster): number | null {
  const metrics = c.params?.metrics;
  return isObj(metrics) ? numOf(metrics.avg_similarity) : null;
}

interface LineageInfo {
  text: string;
  jaccard: number | null;
}

/** История темы между обновлениями базы (params.lineage от graph-compute). */
function clusterLineage(c: ApiCluster): LineageInfo | null {
  const lineage = c.params?.lineage;
  if (!isObj(lineage)) return null;
  const jaccard = numOf(lineage.jaccard);
  const mergedFrom = Array.isArray(lineage.merged_from) ? lineage.merged_from.length : 0;
  const splitFrom = Array.isArray(lineage.split_from) ? lineage.split_from.length : 0;
  const prev = typeof lineage.previous_cluster_id === "string" && lineage.previous_cluster_id;
  if (mergedFrom >= 2) {
    return { text: i18n.t("cluster:lineage.merged", { count: mergedFrom }), jaccard };
  }
  if (splitFrom > 0) {
    return { text: i18n.t("cluster:lineage.split"), jaccard };
  }
  if (prev && jaccard !== null) {
    return {
      text:
        jaccard >= 0.995
          ? i18n.t("cluster:lineage.unchanged")
          : i18n.t("cluster:lineage.continues", { percent: Math.round(jaccard * 100) }),
      jaccard,
    };
  }
  return null;
}

export function ClusterPage() {
  const { id = "" } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { t } = useTranslation("cluster");

  const [phase, setPhase] = useState<Phase>("loading");
  const [cluster, setCluster] = useState<ApiCluster | null>(null);
  const [runtimeSettings, setRuntimeSettings] = useState(loadHypothesisRuntimeSettings);
  const mounted = useRef(true);
  const { runTask, isTaskRunning } = useBackgroundTasks();
  const { preview, openCitation, close: closePreview, retry: retryPreview } = useDocPreview();
  const genBusy = isTaskRunning("hypotheses:generate");
  const displayLabel = cluster ? cleanInternalTitle(cluster.label) || t("untitled") : "";
  const displaySummary =
    cluster && cluster.summary && !hasInternalNoise(cluster.summary) ? cluster.summary : "";
  const density = cluster ? clusterDensity(cluster) : null;
  const lineage = cluster ? clusterLineage(cluster) : null;

  useEffect(() => {
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    fetchHypothesisRuntimeSettings()
      .then((next) => {
        if (mounted.current) setRuntimeSettings(next);
      })
      .catch(() => setRuntimeSettings(loadHypothesisRuntimeSettings()));
  }, []);

  const load = useCallback(async () => {
    setPhase("loading");
    try {
      const found = await getCluster(id);
      setCluster(found);
      setPhase("ready");
    } catch {
      setCluster(null);
      setPhase("error");
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const generateFromCluster = useCallback(
    (c: ApiCluster) => {
      const label = cleanInternalTitle(c.label) || t("task.fallbackLabel");
      // Broaden retrieval with the cluster's summary + keywords so generated
      // hypotheses draw on the whole theme, not just its short label.
      const kpi_description = [c.summary, (c.keywords || []).join(", ")].filter(Boolean).join(". ");
      void runTask({
        key: "hypotheses:generate",
        title: t("task.title"),
        description: t("task.theme", { label }),
        successMessage: (created) => t("task.success", { label, count: created.length }),
        errorMessage: t("task.error"),
        run: () =>
          generateHypotheses({
            kpi_title: c.label,
            kpi_description,
            count: runtimeSettings.clusterGenerateCount,
          }),
        onSuccess: () => {
          if (mounted.current) navigate("/hypotheses");
        },
      });
    },
    [navigate, runTask, runtimeSettings.clusterGenerateCount, t],
  );

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-6 md:py-8">
      <Button variant="ghost" size="sm" className="-ml-2 mb-4" onClick={() => navigate(-1)}>
        <ArrowLeft className="size-4" aria-hidden />
        {t("back")}
      </Button>

      {phase === "loading" && (
        <div className="flex justify-center py-14">
          <Spinner size="lg" label={t("loading")} />
        </div>
      )}

      {phase === "error" && (
        <ErrorState
          message={cluster === null ? t("errorNotFound") : t("errorLoad")}
          onRetry={() => void load()}
        />
      )}

      {phase === "ready" && cluster && (
        <div className="space-y-5 pb-10">
          <div className="space-y-2">
            <Kicker className="text-brand">{t("kicker")}</Kicker>
            <h1 className="font-display text-balance text-[1.75rem] leading-tight">
              {displayLabel}
            </h1>
            <p className="font-mono text-xs text-muted-foreground">
              {t("meta.documents", { count: cluster.document_count })} ·{" "}
              {t("meta.fragments", { count: cluster.chunk_count })} ·{" "}
              {t("meta.updated", { date: formatDateShort(cluster.updated_at) })}
            </p>
          </div>

          {(density !== null || lineage) && (
            <Card className="space-y-2.5 p-4">
              {density !== null && (
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <span className="inline-flex items-center gap-1">
                    {t("density.label")}
                    <InfoHint>{t("density.hint")}</InfoHint>
                  </span>
                  <SegmentBar
                    value={Math.round(density * 9)}
                    tone={density >= 0.6 ? "ok" : density >= 0.4 ? "brand" : "warn"}
                    label={t("density.bar", { percent: Math.round(density * 100) })}
                  />
                  <span className="font-mono text-xs text-muted-foreground">
                    {Math.round(density * 100)} %
                  </span>
                </div>
              )}
              {lineage && <p className="text-sm text-muted-foreground">{lineage.text}</p>}
            </Card>
          )}

          {cluster.params?.itc && (
            <ItcSummary
              itc={cluster.params.itc}
              title={t("itc.title")}
              evidenceCitationLabel={t("itc.citations")}
            />
          )}
          {displaySummary !== "" && (
            <RichText className="text-sm text-muted-foreground">{displaySummary}</RichText>
          )}
          {cluster.keywords.map(cleanInternalTitle).filter(Boolean).length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {[...new Set(cluster.keywords.map(cleanInternalTitle).filter(Boolean))].map((k) => (
                <Badge key={k} variant="outline">
                  {k}
                </Badge>
              ))}
            </div>
          )}
          {cluster.representatives && cluster.representatives.length > 0 && (
            <Section title={t("docsSection")}>
              <div className="space-y-2">
                {cluster.representatives.map((r) => {
                  // Ключ — из содержимого фрагмента: стабильного id у представителя нет.
                  const key = [r.document_id, r.filename, r.snippet].join("|");
                  return r.document_id ? (
                    <CardButton
                      key={key}
                      onClick={() =>
                        openCitation({
                          documentId: r.document_id ?? "",
                          filename: r.filename,
                          snippet: r.snippet,
                        })
                      }
                      className="p-3"
                    >
                      {r.filename && (
                        <p className="mb-1 flex items-center gap-1 font-mono text-xs text-muted-foreground">
                          <FileText className="size-3" aria-hidden />
                          {r.filename}
                        </p>
                      )}
                      {r.snippet && <p className="text-sm">{r.snippet}</p>}
                    </CardButton>
                  ) : (
                    <Card key={key} className="bg-muted/30 p-3">
                      {r.filename && (
                        <p className="mb-1 flex items-center gap-1 font-mono text-xs text-muted-foreground">
                          <FileText className="size-3" aria-hidden />
                          {r.filename}
                        </p>
                      )}
                      {r.snippet && <p className="text-sm">{r.snippet}</p>}
                    </Card>
                  );
                })}
              </div>
            </Section>
          )}
          <Button
            variant="brand"
            size="sm"
            disabled={genBusy}
            onClick={() => void generateFromCluster(cluster)}
          >
            <Wand2 className="size-4" aria-hidden />
            {genBusy ? t("generating") : t("generate")}
          </Button>
        </div>
      )}

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
