import { test, expect, type Page } from "@playwright/test";
import { expectEnglish } from "./english";

async function openSettings(page: Page, settings: string, openNav: string) {
  const button = page.getByRole("button", { name: settings, exact: true });
  if (!(await button.isVisible()))
    await page.getByRole("button", { name: openNav, exact: true }).click();
  await button.click();
}

test("language switches in Settings, persists across reloads and can be reverted", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator("html")).toHaveAttribute("lang", "es");
  await expect(
    page.getByRole("heading", { name: "Aplicaciones", exact: true }),
  ).toBeVisible();

  await openSettings(page, "Ajustes", "Abrir navegación");
  await page.getByRole("tab", { name: "Idioma", exact: true }).click();
  await page.getByRole("button", { name: "English", exact: true }).click();
  // The page behind the dialog re-renders without a reload.
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(
    page.getByRole("heading", { name: "Settings", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("heading", { name: "Applications", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Activity", exact: true, includeHidden: true }),
  ).toBeAttached();

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(
    page.getByRole("heading", { name: "Applications", exact: true }),
  ).toBeVisible();

  await openSettings(page, "Settings", "Open navigation");
  await page.getByRole("tab", { name: "Language", exact: true }).click();
  await page.getByRole("button", { name: "Español", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("lang", "es");
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("link", { name: "Actividad", exact: true, includeHidden: true }),
  ).toBeAttached();
});

test("a failed simulated deployment shows English error and stage text", async ({
  page,
}) => {
  await page.addInitScript(() => localStorage.setItem("darsena.locale", "en"));
  await page.goto("/apps/new");
  await page.getByLabel("Repository", { exact: true }).fill("demo/example");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page
    .getByLabel("Application name", { exact: true })
    .fill(`app-${Date.now().toString(36)}`);
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.getByRole("button", { name: "Build failure", exact: true }).click();
  await page
    .getByRole("button", { name: "Simulate deployment", exact: true })
    .click();
  const alert = page.getByRole("alert").filter({ hasText: "Failed" });
  await expect(alert).toContainText(
    "The simulated build failed. Check the Dockerfile and try again.",
  );
  const stages = page.locator(".stage-list");
  await expect(stages).toContainText("Build image");
  await expect(stages).toContainText("Validate configuration");
  await expect(stages).not.toContainText("Construir imagen");
  await expect(alert).not.toContainText("La construcción");
});

test("English mode leaves no Spanish text in the main screens", async ({
  page,
  isMobile,
}) => {
  await page.addInitScript(() => localStorage.setItem("darsena.locale", "en"));
  await page.goto("/apps/new");
  await expectEnglish(page);
  await page.getByLabel("Repository", { exact: true }).fill("demo/example");
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page
    .getByLabel("Application name", { exact: true })
    .fill(`app-${Date.now().toString(36)}`);
  await expectEnglish(page);
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.getByRole("button", { name: "Build failure", exact: true }).click();
  await expectEnglish(page);
  await page.getByRole("button", { name: "Simulate deployment", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "Failed" })).toBeVisible();
  await expectEnglish(page);
  const operationUrl = page.url();
  const base = operationUrl.replace(/\/deployments\/.*$/, "");

  await page.goto(base);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expectEnglish(page);
  await page.goto(`${base}/deployments`);
  await expect(page.getByRole("heading", { name: "Deployment history" })).toBeVisible();
  await expectEnglish(page);

  await page.goto("/");
  await expect(page.locator("a.app-row").first()).toBeVisible();
  await expectEnglish(page);
  await page.goto("/activity");
  await expectEnglish(page);
  await page.goto("/diagnostics");
  await expect(page.getByRole("heading", { name: "Diagnostics", level: 1 })).toBeVisible();
  await expectEnglish(page);

  await page.goto("/");
  if (isMobile) await page.getByRole("button", { name: "Open navigation", exact: true }).click();
  await page.getByRole("button", { name: /Search or go to/ }).click();
  await expect(page.getByRole("option").first()).toBeVisible();
  await expectEnglish(page);
});
