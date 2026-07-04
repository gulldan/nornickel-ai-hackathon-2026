import { beforeAll, describe, expect, test } from "bun:test";

import { initI18n } from "@/shared/i18n";

import { formatDate, humanSize, pluralRu } from "./format";

beforeAll(() => {
  // В bun test нет DOM; initI18n выставляет document.documentElement.lang.
  (globalThis as { document?: unknown }).document ??= { documentElement: { lang: "" } };
  initI18n();
});

describe("humanSize", () => {
  test("байты и переходы единиц", () => {
    expect(humanSize(0)).toBe("0 Б");
    expect(humanSize(1093)).toBe("1.1 КБ");
    expect(humanSize(5 * 1024 * 1024)).toBe("5.0 МБ");
    expect(humanSize(120 * 1024)).toBe("120 КБ"); // >=100 — без дробной части
  });

  test("мусор на входе — прочерк", () => {
    expect(humanSize(-1)).toBe("—");
    expect(humanSize(Number.NaN)).toBe("—");
  });
});

describe("formatDate", () => {
  test("пустая или битая строка — прочерк", () => {
    expect(formatDate("")).toBe("—");
    expect(formatDate("не дата")).toBe("—");
  });

  test("валидная дата локализуется", () => {
    expect(formatDate("2026-05-28T10:00:00Z")).toContain("2026");
  });
});

describe("pluralRu", () => {
  const forms: [string, string, string] = ["документ", "документа", "документов"];
  test.each([
    [1, "документ"],
    [2, "документа"],
    [5, "документов"],
    [11, "документов"],
    [21, "документ"],
    [104, "документа"],
  ])("%i → %s", (n, expected) => {
    expect(pluralRu(n, forms)).toBe(expected);
  });
});
