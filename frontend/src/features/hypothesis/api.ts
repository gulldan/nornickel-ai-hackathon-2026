// Hypothesis Factory endpoints. The assessment / detail objects mirror
// schemas/hypothesis/v1; every field is optional so a partially-scored
// hypothesis renders without guards.
import { ApiError, postJSON, putJSON, request } from "@/shared/api/client";
import { i18n } from "@/shared/i18n";

interface ScoredDim {
  score?: number;
  /** Plain-language explanation of the figure (e.g. why novelty is this high). */
  rationale?: string;
}

/** Active-learning priority (assessment.learning_priority) — "how much testing
 *  this hypothesis would teach us". The headline `score` (0..1) combines its
 *  potential value, how uncertain we still are, and how thin the evidence is, so
 *  the board can surface what to test next. All fields 0..1, all optional. */
interface LearningPriority {
  /** Overall "what to test next" score, 0..1. */
  score?: number;
  /** Potential payoff if the hypothesis holds. */
  value?: number;
  /** How unsure the system still is (more unknown → more to learn). */
  uncertainty?: number;
  /** Strength of the evidence gathered so far. */
  evidence_quality?: number;
}

interface RiskDim {
  score?: number;
  level?: string;
  factors?: string[];
  rationale?: string;
}

export type Verdict = "supported" | "refuted" | "mixed" | "insufficient";

interface VerifyCheck {
  verdict?: Verdict;
  confidence?: number;
  rationale?: string;
  supporting?: string[];
  contradicting?: string[];
  model?: string;
  checked_at?: string;
}

interface TrlLevel {
  level: number;
  met: boolean;
  note?: string;
}

/** One ITC component (scientific momentum / novelty / impact / hype-readiness).
 *  value is 0..100, norm 0..1; note is the plain-language reading. */
export interface ItcComponent {
  key: string;
  name: string;
  value: number;
  norm: number;
  note?: string;
}

/** Technology index (ITC) — a deterministic, corpus-derived technology
 *  index that replaces novelty/confidence as the headline score. Computed
 *  outside the request path by itc-worker/itc.py from bibliometrics +
 *  embeddings + the evidence graph; stored under assessment.itc (hypotheses)
 *  and cluster params.itc (themes). */
export interface Itc {
  /** Final score 1..10. */
  score: number;
  band?: { label: string; note?: string } | null;
  /** Aggregate normalized score 0..1 (for the heatmap/sorting). */
  techscore: number;
  components?: {
    SM?: ItcComponent;
    NV?: ItcComponent;
    IP?: ItcComponent;
    HR?: ItcComponent;
  } | null;
  /** White-Space coordinates 0..1: X=momentum, Y=novelty, size=impact, color=diffusion. */
  axes: { momentum: number; novelty: number; impact: number; diffusion: number };
  signals?: {
    years?: number[];
    year_min?: number | null;
    year_max?: number | null;
    pub_count?: number;
    org_count?: number;
    orgs?: string[];
    evidence_citations?: number;
    novelty_distance?: number;
    document_count?: number;
  };
  method?: string;
  scope?: string;
  computed_at?: string;
}

/** Verification confidence, split into distinct facets (assessment.scores) so a
 *  single "confidence %" never masks unverified or weakly-supported claims.
 *  All numbers are 0..1; `unverified` flags "not enough data to trust". */
export interface AssessmentScores {
  /** How much the corpus supports the hypothesis being true. */
  belief_score?: number;
  /** How sure the verification verdict itself is. */
  verdict_confidence?: number;
  /** Strength/quality of the evidence the verdict rests on. */
  evidence_quality?: number;
  /** True → treat any high score as not yet verified. */
  unverified?: boolean;
}

/** Completeness of a generated hypothesis against the required passport schema
 *  (assessment.schema). `missing` lists absent slots like "intervention",
 *  "mechanism", "target", "validation_plan". */
export interface AssessmentSchema {
  /** Completeness 0..1. */
  score?: number;
  complete?: boolean;
  missing?: string[];
}

interface Assessment {
  novelty?: ScoredDim;
  /** World-practice recency — how fresh the OpenAlex hits for this topic are
   *  (0..1): a burst of recent papers reads as an active, novel research front. */
  topic_freshness?: ScoredDim;
  risk?: RiskDim;
  value?: ScoredDim;
  confidence?: ScoredDim;
  /** Split verification confidence (belief / verdict / evidence + unverified). */
  scores?: AssessmentScores;
  /** Active-learning priority — what to test next (value / uncertainty / evidence). */
  learning_priority?: LearningPriority;
  /** Generated-hypothesis schema completeness (score / complete / missing[]). */
  schema?: AssessmentSchema;
  /** Technology index (ITC) — primary readiness score; see Itc. */
  itc?: Itc;
  /** Transparent composite ranking — the deterministic, weighted-linear score the
   *  board sorts hypotheses by. The LLM only extracts the features; the final
   *  number is computed in the service from explained factors (see Ranking). */
  ranking?: Ranking;
  /** Readiness on the TRL scale (GOST R 58048-2017); levels[] is the per-level breakdown. */
  trl?: {
    level?: number;
    name?: string;
    rationale?: string;
    method?: string;
    standard?: string;
    signals?: string[];
    levels?: TrlLevel[];
  };
  /** Result of the last confirm/refute check against the corpus. */
  check?: VerifyCheck;
}

/** One explained line of the ranking breakdown: a normalized feature in [0,1],
 *  its weight, the resulting contribution (weight×value) and a short reason.
 *  The contributions sum to the composite score. */
interface RankingFactor {
  key: string;
  label: string;
  weight: number;
  value: number;
  contribution: number;
  detail: string;
  scored: boolean;
}

/** Transparent ranking result (assessment.ranking): the composite score plus the
 *  inspectable factor breakdown and the published formula. */
export interface Ranking {
  score: number;
  factors: RankingFactor[];
  method?: string;
  formula?: string;
  version?: string;
  /** Решение эксперта как прозрачный множитель поверх взвешенной суммы. */
  expert?: { status: string; multiplier: number; label?: string };
}

interface QuantParam {
  name?: string;
  value?: number;
  unit?: string;
  role?: string;
}

interface Competitor {
  name?: string;
  approach?: string;
  strengths?: string[];
  weaknesses?: string[];
  maturity?: string;
  source?: string;
}

interface TaxRef {
  code: string;
  name: string;
}

export interface HypothesisDetail {
  verification?: {
    method?: string;
    metrics?: string[];
    success_criteria?: string;
    horizon?: string;
  };
  drivers?: string[];
  problem_addressed?: string;
  application_potential?: { objects?: string[]; conditions?: string; constraints?: string[] };
  quantitative_parameters?: QuantParam[];
  /** Object of study — material/system class (e.g. Ni-based superalloy). */
  material_system?: string;
  /** Materials-specific passport fields: concrete levers and validation methods. */
  composition_change?: string;
  process_change?: string;
  microstructure_mechanism?: string;
  target_property?: string;
  characterization_methods?: string[];
  test_methods?: string[];
  failure_modes?: string[];
  /** Cause→effect chain (CMPP for materials: composition → process →
   *  microstructure → property → KPI). */
  causal_chain?: { stage?: string; change?: string }[];
  /** Lab experiment plan that makes the hypothesis testable. Generated on demand
   *  by POST /hypotheses/{id}/experiment (planExperiment). Every field is
   *  best-effort/optional — render defensively. The structured planner emits
   *  materials, process parameters, characterization/test methods, controls,
   *  success criteria, cost/time estimates and risks; older plans carry only the
   *  legacy variables/methods/success_criteria/horizon, so both shapes are kept
   *  (success_criteria may be a single string OR an array). */
  experiment_plan?: {
    /** Domain-specific experiment class chosen by the planner. */
    experiment_type?:
      | "new_alloy"
      | "process_route"
      | "coating_corrosion"
      | "battery_material"
      | "generic"
      | string;
    /** Domain-specific plan sections, e.g. alloy charge → melting → heat treatment. */
    sections?: { title?: string; purpose?: string; items?: string[] }[];
    /** Materials / reagents to prepare. */
    materials?: string[];
    /** Process parameters to set or vary, e.g. {name: "температура", range: "1200–1400 °C"}. */
    process_parameters?: { name?: string; range?: string }[];
    /** Characterization methods (SEM, XRD, …). */
    characterization_methods?: string[];
    /** Test / property-measurement methods. */
    test_methods?: string[];
    /** Control samples / baseline conditions. */
    controls?: string[];
    /** Measurable criteria that confirm or refute the hypothesis. */
    success_criteria?: string | string[];
    /** Rough cost estimate (e.g. "low"/"medium"/"high" or free-form). */
    estimated_cost?: string;
    /** Rough time estimate (e.g. "days"/"weeks"/"months" or free-form). */
    estimated_time?: string;
    /** Risks / pitfalls to watch for. */
    risks?: string[];
    /** Generic parameter list — accepted in case the planner emits flat strings. */
    parameters?: string[];
    /** Generic method list — legacy / fallback. */
    methods?: string[];
    /** LLM model that drafted the plan. */
    model?: string;
    planned_at?: string;
    /** Legacy fields (kept for older plans and the digest export). */
    variables?: string[];
    horizon?: string;
  };
  /** Physico-chemical / engineering feasibility sanity-check. */
  feasibility?: { aspect?: string; level?: "low" | "medium" | "high" | ""; note?: string }[];
  /** Corpus-grounded competitor analysis (POST /hypotheses/{id}/competitors). */
  competitors?: {
    summary?: string;
    items?: Competitor[];
    model?: string;
    analyzed_at?: string;
  };
  /** Scientific-specialty classification (POST /hypotheses/{id}/tag). */
  classification?: {
    research_type?: "теоретическое исследование" | "практическое";
    grnti?: TaxRef[];
    vak?: TaxRef[];
    asjc?: TaxRef[];
    model?: string;
    tagged_at?: string;
  };
}

export type HypothesisStatus =
  | "draft"
  | "generated"
  | "under_review"
  | "approved"
  | "rejected"
  | "archived";

export type EvidenceStance = "supports" | "contradicts" | "context";

export interface ApiEvidence {
  id: string;
  document_id?: string;
  chunk_id: string;
  filename: string;
  snippet: string;
  stance: EvidenceStance;
  score: number | null;
  ord: number;
  /** Server-side explanation of why the fragment is attached to the hypothesis. */
  relation?: string;
  /** Numbers/units extracted from the exact fragment, if present. */
  numeric_signals?: string[];
  /** 1-based page span in the source document (PDF); absent/0 when unknown. */
  page_start?: number;
  page_end?: number;
  /** Enclosing section of the fragment, when the parser supplied structure. */
  section_heading?: string;
  /** Происхождение фрагмента: input — из документов, приложенных к цели;
   *  knowledge — из общей базы знаний; web — из внешних источников. */
  origin?: "input" | "knowledge" | "web";
}

export interface ApiHypothesis {
  id: string;
  owner_id: string;
  run_id: string;
  title: string;
  statement: string;
  rationale: string;
  method: string;
  status: HypothesisStatus;
  kpi_id?: string;
  primary_cluster_id?: string;
  trl: number | null;
  novelty_score: number | null;
  risk_score: number | null;
  value_score: number | null;
  confidence_score: number | null;
  composite_score: number | null;
  measurable: boolean;
  organization: string;
  function_area: string;
  source_type: string;
  location: string;
  tags: string[];
  assessment: Assessment;
  detail: HypothesisDetail;
  generation: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  evidence_count?: number;
  evidence_document_count?: number;
  evidence_support_count?: number;
  evidence_contradict_count?: number;
  evidence: ApiEvidence[];
}

export interface ApiRevision {
  id: string;
  hypothesis_id: string;
  revision_no: number;
  editor_id: string;
  action: string;
  summary: string;
  patch: Record<string, unknown>;
  created_at: string;
}

export interface RevisionInput {
  action: string;
  summary?: string;
  editor_id?: string;
}

export interface HypothesisListParams {
  q?: string;
  queue?: string;
  status?: string;
  kpi_id?: string;
  cluster_id?: string;
  function_area?: string;
  source_type?: string;
  organization?: string;
  min_trl?: number;
  max_trl?: number;
  tags?: string;
  /** CSV id документов: гипотезы, чьи доказательства ссылаются хотя бы на один. */
  document_ids?: string;
  order_by?: string;
  limit?: number;
  offset?: number;
  /** "ref" — лёгкие элементы (id/kpi_id/status/title + скаляры generation)
   *  для страниц-счётчиков; без параметра — полные объекты. */
  view?: "ref";
}

function queryString(params: HypothesisListParams = {}): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "" || value === "all") continue;
    q.set(key, String(value));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

export function listHypotheses(params?: HypothesisListParams): Promise<ApiHypothesis[]> {
  return request<ApiHypothesis[]>(`/hypotheses${queryString(params)}`);
}

export interface HypothesisBoardResponse {
  items: ApiHypothesis[];
  total: number;
  limit: number;
  offset: number;
  queue_counts: Record<string, number>;
  facets: Record<string, string[]>;
}

export function getHypothesisBoard(
  params?: HypothesisListParams,
): Promise<HypothesisBoardResponse> {
  return request<HypothesisBoardResponse>(`/hypotheses/board${queryString(params)}`);
}

export function getHypothesis(id: string): Promise<ApiHypothesis> {
  return request<ApiHypothesis>(`/hypotheses/${id}`);
}

/** Read-modify-write: send the full hypothesis (plus an optional audit revision). */
export function updateHypothesis(
  id: string,
  body: Partial<ApiHypothesis> & { revision?: RevisionInput },
): Promise<ApiHypothesis> {
  return putJSON<ApiHypothesis>(`/hypotheses/${id}`, body);
}

export function addHypothesisRevision(id: string, body: RevisionInput): Promise<ApiRevision> {
  return postJSON<ApiRevision>(`/hypotheses/${id}/revisions`, body);
}

type HypothesisJobKind = "generate" | "verify" | "assess_trl" | "competitors" | "refine" | "tag";

type HypothesisJobStatus = "queued" | "running" | "succeeded" | "failed";

interface HypothesisJobInput {
  hypothesis_id?: string;
  kpi_id?: string;
  kpi_title?: string;
  kpi_metric?: string;
  kpi_description?: string;
  /** Жёсткие ограничения исследователя: сырьё, бюджет, оборудование, нормативы. */
  constraints?: string;
  count?: number;
  /** Ограничить поиск доказательств этими документами (id). */
  document_ids?: string[];
}

export interface ApiHypothesisJob {
  id: string;
  owner_id: string;
  kind: HypothesisJobKind;
  status: HypothesisJobStatus;
  input: HypothesisJobInput;
  result_ids: string[];
  error?: string;
  created_at: string;
  started_at?: string;
  heartbeat_at?: string;
  finished_at?: string;
}

function createHypothesisJob(
  kind: HypothesisJobKind,
  input: HypothesisJobInput,
): Promise<ApiHypothesisJob> {
  return postJSON<ApiHypothesisJob>("/hypothesis-jobs", { kind, ...input });
}

function getHypothesisJob(id: string): Promise<ApiHypothesisJob> {
  return request<ApiHypothesisJob>(`/hypothesis-jobs/${id}`);
}

export function listHypothesisJobs(limit = 30): Promise<ApiHypothesisJob[]> {
  return request<ApiHypothesisJob[]>(`/hypothesis-jobs?limit=${limit}`);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

async function waitHypothesisJob(job: ApiHypothesisJob): Promise<ApiHypothesisJob> {
  let current = job;
  for (;;) {
    if (current.status === "succeeded") return current;
    if (current.status === "failed")
      throw new ApiError(500, current.error || i18n.t("errors.jobFailed"));
    await sleep(1500);
    current = await getHypothesisJob(job.id);
  }
}

async function runHypothesisJob(
  kind: HypothesisJobKind,
  input: HypothesisJobInput,
): Promise<ApiHypothesisJob> {
  return waitHypothesisJob(await createHypothesisJob(kind, input));
}

/** Analyze competing approaches for a hypothesis from the corpus. */
export async function analyzeCompetitors(id: string): Promise<ApiHypothesis> {
  const job = await runHypothesisJob("competitors", { hypothesis_id: id });
  return getHypothesis(job.result_ids[0] ?? id);
}

/** Multi-agent refine: verify against the corpus, then revise + re-verify if weak. */
export async function refineHypothesis(id: string): Promise<ApiHypothesis> {
  const job = await runHypothesisJob("refine", { hypothesis_id: id });
  return getHypothesis(job.result_ids[0] ?? id);
}

/** Auto-tag a hypothesis with GRNTI / VAK / Scopus specialties. */
export async function tagHypothesis(id: string): Promise<ApiHypothesis> {
  const job = await runHypothesisJob("tag", { hypothesis_id: id });
  return getHypothesis(job.result_ids[0] ?? id);
}

/** Draft a structured lab experiment plan for a hypothesis (materials, process
 *  parameters, methods, controls, success criteria, cost/time, risks). Returns
 *  the hypothesis with detail.experiment_plan populated. */
export function planExperiment(id: string): Promise<ApiHypothesis> {
  return postJSON<ApiHypothesis>(`/hypotheses/${id}/experiment`, {});
}

export function listHypothesisRevisions(id: string): Promise<ApiRevision[]> {
  return request<ApiRevision[]>(`/hypotheses/${id}/revisions`);
}

// ---- evidence graph ("Check against the knowledge base") ----

type GraphNodeKind = "hypothesis" | "document";

export interface GraphNode {
  id: string;
  kind: GraphNodeKind;
  label: string;
  /** hypotheses: lifecycle status */
  status?: string;
  /** hypotheses: readiness 1..9 */
  trl?: number | null;
  /** hypotheses: corpus-check verdict, if checked */
  verdict?: Verdict | "";
  /** documents: how many hypotheses cite it */
  degree?: number;
  /** hypotheses: bound goal — lets the graph filter by KPI */
  kpi_id?: string;
}

export interface GraphEdge {
  source: string;
  target: string;
  /** supports → confirmation, contradicts → refutation, context → reference */
  class: EvidenceStance;
  score?: number | null;
  chunk_id?: string;
}

export interface HypothesisGraphData {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

/** Owner-wide evidence graph: hypotheses ↔ cited documents, edges by evidence stance.
 *  This is separate from the typed knowledge graph used for KPI bridge directions. */
export function getHypothesisGraph(): Promise<HypothesisGraphData> {
  return request<HypothesisGraphData>("/hypotheses/graph");
}

// ---- scoring weights (expert-tunable ranking) ----

/** Editable weights for the transparent composite ranking. Each is 0..1; the
 *  server validates (≥1 positive, each in [0,1]) and returns them normalized to
 *  sum 1, after which the board re-ranks on its next load. */
export interface ScoringWeights {
  /** Соответствие цели (KPI). */
  kpi_fit: number;
  /** Доказательная база. */
  evidence: number;
  /** Новизна. */
  novelty: number;
  /** Ценность. */
  value: number;
  /** Управляемость риска. */
  risk_inv: number;
  /** Готовность (TRL). */
  trl_fit: number;
}

/** Current ranking weights. */
export function getScoringWeights(): Promise<ScoringWeights> {
  return request<ScoringWeights>("/hypotheses/scoring-weights");
}

/** Save ranking weights; returns them normalized to sum 1. */
export function saveScoringWeights(weights: ScoringWeights): Promise<ScoringWeights> {
  return putJSON<ScoringWeights>("/hypotheses/scoring-weights", weights);
}

/** Generate hypotheses for a KPI (referenced by id or created from kpi_title). */
export async function generateHypotheses(body: {
  kpi_id?: string;
  kpi_title?: string;
  kpi_metric?: string;
  kpi_description?: string;
  /** Жёсткие ограничения исследователя: сырьё, бюджет, оборудование, нормативы. */
  constraints?: string;
  count?: number;
  /** Ограничить поиск доказательств этими документами (id). */
  document_ids?: string[];
}): Promise<ApiHypothesis[]> {
  const job = await runHypothesisJob("generate", body);
  return Promise.all(job.result_ids.map((id) => getHypothesis(id)));
}
