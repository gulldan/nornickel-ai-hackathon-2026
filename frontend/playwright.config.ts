import { defineConfig } from "@playwright/test";

// E2E/a11y гоняются против живого стенда (нужен работающий бэкенд):
//   E2E_BASE_URL=http://localhost:5173 E2E_PASS=... bun run a11y
// Бюджет всего прогона — до минуты: логин один в global-setup (storageState),
// тесты только грузят страницу и запускают axe.
export default defineConfig({
  testDir: "tests/e2e",
  globalSetup: "./tests/e2e/global-setup.ts",
  timeout: 30_000,
  retries: 0,
  workers: 4,
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:5173",
    storageState: "test-results/.auth-state.json",
    viewport: { width: 1440, height: 1000 },
  },
});
