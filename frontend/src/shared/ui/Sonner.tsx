import type { ComponentProps } from "react";
import { Toaster as SonnerToaster } from "sonner";

/** App toast surface. Wraps sonner with project defaults; props override them. */
export function Toaster(props: ComponentProps<typeof SonnerToaster>) {
  return (
    <SonnerToaster
      position="top-right"
      toastOptions={{
        classNames: {
          toast: "rounded-md border bg-background text-foreground shadow-lg",
          description: "text-muted-foreground",
        },
      }}
      {...props}
    />
  );
}
