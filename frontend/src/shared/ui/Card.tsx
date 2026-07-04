import type { ComponentProps } from "react";

import { cn } from "@/shared/lib/cn";

const CARD_BASE = "rounded-xl border bg-card text-card-foreground";
// Единый ховер/фокус кликабельной карточки — вместо ручного набора классов,
// скопированного по фичам.
const CARD_INTERACTIVE =
  "cursor-pointer outline-none transition-colors hover:border-brand-border focus-visible:ring-2 focus-visible:ring-ring";

export function Card({
  className,
  size = "default",
  interactive = false,
  ...props
}: ComponentProps<"div"> & { size?: "sm" | "default"; interactive?: boolean }) {
  return (
    <div
      data-size={size}
      className={cn(
        CARD_BASE,
        interactive && CARD_INTERACTIVE,
        size === "sm" &&
          "[&_[data-slot=card-content]]:p-4 [&_[data-slot=card-content]]:pt-0 [&_[data-slot=card-footer]]:p-4 [&_[data-slot=card-footer]]:pt-0 [&_[data-slot=card-header]]:p-4",
        className,
      )}
      {...props}
    />
  );
}

/** Кликабельная карточка целиком: стили Card на семантичном <button>
 *  вместо role="button" на div. */
export function CardButton({ className, ...props }: ComponentProps<"button">) {
  return (
    <button
      type="button"
      className={cn(CARD_BASE, CARD_INTERACTIVE, "w-full text-left", className)}
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      data-slot="card-header"
      className={cn("flex flex-col space-y-1.5 p-6", className)}
      {...props}
    />
  );
}

export function CardTitle({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("font-semibold leading-none tracking-tight", className)} {...props} />;
}

export function CardDescription({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("text-sm text-muted-foreground", className)} {...props} />;
}

export function CardContent({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="card-content" className={cn("p-6 pt-0", className)} {...props} />;
}
