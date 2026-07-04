// Отчёт по одной R&D-гипотезе «по мотивам ГОСТ 7.32»: единый сборщик контента
// в виде плоского списка блоков. Из одних и тех же блоков рендерятся Word
// (hypothesisDocx.ts) и печатная страница для PDF (HypothesisReportPage) —
// содержимое обязано совпадать. Структура: шапка → паспорт → РЕФЕРАТ →
// ВВЕДЕНИЕ → 1 Гипотеза → 2 Оценки → 3 Доказательства → 4 План проверки →
// 5 Мировая практика → ЗАКЛЮЧЕНИЕ → СПИСОК ИСТОЧНИКОВ → Приложение А.
import { type ApiEvidence, type ApiHypothesis, type ApiRevision } from "@/features/hypothesis/api";
import { verdictMeta } from "@/features/hypothesis/board/model";
import {
  CONFIDENCE_FACETS,
  displayHypothesisTitle,
  pct,
  priorityScore,
  schemaMissingLabels,
  statusMeta,
  successCriteriaLines,
  trlMeta,
  visibleMeta,
} from "@/features/hypothesis/model";
import { parseGeneration, type GenerationInfo } from "@/features/hypothesis/provenance";
import {
  itcBandLine,
  itcComponents,
  kpiTargetLabel,
} from "@/features/hypothesis/report/reportData";
import { type ApiKPI } from "@/features/kpi";
import { i18n } from "@/shared/i18n";
import { formatDate, formatDateShort, formatDateTime } from "@/shared/lib/format";

export type ReportBlock =
  | {
      kind: "doctitle";
      org: string | null;
      doctype: string;
      title: string;
      meta: string[];
      lines: string[];
    }
  | { kind: "heading"; text: string; caps?: boolean }
  | { kind: "subheading"; text: string }
  | { kind: "paragraph"; text: string; muted?: boolean; italic?: boolean; bold?: boolean }
  | { kind: "kv"; rows: { label: string; value: string }[] }
  | { kind: "table"; title?: string; head: string[]; rows: string[][] }
  | { kind: "list"; items: string[]; ordered?: boolean }
  | { kind: "quote"; source: string; text: string; note?: string }
  | { kind: "references"; items: { n: number; text: string; url?: string }[] };

export interface HypothesisReportInput {
  h: ApiHypothesis;
  kpi?: ApiKPI;
  clusterTitle?: string;
  ownerName?: string | null;
  revisions: ApiRevision[];
}

export interface HypothesisReportDoc {
  fileName: string;
  title: string;
  blocks: ReportBlock[];
}

// Служебные теги конвейера — не научные специальности (правило детальной панели).
const SERVICE_TAGS = new Set([
  "auto_bridge",
  "auto_cluster",
  "discovery",
  "graph_direction",
  "on_demand",
  "fallback_evidence",
]);
const QUOTES_FOR_MAX = 6;
const COMPETITORS_MAX = 5;
const REVISIONS_MAX = 5;
const STAGE_ITEMS_MAX = 6;

// Ключи собираются динамически из десятков шаблонов — типизированный t здесь
// не работает; один осознанный каст вместо сотни литеральных вызовов.
const t = i18n.t.bind(i18n) as (key: string, opts?: Record<string, unknown>) => string;

/** statement/rationale приходят markdown-ish — в печатный документ идёт чистый текст. */
function plainText(s: string): string {
  return s
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/\*([^*]+)\*/g, "$1")
    .replace(/^#+\s*/gm, "")
    .replace(/^\s*[-•]\s+/gm, "— ")
    .trim();
}

/** Цитаты из OCR бывают рваными: обрезаем ~400 знаков по границе предложения. */
function trimSnippet(s: string): string {
  const flat = s.replace(/\s+/g, " ").trim();
  if (flat.length <= 400) return flat;
  const cut = flat.slice(0, 400);
  const end = Math.max(cut.lastIndexOf(". "), cut.lastIndexOf("! "), cut.lastIndexOf("? "));
  return end > 200 ? cut.slice(0, end + 1) : `${cut}…`;
}

/** Реферат склеивается из шаблонных фраз — каждая обязана кончаться точкой. */
function joinSentences(parts: string[]): string {
  return parts
    .map((p) => p.trim())
    .filter(Boolean)
    .map((p) => (/[.!?…]$/.test(p) ? p : `${p}.`))
    .join(" ");
}

function firstSentence(s: string): string {
  const flat = plainText(s).replace(/\s+/g, " ");
  const m = /^.{20,240}?[.!?](\s|$)/.exec(flat);
  return (m ? m[0] : flat.slice(0, 240)).trim();
}

function pagesLabel(ev: ApiEvidence): string {
  if (!ev.page_start) return "";
  const end = ev.page_end && ev.page_end !== ev.page_start ? `–${ev.page_end}` : "";
  return t("hypothesis:hreport.pages", { pages: `${ev.page_start}${end}` });
}

interface CitationRegistry {
  refOfDoc: Map<string, number>;
  entries: { n: number; text: string; url?: string }[];
}

/** Сквозная нумерация [1]…[N]: внутренние документы (дедуп по document_id),
 *  затем внешние публикации; список источников покрывает весь провенанс. */
function buildCitations(h: ApiHypothesis, gen: GenerationInfo): CitationRegistry {
  const refOfDoc = new Map<string, number>();
  const entries: { n: number; text: string; url?: string }[] = [];

  const docs = new Map<string, ApiEvidence[]>();
  for (const ev of h.evidence) {
    const key = ev.document_id || ev.filename;
    if (!key) continue;
    const list = docs.get(key);
    if (list) {
      list.push(ev);
    } else {
      docs.set(key, [ev]);
    }
  }
  for (const [key, group] of docs) {
    const n = entries.length + 1;
    refOfDoc.set(key, n);
    const first = group[0] as ApiEvidence;
    const sections = [
      ...new Set(group.map((e) => (e.section_heading ?? "").trim()).filter(Boolean)),
    ].slice(0, 3);
    const pageSpans = [...new Set(group.map((e) => pagesLabel(e)).filter(Boolean))].slice(0, 4);
    const parts = [t("hypothesis:hreport.internalDoc", { filename: first.filename })];
    if (sections.length > 0) {
      parts.push(t("hypothesis:hreport.internalSections", { sections: sections.join("; ") }));
    }
    if (pageSpans.length > 0) parts.push(pageSpans.join("; "));
    entries.push({ n, text: parts.join(" — ") });
  }

  for (const w of gen.externalWorks) {
    const n = entries.length + 1;
    refOfDoc.set(`ext:${w.doi ?? w.title}`, n);
    const venue = w.venue ? ` // ${w.venue}` : "";
    const year = w.year ? `. — ${w.year}` : "";
    const doi = w.doi ? `. — DOI: ${w.doi}` : w.source ? `. — ${w.source}` : "";
    entries.push({
      n,
      text: `${w.title}${venue}${year}${doi}`,
      url: w.doi ? `https://doi.org/${w.doi}` : undefined,
    });
  }

  return { refOfDoc, entries };
}

function evidenceCounts(h: ApiHypothesis): { forCount: number; againstCount: number } {
  const check = h.assessment.check;
  const evFor = h.evidence.filter((e) => e.stance === "supports").length;
  const evAgainst = h.evidence.filter((e) => e.stance === "contradicts").length;
  return {
    forCount: check?.supporting?.length ?? evFor,
    againstCount: check?.contradicting?.length ?? evAgainst,
  };
}

function generationKindLabel(gen: GenerationInfo): string {
  switch (gen.kind) {
    case "auto_bridge": {
      const themes = gen.themeA && gen.themeB ? ` (${gen.themeA} × ${gen.themeB})` : "";
      return t("hypothesis:hreport.genKindBridge") + themes;
    }
    case "auto_cluster":
      return (
        t("hypothesis:hreport.genKindCluster") + (gen.clusterLabel ? ` (${gen.clusterLabel})` : "")
      );
    case "on_demand":
      return t("hypothesis:hreport.genKindOnDemand");
    case "fallback":
      return t("hypothesis:hreport.genKindFallback");
    default:
      return t("hypothesis:hreport.genKindUnknown");
  }
}

function recommendation(h: ApiHypothesis): string {
  const verdict = h.assessment.check?.verdict;
  if (h.status === "approved" || verdict === "supported") {
    return t("hypothesis:hreport.recApprove");
  }
  if (h.status === "rejected" || verdict === "refuted") return t("hypothesis:hreport.recReject");
  if (h.status === "under_review" || verdict === "mixed" || verdict === "insufficient") {
    const missing = schemaMissingLabels(h.assessment.schema?.missing);
    return missing.length > 0
      ? t("hypothesis:hreport.recReviewMissing", { missing: missing.join(", ") })
      : t("hypothesis:hreport.recReview");
  }
  return t("hypothesis:hreport.recDecide");
}

function targetEffect(h: ApiHypothesis, kpi?: ApiKPI): string {
  const targets = (h.detail.quantitative_parameters ?? []).filter(
    (p) => (p.role ?? "").toLowerCase() === "target" && p.name,
  );
  if (targets.length > 0) {
    return targets
      .map((p) => `${p.name}: ${p.value ?? "—"}${p.unit ? ` ${p.unit}` : ""}`)
      .join("; ");
  }
  return kpi ? `${kpi.title}: ${kpiTargetLabel(kpi)}` : "";
}

export function buildHypothesisReport(input: HypothesisReportInput): HypothesisReportDoc {
  const { h, kpi, clusterTitle, ownerName, revisions } = input;
  const a = h.assessment;
  const d = h.detail;
  const gen = parseGeneration(h);
  const title = displayHypothesisTitle(h);
  const verdict = verdictMeta(h);
  const { forCount, againstCount } = evidenceCounts(h);
  const citations = buildCitations(h, gen);
  const blocks: ReportBlock[] = [];
  const id8 = h.id.slice(0, 8);

  const refOf = (ev: ApiEvidence): number | undefined =>
    citations.refOfDoc.get(ev.document_id || ev.filename);
  const extRef = (key: string): number | undefined => citations.refOfDoc.get(`ext:${key}`);

  // ---- Шапка (титульный блок) ----
  const provenanceLines = [
    t("hypothesis:hreport.generatedBy", {
      model: gen.model ?? "LLM",
      kind: generationKindLabel(gen),
      date: formatDateShort(h.created_at),
    }),
  ];
  if (ownerName) provenanceLines.push(t("hypothesis:hreport.ownerLine", { name: ownerName }));
  const binding = [
    kpi ? t("hypothesis:hreport.goalLine", { title: kpi.title, target: kpiTargetLabel(kpi) }) : "",
    clusterTitle ? t("hypothesis:hreport.themeLine", { title: clusterTitle }) : "",
  ].filter(Boolean);
  if (binding.length > 0) provenanceLines.push(binding.join(" · "));
  provenanceLines.push(
    ["approved", "rejected", "under_review"].includes(h.status)
      ? t("hypothesis:hreport.expertVerified")
      : t("hypothesis:hreport.expertNotVerified"),
  );
  if (!h.measurable) provenanceLines.push(t("hypothesis:hreport.scientificNote"));

  blocks.push({
    kind: "doctitle",
    org: visibleMeta(h.organization),
    doctype: t("hypothesis:hreport.doctype"),
    title,
    meta: [
      id8,
      statusMeta(h.status).label,
      verdict?.label ?? "",
      formatDateShort(h.updated_at),
      t("hypothesis:hreport.generatedAt", { date: formatDateTime(new Date().toISOString()) }),
      t("hypothesis:hreport.version", { n: revisions.length + 1 }),
    ].filter(Boolean),
    lines: provenanceLines,
  });

  // ---- Паспорт гипотезы ----
  const scienceTags = h.tags.filter((tag) => !SERVICE_TAGS.has(tag));
  const missing = schemaMissingLabels(a.schema?.missing);
  const passport: { label: string; value: string }[] = [
    {
      label: t("hypothesis:hreport.p.goal"),
      value: kpi ? `${kpi.title} (${kpiTargetLabel(kpi)})` : "",
    },
    { label: t("hypothesis:hreport.p.theme"), value: clusterTitle ?? "" },
    {
      label: t("hypothesis:hreport.p.verdict"),
      value: verdict
        ? `${verdict.label} (${t("hypothesis:hreport.forAgainst", { forCount, againstCount })})`
        : "",
    },
    {
      label: t("hypothesis:hreport.p.priority"),
      value: priorityScore(h) === null ? "" : `${priorityScore(h)}/100`,
    },
    { label: t("hypothesis:hreport.p.novelty"), value: pct(h.novelty_score) },
    { label: t("hypothesis:hreport.p.value"), value: pct(h.value_score) },
    {
      label: t("hypothesis:hreport.p.risk"),
      value: pct(h.risk_score) + (a.risk?.level ? ` (${a.risk.level})` : ""),
    },
    { label: t("hypothesis:hreport.p.confidence"), value: pct(h.confidence_score) },
    {
      label: t("hypothesis:hreport.p.itc"),
      value: a.itc
        ? `${a.itc.score}/10${itcBandLine(a.itc) ? ` — ${itcBandLine(a.itc)}` : ""}`
        : "",
    },
    {
      label: t("hypothesis:hreport.p.trl"),
      value:
        h.trl === null
          ? t("hypothesis:hreport.trlPending")
          : `TRL ${h.trl}/9${a.trl?.name ? ` — ${a.trl.name}` : ""}`,
    },
    {
      label: t("hypothesis:hreport.p.evidence"),
      value:
        h.evidence.length > 0 || forCount + againstCount > 0
          ? t("hypothesis:hreport.evidenceLine", {
              docs:
                h.evidence_document_count ??
                new Set(h.evidence.map((e) => e.document_id || e.filename)).size,
              forCount,
              againstCount,
            })
          : "",
    },
    { label: t("hypothesis:hreport.p.specialties"), value: scienceTags.join(", ") },
    {
      label: t("hypothesis:hreport.p.completeness"),
      value: a.schema?.score
        ? pct(a.schema.score) +
          (missing.length > 0
            ? ` (${t("hypothesis:hreport.missing", { missing: missing.join(", ") })})`
            : "")
        : "",
    },
  ].filter((r) => r.value !== "" && r.value !== "—");
  blocks.push({ kind: "kv", rows: passport });
  blocks.push({
    kind: "paragraph",
    text: t("hypothesis:hreport.disclaimer"),
    muted: true,
    italic: true,
  });

  // ---- РЕФЕРАТ ----
  blocks.push({ kind: "heading", text: t("hypothesis:hreport.abstractTitle"), caps: true });
  const objectText =
    [d.material_system, d.target_property].filter(Boolean).join("; ") || firstSentence(h.statement);
  const abstractParts = [
    t("hypothesis:hreport.a.object", { object: objectText }),
    kpi
      ? t("hypothesis:hreport.a.goal", { title: kpi.title, target: kpiTargetLabel(kpi) })
      : d.problem_addressed
        ? t("hypothesis:hreport.a.goalProblem", { problem: firstSentence(d.problem_addressed) })
        : "",
    t("hypothesis:hreport.a.data", {
      docs:
        h.evidence_document_count ??
        new Set(h.evidence.map((e) => e.document_id || e.filename)).size,
      external: gen.externalWorks.length,
    }),
    verdict
      ? t("hypothesis:hreport.a.result", { verdict: verdict.label, forCount, againstCount })
      : t("hypothesis:hreport.a.resultPending"),
    t("hypothesis:hreport.a.recommendation", { rec: recommendation(h) }),
    targetEffect(h, kpi) ? t("hypothesis:hreport.a.effect", { effect: targetEffect(h, kpi) }) : "",
  ].filter(Boolean);
  blocks.push({ kind: "paragraph", text: joinSentences(abstractParts) });
  const keywords = [
    ...scienceTags,
    ...(d.classification?.grnti ?? []).slice(0, 3).map((g) => g.name),
  ];
  const keywordsFallback = [d.target_property, d.material_system].filter(Boolean) as string[];
  const kw = (keywords.length > 0 ? keywords : keywordsFallback).slice(0, 8);
  if (kw.length > 0) {
    blocks.push({
      kind: "paragraph",
      text: t("hypothesis:hreport.keywords", { words: kw.join(", ").toUpperCase() }),
    });
  }

  // ---- ВВЕДЕНИЕ ----
  blocks.push({ kind: "heading", text: t("hypothesis:hreport.introTitle"), caps: true });
  blocks.push({
    kind: "paragraph",
    text: plainText(d.problem_addressed || h.rationale || firstSentence(h.statement)),
  });
  if (kpi) {
    blocks.push({
      kind: "paragraph",
      text: t("hypothesis:hreport.introGoal", { title: kpi.title, target: kpiTargetLabel(kpi) }),
    });
  }
  if ((d.drivers?.length ?? 0) > 0) {
    blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.driversLead"), bold: true });
    blocks.push({ kind: "list", items: d.drivers as string[] });
  }
  const freshness = a.topic_freshness;
  const relevance = [
    freshness?.rationale
      ? t("hypothesis:hreport.freshnessNote", {
          value: pct(freshness.score),
          rationale: freshness.rationale,
        })
      : "",
    a.novelty?.rationale ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  if (relevance) blocks.push({ kind: "paragraph", text: relevance });
  blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.abbreviations"), muted: true });

  // ---- Основная часть: нумерация после фильтрации пустых разделов ----
  let sec = 0;

  // 1 Гипотеза и её обоснование — присутствует всегда.
  blocks.push({
    kind: "heading",
    text: `${++sec} ${t("hypothesis:hreport.sec1Title")}`,
  });
  blocks.push({ kind: "paragraph", text: plainText(h.statement) });
  if (h.rationale.trim() !== "") {
    blocks.push({ kind: "subheading", text: t("hypothesis:hreport.rationaleSub") });
    blocks.push({ kind: "paragraph", text: plainText(h.rationale) });
  }
  if (gen.constraints) {
    blocks.push({
      kind: "paragraph",
      text: t("hypothesis:hreport.constraintsLead", { constraints: gen.constraints }),
    });
  }
  const quant = (d.quantitative_parameters ?? []).filter((p) => p.name);
  if (quant.length > 0) {
    blocks.push({
      kind: "table",
      title: t("hypothesis:hreport.quantTableTitle"),
      head: [
        t("hypothesis:hreport.q.param"),
        t("hypothesis:hreport.q.value"),
        t("hypothesis:hreport.q.unit"),
        t("hypothesis:hreport.q.role"),
      ],
      rows: quant.map((p) => [
        p.name ?? "",
        p.value === undefined ? "—" : String(p.value),
        p.unit ?? "",
        p.role ?? "",
      ]),
    });
  }
  const materialRows = [
    { label: t("hypothesis:hreport.m.system"), value: d.material_system ?? "" },
    { label: t("hypothesis:hreport.m.composition"), value: d.composition_change ?? "" },
    { label: t("hypothesis:hreport.m.process"), value: d.process_change ?? "" },
    { label: t("hypothesis:hreport.m.microstructure"), value: d.microstructure_mechanism ?? "" },
    { label: t("hypothesis:hreport.m.property"), value: d.target_property ?? "" },
  ].filter((r) => r.value.trim() !== "");
  if (materialRows.length > 0) {
    blocks.push({ kind: "subheading", text: t("hypothesis:hreport.materialsSub") });
    blocks.push({ kind: "kv", rows: materialRows });
  }
  const ap = d.application_potential;
  if (ap && ((ap.objects?.length ?? 0) > 0 || (ap.conditions ?? "") !== "")) {
    const text = [
      (ap.objects?.length ?? 0) > 0
        ? t("hypothesis:hreport.applicationObjects", {
            objects: (ap.objects as string[]).join(", "),
          })
        : "",
      ap.conditions
        ? t("hypothesis:hreport.applicationConditions", { conditions: ap.conditions })
        : "",
    ]
      .filter(Boolean)
      .join(" ");
    blocks.push({ kind: "paragraph", text });
  }
  const chain = (d.causal_chain ?? []).filter((s) => s.stage || s.change);
  if (chain.length > 0) {
    blocks.push({ kind: "subheading", text: `${sec}.1 ${t("hypothesis:hreport.mechanismSub")}` });
    blocks.push({
      kind: "table",
      title: t("hypothesis:hreport.causalTableTitle"),
      head: [t("hypothesis:hreport.c.stage"), t("hypothesis:hreport.c.change")],
      rows: chain.map((s) => [s.stage ?? "", s.change ?? ""]),
    });
  }

  // 2 Оценки и их объяснения.
  blocks.push({ kind: "heading", text: `${++sec} ${t("hypothesis:hreport.sec2Title")}` });
  blocks.push({
    kind: "paragraph",
    text: t("hypothesis:hreport.disclaimer"),
    muted: true,
    italic: true,
  });
  const ranking = a.ranking;
  if (ranking && (ranking.factors?.length ?? 0) > 0) {
    blocks.push({
      kind: "table",
      title: t("hypothesis:hreport.rankingTableTitle"),
      head: [
        t("hypothesis:hreport.r.factor"),
        t("hypothesis:hreport.r.weight"),
        t("hypothesis:hreport.r.value"),
        t("hypothesis:hreport.r.contribution"),
        t("hypothesis:hreport.r.detail"),
      ],
      rows: ranking.factors.map((f) => [
        f.label,
        f.weight.toFixed(2),
        f.scored ? pct(f.value) : "—",
        f.contribution.toFixed(3),
        f.detail,
      ]),
    });
    const method = [ranking.method, ranking.formula, ranking.version].filter(Boolean).join(" · ");
    if (method) blocks.push({ kind: "paragraph", text: method, muted: true });
    if (priorityScore(h) !== null) {
      blocks.push({
        kind: "paragraph",
        text: t("hypothesis:hreport.priorityTotal", { score: priorityScore(h) }),
        bold: true,
      });
    }
  }
  const dims = [
    {
      label: t("hypothesis:hreport.p.novelty"),
      score: a.novelty?.score,
      why: a.novelty?.rationale,
    },
    {
      label: t("hypothesis:hreport.freshnessLabel"),
      score: a.topic_freshness?.score,
      why: a.topic_freshness?.rationale,
    },
    { label: t("hypothesis:hreport.p.value"), score: a.value?.score, why: a.value?.rationale },
  ];
  for (const dim of dims) {
    if (dim.score === undefined && !dim.why) continue;
    blocks.push({
      kind: "paragraph",
      text: `${dim.label}: ${pct(dim.score)}${dim.why ? ` — ${dim.why}` : ""}`,
    });
  }
  if (a.risk && (a.risk.score !== undefined || (a.risk.factors?.length ?? 0) > 0)) {
    const lead = `${t("hypothesis:hreport.p.risk")}: ${pct(a.risk.score)}${
      a.risk.level ? ` (${a.risk.level})` : ""
    }${a.risk.rationale ? ` — ${a.risk.rationale}` : ""}`;
    blocks.push({ kind: "paragraph", text: lead });
    if ((a.risk.factors?.length ?? 0) > 0) {
      blocks.push({ kind: "list", items: a.risk.factors as string[] });
    }
  }
  if (h.trl !== null || a.trl) {
    const trlLine = [
      t("hypothesis:hreport.trlLine", {
        level: h.trl ?? a.trl?.level ?? "—",
        name: a.trl?.name ?? trlMeta(h.trl).label,
      }),
      a.trl?.rationale ?? "",
      a.trl?.standard ? `(${a.trl.standard})` : "",
    ]
      .filter(Boolean)
      .join(" ");
    blocks.push({ kind: "paragraph", text: trlLine });
    const level = h.trl ?? a.trl?.level ?? 0;
    const levels = (a.trl?.levels ?? []).filter((l) => l.level <= level + 1);
    if (levels.length > 0) {
      blocks.push({
        kind: "list",
        items: levels.map(
          (l) =>
            `TRL ${l.level}: ${
              l.met ? t("hypothesis:hreport.trlMet") : t("hypothesis:hreport.trlNotMet")
            }${l.note ? ` — ${l.note}` : ""}`,
        ),
      });
    }
  }
  if (a.itc) {
    blocks.push({
      kind: "paragraph",
      text: `${t("hypothesis:hreport.p.itc")}: ${a.itc.score}/10${
        itcBandLine(a.itc) ? ` — ${itcBandLine(a.itc)}` : ""
      }`,
    });
    const comps = itcComponents(a.itc);
    if (comps.length > 0) {
      blocks.push({
        kind: "kv",
        rows: comps.map((c) => ({
          label: c.name,
          value: `${Math.round(c.norm * 100)}${c.note ? ` — ${c.note}` : ""}`,
        })),
      });
    }
  }
  if (a.scores) {
    const rows = CONFIDENCE_FACETS.map((f) => ({
      label: f.label,
      value: a.scores?.[f.key] === undefined ? "" : pct(a.scores[f.key] as number),
    })).filter((r) => r.value !== "");
    if (rows.length > 0) {
      blocks.push({ kind: "subheading", text: t("hypothesis:hreport.confidenceSub") });
      blocks.push({ kind: "kv", rows });
    }
    if (a.scores.unverified) {
      blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.unverifiedNote"), muted: true });
    }
  }
  if (a.learning_priority?.score !== undefined) {
    blocks.push({
      kind: "paragraph",
      text: t("hypothesis:hreport.learningLine", {
        score: pct(a.learning_priority.score),
        value: pct(a.learning_priority.value),
        uncertainty: pct(a.learning_priority.uncertainty),
      }),
    });
  }

  // 3 Доказательства за и против — не пропускается, деградирует честно.
  blocks.push({ kind: "heading", text: `${++sec} ${t("hypothesis:hreport.sec3Title")}` });
  const check = a.check;
  if (!check && h.evidence.length === 0) {
    blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.noEvidence") });
  } else {
    if (check?.verdict && verdict) {
      blocks.push({
        kind: "paragraph",
        text: `${verdict.label}${
          check.confidence === undefined
            ? ""
            : ` (${t("hypothesis:hreport.checkConfidence", { value: pct(check.confidence) })})`
        }`,
        bold: true,
      });
      if (check.rationale) blocks.push({ kind: "paragraph", text: check.rationale });
      const meta = [
        check.model ? t("hypothesis:hreport.checkModel", { model: check.model }) : "",
        check.checked_at
          ? t("hypothesis:hreport.checkedAt", { date: formatDateTime(check.checked_at) })
          : "",
      ]
        .filter(Boolean)
        .join(" · ");
      if (meta) blocks.push({ kind: "paragraph", text: meta, muted: true });
    } else {
      blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.checkPending"), muted: true });
    }
    const contextCount = h.evidence.filter((e) => e.stance === "context").length;
    blocks.push({
      kind: "table",
      title: t("hypothesis:hreport.balanceTableTitle"),
      head: [
        t("hypothesis:hreport.b.total"),
        t("hypothesis:hreport.b.docs"),
        t("hypothesis:hreport.b.for"),
        t("hypothesis:hreport.b.against"),
        t("hypothesis:hreport.b.context"),
      ],
      rows: [
        [
          String(h.evidence_count ?? h.evidence.length),
          String(
            h.evidence_document_count ??
              new Set(h.evidence.map((e) => e.document_id || e.filename)).size,
          ),
          String(forCount),
          String(againstCount),
          String(contextCount),
        ],
      ],
    });

    const quoteBlock = (ev: ApiEvidence): ReportBlock => {
      const n = refOf(ev);
      const src = [
        n === undefined ? "" : `[${n}]`,
        ev.filename,
        ev.section_heading ?? "",
        pagesLabel(ev),
      ]
        .filter(Boolean)
        .join(", ");
      const note = [
        ev.relation ?? "",
        (ev.numeric_signals?.length ?? 0) > 0
          ? `(${(ev.numeric_signals as string[]).join("; ")})`
          : "",
      ]
        .filter(Boolean)
        .join(" ");
      return { kind: "quote", source: src, text: trimSnippet(ev.snippet), note: note || undefined };
    };

    const supports = h.evidence
      .filter((e) => e.stance === "supports")
      .toSorted((x, y) => (y.score ?? 0) - (x.score ?? 0));
    if (supports.length > 0) {
      blocks.push({ kind: "subheading", text: t("hypothesis:hreport.forSub") });
      for (const ev of supports.slice(0, QUOTES_FOR_MAX)) blocks.push(quoteBlock(ev));
      if (supports.length > QUOTES_FOR_MAX) {
        blocks.push({
          kind: "paragraph",
          text: t("hypothesis:hreport.moreQuotes", { count: supports.length - QUOTES_FOR_MAX }),
          muted: true,
        });
      }
    }
    blocks.push({ kind: "subheading", text: t("hypothesis:hreport.againstSub") });
    const contradicts = h.evidence.filter((e) => e.stance === "contradicts");
    if (contradicts.length > 0) {
      for (const ev of contradicts) blocks.push(quoteBlock(ev));
    } else {
      blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.noAgainst") });
    }
    if (contextCount > 0) {
      blocks.push({
        kind: "paragraph",
        text: t("hypothesis:hreport.contextCount", { count: contextCount }),
        muted: true,
      });
    }
    if (gen.externalWorks.length > 0) {
      blocks.push({ kind: "subheading", text: t("hypothesis:hreport.externalSub") });
      blocks.push({
        kind: "list",
        items: gen.externalWorks.map((w) => {
          const n = extRef(w.doi ?? w.title);
          return `${n === undefined ? "" : `[${n}] `}${w.title}${w.venue ? `, ${w.venue}` : ""}${
            w.year ? ` (${w.year})` : ""
          }`;
        }),
      });
    }
  }

  // 4 План проверки — пропускается целиком, когда нечего печатать.
  const plan = d.experiment_plan;
  const hasVerification =
    (d.verification?.method ?? "") !== "" ||
    (d.verification?.metrics?.length ?? 0) > 0 ||
    successCriteriaLines(d.verification?.success_criteria).length > 0;
  if (hasVerification || plan || (d.feasibility?.length ?? 0) > 0) {
    blocks.push({ kind: "heading", text: `${++sec} ${t("hypothesis:hreport.sec4Title")}` });
    if (d.verification?.method) {
      blocks.push({ kind: "paragraph", text: d.verification.method });
    }
    if ((d.verification?.metrics?.length ?? 0) > 0) {
      blocks.push({
        kind: "paragraph",
        text: t("hypothesis:hreport.metricsLead"),
        bold: true,
      });
      blocks.push({ kind: "list", items: d.verification?.metrics as string[] });
    }
    const baseCriteria = successCriteriaLines(d.verification?.success_criteria);
    const planCriteria = successCriteriaLines(plan?.success_criteria);
    const criteria = [...new Set([...baseCriteria, ...planCriteria])];
    if (criteria.length > 0) {
      blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.criteriaLead"), bold: true });
      blocks.push({ kind: "list", items: criteria, ordered: true });
    }
    if (d.verification?.horizon) {
      blocks.push({
        kind: "paragraph",
        text: t("hypothesis:hreport.horizonLine", { horizon: d.verification.horizon }),
      });
    }
    if (plan) {
      const stages = (plan.sections ?? []).filter((s) => s.title || (s.items?.length ?? 0) > 0);
      if (stages.length > 0) {
        blocks.push({ kind: "subheading", text: t("hypothesis:hreport.roadmapSub") });
        stages.forEach((s, i) => {
          blocks.push({
            kind: "paragraph",
            text: `${i + 1}. ${s.title ?? ""}${s.purpose ? ` — ${s.purpose}` : ""}`,
            bold: true,
          });
          if ((s.items?.length ?? 0) > 0) {
            blocks.push({ kind: "list", items: (s.items as string[]).slice(0, STAGE_ITEMS_MAX) });
          }
        });
      }
      const params = (plan.process_parameters ?? []).filter((p) => p.name);
      if (params.length > 0) {
        blocks.push({
          kind: "table",
          title: t("hypothesis:hreport.processTableTitle"),
          head: [t("hypothesis:hreport.q.param"), t("hypothesis:hreport.q.range")],
          rows: params.map((p) => [p.name ?? "", p.range ?? ""]),
        });
      }
      const methodLines = [
        { label: t("hypothesis:hreport.pl.materials"), items: plan.materials },
        {
          label: t("hypothesis:hreport.pl.characterization"),
          items: [
            ...new Set([
              ...(plan.characterization_methods ?? []),
              ...(d.characterization_methods ?? []),
            ]),
          ],
        },
        {
          label: t("hypothesis:hreport.pl.tests"),
          items: [...new Set([...(plan.test_methods ?? []), ...(d.test_methods ?? [])])],
        },
        { label: t("hypothesis:hreport.pl.controls"), items: plan.controls },
      ].filter((r) => (r.items?.length ?? 0) > 0);
      if (methodLines.length > 0) {
        blocks.push({
          kind: "kv",
          rows: methodLines.map((r) => ({
            label: r.label,
            value: (r.items as string[]).join(", "),
          })),
        });
      }
      if (plan.estimated_cost || plan.estimated_time) {
        blocks.push({
          kind: "paragraph",
          text: t("hypothesis:hreport.resourcesLine", {
            cost: plan.estimated_cost ?? "—",
            time: plan.estimated_time ?? "—",
          }),
        });
      }
      const risks = [...new Set([...(plan.risks ?? []), ...(d.failure_modes ?? [])])];
      if (risks.length > 0) {
        blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.risksLead"), bold: true });
        blocks.push({ kind: "list", items: risks });
      }
      const planMeta = [
        plan.model ? t("hypothesis:hreport.planModel", { model: plan.model }) : "",
        plan.planned_at ? formatDate(plan.planned_at) : "",
      ]
        .filter(Boolean)
        .join(", ");
      if (planMeta) blocks.push({ kind: "paragraph", text: planMeta, muted: true });
    }
    const feasibility = (d.feasibility ?? []).filter((f) => f.aspect);
    if (feasibility.length > 0) {
      blocks.push({
        kind: "table",
        title: t("hypothesis:hreport.feasibilityTableTitle"),
        head: [
          t("hypothesis:hreport.f.aspect"),
          t("hypothesis:hreport.f.level"),
          t("hypothesis:hreport.f.note"),
        ],
        rows: feasibility.map((f) => [
          f.aspect ?? "",
          f.level ? t(`hypothesis:hreport.level.${f.level}`) : "—",
          f.note ?? "",
        ]),
      });
    }
  }

  // 5 Мировая практика и ограничения.
  const competitors = d.competitors;
  const constraintsList = ap?.constraints ?? [];
  if ((competitors?.items?.length ?? 0) > 0 || competitors?.summary || constraintsList.length > 0) {
    blocks.push({ kind: "heading", text: `${++sec} ${t("hypothesis:hreport.sec5Title")}` });
    if (competitors?.summary) blocks.push({ kind: "paragraph", text: competitors.summary });
    if ((competitors?.items?.length ?? 0) > 0) {
      blocks.push({
        kind: "table",
        title: t("hypothesis:hreport.competitorsTableTitle"),
        head: [
          t("hypothesis:hreport.co.name"),
          t("hypothesis:hreport.co.approach"),
          t("hypothesis:hreport.co.maturity"),
          t("hypothesis:hreport.co.source"),
        ],
        rows: (competitors?.items ?? [])
          .slice(0, COMPETITORS_MAX)
          .map((c) => [
            c.name ?? "",
            [c.approach, (c.strengths ?? []).join("; "), (c.weaknesses ?? []).join("; ")]
              .filter(Boolean)
              .join(". "),
            c.maturity ?? "",
            c.source ?? "",
          ]),
      });
      const compMeta = [
        competitors?.model ? t("hypothesis:hreport.planModel", { model: competitors.model }) : "",
        competitors?.analyzed_at ? formatDate(competitors.analyzed_at) : "",
      ]
        .filter(Boolean)
        .join(", ");
      if (compMeta) blocks.push({ kind: "paragraph", text: compMeta, muted: true });
    }
    if (constraintsList.length > 0) {
      blocks.push({ kind: "paragraph", text: t("hypothesis:hreport.limitationsLead"), bold: true });
      blocks.push({ kind: "list", items: constraintsList });
    }
  }

  // ---- ЗАКЛЮЧЕНИЕ ----
  blocks.push({ kind: "heading", text: t("hypothesis:hreport.conclusionTitle"), caps: true });
  const conclusions: string[] = [];
  conclusions.push(
    verdict
      ? t("hypothesis:hreport.conclVerdict", {
          verdict: verdict.label,
          forCount,
          againstCount,
        })
      : t("hypothesis:hreport.conclVerdictPending"),
  );
  if (h.trl !== null || a.itc) {
    const trlPart = h.trl === null ? "—" : `${h.trl}${a.trl?.name ? ` — ${a.trl.name}` : ""}`;
    conclusions.push(
      a.itc
        ? t("hypothesis:hreport.conclReadiness", {
            trl: trlPart,
            itc: `${a.itc.score}/10${itcBandLine(a.itc) ? ` (${itcBandLine(a.itc)})` : ""}`,
          })
        : t("hypothesis:hreport.conclReadinessTrlOnly", { trl: trlPart }),
    );
  }
  const topFactor = (ranking?.factors ?? []).toSorted((x, y) => y.contribution - x.contribution)[0];
  if (priorityScore(h) !== null) {
    conclusions.push(
      t("hypothesis:hreport.conclPriority", {
        score: priorityScore(h),
        factor: topFactor ? `${topFactor.label} (+${topFactor.contribution.toFixed(2)})` : "—",
      }),
    );
  }
  const mainRisk = a.risk?.factors?.[0] ?? d.experiment_plan?.risks?.[0];
  if (mainRisk) {
    conclusions.push(t("hypothesis:hreport.conclRisk", { risk: mainRisk }));
  }
  if (a.learning_priority?.score !== undefined) {
    conclusions.push(
      t("hypothesis:hreport.conclLearning", { score: pct(a.learning_priority.score) }),
    );
  }
  blocks.push({ kind: "list", items: conclusions, ordered: true });
  blocks.push({
    kind: "paragraph",
    text: t("hypothesis:hreport.recommendationLead", { rec: recommendation(h) }),
    bold: true,
  });
  if (targetEffect(h, kpi)) {
    blocks.push({
      kind: "paragraph",
      text: t("hypothesis:hreport.effectLine", { effect: targetEffect(h, kpi) }),
    });
  }

  // ---- СПИСОК ИСПОЛЬЗОВАННЫХ ИСТОЧНИКОВ ----
  if (citations.entries.length > 0) {
    blocks.push({ kind: "heading", text: t("hypothesis:hreport.referencesTitle"), caps: true });
    blocks.push({ kind: "references", items: citations.entries });
  }

  // ---- Приложение А: сведения о формировании ----
  const genRows = [
    { label: t("hypothesis:hreport.g.kind"), value: generationKindLabel(gen) },
    {
      label: t("hypothesis:hreport.g.model"),
      value: [gen.model, gen.promptVersion].filter(Boolean).join(", "),
    },
    {
      label: t("hypothesis:hreport.g.mediators"),
      value: gen.mediators.length > 0 ? String(gen.mediators.length) : "",
    },
    {
      label: t("hypothesis:hreport.g.scores"),
      value: gen.scores
        ? [
            gen.scores.composite !== undefined ? `composite ${pct(gen.scores.composite)}` : "",
            gen.scores.convergence !== undefined
              ? `convergence ${pct(gen.scores.convergence)}`
              : "",
            gen.scores.affinity !== undefined ? `affinity ${pct(gen.scores.affinity)}` : "",
          ]
            .filter(Boolean)
            .join(" · ")
        : "",
    },
    {
      label: t("hypothesis:hreport.g.noveltyGate"),
      value: gen.gates?.novelty
        ? [
            gen.gates.novelty.topSim !== undefined
              ? `top-sim ${pct(gen.gates.novelty.topSim)}`
              : "",
            gen.gates.novelty.nearestFilename
              ? t("hypothesis:hreport.g.nearest", { file: gen.gates.novelty.nearestFilename })
              : "",
          ]
            .filter(Boolean)
            .join(" · ")
        : "",
    },
    { label: t("hypothesis:hreport.g.fallback"), value: gen.fallbackReason ?? "" },
  ].filter((r) => r.value !== "");
  const recentRevisions = revisions.slice(0, REVISIONS_MAX);
  if (genRows.length > 0 || recentRevisions.length > 0) {
    blocks.push({ kind: "heading", text: t("hypothesis:hreport.appendixTitle"), caps: true });
    if (genRows.length > 0) blocks.push({ kind: "kv", rows: genRows });
    if (recentRevisions.length > 0) {
      blocks.push({
        kind: "table",
        title: t("hypothesis:hreport.historyTableTitle"),
        head: [
          t("hypothesis:hreport.h.no"),
          t("hypothesis:hreport.h.date"),
          t("hypothesis:hreport.h.action"),
          t("hypothesis:hreport.h.comment"),
        ],
        rows: recentRevisions.map((r) => [
          String(r.revision_no),
          formatDateTime(r.created_at),
          r.action,
          r.summary,
        ]),
      });
    }
    blocks.push({
      kind: "paragraph",
      text: t("hypothesis:hreport.systemLink", { path: `/hypotheses/${h.id}` }),
      muted: true,
    });
  }

  return {
    fileName: t("hypothesis:hreport.fileName", { id: id8 }),
    title,
    blocks,
  };
}
