import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
async function start(page: Page, scenario = "Éxito") {
  const name = `app-${randomUUID().slice(0, 8)}`;
  await page.goto("/applications/new");
  await page.getByLabel("Repositorio", { exact: true }).fill("demo/example");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page.getByLabel("Nombre de la aplicación", { exact: true }).fill(name);
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page.getByRole("button", { name: scenario, exact: true }).click();
  await page
    .getByRole("button", { name: "Simular despliegue", exact: true })
    .click();
  await expect(page).toHaveURL(/\/operations\/[a-z0-9]+$/);
  return name;
}
async function noOverflow(page: Page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBeTruthy();
}

test("overview, search, empty state and responsive layout", async ({
  page,
}, info) => {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Aplicaciones", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("bitacora", { exact: true })).toBeVisible();
  await noOverflow(page);
  await page.screenshot({
    path: info.outputPath("overview.png"),
    fullPage: true,
  });
  await page
    .getByRole("searchbox", { name: "Buscar aplicaciones", exact: true })
    .fill("no-such-resource");
  await expect(
    page.getByText("Sin coincidencias", { exact: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(/q=no-such-resource/);
  await page.reload();
  await expect(
    page.getByRole("searchbox", { name: "Buscar aplicaciones", exact: true }),
  ).toHaveValue("no-such-resource");
});

test("successful deployment, logs, direct links and activity", async ({
  page,
}, info) => {
  const name = await start(page);
  await expect(
    page.getByText("Tu aplicación está lista en la demo", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Dirección ficticia, no navegable", { exact: true }),
  ).toBeVisible();
  await noOverflow(page);
  await page.screenshot({
    path: info.outputPath("deployment.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Pausar", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Seguir", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("searchbox", { name: "Buscar en el registro", exact: true })
    .fill("no-log-matches");
  await expect(
    page.getByText("No hay líneas que coincidan.", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText("Tu aplicación está lista en la demo", { exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Ver aplicación", exact: true }).click();
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
  await page.goto("/activity");
  await expect(page.getByText(name, { exact: true })).toBeVisible();
});

for (const scenario of ["Fallo de build", "Fallo de salud"]) {
  test(`${scenario} is actionable and can be retried`, async ({
    page,
  }, info) => {
    await start(page, scenario);
    await expect(
      page.getByRole("alert").filter({ hasText: "Fallido" }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("failure.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Volver a intentar", exact: true })
      .click();
    await expect(
      page.getByText("Tu aplicación está lista en la demo", { exact: true }),
    ).toBeVisible();
  });
}

test("disconnect and reconnect recover current server state", async ({
  page,
  request,
}) => {
  await start(page);
  const opID = page.url().split("/").pop()!;
  await page
    .getByRole("button", { name: "Simular desconexión", exact: true })
    .click();
  await expect(
    page.getByText("La operación continúa en el servidor.", { exact: false }),
  ).toBeVisible();
  await expect
    .poll(
      async () =>
        (await (await request.get(`/api/v1/operations/${opID}`)).json()).status,
    )
    .toBe("succeeded");
  await page.getByRole("button", { name: "Reconectar", exact: true }).click();
  await expect(
    page.getByText("Tu aplicación está lista en la demo", { exact: true }),
  ).toBeVisible();
});

test("form validation, draft restoration and review layout", async ({
  page,
}, info) => {
  await page.goto("/applications/new");
  await page.getByLabel("Repositorio", { exact: true }).fill("invalid");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Indica propietario/nombre",
  );
  await expect(page.getByLabel("Repositorio", { exact: true })).toBeFocused();
  await page.getByLabel("Repositorio", { exact: true }).fill("demo/restored");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page
    .getByLabel("Nombre de la aplicación", { exact: true })
    .fill("restored-draft");
  await page.reload();
  await expect(page.getByLabel("Repositorio", { exact: true })).toHaveValue(
    "demo/restored",
  );
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await expect(
    page.getByLabel("Nombre de la aplicación", { exact: true }),
  ).toHaveValue("restored-draft");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await expect(
    page.getByText("Todo listo para la simulación", { exact: true }),
  ).toBeVisible();
  await noOverflow(page);
  await page.screenshot({
    path: info.outputPath("review.png"),
    fullPage: true,
  });
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});

test("themes persist, respect system changes and navigation works with keyboard", async ({
  page,
  isMobile,
}) => {
  await page.goto("/");
  if (isMobile)
    await page
      .getByRole("button", { name: "Abrir navegación", exact: true })
      .click();
  await page.getByRole("button", { name: "Oscuro", exact: true }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.reload();
  await expect(page.locator("html")).toHaveClass(/dark/);
  if (isMobile)
    await page
      .getByRole("button", { name: "Abrir navegación", exact: true })
      .click();
  await page.getByRole("button", { name: "Claro", exact: true }).click();
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  await page.getByRole("button", { name: "Sistema", exact: true }).click();
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveClass(/dark/);
  if (isMobile) {
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("button", { name: "Abrir navegación", exact: true }),
    ).toBeFocused();
  }
  await page
    .getByRole("searchbox", { name: "Buscar aplicaciones", exact: true })
    .focus();
  await page.keyboard.type("atlas");
  await expect(page.getByText("atlas-api", { exact: true })).toBeVisible();
  await noOverflow(page);
});

test("overview accessibility and recoverable API errors", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("bitacora", { exact: true })).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.route("**/api/v1/applications", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: "Servicio temporalmente no disponible." }),
    }),
  );
  await page.reload();
  await expect(
    page.getByText("Servicio temporalmente no disponible.", { exact: true }),
  ).toBeVisible();
  await page.unroute("**/api/v1/applications");
  await page
    .getByRole("button", { name: "Volver a intentar", exact: true })
    .click();
  await expect(page.getByText("bitacora", { exact: true })).toBeVisible();
});

test("empty installation and missing pages have a usable next action", async ({
  page,
}) => {
  await page.route("**/api/v1/applications", (route) =>
    route.fulfill({ contentType: "application/json", body: "[]" }),
  );
  await page.goto("/");
  await expect(
    page.getByText("Tu espacio está listo", { exact: true }),
  ).toBeVisible();
  await page.goto("/does-not-exist");
  await expect(
    page.getByText("Esta página no existe", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Volver a aplicaciones", exact: true })
    .click();
  await expect(
    page.getByText("Tu espacio está listo", { exact: true }),
  ).toBeVisible();
});
