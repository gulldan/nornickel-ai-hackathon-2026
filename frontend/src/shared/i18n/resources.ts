// Словари собраны статически: неймспейс на фичу, язык добавляется папкой
// locales/<код> и блоком ниже. Русский — эталон: типизация ключей t()
// выводится из него (см. i18next.d.ts), недостающий перевод падает на ru.
import ruAdmin from "./locales/ru/admin.json";
import ruApp from "./locales/ru/app.json";
import ruAuth from "./locales/ru/auth.json";
import ruCluster from "./locales/ru/cluster.json";
import ruCommon from "./locales/ru/common.json";
import ruDocument from "./locales/ru/document.json";
import ruHypothesis from "./locales/ru/hypothesis.json";
import ruHypothesisDetail from "./locales/ru/hypothesisDetail.json";
import ruKpi from "./locales/ru/kpi.json";
import ruSearch from "./locales/ru/search.json";
import ruSummary from "./locales/ru/summary.json";
import ruTask from "./locales/ru/task.json";

import enAdmin from "./locales/en/admin.json";
import enApp from "./locales/en/app.json";
import enAuth from "./locales/en/auth.json";
import enCluster from "./locales/en/cluster.json";
import enCommon from "./locales/en/common.json";
import enDocument from "./locales/en/document.json";
import enHypothesis from "./locales/en/hypothesis.json";
import enHypothesisDetail from "./locales/en/hypothesisDetail.json";
import enKpi from "./locales/en/kpi.json";
import enSearch from "./locales/en/search.json";
import enSummary from "./locales/en/summary.json";
import enTask from "./locales/en/task.json";

import zhAdmin from "./locales/zh/admin.json";
import zhApp from "./locales/zh/app.json";
import zhAuth from "./locales/zh/auth.json";
import zhCluster from "./locales/zh/cluster.json";
import zhCommon from "./locales/zh/common.json";
import zhDocument from "./locales/zh/document.json";
import zhHypothesis from "./locales/zh/hypothesis.json";
import zhHypothesisDetail from "./locales/zh/hypothesisDetail.json";
import zhKpi from "./locales/zh/kpi.json";
import zhSearch from "./locales/zh/search.json";
import zhSummary from "./locales/zh/summary.json";
import zhTask from "./locales/zh/task.json";

export const defaultNS = "common";

export const resources = {
  ru: {
    admin: ruAdmin,
    app: ruApp,
    auth: ruAuth,
    cluster: ruCluster,
    common: ruCommon,
    document: ruDocument,
    hypothesis: ruHypothesis,
    hypothesisDetail: ruHypothesisDetail,
    kpi: ruKpi,
    search: ruSearch,
    summary: ruSummary,
    task: ruTask,
  },
  en: {
    admin: enAdmin,
    app: enApp,
    auth: enAuth,
    cluster: enCluster,
    common: enCommon,
    document: enDocument,
    hypothesis: enHypothesis,
    hypothesisDetail: enHypothesisDetail,
    kpi: enKpi,
    search: enSearch,
    summary: enSummary,
    task: enTask,
  },
  zh: {
    admin: zhAdmin,
    app: zhApp,
    auth: zhAuth,
    cluster: zhCluster,
    common: zhCommon,
    document: zhDocument,
    hypothesis: zhHypothesis,
    hypothesisDetail: zhHypothesisDetail,
    kpi: zhKpi,
    search: zhSearch,
    summary: zhSummary,
    task: zhTask,
  },
} as const;
