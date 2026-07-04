import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

// Аудит доступности живого стенда (axe-core, WCAG 2.0 A/AA). Авторизация —
// через storageState из global-setup, тест только грузит страницу и меряет.
const PAGES: { name: string; path: string }[] = [
  { name: "поиск (главная)", path: "/" },
  { name: "гипотезы", path: "/hypotheses" },
  { name: "темы", path: "/summary" },
  { name: "цели", path: "/kpi" },
  { name: "история", path: "/history" },
  { name: "документы", path: "/operator/documents" },
  { name: "обзор", path: "/admin" },
  { name: "метрики", path: "/admin/metrics" },
  { name: "настройки", path: "/admin/settings" },
];

test.describe("@a11y аудит страниц", () => {
  for (const { name, path } of PAGES) {
    test(`@a11y ${name}`, async ({ page }) => {
      await page.goto(path, { waitUntil: "load" });
      // Дождаться каркаса страницы + короткий зазор на данные и анимации
      // (networkidle не наступает: фон поллит /system/activity).
      await page.locator("main, [role=main]").first().waitFor({ timeout: 15_000 });
      await page.waitForTimeout(700);
      const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
      const serious = results.violations.filter(
        (v) => v.impact === "serious" || v.impact === "critical",
      );
      expect(serious.map((v) => `${v.id}: ${v.help} (${v.nodes.length} узлов)`)).toEqual([]);
    });
  }
});
