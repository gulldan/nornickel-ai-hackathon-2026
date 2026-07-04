import type { ReactNode } from "react";

import { cn } from "@/shared/lib/cn";
import { Kicker } from "@/shared/ui/Kicker";

/** Озаглавленная секция: моно-капс кикер + опциональное описание + контент.
 *  Единый вертикальный ритм вместо инлайн-Kicker с разными отступами. */
export function Section({
  title,
  description,
  action,
  children,
  className,
}: {
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={className}>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <Kicker>{title}</Kicker>
        {action}
      </div>
      {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      <div className={cn(description ? "mt-3" : "mt-2")}>{children}</div>
    </section>
  );
}
