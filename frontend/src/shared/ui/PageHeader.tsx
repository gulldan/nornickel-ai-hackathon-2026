import type { ReactNode } from "react";

import { Kicker } from "@/shared/ui/Kicker";

interface PageHeaderProps {
  title: string;
  description?: string;
  /** Моно-капс лейбл над заголовком («БАЗА ЗНАНИЙ», «ПОРТФЕЛЬ»). */
  kicker?: string;
  /** Optional badge to the right of the title. */
  badge?: ReactNode;
  /** Action buttons in the right part of the header. */
  actions?: ReactNode;
}

/** Unified page header: kicker + display title + description + actions. */
export function PageHeader({ title, description, kicker, badge, actions }: PageHeaderProps) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div className="min-w-0">
        {kicker && <Kicker className="mb-1.5">{kicker}</Kicker>}
        <h1 className="font-display flex items-center gap-2.5 text-[1.75rem] leading-none">
          {title}
          {badge}
        </h1>
        {description && <p className="mt-2 text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex min-w-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
