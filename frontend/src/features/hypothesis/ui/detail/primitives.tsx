// Общие примитивы детальных панелей гипотезы и направления (раньше были
// продублированы в двух файлах).
import { useState } from "react";
import type { ComponentType, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown } from "lucide-react";

import { cn } from "@/shared/lib/cn";
import { Card, CardContent } from "@/shared/ui/Card";
import { InfoHint } from "@/shared/ui/InfoHint";

export function Panel({
  title,
  titleHint,
  icon: Icon,
  action,
  count,
  collapsible = false,
  defaultOpen = true,
  children,
}: {
  title?: string;
  /** Optional plain-language definition shown as an «i» tooltip by the title. */
  titleHint?: string;
  icon?: ComponentType<{ className?: string }>;
  action?: ReactNode;
  count?: number;
  collapsible?: boolean;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const [open, setOpen] = useState(defaultOpen);
  const showBody = !collapsible || open;
  const titleInner = (
    <>
      {Icon && <Icon className="size-4 text-muted-foreground" />}
      <span className="kicker text-foreground">{title}</span>
      {count !== undefined && count > 0 && (
        <span className="rounded-full bg-muted px-1.5 font-mono text-xs tabular-nums text-muted-foreground">
          {count}
        </span>
      )}
    </>
  );
  return (
    <Card>
      <CardContent className="space-y-3 p-5">
        {(title || action) && (
          <div className="flex items-center justify-between gap-2">
            <div className="flex flex-1 items-center gap-1.5">
              {collapsible ? (
                <button
                  type="button"
                  onClick={() => setOpen((o) => !o)}
                  className="flex flex-1 items-center gap-2 text-left"
                  aria-expanded={open}
                >
                  <ChevronDown
                    className={cn(
                      "size-4 shrink-0 text-muted-foreground transition-transform",
                      open && "rotate-180",
                    )}
                  />
                  {titleInner}
                </button>
              ) : (
                <h3 className="flex items-center gap-2">{titleInner}</h3>
              )}
              {titleHint && (
                <InfoHint label={t("panels.whatIs", { title: title ?? "" })}>{titleHint}</InfoHint>
              )}
            </div>
            {action}
          </div>
        )}
        {showBody && children}
      </CardContent>
    </Card>
  );
}

export function FieldLabel({ children }: { children: ReactNode }) {
  return <p className="kicker text-muted-foreground">{children}</p>;
}

export function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className="mt-1 text-sm">{children}</div>
    </div>
  );
}

export function BulletList({ items, small = false }: { items: string[]; small?: boolean }) {
  return (
    <ul
      className={cn(
        "list-disc space-y-0.5 pl-4 text-muted-foreground",
        small ? "text-xs" : "text-sm",
      )}
    >
      {items.map((x) => (
        <li key={x}>{x}</li>
      ))}
    </ul>
  );
}

/** Счётчик доказательств «за/против» — раздельная полоса в цветах семантики. */
export function ConsensusMeter({
  forCount,
  againstCount,
}: {
  forCount: number;
  againstCount: number;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const total = forCount + againstCount;
  if (total === 0) return null;
  const forPct = (forCount / total) * 100;
  return (
    <div className="space-y-1">
      <div className="flex h-2 overflow-hidden rounded-full bg-muted" aria-hidden>
        <div className="bg-ok" style={{ width: `${forPct}%` }} />
        <div className="bg-risk" style={{ width: `${100 - forPct}%` }} />
      </div>
      <div className="flex items-center justify-between text-xs">
        <span className="inline-flex items-center gap-1 font-medium text-ok">
          <span className="size-2 rounded-full bg-ok" aria-hidden />
          {t("confidence.fragmentsFor", { count: forCount })}
        </span>
        <span className="inline-flex items-center gap-1 font-medium text-risk">
          {t("confidence.fragmentsAgainst", { count: againstCount })}
          <span className="size-2 rounded-full bg-risk" aria-hidden />
        </span>
      </div>
    </div>
  );
}
