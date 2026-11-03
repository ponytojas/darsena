import { test, expect, type Page } from "@playwright/test";
import { violations } from "./axe";

const summary = { project_id: null, version: 1, updated_at: "2026-01-01T00:00:00Z", service_used: false, config: null, config_unavailable: false, service: null, service_unavailable: false };

/** Image checks: a pinned reference is echoed back as available; anything else is not found. */
async function mockImages(page: Page) {
  await page.route(/\/api\/v1\/worker\/image\?/, route => {
    const ref = new URL(route.request().url()).searchParams.get("ref") ?? "";
    return ref.includes("@sha256:")
      ? route.fulfill({ json: { reference: ref, status: "available", platform: "linux/arm64", daemon_platform: "linux/arm64" } })
      : route.fulfill({ status: 404, json: { error: "not found", code: "image_not_found" } });
  });
}

// Later routes take precedence, so a test can override this default.
test.beforeEach(async ({ page }) => mockImages(page));

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
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
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
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary }] }));
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
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service", async route => {
    if (route.request().method() === "GET") { lookups++; await route.fulfill({ status: 404, json: { error: "not found" } }); return; }
    creates++;
    if (creates === 1) { revision = 2; await route.fulfill({ status: 409, json: { error: "configuration revision conflict", code: "revision_conflict_before_dispatch" } }); return; }
    expect(route.request().postDataJSON().revision).toBe(2);
    await route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 2, phase: "observed", observation: "running", version: 3 } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary }] }));
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
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    const url = route.request().url();
    if (url.endsWith("/stop")) { expect(route.request().postDataJSON()).toEqual({ version: 1 }); state = "stopped"; version = 2; lastAction = { kind: "stop", phase: "observed", observation: "stopped" }; }
    if (url.endsWith("/remove")) { expect(route.request().postDataJSON()).toEqual({ version: 2, acknowledge_remove: true }); state = "absent"; version = 3; lastAction = { kind: "remove", phase: "observed", observation: "absent" }; }
    await route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: state, version, ...(lastAction ? { action: lastAction } : {}) } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary }] }));
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
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    if (route.request().url().endsWith("/stop")) { stops++; await route.abort("failed"); return; }
    if (route.request().url().endsWith("/refresh")) refreshes++;
    await route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: refreshes ? "stopped" : "running", version: refreshes ? 2 : 1 } });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary }] }));
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
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary }] }));
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
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app9/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/applications", async route => {
    if (route.request().method() === "POST") {
      created.push(route.request().postDataJSON());
      await route.fulfill({ json: { id: "app9", name: "nueva-app", image: `test@sha256:${"c".repeat(64)}`, port: 9000, created_at: "2026-01-01T00:00:00Z" } });
    } else await route.fulfill({ json: created.length ? [{ id: "app9", name: "nueva-app", image: `test@sha256:${"c".repeat(64)}`, port: 9000, created_at: "2026-01-01T00:00:00Z", ...summary }] : [] });
  });
  await page.goto("/apps/new");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await expect(page.getByRole("heading", { name: "Nueva aplicación", level: 1 })).toBeVisible();
  await expect(page.getByRole("radio")).toHaveCount(0);
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("X");
  await form.getByLabel("Imagen", { exact: true }).fill("sin-digest");
  await form.getByLabel("Puerto").fill("70000");
  await form.getByRole("button", { name: "Guardar definición" }).click();
  await expect(form.getByText(/Usa de 2 a 40 caracteres/)).toBeVisible();
  await expect(form.getByText("No se encuentra esa imagen o etiqueta en un registro público.")).toBeVisible();
  await expect(form.getByText("Indica un puerto entre 1 y 65535.")).toBeVisible();
  await expect(form.getByLabel("Nombre", { exact: true })).toBeFocused();
  expect(created).toHaveLength(0);
  await form.getByLabel("Nombre", { exact: true }).fill("nueva-app");
  await form.getByLabel("Imagen", { exact: true }).fill(`test@sha256:${"c".repeat(64)}`);
  await form.getByLabel("Puerto").fill("9000");
  await form.getByRole("button", { name: "Guardar definición" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  await expect(page.getByRole("heading", { name: "nueva-app" })).toBeVisible();
  expect(created).toEqual([{ name: "nueva-app", image: `test@sha256:${"c".repeat(64)}`, port: 9000 }]);
});

test("the list reads service state from the summary and never queries each service", async ({ page }) => {
  const serviceGets: string[] = [];
  const image = `test@sha256:${"a".repeat(64)}`;
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/*/service**", async route => {
    if (route.request().method() === "GET") serviceGets.push(route.request().url());
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [{ id: "p1", name: "Taller", color: "teal", version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [
    { id: "app1", name: "bitacora", image, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary, project_id: "p1", service_used: true, service: { phase: "observed", observation: "running", version: 1 } },
    { id: "app2", name: "faro", image, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary, service_unavailable: true },
  ] }));
  await page.goto("/");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  const project = page.getByRole("region", { name: "Taller" });
  await expect(project.getByText("bitacora")).toBeVisible();
  await expect(project.locator(".project-swatch")).toHaveAttribute("aria-hidden", "true");
  const ungrouped = page.getByRole("region", { name: "Sin proyecto" });
  await expect(ungrouped.getByText("faro")).toBeVisible();
  await expect(ungrouped.getByText("Estado del servicio no disponible")).toBeVisible();
  expect(serviceGets).toEqual([]);
});

test("settings lock the image and block deleting once the application had a service", async ({ page }) => {
  const image = `test@sha256:${"a".repeat(64)}`;
  let writes = 0;
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary, service_used: true }] }));
  await page.route("**/api/v1/worker/applications/app1", route => { if (route.request().method() !== "GET") writes++; return route.fulfill({ status: 404, json: { error: "x" } }); });
  await page.goto("/apps/definition/app1/settings");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await expect(page.getByLabel("Imagen", { exact: true })).toBeDisabled();
  await expect(page.getByText(/La recreación del servicio aún no está disponible/).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "Eliminar definición" })).toBeDisabled();
  await expect(page.getByText("No se puede eliminar porque la aplicación tiene o ha tenido un servicio.")).toBeVisible();
  expect(writes).toBe(0);
});

async function mockExperimental(page: Page, apps: unknown[]) {
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/*/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: apps }));
}

async function signIn(page: Page) {
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
}

const lockedApp = () => ({ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary });

test("settings show translated messages when the server refuses an image change or a delete", async ({ page }) => {
  await mockExperimental(page, [lockedApp()]);
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  let deleteCode = "application_has_service";
  await page.route("**/api/v1/worker/applications/app1", route => {
    const method = route.request().method();
    if (method === "PATCH") return route.fulfill({ status: 409, json: { error: "locked", code: "image_locked" } });
    if (method === "DELETE") return route.fulfill({ status: 409, json: { error: "busy", code: deleteCode } });
    return route.fulfill({ json: lockedApp() });
  });
  await page.goto("/apps/definition/app1/settings");
  await signIn(page);
  await page.getByLabel("Imagen", { exact: true }).fill(`test@sha256:${"b".repeat(64)}`);
  await page.getByRole("button", { name: "Guardar cambios" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "La imagen ya no se puede cambiar porque la aplicación tiene un servicio" })).toBeVisible();

  await page.getByRole("button", { name: "Eliminar definición" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(/Escribe «bitacora»/).fill("bitacora");
  await dialog.getByRole("button", { name: "Eliminar definición" }).click();
  await expect(dialog.getByRole("alert")).toContainText("Esta aplicación tiene o ha tenido un servicio y no se puede eliminar.");

  deleteCode = "runtime_busy";
  await dialog.getByRole("button", { name: "Eliminar definición" }).click();
  await expect(dialog.getByRole("alert")).toContainText("Hay otra operación de runtime en curso");
});

test("the restore dialog says it does not modify the existing service in experimental mode", async ({ page }) => {
  await mockExperimental(page, [lockedApp()]);
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 2, variables: [] } }));
  await page.route("**/api/v1/worker/config/app1/history", route => route.fulfill({ json: { entries: [
    { revision: 2, created_at: "2026-01-02T00:00:00Z", base: false, changes: [{ name: "TOKEN", kind: "changed", secret: true }] },
    { revision: 1, created_at: "2026-01-01T00:00:00Z", base: true, changes: [{ name: "TOKEN", kind: "added", secret: true }] },
  ] } }));
  await page.goto("/apps/definition/app1/history");
  await signIn(page);
  await page.getByRole("button", { name: "Restaurar valor anterior de TOKEN" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("No modifica el servicio existente.")).toBeVisible();
  await expect(dialog.getByText("El valor no se muestra.")).toBeVisible();
});

test("a failed project assignment after creating warns that the definition has no project", async ({ page }) => {
  await mockExperimental(page, []);
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [{ id: "p1", name: "Taller", color: "teal", version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/applications/app9", route => route.fulfill({ json: { ...lockedApp(), id: "app9", name: "nueva-app" } }));
  await page.route("**/api/v1/worker/applications/assign", route => route.fulfill({ status: 500, json: { error: "boom" } }));
  await page.route("**/api/v1/worker/applications", route => route.request().method() === "POST"
    ? route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "nueva-app" } })
    : route.fulfill({ json: [] }));
  await page.goto("/apps/new?kind=definition&project=p1");
  await signIn(page);
  await page.getByLabel("Nombre", { exact: true }).fill("nueva-app");
  await page.getByLabel("Imagen", { exact: true }).fill(`test@sha256:${"c".repeat(64)}`);
  await page.getByRole("button", { name: "Guardar definición" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  await expect(page.getByText(/La definición se ha creado sin proyecto/)).toBeVisible();
});

test("the image field resolves a tag, offers an explicit download and follows it until available", async ({ page }) => {
  const pinned = `docker.io/library/nginx@sha256:${"d".repeat(64)}`;
  let status = "missing";
  const pulls: unknown[] = [];
  const created: { image?: string }[] = [];
  await mockExperimental(page, []);
  await page.route(/\/api\/v1\/worker\/image\?/, route => {
    const ref = new URL(route.request().url()).searchParams.get("ref");
    if (ref !== "nginx:1.27" && ref !== pinned) return route.fulfill({ status: 404, json: { error: "x", code: "image_not_found" } });
    const reply = { reference: pinned, ...(ref === pinned ? {} : { resolved_from: "docker.io/library/nginx:1.27" }), status, platform: "linux/arm64", daemon_platform: "linux/arm64" };
    if (status === "pulling") status = "available";
    return route.fulfill({ json: reply });
  });
  await page.route("**/api/v1/worker/image/pull", route => {
    pulls.push(route.request().postDataJSON());
    status = "pulling";
    return route.fulfill({ status: 202, json: { reference: pinned, status: "pulling" } });
  });
  await page.route("**/api/v1/worker/applications", route => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    created.push(route.request().postDataJSON());
    return route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "web", image: pinned } });
  });
  await page.route("**/api/v1/worker/applications/app9", route => route.fulfill({ json: { ...lockedApp(), id: "app9", name: "web", image: pinned } }));
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision: 0, variables: [] } }));
  await page.goto("/apps/new?kind=definition");
  await signIn(page);
  await page.getByLabel("Nombre", { exact: true }).fill("web");
  const field = page.getByLabel("Imagen", { exact: true });
  await field.fill("nginx:1.27");
  await expect(page.getByText(/nginx:1\.27 apunta ahora a este digest/)).toBeVisible();
  await expect(page.getByText(pinned)).toBeVisible();
  await expect(page.getByText("Aún no está descargada en el runtime.")).toBeVisible();
  expect(await violations(page)).toEqual([]);
  await page.getByRole("button", { name: "Descargar imagen" }).click();
  await expect(page.getByText("Descargada en el runtime (linux/arm64).")).toBeVisible();
  expect(pulls).toEqual([{ reference: pinned }]);
  await page.getByRole("button", { name: "Guardar definición" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  // What is saved is the pinned digest, never the moving tag.
  expect(created[0].image).toBe(pinned);
});

test("a service is created without variables, and a rejection before dispatch is not uncertain", async ({ page }) => {
  const bodies: unknown[] = [];
  let reply: { status: number; json: unknown } = { status: 409, json: { error: "missing", code: "image_missing" } };
  let exists = false;
  await mockExperimental(page, [lockedApp()]);
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: exists ? 1 : 0, variables: [] } }));
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    if (route.request().method() === "POST") {
      bodies.push(route.request().postDataJSON());
      if (reply.status < 300) exists = true;
      return route.fulfill(reply);
    }
    return exists
      ? route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1 } })
      : route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/apps/definition/app1");
  await signIn(page);
  await expect(page.getByText("Sin variables: se guardará una configuración vacía como revisión 1.")).toBeVisible();
  await page.getByLabel("Entiendo que el servicio no tendrá red ni URL pública.").check();
  await page.getByRole("button", { name: "Crear servicio aislado" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "La imagen no está descargada en el runtime" })).toBeVisible();
  await expect(page.getByText(/No se ha podido confirmar el resultado/)).toHaveCount(0);
  expect(bodies).toEqual([{ revision: 0, acknowledge_no_network: true }]);
  reply = { status: 201, json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1 } };
  await page.getByLabel("Entiendo que el servicio no tendrá red ni URL pública.").check();
  await page.getByRole("button", { name: "Crear servicio aislado" }).click();
  await expect(page.getByRole("button", { name: "Parar servicio" })).toBeVisible();
});

test("the next step asks to download a missing image before offering creation", async ({ page }) => {
  await mockExperimental(page, [lockedApp()]);
  await page.route(/\/api\/v1\/worker\/image\?/, route => route.fulfill({ json: { reference: lockedApp().image, status: "missing", daemon_platform: "linux/arm64" } }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await signIn(page);
  await expect(page.getByText("Antes de crear el servicio, la imagen tiene que estar descargada en el runtime.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Descargar imagen" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Crear servicio aislado" })).toHaveCount(0);
});
