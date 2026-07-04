import type { QueueKey } from "@/features/hypothesis/board/model";
import { cn } from "@/shared/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/Tooltip";

/** Сегмент-фильтр очередей: одна строка пилюль со счётчиками. Заменяет и
 *  четыре тяжёлые карточки, и дублирующий блок «Автообработка». */
export function QueueFilter({
  queues,
  active,
  onSelect,
}: {
  queues: { key: QueueKey; label: string; hint: string; count: number }[];
  active: QueueKey;
  onSelect: (key: QueueKey) => void;
}) {
  return (
    <div className="inline-flex flex-wrap gap-1 rounded-xl border bg-secondary/60 p-1">
      {queues.map((q) => {
        const on = q.key === active;
        return (
          <Tooltip key={q.key}>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={() => onSelect(q.key)}
                aria-pressed={on}
                className={cn(
                  "inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  on
                    ? "bg-card text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {q.label}
                <span
                  className={cn(
                    "font-mono text-xs tabular-nums",
                    on ? "text-foreground" : "text-muted-foreground",
                  )}
                >
                  {q.count}
                </span>
              </button>
            </TooltipTrigger>
            <TooltipContent>{q.hint}</TooltipContent>
          </Tooltip>
        );
      })}
    </div>
  );
}
