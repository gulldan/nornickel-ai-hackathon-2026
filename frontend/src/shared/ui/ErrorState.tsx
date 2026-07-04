import { RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/shared/ui/Button";

interface ErrorStateProps {
  /** What went wrong, in plain, human Russian. */
  message: string;
  onRetry?: () => void;
  /** compact — an embeddable variant (inside panels/sheets). */
  variant?: "page" | "compact";
}

/** Unified error state with retry. */
export function ErrorState({ message, onRetry, variant = "page" }: ErrorStateProps) {
  const { t } = useTranslation();
  const pad = variant === "page" ? "py-14" : "py-6";
  return (
    <div className={`flex w-full flex-col items-center text-center ${pad}`}>
      <p className="text-sm font-medium">{t("ui.somethingWentWrong")}</p>
      <p className="mt-1 max-w-sm text-sm text-muted-foreground">{message}</p>
      {onRetry && (
        <Button variant="outline" size="sm" className="mt-4" onClick={onRetry}>
          <RefreshCw className="size-3.5" aria-hidden />
          {t("actions.retry")}
        </Button>
      )}
    </div>
  );
}
