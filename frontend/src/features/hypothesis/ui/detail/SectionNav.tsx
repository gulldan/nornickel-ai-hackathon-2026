import { useEffect, useState } from "react";

import { cn } from "@/shared/lib/cn";

export interface SectionLink {
  id: string;
  label: string;
  count?: number;
}

export function SectionNav({ title, sections }: { title: string; sections: SectionLink[] }) {
  const idKey = sections.map((s) => s.id).join("|");
  const [active, setActive] = useState(sections[0]?.id ?? "");

  useEffect(() => {
    const els = idKey
      .split("|")
      .map((id) => document.getElementById(id))
      .filter((el): el is HTMLElement => el !== null);
    if (els.length === 0) {
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        const first = entries
          .filter((e) => e.isIntersecting)
          .toSorted((x, y) => x.boundingClientRect.top - y.boundingClientRect.top)
          .at(0);
        if (first) {
          setActive(first.target.id);
        }
      },
      { rootMargin: "-15% 0px -70% 0px" },
    );
    for (const el of els) {
      observer.observe(el);
    }
    return () => observer.disconnect();
  }, [idKey]);

  const go = (id: string) => {
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
    setActive(id);
  };

  return (
    <nav aria-label={title} className="rounded-xl border bg-card p-4">
      <p className="kicker text-muted-foreground">{title}</p>
      <ul className="mt-2.5 space-y-0.5">
        {sections.map((s) => (
          <li key={s.id}>
            <button
              type="button"
              onClick={() => go(s.id)}
              aria-current={active === s.id ? "true" : undefined}
              className={cn(
                "flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-[13px] transition-colors",
                active === s.id
                  ? "bg-brand-wash/60 font-medium text-foreground"
                  : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
              )}
            >
              <span className="truncate">{s.label}</span>
              {s.count !== undefined && s.count > 0 && (
                <span className="rounded-full bg-muted px-1.5 font-mono text-[11px] tabular-nums text-muted-foreground">
                  {s.count}
                </span>
              )}
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}
