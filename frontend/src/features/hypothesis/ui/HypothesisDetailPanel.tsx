import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ComponentType } from "react";
import { useTranslation } from "react-i18next";
import {
  BadgeCheck,
  Check,
  Clock,
  FlaskConical,
  GitBranch,
  Layers,
  MapPin,
  MessageSquarePlus,
  Network,
  Pencil,
  ShieldQuestion,
  Tag,
  Target,
  TestTube,
  UserRound,
  Users,
  X,
} from "lucide-react";
import { toast } from "sonner";

import {
  addHypothesisRevision,
  analyzeCompetitors,
  type ApiEvidence,
  type ApiHypothesis,
  type ApiRevision,
  type HypothesisStatus,
  listHypothesisRevisions,
  planExperiment,
  refineHypothesis,
  tagHypothesis,
  updateHypothesis,
} from "@/features/hypothesis/api";
import { loadAuth } from "@/shared/api/client";
import { cn } from "@/shared/lib/cn";
import { formatDateShort, formatDateTime } from "@/shared/lib/format";
import { GLOSSARY } from "@/shared/glossary";
import {
  directionMeta,
  displayHypothesisTitle,
  isResearchDirection,
  researchTypeMeta,
  researchTypeTag,
  reviewStatusMeta,
  visibleMeta,
} from "@/features/hypothesis/model";
import { useOwnerNames } from "@/features/hypothesis/owners";
import { useBackgroundTasks } from "@/features/task";
import { AiNote } from "@/shared/ui/AiNote";
import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import { Card, CardContent } from "@/shared/ui/Card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/Dialog";
import { Input } from "@/shared/ui/Input";
import { Label } from "@/shared/ui/Label";
import { DocPreviewSheet, useDocPreview } from "@/features/document";
import { RoadmapEditDialog } from "@/features/hypothesis/ui/RoadmapEditor";
import { EvidenceMap, evidenceArticleCount } from "@/features/hypothesis/ui/EvidenceMap";
import { BulletList, Field, FieldLabel, Panel } from "@/features/hypothesis/ui/detail/primitives";
import { ProvenancePanel } from "@/features/hypothesis/ui/detail/ProvenancePanel";
import { Scoreboard } from "@/features/hypothesis/ui/detail/Scoreboard";
import { SectionNav, type SectionLink } from "@/features/hypothesis/ui/detail/SectionNav";
import {
  CausalChain,
  ExperimentPlan,
  FeasibilityCheck,
  MaterialsPassport,
  SchemaCompleteness,
} from "@/features/hypothesis/ui/HypothesisInsights";
import { parseGeneration } from "@/features/hypothesis/provenance";
import { RichText } from "@/shared/ui/RichText";

interface HypothesisDetailPanelProps {
  hypothesis: ApiHypothesis;
  kpiTitle?: string;
  /** Theme (cluster) the hypothesis belongs to — shows the Цель ▸ Тема chain. */
  clusterTitle?: string;
  /** Called with the fresh hypothesis after an expert action mutates it. */
  onChanged: (updated: ApiHypothesis) => void;
}

export function HypothesisDetailPanel({
  hypothesis,
  kpiTitle,
  clusterTitle,
  onChanged,
}: HypothesisDetailPanelProps) {
  const { t } = useTranslation("hypothesisDetail");
  const [revisions, setRevisions] = useState<ApiRevision[]>([]);
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState(false);
  const [rationaleOpen, setRationaleOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [roadmapOpen, setRoadmapOpen] = useState(false);
  const [editForm, setEditForm] = useState({ title: "", statement: "", rationale: "" });
  const mounted = useRef(true);
  const { runTask, isTaskRunning } = useBackgroundTasks();

  const editor = loadAuth()?.user.username ?? "expert";
  const ownerName = useOwnerNames();
  const owner = ownerName(hypothesis.owner_id);
  const displayTitle = displayHypothesisTitle(hypothesis);

  // Click an evidence source → open its document and highlight the cited fragment.
  const { preview, openCitation, close: closePreview, retry: retryPreview } = useDocPreview();

  const loadRevisions = useCallback(() => {
    listHypothesisRevisions(hypothesis.id)
      .then(setRevisions)
      .catch(() => setRevisions([]));
  }, [hypothesis.id]);

  useEffect(() => {
    loadRevisions();
  }, [loadRevisions]);

  useEffect(() => {
    return () => {
      mounted.current = false;
    };
  }, []);

  const taskKey = useCallback(
    (action: string) => `hypothesis:${hypothesis.id}:${action}`,
    [hypothesis.id],
  );
  const competing = isTaskRunning(taskKey("competitors"));
  const refining = isTaskRunning(taskKey("refine"));
  const tagging = isTaskRunning(taskKey("tag"));
  const planning = isTaskRunning(taskKey("experiment"));

  const changeStatus = useCallback(
    async (status: HypothesisStatus, action: string, summary: string) => {
      setBusy(true);
      try {
        const updated = await updateHypothesis(hypothesis.id, {
          ...hypothesis,
          status,
          revision: { action, summary, editor_id: editor },
        });
        onChanged(updated);
        loadRevisions();
        toast.success(t("actions.statusUpdated"));
      } catch (e) {
        toast.error(e instanceof Error ? e.message : t("actions.statusUpdateFailed"));
      } finally {
        setBusy(false);
      }
    },
    [hypothesis, editor, onChanged, loadRevisions, t],
  );

  const openEdit = useCallback(() => {
    setEditForm({
      title: hypothesis.title,
      statement: hypothesis.statement,
      rationale: hypothesis.rationale,
    });
    setEditOpen(true);
  }, [hypothesis]);

  const saveEdit = useCallback(async () => {
    const statement = editForm.statement.trim();
    if (statement === "") return;
    setBusy(true);
    try {
      const updated = await updateHypothesis(hypothesis.id, {
        ...hypothesis,
        title: editForm.title.trim(),
        statement,
        rationale: editForm.rationale.trim(),
        revision: { action: "edited", summary: t("edit.revisionSummary"), editor_id: editor },
      });
      setEditOpen(false);
      onChanged(updated);
      loadRevisions();
      toast.success(t("edit.saved"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("edit.saveFailed"));
    } finally {
      setBusy(false);
    }
  }, [editForm, hypothesis, editor, onChanged, loadRevisions, t]);

  const submitComment = useCallback(async () => {
    const text = comment.trim();
    if (text === "") {
      return;
    }
    setBusy(true);
    try {
      await addHypothesisRevision(hypothesis.id, {
        action: "commented",
        summary: text,
        editor_id: editor,
      });
      setComment("");
      loadRevisions();
      toast.success(t("actions.commentAdded"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("actions.commentAddFailed"));
    } finally {
      setBusy(false);
    }
  }, [comment, hypothesis.id, editor, loadRevisions, t]);

  const runCompetitors = useCallback(() => {
    void runTask({
      key: taskKey("competitors"),
      title: t("competitors.taskTitle"),
      description: displayTitle,
      successMessage: (updated: ApiHypothesis) => {
        const n = updated.detail.competitors?.items?.length ?? 0;
        return n > 0 ? t("competitors.found", { count: n }) : t("competitors.noneFound");
      },
      errorMessage: t("competitors.taskFailed"),
      run: () => analyzeCompetitors(hypothesis.id),
      onSuccess: (updated) => {
        if (!mounted.current) return;
        onChanged(updated);
        loadRevisions();
      },
    });
  }, [hypothesis.id, displayTitle, loadRevisions, onChanged, runTask, taskKey, t]);

  const runRefine = useCallback(() => {
    void runTask({
      key: taskKey("refine"),
      title: t("refine.taskTitle"),
      description: displayTitle,
      successMessage: t("refine.taskSuccess"),
      errorMessage: t("refine.taskFailed"),
      run: () => refineHypothesis(hypothesis.id),
      onSuccess: (updated) => {
        if (!mounted.current) return;
        onChanged(updated);
        loadRevisions();
      },
    });
  }, [hypothesis.id, displayTitle, loadRevisions, onChanged, runTask, taskKey, t]);

  const runTag = useCallback(() => {
    void runTask({
      key: taskKey("tag"),
      title: t("tags.taskTitle"),
      description: displayTitle,
      successMessage: t("tags.taskSuccess"),
      errorMessage: t("tags.taskFailed"),
      run: () => tagHypothesis(hypothesis.id),
      onSuccess: (updated) => {
        if (!mounted.current) return;
        onChanged(updated);
        loadRevisions();
      },
    });
  }, [hypothesis.id, displayTitle, loadRevisions, onChanged, runTask, taskKey, t]);

  const runPlanExperiment = useCallback(() => {
    void runTask({
      key: taskKey("experiment"),
      title: t("experiment.taskTitle"),
      description: displayTitle,
      successMessage: t("experiment.taskSuccess"),
      errorMessage: t("experiment.taskFailed"),
      run: () => planExperiment(hypothesis.id),
      onSuccess: (updated) => {
        if (!mounted.current) return;
        onChanged(updated);
        loadRevisions();
      },
    });
  }, [hypothesis.id, displayTitle, loadRevisions, onChanged, runTask, taskKey, t]);

  const openEvidenceDoc = useCallback(
    (ev: ApiEvidence) => {
      if (ev.document_id) {
        openCitation({
          documentId: ev.document_id,
          chunkId: ev.chunk_id,
          filename: ev.filename,
          snippet: ev.snippet,
          page: ev.page_start,
        });
      }
    },
    [openCitation],
  );

  const a = hypothesis.assessment;
  const d = hypothesis.detail;
  const schema = a.schema;
  const direction = isResearchDirection(hypothesis) ? directionMeta() : null;
  const reviewStatus = reviewStatusMeta(hypothesis.status);
  const research = researchTypeMeta(researchTypeTag(hypothesis.tags));
  // Служебные теги конвейера — не научные специальности.
  const scienceTags = hypothesis.tags.filter(
    (tag) => !["auto_bridge", "auto_cluster", "discovery", "graph_direction"].includes(tag),
  );
  const evidenceArticles = evidenceArticleCount(hypothesis.evidence);

  const hasDetails =
    (d.problem_addressed ?? "") !== "" ||
    (d.drivers?.length ?? 0) > 0 ||
    (d.quantitative_parameters?.length ?? 0) > 0 ||
    (d.application_potential?.objects?.length ?? 0) > 0 ||
    (d.verification?.method ?? "") !== "";

  const hasProvenance = parseGeneration(hypothesis).kind !== "unknown";
  const hasPassport = Boolean(
    d.material_system ||
    d.composition_change ||
    d.process_change ||
    d.microstructure_mechanism ||
    d.target_property ||
    (d.characterization_methods?.length ?? 0) > 0 ||
    (d.test_methods?.length ?? 0) > 0 ||
    (d.failure_modes?.length ?? 0) > 0,
  );
  const hasMechanism = (d.causal_chain?.length ?? 0) > 0;
  const hasFeasibility = (d.feasibility?.length ?? 0) > 0;
  const revisionCount = revisions.length;

  const navSections = useMemo<SectionLink[]>(() => {
    const list: SectionLink[] = [];
    if (hasProvenance) list.push({ id: "born", label: t("provenance.title") });
    if (hasPassport) list.push({ id: "passport", label: t("panels.materials") });
    if (hasMechanism) list.push({ id: "mechanism", label: t("nav.mechanism") });
    list.push({ id: "experiment", label: t("panels.experiment") });
    if (hasFeasibility) list.push({ id: "feasibility", label: t("panels.feasibility") });
    list.push({ id: "evidence", label: t("panels.evidence"), count: evidenceArticles });
    if (hasDetails) list.push({ id: "details", label: t("panels.details") });
    list.push({ id: "competitors", label: t("panels.competitors") });
    list.push({ id: "specialties", label: t("panels.specialties") });
    if (revisionCount > 0) {
      list.push({ id: "history", label: t("panels.history"), count: revisionCount });
    }
    return list;
  }, [
    hasProvenance,
    hasPassport,
    hasMechanism,
    hasFeasibility,
    hasDetails,
    evidenceArticles,
    revisionCount,
    t,
  ]);

  const organization = visibleMeta(hypothesis.organization);
  const functionArea = visibleMeta(hypothesis.function_area);
  const location = visibleMeta(hypothesis.location);
  const sourceType = visibleMeta(hypothesis.source_type);
  const meta = [
    owner && { icon: UserRound, text: t("hero.owner", { name: owner }) },
    organization && { icon: FlaskConical, text: organization },
    functionArea && { icon: null, text: functionArea },
    location && { icon: MapPin, text: location },
    kpiTitle && { icon: Target, text: kpiTitle },
    clusterTitle && { icon: Layers, text: clusterTitle },
    { icon: Clock, text: formatDateShort(hypothesis.created_at) },
  ].filter(Boolean) as { icon: ComponentType<{ className?: string }> | null; text: string }[];

  const decisionControls = (
    <div className="space-y-2">
      <div className="grid grid-cols-1 gap-1.5">
        <Button
          size="sm"
          disabled={busy}
          onClick={() => void changeStatus("approved", "approved", t("expert.summaryApproved"))}
        >
          <Check className="size-3.5" aria-hidden />
          {t("actions.approve")}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() =>
            void changeStatus("under_review", "status_changed", t("expert.summaryReview"))
          }
        >
          <Clock className="size-3.5" aria-hidden />
          {t("actions.sendToReview")}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          disabled={busy}
          onClick={() => void changeStatus("rejected", "rejected", t("expert.summaryRejected"))}
        >
          <X className="size-3.5" aria-hidden />
          {t("actions.reject")}
        </Button>
      </div>
      <textarea
        value={comment}
        onChange={(e) => setComment(e.target.value)}
        placeholder={t("expert.commentPlaceholder")}
        rows={2}
        className="w-full resize-y rounded-lg border border-input bg-transparent px-3 py-2 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-ring"
      />
      <Button
        size="sm"
        variant="secondary"
        className="w-full"
        disabled={busy || comment.trim() === ""}
        onClick={() => void submitComment()}
      >
        <MessageSquarePlus className="size-3.5" aria-hidden />
        {t("actions.addComment")}
      </Button>
    </div>
  );

  return (
    <div className="space-y-5 pb-16">
      {/* Hero: identity + the claim itself; assessment lives in the scoreboard below. */}
      <Card>
        <CardContent className="space-y-2.5 p-5">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2">
              {direction && <Badge className={direction.className}>{direction.label}</Badge>}
              {reviewStatus && (
                <Badge className={reviewStatus.className}>{reviewStatus.label}</Badge>
              )}
              {sourceType && <Badge variant="outline">{sourceType}</Badge>}
              {research && <Badge className={research.className}>{research.label}</Badge>}
              {!hypothesis.measurable && <Badge variant="outline">{t("hero.scientific")}</Badge>}
            </div>
            <Button
              size="sm"
              variant="ghost"
              className="-mr-2 -mt-1"
              disabled={busy}
              onClick={openEdit}
            >
              <Pencil className="size-3.5" aria-hidden />
              {t("edit.open")}
            </Button>
          </div>
          <h1 className="font-display text-2xl leading-tight md:text-[1.75rem]">{displayTitle}</h1>
          {direction && <p className="text-sm text-muted-foreground">{t("hero.directionNote")}</p>}
          {schema && <SchemaCompleteness schema={schema} />}
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
            {meta.map((m) => (
              <span key={m.text} className="inline-flex items-center gap-1">
                {m.icon && <m.icon className="size-3.5 shrink-0" />}
                {m.text}
              </span>
            ))}
          </div>
          <div className="border-t pt-3">
            <RichText className="text-[15px] leading-relaxed">{hypothesis.statement}</RichText>
            {hypothesis.rationale && (
              <div className="mt-3">
                <FieldLabel>{t("fields.rationale")}</FieldLabel>
                <RichText
                  className={cn(
                    "mt-1 text-sm leading-relaxed text-muted-foreground",
                    hypothesis.rationale.length > 280 && !rationaleOpen && "line-clamp-4",
                  )}
                >
                  {hypothesis.rationale}
                </RichText>
                {hypothesis.rationale.length > 280 && (
                  <button
                    type="button"
                    onClick={() => setRationaleOpen((o) => !o)}
                    className="mt-1 text-xs font-medium text-primary hover:underline"
                  >
                    {rationaleOpen ? t("actions.collapse") : t("actions.showFull")}
                  </button>
                )}
              </div>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Скорборд: все оценки в один взгляд, объяснения — по клику на плитку. */}
      <Scoreboard
        hypothesis={hypothesis}
        title={displayTitle}
        refining={refining}
        onRefine={() => void runRefine()}
      />

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1fr)_264px] lg:items-start">
        {/* Main: the hypothesis story + its evidence. */}
        <div className="min-w-0 space-y-5">
          {hasProvenance && (
            <section id="born" className="scroll-mt-16">
              <ProvenancePanel
                hypothesis={hypothesis}
                onOpenDocument={(documentId, filename) =>
                  openCitation({ documentId, filename: filename ?? t("evidence.documentFallback") })
                }
              />
            </section>
          )}

          {hasPassport && (
            <section id="passport" className="scroll-mt-16">
              <Panel title={t("panels.materials")} icon={FlaskConical}>
                <MaterialsPassport detail={d} />
              </Panel>
            </section>
          )}

          {hasMechanism && d.causal_chain && (
            <section id="mechanism" className="scroll-mt-16">
              <Panel title={t("panels.mechanism")} icon={GitBranch} titleHint={GLOSSARY.cmpp}>
                <CausalChain steps={d.causal_chain} />
              </Panel>
            </section>
          )}

          <section id="experiment" className="scroll-mt-16">
            <Panel
              title={t("panels.experiment")}
              icon={TestTube}
              titleHint={GLOSSARY.experiment}
              action={
                <div className="flex gap-1.5">
                  {d.experiment_plan && (
                    <Button size="sm" variant="ghost" onClick={() => setRoadmapOpen(true)}>
                      <Pencil className="size-3.5" aria-hidden />
                      {t("roadmapEdit.open")}
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={planning}
                    onClick={() => void runPlanExperiment()}
                  >
                    <TestTube className="size-3.5" aria-hidden />
                    {planning
                      ? t("experiment.composing")
                      : d.experiment_plan
                        ? t("experiment.updatePlan")
                        : t("experiment.createPlan")}
                  </Button>
                </div>
              }
            >
              {d.experiment_plan ? (
                <>
                  <ExperimentPlan plan={d.experiment_plan} />
                  <AiNote>{t("experiment.aiNote")}</AiNote>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">{t("experiment.empty")}</p>
              )}
            </Panel>
          </section>

          {hasFeasibility && d.feasibility && (
            <section id="feasibility" className="scroll-mt-16">
              <Panel
                title={t("panels.feasibility")}
                icon={ShieldQuestion}
                titleHint={GLOSSARY.feasibility}
              >
                <FeasibilityCheck items={d.feasibility} />
              </Panel>
            </section>
          )}

          <section id="evidence" className="scroll-mt-16">
            <Panel title={t("panels.evidence")} icon={Network} count={evidenceArticles}>
              <EvidenceMap evidence={hypothesis.evidence} onOpen={openEvidenceDoc} />
            </Panel>
          </section>

          {hasDetails && (
            <section id="details" className="scroll-mt-16">
              <Panel title={t("panels.details")} collapsible defaultOpen={false}>
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  {(d.problem_addressed ?? "") !== "" && (
                    <Field label={t("fields.problem")}>
                      <RichText className="text-sm text-muted-foreground">
                        {d.problem_addressed}
                      </RichText>
                    </Field>
                  )}
                  {d.drivers && d.drivers.length > 0 && (
                    <Field label={t("fields.drivers")}>
                      <BulletList items={d.drivers} />
                    </Field>
                  )}
                  {d.application_potential?.objects &&
                    d.application_potential.objects.length > 0 && (
                      <Field label={t("fields.applicationPotential")}>
                        <BulletList items={d.application_potential.objects} />
                      </Field>
                    )}
                  {(d.verification?.method ?? "") !== "" && (
                    <Field label={t("fields.howToVerify")}>
                      <RichText className="text-sm text-muted-foreground">
                        {d.verification?.method}
                      </RichText>
                      {d.verification?.metrics && d.verification.metrics.length > 0 && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {t("fields.metrics", { list: d.verification.metrics.join(", ") })}
                        </p>
                      )}
                    </Field>
                  )}
                </div>
                {d.quantitative_parameters && d.quantitative_parameters.length > 0 && (
                  <div>
                    <FieldLabel>{t("fields.quantParams")}</FieldLabel>
                    <div className="mt-1 flex flex-wrap gap-1.5">
                      {d.quantitative_parameters.map((p) => (
                        <Badge key={`${p.name}-${p.value}`} variant="secondary">
                          {p.name}: {p.value}
                          {p.unit ? ` ${p.unit}` : ""}
                        </Badge>
                      ))}
                    </div>
                  </div>
                )}
              </Panel>
            </section>
          )}

          <section id="competitors" className="scroll-mt-16">
            <Panel
              title={t("panels.competitors")}
              icon={Users}
              collapsible
              defaultOpen={Boolean(d.competitors)}
              action={
                <Button
                  size="sm"
                  variant="outline"
                  disabled={competing}
                  onClick={() => void runCompetitors()}
                >
                  <Users className="size-3.5" aria-hidden />
                  {competing
                    ? t("competitors.analyzing")
                    : d.competitors
                      ? t("actions.update")
                      : t("competitors.findInBase")}
                </Button>
              }
            >
              {d.competitors?.summary && (
                <p className="text-sm text-muted-foreground">{d.competitors.summary}</p>
              )}
              {d.competitors?.items && d.competitors.items.length > 0 ? (
                <div className="space-y-2">
                  {d.competitors.items.map((c) => (
                    <div
                      key={[c.name, c.maturity, c.source].join("|")}
                      className="rounded-lg border bg-muted/30 p-2.5"
                    >
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">{c.name || "—"}</span>
                        {c.maturity && <Badge variant="outline">{c.maturity}</Badge>}
                        {c.source && (
                          <span className="text-xs text-muted-foreground">{c.source}</span>
                        )}
                      </div>
                      {c.approach && (
                        <p className="mt-1 text-sm text-muted-foreground">{c.approach}</p>
                      )}
                      <div className="mt-1.5 grid grid-cols-1 gap-2 sm:grid-cols-2">
                        {c.strengths && c.strengths.length > 0 && (
                          <div>
                            <p className="text-xs font-medium text-ok">
                              {t("competitors.strengths")}
                            </p>
                            <BulletList items={c.strengths} small />
                          </div>
                        )}
                        {c.weaknesses && c.weaknesses.length > 0 && (
                          <div>
                            <p className="text-xs font-medium text-risk">
                              {t("competitors.weaknesses")}
                            </p>
                            <BulletList items={c.weaknesses} small />
                          </div>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {d.competitors ? t("competitors.notFound") : t("competitors.notStarted")}
                </p>
              )}
            </Panel>
          </section>

          <section id="specialties" className="scroll-mt-16">
            <Panel
              title={t("panels.specialties")}
              icon={Tag}
              titleHint={GLOSSARY.specialties}
              collapsible
              defaultOpen={scienceTags.length > 0}
              action={
                <Button
                  size="sm"
                  variant="outline"
                  disabled={tagging}
                  onClick={() => void runTag()}
                >
                  <Tag className="size-3.5" aria-hidden />
                  {tagging
                    ? t("tags.determining")
                    : scienceTags.length > 0
                      ? t("actions.update")
                      : t("tags.determine")}
                </Button>
              }
            >
              {scienceTags.length > 0 ? (
                <div className="space-y-2">
                  <div className="flex flex-wrap gap-1.5">
                    {scienceTags.map((tag) => (
                      <Badge key={tag} variant="secondary">
                        {tag}
                      </Badge>
                    ))}
                  </div>
                  <AiNote>{t("tags.aiNote")}</AiNote>
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">{t("tags.empty")}</p>
              )}
            </Panel>
          </section>

          {/* На мобиле решение эксперта — обычная секция; на десктопе оно живёт в липкой рейке. */}
          <div className="lg:hidden">
            <Panel title={t("panels.expertDecision")} icon={BadgeCheck}>
              {decisionControls}
            </Panel>
          </div>

          {revisions.length > 0 && (
            <section id="history" className="scroll-mt-16">
              <Panel
                title={t("panels.history")}
                icon={Clock}
                collapsible
                defaultOpen={false}
                count={revisions.length}
              >
                <ol className="space-y-2">
                  {revisions.map((r) => (
                    <li key={r.id} className="text-sm">
                      <span className="font-medium">#{r.revision_no}</span>{" "}
                      <span className="text-muted-foreground">{r.action}</span>
                      {r.summary ? ` — ${r.summary}` : ""}
                      <span className="ml-1 text-xs text-muted-foreground">
                        · {r.editor_id || t("history.system")} · {formatDateTime(r.created_at)}
                      </span>
                    </li>
                  ))}
                </ol>
              </Panel>
            </section>
          )}
        </div>

        {/* Липкая рейка: навигация по секциям + решение эксперта всегда под рукой. */}
        <aside className="hidden space-y-4 lg:sticky lg:top-16 lg:block">
          <SectionNav title={t("nav.title")} sections={navSections} />
          <div className="rounded-xl border bg-card p-4">
            <div className="flex items-center justify-between gap-2">
              <p className="kicker text-muted-foreground">{t("panels.expertDecision")}</p>
              {reviewStatus && (
                <Badge className={reviewStatus.className}>{reviewStatus.label}</Badge>
              )}
            </div>
            <div className="mt-3">{decisionControls}</div>
          </div>
        </aside>
      </div>

      {roadmapOpen && (
        <RoadmapEditDialog
          open={roadmapOpen}
          onOpenChange={setRoadmapOpen}
          hypothesis={hypothesis}
          onChanged={(updated) => {
            onChanged(updated);
            loadRevisions();
          }}
        />
      )}

      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("edit.dialogTitle")}</DialogTitle>
            <DialogDescription>{t("edit.dialogDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="edit-title">{t("edit.title")}</Label>
              <Input
                id="edit-title"
                value={editForm.title}
                onChange={(e) => setEditForm((f) => ({ ...f, title: e.target.value }))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="edit-statement">{t("edit.statement")}</Label>
              <textarea
                id="edit-statement"
                value={editForm.statement}
                onChange={(e) => setEditForm((f) => ({ ...f, statement: e.target.value }))}
                rows={5}
                className="flex w-full resize-none rounded-lg border border-input bg-card px-3 py-2 text-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="edit-rationale">{t("edit.rationale")}</Label>
              <textarea
                id="edit-rationale"
                value={editForm.rationale}
                onChange={(e) => setEditForm((f) => ({ ...f, rationale: e.target.value }))}
                rows={5}
                className="flex w-full resize-none rounded-lg border border-input bg-card px-3 py-2 text-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setEditOpen(false)}>
              {t("edit.cancel")}
            </Button>
            <Button
              variant="brand"
              size="sm"
              disabled={busy || editForm.statement.trim() === ""}
              onClick={() => void saveEdit()}
            >
              {t("edit.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DocPreviewSheet
        doc={preview ? (preview.full ?? preview.doc) : null}
        fragmentId={preview?.fragmentId}
        highlight={preview?.highlight}
        citedFragmentIds={
          preview
            ? hypothesis.evidence
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
