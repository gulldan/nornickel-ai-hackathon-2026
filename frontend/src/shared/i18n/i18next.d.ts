// Типизация ключей переводов: эталон — русские словари. Несуществующий ключ
// в t() — ошибка компиляции, а не молчаливый фолбэк в рантайме.
import type { defaultNS, resources } from "./resources";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: typeof defaultNS;
    resources: (typeof resources)["ru"];
  }
}
