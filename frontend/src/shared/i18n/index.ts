// Ядро локализации. Единственный реестр языков — LANGUAGES: добавить или
// выключить язык можно здесь одной записью (плюс папка словарей в locales/).
// Инициализацию выполняет initI18n() в точке входа приложения (src/index.tsx).
import { createInstance } from "i18next";
import { initReactI18next } from "react-i18next";

import { defaultNS, resources } from "./resources";

export const LANGUAGES = [
  { code: "ru", nativeLabel: "Русский", bcp47: "ru-RU" },
  { code: "en", nativeLabel: "English", bcp47: "en-US" },
  { code: "zh", nativeLabel: "中文", bcp47: "zh-CN" },
] as const;

export type LanguageCode = (typeof LANGUAGES)[number]["code"];

const STORAGE_KEY = "rag.lang";
const FALLBACK: LanguageCode = "ru";

export const i18n = createInstance();

function isSupported(code: string | null | undefined): code is LanguageCode {
  return LANGUAGES.some((l) => l.code === code);
}

// Выбор языка: явный выбор пользователя → русский. Автоопределение по
// браузеру убрано намеренно: аудитория стенда русскоязычная, а англоязычный
// браузер не должен молча переключать интерфейс — язык меняется только руками.
function detectLanguage(): LanguageCode {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (isSupported(saved)) return saved;
  } catch {
    /* приватный режим без localStorage — используем язык по умолчанию */
  }
  return FALLBACK;
}

/** Текущий язык интерфейса. */
export function currentLanguage(): LanguageCode {
  const base = (i18n.language ?? FALLBACK).slice(0, 2);
  return isSupported(base) ? base : FALLBACK;
}

/** BCP-47 локаль текущего языка — для дат/чисел (Intl). */
export function currentLocale(): string {
  return LANGUAGES.find((l) => l.code === currentLanguage())?.bcp47 ?? "ru-RU";
}

/** Переключить язык: применяется сразу и запоминается между сессиями. */
export function setLanguage(code: LanguageCode): void {
  void i18n.changeLanguage(code);
  try {
    localStorage.setItem(STORAGE_KEY, code);
  } catch {
    /* без localStorage выбор живёт до перезагрузки */
  }
  document.documentElement.lang = code;
}

/** Инициализация словарей и связки с React; идемпотентна. */
export function initI18n(): void {
  if (i18n.isInitialized) return;
  void i18n.use(initReactI18next).init({
    resources,
    defaultNS,
    lng: detectLanguage(),
    fallbackLng: FALLBACK,
    interpolation: { escapeValue: false }, // React сам экранирует
    returnEmptyString: false,
  });
  document.documentElement.lang = currentLanguage();
}
