// Word-рендерер отчёта по гипотезе (блоки из hypothesisReport.ts) в оформлении
// «по мотивам ГОСТ 7.32»: Times New Roman 12 pt, полуторный интервал, поля
// 30/15/20/20 мм, номер страницы внизу по центру (кроме первой). Модуль
// тяжёлый (библиотека docx) — подключать только динамическим import().
import {
  AlignmentType,
  BorderStyle,
  Document,
  ExternalHyperlink,
  Footer,
  Packer,
  PageNumber,
  Paragraph,
  ShadingType,
  Table,
  TableCell,
  TableRow,
  TextRun,
  WidthType,
} from "docx";

import { downloadFile } from "@/features/hypothesis/model";
import { i18n } from "@/shared/i18n";

import type { HypothesisReportDoc, ReportBlock } from "./hypothesisReport";

const INK = "111827";
const MUTED = "6B7280";
const BORDER = "D1D5DB";
const HEAD_WASH = "F3F4F6";
const FONT = "Times New Roman";
// Полуторный интервал: 240 twips = одинарный.
const LINE_15 = 360;
// Абзацный отступ 1,25 см.
const INDENT = 709;

const thinBorder = { style: BorderStyle.SINGLE, size: 4, color: BORDER } as const;

function run(
  text: string,
  o: { size?: number; bold?: boolean; italic?: boolean; color?: string; caps?: boolean } = {},
): TextRun {
  return new TextRun({
    text,
    font: FONT,
    size: o.size ?? 24,
    bold: o.bold,
    italics: o.italic,
    color: o.color ?? INK,
    allCaps: o.caps,
  });
}

function paragraphBlock(b: Extract<ReportBlock, { kind: "paragraph" }>): Paragraph {
  return new Paragraph({
    alignment: b.muted ? AlignmentType.LEFT : AlignmentType.JUSTIFIED,
    indent: b.muted ? undefined : { firstLine: INDENT },
    spacing: { line: LINE_15, after: 120 },
    children: [
      run(b.text, {
        size: b.muted ? 20 : 24,
        bold: b.bold,
        italic: b.italic,
        color: b.muted ? MUTED : INK,
      }),
    ],
  });
}

function tableCaption(n: number, title: string): Paragraph {
  return new Paragraph({
    spacing: { before: 200, after: 80, line: LINE_15 },
    children: [run(i18n.t("hypothesis:hreport.tableCaption", { n, title }), { size: 22 })],
  });
}

function cell(text: string, o: { head?: boolean } = {}): TableCell {
  return new TableCell({
    shading: o.head ? { type: ShadingType.CLEAR, fill: HEAD_WASH } : undefined,
    margins: { top: 60, bottom: 60, left: 100, right: 100 },
    children: [
      new Paragraph({
        spacing: { after: 0 },
        children: [run(text, { size: 20, bold: o.head })],
      }),
    ],
  });
}

function dataTable(head: string[], rows: string[][]): Table {
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
    rows: [
      new TableRow({
        tableHeader: true,
        cantSplit: true,
        children: head.map((hd) => cell(hd, { head: true })),
      }),
      ...rows.map((r) => new TableRow({ cantSplit: true, children: r.map((c) => cell(c)) })),
    ],
  });
}

function kvTable(rows: { label: string; value: string }[]): Table {
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
    rows: rows.map(
      (r) =>
        new TableRow({
          cantSplit: true,
          children: [
            new TableCell({
              width: { size: 32, type: WidthType.PERCENTAGE },
              shading: { type: ShadingType.CLEAR, fill: HEAD_WASH },
              margins: { top: 60, bottom: 60, left: 100, right: 100 },
              children: [
                new Paragraph({
                  spacing: { after: 0 },
                  children: [run(r.label, { size: 20, bold: true })],
                }),
              ],
            }),
            new TableCell({
              width: { size: 68, type: WidthType.PERCENTAGE },
              margins: { top: 60, bottom: 60, left: 100, right: 100 },
              children: [
                new Paragraph({ spacing: { after: 0 }, children: [run(r.value, { size: 20 })] }),
              ],
            }),
          ],
        }),
    ),
  });
}

function doctitleBlocks(b: Extract<ReportBlock, { kind: "doctitle" }>): Paragraph[] {
  const out: Paragraph[] = [];
  if (b.org) {
    out.push(
      new Paragraph({
        alignment: AlignmentType.CENTER,
        spacing: { after: 160 },
        children: [run(b.org, { size: 22 })],
      }),
    );
  }
  out.push(
    new Paragraph({
      alignment: AlignmentType.CENTER,
      spacing: { after: 200 },
      children: [run(b.doctype, { size: 24, bold: true, caps: true })],
    }),
    new Paragraph({
      alignment: AlignmentType.CENTER,
      spacing: { after: 160 },
      children: [run(b.title, { size: 32, bold: true })],
    }),
    new Paragraph({
      alignment: AlignmentType.CENTER,
      spacing: { after: 160 },
      children: [run(b.meta.join(" · "), { size: 20, color: MUTED })],
    }),
  );
  for (const line of b.lines) {
    out.push(
      new Paragraph({
        alignment: AlignmentType.CENTER,
        spacing: { after: 60 },
        children: [run(line, { size: 20, color: MUTED })],
      }),
    );
  }
  out.push(
    new Paragraph({
      spacing: { after: 240 },
      border: { bottom: { style: BorderStyle.SINGLE, size: 6, color: BORDER } },
      children: [],
    }),
  );
  return out;
}

function renderBlocks(doc: HypothesisReportDoc): (Paragraph | Table)[] {
  const out: (Paragraph | Table)[] = [];
  let tableNo = 0;
  for (const b of doc.blocks) {
    switch (b.kind) {
      case "doctitle":
        out.push(...doctitleBlocks(b));
        break;
      case "heading":
        out.push(
          new Paragraph({
            alignment: b.caps ? AlignmentType.CENTER : AlignmentType.LEFT,
            spacing: { before: 360, after: 200, line: LINE_15 },
            children: [run(b.text, { size: 28, bold: true, caps: b.caps })],
          }),
        );
        break;
      case "subheading":
        out.push(
          new Paragraph({
            spacing: { before: 240, after: 120, line: LINE_15 },
            children: [run(b.text, { size: 24, bold: true })],
          }),
        );
        break;
      case "paragraph":
        out.push(paragraphBlock(b));
        break;
      case "kv":
        out.push(kvTable(b.rows));
        break;
      case "table":
        if (b.title) out.push(tableCaption(++tableNo, b.title));
        out.push(dataTable(b.head, b.rows));
        break;
      case "list":
        b.items.forEach((item, i) => {
          out.push(
            new Paragraph({
              spacing: { after: 60, line: LINE_15 },
              indent: { left: INDENT },
              bullet: b.ordered ? undefined : { level: 0 },
              children: [run(b.ordered ? `${i + 1}. ${item}` : item, { size: 22 })],
            }),
          );
        });
        break;
      case "quote":
        out.push(
          new Paragraph({
            spacing: { before: 120, after: 40, line: LINE_15 },
            children: [run(b.source, { size: 20, bold: true })],
          }),
          new Paragraph({
            indent: { left: 567 },
            spacing: { after: 40, line: LINE_15 },
            children: [run(`«${b.text}»`, { size: 22, italic: true })],
          }),
        );
        if (b.note) {
          out.push(
            new Paragraph({
              indent: { left: 567 },
              spacing: { after: 80 },
              children: [run(b.note, { size: 18, color: MUTED })],
            }),
          );
        }
        break;
      case "references":
        for (const item of b.items) {
          const children: (TextRun | ExternalHyperlink)[] = [
            run(`${item.n}. ${item.text}`, { size: 22 }),
          ];
          if (item.url) {
            children.push(
              new ExternalHyperlink({
                link: item.url,
                children: [
                  new TextRun({
                    text: ` ${item.url}`,
                    font: FONT,
                    size: 22,
                    color: "203BC1",
                    underline: {},
                  }),
                ],
              }),
            );
          }
          out.push(
            new Paragraph({
              spacing: { after: 80, line: LINE_15 },
              indent: { left: INDENT, hanging: INDENT },
              children,
            }),
          );
        }
        break;
    }
  }
  return out;
}

export async function downloadHypothesisReportDocx(report: HypothesisReportDoc): Promise<void> {
  const doc = new Document({
    creator: "Hypothesis Factory",
    title: report.title,
    styles: { default: { document: { run: { font: FONT, size: 24, color: INK } } } },
    sections: [
      {
        properties: {
          titlePage: true,
          page: { margin: { top: 1134, bottom: 1134, left: 1701, right: 850 } },
        },
        footers: {
          default: new Footer({
            children: [
              new Paragraph({
                alignment: AlignmentType.CENTER,
                children: [
                  new TextRun({
                    children: [PageNumber.CURRENT],
                    font: FONT,
                    size: 20,
                    color: MUTED,
                  }),
                ],
              }),
            ],
          }),
          first: new Footer({ children: [] }),
        },
        children: renderBlocks(report),
      },
    ],
  });
  const blob = await Packer.toBlob(doc);
  downloadFile(report.fileName, blob);
}
