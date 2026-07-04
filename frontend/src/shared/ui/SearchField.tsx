import { Search } from "lucide-react";

import { Input } from "@/shared/ui/Input";

interface SearchFieldProps {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  ariaLabel: string;
  className?: string;
}

/** Unified filter-search field with an icon (tables, lists). */
export function SearchField({
  value,
  onChange,
  placeholder,
  ariaLabel,
  className = "",
}: SearchFieldProps) {
  return (
    <div className={`relative ${className}`}>
      <Search
        className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
        aria-hidden
      />
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="pl-9"
        aria-label={ariaLabel}
      />
    </div>
  );
}
