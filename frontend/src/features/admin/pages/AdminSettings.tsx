import { useEffect, useState } from "react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { ScoringWeightsCard } from "@/features/hypothesis";
import { Button } from "@/shared/ui/Button";
import { PageHeader } from "@/shared/ui/PageHeader";
import { Tabs } from "@/shared/ui/Tabs";
import { useAppSettings } from "../model";
import { CostCard } from "../ui/CostCard";
import { GenerationFlagsCard } from "../ui/GenerationFlagsCard";
import { ProviderCard } from "../ui/ProviderCard";
import { RuntimeSettingsCard } from "../ui/RuntimeSettingsCard";

type SettingsTab = "model" | "generation" | "ranking";

export function AdminSettings() {
  const { t } = useTranslation("admin");
  const [tab, setTab] = useState<SettingsTab>("model");
  const app = useAppSettings();

  useEffect(() => {
    if (app.failed) toast.error(t("system.loadFailed"));
  }, [app.failed, t]);

  const save = async () => {
    if (await app.save()) toast.success(t("system.saved"));
    else toast.error(t("system.saveFailed"));
  };

  const tabHints: Record<SettingsTab, string> = {
    model: t("settings.tabHints.model"),
    generation: t("settings.tabHints.generation"),
    ranking: t("settings.tabHints.ranking"),
  };

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-6 md:py-8">
      <PageHeader title={t("settings.title")} description={t("settings.description")} />

      <Tabs
        className="mt-6"
        tabs={[
          { id: "model", label: t("settings.tabs.model") },
          { id: "generation", label: t("settings.tabs.generation") },
          { id: "ranking", label: t("settings.tabs.ranking") },
        ]}
        active={tab}
        onChange={(id) => setTab(id as SettingsTab)}
      />
      <p className="mt-3 max-w-3xl text-sm text-muted-foreground">{tabHints[tab]}</p>

      {tab === "model" && app.loaded && (
        <div className="mt-6 grid max-w-3xl gap-5">
          <ProviderCard app={app} />
          <CostCard app={app} />
        </div>
      )}
      {tab === "generation" && (
        <div className="mt-6 grid grid-cols-1 items-start gap-5 lg:grid-cols-2">
          {app.loaded && <GenerationFlagsCard app={app} />}
          <RuntimeSettingsCard />
        </div>
      )}
      {tab === "ranking" && (
        <div className="mt-6 max-w-3xl">
          <ScoringWeightsCard />
        </div>
      )}

      {app.dirty && (
        <div className="sticky bottom-4 z-10 mt-6 flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3 shadow-lg">
          <p className="text-sm text-muted-foreground">{t("system.unsaved")}</p>
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" disabled={app.busy} onClick={app.discard}>
              {t("actions.cancel")}
            </Button>
            <Button size="sm" disabled={app.busy} onClick={() => void save()}>
              {app.busy ? t("actions.saving") : t("actions.save")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
