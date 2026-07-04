import { useTranslation } from "react-i18next";

import { cn } from "@/shared/lib/cn";

const TONE = {
  brand: "bg-brand",
  ok: "bg-ok",
  warn: "bg-warn",
  risk: "bg-risk",
  ink: "bg-foreground",
} as const;

/** Дискретная «приборная» шкала: value из total заполненных сегментов.
 *  Заменяет процентные бары у оценок, чтобы числа читались как измерение,
 *  а не как маркетинговый прогресс. */
export function SegmentBar({
  value,
  total = 9,
  tone = "brand",
  label,
  className,
}: {
  value: number;
  total?: number;
  tone?: keyof typeof TONE;
  label?: string;
  className?: string;
}) {
  const { t } = useTranslation();
  const filled = Math.max(0, Math.min(total, Math.round(value)));
  return (
    <div
      // oxlint-disable-next-line jsx-a11y/prefer-tag-over-role -- составное «изображение» из div-сегментов, семантичного тега нет
      role="img"
      aria-label={label ?? t("ui.ofTotal", { value: filled, total })}
      className={cn("flex items-center gap-[3px]", className)}
    >
      {Array.from({ length: total }, (_, i) => (
        <span
          key={i}
          className={cn("h-2 w-1.5 rounded-[1px]", i < filled ? TONE[tone] : "bg-border")}
        />
      ))}
    </div>
  );
}
