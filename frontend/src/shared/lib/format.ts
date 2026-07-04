// Unified presentation formatters. Pages do NOT format dates/sizes/
// pluralization themselves — everything goes through this module (the convention
// is checked by scripts/check-conventions.ts, which runs together with lint).
// Даты и единицы следуют текущему языку интерфейса (shared/i18n).

import { currentLocale, i18n } from "@/shared/i18n";

const SIZE_UNIT_KEYS = ["bytes", "kb", "mb", "gb", "tb"] as const;

/** Human-readable byte size: 1093 → «1.1 КБ». */
export function humanSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < SIZE_UNIT_KEYS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  // Цикл не выпускает unit за границы массива; «??» — только для типов.
  const unitKey = SIZE_UNIT_KEYS[unit] ?? "bytes";
  const digits = value >= 100 || unit === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${i18n.t(`units.${unitKey}`)}`;
}

/** Date: «28 мая 2026». Empty/invalid string → «—». */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (!iso || Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString(currentLocale(), {
    day: "numeric",
    month: "long",
    year: "numeric",
  });
}

/** Date and time: «28 мая, 14:05». Empty/invalid string → «—». */
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (!iso || Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString(currentLocale(), {
    day: "numeric",
    month: "long",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Short date for tables: «28.05.2026». */
export function formatDateShort(iso: string): string {
  const d = new Date(iso);
  if (!iso || Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString(currentLocale());
}

/** Russian pluralization: pluralRu(5, ["документ", "документа", "документов"]).
 *  Легаси-хелпер для ещё не мигрированных строк; новые строки склоняются в
 *  словарях i18n (суффиксы _one/_few/_many). */
export function pluralRu(n: number, forms: [string, string, string]): string {
  const abs = Math.abs(n) % 100;
  const last = abs % 10;
  if (abs > 10 && abs < 20) return forms[2];
  if (last > 1 && last < 5) return forms[1];
  if (last === 1) return forms[0];
  return forms[2];
}
