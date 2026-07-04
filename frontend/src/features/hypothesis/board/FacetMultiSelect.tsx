// Мультиселект фасета доски: чекбоксы значений со счётчиками, поиск по длинным
// спискам, сброс. Меню не закрывается при выборе — комбинации набираются одним
// заходом.
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown } from "lucide-react";

import { Badge } from "@/shared/ui/Badge";
import { Button } from "@/shared/ui/Button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/DropdownMenu";
import { Input } from "@/shared/ui/Input";

const SEARCH_THRESHOLD = 8;

export interface FacetOption {
  value: string;
  label?: string;
  count: number;
}

export function FacetMultiSelect({
  label,
  options,
  selected,
  onChange,
}: {
  label: string;
  options: FacetOption[];
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const { t } = useTranslation("hypothesis");
  const [query, setQuery] = useState("");

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === "") return options;
    return options.filter((o) => (o.label ?? o.value).toLowerCase().includes(q));
  }, [options, query]);

  const toggle = (value: string) => {
    onChange(selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value]);
  };

  if (options.length === 0 && selected.length === 0) return null;

  return (
    <DropdownMenu onOpenChange={(open) => !open && setQuery("")}>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" className="font-normal">
          {label}
          {selected.length > 0 && (
            <Badge variant="brand" className="px-1.5 tabular-nums">
              {selected.length}
            </Badge>
          )}
          <ChevronDown className="size-4 text-muted-foreground" aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        {options.length > SEARCH_THRESHOLD && (
          <div className="p-1">
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t("board.facetSearch")}
              aria-label={t("board.facetSearch")}
              className="h-8"
              onKeyDown={(e) => e.stopPropagation()}
            />
          </div>
        )}
        <div className="max-h-72 overflow-y-auto">
          {visible.map((o) => (
            <DropdownMenuCheckboxItem
              key={o.value}
              checked={selected.includes(o.value)}
              onCheckedChange={() => toggle(o.value)}
              onSelect={(e) => e.preventDefault()}
            >
              <span className="min-w-0 flex-1 truncate">{o.label ?? o.value}</span>
              <span className="text-xs tabular-nums text-muted-foreground">{o.count}</span>
            </DropdownMenuCheckboxItem>
          ))}
          {visible.length === 0 && (
            <p className="px-2 py-2 text-sm text-muted-foreground">{t("board.facetNothing")}</p>
          )}
        </div>
        {selected.length > 0 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onChange([])}>
              {t("board.facetReset")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
