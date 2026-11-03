import type { Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

/** Axe samples colors mid-fade unless animations and transitions have settled. */
export async function violations(page: Page) {
  await page.waitForFunction(() =>
    document.getAnimations().every((a) => a.playState !== "running"),
  );
  return (await new AxeBuilder({ page }).analyze()).violations;
}
