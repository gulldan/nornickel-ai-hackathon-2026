import type { ReactNode } from "react";

import { cn } from "@/shared/lib/cn";

type SegmentedOption<T extends string> = {
  value: T;
  label: ReactNode;
};

/** Сегментированный переключатель — единый идиом «bg-foreground» активного
 *  сегмента, вынесенный из локальных копий (период, метрика, вид страницы).
 *  `size="md"` — крупный вариант для переключения представлений страницы. */
export function Segmented<T extends string>({
  options,
  value,
  onChange,
  size = "sm",
  className,
  "aria-label": ariaLabel,
}: {
  options: readonly SegmentedOption<T>[];
  value: T;
  onChange: (value: T) => void;
  size?: "sm" | "md";
  className?: string;
  "aria-label"?: string;
}) {
  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      className={cn("inline-flex rounded-lg border p-0.5", className)}
    >
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(o.value)}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md font-mono transition-colors",
              size === "md" ? "px-3.5 py-1.5 text-sm" : "px-2.5 py-1 text-xs",
              active
                ? "bg-foreground text-background"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
