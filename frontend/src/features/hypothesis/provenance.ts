// Типизированный разбор JSONB-поля generation: происхождение гипотезы
// (мост между темами / тема / генерация по цели / fallback) и результаты
// анти-хайп проверок discovery-воркера. Все поля best-effort — старые записи
// рендерятся без гейтов и скоров.
import type { ApiHypothesis } from "@/features/hypothesis/api";

type UnknownObj = Record<string, unknown>;

function isObj(v: unknown): v is UnknownObj {
  return typeof v === "object" && v !== null;
}

function numOf(o: UnknownObj, key: string): number | undefined {
  const v = o[key];
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function strOf(o: UnknownObj, key: string): string | undefined {
  const v = o[key];
  return typeof v === "string" && v.trim() !== "" ? v.trim() : undefined;
}

function boolOf(o: UnknownObj, key: string): boolean | undefined {
  const v = o[key];
  return typeof v === "boolean" ? v : undefined;
}

interface BridgeScores {
  affinity?: number;
  maverick?: number;
  vanguard?: number;
  bridgingCentrality?: number;
  convergence?: number;
  composite?: number;
}

interface GateCheck {
  passed?: boolean;
}

interface ConvergenceGate extends GateCheck {
  required?: number;
  actual?: number;
}

interface NoveltyGate extends GateCheck {
  threshold?: number;
  topSim?: number;
  nearestDocId?: string;
  nearestFilename?: string;
}

interface BridgeGates {
  convergence?: ConvergenceGate;
  grounding?: GateCheck;
  novelty?: NoveltyGate;
}

type GenerationKind = "auto_bridge" | "auto_cluster" | "on_demand" | "fallback" | "unknown";

export interface GenerationInfo {
  kind: GenerationKind;
  model?: string;
  promptVersion?: string;
  clusterLabel?: string;
  themeA?: string;
  themeB?: string;
  /** Document ids документов-посредников моста. */
  mediators: string[];
  scores?: BridgeScores;
  gates?: BridgeGates;
  fallbackReason?: string;
  /** Ограничения исследователя (сырьё, бюджет, оборудование, нормативы), заданные при генерации. */
  constraints?: string;
  /** Открытые публикации (мировая практика), учтённые при генерации. */
  externalWorks: ExternalWork[];
}

export interface ExternalWork {
  title: string;
  year?: number;
  doi?: string;
  venue?: string;
  source?: string;
}

function kindOf(g: UnknownObj): GenerationKind {
  const kind = strOf(g, "kind");
  if (kind === "auto_bridge") return "auto_bridge";
  if (kind === "auto_cluster" || strOf(g, "semantic_kind") === "research_direction") {
    return "auto_cluster";
  }
  if (kind === "fallback_evidence" || strOf(g, "fallback_reason") !== undefined) return "fallback";
  if (kind === "on_demand") return "on_demand";
  return "unknown";
}

export function parseGeneration(h: Pick<ApiHypothesis, "generation" | "method">): GenerationInfo {
  const g: UnknownObj = isObj(h.generation) ? h.generation : {};
  const kind = kindOf(g);

  const mediators: string[] = Array.isArray(g.mediators)
    ? g.mediators.filter((m): m is string => typeof m === "string" && m !== "")
    : [];

  const rawScores = g.scores;
  const scores: BridgeScores | undefined = isObj(rawScores)
    ? {
        affinity: numOf(rawScores, "affinity"),
        maverick: numOf(rawScores, "maverick"),
        vanguard: numOf(rawScores, "vanguard"),
        bridgingCentrality: numOf(rawScores, "bridging_centrality"),
        convergence: numOf(rawScores, "convergence"),
        composite: numOf(rawScores, "composite"),
      }
    : undefined;

  const rawGates = g.gates;
  let gates: BridgeGates | undefined;
  if (isObj(rawGates)) {
    gates = {};
    if (isObj(rawGates.convergence)) {
      gates.convergence = {
        required: numOf(rawGates.convergence, "required"),
        actual: numOf(rawGates.convergence, "actual"),
        passed: boolOf(rawGates.convergence, "passed"),
      };
    }
    if (isObj(rawGates.grounding)) {
      gates.grounding = { passed: boolOf(rawGates.grounding, "passed") };
    }
    if (isObj(rawGates.novelty)) {
      gates.novelty = {
        threshold: numOf(rawGates.novelty, "threshold"),
        topSim: numOf(rawGates.novelty, "top_sim"),
        nearestDocId: strOf(rawGates.novelty, "nearest_doc_id"),
        nearestFilename: strOf(rawGates.novelty, "nearest_filename"),
        passed: boolOf(rawGates.novelty, "passed"),
      };
    }
  }
  // Совместимость: у части записей проба новизны лежит рядом с gates.
  if (!gates?.novelty && isObj(g.novelty_probe)) {
    gates = gates ?? {};
    gates.novelty = {
      topSim: numOf(g.novelty_probe, "top_sim"),
      nearestDocId: strOf(g.novelty_probe, "nearest_doc_id"),
      nearestFilename: strOf(g.novelty_probe, "nearest_filename"),
    };
  }

  return {
    kind: kind === "unknown" && h.method === "combination" ? "auto_bridge" : kind,
    model: strOf(g, "model"),
    promptVersion: strOf(g, "prompt_version"),
    clusterLabel: strOf(g, "cluster_label"),
    themeA: strOf(g, "theme_a"),
    themeB: strOf(g, "theme_b"),
    mediators,
    scores,
    gates,
    fallbackReason: strOf(g, "fallback_reason"),
    constraints: isObj(g.inputs) ? strOf(g.inputs, "constraints") : undefined,
    externalWorks: parseExternalWorks(g),
  };
}

function parseExternalWorks(g: UnknownObj): ExternalWork[] {
  const inputs = g.inputs;
  if (!isObj(inputs) || !Array.isArray(inputs.external_works)) return [];
  const out: ExternalWork[] = [];
  for (const raw of inputs.external_works) {
    if (!isObj(raw)) continue;
    const title = strOf(raw, "title");
    if (!title) continue;
    out.push({
      title,
      year: numOf(raw, "year"),
      doi: strOf(raw, "doi"),
      venue: strOf(raw, "venue"),
      source: strOf(raw, "source"),
    });
  }
  return out;
}
