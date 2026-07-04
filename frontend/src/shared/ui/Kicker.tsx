import type { ComponentProps } from "react";

import { cn } from "@/shared/lib/cn";

/** Моно-капс структурный лейбл («ВОПРОС», «ИСТОЧНИКИ · 4») — маркер разделов
 *  bold-стиля. По умолчанию приглушённый; для акцента передать text-brand. */
export function Kicker({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("kicker text-muted-foreground", className)} {...props} />;
}
