// Подсветка совпадений запроса в тексте: чистые текст-утилиты, общие для
// поиска и предпросмотра документов (в shared — чтобы фичи не зацикливались).

function normalize(text: string): string {
  return text.toLowerCase().replace(/ё/g, "е");
}

const CJK = /\p{Script=Han}/u;

function tokenize(query: string): string[] {
  // Иероглифы — токены без порога длины: китайское «слово» — 1–2 знака.
  return normalize(query)
    .split(/[^\p{L}\p{N}]+/u)
    .filter((t) => t.length >= 3 || CJK.test(t));
}

/** Highlight matches in text. Returns text parts with a highlight flag. */
export function highlightParts(text: string, query: string): { part: string; hit: boolean }[] {
  const tokens = tokenize(query);
  if (tokens.length === 0) return [{ part: text, hit: false }];
  // «е» в токене покрывает и «ё» в тексте (normalize сводит ё→е).
  const pattern = new RegExp(
    `(${tokens.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/е/g, "[её]")).join("|")})`,
    "gi",
  );
  return text
    .split(pattern)
    .filter((p) => p !== "")
    .map((part) => ({
      part,
      hit: tokens.some((t) => normalize(part).includes(t)),
    }));
}
