import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { toast } from "sonner";

import { listHypothesisJobs, type ApiHypothesisJob } from "@/features/hypothesis";
import { getSystemActivity, type SystemActivity } from "@/features/task/api";
import { i18n } from "@/shared/i18n";

type BackgroundTaskStatus = "running" | "success" | "error";

export interface BackgroundTask {
  id: string;
  key?: string;
  title: string;
  description?: string;
  status: BackgroundTaskStatus;
  startedAt: string;
  finishedAt?: string;
  error?: string;
}

interface RunTaskOptions<T> {
  key?: string;
  title: string;
  description?: string;
  successMessage?: string | ((result: T) => string);
  errorMessage?: string | ((error: unknown) => string);
  run: () => Promise<T>;
  onSuccess?: (result: T) => void;
  onError?: (error: unknown) => void;
}

interface TaskContextValue {
  tasks: BackgroundTask[];
  activeCount: number;
  /** Пульс фоновой фабрики знаний (темы/мосты/саммари); null — ещё не получен. */
  activity: SystemActivity | null;
  runTask: <T>(options: RunTaskOptions<T>) => Promise<T | undefined>;
  isTaskRunning: (key: string) => boolean;
  clearFinished: () => void;
}

const TaskContext = createContext<TaskContextValue | null>(null);

function taskID(): string {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : i18n.t("task:toasts.failed");
}

/** Подписи видов фоновых задач гипотез — общие для уведомлений и метрик.
 *  Тексты в словаре task (jobs.*); геттеры сохраняют доступ JOB_TITLES[kind]. */
export const JOB_TITLES: Record<string, string> = {
  get generate() {
    return i18n.t("task:jobs.generate");
  },
  get verify() {
    return i18n.t("task:jobs.verify");
  },
  get enrich() {
    return i18n.t("task:jobs.enrich");
  },
  get assess_trl() {
    return i18n.t("task:jobs.assess_trl");
  },
  get competitors() {
    return i18n.t("task:jobs.competitors");
  },
  get refine() {
    return i18n.t("task:jobs.refine");
  },
  get tag() {
    return i18n.t("task:jobs.tag");
  },
};

const AUTO_JOB_KINDS = new Set(["verify", "assess_trl"]);
const RUNNING_JOB_FRESH_MS = 120_000;

function shouldShowBackendJob(job: ApiHypothesisJob): boolean {
  if (!AUTO_JOB_KINDS.has(job.kind)) return true;
  if (job.status !== "running") return false;
  const heartbeat = Date.parse(job.heartbeat_at || job.started_at || job.created_at);
  return Number.isFinite(heartbeat) && Date.now() - heartbeat <= RUNNING_JOB_FRESH_MS;
}

function taskFromJob(job: ApiHypothesisJob): BackgroundTask {
  return {
    id: `job:${job.id}`,
    key: `job:${job.id}`,
    title: JOB_TITLES[job.kind] ?? i18n.t("task:jobs.fallback"),
    description: job.input.kpi_title || job.input.hypothesis_id || job.input.kpi_id,
    status: job.status === "failed" ? "error" : job.status === "succeeded" ? "success" : "running",
    startedAt: job.started_at || job.created_at,
    finishedAt: job.finished_at,
    error: job.error,
  };
}

function message<T>(
  value: string | ((result: T) => string) | undefined,
  result: T,
  fallback: string,
): string {
  return typeof value === "function" ? value(result) : (value ?? fallback);
}

function errorMessage(
  value: string | ((error: unknown) => string) | undefined,
  error: unknown,
): string {
  return typeof value === "function" ? value(error) : (value ?? errorText(error));
}

export function TaskProvider({ children }: { children: ReactNode }) {
  const [tasks, setTasks] = useState<BackgroundTask[]>([]);
  const [activity, setActivity] = useState<SystemActivity | null>(null);

  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      try {
        const next = await getSystemActivity();
        if (!cancelled) setActivity(next);
      } catch {
        // Эндпоинт может отсутствовать на старом бэкенде — пульс просто скрыт.
      }
    };
    void poll();
    const id = window.setInterval(() => void poll(), 10_000);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const refresh = async () => {
      try {
        const jobs = await listHypothesisJobs(20);
        if (cancelled) return;
        const backendTasks = jobs.filter(shouldShowBackendJob).map(taskFromJob);
        setTasks((prev) => {
          const localTasks = prev.filter((task) => !task.id.startsWith("job:"));
          return [...localTasks, ...backendTasks].slice(0, 30);
        });
      } catch {
        // Auth may not be ready yet, or the API may be unavailable on login screens.
      }
    };
    void refresh();
    const id = window.setInterval(() => void refresh(), 5000);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, []);

  const runTask = useCallback(async <T,>(options: RunTaskOptions<T>): Promise<T | undefined> => {
    const id = taskID();
    const startedAt = new Date().toISOString();
    setTasks((prev) =>
      [
        {
          id,
          key: options.key,
          title: options.title,
          description: options.description,
          status: "running" as const,
          startedAt,
        },
        ...prev,
      ].slice(0, 30),
    );

    try {
      const result = await options.run();
      setTasks((prev) =>
        prev.map((task) =>
          task.id === id
            ? { ...task, status: "success", finishedAt: new Date().toISOString() }
            : task,
        ),
      );
      options.onSuccess?.(result);
      toast.success(message(options.successMessage, result, i18n.t("task:toasts.done")));
      return result;
    } catch (error) {
      const text = errorMessage(options.errorMessage, error);
      setTasks((prev) =>
        prev.map((task) =>
          task.id === id
            ? { ...task, status: "error", finishedAt: new Date().toISOString(), error: text }
            : task,
        ),
      );
      options.onError?.(error);
      toast.error(text);
      return undefined;
    }
  }, []);

  const isTaskRunning = useCallback(
    (key: string) => tasks.some((task) => task.key === key && task.status === "running"),
    [tasks],
  );

  const clearFinished = useCallback(() => {
    setTasks((prev) => prev.filter((task) => task.status === "running"));
  }, []);

  const activeCount =
    tasks.filter((task) => task.status === "running").length +
    (activity?.workers.filter((w) => w.state === "running").length ?? 0);
  const value = useMemo(
    () => ({ tasks, activeCount, activity, runTask, isTaskRunning, clearFinished }),
    [tasks, activeCount, activity, runTask, isTaskRunning, clearFinished],
  );

  return <TaskContext.Provider value={value}>{children}</TaskContext.Provider>;
}

export function useBackgroundTasks(): TaskContextValue {
  const ctx = useContext(TaskContext);
  if (!ctx) throw new Error("useBackgroundTasks must be used within TaskProvider");
  return ctx;
}
