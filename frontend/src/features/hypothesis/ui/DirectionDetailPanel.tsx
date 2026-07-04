import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Compass, FileText, Layers, Network, Sparkles, Target, Wand2 } from "lucide-react";

import {
  generateHypotheses,
  type ApiEvidence,
  type ApiHypothesis,
} from "@/features/hypothesis/api";
import {
  cleanInternalTitle,
  displayHypothesisTitle,
  directionMeta,
} from "@/features/hypothesis/model";
import { useBackgroundTasks } from "@/features/task";
import { DocPreviewSheet, useDocPreview } from "@/features/document";
import { EvidenceMap, evidenceArticleCount } from "@/features/hypothesis/ui/EvidenceMap";
import { ItcSummary } from "@/features/hypothesis/ui/Itc";
import { BulletList, Field, FieldLabel, Panel } from "@/features/hypothesis/ui/detail/primitives";
import { ProvenancePanel } from "@/features/hypothesis/ui/detail/ProvenancePanel";
import { AiNote } from "@/shared/ui/AiNote";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { Card, CardContent } from "@/shared/ui/Card";
import { RichText } from "@/shared/ui/RichText";
import { formatDateShort } from "@/shared/lib/format";
import {
  fetchHypothesisRuntimeSettings,
  loadHypothesisRuntimeSettings,
} from "@/shared/appSettings";

interface DirectionDetailPanelProps {
  direction: ApiHypothesis;
  clusterTitle?: string;
}

export function DirectionDetailPanel({ direction, clusterTitle }: DirectionDetailPanelProps) {
  const { t } = useTranslation("hypothesisDetail");
  const navigate = useNavigate();
  const { runTask, isTaskRunning } = useBackgroundTasks();
  const { preview, openCitation, close: closePreview, retry: retryPreview } = useDocPreview();
  const [runtimeSettings, setRuntimeSettings] = useState(loadHypothesisRuntimeSettings);

  const title = displayHypothesisTitle(direction);
  const meta = directionMeta();
  const clusterLabel =
    cleanInternalTitle(generationText(direction, "cluster_label") ?? "") || clusterTitle;
  const evidenceArticles = evidenceArticleCount(direction.evidence);
  const taskKey = `direction:${direction.id}:generate`;
  const generating = isTaskRunning(taskKey);

  useEffect(() => {
    let cancelled = false;
    fetchHypothesisRuntimeSettings()
      .then((next) => {
        if (!cancelled) setRuntimeSettings(next);
      })
      .catch(() => {
        if (!cancelled) setRuntimeSettings(loadHypothesisRuntimeSettings());
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const openEvidenceDoc = useCallback(
    (ev: ApiEvidence) => {
      if (!ev.document_id) return;
      openCitation({
        documentId: ev.document_id,
        chunkId: ev.chunk_id,
        filename: ev.filename,
        snippet: ev.snippet,
        page: ev.page_start,
      });
    },
    [openCitation],
  );

  const generateFromDirection = useCallback(() => {
    // Данные для генерации на бэкенде (промпт LLM) — не переводятся.
    const kpiDescription = [
      direction.statement,
      direction.rationale ? `Обоснование: ${direction.rationale}` : null,
      clusterLabel ? `Тема: ${clusterLabel}` : null,
      direction.tags.length > 0 ? `Теги: ${direction.tags.join(", ")}` : null,
    ]
      .filter(Boolean)
      .join("\n\n");

    void runTask({
      key: taskKey,
      title: t("direction.taskTitle"),
      description: t("direction.taskDescription", { title }),
      successMessage: (created) => t("direction.created", { count: created.length }),
      errorMessage: t("direction.taskFailed"),
      run: () =>
        generateHypotheses({
          kpi_title: title,
          kpi_description: kpiDescription,
          count: runtimeSettings.directionGenerateCount,
        }),
      onSuccess: () => navigate("/hypotheses"),
    });
  }, [
    clusterLabel,
    direction.rationale,
    direction.statement,
    direction.tags,
    navigate,
    runTask,
    runtimeSettings.directionGenerateCount,
    t,
    taskKey,
    title,
  ]);

  const detail = direction.detail;
  const scores = [
    scoreItem(t("scores.value"), direction.assessment.value?.score ?? direction.value_score),
    scoreItem(t("scores.novelty"), direction.assessment.novelty?.score ?? direction.novelty_score),
    scoreItem(t("scores.risk"), direction.assessment.risk?.score ?? direction.risk_score),
  ].filter((item): item is ScoreItem => item !== null);

  const hasContext =
    (detail.problem_addressed ?? "") !== "" ||
    (detail.drivers?.length ?? 0) > 0 ||
    (detail.application_potential?.objects?.length ?? 0) > 0 ||
    direction.tags.length > 0;

  return (
    <div className="space-y-5 pb-10">
      <Card>
        <CardContent className="space-y-2.5 p-5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge className={meta.className}>{meta.label}</Badge>
            {evidenceArticles > 0 && (
              <Badge variant="secondary">
                {t("evidence.documents", { count: evidenceArticles })}
              </Badge>
            )}
            {clusterLabel && <Badge variant="outline">{clusterLabel}</Badge>}
            <span className="text-xs text-muted-foreground">
              {t("direction.updatedAt", { date: formatDateShort(direction.updated_at) })}
            </span>
          </div>
          <h1 className="font-display text-2xl leading-tight md:text-[1.75rem]">{title}</h1>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_340px] lg:items-start">
        <div className="min-w-0 space-y-5">
          <Panel title={t("panels.direction")} icon={Compass}>
            {direction.statement ? (
              <RichText className="text-base leading-relaxed">{direction.statement}</RichText>
            ) : (
              <p className="text-sm text-muted-foreground">{t("direction.noDescription")}</p>
            )}
            {direction.rationale && (
              <div className="border-t pt-3">
                <FieldLabel>{t("fields.whyHighlighted")}</FieldLabel>
                <RichText className="mt-1 text-sm leading-relaxed text-muted-foreground">
                  {direction.rationale}
                </RichText>
              </div>
            )}
            <AiNote>{t("direction.aiNote")}</AiNote>
          </Panel>

          <ProvenancePanel
            hypothesis={direction}
            onOpenDocument={(documentId, filename) =>
              openCitation({ documentId, filename: filename ?? t("evidence.documentFallback") })
            }
          />

          {direction.evidence.length > 0 && (
            <Panel title={t("panels.directionDocuments")} icon={Network} count={evidenceArticles}>
              <EvidenceMap
                evidence={direction.evidence}
                onOpen={openEvidenceDoc}
                subjectLabel={t("evidence.subjectDirection")}
              />
            </Panel>
          )}

          {hasContext && (
            <Panel title={t("panels.context")} icon={Layers}>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                {(detail.problem_addressed ?? "") !== "" && (
                  <Field label={t("fields.problem")}>
                    <RichText className="text-sm text-muted-foreground">
                      {detail.problem_addressed}
                    </RichText>
                  </Field>
                )}
                {detail.drivers && detail.drivers.length > 0 && (
                  <Field label={t("fields.driversShort")}>
                    <BulletList items={detail.drivers} />
                  </Field>
                )}
                {detail.application_potential?.objects &&
                  detail.application_potential.objects.length > 0 && (
                    <Field label={t("fields.application")}>
                      <BulletList items={detail.application_potential.objects} />
                    </Field>
                  )}
                {direction.tags.length > 0 && (
                  <Field label={t("fields.areas")}>
                    <div className="flex flex-wrap gap-1.5">
                      {direction.tags.map((tag) => (
                        <Badge key={tag} variant="secondary">
                          {tag}
                        </Badge>
                      ))}
                    </div>
                  </Field>
                )}
              </div>
            </Panel>
          )}
        </div>

        <aside className="order-first space-y-4 lg:order-2 lg:sticky lg:top-6">
          {direction.assessment.itc && (
            <ItcSummary
              itc={direction.assessment.itc}
              title={t("direction.itcTitle")}
              evidenceCitationLabel={t("direction.itcCitations")}
            />
          )}

          {scores.length > 0 && (
            <Panel title={t("panels.directionScores")} icon={Target}>
              <div className="grid grid-cols-3 gap-2">
                {scores.map((item) => (
                  <ScoreTile key={item.label} item={item} />
                ))}
              </div>
            </Panel>
          )}

          <Panel title={t("panels.nextStep")} icon={Sparkles}>
            <Button
              className="w-full"
              size="sm"
              disabled={generating}
              onClick={generateFromDirection}
            >
              <Wand2 className="size-4" aria-hidden />
              {generating ? t("direction.generating") : t("direction.generate")}
            </Button>
          </Panel>

          {direction.evidence.length > 0 && (
            <Panel title={t("panels.sources")} icon={FileText}>
              <p className="text-sm text-muted-foreground">
                {t("direction.sourcesSummary", {
                  fragments: t("evidence.fragments", { count: direction.evidence.length }),
                  documents: t("evidence.sourceDocs", { count: evidenceArticles }),
                })}
              </p>
            </Panel>
          )}
        </aside>
      </div>

      <DocPreviewSheet
        doc={preview ? (preview.full ?? preview.doc) : null}
        fragmentId={preview?.fragmentId}
        highlight={preview?.highlight}
        citedFragmentIds={
          preview
            ? direction.evidence
                .filter((e) => e.document_id === preview.doc.id && e.chunk_id)
                .map((e) => e.chunk_id)
            : undefined
        }
        loading={preview ? !preview.full && !preview.failed : false}
        error={preview?.failed ?? false}
        onRetry={retryPreview}
        onClose={closePreview}
      />
    </div>
  );
}

function generationText(direction: ApiHypothesis, key: string): string | undefined {
  const value = direction.generation?.[key];
  return typeof value === "string" && value.trim() !== "" ? value.trim() : undefined;
}

interface ScoreItem {
  label: string;
  value: string;
}

function scoreItem(label: string, value: number | null | undefined): ScoreItem | null {
  if (value === null || value === undefined) return null;
  const pct = Math.round(Math.max(0, Math.min(1, value)) * 100);
  return { label, value: `${pct}%` };
}

function ScoreTile({ item }: { item: ScoreItem }) {
  return (
    <div className="rounded-lg border bg-muted/30 p-3 text-center">
      <div className="text-lg font-semibold tabular-nums">{item.value}</div>
      <div className="mt-0.5 text-xs text-muted-foreground">{item.label}</div>
    </div>
  );
}
