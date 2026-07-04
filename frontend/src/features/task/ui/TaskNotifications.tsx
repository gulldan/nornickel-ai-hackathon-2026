import { Bell, CheckCircle2, CircleAlert, Loader2, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useBackgroundTasks, type BackgroundTask } from "@/features/task/TaskContext";
import { WORKER_LABELS, type SystemActivity, type WorkerStatus } from "@/features/task/api";
import { currentLocale } from "@/shared/i18n";
import { cn } from "@/shared/lib/cn";
import { Button } from "@/shared/ui/Button";
import { Kicker } from "@/shared/ui/Kicker";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/DropdownMenu";

function statusIcon(task: BackgroundTask) {
  if (task.status === "running") {
    return <Loader2 className="size-4 animate-spin text-primary" aria-hidden />;
  }
  if (task.status === "success") {
    return <CheckCircle2 className="size-4 text-ok" aria-hidden />;
  }
  return <CircleAlert className="size-4 text-destructive" aria-hidden />;
}

function taskTime(task: BackgroundTask): string {
  const value = task.finishedAt ?? task.startedAt;
  return new Intl.DateTimeFormat(currentLocale(), {
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

function WorkerRow({ worker }: { worker: WorkerStatus }) {
  const label = WORKER_LABELS[worker.name] ?? worker.name;
  const progress =
    worker.progressTotal && worker.progressTotal > 0
      ? ` ${worker.progressDone ?? 0}/${worker.progressTotal}`
      : "";
  return (
    <div className="flex items-center gap-2 rounded-md px-2 py-1.5">
      {worker.state === "running" ? (
        <Loader2 className="size-4 shrink-0 animate-spin text-brand" aria-hidden />
      ) : worker.state === "error" ? (
        <CircleAlert className="size-4 shrink-0 text-risk" aria-hidden />
      ) : (
        <CheckCircle2 className="size-4 shrink-0 text-ok" aria-hidden />
      )}
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">
          {label}
          {worker.state === "running" && (
            <span className="font-mono text-xs text-muted-foreground">{progress}</span>
          )}
        </p>
        {worker.state === "error" && worker.lastError && (
          <p className="mt-0.5 line-clamp-2 text-xs text-risk">{worker.lastError}</p>
        )}
      </div>
    </div>
  );
}

/** Секция «Система»: чем занята фоновая фабрика знаний прямо сейчас. */
function SystemPulse({ activity }: { activity: SystemActivity }) {
  const { t } = useTranslation("task");
  const busy = activity.workers.filter((w) => w.state !== "idle");
  const shown = busy.length > 0 ? busy : activity.workers;
  if (shown.length === 0) return null;
  return (
    <>
      <Kicker className="px-2 pb-1 pt-2">{t("notifications.system")}</Kicker>
      {shown.map((w) => (
        <WorkerRow key={w.name} worker={w} />
      ))}
      {busy.length === 0 && (
        <p className="px-2 pb-1.5 text-xs text-muted-foreground">{t("notifications.allDone")}</p>
      )}
      <DropdownMenuSeparator />
    </>
  );
}

function TaskRow({ task }: { task: BackgroundTask }) {
  return (
    <div className="flex gap-2 rounded-md px-2 py-2">
      <div className="mt-0.5 shrink-0">{statusIcon(task)}</div>
      <div className="min-w-0 flex-1">
        <div className="flex items-start justify-between gap-2">
          <p className="truncate text-sm font-medium">{task.title}</p>
          <span className="shrink-0 text-[11px] text-muted-foreground">{taskTime(task)}</span>
        </div>
        {task.description && (
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{task.description}</p>
        )}
        {task.error && <p className="mt-0.5 line-clamp-2 text-xs text-destructive">{task.error}</p>}
      </div>
    </div>
  );
}

export function TaskNotifications() {
  const { t } = useTranslation("task");
  const { tasks, activeCount, activity, clearFinished } = useBackgroundTasks();
  const finishedCount = tasks.filter((task) => task.status !== "running").length;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="relative"
          aria-label={
            activeCount > 0
              ? t("notifications.bellAria", { count: activeCount })
              : t("notifications.bellAriaIdle")
          }
        >
          <Bell className="size-4" aria-hidden />
          {activeCount > 0 && (
            <span
              className={cn(
                "absolute -right-0.5 -top-0.5 flex min-w-4 items-center justify-center rounded-full",
                "bg-primary px-1 text-[10px] font-semibold leading-4 text-primary-foreground",
              )}
            >
              {activeCount}
            </span>
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuLabel className="flex items-center justify-between gap-2">
          <span>{t("notifications.title")}</span>
          {activeCount > 0 && (
            <span className="rounded-full bg-muted px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground">
              {t("notifications.inProgress", { count: activeCount })}
            </span>
          )}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        {activity && <SystemPulse activity={activity} />}
        {tasks.length === 0 ? (
          <p className="px-2 py-5 text-center text-sm text-muted-foreground">
            {t("notifications.empty")}
          </p>
        ) : (
          <div className="max-h-80 overflow-y-auto">
            {tasks.map((task) => (
              <TaskRow key={task.id} task={task} />
            ))}
          </div>
        )}
        {finishedCount > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onSelect={(event) => {
                event.preventDefault();
                clearFinished();
              }}
            >
              <Trash2 className="size-4" aria-hidden />
              {t("notifications.clearFinished")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
