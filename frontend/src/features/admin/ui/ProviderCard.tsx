import { KeyRound, RotateCcw, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/shared/ui/Button";
import { Input } from "@/shared/ui/Input";
import { Label } from "@/shared/ui/Label";
import { Segmented } from "@/shared/ui/Segmented";
import {
  OPENROUTER_URL,
  YANDEX_URL,
  providerOf,
  type AppSettingsState,
  type Provider,
} from "../model";
import { FieldRow, SettingsCard, SourceBadge, StatStrip } from "./SettingsBits";

const MODEL_PLACEHOLDER: Record<Provider, string> = {
  yandex: "gpt://<идентификатор-каталога>/deepseek-v4-flash/latest",
  openrouter: "nvidia/nemotron-3-super-120b-a12b:free",
  custom: "qwen3-32b",
  stub: "",
};

export function ProviderCard({ app }: { app: AppSettingsState }) {
  const { t } = useTranslation("admin");

  const url = app.effective("VLLM_URL");
  const provider = providerOf(url);
  const segValue = provider === "stub" ? "custom" : provider;

  const onProvider = (p: "yandex" | "openrouter" | "custom") => {
    if (p === "yandex") app.setValue("VLLM_URL", YANDEX_URL);
    if (p === "openrouter") app.setValue("VLLM_URL", OPENROUTER_URL);
    if (p === "custom" && segValue !== "custom") app.setValue("VLLM_URL", "");
  };

  const model = app.effective("VLLM_MODEL");
  const keyItem = app.item("VLLM_API_KEY");
  const keySet = keyItem ? keyItem.source !== "default" : false;

  const providerNames: Record<Provider, string> = {
    yandex: t("system.providerYandex"),
    openrouter: t("system.providerOpenRouter"),
    custom: t("system.providerCustom"),
    stub: t("system.providerStub"),
  };

  const backToDeployment = () => {
    app.setValue("VLLM_URL", "");
    app.setValue("VLLM_MODEL", "");
    app.setValue("VLLM_API_KEY", "");
  };

  return (
    <SettingsCard
      icon={Sparkles}
      title={t("system.llm")}
      description={t("system.llmDesc")}
      footer={
        <Button variant="ghost" size="sm" disabled={app.busy} onClick={backToDeployment}>
          <RotateCcw className="size-3.5" aria-hidden />
          {t("system.backToDeployment")}
        </Button>
      }
    >
      <StatStrip
        items={[
          { label: t("system.statProvider"), value: providerNames[provider] },
          { label: t("system.statModel"), value: model || "—" },
          {
            label: t("system.statKey"),
            value: keySet ? t("system.statusKeySet") : t("system.statusKeyMissing"),
          },
        ]}
      />

      <div className="grid gap-1">
        <span className="flex items-center gap-2">
          <Label>{t("system.provider")}</Label>
          <SourceBadge app={app} k="VLLM_URL" />
        </span>
        <Segmented
          aria-label={t("system.provider")}
          size="md"
          value={segValue}
          onChange={onProvider}
          options={[
            { value: "yandex", label: t("system.providerYandex") },
            { value: "openrouter", label: t("system.providerOpenRouter") },
            { value: "custom", label: t("system.providerCustom") },
          ]}
        />
      </div>

      {segValue === "custom" && (
        <FieldRow id="sys-url" label={t("system.url")} hint={t("system.urlHint")}>
          <Input
            id="sys-url"
            value={url}
            onChange={(e) => app.setValue("VLLM_URL", e.target.value)}
            placeholder="http://llama-server:8080"
          />
        </FieldRow>
      )}

      <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2">
        <FieldRow
          id="sys-model"
          label={t("system.model")}
          hint={t("system.modelHint")}
          badge={<SourceBadge app={app} k="VLLM_MODEL" />}
        >
          <Input
            id="sys-model"
            value={model}
            onChange={(e) => app.setValue("VLLM_MODEL", e.target.value)}
            placeholder={MODEL_PLACEHOLDER[provider]}
          />
        </FieldRow>
        <FieldRow
          id="sys-apikey"
          label={t("system.apiKey")}
          hint={t("system.apiKeyHint")}
          badge={
            <>
              <KeyRound className="size-3 text-muted-foreground" aria-hidden />
              <SourceBadge app={app} k="VLLM_API_KEY" />
            </>
          }
        >
          <Input
            id="sys-apikey"
            type="password"
            autoComplete="off"
            value={app.sourceOf("VLLM_API_KEY") === "draft" ? app.effective("VLLM_API_KEY") : ""}
            onChange={(e) => app.setValue("VLLM_API_KEY", e.target.value)}
            placeholder={keySet ? t("system.apiKeySet") : t("system.apiKeyUnset")}
          />
        </FieldRow>
      </div>
    </SettingsCard>
  );
}
