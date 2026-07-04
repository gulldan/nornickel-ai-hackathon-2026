// Состояние глобальных рантайм-настроек: один загруженный список + общий
// черновик правок, который карточки страницы настроек редактируют совместно и
// сохраняют одной кнопкой.
import { useCallback, useEffect, useMemo, useState } from "react";

import { adminGetAppSettings, adminPutAppSettings, type AppSettingView } from "./api";

export const OPENROUTER_URL = "https://openrouter.ai/api";
export const YANDEX_URL = "https://llm.api.cloud.yandex.net";

export type Provider = "yandex" | "openrouter" | "custom" | "stub";

export function providerOf(url: string): Provider {
  if (url === "") return "stub";
  if (url.includes("openrouter.ai")) return "openrouter";
  if (url.includes("cloud.yandex.net")) return "yandex";
  return "custom";
}

export function parseBool(v: string): boolean {
  switch (v.trim().toLowerCase()) {
    case "1":
    case "t":
    case "true":
    case "yes":
    case "on":
      return true;
    default:
      return false;
  }
}

type SettingSource = "db" | "env" | "default" | "draft";

export interface AppSettingsState {
  loaded: boolean;
  failed: boolean;
  busy: boolean;
  dirty: boolean;
  /** Эффективное значение: черновик → оверрайд БД → env → дефолт. */
  effective: (key: string) => string;
  sourceOf: (key: string) => SettingSource | null;
  item: (key: string) => AppSettingView | undefined;
  setValue: (key: string, value: string) => void;
  /** Сохранить черновик ("" → снять оверрайд). */
  save: () => Promise<boolean>;
  /** Отбросить несохранённые правки. */
  discard: () => void;
  /** Снять все оверрайды (вернуть конфигурацию развёртывания). */
  resetAll: () => Promise<boolean>;
}

export function useAppSettings(): AppSettingsState {
  const [items, setItems] = useState<AppSettingView[]>([]);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    adminGetAppSettings()
      .then((res) => {
        if (cancelled) return;
        setItems(res.settings);
        setLoaded(true);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const byKey = useMemo(() => new Map(items.map((s) => [s.key, s])), [items]);

  const effective = useCallback(
    (key: string): string => {
      const pending = draft[key];
      if (pending !== undefined) return pending;
      const s = byKey.get(key);
      if (!s) return "";
      if (s.hasOverride) return s.override;
      if (s.envSet) return s.envValue;
      return s.default;
    },
    [draft, byKey],
  );

  const sourceOf = useCallback(
    (key: string): SettingSource | null => {
      if (key in draft) return "draft";
      return byKey.get(key)?.source ?? null;
    },
    [draft, byKey],
  );

  const item = useCallback((key: string) => byKey.get(key), [byKey]);

  const setValue = useCallback((key: string, value: string) => {
    setDraft((prev) => ({ ...prev, [key]: value }));
  }, []);

  const push = useCallback(async (values: Record<string, string | null>): Promise<boolean> => {
    setBusy(true);
    try {
      const res = await adminPutAppSettings(values);
      setItems(res.settings);
      setDraft({});
      return true;
    } catch {
      return false;
    } finally {
      setBusy(false);
    }
  }, []);

  const save = useCallback(async (): Promise<boolean> => {
    const values: Record<string, string | null> = {};
    for (const [k, v] of Object.entries(draft)) values[k] = v === "" ? null : v;
    if (Object.keys(values).length === 0) return true;
    return push(values);
  }, [draft, push]);

  const resetAll = useCallback(async (): Promise<boolean> => {
    const values: Record<string, string | null> = {};
    for (const s of items) if (s.hasOverride) values[s.key] = null;
    setDraft({});
    if (Object.keys(values).length === 0) return true;
    return push(values);
  }, [items, push]);

  const discard = useCallback(() => setDraft({}), []);

  return {
    loaded,
    failed,
    busy,
    dirty: Object.keys(draft).length > 0,
    effective,
    sourceOf,
    item,
    setValue,
    save,
    discard,
    resetAll,
  };
}
