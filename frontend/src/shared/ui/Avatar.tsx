import * as AvatarPrimitive from "@radix-ui/react-avatar";
import type { ComponentProps } from "react";

import { cn } from "@/shared/lib/cn";

export function Avatar({
  className,
  size = "default",
  ...props
}: ComponentProps<typeof AvatarPrimitive.Root> & { size?: "sm" | "default" }) {
  return (
    <AvatarPrimitive.Root
      className={cn(
        "relative flex shrink-0 overflow-hidden rounded-full",
        size === "sm" ? "size-8" : "size-9",
        className,
      )}
      {...props}
    />
  );
}

export function AvatarFallback({
  className,
  ...props
}: ComponentProps<typeof AvatarPrimitive.Fallback>) {
  return (
    <AvatarPrimitive.Fallback
      className={cn(
        "flex size-full items-center justify-center rounded-full bg-muted text-sm font-medium",
        className,
      )}
      {...props}
    />
  );
}
