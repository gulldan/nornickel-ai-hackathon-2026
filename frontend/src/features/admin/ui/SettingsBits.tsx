// Общие детали карточек настроек: единая шапка карточки, бейдж источника
// значения и строки «подпись + контрол», чтобы все вкладки читались одинаково.
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/shared/ui/Badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/ui/Card";
import { Label } from "@/shared/ui/Label";
import { Switch } from "@/shared/ui/CSwitch";
import { parseBool, type AppSettingsState } from "../model";

export function SettingsCard({
  icon: Icon,
  title,
  titleExtra,
  description,
  children,
  footer,
  className,
}: {
  icon: LucideIcon;
  title: string;
  titleExtra?: ReactNode;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
}) {
  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          <Icon className="size-4 text-muted-foreground" aria-hidden />
          {title}
          {titleExtra}
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="space-y-5">
        {children}
        {footer && (
          <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
            {footer}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function SourceBadge({ app, k }: { app: AppSettingsState; k: string }) {
  const { t } = useTranslation("admin");
  const source = app.sourceOf(k);
  if (source === null) return null;
  const labels = {
    db: t("system.source.db"),
    env: t("system.source.env"),
    default: t("system.source.default"),
    draft: t("system.source.draft"),
  } as const;
  return (
    <Badge variant="outline" className="text-[10px] font-normal text-muted-foreground">
      {labels[source]}
    </Badge>
  );
}

/** Строка с тумблером: подпись и пояснение слева, Switch справа. */
export function ToggleRow({
  id,
  label,
  hint,
  badge,
  checked,
  onCheckedChange,
}: {
  id: string;
  label: string;
  hint?: string;
  badge?: ReactNode;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-3 rounded-md border px-3 py-2.5">
      <div className="space-y-1">
        <span className="flex items-center gap-2">
          <Label htmlFor={id}>{label}</Label>
          {badge}
        </span>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onCheckedChange} />
    </div>
  );
}

export function BoolRow({
  app,
  k,
  label,
  hint,
}: {
  app: AppSettingsState;
  k: string;
  label: string;
  hint?: string;
}) {
  return (
    <ToggleRow
      id={`sys-${k}`}
      label={label}
      hint={hint}
      badge={<SourceBadge app={app} k={k} />}
      checked={parseBool(app.effective(k))}
      onCheckedChange={(checked) => app.setValue(k, checked ? "true" : "false")}
    />
  );
}

/** Поле «подпись + пояснение + контрол» для инпутов; unit — подпись единицы. */
export function FieldRow({
  id,
  label,
  hint,
  badge,
  unit,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  badge?: ReactNode;
  unit?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-1">
      <span className="flex items-center gap-2">
        <Label htmlFor={id}>{label}</Label>
        {badge}
      </span>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      <div className="flex items-center gap-2">
        {children}
        {unit && <span className="text-xs text-muted-foreground">{unit}</span>}
      </div>
    </div>
  );
}

/** «Приборная» полоса фактов: подпись-кикер + моноширинное значение. */
export function StatStrip({ items }: { items: { label: string; value: string }[] }) {
  return (
    <div className="grid grid-cols-1 gap-px overflow-hidden rounded-md border bg-border sm:grid-cols-3">
      {items.map((it) => (
        <div key={it.label} className="min-w-0 bg-muted/30 px-3 py-2">
          <span className="kicker block">{it.label}</span>
          <span className="block truncate font-mono text-sm" title={it.value}>
            {it.value}
          </span>
        </div>
      ))}
    </div>
  );
}
