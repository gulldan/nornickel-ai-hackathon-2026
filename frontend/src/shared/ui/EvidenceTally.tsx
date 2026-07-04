import { useTranslation } from "react-i18next";

import { cn } from "@/shared/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/Tooltip";

/** Единая сводка доказательств: «за / против / упоминания». Пока гипотеза не
 *  проверена по базе, счётчики бессмысленны — показываем честное «пока не
 *  проверено». Один компонент для карточки доски и паспорта гипотезы. */
export function EvidenceTally({
  supports,
  contradicts,
  mentions,
  verified,
  className,
}: {
  supports: number;
  contradicts: number;
  mentions: number;
  verified: boolean;
  className?: string;
}) {
  const { t } = useTranslation();
  if (!verified && supports === 0 && contradicts === 0) {
    return (
      <span className={cn("text-xs text-muted-foreground", className)}>
        {t("ui.evidence.notVerified")}
      </span>
    );
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          className={cn("inline-flex flex-wrap items-center gap-x-3 gap-y-1 text-xs", className)}
        >
          <Dot tone="bg-ok" value={supports} label={t("ui.evidence.for")} />
          <Dot tone="bg-risk" value={contradicts} label={t("ui.evidence.against")} />
          <Dot
            tone="bg-muted-foreground/40"
            value={mentions}
            label={t("ui.evidence.mentions")}
            muted
          />
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">{t("ui.evidence.hint")}</TooltipContent>
    </Tooltip>
  );
}

function Dot({
  tone,
  value,
  label,
  muted,
}: {
  tone: string;
  value: number;
  label: string;
  muted?: boolean;
}) {
  return (
    <span className={cn("inline-flex items-center gap-1.5", muted && "text-muted-foreground")}>
      <span className={cn("size-2 rounded-full", tone)} aria-hidden />
      <span className="font-mono font-medium">{value}</span> {label}
    </span>
  );
}
