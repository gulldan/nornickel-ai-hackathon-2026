import { afterEach, beforeEach, describe, expect, setSystemTime, test } from "bun:test";

import { invalidateClusters, listClusters } from "./api";

const originalFetch = globalThis.fetch;

let calls = 0;
let failNext = false;

describe("listClusters cache", () => {
  beforeEach(() => {
    calls = 0;
    failNext = false;
    invalidateClusters();
    globalThis.fetch = (async () => {
      calls++;
      if (failNext) {
        failNext = false;
        return new Response(JSON.stringify({ error: "boom" }), { status: 500 });
      }
      return new Response(JSON.stringify([{ id: `c-${calls}` }]));
    }) as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    setSystemTime();
  });

  test("параллельные и повторные вызовы в пределах TTL делят один запрос", async () => {
    const [a, b] = await Promise.all([listClusters(), listClusters()]);
    expect(a).toBe(b);
    expect(await listClusters()).toBe(a);
    expect(calls).toBe(1);
  });

  test("ошибка не кэшируется — следующий вызов перезапрашивает", async () => {
    failNext = true;
    await expect(listClusters()).rejects.toThrow("boom");
    const next = await listClusters();
    expect(next[0]?.id).toBe("c-2");
    expect(calls).toBe(2);
  });

  test("invalidateClusters сбрасывает кэш", async () => {
    await listClusters();
    invalidateClusters();
    await listClusters();
    expect(calls).toBe(2);
  });

  test("после истечения TTL список перезапрашивается", async () => {
    const start = new Date("2026-01-01T00:00:00Z");
    setSystemTime(start);
    await listClusters();
    setSystemTime(new Date(start.getTime() + 119_000));
    await listClusters();
    expect(calls).toBe(1);
    setSystemTime(new Date(start.getTime() + 121_000));
    await listClusters();
    expect(calls).toBe(2);
  });
});
