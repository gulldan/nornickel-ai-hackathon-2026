import { Coins } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Input } from "@/shared/ui/Input";
import type { AppSettingsState } from "../model";
import { FieldRow, SettingsCard, SourceBadge } from "./SettingsBits";

/** Курс и прайс для расчёта расхода LLM на «Метриках» — правится без редеплоя
 *  (оверрайд БД → env → дефолт), как и провайдер. */
export function CostCard({ app }: { app: AppSettingsState }) {
  const { t } = useTranslation("admin");
  return (
    <SettingsCard icon={Coins} title={t("system.cost.title")} description={t("system.cost.desc")}>
      <FieldRow
        id="sys-rub-rate"
        label={t("system.cost.rubRate")}
        hint={t("system.cost.rubRateHint")}
        unit={t("system.cost.rubRateUnit")}
        badge={<SourceBadge app={app} k="LLM_RUB_PER_USD" />}
      >
        <Input
          id="sys-rub-rate"
          inputMode="decimal"
          value={app.effective("LLM_RUB_PER_USD")}
          onChange={(e) => app.setValue("LLM_RUB_PER_USD", e.target.value)}
          placeholder="90"
        />
      </FieldRow>
      <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2">
        <FieldRow
          id="sys-cost-currency"
          label={t("system.cost.currency")}
          hint={t("system.cost.currencyHint")}
          badge={<SourceBadge app={app} k="LLM_COST_CURRENCY" />}
        >
          <Input
            id="sys-cost-currency"
            value={app.effective("LLM_COST_CURRENCY")}
            onChange={(e) => app.setValue("LLM_COST_CURRENCY", e.target.value)}
            placeholder="₽"
          />
        </FieldRow>
        <FieldRow
          id="sys-prices"
          label={t("system.cost.prices")}
          hint={t("system.cost.pricesHint")}
          badge={<SourceBadge app={app} k="LLM_PRICES" />}
        >
          <Input
            id="sys-prices"
            value={app.effective("LLM_PRICES")}
            onChange={(e) => app.setValue("LLM_PRICES", e.target.value)}
            placeholder="yandexgpt=800/800"
          />
        </FieldRow>
      </div>
    </SettingsCard>
  );
}
