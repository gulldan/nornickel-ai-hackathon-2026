import type { ComponentType, ReactNode } from "react";

import { cn } from "@/shared/lib/cn";

/** Единая плитка-метрика: подпись, крупное моно-число, вторичная строка и
 *  опциональная иконка. Один компонент для Обзора, Метрик и любых сводок —
 *  вместо трёх почти одинаковых ручных реализаций. */
export function MetricTile({
  label,
  value,
  sub,
  icon: Icon,
  hint,
  className,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  icon?: ComponentType<{ className?: string }>;
  hint?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("rounded-xl border bg-card p-4", className)}>
      <div className="flex items-center justify-between gap-2">
        {/* Подпись всегда в одну строку: двухстрочные подписи опускали число,
            и цифры в ряду плиток «скакали» по высоте. */}
        <span className="inline-flex min-w-0 items-center gap-1 text-sm text-muted-foreground">
          <span className="truncate" title={label}>
            {label}
          </span>
          {hint}
        </span>
        {Icon && <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />}
      </div>
      <p className="mt-1.5 font-mono text-2xl font-semibold tabular-nums leading-none">{value}</p>
      {sub && <p className="mt-1.5 text-xs text-muted-foreground">{sub}</p>}
    </div>
  );
}
