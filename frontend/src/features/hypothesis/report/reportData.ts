// Данные печатного/Word-отчёта по портфелю: один загрузчик и общие помощники
// для страницы /hypotheses/report и генератора DOCX — контент обязан совпадать.
import { listClusters, type ApiCluster } from "@/features/cluster";
import {
  getHypothesis,
  getHypothesisBoard,
  type ApiHypothesis,
  type Itc,
  type ItcComponent,
} from "@/features/hypothesis/api";
import { isFallbackDraft, isResearchDirection, priorityScore } from "@/features/hypothesis/model";
import { parseGeneration, type ExternalWork } from "@/features/hypothesis/provenance";
import { listKPIs, type ApiKPI } from "@/features/kpi";

// Полный портфель в отчёте не нужен: топ по приоритету + примечание об остатке.
const REPORT_ROWS_MAX = 40;
const EXEC_THEMES_MAX = 8;
const PASSPORTS_MAX = 4;
const PILOTS_MAX = 5;

interface ThemeRow {
  cluster: ApiCluster;
  itc: Itc;
  linked: number;
}

export interface PilotProvenance {
  constraints?: string;
  works: ExternalWork[];
}

interface KpiRow {
  kpi: ApiKPI;
  total: number;
  approved: number;
  best: number | null;
}

export interface ReportData {
  ranked: ApiHypothesis[];
  shown: ApiHypothesis[];
  approvedTotal: number;
  kpis: ApiKPI[];
  kpiRows: KpiRow[];
  execThemes: ThemeRow[];
  passports: ThemeRow[];
  pilots: ApiHypothesis[];
  provenance: Record<string, PilotProvenance>;
}

export function kpiTargetLabel(k: ApiKPI): string {
  if (k.baseline === null && k.target === null) return "—";
  const unit = k.unit !== "" ? ` ${k.unit}` : "";
  if (k.baseline !== null && k.target !== null) return `${k.baseline} → ${k.target}${unit}`;
  return `${k.baseline ?? k.target}${unit}`;
}

// ИТЦ с бэка может прийти без компонент/полосы — отчёт не должен падать.
export function itcComponents(itc: Itc): ItcComponent[] {
  const c = itc.components;
  if (!c) return [];
  return [c.SM, c.NV, c.IP, c.HR].filter((x): x is ItcComponent => Boolean(x));
}

export function itcBandLine(itc: Itc): string {
  const label = itc.band?.label ?? "";
  const note = itc.band?.note ?? "";
  if (label && note) return `${label} — ${note}`;
  return label || note;
}

async function loadProvenance(pilots: ApiHypothesis[]): Promise<Record<string, PilotProvenance>> {
  const pairs = await Promise.all(
    pilots.map(async (h) => {
      try {
        const full = await getHypothesis(h.id);
        const gen = parseGeneration(full);
        return [h.id, { constraints: gen.constraints, works: gen.externalWorks }] as const;
      } catch {
        return [h.id, { works: [] }] as const;
      }
    }),
  );
  return Object.fromEntries(pairs);
}

export async function loadReportData(): Promise<ReportData> {
  const [board, kpis, clusters] = await Promise.all([
    getHypothesisBoard({ limit: 500, offset: 0 }),
    listKPIs(),
    listClusters().catch(() => [] as ApiCluster[]),
  ]);

  // Основной портфель: без разведочных направлений и fallback-черновиков.
  const ranked = board.items
    .filter((h) => !isFallbackDraft(h) && !isResearchDirection(h))
    .toSorted((a, b) => (priorityScore(b) ?? -1) - (priorityScore(a) ?? -1));

  const themes: ThemeRow[] = clusters
    .flatMap((c) => {
      const itc = c.params?.itc;
      if (!itc) return [];
      const linked = ranked.filter((h) => h.primary_cluster_id === c.id).length;
      return [{ cluster: c, itc, linked }];
    })
    .toSorted((a, b) => b.itc.techscore - a.itc.techscore);

  const kpiRows: KpiRow[] = kpis.map((k) => {
    const linked = ranked.filter((h) => h.kpi_id === k.id);
    const top = linked[0];
    return {
      kpi: k,
      total: linked.length,
      approved: linked.filter((h) => h.status === "approved").length,
      best: top ? priorityScore(top) : null,
    };
  });

  const pilots = ranked.slice(0, PILOTS_MAX);
  return {
    ranked,
    shown: ranked.slice(0, REPORT_ROWS_MAX),
    approvedTotal: ranked.filter((h) => h.status === "approved").length,
    kpis,
    kpiRows,
    execThemes: themes.slice(0, EXEC_THEMES_MAX),
    passports: themes.slice(0, PASSPORTS_MAX),
    pilots,
    provenance: await loadProvenance(pilots),
  };
}
