import { test, expect, type Page } from "@playwright/test";
import { violations } from "./axe";
import { randomUUID } from "node:crypto";

async function startDeployment(page: Page) {
  await page.goto("/apps/new");
  await page.getByLabel("Repositorio", { exact: true }).fill("demo/example");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page
    .getByLabel("Nombre de la aplicación", { exact: true })
    .fill(`app-${randomUUID().slice(0, 8)}`);
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page.getByRole("button", { name: "Simular despliegue", exact: true }).click();
  await expect(page).toHaveURL(/\/apps\/example\/[^/]+\/deployments\/[a-z0-9]+$/);
  return page.url();
}

async function hasFocusIndicator(page: Page, selector: string) {
  await page.keyboard.press("Tab");
  return page.locator(selector).first().evaluate((el) => {
    (el as HTMLElement).focus();
    const s = getComputedStyle(el);
    const outline = s.outlineStyle !== "none" && parseFloat(s.outlineWidth) > 0;
    return outline || (s.boxShadow !== "none" && s.boxShadow !== "");
  });
}

test("axe: applications list, creation and settings", async ({ page, isMobile }) => {
  await startDeployment(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Aplicaciones", level: 1 })).toBeVisible();
  await expect(page.locator("a.app-row").first()).toBeVisible();
  expect(await violations(page)).toEqual([]);

  await page.goto("/apps/new");
  await expect(page.getByLabel("Repositorio", { exact: true })).toBeVisible();
  expect(await violations(page)).toEqual([]);
  await page.getByLabel("Repositorio", { exact: true }).fill("demo/example");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await expect(page.getByLabel("Nombre de la aplicación", { exact: true })).toBeVisible();
  expect(await violations(page)).toEqual([]);

  await page.goto("/");
  if (isMobile) await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
  await page.getByRole("button", { name: "Ajustes", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Ajustes", exact: true })).toBeVisible();
  expect(await violations(page)).toEqual([]);
});

test("axe: example detail tabs and operation detail", async ({ page }) => {
  const operationUrl = await startDeployment(page);
  const base = operationUrl.replace(/\/deployments\/.*$/, "");
  await expect(page.getByRole("heading", { name: "Detalle del despliegue" }).first()).toBeVisible();
  expect(await violations(page)).toEqual([]);
  await page.goto(base);
  await expect(page.getByRole("link", { name: "Despliegues", exact: true })).toBeVisible();
  expect(await violations(page)).toEqual([]);
  await page.goto(`${base}/deployments`);
  await expect(page.getByRole("heading", { name: "Historial de despliegues" })).toBeVisible();
  expect(await violations(page)).toEqual([]);
});

test("axe: command palette on any viewport", async ({ page, isMobile }) => {
  await page.goto("/");
  const entry = page.getByRole("button", { name: /Buscar o ir a/ });
  if (isMobile) await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
  await entry.click();
  await expect(page.getByRole("option").first()).toBeVisible();
  expect(await violations(page)).toEqual([]);
});

test("keyboard focus is visible on tabs, rows and rail links", async ({ page, isMobile }) => {
  const operationUrl = await startDeployment(page);
  const base = operationUrl.replace(/\/deployments\/.*$/, "");
  await page.goto(base);
  expect(await hasFocusIndicator(page, "main nav a")).toBe(true);
  await page.goto("/");
  await expect(page.locator("a.app-row").first()).toBeVisible();
  expect(await hasFocusIndicator(page, "a.app-row")).toBe(true);
  if (!isMobile) expect(await hasFocusIndicator(page, "#sidebar-navigation nav a")).toBe(true);
});

test("palette keeps a visible indicator on the active option", async ({ page, isMobile }) => {
  await page.goto("/");
  if (isMobile) await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
  await page.getByRole("button", { name: /Buscar o ir a/ }).click();
  const box = page.getByRole("combobox");
  await expect(box).toBeFocused();
  await page.keyboard.press("ArrowDown");
  const id = await box.getAttribute("aria-activedescendant");
  expect(id).toBeTruthy();
  const bg = await page.locator(`[id="${id}"]`).evaluate((el) => getComputedStyle(el).backgroundColor);
  const other = await page.getByRole("option").last().evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(bg).not.toBe(other);
});
