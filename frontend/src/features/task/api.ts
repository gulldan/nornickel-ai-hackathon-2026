// Пульс фоновой фабрики знаний: main-service агрегирует статусы воркеров
// (темы/мосты/саммари/технологичность) из Valkey в GET /system/activity.
import { request } from "@/shared/api/client";
import { i18n } from "@/shared/i18n";

type WorkerState = "idle" | "running" | "error";

export interface WorkerStatus {
  name: string;
  state: WorkerState;
  epoch: number | null;
  progressDone: number | null;
  progressTotal: number | null;
  updatedAt: string | null;
  lastError: string | null;
  /** Длительность последнего успешного прогона, сек; null у старых статусов. */
  lastRunSeconds: number | null;
}

export interface SystemActivity {
  corpusEpoch: number;
  workers: WorkerStatus[];
  lastEpochs: Partial<Record<"clusters" | "discovery" | "raptor", number>>;
}

// Подписи воркеров — в словаре task (workers.*); геттеры сохраняют доступ
// WORKER_LABELS[name] и переводятся при смене языка.
export const WORKER_LABELS: Record<string, string> = {
  get clusters() {
    return i18n.t("task:workers.clusters");
  },
  get discovery() {
    return i18n.t("task:workers.discovery");
  },
  get raptor() {
    return i18n.t("task:workers.raptor");
  },
  get itc() {
    return i18n.t("task:workers.itc");
  },
  get eval() {
    return i18n.t("task:workers.eval");
  },
};

type UnknownObj = Record<string, unknown>;

function isObj(v: unknown): v is UnknownObj {
  return typeof v === "object" && v !== null;
}

function intOrNull(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

function strOrNull(v: unknown): string | null {
  return typeof v === "string" && v !== "" ? v : null;
}

export async function getSystemActivity(): Promise<SystemActivity> {
  const raw = await request<unknown>("/system/activity");
  const o: UnknownObj = isObj(raw) ? raw : {};
  const workersRaw = Array.isArray(o.workers) ? o.workers : [];
  const workers: WorkerStatus[] = workersRaw.filter(isObj).map((w) => {
    const state = w.state === "running" || w.state === "error" ? w.state : "idle";
    return {
      name: typeof w.name === "string" ? w.name : "",
      state,
      epoch: intOrNull(w.epoch),
      progressDone: intOrNull(w.progress_done ?? w.progressDone),
      progressTotal: intOrNull(w.progress_total ?? w.progressTotal),
      updatedAt: strOrNull(w.updated_at ?? w.updatedAt),
      lastError: strOrNull(w.last_error ?? w.lastError),
      lastRunSeconds: intOrNull(w.last_run_seconds ?? w.lastRunSeconds),
    };
  });
  const le = isObj(o.last_epochs ?? o.lastEpochs) ? (o.last_epochs ?? o.lastEpochs) : {};
  const lastEpochs: SystemActivity["lastEpochs"] = {};
  if (isObj(le)) {
    for (const key of ["clusters", "discovery", "raptor"] as const) {
      const v = intOrNull(le[key]);
      if (v !== null) lastEpochs[key] = v;
    }
  }
  return {
    corpusEpoch: intOrNull(o.corpus_epoch ?? o.corpusEpoch) ?? 0,
    workers,
    lastEpochs,
  };
}
