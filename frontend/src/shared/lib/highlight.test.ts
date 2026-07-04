import { describe, expect, test } from "bun:test";

import { highlightParts } from "./highlight";

const hits = (text: string, query: string) =>
  highlightParts(text, query)
    .filter((p) => p.hit)
    .map((p) => p.part);

describe("highlightParts", () => {
  test("пустой запрос — один фрагмент без подсветки", () => {
    expect(highlightParts("текст", "")).toEqual([{ part: "текст", hit: false }]);
  });

  test("короткие токены (<3 символов) не подсвечиваются", () => {
    expect(hits("он и она", "он и")).toEqual([]);
  });

  test("регистр и ё/е не влияют", () => {
    expect(hits("Тёмная материя", "темная")).toEqual(["Тёмная"]);
  });

  test("иероглифы подсвечиваются без порога длины", () => {
    expect(hits("量子计算的进展", "量子")).toEqual(["量子"]);
  });

  test("спецсимволы запроса экранируются", () => {
    expect(() => highlightParts("f(x) = x2", "f(x)")).not.toThrow();
  });

  test("части восстанавливают исходный текст", () => {
    const text = "Поиск по данным и данные о поиске";
    const joined = highlightParts(text, "данные поиск")
      .map((p) => p.part)
      .join("");
    expect(joined).toBe(text);
  });
});
