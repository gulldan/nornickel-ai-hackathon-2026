import { putJSON, request } from "@/shared/api/client";

const STORAGE_KEY = "rag:hypothesis-runtime-settings:v1";
export const GENERATION_COUNT_MIN = 3;
export const GENERATION_COUNT_MAX = 10;

export interface HypothesisRuntimeSettings {
  defaultGenerateCount: number;
  clusterGenerateCount: number;
  directionGenerateCount: number;
  generationTimeoutSec: number;
  readyTrlMin: number;
  readyScoreMin: number;
  riskScoreMin: number;
  graphDirectionLimit: number;
  deepPostprocessEnabled: boolean;
  /** Исключённые направления — фабрика не предлагает гипотез в них. */
  excludedDirections: string;
}

interface HypothesisRuntimeSettingsDTO {
  default_generate_count?: number;
  cluster_generate_count?: number;
  direction_generate_count?: number;
  generation_timeout_sec?: number;
  ready_trl_min?: number;
  ready_score_min?: number;
  risk_score_min?: number;
  graph_direction_limit?: number;
  deep_postprocess_enabled?: boolean;
  excluded_directions?: string;
}

const DEFAULT_HYPOTHESIS_RUNTIME_SETTINGS: HypothesisRuntimeSettings = {
  defaultGenerateCount: 5,
  clusterGenerateCount: 3,
  directionGenerateCount: 3,
  generationTimeoutSec: 300,
  readyTrlMin: 4,
  readyScoreMin: 55,
  riskScoreMin: 70,
  graphDirectionLimit: 5,
  deepPostprocessEnabled: false,
  excludedDirections: "",
};

function clampInt(value: unknown, min: number, max: number, fallback: number): number {
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, Math.round(n)));
}

function normalizeHypothesisRuntimeSettings(
  value: Partial<HypothesisRuntimeSettings>,
): HypothesisRuntimeSettings {
  const defaults = DEFAULT_HYPOTHESIS_RUNTIME_SETTINGS;
  return {
    defaultGenerateCount: clampInt(
      value.defaultGenerateCount,
      GENERATION_COUNT_MIN,
      GENERATION_COUNT_MAX,
      defaults.defaultGenerateCount,
    ),
    clusterGenerateCount: clampInt(
      value.clusterGenerateCount,
      GENERATION_COUNT_MIN,
      GENERATION_COUNT_MAX,
      defaults.clusterGenerateCount,
    ),
    directionGenerateCount: clampInt(
      value.directionGenerateCount,
      1,
      GENERATION_COUNT_MAX,
      defaults.directionGenerateCount,
    ),
    generationTimeoutSec: clampInt(
      value.generationTimeoutSec,
      30,
      600,
      defaults.generationTimeoutSec,
    ),
    readyTrlMin: clampInt(value.readyTrlMin, 1, 9, defaults.readyTrlMin),
    readyScoreMin: clampInt(value.readyScoreMin, 0, 100, defaults.readyScoreMin),
    riskScoreMin: clampInt(value.riskScoreMin, 0, 100, defaults.riskScoreMin),
    graphDirectionLimit: clampInt(value.graphDirectionLimit, 1, 20, defaults.graphDirectionLimit),
    deepPostprocessEnabled: Boolean(value.deepPostprocessEnabled),
    excludedDirections: (value.excludedDirections ?? "").slice(0, 600),
  };
}

function fromDTO(value: HypothesisRuntimeSettingsDTO): HypothesisRuntimeSettings {
  return normalizeHypothesisRuntimeSettings({
    defaultGenerateCount: value.default_generate_count,
    clusterGenerateCount: value.cluster_generate_count,
    directionGenerateCount: value.direction_generate_count,
    generationTimeoutSec: value.generation_timeout_sec,
    readyTrlMin: value.ready_trl_min,
    readyScoreMin: value.ready_score_min,
    riskScoreMin: value.risk_score_min,
    graphDirectionLimit: value.graph_direction_limit,
    deepPostprocessEnabled: value.deep_postprocess_enabled,
    excludedDirections: value.excluded_directions,
  });
}

function toDTO(value: HypothesisRuntimeSettings): HypothesisRuntimeSettingsDTO {
  const normalized = normalizeHypothesisRuntimeSettings(value);
  return {
    default_generate_count: normalized.defaultGenerateCount,
    cluster_generate_count: normalized.clusterGenerateCount,
    direction_generate_count: normalized.directionGenerateCount,
    generation_timeout_sec: normalized.generationTimeoutSec,
    ready_trl_min: normalized.readyTrlMin,
    ready_score_min: normalized.readyScoreMin,
    risk_score_min: normalized.riskScoreMin,
    graph_direction_limit: normalized.graphDirectionLimit,
    deep_postprocess_enabled: normalized.deepPostprocessEnabled,
    excluded_directions: normalized.excludedDirections,
  };
}

export function loadHypothesisRuntimeSettings(): HypothesisRuntimeSettings {
  if (typeof localStorage === "undefined") return DEFAULT_HYPOTHESIS_RUNTIME_SETTINGS;
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_HYPOTHESIS_RUNTIME_SETTINGS;
    return normalizeHypothesisRuntimeSettings(
      JSON.parse(raw) as Partial<HypothesisRuntimeSettings>,
    );
  } catch {
    return DEFAULT_HYPOTHESIS_RUNTIME_SETTINGS;
  }
}

function saveHypothesisRuntimeSettings(
  value: Partial<HypothesisRuntimeSettings>,
): HypothesisRuntimeSettings {
  const normalized = normalizeHypothesisRuntimeSettings(value);
  if (typeof localStorage !== "undefined") {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(normalized));
    window.dispatchEvent(new CustomEvent("hypothesis-runtime-settings:update"));
  }
  return normalized;
}

export async function fetchHypothesisRuntimeSettings(): Promise<HypothesisRuntimeSettings> {
  const dto = await request<HypothesisRuntimeSettingsDTO>("/hypotheses/runtime-settings");
  return saveHypothesisRuntimeSettings(fromDTO(dto));
}

export async function persistHypothesisRuntimeSettings(
  value: Partial<HypothesisRuntimeSettings>,
): Promise<HypothesisRuntimeSettings> {
  const dto = await putJSON<HypothesisRuntimeSettingsDTO>(
    "/hypotheses/runtime-settings",
    toDTO(normalizeHypothesisRuntimeSettings(value)),
  );
  return saveHypothesisRuntimeSettings(fromDTO(dto));
}

export function persistDefaultHypothesisRuntimeSettings(): Promise<HypothesisRuntimeSettings> {
  return persistHypothesisRuntimeSettings(DEFAULT_HYPOTHESIS_RUNTIME_SETTINGS);
}
