import { useTranslation } from "react-i18next";
import { Layers, Zap, Database } from "lucide-react";
import type { AnswerMeta, RagTrace } from "@/features/search/api";
import { currentLocale } from "@/shared/i18n";
import { Kicker } from "@/shared/ui/Kicker";

// Подписи этапов и деградаций живут в словарях — здесь только ключи по кодам.
const STAGE_KEYS = {
  embed: "trace.stages.embed",
  retrieve: "trace.stages.retrieve",
  rerank: "trace.stages.rerank",
  generate: "trace.stages.generate",
  total: "trace.stages.total",
} as const;

const DEGRADED_KEYS = {
  reranker_unavailable: "trace.degraded.rerankerUnavailable",
  embed_unavailable: "trace.degraded.embedUnavailable",
  dense_unavailable: "trace.degraded.denseUnavailable",
  lexical_unavailable: "trace.degraded.lexicalUnavailable",
} as const;

/** Развёрнутая панель «Как искали»: воронка кандидатов, тайминги этапов,
 *  деградации и отчёт о цитатах. Все данные — реальные из трассы ответа. */
export function SearchTrace({ meta }: { meta: AnswerMeta }) {
  const { t } = useTranslation("search");
  const tr: RagTrace | undefined = meta.trace;
  const fmtMs = (ms: number): string => {
    if (ms >= 1000) {
      // Десятичный разделитель зависит от языка (1,2 с / 1.2 s).
      const value = new Intl.NumberFormat(currentLocale()).format(Math.round(ms / 100) / 10);
      return t("trace.seconds", { value });
    }
    return t("trace.millis", { value: Math.round(ms) });
  };
  return (
    <div className="mt-3 space-y-3 rounded-lg border bg-secondary/50 p-4">
      <Kicker>{t("trace.title")}</Kicker>
      {tr ? (
        <>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
            <Database className="size-3.5 text-muted-foreground" aria-hidden />
            <span>
              {t("trace.candidates", {
                dense: tr.candidatesDense,
                lexical: tr.candidatesLexical,
              })}
            </span>
            <span className="text-muted-foreground">→</span>
            <span>{t("trace.fused", { count: tr.candidatesFused })}</span>
            <span className="text-muted-foreground">→</span>
            <span className="font-medium">
              {t("trace.returned", { count: tr.candidatesReturned })}
            </span>
          </div>
          {tr.stages.length > 0 && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-xs text-muted-foreground">
              <Zap className="size-3.5" aria-hidden />
              {tr.stages.map((s) => {
                const key = STAGE_KEYS[s.stage as keyof typeof STAGE_KEYS];
                return (
                  <span key={s.stage}>
                    {key ? t(key) : s.stage} {fmtMs(s.millis)}
                  </span>
                );
              })}
            </div>
          )}
          {tr.raptorExpanded > 0 && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Layers className="size-3.5" aria-hidden />
              {t("trace.raptorExpanded", { count: tr.raptorExpanded })}
            </div>
          )}
          {tr.degraded && (
            <p className="rounded-md bg-warn-wash px-3 py-2 text-sm text-warn">
              {t(
                DEGRADED_KEYS[tr.degradedReason as keyof typeof DEGRADED_KEYS] ??
                  "trace.degraded.default",
              )}
            </p>
          )}
          {tr.uncitedRemoved.length > 0 && (
            <p className="text-sm text-muted-foreground">
              {t("trace.uncitedRemoved", { count: tr.uncitedRemoved.length })}
            </p>
          )}
        </>
      ) : (
        <p className="text-sm text-muted-foreground">{t("trace.noTrace")}</p>
      )}
      <p className="font-mono text-xs text-muted-foreground">
        {meta.cached && `${t("trace.cachedNote")} · `}
        {meta.model && t("trace.model", { model: meta.model })}
      </p>
    </div>
  );
}
