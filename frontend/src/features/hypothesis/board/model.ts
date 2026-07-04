// Логика доски гипотез: очереди, сортировки, вердикты, происхождение,
// счётчики доказательств. Чистые функции без React.
import type { ApiCluster } from "@/features/cluster";
import type { ApiHypothesis } from "@/features/hypothesis/api";
import {
  cleanInternalTitle,
  isResearchDirection,
  priorityScore,
} from "@/features/hypothesis/model";
import { parseGeneration } from "@/features/hypothesis/provenance";
import {
  GENERATION_COUNT_MAX,
  GENERATION_COUNT_MIN,
  type HypothesisRuntimeSettings,
} from "@/shared/appSettings";
import { i18n } from "@/shared/i18n";

export type QueueKey = "all" | "needs_verify" | "needs_trl" | "ready" | "risk" | "insufficient";
export type KindTab = "hypotheses" | "directions" | "drafts" | "graph" | "methodology";
export type SortKey = "rank" | "learning" | "itc" | "novelty" | "trl" | "recent";

export const BOARD_LIMIT = 200;
export const KIND_TABS = new Set<KindTab>([
  "hypotheses",
  "directions",
  "drafts",
  "graph",
  "methodology",
]);

// Sort options for the board. Each maps a hypothesis to a number; the list is
// sorted descending (best/newest first). "rank" uses the transparent composite
// ranking, so the default order is the explainable "best to test first".
// Подписи — ключи неймспейса hypothesis, t() дергается в рендере.
export const SORTS: {
  key: SortKey;
  labelKey: `sorts.${SortKey}`;
  value: (h: ApiHypothesis) => number;
}[] = [
  { key: "rank", labelKey: "sorts.rank", value: (h) => priorityScore(h) ?? 0 },
  {
    key: "learning",
    labelKey: "sorts.learning",
    value: (h) => h.assessment?.learning_priority?.score ?? -1,
  },
  {
    key: "itc",
    labelKey: "sorts.itc",
    value: (h) => h.assessment?.itc?.techscore ?? (h.assessment?.itc?.score ?? 0) / 10,
  },
  {
    key: "novelty",
    labelKey: "sorts.novelty",
    value: (h) => h.assessment?.itc?.axes?.novelty ?? h.novelty_score ?? 0,
  },
  { key: "trl", labelKey: "sorts.trl", value: (h) => h.trl ?? 0 },
  { key: "recent", labelKey: "sorts.recent", value: (h) => Date.parse(h.created_at) || 0 },
];

export function serverOrderBy(sortBy: SortKey): string {
  if (sortBy === "recent") return "created_at";
  if (sortBy === "trl") return "trl";
  if (sortBy === "novelty") return "novelty";
  return "";
}

type QueueDictKey = "all" | "needsVerify" | "ready" | "risk" | "insufficient";

export const WORK_QUEUES: {
  key: QueueKey;
  labelKey: `queues.${QueueDictKey}.label`;
  hintKey: `queues.${QueueDictKey}.hint`;
}[] = [
  { key: "ready", labelKey: "queues.ready.label", hintKey: "queues.ready.hint" },
  { key: "risk", labelKey: "queues.risk.label", hintKey: "queues.risk.hint" },
  {
    key: "needs_verify",
    labelKey: "queues.needsVerify.label",
    hintKey: "queues.needsVerify.hint",
  },
  {
    key: "insufficient",
    labelKey: "queues.insufficient.label",
    hintKey: "queues.insufficient.hint",
  },
  { key: "all", labelKey: "queues.all.label", hintKey: "queues.all.hint" },
];

export function verdictOf(h: ApiHypothesis): string {
  return h.assessment?.check?.verdict ?? "";
}

export function isClosed(h: ApiHypothesis): boolean {
  return h.status === "rejected" || h.status === "archived";
}

/** Single work queue a hypothesis belongs to — a partition keyed on the verify
 *  verdict so a hypothesis is in exactly one queue (never both «Готовые» and
 *  «Противоречия»). Closed (rejected/archived) belongs to no work queue, only
 *  «Все». Risk score and TRL are card signals, not queue selectors. */
function primaryQueue(h: ApiHypothesis): QueueKey | null {
  if (isClosed(h)) return null;
  const verdict = verdictOf(h);
  if (h.status === "approved" || verdict === "supported") return "ready";
  if (verdict === "refuted" || verdict === "mixed") return "risk";
  if (verdict === "insufficient") return "insufficient";
  return "needs_verify";
}

export function matchesQueue(
  h: ApiHypothesis,
  queue: QueueKey,
  _settings: HypothesisRuntimeSettings,
): boolean {
  if (queue === "all") return true;
  if (queue === "needs_trl") return !isClosed(h) && h.trl === null;
  return primaryQueue(h) === queue;
}

export type BadgeTone = "ok" | "risk" | "warn" | "secondary" | "brand";

export function verdictMeta(h: ApiHypothesis): { label: string; tone: BadgeTone } | null {
  switch (verdictOf(h)) {
    case "supported":
      return { label: i18n.t("hypothesis:verdicts.supported"), tone: "ok" };
    case "refuted":
      return { label: i18n.t("hypothesis:verdicts.refuted"), tone: "risk" };
    case "mixed":
      return { label: i18n.t("hypothesis:verdicts.mixed"), tone: "warn" };
    case "insufficient":
      return { label: i18n.t("hypothesis:verdicts.insufficient"), tone: "secondary" };
    default:
      return null;
  }
}

/** Происхождение гипотезы одним бейджем: мост / тема / цель / вручную. */
export function originMeta(h: ApiHypothesis): { label: string; tone: BadgeTone } | null {
  const gen = parseGeneration(h);
  if (gen.kind === "auto_bridge" || h.method === "combination" || h.tags.includes("auto_bridge")) {
    return { label: i18n.t("hypothesis:origins.bridge"), tone: "brand" };
  }
  if (gen.kind === "auto_cluster" || isResearchDirection(h)) {
    return { label: i18n.t("hypothesis:origins.topic"), tone: "secondary" };
  }
  if (h.method === "manual")
    return { label: i18n.t("hypothesis:origins.manual"), tone: "secondary" };
  if (h.kpi_id) return { label: i18n.t("hypothesis:origins.goal"), tone: "secondary" };
  return null;
}

export interface EvidenceStats {
  total: number;
  docs: number;
  supports: number;
  contradicts: number;
  context: number;
}

export function evidenceStats(h: ApiHypothesis): EvidenceStats {
  const check = h.assessment?.check;
  const supports =
    h.evidence_support_count ??
    check?.supporting?.length ??
    h.evidence.filter((e) => e.stance === "supports").length;
  const contradicts =
    h.evidence_contradict_count ??
    check?.contradicting?.length ??
    h.evidence.filter((e) => e.stance === "contradicts").length;
  const total =
    h.evidence_count ??
    Math.max(
      supports + contradicts,
      h.evidence.length,
      (check?.supporting?.length ?? 0) + (check?.contradicting?.length ?? 0),
    );
  const docs =
    h.evidence_document_count ??
    new Set(
      h.evidence
        .map((e) => e.document_id || e.filename || e.chunk_id)
        .filter((v): v is string => Boolean(v)),
    ).size;
  return {
    total,
    docs,
    supports,
    contradicts,
    context: Math.max(0, total - supports - contradicts),
  };
}

export function clusterLabel(c: ApiCluster): string {
  return cleanInternalTitle(c.label);
}

export function clampGenerateCount(value: string): number {
  const n = Number(value);
  if (!Number.isFinite(n)) return GENERATION_COUNT_MIN;
  return Math.max(GENERATION_COUNT_MIN, Math.min(GENERATION_COUNT_MAX, Math.round(n)));
}
