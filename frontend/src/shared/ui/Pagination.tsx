import { ChevronLeft, ChevronRight } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/shared/lib/cn";
import { Button } from "@/shared/ui/Button";

/** Классическая страничная навигация: ‹ 1 … 4 5 6 … 12 ›. Единый компонент
 *  для всех списков вместо «Показать ещё», раздувавшего страницу вниз. */
export function Pagination({
  page,
  pageCount,
  onPage,
  className,
}: {
  page: number;
  pageCount: number;
  onPage: (page: number) => void;
  className?: string;
}) {
  const { t } = useTranslation();
  if (pageCount <= 1) return null;
  return (
    <nav
      className={cn("flex flex-wrap items-center justify-center gap-1", className)}
      aria-label={t("ui.pagination.label")}
    >
      <Button
        variant="ghost"
        size="icon-sm"
        disabled={page <= 1}
        onClick={() => onPage(page - 1)}
        aria-label={t("ui.pagination.prev")}
      >
        <ChevronLeft className="size-4" aria-hidden />
      </Button>
      {pageItems(page, pageCount).map((it, i, items) => {
        if (it === "gap") {
          // Разрыв однозначно определяется предыдущим номером (их максимум два).
          const after = items[i - 1];
          return (
            <span key={`gap-${String(after)}`} className="px-1 text-sm text-muted-foreground">
              …
            </span>
          );
        }
        return (
          <button
            key={it}
            type="button"
            aria-current={it === page ? "page" : undefined}
            onClick={() => onPage(it)}
            className={cn(
              "min-w-8 rounded-lg px-2 py-1.5 text-center font-mono text-sm tabular-nums transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              it === page
                ? "bg-foreground text-background"
                : "text-muted-foreground hover:bg-secondary hover:text-foreground",
            )}
          >
            {it}
          </button>
        );
      })}
      <Button
        variant="ghost"
        size="icon-sm"
        disabled={page >= pageCount}
        onClick={() => onPage(page + 1)}
        aria-label={t("ui.pagination.next")}
      >
        <ChevronRight className="size-4" aria-hidden />
      </Button>
    </nav>
  );
}

/** Номера с многоточиями: всегда 1 и N, вокруг текущей — по соседу. */
export function pageItems(page: number, pageCount: number): (number | "gap")[] {
  if (pageCount <= 7) {
    return Array.from({ length: pageCount }, (_, i) => i + 1);
  }
  const wanted = [...new Set([1, page - 1, page, page + 1, pageCount])]
    .filter((p) => p >= 1 && p <= pageCount)
    .toSorted((a, b) => a - b);
  const out: (number | "gap")[] = [];
  let prev = 0;
  for (const p of wanted) {
    if (p - prev === 2) out.push(prev + 1);
    else if (p - prev > 2) out.push("gap");
    out.push(p);
    prev = p;
  }
  return out;
}
