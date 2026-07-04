import { FileQuestion, RefreshCw, ServerCrash, ShieldX } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Button } from "@/shared/ui/Button";

function ErrorShell({
  icon,
  code,
  title,
  description,
  children,
}: {
  icon: ReactNode;
  code: string;
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex min-h-[60vh] w-full flex-col items-center justify-center px-4 py-16 text-center">
      <div className="flex size-14 items-center justify-center rounded-full bg-muted text-muted-foreground">
        {icon}
      </div>
      <p className="mt-5 text-xs font-semibold uppercase tracking-widest text-muted-foreground">
        {code}
      </p>
      <h1 className="mt-1 text-xl font-semibold tracking-tight">{title}</h1>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">{description}</p>
      <div className="mt-5 flex gap-2">{children}</div>
    </div>
  );
}

/** 404 — unknown route. */
export function NotFound() {
  const { t } = useTranslation();
  return (
    <ErrorShell
      icon={<FileQuestion className="size-7" aria-hidden />}
      code={t("errorPages.notFound.code")}
      title={t("errorPages.notFound.title")}
      description={t("errorPages.notFound.description")}
    >
      <Button asChild>
        <Link to="/">{t("errorPages.toHome")}</Link>
      </Button>
    </ErrorShell>
  );
}

/** 403 — the signed-in account lacks the required role. */
export function Forbidden() {
  const { t } = useTranslation();
  return (
    <ErrorShell
      icon={<ShieldX className="size-7" aria-hidden />}
      code={t("errorPages.forbidden.code")}
      title={t("errorPages.forbidden.title")}
      description={t("errorPages.forbidden.description")}
    >
      <Button asChild>
        <Link to="/">{t("errorPages.toHome")}</Link>
      </Button>
    </ErrorShell>
  );
}

/** 500 — an unexpected render/runtime failure. */
function ServerError({ onRetry }: { onRetry?: () => void }) {
  const { t } = useTranslation();
  return (
    <ErrorShell
      icon={<ServerCrash className="size-7" aria-hidden />}
      code={t("errorPages.serverError.code")}
      title={t("errorPages.serverError.title")}
      description={t("errorPages.serverError.description")}
    >
      <Button
        onClick={() => {
          onRetry?.();
          window.location.reload();
        }}
      >
        <RefreshCw className="size-4" aria-hidden />
        {t("actions.refresh")}
      </Button>
    </ErrorShell>
  );
}

interface BoundaryState {
  failed: boolean;
}

/** ErrorBoundary renders the 500 page when anything below it throws. */
export class ErrorBoundary extends Component<{ children: ReactNode }, BoundaryState> {
  state: BoundaryState = { failed: false };

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // The demo has no error collector; the console keeps the stack reachable.
    console.error("unhandled UI error", error, info.componentStack);
  }

  render() {
    if (this.state.failed) {
      return <ServerError onRetry={() => this.setState({ failed: false })} />;
    }
    return this.props.children;
  }
}
