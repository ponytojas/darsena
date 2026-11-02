import { test, expect, type Page } from "@playwright/test";

async function openList(page: Page) {
  const toggle = page.getByRole("button", { name: "Abrir navegación" });
  if (await toggle.isVisible()) await toggle.click();
  await page.locator("#sidebar-navigation").getByRole("link", { name: /^Aplicaciones/ }).click();
}


test("owner creates one isolated service and can query an uncertain result", async ({ page }, info) => {
  let creates = 0;
  let exists = false;
  const service = { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: "running" };
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    expect(route.request().headers()["x-darsena-csrf"]).toBe("session-token");
    if (route.request().method() === "POST" && !route.request().url().endsWith("/refresh")) {
      creates++;
      expect(route.request().postDataJSON()).toEqual({ revision: 1, acknowledge_no_network: true });
      exists = true;
      await route.abort("failed");
    } else if (exists) await route.fulfill({ json: service });
    else await route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await expect(page.getByRole("heading", { name: "Acceso del propietario" })).toBeVisible();
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await expect(page.getByText("Todavía no hay un servicio para esta aplicación.")).toBeVisible();
  await page.getByLabel("Entiendo que el servicio no tendrá red ni URL pública.").check();
  await page.getByRole("button", { name: "Crear servicio aislado" }).click();
  await expect(page.getByText("En ejecución", { exact: true })).toBeVisible();
  await expect(page.getByText(/No se ha podido confirmar el resultado/)).toHaveCount(0);
  await page.getByRole("button", { name: "Consultar estado" }).click();
  expect(creates).toBe(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  await page.screenshot({ path: info.outputPath("experimental-service.png"), fullPage: true });
  await page.evaluate(() => { history.pushState({}, "", "/operations/example"); window.dispatchEvent(new PopStateEvent("popstate")); });
  await expect(page.getByText("Esta vista pertenece a las simulaciones de la demo.")).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Acceso del propietario" })).toBeVisible();
});

test("revision conflict before dispatch permits a reviewed retry only after a fresh absence", async ({ page }) => {
  let revision = 1;
  let creates = 0;
  let lookups = 0;
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service", async route => {
    if (route.request().method() === "GET") { lookups++; await route.fulfill({ status: 404, json: { error: "not found" } }); return; }
    creates++;
    if (creates === 1) { revision = 2; await route.fulfill({ status: 409, json: { error: "configuration revision conflict", code: "revision_conflict_before_dispatch" } }); return; }
    expect(route.request().postDataJSON().revision).toBe(2);
    await route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 2, phase: "observed", observation: "running", version: 3 } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await page.getByLabel("Entiendo que el servicio no tendrá red ni URL pública.").check();
  await page.getByRole("button", { name: "Crear servicio aislado" }).click();
  await expect(page.getByText(/La configuración cambió antes de enviar/)).toBeVisible();
  await expect(page.getByText(/Revisión 2 ·/)).toBeVisible();
  expect(lookups).toBeGreaterThanOrEqual(2);
  await page.getByLabel("Entiendo que el servicio no tendrá red ni URL pública.").check();
  await page.getByRole("button", { name: "Crear servicio aislado" }).click();
  await expect.poll(() => creates).toBe(2);
});

test("stops and removes only after observed state, with confirmation", async ({ page }, info) => {
  let state = "running";
  let version = 1;
  let lastAction: object | undefined;
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    const url = route.request().url();
    if (url.endsWith("/stop")) { expect(route.request().postDataJSON()).toEqual({ version: 1 }); state = "stopped"; version = 2; lastAction = { kind: "stop", phase: "observed", observation: "stopped" }; }
    if (url.endsWith("/remove")) { expect(route.request().postDataJSON()).toEqual({ version: 2, acknowledge_remove: true }); state = "absent"; version = 3; lastAction = { kind: "remove", phase: "observed", observation: "absent" }; }
    await route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: state, version, ...(lastAction ? { action: lastAction } : {}) } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await page.getByRole("button", { name: "Parar servicio" }).click();
  await expect(page.getByText("Detenido", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Eliminar servicio", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.screenshot({ path: info.outputPath("remove-confirmation.png"), fullPage: true });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Eliminar servicio", exact: true })).toBeFocused();
  await page.getByRole("button", { name: "Eliminar servicio", exact: true }).click();
  await page.getByRole("button", { name: "Confirmar eliminación del servicio" }).click();
  await expect(page.getByText("No encontrado", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Crear servicio aislado" })).toHaveCount(0);
});

test("an uncertain stop remains blocked after navigation until the state is queried", async ({ page }) => {
  let stops = 0;
  let refreshes = 0;
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    if (route.request().url().endsWith("/stop")) { stops++; await route.abort("failed"); return; }
    if (route.request().url().endsWith("/refresh")) refreshes++;
    await route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: refreshes ? "stopped" : "running", version: refreshes ? 2 : 1 } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await page.getByRole("button", { name: "Parar servicio" }).click();
  await expect(page.getByText(/No se ha confirmado el resultado de la acción/)).toBeVisible();
  await page.reload();
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await expect(page.getByText(/No se ha confirmado el resultado de la acción/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Parar servicio" })).toHaveCount(0);
  await page.getByRole("button", { name: "Consultar estado" }).click();
  await expect(page.getByRole("button", { name: "Eliminar servicio", exact: true })).toBeVisible();
  expect(stops).toBe(1);
  expect(refreshes).toBe(1);
});

test("an unsaved variable draft asks before leaving and survives keep-editing", async ({ page }) => {
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.goto("/apps/definition/app1/variables");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await page.getByRole("button", { name: "Añadir variable" }).click();
  await page.getByLabel("Nombre de variable").fill("BORRADOR");
  await openList(page);
  await expect(page.getByRole("dialog", { name: "¿Descartar el borrador?" })).toBeVisible();
  await page.getByRole("button", { name: "Seguir editando" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page).toHaveURL(/\/apps\/definition\/app1\/variables$/);
  await expect(page.getByLabel("Nombre de variable")).toHaveValue("BORRADOR");
  // Switching tabs inside the same definition keeps the draft without asking.
  await page.getByRole("link", { name: "Resumen", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("link", { name: "Variables", exact: true }).click();
  await expect(page.getByLabel("Nombre de variable")).toHaveValue("BORRADOR");
  await openList(page);
  await page.getByRole("button", { name: "Descartar borrador" }).click();
  await expect(page).toHaveURL(/\/$/);
});

test("experimental creation opens the definition form directly and lands on the next step", async ({ page }) => {
  const created: unknown[] = [];
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app9/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/applications", async route => {
    if (route.request().method() === "POST") {
      created.push(route.request().postDataJSON());
      await route.fulfill({ json: { id: "app9", name: "nueva-app", image: `test@sha256:${"c".repeat(64)}`, port: 9000, created_at: "2026-01-01T00:00:00Z" } });
    } else await route.fulfill({ json: created.length ? [{ id: "app9", name: "nueva-app", image: `test@sha256:${"c".repeat(64)}`, port: 9000, created_at: "2026-01-01T00:00:00Z" }] : [] });
  });
  await page.goto("/apps/new");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await expect(page.getByRole("heading", { name: "Nueva aplicación", level: 1 })).toBeVisible();
  await expect(page.getByRole("radio")).toHaveCount(0);
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("X");
  await form.getByLabel("Imagen por digest").fill("sin-digest");
  await form.getByLabel("Puerto").fill("70000");
  await form.getByRole("button", { name: "Guardar definición" }).click();
  await expect(form.getByText(/Usa de 2 a 40 caracteres/)).toBeVisible();
  await expect(form.getByText(/Indica la imagen con su digest/)).toBeVisible();
  await expect(form.getByText("Indica un puerto entre 1 y 65535.")).toBeVisible();
  await expect(form.getByLabel("Nombre", { exact: true })).toBeFocused();
  expect(created).toHaveLength(0);
  await form.getByLabel("Nombre", { exact: true }).fill("nueva-app");
  await form.getByLabel("Imagen por digest").fill(`test@sha256:${"c".repeat(64)}`);
  await form.getByLabel("Puerto").fill("9000");
  await form.getByRole("button", { name: "Guardar definición" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  await expect(page.getByRole("heading", { name: "nueva-app" })).toBeVisible();
  expect(created).toEqual([{ name: "nueva-app", image: `test@sha256:${"c".repeat(64)}`, port: 9000 }]);
});
