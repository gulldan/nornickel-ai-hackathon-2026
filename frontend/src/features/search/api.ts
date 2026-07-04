// RAG search backend: chats are the conversational retrieval sessions, messages
// carry the assistant answer plus its cited source chunks.
import { postJSON, request } from "@/shared/api/client";

export interface ApiSource {
  document_id: string;
  filename: string;
  chunk_id: string;
  snippet: string;
  score: number;
  // Span provenance of the cited chunk. Optional: omitted or 0/"" when unknown.
  char_start?: number;
  char_end?: number;
  page_start?: number;
  page_end?: number;
  section_heading?: string;
}

export interface ApiChat {
  id: string;
  owner_id: string;
  /** Имя автора запроса (для истории); резолвится бэкендом. */
  owner_username?: string;
  title: string;
  /** Страница, с которой начат диалог ("search"); пусто у старых записей. */
  source?: string;
  created_at: string;
}

export interface ChatPage {
  items: ApiChat[];
  total: number;
}

/** Один замеренный этап конвейера ответа. */
interface RagStage {
  stage: string;
  millis: number;
}

/** Трасса «как искали»: этапы, воронка кандидатов, деградации и отчёт о цитатах. */
export interface RagTrace {
  stages: RagStage[];
  candidatesDense: number;
  candidatesLexical: number;
  candidatesFused: number;
  candidatesReturned: number;
  degraded: boolean;
  degradedReason: string;
  abstainReason: string;
  topScore: number;
  scoreFloor: number;
  cited: string[];
  uncitedRemoved: string[];
  raptorExpanded: number;
}

export interface AnswerMeta {
  model?: string;
  cached?: boolean;
  trace?: RagTrace;
}

type UnknownObj = Record<string, unknown>;

function isObj(v: unknown): v is UnknownObj {
  return typeof v === "object" && v !== null;
}

function num(o: UnknownObj, camel: string, snake: string): number {
  const v = o[camel] ?? o[snake];
  if (typeof v === "number" && Number.isFinite(v)) return v;
  // protojson кодирует int64 строкой ("9205") — принимаем обе формы.
  if (typeof v === "string" && v !== "" && Number.isFinite(Number(v))) return Number(v);
  return 0;
}

function str(o: UnknownObj, camel: string, snake: string): string {
  const v = o[camel] ?? o[snake];
  return typeof v === "string" ? v : "";
}

function strs(o: UnknownObj, camel: string, snake: string): string[] {
  const v = o[camel] ?? o[snake];
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

/** Нормализует meta сообщения (protojson camelCase или snake_case) в AnswerMeta. */
export function parseAnswerMeta(meta: unknown): AnswerMeta | undefined {
  let m: unknown = meta;
  if (typeof m === "string") {
    if (!m.trim()) return undefined;
    try {
      m = JSON.parse(m);
    } catch {
      return undefined;
    }
  }
  if (!isObj(m)) return undefined;
  const t = m.trace;
  const trace: RagTrace | undefined = isObj(t)
    ? {
        stages: Array.isArray(t.stages)
          ? t.stages.filter(isObj).map((s) => ({
              stage: str(s, "stage", "stage"),
              millis: num(s, "millis", "millis"),
            }))
          : [],
        candidatesDense: num(t, "candidatesDense", "candidates_dense"),
        candidatesLexical: num(t, "candidatesLexical", "candidates_lexical"),
        candidatesFused: num(t, "candidatesFused", "candidates_fused"),
        candidatesReturned: num(t, "candidatesReturned", "candidates_returned"),
        degraded: t.degraded === true,
        degradedReason: str(t, "degradedReason", "degraded_reason"),
        abstainReason: str(t, "abstainReason", "abstain_reason"),
        topScore: num(t, "topScore", "top_score"),
        scoreFloor: num(t, "scoreFloor", "score_floor"),
        cited: strs(t, "cited", "cited"),
        uncitedRemoved: strs(t, "uncitedRemoved", "uncited_removed"),
        raptorExpanded: num(t, "raptorExpanded", "raptor_expanded"),
      }
    : undefined;
  const model = typeof m.model === "string" && m.model ? m.model : undefined;
  const cached = typeof m.cached === "boolean" ? m.cached : undefined;
  if (!trace && !model && cached === undefined) return undefined;
  return { model, cached, trace };
}

export interface ApiMessage {
  id: string;
  chat_id: string;
  role: "user" | "assistant" | "system";
  content: string;
  sources?: ApiSource[];
  created_at: string;
  /** Провенанс ответа ({model, cached, trace}) — JSON-объект или строка. */
  meta?: unknown;
}

export function listChats(params?: {
  limit?: number;
  offset?: number;
  /** Вся история (все пользователи) — сервер разрешит только администратору. */
  all?: boolean;
}): Promise<ChatPage> {
  const q = new URLSearchParams();
  if (params?.limit) q.set("limit", String(params.limit));
  if (params?.offset) q.set("offset", String(params.offset));
  if (params?.all) q.set("all", "1");
  const qs = q.toString();
  return request<ChatPage>(`/chats${qs ? `?${qs}` : ""}`);
}

export function createChat(title: string, source = "search"): Promise<ApiChat> {
  return postJSON<ApiChat>("/chats", { title, source });
}

export function listMessages(chatId: string): Promise<ApiMessage[]> {
  return request<ApiMessage[]>(`/chats/${chatId}/messages`);
}

export function sendMessage(chatId: string, content: string): Promise<ApiMessage> {
  return postJSON<ApiMessage>(`/chats/${chatId}/messages`, { content });
}
