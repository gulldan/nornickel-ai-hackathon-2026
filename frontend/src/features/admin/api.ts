// Admin / operator endpoints: account + document oversight, LLM usage/budget,
// and live pipeline-performance latencies.
import type { ApiDocument } from "@/features/document";
import { putJSON, request } from "@/shared/api/client";
import type { FileType } from "@/shared/types";

export interface ApiAccount {
  id: string;
  username: string;
  roles: string[];
  created_at: string;
}

export function adminListUsers(): Promise<ApiAccount[]> {
  return request<ApiAccount[]>("/admin/users");
}

/** Параметры страницы корпуса: серверные пагинация, поиск и фильтры. */
export interface AdminDocumentsParams {
  limit?: number;
  offset?: number;
  /** Подстрока в имени файла или заголовке, без учёта регистра. */
  q?: string;
  status?: string;
  /** Категория типа файла — как в fileTypeOf ("pdf", "docx", …). */
  type?: string;
}

/** Страница списка документов: items — только текущее окно. */
export interface AdminDocumentList {
  items: ApiDocument[];
  total: number;
  limit: number;
  offset: number;
}

export function adminListDocuments(params: AdminDocumentsParams = {}): Promise<AdminDocumentList> {
  const qs = new URLSearchParams();
  if (params.limit !== undefined) qs.set("limit", String(params.limit));
  if (params.offset !== undefined) qs.set("offset", String(params.offset));
  if (params.q) qs.set("q", params.q);
  if (params.status) qs.set("status", params.status);
  if (params.type) qs.set("type", params.type);
  const suffix = qs.size > 0 ? `?${qs.toString()}` : "";
  return request<AdminDocumentList>(`/admin/documents${suffix}`);
}

/** Агрегаты корпуса для «Обзора» — дашборд не тянет полный список. */
export interface AdminDocumentsStats {
  total: number;
  by_status: Record<string, number>;
  by_file_type: Partial<Record<FileType, number>>;
  total_size_bytes: number;
  total_chunks: number;
  /** Последние 14 календарных дней (UTC), дни без загрузок — нули. */
  uploads_by_day: { day: string; count: number }[];
  top_by_chunks: { id: string; filename: string; title?: string; chunk_count: number }[];
  indexed_pct: number;
}

export function adminDocumentsStats(): Promise<AdminDocumentsStats> {
  return request<AdminDocumentsStats>("/admin/documents/stats");
}

// ---- Live pipeline performance (latency stages from Prometheus) ----

/** One stage's p50/p95 latency in seconds; either may be absent (no samples). */
interface StageLatency {
  p50?: number;
  p95?: number;
}

/** Полная длительность фоновых задач гипотез одного вида (старт → финиш). */
export interface HypothesisJobStat {
  kind: string;
  count: number;
  p50_sec: number;
  max_sec: number;
}

/** Live per-stage latency over real traffic — the query pipeline (keyed by
 *  stage) and document ingestion (keyed by queue). No evaluation set involved.
 *  hypothesis_jobs — сводка длительности durable-задач гипотез за последние 24 ч. */
export interface PerfResponse {
  pipeline: Record<string, StageLatency>;
  ingestion: Record<string, StageLatency>;
  hypothesis_jobs: HypothesisJobStat[];
}

/** Live pipeline latencies for the Metrics «Производительность» view. */
export function getPerf(): Promise<PerfResponse> {
  return request<PerfResponse>("/admin/perf");
}

// ---- LLM usage & budget (Metrics dashboard, provider-agnostic) ----

/** One usage aggregate: requests + tokens + cost over some slice. */
interface LlmUsageRow {
  requests: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  cost_usd: number;
}

/** One provider's slice of the spend, priced in its own currency plus a ruble
 *  equivalent. `kind`: real money / notional (organizer key) / free (local). */
export type LlmProvider = LlmUsageRow & {
  key: string;
  label: string;
  currency: string;
  kind: "real" | "notional" | "free";
  cost_estimated: boolean;
  cost_rub: number;
};

export interface LlmUsage {
  range_days: number;
  from: string;
  to: string;
  /** Human label of the generation backend ("Yandex AI Studio", "OpenRouter", …). */
  provider: string;
  /** Currency symbol for all cost fields (provider currency, e.g. "₽"). */
  currency: string;
  /** true when some provider's cost is a price-list estimate, not reported. */
  cost_estimated: boolean;
  /** Unified spend in rubles across all providers (OpenRouter USD converted). */
  total_cost_rub: number;
  /** USD→RUB rate used for the unified ruble total. */
  rub_per_usd: number;
  totals: LlmUsageRow;
  /** Per-provider breakdown, each in its own currency — costs are never summed. */
  providers: LlmProvider[];
  by_day: (LlmUsageRow & { day: string })[];
  by_model: (LlmUsageRow & { model: string })[];
  by_operation: (LlmUsageRow & { operation: string })[];
  /** Average cost of a single hypothesis: the lifecycle operations summed ÷ count. */
  per_hypothesis: {
    hypotheses: number;
    requests: number;
    total_tokens: number;
    cost_usd: number;
    cost_rub: number;
    operations: string[];
  };
  budget: { credits_total: number; credits_used: number; credits_remaining: number };
  quota: {
    today_requests: number;
    daily_limit: number;
    per_min_used: number;
    per_min_limit: number;
  };
}

/** LLM usage over the last `days` (7 | 30) for the Metrics dashboard. */
export function getLlmUsage(days = 7): Promise<LlmUsage> {
  return request<LlmUsage>(`/admin/llm-usage?days=${days}`);
}

/** Глобальный рантайм-параметр: ключ = env-переменная, оверрайд живёт в БД. */
export interface AppSettingView {
  key: string;
  kind: "string" | "bool" | "number" | "secret";
  group: "llm" | "pubsearch" | "generation";
  default: string;
  override: string;
  hasOverride: boolean;
  envValue: string;
  envSet: boolean;
  source: "db" | "env" | "default";
}

export function adminGetAppSettings(): Promise<{ settings: AppSettingView[] }> {
  return request<{ settings: AppSettingView[] }>("/admin/settings");
}

/** null или "" сбрасывает оверрайд (возврат к конфигурации развёртывания). */
export function adminPutAppSettings(
  values: Record<string, string | null>,
): Promise<{ settings: AppSettingView[] }> {
  return putJSON<{ settings: AppSettingView[] }>("/admin/settings", { values });
}
