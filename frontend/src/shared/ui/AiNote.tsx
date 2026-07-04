// Honest, low-key caption for any AI- or corpus-derived figure (verdict,
// auto-tags, ITC, …) so a non-expert understands the status of a number:
// it is a machine estimate from the knowledge base, not ground truth. Mirrors
// the «Считается по базе знаний» footnote already used in ItcSummary.
// (the footnote text means "Computed from the knowledge base")
import { Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/shared/lib/cn";

export function AiNote({ children, className }: { children?: string; className?: string }) {
  const { t } = useTranslation();
  return (
    <p className={cn("flex items-center gap-1 text-[11px] text-muted-foreground", className)}>
      <Sparkles className="size-3 shrink-0" aria-hidden />
      {children ?? t("ui.aiNote")}
    </p>
  );
}
