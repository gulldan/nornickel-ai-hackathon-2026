// «Доказательства» — article-first evidence view. Only corpus-linked evidence is
// shown: rows without document_id are seed/LLM placeholders, not verifiable proof.
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { ArrowUpRight, ChevronDown } from "lucide-react";

import { type ApiEvidence, type EvidenceStance } from "@/features/hypothesis/api";
import { cn } from "@/shared/lib/cn";

type Translate = TFunction<"hypothesisDetail">;

const DOT: Record<EvidenceStance, string> = {
  supports: "bg-ok",
  contradicts: "bg-risk",
  context: "bg-muted-foreground/40",
};
const ROLE_TEXT: Record<EvidenceStance, string> = {
  supports: "text-ok",
  contradicts: "text-risk",
  context: "text-muted-foreground",
};
const ORDER: EvidenceStance[] = ["supports", "contradicts", "context"];
const SHOWN = 4;
const QUANT_RE =
  /[+-]?\d+(?:[.,]\d+)?\s?(?:%|°C|K|МПа|ГПа|MPa|GPa|мА·ч\/г|mAh\/g|мм\/год|mm\/year|мкм|μm|nm|нм|h|ч|мин|циклов?)/gi;

interface EvidenceArticle {
  key: string;
  filename: string;
  documentId: string;
  stance: EvidenceStance;
  score: number;
  fromInput: boolean;
  fragments: ApiEvidence[];
}

function articleKey(e: ApiEvidence): string {
  return `doc:${e.document_id}`;
}

function primaryStance(items: ApiEvidence[]): EvidenceStance {
  const first = items.toSorted((a, b) => ORDER.indexOf(a.stance) - ORDER.indexOf(b.stance)).at(0);
  return first?.stance ?? "context";
}

function groupEvidence(evidence: ApiEvidence[]): EvidenceArticle[] {
  const map = new Map<string, ApiEvidence[]>();
  for (const e of evidence) {
    if (!e.document_id) continue;
    const key = articleKey(e);
    map.set(key, [...(map.get(key) ?? []), e]);
  }
  return [...map.entries()]
    .map(([key, items]) => {
      const sorted = items.toSorted(
        (a, b) =>
          ORDER.indexOf(a.stance) - ORDER.indexOf(b.stance) || (b.score ?? 0) - (a.score ?? 0),
      );
      const head = sorted.at(0);
      return {
        key,
        filename: head?.filename ?? "",
        documentId: head?.document_id ?? "",
        stance: primaryStance(sorted),
        score: Math.max(...sorted.map((e) => e.score ?? 0)),
        fromInput: sorted.some((e) => e.origin === "input"),
        fragments: sorted,
      };
    })
    .toSorted((a, b) => ORDER.indexOf(a.stance) - ORDER.indexOf(b.stance) || b.score - a.score);
}

export function evidenceArticleCount(evidence: ApiEvidence[]): number {
  return groupEvidence(evidence).length;
}

/** «стр. N · Раздел» from the fragment's source provenance — shown so the reader
 *  knows where the quote sits and that clicking jumps there. "" when unknown. */
function locationLabel(t: Translate, e: ApiEvidence): string {
  const parts: string[] = [];
  const start = e.page_start ?? 0;
  if (start > 0) {
    const end = e.page_end ?? 0;
    parts.push(
      end > start ? t("evidence.pageRange", { start, end }) : t("evidence.page", { page: start }),
    );
  }
  const section = (e.section_heading ?? "").trim();
  if (section !== "") parts.push(section);
  return parts.join(" · ");
}

// Машинные роли фрагмента в мостовой гипотезе → человеческие формулировки
// (сырое theme_a/theme_b/mediator в UI не показываем).
const RELATION_ROLE_KEYS: Record<
  string,
  | "evidence.relationRole.theme_a"
  | "evidence.relationRole.theme_b"
  | "evidence.relationRole.mediator"
> = {
  theme_a: "evidence.relationRole.theme_a",
  theme_b: "evidence.relationRole.theme_b",
  mediator: "evidence.relationRole.mediator",
};

function relationComment(t: Translate, evidence: ApiEvidence, subjectLabel: string): string {
  const prefix = t("evidence.relationPrefix", { subject: subjectLabel });
  const relation = (evidence.relation ?? "").trim();
  if (relation !== "") {
    const roleKey = RELATION_ROLE_KEYS[relation.toLowerCase()];
    return `${prefix} ${roleKey ? t(roleKey) : relation}`;
  }
  const stanceText = t(`evidence.relationText.${evidence.stance}`);
  if ((evidence.numeric_signals?.length ?? 0) > 0) {
    const list = evidence.numeric_signals?.slice(0, 3).join(", ") ?? "";
    return `${prefix} ${stanceText} ${t("evidence.numericSignals", { list })}`;
  }
  const values = [...new Set((evidence.snippet.match(QUANT_RE) ?? []).map((v) => v.trim()))].slice(
    0,
    3,
  );
  if (values.length === 0) return `${prefix} ${stanceText}`;
  return `${prefix} ${stanceText} ${t("evidence.numericSignals", { list: values.join(", ") })}`;
}

export function EvidenceMap({
  evidence,
  onOpen,
  subjectLabel,
}: {
  evidence: ApiEvidence[];
  onOpen: (evidence: ApiEvidence) => void;
  subjectLabel?: string;
}) {
  const { t } = useTranslation("hypothesisDetail");
  const [all, setAll] = useState(false);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [stanceFilter, setStanceFilter] = useState<EvidenceStance | null>(null);
  const subject = subjectLabel ?? t("evidence.subjectHypothesis");
  const articles = groupEvidence(evidence);
  const total = articles.length;
  const fragmentTotal = articles.reduce((sum, article) => sum + article.fragments.length, 0);
  if (total === 0) {
    return <p className="text-sm text-muted-foreground">{t("evidence.empty")}</p>;
  }
  const filtered = stanceFilter ? articles.filter((a) => a.stance === stanceFilter) : articles;
  const shown = all ? filtered : filtered.slice(0, SHOWN);
  const counts = ORDER.map((s) => ({ s, n: articles.filter((a) => a.stance === s).length })).filter(
    (x) => x.n > 0,
  );

  return (
    <div className="space-y-2.5">
      <div className="flex h-2 overflow-hidden rounded-full bg-muted" aria-hidden>
        {counts.map((c) => (
          <div key={c.s} className={DOT[c.s]} style={{ width: `${(c.n / total) * 100}%` }} />
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span>
          <span className="font-medium text-foreground">{total}</span>{" "}
          {t("evidence.docsWithFragment", { count: total })}
        </span>
        {fragmentTotal > total && (
          <span>
            <span className="font-medium text-foreground">{fragmentTotal}</span>{" "}
            {t("evidence.fragmentsBare", { count: fragmentTotal })}
          </span>
        )}
        {counts.map((c) => (
          <button
            key={c.s}
            type="button"
            aria-pressed={stanceFilter === c.s}
            title={t("evidence.filterHint")}
            onClick={() => setStanceFilter((prev) => (prev === c.s ? null : c.s))}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border border-transparent px-1.5 py-0.5 transition-colors hover:bg-muted",
              stanceFilter === c.s && "border-border bg-muted text-foreground",
            )}
          >
            <span className={cn("size-2 rounded-full", DOT[c.s])} />
            <span className="font-medium text-foreground">{c.n}</span> {t(`evidence.count.${c.s}`)}
          </button>
        ))}
        {stanceFilter && (
          <button
            type="button"
            onClick={() => setStanceFilter(null)}
            className="font-medium text-primary hover:underline"
          >
            {t("evidence.filterReset")}
          </button>
        )}
      </div>

      <div className="overflow-hidden rounded-xl border">
        {shown.map((article, i) => {
          const expanded = open[article.key] ?? article.fragments.length > 1;
          const first = article.fragments[0];
          if (!first) return null;
          return (
            <div key={article.key} className={cn(i > 0 && "border-t")}>
              <div className="flex items-start gap-3 px-3 py-2.5">
                <span
                  className={cn("mt-1.5 size-2 shrink-0 rounded-full", DOT[article.stance])}
                  aria-hidden
                />
                <button
                  type="button"
                  onClick={() => onOpen(first)}
                  title={t("evidence.openDocTitle")}
                  className={cn("group min-w-0 flex-1 text-left", "hover:text-primary")}
                >
                  <span className="flex items-center gap-2">
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">
                      {article.filename || t("evidence.unnamedDocument")}
                    </span>
                    {article.fromInput && (
                      <span className="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground">
                        {t("evidence.fromInput")}
                      </span>
                    )}
                    <span className={cn("shrink-0 text-xs font-medium", ROLE_TEXT[article.stance])}>
                      {t(`evidence.role.${article.stance}`)}
                    </span>
                    <ArrowUpRight
                      className="size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100"
                      aria-hidden
                    />
                  </span>
                  {first.snippet && (
                    <>
                      <span className="mt-0.5 line-clamp-1 block text-xs text-muted-foreground">
                        {first.snippet}
                      </span>
                      <span className="mt-1 line-clamp-2 block text-xs text-muted-foreground/90">
                        {relationComment(t, first, subject)}
                      </span>
                      {locationLabel(t, first) && (
                        <span className="mt-1 block text-[11px] font-medium text-muted-foreground">
                          {locationLabel(t, first)}
                        </span>
                      )}
                    </>
                  )}
                </button>
                {article.fragments.length > 1 && (
                  <button
                    type="button"
                    onClick={() => setOpen((prev) => ({ ...prev, [article.key]: !expanded }))}
                    className="inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-xs font-medium text-muted-foreground hover:bg-muted"
                    aria-label={
                      expanded ? t("evidence.collapseFragments") : t("evidence.expandFragments")
                    }
                  >
                    {article.fragments.length}
                    <ChevronDown
                      className={cn("size-3.5 transition-transform", expanded && "rotate-180")}
                      aria-hidden
                    />
                  </button>
                )}
              </div>
              {expanded && article.fragments.length > 1 && (
                <div className="space-y-1 border-t bg-muted/25 px-3 py-2">
                  {article.fragments.map((fragment, idx) => (
                    <button
                      key={fragment.id}
                      type="button"
                      onClick={() => onOpen(fragment)}
                      className={cn(
                        "flex w-full items-start gap-2 rounded-md px-2 py-1.5 text-left text-xs",
                        "hover:bg-background",
                      )}
                    >
                      <span className="mt-0.5 shrink-0 font-medium text-muted-foreground">
                        {idx + 1}.
                      </span>
                      <span className="min-w-0 flex-1 text-muted-foreground">
                        <span className={cn("mr-1 font-medium", ROLE_TEXT[fragment.stance])}>
                          {t(`evidence.role.${fragment.stance}`)}:
                        </span>
                        {fragment.snippet || t("evidence.emptyFragment")}
                        <span className="mt-1 block text-muted-foreground/90">
                          {relationComment(t, fragment, subject)}
                        </span>
                      </span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {filtered.length > SHOWN && (
        <button
          type="button"
          onClick={() => setAll((a) => !a)}
          className="text-xs font-medium text-primary hover:underline"
        >
          {all ? t("actions.collapse") : t("evidence.showAll", { count: filtered.length })}
        </button>
      )}
    </div>
  );
}
