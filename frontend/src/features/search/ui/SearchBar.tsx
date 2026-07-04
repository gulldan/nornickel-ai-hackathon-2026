import React, { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Search, ArrowRight } from "lucide-react";
import { Input } from "@/shared/ui/Input";
import { Button } from "@/shared/ui/Button";

interface SearchBarProps {
  initialValue?: string;
  size?: "lg" | "default";
  autoFocus?: boolean;
  onSearch: (query: string) => void;
}

export function SearchBar({
  initialValue = "",
  size = "default",
  autoFocus,
  onSearch,
}: SearchBarProps) {
  const { t } = useTranslation("search");
  const [value, setValue] = useState(initialValue);
  useEffect(() => {
    setValue(initialValue);
  }, [initialValue]);
  const submit = (query: string) => {
    const q = query.trim();
    if (!q) return;
    onSearch(q);
  };
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      submit(value);
    }
  };
  const isLg = size === "lg";
  return (
    <div className="relative flex w-full items-center gap-2">
      <Search
        className={`pointer-events-none absolute left-3.5 text-muted-foreground ${
          isLg ? "size-5" : "size-4"
        }`}
        aria-hidden
      />

      <Input
        aria-label={t("bar.queryAriaLabel")}
        autoFocus={autoFocus}
        value={value}
        placeholder={t("bar.placeholder")}
        className={isLg ? "h-12 pl-11 pr-28 text-base" : "h-10 pl-10 pr-24"}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
      />

      <Button
        variant="brand"
        size={isLg ? "default" : "sm"}
        className="absolute right-1.5"
        onClick={() => submit(value)}
      >
        {t("bar.submit")}
        <ArrowRight className="size-4" aria-hidden />
      </Button>
    </div>
  );
}
