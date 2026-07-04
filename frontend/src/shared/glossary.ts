// Plain-language definitions for the jargon a non-technical reader meets on the
// Hypothesis Factory board. Kept in one place so wording stays consistent
// wherever an InfoHint explains a term. The texts live in the common dictionary
// (glossary.*); getters keep the existing `GLOSSARY.term` call sites working
// and re-resolve on language change (the app remounts on switch).
import { i18n } from "@/shared/i18n";

export const GLOSSARY = {
  get goal() {
    return i18n.t("common:glossary.goal");
  },
  get theme() {
    return i18n.t("common:glossary.theme");
  },
  get hypothesis() {
    return i18n.t("common:glossary.hypothesis");
  },
  get landscape() {
    return i18n.t("common:glossary.landscape");
  },
  get trl() {
    return i18n.t("common:glossary.trl");
  },
  get itc() {
    return i18n.t("common:glossary.itc");
  },
  get verify() {
    return i18n.t("common:glossary.verify");
  },
  get specialties() {
    return i18n.t("common:glossary.specialties");
  },
  get ranking() {
    return i18n.t("common:glossary.ranking");
  },
  get cmpp() {
    return i18n.t("common:glossary.cmpp");
  },
  get experiment() {
    return i18n.t("common:glossary.experiment");
  },
  get feasibility() {
    return i18n.t("common:glossary.feasibility");
  },
} as const;
