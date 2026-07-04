import { request, type FullConfig } from "@playwright/test";

// Логин один на весь прогон: кладём rag.auth в storageState, воркеры его
// переиспользуют. На логине два рубежа против брутфорса (nginx 30 r/m,
// локаут auth-сервиса) — при 429 ждём Retry-After.
export default async function globalSetup(config: FullConfig) {
  const { baseURL, storageState } = config.projects[0]!.use;
  const username = process.env.E2E_USER ?? "admin";
  const password = process.env.E2E_PASS ?? process.env.ADMIN_PASSWORD;
  if (!password) {
    throw new Error("Set E2E_PASS or ADMIN_PASSWORD before running Playwright e2e tests");
  }

  const ctx = await request.newContext({ baseURL });
  let auth: unknown;
  for (let attempt = 0; auth === undefined; attempt++) {
    const resp = await ctx.post("/api/v1/auth/login", {
      data: {
        username,
        password,
      },
    });
    if (resp.status() === 429 && attempt < 3) {
      const retryAfter = Number(resp.headers()["retry-after"]) || 25;
      await new Promise((r) => setTimeout(r, (retryAfter + 2) * 1000));
      continue;
    }
    if (!resp.ok()) throw new Error(`логин не прошёл: ${resp.status()}`);
    auth = await resp.json();
  }
  await ctx.dispose();

  const fs = await import("node:fs");
  const path = await import("node:path");
  const state = {
    cookies: [],
    origins: [
      {
        origin: new URL(baseURL!).origin,
        localStorage: [{ name: "rag.auth", value: JSON.stringify(auth) }],
      },
    ],
  };
  fs.mkdirSync(path.dirname(String(storageState)), { recursive: true });
  fs.writeFileSync(String(storageState), JSON.stringify(state));
}
