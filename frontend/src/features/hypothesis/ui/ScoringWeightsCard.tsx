import { useEffect, useState } from "react";
import { Scale, RotateCcw } from "lucide-react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";

import {
  getScoringWeights,
  saveScoringWeights,
  type ScoringWeights,
} from "@/features/hypothesis/api";
import { Button } from "@/shared/ui/Button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/ui/Card";
import { InfoHint } from "@/shared/ui/InfoHint";
import { Label } from "@/shared/ui/Label";

const WEIGHT_FIELDS = [
  "kpi_fit",
  "evidence",
  "novelty",
  "value",
  "risk_inv",
  "trl_fit",
] as const satisfies readonly (keyof ScoringWeights)[];

const DEFAULT_WEIGHTS: ScoringWeights = {
  kpi_fit: 0.25,
  evidence: 0.2,
  novelty: 0.2,
  value: 0.15,
  risk_inv: 0.1,
  trl_fit: 0.1,
};

export function ScoringWeightsCard() {
  const { t } = useTranslation("admin");
  const [weights, setWeights] = useState<ScoringWeights | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getScoringWeights()
      .then((w) => {
        if (!cancelled) setWeights(w);
      })
      .catch(() => {
        if (!cancelled) setWeights(DEFAULT_WEIGHTS);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const total = weights ? WEIGHT_FIELDS.reduce((sum, f) => sum + (weights[f] || 0), 0) : 0;

  const setField = (key: keyof ScoringWeights, value: number) => {
    setWeights((prev) => (prev ? { ...prev, [key]: value } : prev));
  };

  const save = async () => {
    if (!weights) return;
    if (total <= 0) {
      toast.error(t("weights.atLeastOne"));
      return;
    }
    setBusy(true);
    try {
      const normalized = await saveScoringWeights(weights);
      setWeights(normalized);
      toast.success(t("weights.saved"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("weights.saveFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5 text-base">
          <Scale className="size-4 text-muted-foreground" aria-hidden />
          {t("weights.title")}
          <InfoHint label={t("weights.hintLabel")}>{t("weights.hintBody")}</InfoHint>
        </CardTitle>
        <CardDescription>{t("weights.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3.5">
        {weights === null ? (
          <p className="text-sm text-muted-foreground">{t("loading")}</p>
        ) : (
          <>
            {WEIGHT_FIELDS.map((key) => {
              const value = weights[key] || 0;
              const share = total > 0 ? Math.round((value / total) * 100) : 0;
              const label = t(`weights.fields.${key}.label`);
              return (
                <div key={key} className="space-y-1">
                  <div className="flex items-baseline justify-between gap-2">
                    <Label htmlFor={`weight-${key}`} className="font-normal">
                      {label}
                      <span className="ml-1.5 text-xs text-muted-foreground">
                        — {t(`weights.fields.${key}.hint`)}
                      </span>
                    </Label>
                    <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
                      {Math.round(value * 100)}%
                      <span className="ml-1.5 text-foreground">
                        {t("weights.share", { share })}
                      </span>
                    </span>
                  </div>
                  <input
                    id={`weight-${key}`}
                    type="range"
                    min={0}
                    max={100}
                    step={1}
                    value={Math.round(value * 100)}
                    onChange={(e) => setField(key, Number(e.target.value) / 100)}
                    className="w-full accent-primary"
                    aria-label={label}
                  />
                </div>
              );
            })}
            <div className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
              <p className="text-xs text-muted-foreground">
                {t("weights.total", { total: Math.round(total * 100) })}
              </p>
              <div className="flex gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={() => setWeights(DEFAULT_WEIGHTS)}
                >
                  <RotateCcw className="size-3.5" aria-hidden />
                  {t("actions.resetToDefaults")}
                </Button>
                <Button size="sm" disabled={busy} onClick={() => void save()}>
                  {busy ? t("actions.saving") : t("actions.save")}
                </Button>
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
