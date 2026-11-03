import { test, expect, type Page } from "@playwright/test";
import { violations } from "./axe";
import { randomUUID } from "node:crypto";
async function start(page: Page, scenario = "Éxito") {
  const name = `app-${randomUUID().slice(0, 8)}`;
  await page.goto("/apps/new");
  await page.getByLabel("Repositorio", { exact: true }).fill("demo/example");
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page.getByLabel("Nombre de la aplicación", { exact: true }).fill(name);
  await page.getByRole("button", { name: "Continuar", exact: true }).click();
  await page.getByRole("button", { name: scenario, exact: true }).click();
  await page
    .getByRole("button", { name: "Simular despliegue", exact: true })
    .click();
  await expect(page).toHaveURL(/\/apps\/example\/[^/]+\/deployments\/[a-z0-9]+$/);
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
  await expect(page.getByRole("group", { name: "Resumen por estado" })).toContainText("Total");
  await expect(page.getByRole("button", { name: "Ejemplos", exact: false }).first()).toBeVisible();
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
  await page.getByRole("link", { name: "Despliegues", exact: true }).click();
  await expect(page).toHaveURL(/\/apps\/example\/[^/]+\/deployments$/);
  await expect(page.getByRole("link", { name: /Éxito|Correcto|Completado/ }).first()).toBeVisible();
  await page.goto("/activity");
  await expect(page.getByText(name, { exact: true })).toBeVisible();
  await page.getByRole("link", { name: new RegExp(name) }).first().click();
  await expect(page).toHaveURL(/\/apps\/example\/[^/]+\/deployments\/[a-z0-9]+$/);
  await expect(page.getByRole("link", { name: "Despliegues", exact: true })).toBeVisible();
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

test("example rows summarise the latest deployments for assistive technology", async ({ page }) => {
  const name = await start(page, "Fallo de build");
  await page.getByRole("button", { name: "Volver a intentar", exact: true }).click();
  await expect(page.getByText("Tu aplicación está lista en la demo", { exact: true })).toBeVisible();
  await page.goto("/");
  const row = page.locator(".app-row", { hasText: name });
  await expect(row.locator(".sr-only", { hasText: "Últimos 2 despliegues: 1 correcto, 1 fallido" })).toHaveCount(1);
  await expect(row.locator(".app-history-dots i")).toHaveCount(2);
  await expect(row.locator(".app-history-dots")).toHaveAttribute("aria-hidden", "true");
});

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
  await page.goto("/apps/new");
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
  expect((await violations(page))).toEqual([]);
});

test("themes persist, respect system changes and navigation works with keyboard", async ({
  page,
  isMobile,
}) => {
  await page.goto("/");
  const openSettings = async () => {
    if (await page.getByRole("button", { name: "Abrir navegación", exact: true }).isVisible())
      await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
    await page.getByRole("button", { name: "Ajustes", exact: true }).click();
  };
  await openSettings();
  await page.getByRole("button", { name: "Oscuro", exact: true }).click();
  await page.getByRole("radio", { name: "Naranja", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "orange");
  await expect(page.locator("html")).toHaveClass(/dark/);
  expect((await violations(page))).toEqual([]);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Ajustes", exact: true })).toBeFocused();
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-accent", "orange");
  await expect(page.locator("html")).toHaveClass(/dark/);
  await openSettings();
  await page.getByRole("button", { name: "Claro", exact: true }).click();
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  await page.getByRole("button", { name: "Sistema", exact: true }).click();
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.keyboard.press("Escape");
  if (isMobile) {
    await expect(page.getByRole("button", { name: "Cerrar navegación", exact: true })).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: "Abrir navegación", exact: true })).toBeFocused();
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
  expect((await violations(page))).toEqual([]);
  await page.route("**/api/v1/applications", (route) =>
    route.fulfill({
      status: 503,
      contentType: "application/json",
      body: JSON.stringify({ error: "Servicio temporalmente no disponible." }),
    }),
  );
  await page.reload();
  await expect(
    page.getByText(
      "El servicio no está disponible ahora. Inténtalo de nuevo en unos instantes.",
      { exact: true },
    ),
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

test("filters combine with search and keyboard navigation", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("bitacora", { exact: true })).toBeVisible();
  await page.keyboard.press("/");
  const search = page.getByRole("searchbox", { name: "Buscar aplicaciones", exact: true });
  await expect(search).toBeFocused();
  await search.fill("atlas");
  await expect(page).toHaveURL(/q=atlas/);
  await page.getByRole("button", { name: /^Requieren atención/ }).click();
  await expect(page).toHaveURL(/health=attention/);
  await expect(page.getByText("Sin coincidencias", { exact: true })).toBeVisible();
  await expect(search).toHaveValue("atlas");
  await page.getByRole("button", { name: /^Todas/ }).click();
  await expect(page).not.toHaveURL(/health=/);
  await expect(page.getByText("atlas-api", { exact: true })).toBeVisible();
  await expect(page.getByText("bitacora", { exact: true })).toHaveCount(0);
  await page.getByRole("group", { name: "Filtrar aplicaciones" }).getByRole("button", { name: /^Ejemplos/ }).click();
  await expect(page).toHaveURL(/kind=example/);
  await expect(page.getByText("atlas-api", { exact: true })).toBeVisible();
  await search.focus();
  await page.keyboard.press("Escape");
  await expect(search).toHaveValue("");
  await page.keyboard.press("ArrowDown");
  await expect(page.locator(".app-row").first()).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(page.locator(".app-row").nth(1)).toBeFocused();
  await page.keyboard.press("End");
  await expect(page.locator(".app-row").last()).toBeFocused();
  await page.keyboard.press("Home");
  await expect(page.locator(".app-row").first()).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/apps\/example\/[a-z0-9-]+$/);
});

test("groups collapse and remember their state", async ({ page }) => {
  await page.goto("/");
  const toggle = page.locator(".apps-group-heading button");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator(".app-row").first()).toBeHidden();
  await page.reload();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await toggle.click();
  await expect(page.locator(".app-row").first()).toBeVisible();
});

test("activity lists only example deployments and links to diagnostics", async ({ page }) => {
  await page.goto("/activity?view=worker");
  await expect(page.getByRole("heading", { name: "Actividad", level: 1 })).toBeVisible();
  await expect(page.getByRole("button", { name: "Trabajos del worker" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Despliegues de ejemplo" })).toHaveCount(0);
  await expect(page.locator("#sidebar-navigation").getByRole("link", { name: "Actividad", includeHidden: true }).first()).toHaveAttribute("href", "/activity");
});

test("diagnostics reports a missing worker with a copyable command", async ({ page, context, isMobile }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]).catch(() => undefined);
  await page.goto("/diagnostics");
  await expect(page.getByRole("heading", { name: "Diagnóstico", level: 1 })).toBeVisible();
  const worker = page.getByTestId("check-worker");
  await expect(worker).toContainText("No configurado en este servidor.");
  await expect(worker.getByText("Siguiente paso")).toBeVisible();
  await expect(worker.locator("pre").first()).toContainText("darsena worker --demo --state");
  await expect(page.getByRole("heading", { name: "Trabajos simulados del worker" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Actualizar estado" })).toBeVisible();
  await worker.getByRole("button", { name: /^Copiar comando para iniciar el worker/ }).click();
  await expect(worker.getByRole("button", { name: /^Copiado: comando para iniciar el worker/ })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: /Copiado|No se pudo copiar/ })).toHaveCount(1);
  await page.getByRole("button", { name: "Actualizar estado" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Estado actualizado." })).toHaveCount(1);
  await noOverflow(page);
  expect((await violations(page))).toEqual([]);
  if (isMobile) return;
  await page.goto("/");
  await page.getByRole("link", { name: /Demo · /i }).click();
  await expect(page).toHaveURL(/\/diagnostics$/);
});

test("sidebar visibility persists on desktop and mobile navigation closes", async ({ page, isMobile }) => {
  await page.goto("/");
  const open = page.getByRole("button", { name: "Abrir navegación", exact: true });
  const close = page.getByRole("button", { name: "Cerrar navegación", exact: true });
  if (isMobile) {
    await open.click();
    await page.getByRole("navigation").getByRole("link", { name: "Actividad" }).click();
    await expect(open).toHaveAttribute("aria-expanded", "false");
    await expect(page.locator("#main-content")).toBeFocused();
  } else {
    await close.click();
    await expect(open).toHaveAttribute("aria-expanded", "false");
    // Collapsed desktop sidebar is a 56px icon rail: links stay reachable by name, labels are gone.
    await expect(page.locator("#sidebar-navigation")).toBeVisible();
    expect((await page.locator("#sidebar-navigation").boundingBox())?.width).toBe(56);
    await expect(page.getByRole("navigation").getByRole("link", { name: "Actividad" })).toBeVisible();
    await expect(page.locator("#sidebar-navigation .brand-name")).toBeHidden();
    await page.reload();
    await expect(open).toBeVisible();
    await page.keyboard.press("ControlOrMeta+b");
    await expect(close).toHaveAttribute("aria-expanded", "true");
    expect((await page.locator("#sidebar-navigation").boundingBox())?.width).toBeGreaterThan(150);
    await page.getByRole("button", { name: "Ajustes", exact: true }).click();
    await page.keyboard.press("ControlOrMeta+b");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(close).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("ControlOrMeta+b");
    await expect(open).toHaveAttribute("aria-expanded", "false");
  }
});


test("settings returns focus to the navigation toggle when the sidebar becomes hidden", async ({ page, isMobile }) => {
  test.skip(isMobile, "Exercises a desktop to mobile viewport change");
  await page.goto("/");
  await page.getByRole("button", { name: "Ajustes", exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Abrir navegación", exact: true })).toBeFocused();
  await page.setViewportSize({ width: 1000, height: 800 });
  await expect(page.getByRole("button", { name: "Cerrar navegación", exact: true })).toHaveAttribute("aria-expanded", "true");
  await page.getByRole("button", { name: "Cerrar navegación", exact: true }).click();
  await page.getByRole("button", { name: "Ajustes", exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Abrir navegación", exact: true })).toBeFocused();
});

test("direct links work and legacy URLs redirect", async ({ page, isMobile }) => {
  const name = await start(page);
  const operationId = page.url().split("/").pop()!;
  const response = await page.request.get(`/api/v1/operations/${operationId}`);
  const applicationId = (await response.json()).application_id as string;
  await page.goto(`/apps/example/${applicationId}`);
  await expect(page.getByRole("heading", { name, exact: true }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Simular despliegue", exact: true })).toBeVisible();
  await page.goto(`/apps/example/${applicationId}/deployments/${operationId}`);
  await expect(page.getByRole("heading", { name: "Detalle del despliegue" }).first()).toBeVisible();
  if (!isMobile) await expect(page.locator(".breadcrumbs").getByText(name)).toBeVisible();
  await page.goto(`/applications/${applicationId}`);
  await expect(page).toHaveURL(new RegExp(`/apps/example/${applicationId}$`));
  await expect(page.getByRole("heading", { name, exact: true }).first()).toBeVisible();
  await page.goto(`/operations/${operationId}`);
  await expect(page).toHaveURL(new RegExp(`/apps/example/${applicationId}/deployments/${operationId}$`));
  await expect(page.getByRole("heading", { name: "Detalle del despliegue" }).first()).toBeVisible();
  await page.goto("/operations/does-not-exist");
  await expect(page.getByText("No se encuentra este despliegue", { exact: true })).toBeVisible();
});

test("settings: categories by keyboard and GitHub link preference", async ({ page, isMobile }) => {
  await page.goto("/");
  const github = page.getByRole("link", { name: "Dársena en GitHub", includeHidden: true });
  if (await page.getByRole("button", { name: "Abrir navegación", exact: true }).isVisible())
    await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
  await expect(github).toBeVisible();
  await page.getByRole("button", { name: "Ajustes", exact: true }).click();
  const appearance = page.getByRole("tab", { name: "Apariencia", exact: true });
  await expect(appearance).toHaveAttribute("aria-selected", "true");
  await appearance.focus();
  await page.keyboard.press(isMobile ? "ArrowRight" : "ArrowDown");
  await expect(page.getByRole("tab", { name: "Idioma", exact: true })).toBeFocused();
  await page.keyboard.press(isMobile ? "ArrowRight" : "ArrowDown");
  const toggle = page.getByRole("switch", { name: "Mostrar enlace a GitHub" });
  await expect(page.getByRole("tab", { name: "Interfaz", exact: true })).toHaveAttribute("aria-selected", "true");
  expect((await violations(page))).toEqual([]);
  await expect(toggle).toBeChecked();
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  await page.keyboard.press("Escape");
  if (isMobile) await expect(github).toBeHidden();
  await expect(github).toHaveCount(0);
  await page.reload();
  await expect(github).toHaveCount(0);
  if (await page.getByRole("button", { name: "Abrir navegación", exact: true }).isVisible())
    await page.getByRole("button", { name: "Abrir navegación", exact: true }).click();
  await page.getByRole("button", { name: "Ajustes", exact: true }).click();
  await page.getByRole("tab", { name: "Interfaz", exact: true }).click();
  await page.getByRole("switch", { name: "Mostrar enlace a GitHub" }).click();
  await expect(github).toBeVisible();
});
