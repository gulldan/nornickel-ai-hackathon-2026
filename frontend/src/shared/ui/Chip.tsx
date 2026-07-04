import type { ComponentProps } from "react";

import { cn } from "@/shared/lib/cn";
import { badgeVariants } from "@/shared/ui/Badge";

/** Pill-shaped clickable element with the outline-Badge look. Used for quick
 *  picks like popular topics and recent queries. Renders a real <button> so it
 *  is keyboard-accessible. */
export function Chip({ className, type, ...props }: ComponentProps<"button">) {
  return (
    <button
      type={type ?? "button"}
      className={cn(
        badgeVariants({ variant: "outline" }),
        "cursor-pointer rounded-full px-3 py-1.5 text-sm font-normal transition-colors hover:border-brand-border hover:bg-brand-wash hover:text-accent-foreground",
        className,
      )}
      {...props}
    />
  );
}
