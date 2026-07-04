import { describe, expect, test } from "bun:test";

import { pageItems } from "./Pagination";

describe("pageItems", () => {
  test("до 7 страниц — все номера подряд", () => {
    expect(pageItems(2, 5)).toEqual([1, 2, 3, 4, 5]);
    expect(pageItems(1, 1)).toEqual([1]);
  });

  test("середина — края и соседи с разрывами", () => {
    expect(pageItems(6, 12)).toEqual([1, "gap", 5, 6, 7, "gap", 12]);
  });

  test("края без лишних разрывов", () => {
    expect(pageItems(1, 12)).toEqual([1, 2, "gap", 12]);
    expect(pageItems(12, 12)).toEqual([1, "gap", 11, 12]);
  });

  test("зазор в одну страницу схлопывается в номер, а не в многоточие", () => {
    expect(pageItems(4, 12)).toEqual([1, 2, 3, 4, 5, "gap", 12]);
  });
});
