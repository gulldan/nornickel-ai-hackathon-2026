// KPI endpoints — the business targets that seed hypothesis generation.
import { postJSON, putJSON, request } from "@/shared/api/client";
import type { ApiDocument } from "@/features/document";

/** One candidate "bridge" research direction synthesized from the corpus
 *  knowledge graph: a cross-document Process → Property → KPI combination the
 *  documents only imply when read together. Backend for this is still settling,
 *  so every field is optional except a best-effort `statement`. */
export interface GraphHypothesis {
  /** The proposed direction, in plain Russian (always present in practice). */
  statement: string;
  /** Optional short headline. */
  title?: string;
  /** Provenance: the graph path that suggested it (process / property / KPI). */
  process?: string;
  property?: string;
  kpi?: string;
  /** Free-form note on where in the graph this bridge came from. */
  rationale?: string;
  /** Source documents that, combined, imply the bridge. */
  documents?: { id?: string; filename?: string }[];
  /** Confidence / strength 0..1, if the service scored it. */
  score?: number;
}

export interface ApiKPI {
  id: string;
  owner_id: string;
  title: string;
  description: string;
  metric: string;
  unit: string;
  direction: string;
  baseline: number | null;
  target: number | null;
  function_area: string;
  status: string;
  detail: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface KpiInput {
  title: string;
  description?: string;
  metric?: string;
  unit?: string;
  /** increase | decrease | maintain */
  direction?: string;
  baseline?: number | null;
  target?: number | null;
  function_area?: string;
  /** active | archived — мягкое архивирование вместо удаления. */
  status?: string;
  detail?: Record<string, unknown>;
}

/** One candidate goal (KPI) the backend extracted from a representative sample of
 *  the corpus: a measurable property the loaded works optimize. Advisory only —
 *  nothing is stored until the user accepts it into the create-goal dialog. Every
 *  field but `title` is best-effort, mirroring the backend's tolerant schema. */
export interface KpiSuggestion {
  /** Short goal statement, in Russian (always present in practice). */
  title: string;
  /** The measurable metric (e.g. «предел текучести»). */
  metric?: string;
  /** Unit of the metric, when clear («МПа», «%», «мА·ч/г»). */
  unit?: string;
  /** increase | decrease | maintain */
  direction?: string;
  /** Function area / domain (e.g. «Жаропрочные сплавы»). */
  function_area?: string;
  /** Which works imply the goal and why. */
  rationale?: string;
}

/** Структурная цель, извлечённая LLM из свободного текста промпта.
 *  Бэкенд никогда не падает: в худшем случае первая строка становится title. */
export interface ParsedKpiPrompt {
  title: string;
  description?: string;
  metric?: string;
  unit?: string;
  direction?: string;
  function_area?: string;
  constraints?: string;
  baseline?: number | null;
  target?: number | null;
}

/** Документ, приложенный к цели как «входные данные предприятия». */
export interface KpiDocument extends ApiDocument {
  role: string;
  attached_at: string;
}

export function listKPIs(): Promise<ApiKPI[]> {
  return request<ApiKPI[]>("/kpis");
}

export async function parseKpiPrompt(prompt: string): Promise<ParsedKpiPrompt> {
  const raw = await postJSON<{ kpi?: ParsedKpiPrompt }>("/kpis/parse", { prompt });
  return raw?.kpi ?? { title: "" };
}

export async function listKpiDocuments(kpiId: string): Promise<KpiDocument[]> {
  const raw = await request<{ documents?: KpiDocument[] }>(`/kpis/${kpiId}/documents`);
  return raw?.documents ?? [];
}

export function attachKpiDocuments(kpiId: string, documentIds: string[]): Promise<void> {
  return postJSON<void>(`/kpis/${kpiId}/documents`, { document_ids: documentIds });
}

export function detachKpiDocument(kpiId: string, documentId: string): Promise<void> {
  return request<void>(`/kpis/${kpiId}/documents/${documentId}`, { method: "DELETE" });
}

export function createKPI(body: KpiInput): Promise<ApiKPI> {
  return postJSON<ApiKPI>("/kpis", body);
}

export function updateKPI(id: string, body: KpiInput): Promise<ApiKPI> {
  return putJSON<ApiKPI>(`/kpis/${id}`, body);
}

/** Удаляет цель; созданные для неё гипотезы остаются и просто отвязываются. */
export function deleteKPI(id: string): Promise<void> {
  return request<void>(`/kpis/${id}`, { method: "DELETE" });
}

/** Ask the backend to mine candidate goals from a representative sample of the
 *  corpus (one LLM pass). The reply is wrapped under `suggestions`; we accept
 *  `suggestions`, `items` or a bare array so a contract shift never breaks the UI.
 *  Always resolves to an array (empty when the corpus is too small or the LLM is
 *  unavailable — the endpoint never 500s for that). */
export async function suggestKPIs(): Promise<KpiSuggestion[]> {
  const raw = await postJSON<unknown>("/kpis/suggest", {});
  if (Array.isArray(raw)) {
    return raw as KpiSuggestion[];
  }
  const obj = (raw ?? {}) as { suggestions?: unknown; items?: unknown };
  const list = Array.isArray(obj.suggestions)
    ? obj.suggestions
    : Array.isArray(obj.items)
      ? obj.items
      : [];
  return list as KpiSuggestion[];
}

/** Synthesize candidate "bridge" research directions for a KPI from the corpus
 *  knowledge graph (cross-document Process → Property → KPI combinations).
 *  The backend wraps the list under `candidates`; we accept `candidates`,
 *  `items` or a bare array so a contract shift never breaks the UI. Always
 *  resolves to an array (empty when the graph has no bridges yet). */
export async function graphHypotheses(kpiId: string): Promise<GraphHypothesis[]> {
  const raw = await postJSON<unknown>(`/kpis/${kpiId}/graph-hypotheses`, {});
  if (Array.isArray(raw)) {
    return raw as GraphHypothesis[];
  }
  const obj = (raw ?? {}) as { candidates?: unknown; items?: unknown };
  const list = Array.isArray(obj.candidates)
    ? obj.candidates
    : Array.isArray(obj.items)
      ? obj.items
      : [];
  return list as GraphHypothesis[];
}

/** Сохраняет направление из графа знаний черновиком гипотезы на доску —
 *  иначе список направлений живёт только в памяти страницы и теряется. */
export function saveDirectionAsHypothesis(
  kpiId: string,
  item: GraphHypothesis,
): Promise<{ id: string }> {
  const statement = (item.statement ?? "").trim() || (item.title ?? "").trim();
  return postJSON<{ id: string }>("/hypotheses", {
    title: (item.title ?? "").trim() || statement.slice(0, 120),
    statement,
    rationale: (item.rationale ?? "").trim(),
    method: "manual",
    status: "draft",
    kpi_id: kpiId,
    measurable: false,
    tags: ["graph_direction"],
    generation: {
      kind: "graph_direction",
      process: item.process ?? "",
      property: item.property ?? "",
      kpi: item.kpi ?? "",
      documents: (item.documents ?? []).map((d) => d.id).filter(Boolean),
    },
  });
}
