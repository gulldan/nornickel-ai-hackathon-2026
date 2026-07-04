// A tiny «i» icon that reveals a plain-language definition on hover/focus —
// for jargon a non-technical reader (scientist, manager) may not know (TRL,
// technology readiness/ITC, GRNTI/VAK/ASJC, …). Reuses the shared Tooltip primitive.
import type { ReactNode } from "react";
import { Info } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/shared/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/shared/ui/Tooltip";

export function InfoHint({
  children,
  className,
  label,
}: {
  children: ReactNode;
  className?: string;
  /** Accessible name for the trigger button. */
  label?: string;
}) {
  const { t } = useTranslation();
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={label ?? t("ui.hint")}
          className={cn(
            "inline-flex size-3.5 shrink-0 items-center justify-center rounded-full text-muted-foreground/70 transition-colors hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            className,
          )}
        >
          <Info className="size-3.5" aria-hidden />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-xs whitespace-normal text-left leading-relaxed">
        {children}
      </TooltipContent>
    </Tooltip>
  );
}
