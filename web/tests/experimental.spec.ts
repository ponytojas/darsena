import { test, expect, type Page } from "@playwright/test";
import { violations } from "./axe";
import { expectEnglish } from "./english";

const summary = { project_id: null, version: 1, updated_at: "2026-01-01T00:00:00Z", service_used: false, access: "isolated", shared_network: false, domain: null, config: null, config_unavailable: false, service: null, service_unavailable: false };

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
      expect(route.request().postDataJSON()).toEqual({ revision: 1 });
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
  await expect(page.getByText(/sin red ni dirección/)).toBeVisible();
  await page.getByRole("button", { name: "Desplegar", exact: true }).click();
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

test("a revision conflict before dispatch explains itself and a retry uses the current revision", async ({ page }) => {
  let revision = 1;
  let creates = 0;
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  const running = { application_id: "app1", operation_id: "initial", revision: 2, phase: "observed", observation: "running", version: 3 };
  await page.route("**/api/v1/worker/applications/app1/service**", async route => {
    if (route.request().url().endsWith("/refresh")) return route.fulfill({ json: running });
    if (route.request().method() === "GET") return creates < 2 ? route.fulfill({ status: 404, json: { error: "not found" } }) : route.fulfill({ json: running });
    creates++;
    if (creates === 1) { revision = 2; return route.fulfill({ status: 409, json: { error: "configuration revision conflict", code: "revision_conflict_before_dispatch" } }); }
    expect(route.request().postDataJSON().revision).toBe(2);
    return route.fulfill({ status: 201, json: running });
  });
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image: `test@sha256:${"a".repeat(64)}`, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary }] }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await page.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "La configuración ha cambiado antes de enviar la operación" })).toBeVisible();
  await expect(page.getByText(/No se ha podido confirmar el resultado/)).toHaveCount(0);
  await page.getByRole("button", { name: "Reintentar" }).click();
  await expect.poll(() => creates).toBe(2);
  await expect(page.getByRole("button", { name: "Parar servicio" })).toBeVisible();
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
  await form.getByText("Más opciones", { exact: true }).click();
  await form.getByLabel("Puerto", { exact: true }).fill("70000");
  await form.getByRole("button", { name: "Guardar sin desplegar" }).click();
  await expect(form.getByText(/Usa de 2 a 40 caracteres/)).toBeVisible();
  await expect(form.getByText("No se encuentra esa imagen o etiqueta en un registro público.")).toBeVisible();
  await expect(form.getByText("Indica un puerto entre 1 y 65535.")).toBeVisible();
  await expect(form.getByLabel("Nombre", { exact: true })).toBeFocused();
  expect(created).toHaveLength(0);
  await form.getByLabel("Nombre", { exact: true }).fill("nueva-app");
  await form.getByLabel("Imagen", { exact: true }).fill(`test@sha256:${"c".repeat(64)}`);
  await form.getByLabel("Puerto", { exact: true }).fill("9000");
  await form.getByRole("button", { name: "Guardar sin desplegar" }).click();
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

test("settings lock the image, and deleting stops and removes the service first", async ({ page }) => {
  const image = `test@sha256:${"a".repeat(64)}`;
  const calls: string[] = [];
  const view = (observation: string, version: number) => ({ application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation, version });
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => {
    const url = route.request().url();
    if (url.endsWith("/stop")) { calls.push("stop"); expect(route.request().postDataJSON()).toEqual({ version: 1 }); return route.fulfill({ json: view("stopped", 2) }); }
    if (url.endsWith("/remove")) { calls.push("remove"); expect(route.request().postDataJSON()).toMatchObject({ version: 2 }); return route.fulfill({ json: view("absent", 3) }); }
    return route.fulfill({ json: view("running", 1) });
  });
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: [{ id: "app1", name: "bitacora", image, port: 8080, created_at: "2026-01-01T00:00:00Z", ...summary, service_used: true }] }));
  await page.route("**/api/v1/worker/applications/app1", route => { if (route.request().method() === "DELETE") { calls.push("delete"); return route.fulfill({ status: 204, body: "" }); } return route.fulfill({ status: 404, json: { error: "x" } }); });
  await page.goto("/apps/definition/app1/settings");
  await page.getByLabel("Credencial del propietario").fill("test-only-credential");
  await page.getByRole("button", { name: "Iniciar sesión" }).click();
  await expect(page.getByLabel("Imagen", { exact: true })).toBeDisabled();
  await expect(page.getByText(/La recreación del servicio aún no está disponible/).first()).toBeVisible();
  await page.getByRole("button", { name: "Eliminar definición" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/se parará y se eliminará su contenedor/)).toBeVisible();
  await dialog.getByLabel(/Escribe «bitacora»/).fill("bitacora");
  await dialog.getByRole("button", { name: "Eliminar definición" }).click();
  await expect(page).toHaveURL(/\/$/);
  expect(calls).toEqual(["stop", "remove", "delete"]);
});

async function mockExperimental(page: Page, apps: unknown[]) {
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready" } }));
  await page.route("**/api/v1/auth/login", route => route.fulfill({ json: { csrf: "session-token" } }));
  await page.route("**/api/v1/worker/applications/*/service**", route => route.fulfill({ status: 404, json: { error: "not found" } }));
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [] }));
  await page.route("**/api/v1/worker/applications", route => route.fulfill({ json: apps }));
}

async function signIn(page: Page) {
  await page.getByLabel(/Credencial del propietario|Owner credential/).fill("test-only-credential");
  await page.getByRole("button", { name: /Iniciar sesión|Sign in/ }).click();
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
  await expect(dialog.getByRole("alert")).toContainText("El servicio de esta aplicación sigue existiendo.");

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
  await page.getByRole("button", { name: "Guardar sin desplegar" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  await expect(page.getByText(/La definición se ha creado sin proyecto/)).toBeVisible();
});

test("Deploy from the form resolves the tag, downloads, creates the project and variables, and starts", async ({ page }) => {
  const canonical = `docker.io/library/nginx:1.27@sha256:${"d".repeat(64)}`;
  let downloaded = false;
  const calls: string[] = [];
  const bodies: Record<string, unknown> = {};
  let revision = 0;
  await mockExperimental(page, []);
  await page.route(/\/api\/v1\/worker\/image\?/, route => {
    const ref = new URL(route.request().url()).searchParams.get("ref");
    if (ref !== "nginx:1.27" && ref !== canonical) return route.fulfill({ status: 404, json: { error: "x", code: "image_not_found" } });
    return route.fulfill({ json: { reference: canonical, resolved_from: "docker.io/library/nginx:1.27", status: downloaded ? "available" : "missing", platform: "linux/arm64", daemon_platform: "linux/arm64" } });
  });
  await page.route("**/api/v1/worker/image/pull", route => { calls.push("pull"); bodies.pull = route.request().postDataJSON(); downloaded = true; return route.fulfill({ status: 202, json: { reference: canonical, status: "pulling" } }); });
  await page.route("**/api/v1/worker/projects", route => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    calls.push("project"); bodies.project = route.request().postDataJSON();
    return route.fulfill({ status: 201, json: { id: "p9", name: "Web", color: "blue", version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" } });
  });
  await page.route("**/api/v1/worker/applications", route => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    calls.push("save"); bodies.save = route.request().postDataJSON();
    return route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "web", image: canonical } });
  });
  await page.route("**/api/v1/worker/applications/assign", route => { calls.push("assign"); return route.fulfill({ json: { assigned: 1 } }); });
  await page.route("**/api/v1/worker/config/app9/variables/*", route => { calls.push("variable"); bodies.variable = route.request().postDataJSON(); revision++; return route.fulfill({ json: { revision, variables: [] } }); });
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision, variables: [] } }));
  const running = { application_id: "app9", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1 };
  await page.route("**/api/v1/worker/applications/app9/service**", route => {
    if (route.request().url().endsWith("/refresh")) return route.fulfill({ json: running });
    if (route.request().method() === "POST") { calls.push("service"); bodies.service = route.request().postDataJSON(); return route.fulfill({ status: 201, json: running }); }
    return route.fulfill({ json: running });
  });
  await page.goto("/apps/new?kind=definition");
  await signIn(page);
  await page.getByLabel("Nombre", { exact: true }).fill("web");
  await page.getByLabel("Imagen", { exact: true }).fill("nginx:1.27");
  await expect(page.getByText("Se descargará al desplegar.")).toBeVisible();
  await expect(page.getByText(/dddddddd/)).toHaveCount(0);
  await page.getByLabel("Proyecto", { exact: true }).selectOption({ label: "+ Nuevo proyecto…" });
  await expect(page.getByLabel("Nombre del nuevo proyecto")).toBeFocused();
  await page.getByLabel("Nombre del nuevo proyecto").fill("Web");
  await page.getByRole("button", { name: "Añadir variable" }).click();
  await expect(page.getByLabel("Nombre 1")).toBeFocused();
  await page.getByLabel("Nombre 1").fill("TOKEN");
  await page.getByLabel(/^Valor/).fill("x");
  expect(await violations(page)).toEqual([]);
  await page.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/, { timeout: 15_000 });
  expect(calls).toEqual(["pull", "project", "save", "assign", "variable", "service"]);
  expect(bodies.pull).toEqual({ reference: canonical });
  expect(bodies.project).toMatchObject({ name: "Web" });
  expect(bodies.save).toEqual({ name: "web", image: canonical, port: 8080 });
  expect(bodies.variable).toEqual({ revision: 0, value: "x", secret: true });
  expect(bodies.service).toEqual({ revision: 1 });
});

test("a rejection before dispatch from Deploy is not uncertain and can be retried", async ({ page }) => {
  let reply: { status: number; json: unknown } = { status: 409, json: { error: "missing", code: "image_missing" } };
  let exists = false;
  const bodies: unknown[] = [];
  await mockExperimental(page, [lockedApp()]);
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: exists ? 1 : 0, variables: [] } }));
  const running = { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1 };
  await page.route("**/api/v1/worker/applications/app1/service**", route => {
    if (route.request().url().endsWith("/refresh")) return route.fulfill({ json: running });
    if (route.request().method() === "POST") {
      bodies.push(route.request().postDataJSON());
      if (reply.status < 300) exists = true;
      return route.fulfill(reply);
    }
    return exists ? route.fulfill({ json: running }) : route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/apps/definition/app1");
  await signIn(page);
  await expect(page.getByText(/Sin variables: se guardará una configuración vacía/)).toBeVisible();
  await page.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "La imagen no está descargada en el runtime" })).toBeVisible();
  await expect(page.getByText(/No se ha podido confirmar el resultado/)).toHaveCount(0);
  expect(bodies).toEqual([{ revision: 0 }]);
  reply = { status: 201, json: running };
  await page.getByRole("button", { name: "Reintentar" }).click();
  await expect(page.getByRole("button", { name: "Parar servicio" })).toBeVisible();
});

test("an incompatible image offers changing it instead of deploying", async ({ page }) => {
  await mockExperimental(page, [lockedApp()]);
  await page.route(/\/api\/v1\/worker\/image\?/, route => route.fulfill({ json: { reference: lockedApp().image, status: "incompatible", reason: "platform", platform: "windows/amd64", daemon_platform: "linux/arm64" } }));
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.goto("/apps/definition/app1");
  await signIn(page);
  await expect(page.getByText("Esta imagen es para windows/amd64 y el runtime usa linux/arm64: no podrá ejecutarse.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Cambiar imagen" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Desplegar", exact: true })).toHaveCount(0);
});

test("a service that ends by itself reads as exited, with its exit code", async ({ page }) => {
  await mockExperimental(page, [{ ...lockedApp(), service_used: true }]);
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.request().url().endsWith("/refresh")
    ? route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: "stopped", version: 2, exit_code: 3 } })
    : route.fulfill({ json: { application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1 } }));
  await page.goto("/apps/definition/app1");
  await signIn(page);
  await expect(page.getByRole("button", { name: "Parar servicio" })).toBeVisible();
  await page.getByRole("button", { name: "Consultar estado" }).click();
  await expect(page.getByText("El proceso terminó con código 3.")).toBeVisible();
  await expect(page.getByText("Terminado", { exact: true })).toBeVisible();
});

// Network access (Fase E4). The proxy port comes from /capabilities and is absent when the listener is off.
async function mockCapabilities(page: Page, appsPort?: number) {
  await page.route("**/api/v1/capabilities", route => route.fulfill({ json: { mode: "experimental", real_execution: { enabled: true, provider: "ready", configuration: "accepted_revision" }, worker: { configured: true, status: "ready" }, state: "ready", vault: "ready", ...(appsPort ? { apps_port: appsPort } : {}) } }));
}
const digest = `test@sha256:${"c".repeat(64)}`;

async function openNewForm(page: Page, appsPort: number | undefined = 7321) {
  await mockExperimental(page, []);
  await mockCapabilities(page, appsPort);
  await page.goto("/apps/new?kind=definition");
  await signIn(page);
}

test("the access selector sends each level to the API and Isolated sends nothing extra", async ({ page }) => {
  const saved: unknown[] = [];
  await openNewForm(page);
  await page.route("**/api/v1/worker/applications", route => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    saved.push(route.request().postDataJSON());
    return route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "web", image: digest } });
  });
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("web");
  await form.getByLabel("Imagen", { exact: true }).fill(digest);
  await form.getByText("Más opciones", { exact: true }).click();
  await expect(form.getByLabel("Acceso", { exact: true })).toHaveValue("isolated");
  await expect(form.getByText("Sin red: no recibe ni hace conexiones.")).toBeVisible();
  await expect(form.getByLabel("Conectar también a la red común")).toHaveCount(0);
  await expect(form.getByLabel("Dominio", { exact: true })).toHaveCount(0);
  await expect(form.getByText("Se ejecutará aislada, sin red ni dirección.")).toBeVisible();

  await form.getByLabel("Acceso", { exact: true }).selectOption("internal");
  await form.getByLabel("Conectar también a la red común").check();
  await expect(form.getByText(/tiene acceso a Internet y puede alcanzar a las demás aplicaciones de su red/)).toBeVisible();
  await expect(form.getByLabel("Dominio", { exact: true })).toHaveCount(0);

  await form.getByLabel("Acceso", { exact: true }).selectOption("local");
  await expect(form.getByLabel("Dominio", { exact: true })).toHaveValue("web");
  await expect(form.getByText("Se abrirá en http://web.localhost:7321")).toBeVisible();
  await expect(form.getByText(/no tiene autenticación: cualquier programa de este equipo, y cualquier web abierta en el navegador/)).toBeVisible();
  await form.getByLabel("Dominio", { exact: true }).fill("mi-web");
  await expect(form.getByText("Se abrirá en http://mi-web.localhost:7321")).toBeVisible();
  expect(await violations(page)).toEqual([]);
  await form.getByRole("button", { name: "Guardar sin desplegar" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  expect(saved).toEqual([{ name: "web", image: digest, port: 8080, access: "local", shared_network: true, domain: "mi-web" }]);
});

test("internal access is sent without a domain, and going back to Isolated sends nothing", async ({ page }) => {
  const saved: unknown[] = [];
  await openNewForm(page);
  await page.route("**/api/v1/worker/applications", route => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    saved.push(route.request().postDataJSON());
    return route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "api", image: digest } });
  });
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("api");
  await form.getByLabel("Imagen", { exact: true }).fill(digest);
  await form.getByText("Más opciones", { exact: true }).click();
  await form.getByLabel("Acceso", { exact: true }).selectOption("internal");
  await form.getByRole("button", { name: "Guardar sin desplegar" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/);
  expect(saved).toEqual([{ name: "api", image: digest, port: 8080, access: "internal", shared_network: false }]);
});

test("an image that declares a port preselects Open with that port, visibly, and never overrides a manual choice", async ({ page }) => {
  const image = (port?: number) => ({ reference: digest, status: "available", platform: "linux/arm64", daemon_platform: "linux/arm64", ...(port ? { exposed_port: port } : {}) });
  await openNewForm(page);
  await page.route(/\/api\/v1\/worker\/image\?/, route => {
    const ref = new URL(route.request().url()).searchParams.get("ref") ?? "";
    return ref === "nginx:1.27" ? route.fulfill({ json: image(80) }) : ref === "nginx:1.28" ? route.fulfill({ json: image(3000) }) : route.fulfill({ json: image() });
  });
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("web");
  await form.getByLabel("Imagen", { exact: true }).fill("nginx:1.27");
  // Without opening anything: the section opens by itself and says why.
  await expect(form.getByLabel("Acceso", { exact: true })).toHaveValue("local");
  await expect(form.getByText("Elegida porque la imagen declara el puerto 80.")).toBeVisible();
  await expect(form.getByLabel("Puerto", { exact: true })).toHaveValue("80");
  await expect(form.getByText("Se abrirá en http://web.localhost:7321")).toBeVisible();
  expect(await violations(page)).toEqual([]);

  await form.getByLabel("Acceso", { exact: true }).selectOption("internal");
  await expect(form.getByText("Elegida porque la imagen declara el puerto 80.")).toHaveCount(0);
  await form.getByLabel("Puerto", { exact: true }).fill("9000");
  const second = page.waitForResponse(/ref=nginx%3A1\.28|ref=nginx:1\.28/);
  await form.getByLabel("Imagen", { exact: true }).fill("nginx:1.28");
  await second;
  await page.waitForTimeout(300);
  await expect(form.getByLabel("Acceso", { exact: true })).toHaveValue("internal");
  await expect(form.getByLabel("Puerto", { exact: true })).toHaveValue("9000");
});

test("an image without a declared port keeps the form Isolated and the section closed", async ({ page }) => {
  await openNewForm(page);
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("web");
  await form.getByLabel("Imagen", { exact: true }).fill(digest);
  await expect(form.getByText("Se descargará al desplegar.").or(form.getByText(/Descargada en el runtime/))).toBeVisible();
  await expect(form.getByLabel("Acceso", { exact: true })).toBeHidden();
  await form.getByText("Más opciones", { exact: true }).click();
  await expect(form.getByLabel("Acceso", { exact: true })).toHaveValue("isolated");
});

test("a domain that is taken is flagged while typing and blocks both buttons", async ({ page }) => {
  const posts: unknown[] = [];
  await openNewForm(page);
  await page.route(/\/api\/v1\/worker\/domains\/check/, route => {
    const name = new URL(route.request().url()).searchParams.get("name");
    return route.fulfill({ json: name === "libre" ? { available: true } : name === "www" ? { available: false, reason: "reserved" } : { available: false, reason: "taken" } });
  });
  await page.route("**/api/v1/worker/applications", route => {
    if (route.request().method() === "POST") posts.push(route.request().postDataJSON());
    return route.fulfill({ json: [] });
  });
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("web");
  await form.getByLabel("Imagen", { exact: true }).fill(digest);
  await form.getByText("Más opciones", { exact: true }).click();
  await form.getByLabel("Acceso", { exact: true }).selectOption("local");
  await expect(form.getByText("Ese dominio ya lo usa otra aplicación. Elige otro.")).toBeVisible();
  expect(await violations(page)).toEqual([]);
  await form.getByRole("button", { name: "Guardar sin desplegar" }).click();
  await expect(form.getByLabel("Dominio", { exact: true })).toBeFocused();
  await form.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(form.getByLabel("Dominio", { exact: true })).toBeFocused();
  await form.getByLabel("Dominio", { exact: true }).fill("www");
  await expect(form.getByText("Ese dominio está reservado. Elige otro.")).toBeVisible();
  await form.getByLabel("Dominio", { exact: true }).fill("Mala_");
  await expect(form.getByText(/Usa de 2 a 40 caracteres: minúsculas/)).toBeVisible();
  await form.getByLabel("Dominio", { exact: true }).fill("libre");
  await expect(form.getByText("Se abrirá en http://libre.localhost:7321")).toBeVisible();
  expect(posts).toEqual([]);
});

test("a domain the server still rejects returns to the field with the draft kept, and Deploy then continues", async ({ page }) => {
  const saves: Record<string, unknown>[] = [];
  const services: unknown[] = [];
  let reject = true;
  await openNewForm(page);
  const running = { application_id: "app9", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1, access: "local", domain: "otro" };
  await page.route("**/api/v1/worker/applications", route => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    saves.push(route.request().postDataJSON());
    return reject ? route.fulfill({ status: 409, json: { error: "taken", code: "domain_taken" } }) : route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "web", image: digest, access: "local", domain: "otro" } });
  });
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision: 0, variables: [] } }));
  await page.route("**/api/v1/worker/applications/app9/service**", route => {
    if (route.request().url().endsWith("/refresh")) return route.fulfill({ json: running });
    if (route.request().method() === "POST") { services.push(route.request().postDataJSON()); return route.fulfill({ status: 201, json: running }); }
    return route.fulfill({ json: running });
  });
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("web");
  await form.getByLabel("Imagen", { exact: true }).fill(digest);
  await form.getByText("Más opciones", { exact: true }).click();
  await form.getByLabel("Acceso", { exact: true }).selectOption("local");
  await form.getByLabel("Puerto", { exact: true }).fill("8081");
  await form.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(form.getByText("Ese dominio ya lo usa otra aplicación. Elige otro.")).toBeVisible();
  await expect(form.getByLabel("Dominio", { exact: true })).toBeFocused();
  await expect(form.getByLabel("Nombre", { exact: true })).toHaveValue("web");
  await expect(form.getByLabel("Imagen", { exact: true })).toHaveValue(digest);
  await expect(form.getByLabel("Puerto", { exact: true })).toHaveValue("8081");
  await expect(form.getByRole("button", { name: "Reintentar" })).toHaveCount(0);
  await expect(form.getByRole("button", { name: "Desplegar", exact: true })).toBeVisible();
  expect(await violations(page)).toEqual([]);
  reject = false;
  await form.getByLabel("Dominio", { exact: true }).fill("otro");
  await form.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/, { timeout: 15_000 });
  expect(saves).toEqual([
    { name: "web", image: digest, port: 8081, access: "local", shared_network: false, domain: "web" },
    { name: "web", image: digest, port: 8081, access: "local", shared_network: false, domain: "otro" },
  ]);
  expect(services).toEqual([{ revision: 0 }]);
});

test("a network error while creating the service can be retried and resumes", async ({ page }) => {
  const services: unknown[] = [];
  let code = "network_unavailable";
  await openNewForm(page);
  const running = { application_id: "app9", operation_id: "initial", revision: 1, phase: "observed", observation: "running", version: 1, access: "internal", domain: null };
  let created = false;
  await page.route("**/api/v1/worker/applications", route => route.request().method() === "POST"
    ? route.fulfill({ status: 201, json: { ...lockedApp(), id: "app9", name: "api", image: digest, access: "internal" } })
    : route.fulfill({ json: [] }));
  await page.route("**/api/v1/worker/config/app9", route => route.fulfill({ json: { revision: 0, variables: [] } }));
  await page.route("**/api/v1/worker/applications/app9/service**", route => {
    if (route.request().url().endsWith("/refresh")) return route.fulfill({ json: running });
    if (route.request().method() === "POST") {
      services.push(route.request().postDataJSON());
      if (code) return route.fulfill({ status: code === "network_unavailable" ? 503 : 409, json: { error: "x", code } });
      created = true;
      return route.fulfill({ status: 201, json: running });
    }
    return created ? route.fulfill({ json: running }) : route.fulfill({ status: 404, json: { error: "x" } });
  });
  const form = page.getByRole("region", { name: "Definición desde imagen" });
  await form.getByLabel("Nombre", { exact: true }).fill("api");
  await form.getByLabel("Imagen", { exact: true }).fill(digest);
  await form.getByText("Más opciones", { exact: true }).click();
  await form.getByLabel("Acceso", { exact: true }).selectOption("internal");
  await form.getByRole("button", { name: "Desplegar", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "No se ha podido crear la red" })).toBeVisible();
  await expect(page.getByText(/No se ha podido confirmar el resultado/)).toHaveCount(0);
  code = "";
  await page.getByRole("button", { name: "Reintentar" }).click();
  await expect(page).toHaveURL(/\/apps\/definition\/app9$/, { timeout: 15_000 });
  expect(services).toEqual([{ revision: 0 }, { revision: 0 }]);
});

const service = (observation: string, extra: object = {}) => ({ application_id: "app1", operation_id: "initial", revision: 1, phase: "observed", observation, version: 1, access: "local", domain: "bitacora", ...extra });
async function openDetail(page: Page, { observation = "running", appsPort = 7321 as number | null, app = {} as object, svc = {} as object } = {}) {
  await mockExperimental(page, [{ ...lockedApp(), service_used: true, access: "local", shared_network: false, domain: "bitacora", ...app }]);
  await mockCapabilities(page, appsPort ?? undefined);
  await page.route("**/api/v1/worker/config/app1", route => route.fulfill({ json: { revision: 1, variables: [] } }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.fulfill({ json: service(observation, svc) }));
  await page.goto("/apps/definition/app1");
  await signIn(page);
}

test("a running local service opens at exactly the domain and the proxy port, in a new tab", async ({ page }) => {
  await openDetail(page);
  const open = page.getByRole("link", { name: "Abrir" });
  await expect(open).toHaveAttribute("href", "http://bitacora.localhost:7321");
  await expect(open).toHaveAttribute("target", "_blank");
  await expect(open).toHaveAttribute("rel", /noopener/);
  await expect(open).toHaveAttribute("rel", /noreferrer/);
  await expect(page.getByText("Abierta en este equipo · bitacora.localhost")).toBeVisible();
  await expect(page.getByText(/127\.0\.0\.1/)).toHaveCount(0);
  expect(await violations(page)).toEqual([]);
});

test("a stopped local service has no link", async ({ page }) => {
  await openDetail(page, { observation: "stopped" });
  await expect(page.getByRole("button", { name: "Eliminar servicio" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Abrir" })).toHaveCount(0);
  await expect(page.getByText("Abierta en este equipo · bitacora.localhost")).toBeVisible();
});

test("without the address listener there is no dead link, and the page says why", async ({ page }) => {
  await openDetail(page, { appsPort: null });
  await expect(page.getByRole("button", { name: "Parar servicio" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Abrir" })).toHaveCount(0);
  await expect(page.getByText(/El oyente de direcciones del servidor está apagado/).first()).toBeVisible();
  expect(await violations(page)).toEqual([]);
});

test("an internal service shows its internal name and an isolated one says it is isolated", async ({ page }) => {
  await openDetail(page, { app: { access: "internal", domain: null }, svc: { access: "internal", domain: null } });
  await expect(page.getByText("Red interna · http://bitacora:8080")).toBeVisible();
  await expect(page.getByRole("link", { name: "Abrir" })).toHaveCount(0);
});

test("an isolated service header says it is not published", async ({ page }) => {
  await openDetail(page, { app: { access: "isolated", domain: null }, svc: { access: "isolated", domain: null } });
  await expect(page.getByText("Aislada · puerto 8080, sin publicar")).toBeVisible();
});

test("the deployed access wins over what is saved", async ({ page }) => {
  await openDetail(page, { app: { access: "internal", domain: null }, svc: { access: "local", domain: "desplegada" } });
  await expect(page.getByRole("link", { name: "Abrir" })).toHaveAttribute("href", "http://desplegada.localhost:7321");
});

test("settings lock access, network, domain and port once a service exists, and explain why", async ({ page }) => {
  await mockExperimental(page, [{ ...lockedApp(), service_used: true, access: "local", shared_network: true, domain: "bitacora", project_id: "p1" }]);
  await mockCapabilities(page);
  await page.route("**/api/v1/worker/projects", route => route.fulfill({ json: [{ id: "p1", name: "Taller", color: "teal", version: 1, created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" }] }));
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.fulfill({ json: service("running") }));
  await page.goto("/apps/definition/app1/settings");
  await signIn(page);
  await expect(page.getByLabel("Acceso", { exact: true })).toBeDisabled();
  await expect(page.getByLabel("Acceso", { exact: true })).toHaveValue("local");
  await expect(page.getByLabel("Conectar también a la red común")).toBeDisabled();
  await expect(page.getByLabel("Conectar también a la red común")).toBeChecked();
  await expect(page.getByLabel("Dominio", { exact: true })).toBeDisabled();
  await expect(page.getByLabel("Dominio", { exact: true })).toHaveValue("bitacora");
  await expect(page.getByLabel("Puerto", { exact: true })).toBeDisabled();
  await expect(page.getByText(/se fijaron al desplegar/)).toBeVisible();
  await expect(page.getByText(/mover la aplicación de proyecto no cambia la de su servicio en marcha/)).toBeVisible();
  expect(await violations(page)).toEqual([]);
});

test("settings of an isolated application with a service keep the port editable and show no project note", async ({ page }) => {
  await mockExperimental(page, [{ ...lockedApp(), service_used: true }]);
  await mockCapabilities(page);
  await page.route("**/api/v1/worker/applications/app1/service**", route => route.fulfill({ json: service("running", { access: "isolated", domain: null }) }));
  await page.goto("/apps/definition/app1/settings");
  await signIn(page);
  await expect(page.getByLabel("Acceso", { exact: true })).toBeDisabled();
  await expect(page.getByLabel("Puerto", { exact: true })).toBeEnabled();
  await expect(page.getByText(/mover la aplicación de proyecto/)).toHaveCount(0);
});

test("settings change access with all fields, report a refused domain on its field and handle access_locked", async ({ page }) => {
  const patches: unknown[] = [];
  let reply: { status: number; json: unknown } = { status: 409, json: { error: "x", code: "domain_taken" } };
  await mockExperimental(page, [lockedApp()]);
  await mockCapabilities(page);
  await page.route("**/api/v1/worker/applications/app1", route => {
    if (route.request().method() !== "PATCH") return route.fulfill({ json: lockedApp() });
    patches.push(route.request().postDataJSON());
    return route.fulfill(reply);
  });
  await page.goto("/apps/definition/app1/settings");
  await signIn(page);
  await expect(page.getByLabel("Acceso", { exact: true })).toBeEnabled();
  await expect(page.getByLabel("Acceso", { exact: true })).toHaveValue("isolated");
  await page.getByLabel("Acceso", { exact: true }).selectOption("local");
  await expect(page.getByLabel("Dominio", { exact: true })).toHaveValue("bitacora");
  await page.getByRole("button", { name: "Guardar cambios" }).click();
  await expect(page.getByText("Ese dominio ya lo usa otra aplicación. Elige otro.")).toBeVisible();
  await expect(page.getByLabel("Dominio", { exact: true })).toBeFocused();
  expect(patches).toEqual([{ version: 1, image: lockedApp().image, port: 8080, access: "local", shared_network: false, domain: "bitacora" }]);

  reply = { status: 409, json: { error: "x", code: "access_locked" } };
  await page.getByLabel("Dominio", { exact: true }).fill("otra");
  await page.getByRole("button", { name: "Guardar cambios" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "ya no se pueden cambiar porque la aplicación tiene un servicio" })).toBeVisible();

  reply = { status: 409, json: { error: "x", code: "update_conflict" } };
  await page.getByLabel("Dominio", { exact: true }).fill("otra2");
  await page.getByRole("button", { name: "Guardar cambios" }).click();
  await expect(page.getByRole("button", { name: "Recargar" })).toBeVisible();
});

test("settings send nothing about the network when only the port changes", async ({ page }) => {
  const patches: unknown[] = [];
  await mockExperimental(page, [{ ...lockedApp(), access: "internal" }]);
  await mockCapabilities(page);
  await page.route("**/api/v1/worker/applications/app1", route => {
    if (route.request().method() !== "PATCH") return route.fulfill({ json: lockedApp() });
    patches.push(route.request().postDataJSON());
    return route.fulfill({ json: { ...lockedApp(), access: "internal", port: 9000 } });
  });
  await page.goto("/apps/definition/app1/settings");
  await signIn(page);
  await page.getByLabel("Puerto", { exact: true }).fill("9000");
  await page.getByRole("button", { name: "Guardar cambios" }).click();
  await expect(page.getByText("Ajustes guardados")).toBeVisible();
  expect(patches).toEqual([{ version: 1, image: lockedApp().image, port: 9000 }]);
});

test("the access states read in English with no Spanish left", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("darsena.locale", "en"));
  await openNewForm(page);
  await page.route(/\/api\/v1\/worker\/domains\/check/, route => route.fulfill({ json: { available: false, reason: "taken" } }));
  const form = page.getByRole("region", { name: "Definition from an image" });
  await form.getByLabel("Name", { exact: true }).fill("web");
  await form.getByLabel("Image", { exact: true }).fill(digest);
  await form.getByText("More options", { exact: true }).click();
  await form.getByLabel("Access", { exact: true }).selectOption("local");
  await expect(form.getByText("Another application already uses that domain. Choose another.")).toBeVisible();
  await expect(form.getByText(/has no authentication: any program on this computer/)).toBeVisible();
  await expectEnglish(page);
  expect(await violations(page)).toEqual([]);

  await openDetail(page);
  await expect(page.getByRole("link", { name: "Open" })).toHaveAttribute("href", "http://bitacora.localhost:7321");
  await expect(page.getByText("Open on this computer · bitacora.localhost")).toBeVisible();
  await expectEnglish(page);
});
