import { useTranslation } from "react-i18next";

import { ScoringWeightsCard } from "@/features/hypothesis/ui/ScoringWeightsCard";
import { AiNote } from "@/shared/ui/AiNote";
import { Section } from "@/shared/ui/Section";

// Шаги пайплайна и словарь определений — только ключи неймспейса hypothesis;
// сами тексты живут в словарях (locales/*/hypothesis.json).
const PIPELINE = [
  "methodology.pipeline.parsing",
  "methodology.pipeline.topics",
  "methodology.pipeline.generation",
  "methodology.pipeline.ranking",
] as const;

const DEFINITIONS = [
  {
    groupKey: "methodology.definitions.evidence.group",
    items: ["methodology.definitions.evidence.stances", "methodology.definitions.evidence.verdict"],
  },
  {
    groupKey: "methodology.definitions.links.group",
    items: [
      "methodology.definitions.links.evidenceGraph",
      "methodology.definitions.links.knowledgeGraph",
    ],
  },
  {
    groupKey: "methodology.definitions.ranking.group",
    items: [
      "methodology.definitions.ranking.kpiFit",
      "methodology.definitions.ranking.evidenceBase",
      "methodology.definitions.ranking.novelty",
      "methodology.definitions.ranking.valueRisk",
      "methodology.definitions.ranking.readiness",
    ],
  },
] as const;

/** Справка по методологии фабрики. Плоская типографика с определениями —
 *  без карточек-в-карточках. */
export function MethodologyPanel() {
  const { t } = useTranslation("hypothesis");
  return (
    <div className="mt-6 max-w-3xl space-y-8">
      <Section title={t("methodology.howTitle")} description={t("methodology.howDescription")}>
        <ol className="space-y-4">
          {PIPELINE.map((step, i) => (
            <li key={step} className="flex gap-3">
              <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-primary font-mono text-xs font-semibold text-primary-foreground">
                {i + 1}
              </span>
              <div>
                <p className="text-sm font-medium">{t(`${step}.title`)}</p>
                <p className="mt-0.5 text-sm leading-relaxed text-muted-foreground">
                  {t(`${step}.text`)}
                </p>
              </div>
            </li>
          ))}
        </ol>
      </Section>

      {DEFINITIONS.map((block) => (
        <Section key={block.groupKey} title={t(block.groupKey)}>
          <dl className="divide-y border-t">
            {block.items.map((it) => (
              <div key={it} className="grid gap-1 py-3 sm:grid-cols-[13rem_1fr] sm:gap-4">
                <dt className="text-sm font-medium">{t(`${it}.term`)}</dt>
                <dd className="text-sm leading-relaxed text-muted-foreground">{t(`${it}.text`)}</dd>
              </div>
            ))}
          </dl>
        </Section>
      ))}

      <Section
        title={t("methodology.weightsTitle")}
        description={t("methodology.weightsDescription")}
      >
        <ScoringWeightsCard />
      </Section>

      <AiNote>{t("methodology.aiNote")}</AiNote>
    </div>
  );
}
