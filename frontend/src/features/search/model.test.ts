import { describe, expect, test } from "bun:test";

import type { ApiSource } from "@/features/search/api";

import { buildSmartAnswer } from "./model";

const src = (n: number, over: Partial<ApiSource> = {}): ApiSource => ({
  document_id: `doc-${n}`,
  filename: `paper-${n}.pdf`,
  chunk_id: `chunk-${n}`,
  snippet: `фрагмент ${n}`,
  score: 1 - n / 10,
  ...over,
});

describe("buildSmartAnswer", () => {
  test("пустой текст — null", () => {
    expect(buildSmartAnswer("   ", [src(1)])).toBeNull();
  });

  test("маркер [S1] становится сноской с номером карточки", () => {
    const a = buildSmartAnswer("Вывод [S1].", [src(1)]);
    expect(a?.markdown).toBe("Вывод[1](#cite-0).");
    expect(a?.chips).toEqual([{ label: 1, docId: "doc-1", fragmentId: "chunk-1" }]);
    expect(a?.plain).toBe("Вывод.");
  });

  test("пачка подряд идущих маркеров дедуплицируется", () => {
    const a = buildSmartAnswer("Итог [S1][S1, S2].", [src(1), src(2)]);
    expect(a?.chips.map((c) => c.label)).toEqual([1, 2]);
  });

  test("маркер без источника просто убирается", () => {
    const a = buildSmartAnswer("Тезис [S9].", [src(1)]);
    expect(a?.chips).toEqual([]);
    expect(a?.markdown).toBe("Тезис.");
  });

  test("цитаты одного документа получают один номер, но разные фрагменты", () => {
    const a = buildSmartAnswer("Раз [S1]. Два [S2].", [
      src(1),
      src(2, { document_id: "doc-1", chunk_id: "chunk-1b" }),
    ]);
    expect(a?.chips.map((c) => c.label)).toEqual([1, 1]);
    expect(a?.chips.map((c) => c.fragmentId)).toEqual(["chunk-1", "chunk-1b"]);
    expect(a?.sources).toHaveLength(1);
  });
});
