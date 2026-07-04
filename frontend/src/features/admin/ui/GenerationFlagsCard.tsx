import { Globe } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Input } from "@/shared/ui/Input";
import { Section } from "@/shared/ui/Section";
import type { AppSettingsState } from "../model";
import { BoolRow, FieldRow, SettingsCard, SourceBadge } from "./SettingsBits";

export function GenerationFlagsCard({ app }: { app: AppSettingsState }) {
  const { t } = useTranslation("admin");
  return (
    <SettingsCard icon={Globe} title={t("system.title")} description={t("system.description")}>
      <Section title={t("system.pubsearch")}>
        <div className="space-y-3">
          <BoolRow
            app={app}
            k="PUBSEARCH_ENABLED"
            label={t("system.pubsearchEnabled")}
            hint={t("system.pubsearchEnabledHint")}
          />
          <FieldRow
            id="sys-mailto"
            label={t("system.mailto")}
            hint={t("system.mailtoHint")}
            badge={<SourceBadge app={app} k="PUBSEARCH_MAILTO" />}
          >
            <Input
              id="sys-mailto"
              type="email"
              value={app.effective("PUBSEARCH_MAILTO")}
              onChange={(e) => app.setValue("PUBSEARCH_MAILTO", e.target.value)}
              placeholder="team@example.com"
            />
          </FieldRow>
        </div>
      </Section>

      <Section title={t("system.generation")}>
        <div className="space-y-3">
          <BoolRow
            app={app}
            k="RAG_GEN_BREADTH"
            label={t("system.genBreadth")}
            hint={t("system.genBreadthHint")}
          />
          <BoolRow
            app={app}
            k="KPI_SUGGEST"
            label={t("system.kpiSuggest")}
            hint={t("system.kpiSuggestHint")}
          />
          <FieldRow
            id="sys-quality"
            label={t("system.qualityFloor")}
            hint={t("system.qualityFloorHint")}
            badge={<SourceBadge app={app} k="RAG_GEN_QUALITY_FLOOR" />}
          >
            <Input
              id="sys-quality"
              type="number"
              min={0}
              max={1}
              step={0.05}
              value={app.effective("RAG_GEN_QUALITY_FLOOR")}
              onChange={(e) => app.setValue("RAG_GEN_QUALITY_FLOOR", e.target.value)}
              className="w-28"
            />
          </FieldRow>
        </div>
      </Section>
    </SettingsCard>
  );
}
