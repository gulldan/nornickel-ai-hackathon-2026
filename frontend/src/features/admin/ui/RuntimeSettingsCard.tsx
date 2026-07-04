import { useEffect, useState } from "react";
import { RotateCcw, SlidersHorizontal } from "lucide-react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";

import { Button } from "@/shared/ui/Button";
import { InfoHint } from "@/shared/ui/InfoHint";
import { Input } from "@/shared/ui/Input";
import { Label } from "@/shared/ui/Label";
import { Section } from "@/shared/ui/Section";
import {
  GENERATION_COUNT_MAX,
  fetchHypothesisRuntimeSettings,
  loadHypothesisRuntimeSettings,
  persistDefaultHypothesisRuntimeSettings,
  persistHypothesisRuntimeSettings,
  type HypothesisRuntimeSettings,
} from "@/shared/appSettings";
import { FieldRow, SettingsCard, ToggleRow } from "./SettingsBits";

type RuntimeNumberKey = Exclude<
  keyof HypothesisRuntimeSettings,
  "deepPostprocessEnabled" | "excludedDirections"
>;

interface RuntimeField {
  key: RuntimeNumberKey;
  min: number;
  max: number;
  unit?: "pcs" | "sec" | "percent";
}

// Два смысловых блока: сколько и как генерируем / когда карточка считается
// готовой или рискованной. Подписи и подсказки — в словаре (runtime.fields.<ключ>).
const GENERATION_FIELDS: RuntimeField[] = [
  { key: "defaultGenerateCount", min: 3, max: 10, unit: "pcs" },
  { key: "clusterGenerateCount", min: 3, max: 10, unit: "pcs" },
  { key: "directionGenerateCount", min: 1, max: 10, unit: "pcs" },
  { key: "generationTimeoutSec", min: 30, max: 180, unit: "sec" },
  { key: "graphDirectionLimit", min: 1, max: 20, unit: "pcs" },
];

const THRESHOLD_FIELDS: RuntimeField[] = [
  { key: "readyTrlMin", min: 1, max: 9 },
  { key: "readyScoreMin", min: 0, max: 100, unit: "percent" },
  { key: "riskScoreMin", min: 0, max: 100, unit: "percent" },
];

function RuntimeFieldInput({
  field,
  value,
  onChange,
}: {
  field: RuntimeField;
  value: number;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation("admin");
  return (
    <FieldRow
      id={`runtime-${field.key}`}
      label={t(`runtime.fields.${field.key}.label`)}
      hint={t(`runtime.fields.${field.key}.hint`)}
      unit={field.unit ? t(`units.${field.unit}`) : undefined}
    >
      <Input
        id={`runtime-${field.key}`}
        type="number"
        min={field.min}
        max={field.max}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-28"
      />
    </FieldRow>
  );
}

export function RuntimeSettingsCard() {
  const { t } = useTranslation("admin");
  const [settings, setSettings] = useState<HypothesisRuntimeSettings>(() =>
    loadHypothesisRuntimeSettings(),
  );
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    fetchHypothesisRuntimeSettings()
      .then((next) => {
        if (!cancelled) setSettings(next);
      })
      .catch(() => {
        if (!cancelled) toast.error(t("runtime.loadFailed"));
      });
    return () => {
      cancelled = true;
    };
  }, [t]);

  const setField = (key: RuntimeNumberKey, value: string) => {
    setSettings((prev) => ({ ...prev, [key]: Number(value) }));
  };

  const save = async () => {
    setBusy(true);
    try {
      const next = await persistHypothesisRuntimeSettings(settings);
      setSettings(next);
      toast.success(t("runtime.saved"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("runtime.saveFailed"));
    } finally {
      setBusy(false);
    }
  };

  const reset = async () => {
    setBusy(true);
    try {
      const next = await persistDefaultHypothesisRuntimeSettings();
      setSettings(next);
      toast.success(t("runtime.resetDone"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("runtime.resetFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <SettingsCard
      icon={SlidersHorizontal}
      title={t("runtime.title")}
      titleExtra={<InfoHint label={t("runtime.hintLabel")}>{t("runtime.hintBody")}</InfoHint>}
      description={t("runtime.description")}
      footer={
        <>
          <p className="text-xs text-muted-foreground">
            {t("runtime.serverLimit", { max: GENERATION_COUNT_MAX })}
          </p>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => void reset()}>
              <RotateCcw className="size-3.5" aria-hidden />
              {t("actions.resetToDefaults")}
            </Button>
            <Button size="sm" disabled={busy} onClick={() => void save()}>
              {busy ? t("actions.saving") : t("actions.save")}
            </Button>
          </div>
        </>
      }
    >
      <Section title={t("runtime.generation")}>
        <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2">
          {GENERATION_FIELDS.map((f) => (
            <RuntimeFieldInput
              key={f.key}
              field={f}
              value={settings[f.key]}
              onChange={(v) => setField(f.key, v)}
            />
          ))}
        </div>
      </Section>

      <Section title={t("runtime.queues")}>
        <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2">
          {THRESHOLD_FIELDS.map((f) => (
            <RuntimeFieldInput
              key={f.key}
              field={f}
              value={settings[f.key]}
              onChange={(v) => setField(f.key, v)}
            />
          ))}
        </div>
        <div className="mt-3 rounded-md border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
          {t("runtime.queuesHint", {
            trl: settings.readyTrlMin,
            score: settings.readyScoreMin,
            risk: settings.riskScoreMin,
          })}
        </div>
      </Section>

      <Section title={t("runtime.domain")}>
        <Label htmlFor="runtime-excluded" className="sr-only">
          {t("runtime.excluded")}
        </Label>
        <textarea
          id="runtime-excluded"
          value={settings.excludedDirections}
          onChange={(e) =>
            setSettings((prev) => ({ ...prev, excludedDirections: e.target.value.slice(0, 600) }))
          }
          rows={2}
          className="flex w-full resize-none rounded-lg border border-input bg-card px-3 py-2 text-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          placeholder={t("runtime.excludedPlaceholder")}
        />
        <p className="mt-1.5 text-xs text-muted-foreground">{t("runtime.excludedHint")}</p>
      </Section>

      <ToggleRow
        id="runtime-deep-postprocess"
        label={t("runtime.deepTitle")}
        hint={t("runtime.deepHint")}
        checked={settings.deepPostprocessEnabled}
        onCheckedChange={(checked) =>
          setSettings((prev) => ({ ...prev, deepPostprocessEnabled: checked }))
        }
      />
    </SettingsCard>
  );
}
