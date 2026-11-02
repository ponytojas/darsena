import { test, expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

async function trigger(page: import("@playwright/test").Page) {
  const entry = page.getByRole("button", { name: /Buscar o ir a/ });
  if (!(await entry.isVisible()))
    await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
  return entry;
}

test("palette opens with the shortcut, filters and navigates by keyboard", async ({ page, isMobile }) => {
  test.skip(!!isMobile, "keyboard shortcut is desktop only");
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Aplicaciones", exact: true })).toBeVisible();
  await page.keyboard.press("ControlOrMeta+k");
  const box = page.getByRole("combobox");
  await expect(box).toBeFocused();
  await expect(page.getByRole("option").first()).toBeVisible();
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations).toEqual([]);
  const name = (await page.getByRole("group", { name: "Aplicaciones" }).getByRole("option").first().locator("span").first().innerText()).slice(1, 6);
  await box.fill(name.toUpperCase());
  await page.keyboard.press("ArrowDown");
  await expect(box).toHaveAttribute("aria-activedescendant", /.+/);
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/apps\/example\//);
  await expect(page.getByRole("combobox")).toHaveCount(0);
});

test("palette closes with Escape and returns focus; language action works", async ({ page }) => {
  await page.goto("/");
  const entry = await trigger(page);
  await entry.click();
  await expect(page.getByRole("combobox")).toBeFocused();
  await page.getByRole("combobox").fill("zzzz");
  await expect(page.getByText("Sin resultados")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("combobox")).toHaveCount(0);
  await expect(entry).toBeFocused();
  await entry.click();
  await page.getByRole("combobox").fill("ingles");
  await page.getByRole("combobox").fill("english");
  await page.keyboard.press("Enter");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await page.evaluate(() => localStorage.removeItem("darsena.locale"));
});
