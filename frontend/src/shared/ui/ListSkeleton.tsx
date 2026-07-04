import { useTranslation } from "react-i18next";

import { Skeleton } from "@/shared/ui/Skeleton";

interface ListSkeletonProps {
  /** Number of placeholder rows to render. */
  rows?: number;
  /**
   * Row shape:
   * - "simple" — a card row with a title line and a short meta line (history);
   * - "result" — a search-result row with a leading icon and snippet lines;
   * - "avatar" — a person row with an avatar, two text lines and a trailing
   *   badge, wrapped in a single bordered card (user lists).
   */
  variant?: "simple" | "result" | "avatar";
  /** Accessible label for the loading list. */
  ariaLabel?: string;
}

/** Loading placeholder for a vertical list of card-like rows. Matches the row
 *  padding/spacing used across history, user and search-result lists. */
export function ListSkeleton({ rows = 4, variant = "simple", ariaLabel }: ListSkeletonProps) {
  const { t } = useTranslation();
  const label = ariaLabel ?? t("ui.loading");
  if (variant === "avatar") {
    return (
      <output className="mt-4 block space-y-4 rounded-lg border bg-card p-4" aria-label={label}>
        {Array.from({ length: rows }, (_, i) => (
          <div key={i} className="flex items-center gap-3">
            <Skeleton className="size-8 rounded-full" />
            <div className="flex-1 space-y-1.5">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="h-3 w-24" />
            </div>
            <Skeleton className="h-5 w-28 rounded-full" />
          </div>
        ))}
      </output>
    );
  }
  if (variant === "result") {
    return (
      <output className="mt-6 block space-y-3" aria-label={label}>
        {Array.from({ length: rows }, (_, i) => (
          <div key={i} className="rounded-lg border p-4">
            <div className="flex gap-3">
              <Skeleton className="size-8 rounded-md" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-2/3" />
                <Skeleton className="h-3.5 w-full" />
                <Skeleton className="h-3.5 w-4/5" />
              </div>
            </div>
          </div>
        ))}
      </output>
    );
  }
  return (
    <ul className="mt-4 space-y-2" aria-label={label}>
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="rounded-lg border bg-card px-4 py-3">
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="mt-2 h-3 w-36" />
        </li>
      ))}
    </ul>
  );
}
