// Генератор бизнес-отчёта в Word (.docx): те же разделы, что и печатная
// страница отчёта. Модуль тяжёлый (библиотека docx) — подключать только
// динамическим import() из обработчика клика.
import {
  AlignmentType,
  BorderStyle,
  Document,
  ExternalHyperlink,
  Packer,
  Paragraph,
  ShadingType,
  Table,
  TableCell,
  TableRow,
  TextRun,
  WidthType,
} from "docx";

import { type ApiHypothesis } from "@/features/hypothesis/api";
import { verdictMeta } from "@/features/hypothesis/board/model";
import {
  displayHypothesisTitle,
  downloadFile,
  pct,
  priorityScore,
  statusMeta,
  successCriteriaLines,
} from "@/features/hypothesis/model";
import { i18n } from "@/shared/i18n";
import { formatDate } from "@/shared/lib/format";

import {
  itcBandLine,
  itcComponents,
  kpiTargetLabel,
  loadReportData,
  type PilotProvenance,
  type ReportData,
} from "./reportData";

const INK = "111827";
const MUTED = "6B7280";
const BRAND = "203BC1";
const BORDER = "E5E7EB";
const HEAD_WASH = "F3F4F6";

function para(
  text: string,
  o: {
    size?: number;
    bold?: boolean;
    color?: string;
    caps?: boolean;
    before?: number;
    after?: number;
    align?: (typeof AlignmentType)[keyof typeof AlignmentType];
    bullet?: boolean;
  } = {},
): Paragraph {
  return new Paragraph({
    spacing: { before: o.before ?? 0, after: o.after ?? 80 },
    alignment: o.align,
    bullet: o.bullet ? { level: 0 } : undefined,
    children: [
      new TextRun({
        text,
        size: o.size ?? 21,
        bold: o.bold,
        color: o.color ?? INK,
        allCaps: o.caps,
      }),
    ],
  });
}

function heading(text: string): Paragraph {
  return para(text, { size: 28, bold: true, before: 360, after: 140 });
}

const thinBorder = { style: BorderStyle.SINGLE, size: 4, color: BORDER } as const;

function cell(children: Paragraph[], o: { head?: boolean; width?: number } = {}): TableCell {
  return new TableCell({
    children,
    width: o.width === undefined ? undefined : { size: o.width, type: WidthType.PERCENTAGE },
    shading: o.head ? { type: ShadingType.CLEAR, fill: HEAD_WASH } : undefined,
    margins: { top: 90, bottom: 90, left: 110, right: 110 },
  });
}

function textCell(
  text: string,
  o: { head?: boolean; width?: number; right?: boolean; bold?: boolean; muted?: boolean } = {},
): TableCell {
  return cell(
    [
      para(text, {
        size: o.head ? 17 : 20,
        bold: o.head || o.bold,
        color: o.muted || o.head ? MUTED : INK,
        caps: o.head,
        after: 0,
        align: o.right ? AlignmentType.RIGHT : undefined,
      }),
    ],
    o,
  );
}

function table(rows: TableRow[]): Table {
  return new Table({
    width: { size: 100, type: WidthType.PERCENTAGE },
    borders: {
      top: thinBorder,
      bottom: thinBorder,
      left: thinBorder,
      right: thinBorder,
      insideHorizontal: thinBorder,
      insideVertical: thinBorder,
    },
    rows,
  });
}

function scoreText(h: ApiHypothesis): string {
  const p = priorityScore(h);
  return p === null ? "—" : String(p);
}

function pilotBlock(
  h: ApiHypothesis,
  index: string,
  prov: PilotProvenance | undefined,
): Paragraph[] {
  const out: Paragraph[] = [
    new Paragraph({
      spacing: { before: 200, after: 80 },
      children: [
        new TextRun({ text: `${index}  `, size: 21, color: MUTED }),
        new TextRun({ text: displayHypothesisTitle(h), size: 21, bold: true, color: INK }),
      ],
    }),
    para(h.statement, { after: 60 }),
  ];
  if (h.rationale !== "") {
    out.push(
      new Paragraph({
        spacing: { after: 60 },
        children: [
          new TextRun({
            text: `${i18n.t("hypothesis:report.pilotRationale")}. `,
            size: 21,
            bold: true,
            color: INK,
          }),
          new TextRun({ text: h.rationale, size: 21, color: INK }),
        ],
      }),
    );
  }
  if (prov?.constraints) {
    out.push(
      para(`${i18n.t("hypothesis:report.pilotConstraints")}: ${prov.constraints}`, {
        size: 18,
        color: MUTED,
      }),
    );
  }
  const criteria = successCriteriaLines(h.detail?.experiment_plan?.success_criteria);
  if (criteria.length > 0) {
    out.push(
      para(`${i18n.t("hypothesis:report.pilotCriteria")}: ${criteria.join("; ")}`, {
        size: 18,
        color: MUTED,
      }),
    );
  }
  if (prov !== undefined && prov.works.length > 0) {
    out.push(para(i18n.t("hypothesis:report.pilotSources"), { size: 18, bold: true, after: 40 }));
    for (const w of prov.works) {
      const tail = `${w.venue ? `, ${w.venue}` : ""}${w.year ? ` (${w.year})` : ""}`;
      const runs: (TextRun | ExternalHyperlink)[] = w.doi
        ? [
            new ExternalHyperlink({
              link: `https://doi.org/${w.doi}`,
              children: [new TextRun({ text: w.title, size: 18, color: BRAND, underline: {} })],
            }),
            new TextRun({ text: `${tail} — doi:${w.doi}`, size: 18, color: MUTED }),
          ]
        : [new TextRun({ text: `${w.title}${tail}`, size: 18, color: MUTED })];
      out.push(new Paragraph({ bullet: { level: 0 }, spacing: { after: 30 }, children: runs }));
    }
  }
  return out;
}

function buildChildren(data: ReportData): (Paragraph | Table)[] {
  const today = formatDate(new Date().toISOString());
  const children: (Paragraph | Table)[] = [
    para(i18n.t("hypothesis:report.kicker", { date: today }), {
      size: 16,
      color: BRAND,
      caps: true,
      after: 120,
    }),
    para(i18n.t("hypothesis:report.title"), { size: 44, bold: true, after: 120 }),
    para(
      i18n.t("hypothesis:report.summary", {
        total: data.ranked.length,
        approved: data.approvedTotal,
        goals: data.kpis.length,
      }),
      { size: 20, color: MUTED, after: 160 },
    ),
  ];

  let n = 0;
  if (data.execThemes.length > 0) {
    children.push(
      heading(`${++n}. ${i18n.t("hypothesis:report.execTitle")}`),
      para(i18n.t("hypothesis:report.execNote"), { size: 18, color: MUTED, after: 120 }),
    );
    for (const { cluster, itc, linked } of data.execThemes) {
      children.push(
        new Paragraph({
          bullet: { level: 0 },
          spacing: { after: 20 },
          children: [
            new TextRun({ text: cluster.label, size: 21, bold: true, color: INK }),
            new TextRun({
              text: ` — ${i18n.t("hypothesis:report.execScore", { score: itc.score })}${itc.band?.label ? ` · ${itc.band.label}` : ""}`,
              size: 21,
              color: MUTED,
            }),
          ],
        }),
      );
      const detail = cluster.summary || cluster.keywords.slice(0, 6).join(", ");
      if (detail) {
        children.push(para(detail, { size: 18, color: MUTED, after: 20 }));
      }
      children.push(
        para(i18n.t("hypothesis:report.execCounts", { docs: cluster.document_count, linked }), {
          size: 18,
          color: MUTED,
          after: 100,
        }),
      );
    }
  }

  if (data.kpiRows.length > 0) {
    children.push(heading(`${++n}. ${i18n.t("hypothesis:report.goalsTitle")}`));
    const rows = [
      new TableRow({
        tableHeader: true,
        children: [
          textCell(i18n.t("hypothesis:report.goalsHead.goal"), { head: true, width: 38 }),
          textCell(i18n.t("hypothesis:report.goalsHead.target"), { head: true, width: 22 }),
          textCell(i18n.t("hypothesis:report.goalsHead.hypotheses"), {
            head: true,
            width: 13,
            right: true,
          }),
          textCell(i18n.t("hypothesis:report.goalsHead.approved"), {
            head: true,
            width: 14,
            right: true,
          }),
          textCell(i18n.t("hypothesis:report.goalsHead.best"), {
            head: true,
            width: 13,
            right: true,
          }),
        ],
      }),
      ...data.kpiRows.map(
        ({ kpi, total, approved, best }) =>
          new TableRow({
            children: [
              cell(
                [
                  para(kpi.title, { size: 20, bold: true, after: 0 }),
                  ...(kpi.metric !== ""
                    ? [para(kpi.metric, { size: 17, color: MUTED, after: 0 })]
                    : []),
                ],
                { width: 38 },
              ),
              textCell(kpiTargetLabel(kpi), { width: 22 }),
              textCell(String(total), { width: 13, right: true }),
              textCell(String(approved), { width: 14, right: true }),
              textCell(best === null ? "—" : `${best}/100`, { width: 13, right: true }),
            ],
          }),
      ),
    ];
    children.push(table(rows));
  }

  children.push(
    heading(`${++n}. ${i18n.t("hypothesis:report.rankingTitle")}`),
    para(i18n.t("hypothesis:report.rankingNote"), { size: 18, color: MUTED, after: 120 }),
  );
  const rankRows = [
    new TableRow({
      tableHeader: true,
      children: [
        textCell(i18n.t("hypothesis:report.rankHead.num"), { head: true, width: 5 }),
        textCell(i18n.t("hypothesis:report.rankHead.hypothesis"), { head: true, width: 41 }),
        textCell(i18n.t("hypothesis:report.rankHead.priority"), {
          head: true,
          width: 11,
          right: true,
        }),
        textCell(i18n.t("hypothesis:report.rankHead.novelty"), {
          head: true,
          width: 9,
          right: true,
        }),
        textCell(i18n.t("hypothesis:report.rankHead.value"), { head: true, width: 9, right: true }),
        textCell(i18n.t("hypothesis:report.rankHead.risk"), { head: true, width: 8, right: true }),
        textCell(i18n.t("hypothesis:report.rankHead.trl"), { head: true, width: 7, right: true }),
        textCell(i18n.t("hypothesis:report.rankHead.status"), { head: true, width: 10 }),
      ],
    }),
    ...data.shown.map((h, i) => {
      const verdict = verdictMeta(h);
      return new TableRow({
        children: [
          textCell(String(i + 1), { width: 5, muted: true }),
          cell(
            [
              para(displayHypothesisTitle(h), { size: 20, after: 0 }),
              ...(verdict ? [para(verdict.label, { size: 16, color: MUTED, after: 0 })] : []),
            ],
            { width: 41 },
          ),
          textCell(scoreText(h), { width: 11, right: true, bold: true }),
          textCell(pct(h.novelty_score), { width: 9, right: true }),
          textCell(pct(h.value_score), { width: 9, right: true }),
          textCell(pct(h.risk_score), { width: 8, right: true }),
          textCell(h.trl === null ? "—" : String(h.trl), { width: 7, right: true }),
          textCell(statusMeta(h.status).label, { width: 10 }),
        ],
      });
    }),
  ];
  children.push(table(rankRows));
  if (data.ranked.length > data.shown.length) {
    children.push(
      para(
        i18n.t("hypothesis:report.truncated", {
          shown: data.shown.length,
          total: data.ranked.length,
        }),
        {
          size: 17,
          color: MUTED,
          before: 80,
        },
      ),
    );
  }

  if (data.passports.length > 0) {
    children.push(heading(`${++n}. ${i18n.t("hypothesis:report.passportsTitle")}`));
    for (const { cluster, itc, linked } of data.passports) {
      children.push(
        new Paragraph({
          spacing: { before: 200, after: 40 },
          children: [
            new TextRun({ text: cluster.label, size: 22, bold: true, color: INK }),
            new TextRun({
              text: `   ${i18n.t("hypothesis:report.passportScore", { score: itc.score })}`,
              size: 20,
              bold: true,
              color: BRAND,
            }),
          ],
        }),
      );
      const band = itcBandLine(itc);
      if (band) children.push(para(band, { size: 18, color: MUTED, after: 40 }));
      if (cluster.summary) children.push(para(cluster.summary, { size: 20, after: 60 }));
      for (const c of itcComponents(itc)) {
        children.push(
          para(`${c.name} (${Math.round(c.norm * 100)})${c.note ? `: ${c.note}` : ""}`, {
            size: 18,
            color: MUTED,
            after: 20,
            bullet: true,
          }),
        );
      }
      children.push(
        para(
          i18n.t("hypothesis:report.passportSignals", {
            pubs: itc.signals?.pub_count ?? cluster.document_count,
            orgs: itc.signals?.org_count ?? 0,
            linked,
          }),
          { size: 18, color: MUTED, after: 80 },
        ),
      );
    }
  }

  if (data.pilots.length > 0) {
    children.push(
      heading(`${++n}. ${i18n.t("hypothesis:report.pilotsTitle")}`),
      para(i18n.t("hypothesis:report.pilotsNote"), { size: 18, color: MUTED, after: 60 }),
    );
    data.pilots.forEach((h, i) => {
      children.push(...pilotBlock(h, `${n}.${i + 1}.`, data.provenance[h.id]));
    });
  }

  children.push(para(i18n.t("hypothesis:report.footer"), { size: 16, color: MUTED, before: 360 }));
  return children;
}

export async function downloadReportDocx(): Promise<void> {
  const data = await loadReportData();
  const doc = new Document({
    creator: "Hypothesis Factory",
    title: i18n.t("hypothesis:report.title"),
    styles: {
      default: { document: { run: { font: "Calibri", size: 21, color: INK } } },
    },
    sections: [
      {
        properties: {
          page: { margin: { top: 794, bottom: 794, left: 794, right: 794 } },
        },
        children: buildChildren(data),
      },
    ],
  });
  const blob = await Packer.toBlob(doc);
  downloadFile(i18n.t("hypothesis:report.docxFileName"), blob);
}
