import { test, expect, type Page } from "@playwright/test";
import { violations } from "./axe";
import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import {
  mkdtempSync,
  chmodSync,
  rmSync,
  existsSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { startStack } from "./worker-stack";
import { expectEnglish } from "./english";

async function openListIn(page: Page) {
  const toggle = page.getByRole("button", { name: "Abrir navegación" });
  if (await toggle.isVisible()) await toggle.click();
  await page
    .locator("#sidebar-navigation")
    .getByRole("link", { name: /^Aplicaciones/ })
    .click();
}
async function port(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const address = s.address();
      if (!address || typeof address === "string")
        return reject(new Error("no address"));
      const value = address.port;
      s.close(() => resolve(value));
    });
    s.on("error", reject);
  });
}
async function until(check: () => Promise<boolean>, message: string) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    try {
      if (await check()) return;
    } catch {
      /* Wait for startup. */
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(message);
}
async function stop(child: ChildProcess) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  await new Promise<void>((resolve) => {
    child.once("exit", () => resolve());
    child.kill("SIGTERM");
  });
}

test("persistent worker activity survives a worker restart", async ({
  page,
}, info) => {
  test.setTimeout(90_000);
  // Short socket paths are required on macOS. Every process and file is test-owned.
  const dir = mkdtempSync(
    join(tmpdir().startsWith("/var/") ? "/tmp" : tmpdir(), "darsena-e2e-"),
  );
  chmodSync(dir, 0o700);
  const binary = join(dir, "darsena"),
    socket = join(dir, "worker.sock"),
    state = join(dir, "state.db");
  let worker: ChildProcess | undefined, server: ChildProcess | undefined;
  try {
    execFileSync("go", ["build", "-o", binary, "../cmd/darsena"], {
      timeout: 60000,
    });
    const keySource = join(dir, "keygen.go"),
      identity = join(dir, "identity.age");
    writeFileSync(
      keySource,
      `package main
import ("os"; "filippo.io/age")
func main(){ key,err:=age.GenerateX25519Identity();if err!=nil{panic(err)};if err=os.WriteFile(os.Args[1],[]byte("# test identity\\n"+key.String()+"\\n"),0600);err!=nil{panic(err)} }`,
    );
    execFileSync("go", ["run", keySource, identity], { timeout: 60000 });
    const startWorker = async () => {
      const child = spawn(binary, [
        "worker",
        "--demo",
        "--state",
        state,
        "--socket",
        socket,
        "--config-db",
        join(dir, "config.db"),
        "--age-identity",
        identity,
      ]);
      await until(async () => existsSync(socket), "worker did not start");
      return child;
    };
    worker = await startWorker();
    const listen = await port();
    server = spawn(binary, [
      "serve",
      "--demo",
      "--addr",
      `127.0.0.1:${listen}`,
      "--web",
      "dist",
      "--worker-socket",
      socket,
    ]);
    const base = `http://127.0.0.1:${listen}`;
    await until(
      async () => (await fetch(`${base}/api/v1/status`)).ok,
      "server did not start",
    );
    await page.goto(`${base}/diagnostics`);
    await expect(
      page.getByRole("heading", { name: "Trabajos simulados del worker" }),
    ).toBeVisible();
    await page
      .getByLabel("Identificador del recurso")
      .fill(`ejemplo-${info.project.name}`);
    let firstRequest = true;
    await page.route("**/api/v1/worker/jobs", async (route) => {
      if (route.request().method() === "POST" && firstRequest) {
        firstRequest = false;
        await route.fetch(); // The worker receives the request, but its response is lost.
        await route.abort();
      } else await route.continue();
    });
    await page.getByRole("button", { name: "Iniciar simulación" }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    await page.getByRole("button", { name: "Iniciar simulación" }).click();
    const row = page
      .getByTestId("worker-job")
      .filter({ hasText: `ejemplo-${info.project.name}` });
    await expect(row).toContainText("Completado", { timeout: 10000 });
    await expect(row).toHaveCount(1);
    await page.screenshot({
      path: info.outputPath("worker-activity.png"),
      fullPage: true,
      animations: "disabled",
    });
    // Legacy URLs redirect to the unified list and to the definition detail.
    await page.goto(`${base}/registered`);
    await expect(page).toHaveURL(`${base}/`);
    await page.goto(`${base}/registered?app=missing-test-app`);
    await expect(page).toHaveURL(`${base}/apps/definition/missing-test-app`);
    await expect(page.getByText("Aplicación no encontrada")).toBeVisible();
    await page.screenshot({
      path: info.outputPath("registered-unknown.png"),
      fullPage: true,
      animations: "disabled",
    });
    const definitionForm = page.getByRole("region", {
      name: "Definición desde imagen",
    });
    // Type choice: keyboard selection, and ?kind= survives a reload and Back.
    await page.goto(`${base}/apps/new`);
    await expect(
      page.getByRole("heading", { name: "Nueva aplicación", level: 1 }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Continuar", exact: true }),
    ).toBeDisabled();
    await page.getByRole("radio", { name: /Ejemplo simulado/ }).focus();
    await page.keyboard.press("ArrowRight");
    await expect(
      page.getByRole("radio", { name: /Definición desde imagen/ }),
    ).toBeChecked();
    await page.getByRole("button", { name: "Continuar", exact: true }).click();
    await expect(page).toHaveURL(/\/apps\/new\?kind=definition$/);
    await expect(definitionForm).toBeVisible();
    await page.reload();
    await expect(definitionForm).toBeVisible();
    await page.getByRole("button", { name: "Cambiar tipo" }).click();
    await expect(page).toHaveURL(`${base}/apps/new`);
    await page.goBack();
    await expect(definitionForm).toBeVisible();
    await page.goto(`${base}/applications/new`);
    await expect(page).toHaveURL(`${base}/apps/new`);
    await page.goto(`${base}/apps/new?kind=example`);
    await expect(page.getByLabel("Repositorio", { exact: true })).toBeVisible();
    await page.goto(`${base}/apps/new?kind=definition`);
    await expect(definitionForm).toBeVisible();
    // Inline translated validation, no native tooltips.
    await definitionForm
      .getByLabel("Nombre", { exact: true })
      .fill("Mal Nombre");
    await definitionForm
      .getByLabel("Imagen por digest")
      .fill("ghcr.io/example/app:latest");
    await definitionForm
      .getByRole("button", { name: "Guardar definición" })
      .click();
    await expect(
      definitionForm.getByText(/Usa de 2 a 40 caracteres/),
    ).toBeVisible();
    await expect(
      definitionForm.getByText(/Añade el digest de la imagen/),
    ).toBeVisible();
    await expect(
      definitionForm.getByLabel("Nombre", { exact: true }),
    ).toBeFocused();
    // Unsaved changes are protected when leaving.
    await openListIn(page);
    await expect(page.getByRole("dialog")).toContainText(
      "Hay cambios sin guardar",
    );
    await page.getByRole("button", { name: "Seguir editando" }).click();
    await expect(page).toHaveURL(/\/apps\/new\?kind=definition$/);
    await definitionForm.getByLabel("Nombre", { exact: true }).fill("");
    await definitionForm.getByLabel("Imagen por digest").fill("");
    await page.screenshot({
      path: info.outputPath("creation-form.png"),
      fullPage: true,
      animations: "disabled",
    });
    await definitionForm
      .getByLabel("Nombre", { exact: true })
      .fill("prueba-app");
    await definitionForm
      .getByLabel("Imagen por digest")
      .fill(`ghcr.io/example/app@sha256:${"a".repeat(64)}`);
    await definitionForm
      .getByRole("button", { name: "Guardar definición" })
      .click();
    await expect(
      page.getByRole("heading", { name: "prueba-app" }),
    ).toBeVisible();
    await expect(page.getByText("La demo no arranca servicios.")).toBeVisible();
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("TOKEN");
    await page.getByLabel("Valor", { exact: true }).fill("test-secret-value");
    await page.route("**/api/v1/capabilities", async (route) => {
      const response = await route.fetch();
      const capabilities = await response.json();
      await route.fulfill({
        response,
        json: {
          ...capabilities,
          worker: { ...capabilities.worker, status: "unavailable" },
          state: "ready",
          vault: "unavailable",
        },
      });
    });
    await expect(
      page.getByText("Almacén cifrado no disponible.", { exact: false }),
    ).toBeVisible({ timeout: 20000 });
    await expect(
      page.getByText("Aunque el estado de trabajos está disponible", {
        exact: false,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Guardar variable" }),
    ).toBeDisabled();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue(
      "test-secret-value",
    );
    await page.unroute("**/api/v1/capabilities");
    await page.getByRole("button", { name: "Actualizar estado" }).click();
    await expect(
      page.getByRole("button", { name: "Guardar variable" }),
    ).toBeEnabled();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue(
      "test-secret-value",
    );
    await page.route("**/api/v1/capabilities", (route) => route.abort());
    await expect(
      page.getByRole("button", { name: "Guardar variable" }),
    ).toBeDisabled({ timeout: 20000 });
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue(
      "test-secret-value",
    );
    await page.unroute("**/api/v1/capabilities");
    await page.getByRole("button", { name: "Actualizar estado" }).click();
    await expect(
      page.getByRole("button", { name: "Guardar variable" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await expect(page.getByText("Secreto guardado")).toBeVisible();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveCount(0);
    await expect(page.getByText("test-secret-value")).toHaveCount(0);
    // A stale revision must preserve the draft until an explicit refresh and save.
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("PUBLIC_TEST");
    await page.getByRole("button", { name: "Normal" }).click();
    await page.getByLabel("Valor", { exact: true }).fill("private-test-value");
    let conflictOnce = true;
    await page.route(
      "**/api/v1/worker/config/**/variables/PUBLIC_TEST",
      async (route) => {
        if (route.request().method() === "PUT" && conflictOnce) {
          conflictOnce = false;
          await route.fulfill({
            status: 409,
            contentType: "application/json",
            body: JSON.stringify({ error: "revision conflict" }),
          });
        } else await route.continue();
      },
    );
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue(
      "private-test-value",
    );
    await expect(
      page.getByRole("button", { name: "Guardar variable" }),
    ).toBeDisabled();
    await page
      .getByRole("button", { name: "Actualizar configuración" })
      .click();
    await expect(
      page.getByRole("button", { name: "Guardar variable" }),
    ).toBeEnabled();
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await expect(page.getByText("private-test-value")).toBeVisible();
    await page.getByRole("button", { name: "Editar PUBLIC_TEST" }).click();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue(
      "private-test-value",
    );
    await page.screenshot({
      path: info.outputPath("editor-public.png"),
      fullPage: true,
      animations: "disabled",
    });
    await page.getByRole("button", { name: "Cancelar" }).click();
    // Adding an existing name explicitly announces replacement and retains its type.
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("TOKEN");
    await expect(
      page.getByRole("button", { name: "Sustituir variable" }),
    ).toBeVisible();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveAttribute(
      "type",
      "password",
    );
    await page.getByRole("button", { name: "Cancelar" }).click();
    await page.getByRole("button", { name: "Descartar borrador" }).click();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect((await violations(page))).toEqual([]);
    await page.screenshot({
      path: info.outputPath("variables.png"),
      fullPage: true,
      animations: "disabled",
    });
    const savedURL = page.url();

    // A response for A must not close B's new draft or invalidate B's config.
    await page.goto(`${base}/apps/new?kind=definition`);
    await definitionForm
      .getByLabel("Nombre", { exact: true })
      .fill("segunda-app");
    await definitionForm
      .getByLabel("Imagen por digest")
      .fill(`ghcr.io/example/second@sha256:${"b".repeat(64)}`);
    await definitionForm
      .getByRole("button", { name: "Guardar definición" })
      .click();
    await expect(
      page.getByRole("heading", { name: "segunda-app" }),
    ).toBeVisible();
    const secondID = new URL(page.url()).pathname.split("/")[3];
    const openList = () => openListIn(page);
    await openList();
    await page.getByRole("link", { name: /prueba-app/ }).click();
    await page.getByRole("link", { name: "Variables", exact: true }).click();
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("LATE_TEST");
    await page.getByLabel("Valor", { exact: true }).fill("test-late-value");
    let releaseResponse!: () => void;
    const responseGate = new Promise<void>((resolve) => {
      releaseResponse = resolve;
    });
    let requestStarted!: () => void;
    const requestSeen = new Promise<void>((resolve) => {
      requestStarted = resolve;
    });
    await page.route(
      "**/api/v1/worker/config/**/variables/LATE_TEST",
      async (route) => {
        const response = await route.fetch();
        requestStarted();
        await responseGate;
        await route.fulfill({ response });
      },
    );
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await requestSeen;
    await openList();
    await page.getByRole("button", { name: "Descartar borrador" }).click();
    await expect(page.getByRole("link", { name: /segunda-app/ })).toBeVisible();
    await page.getByRole("link", { name: /segunda-app/ }).click();
    await expect(
      page.getByRole("heading", { name: "segunda-app" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("DRAFT_B");
    await page.getByLabel("Valor", { exact: true }).fill("test-draft-b");
    let secondFetches = 0;
    page.on("request", (request) => {
      if (
        request.method() === "GET" &&
        request.url().includes(`/worker/config/${secondID}`)
      )
        secondFetches++;
    });
    const lateResponse = page.waitForResponse((response) =>
      response.url().includes("/variables/LATE_TEST"),
    );
    releaseResponse();
    await lateResponse;
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue(
      "test-draft-b",
    );
    expect(secondFetches).toBe(0);
    await page.getByRole("button", { name: "Cancelar" }).click();
    await page.getByRole("button", { name: "Descartar borrador" }).click();
    await openList();
    await page.getByRole("link", { name: /prueba-app/ }).click();
    await page.getByRole("link", { name: "Variables", exact: true }).click();
    await expect(
      page
        .getByRole("listitem")
        .locator("strong")
        .getByText("LATE_TEST", { exact: true }),
    ).toBeVisible();

    await page.goto(`${base}/diagnostics`);
    await page
      .getByLabel("Identificador del recurso")
      .fill("borrador-conexion");
    await stop(worker);
    worker = undefined;
    await expect(
      page.getByText("Worker temporalmente no disponible", { exact: false }),
    ).toBeVisible({ timeout: 20000 });
    await expect(page.getByLabel("Identificador del recurso")).toHaveValue(
      "borrador-conexion",
    );
    await expect(
      page.getByRole("button", { name: "Iniciar simulación" }),
    ).toBeDisabled();
    worker = await startWorker();
    await page.getByRole("button", { name: "Actualizar estado" }).click();
    await expect(
      page.getByRole("button", { name: "Iniciar simulación" }),
    ).toBeEnabled();
    await expect(page.getByLabel("Identificador del recurso")).toHaveValue(
      "borrador-conexion",
    );
    await expect(row).toContainText("Completado", { timeout: 10000 });
    await page.goto(savedURL);
    await expect(
      page.getByRole("heading", { name: "prueba-app" }),
    ).toBeVisible();
    const tokenRow = page
      .getByRole("listitem")
      .filter({ has: page.locator("strong", { hasText: /^TOKEN$/ }) });
    await expect(tokenRow.getByText("Secreto guardado")).toBeVisible();
    await expect(page.getByText("test-secret-value")).toHaveCount(0);
    await page.getByRole("button", { name: "Eliminar TOKEN" }).click();
    await page.getByRole("button", { name: "Confirmar eliminación" }).click();
    await expect(tokenRow).toHaveCount(0);
  } finally {
    if (server) await stop(server);
    if (worker) await stop(worker);
    rmSync(dir, { recursive: true, force: true });
  }
});

test("persistent worker projects: create, edit, delete and undo", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const stack = await startStack();
  const { base } = stack;
  const api = `${base}/api/v1`;
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(`${api}${path}`, { data });
    expect(response.ok()).toBeTruthy();
    return response.json();
  };
  const digest = (n: string) =>
    `ghcr.io/example/${n}@sha256:${n.charCodeAt(0).toString(16).padStart(2, "0").repeat(32)}`;
  const nav = page.locator("#sidebar-navigation");
  const openNav = async () => {
    const toggle = page.getByRole("button", { name: "Abrir navegación" });
    if (await toggle.isVisible()) await toggle.click();
  };
  const closeNav = async () => {
    if ((page.viewportSize()?.width ?? 1000) <= 600)
      await page.getByRole("button", { name: "Cerrar navegación" }).click();
  };
  const projectLink = async (name: string | RegExp) => {
    await openNav();
    return nav.getByRole("link", { name });
  };
  const rows = page.locator(".app-row");
  const assignments = async () =>
    Object.fromEntries(
      (
        (await (
          await page.request.get(`${api}/worker/applications`)
        ).json()) as { name: string; project_id: string | null }[]
      ).map((app) => [app.name, app.project_id]),
    );
  try {
    const apps: { id: string; name: string }[] = [];
    for (const name of ["alfa-app", "beta-app", "gamma-app"])
      apps.push(
        await post("/worker/applications", {
          name,
          image: digest(name),
          port: 8080,
        }),
      );
    const [alfa, beta] = apps;

    // Create from the sidebar, with inline validation and the colour radio group.
    await page.goto(base);
    await openNav();
    await nav.getByRole("button", { name: "Nuevo proyecto" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Crear proyecto" }).click();
    await expect(dialog.getByText("Usa de 1 a 40 caracteres.")).toBeVisible();
    await expect(dialog.getByLabel("Nombre", { exact: true })).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await dialog.getByLabel("Nombre", { exact: true }).fill("Plataforma");
    await dialog.getByRole("radio", { name: "Verde" }).click();
    await expect(dialog.getByRole("radio", { name: "Verde" })).toBeChecked();
    await dialog.getByRole("button", { name: "Crear proyecto" }).click();
    await expect(page).toHaveURL(/\/projects\/[^/]+$/);
    await expect(
      page.getByRole("heading", { name: "Plataforma", level: 1 }),
    ).toBeVisible();
    await expect(
      page.getByText("Este proyecto no tiene aplicaciones"),
    ).toBeVisible();
    const projectID = new URL(page.url()).pathname.split("/")[2];
    expect((await violations(page))).toEqual([]);

    // Duplicate names are reported translated, inside the dialog.
    await openNav();
    await nav.getByRole("button", { name: "Nuevo proyecto" }).click();
    await dialog.getByLabel("Nombre", { exact: true }).fill("plataforma");
    await dialog.getByRole("button", { name: "Crear proyecto" }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      "Ya existe un proyecto con ese nombre.",
    );
    await dialog.getByRole("button", { name: "Cancelar" }).click();

    await post("/worker/applications/assign", {
      project_id: projectID,
      application_ids: [alfa.id, beta.id],
    });
    await page.reload();
    await expect(rows).toHaveCount(2);
    await expect(
      page.getByRole("group", { name: "Resumen por estado" }),
    ).toContainText("2");
    await expect(await projectLink(/Plataforma/)).toContainText("2");
    await closeNav();

    // Edit, with a stale version reported and recoverable.
    await page.getByRole("button", { name: "Editar", exact: true }).click();
    await dialog.getByLabel("Nombre", { exact: true }).fill("Plataforma base");
    const project = (await (
      await page.request.get(`${api}/worker/projects`)
    ).json()) as { id: string; name: string; color: string; version: number }[];
    const current = project.find((p) => p.id === projectID)!;
    const bump = await page.request.patch(
      `${api}/worker/projects/${projectID}`,
      {
        data: { version: current.version, name: "Plataforma", color: "green" },
      },
    );
    expect(bump.ok()).toBeTruthy();
    await dialog.getByRole("button", { name: "Guardar cambios" }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      "Otra persona ha modificado esto",
    );
    await dialog.getByRole("button", { name: "Recargar proyecto" }).click();
    await expect(dialog.getByRole("alert")).toHaveCount(0);
    await expect(dialog.getByLabel("Nombre", { exact: true })).toHaveValue(
      "Plataforma base",
    );
    await dialog.getByRole("button", { name: "Guardar cambios" }).click();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "Plataforma base", level: 1 }),
    ).toBeVisible();
    await expect(
      page.locator("main").getByText("Plataforma base").first(),
    ).toBeVisible();
    await expect(page.locator(".breadcrumb-trail")).toContainText(
      "Plataforma base",
    );

    // Delete and undo: the project and its applications come back.
    await page.getByRole("button", { name: "Eliminar", exact: true }).click();
    await expect(dialog).toContainText(
      "Sus 2 aplicaciones pasarán a «Sin proyecto»",
    );
    await dialog.getByRole("button", { name: "Eliminar proyecto" }).click();
    await expect(page).toHaveURL(`${base}/`);
    const toast = page.getByRole("region", { name: "Notificaciones" });
    await expect(toast).toContainText("Proyecto eliminado");
    await expect(
      page.getByRole("region", { name: "Sin proyecto" }),
    ).toContainText("alfa-app");
    await page.screenshot({
      path: test.info().outputPath("project-deleted.png"),
      animations: "disabled",
    });
    await toast.getByRole("button", { name: "Deshacer" }).click();
    await expect(toast).toContainText("Proyecto restaurado");
    expect(Object.values(await assignments()).filter(Boolean)).toHaveLength(2);
    await (await projectLink(/Plataforma base/)).click();
    await expect(rows).toHaveCount(2);

    // Delete again, move one application elsewhere, then undo: it is not pulled back.
    await page.getByRole("button", { name: "Eliminar", exact: true }).click();
    await dialog.getByRole("button", { name: "Eliminar proyecto" }).click();
    await expect(page).toHaveURL(`${base}/`);
    await expect(toast).toContainText("Proyecto eliminado");
    const other = await post("/worker/projects", {
      name: "Otro",
      color: "red",
    });
    await post("/worker/applications/assign", {
      project_id: other.id,
      application_ids: [alfa.id],
    });
    await toast.getByRole("button", { name: "Deshacer" }).click();
    await expect(toast).toContainText("Proyecto restaurado");
    await (await projectLink(/Plataforma base/)).click();
    await expect(rows).toHaveCount(1);
    await expect(rows.first()).toContainText("beta-app");
    const final = await assignments();
    expect(final["alfa-app"]).toBe(other.id);
    expect(final["gamma-app"]).toBeNull();

    // A project that does not exist offers a way back.
    await page.goto(`${base}/projects/inexistente`);
    await expect(page.getByText("Proyecto no encontrado")).toBeVisible();
    await page.getByRole("link", { name: "Volver a aplicaciones" }).click();
    await expect(page).toHaveURL(`${base}/`);
  } finally {
    await stack.close();
  }
});

test("persistent worker settings: port edit, version conflict and delete", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const stack = await startStack();
  const api = `${stack.base}/api/v1`;
  const image = `ghcr.io/example/ajustes@sha256:${"ab".repeat(32)}`;
  try {
    const created = await page.request.post(`${api}/worker/applications`, {
      data: { name: "ajustes-app", image, port: 8080 },
    });
    expect(created.ok()).toBeTruthy();
    const app = (await created.json()) as { id: string; version: number };
    const settingsURL = `${stack.base}/apps/definition/${app.id}/settings`;

    await page.goto(settingsURL);
    await expect(
      page.getByRole("heading", { name: "ajustes-app", level: 1 }),
    ).toBeVisible();
    await expect(page.getByRole("link", { name: "Ajustes" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    const port = page.getByRole("spinbutton", { name: "Puerto" });
    const save = page.getByRole("button", { name: "Guardar cambios" });
    await expect(page.getByLabel("Imagen por digest")).toBeEnabled();
    await expect(save).toBeDisabled();
    expect((await violations(page))).toEqual([]);

    // Inline validation, then a successful save.
    await port.fill("70000");
    await save.click();
    await expect(
      page.getByText("Indica un puerto entre 1 y 65535."),
    ).toBeVisible();
    await port.fill("9090");
    await save.click();
    await expect(page.getByText("Ajustes guardados")).toBeVisible();
    await expect(save).toBeDisabled();
    expect(
      (
        (await (
          await page.request.get(`${api}/worker/applications/${app.id}`)
        ).json()) as { port: number }
      ).port,
    ).toBe(9090);

    // A direct change makes the loaded version stale: the conflict is reported and "Recargar" resets the form.
    const current = (await (
      await page.request.get(`${api}/worker/applications/${app.id}`)
    ).json()) as { version: number };
    const direct = await page.request.patch(
      `${api}/worker/applications/${app.id}`,
      { data: { version: current.version, image, port: 7070 } },
    );
    expect(direct.ok()).toBeTruthy();
    await port.fill("6060");
    await save.click();
    await expect(
      page
        .getByRole("alert")
        .filter({ hasText: "Otra persona ha modificado esto" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Recargar" }).click();
    await expect(port).toHaveValue("7070");
    await expect(save).toBeDisabled();

    // An unsaved draft is guarded when leaving the definition.
    await port.fill("5050");
    await page.getByRole("link", { name: "Resumen" }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.getByRole("link", { name: "Ajustes" }).click();
    await expect(port).toHaveValue("5050");
    const toggle = page.getByRole("button", { name: "Abrir navegación" });
    if (await toggle.isVisible()) await toggle.click();
    await page
      .locator("#sidebar-navigation")
      .getByRole("link", { name: /^Aplicaciones/ })
      .click();
    const discard = page.getByRole("dialog");
    await expect(discard.getByText("Hay cambios sin guardar")).toBeVisible();
    await discard.getByRole("button", { name: "Descartar borrador" }).click();
    await expect(
      page.getByRole("heading", { name: "ajustes-app", level: 1 }),
    ).toHaveCount(0);

    // Delete: requires typing the name; afterwards it is gone.
    await page.goto(settingsURL);
    await page.getByRole("button", { name: "Eliminar definición" }).click();
    const dialog = page.getByRole("dialog");
    const confirm = dialog.getByRole("button", { name: "Eliminar definición" });
    await expect(confirm).toBeDisabled();
    await dialog.getByLabel(/Escribe «ajustes-app»/).fill("ajustes-app");
    await confirm.click();
    await expect(page).toHaveURL(`${stack.base}/`);
    await expect(
      page.getByText("Definición «ajustes-app» eliminada"),
    ).toBeVisible();
    await expect(page.getByText("ajustes-app", { exact: true })).toHaveCount(0);
    expect(
      (await page.request.get(`${api}/worker/config/${app.id}`)).status(),
    ).toBe(404);
  } finally {
    await stack.close();
  }
});

test("persistent worker selection: move definitions in bulk", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const stack = await startStack();
  const { base } = stack;
  const api = `${base}/api/v1`;
  const post = async (path: string, data: unknown) => {
    const response = await page.request.post(`${api}${path}`, { data });
    expect(response.ok()).toBeTruthy();
    return response.json();
  };
  const digest = (n: string) =>
    `ghcr.io/example/${n}@sha256:${n.charCodeAt(0).toString(16).padStart(2, "0").repeat(32)}`;
  try {
    const project = await post("/worker/projects", {
      name: "Destino",
      color: "green",
    });
    for (const name of ["alfa-app", "beta-app", "gamma-app"])
      await post("/worker/applications", {
        name,
        image: digest(name),
        port: 8080,
      });

    await page.goto(base);
    await expect(page.locator(".app-row")).toHaveCount(6);
    const select = page.getByRole("button", { name: "Seleccionar" });
    await select.click();
    await expect(select).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator("a.app-row")).toHaveCount(0);
    await expect(page.locator("div.app-row")).toHaveCount(3);
    await expect(page.getByRole("checkbox")).toHaveCount(3);
    const bar = page.getByRole("group", { name: "Acciones de selección" });
    await expect(bar).toContainText("0 seleccionadas");
    expect((await violations(page))).toEqual([]);

    // Keyboard: arrows move between rows, Space marks.
    const alfa = page.getByRole("checkbox", { name: "alfa-app" });
    const beta = page.getByRole("checkbox", { name: "beta-app" });
    await alfa.focus();
    await page.keyboard.press("Space");
    await expect(alfa).toBeChecked();
    await page.keyboard.press("ArrowDown");
    await expect(beta).toBeFocused();
    await page.keyboard.press("Space");
    await expect(bar).toContainText("2 seleccionadas");
    // Mouse: toggle gamma on and off.
    const gamma = page.getByRole("checkbox", { name: "gamma-app" });
    await page.locator("label.app-row", { hasText: "gamma-app" }).click();
    await expect(bar).toContainText("3 seleccionadas");
    await gamma.uncheck();
    await expect(bar).toContainText("2 seleccionadas");
    expect((await violations(page))).toEqual([]);

    // Escape leaves selection mode and clears it.
    await page.keyboard.press("Escape");
    await expect(page.locator("a.app-row")).toHaveCount(6);
    await select.click();
    await expect(bar).toContainText("0 seleccionadas");
    await alfa.check();
    await beta.check();

    await bar.getByRole("button", { name: "Mover a proyecto…" }).click();
    await bar
      .getByLabel("Proyecto de destino")
      .selectOption({ label: "Destino" });
    await bar.getByRole("button", { name: "Mover", exact: true }).click();
    await expect(
      page.getByRole("region", { name: "Notificaciones" }),
    ).toContainText("2 aplicaciones movidas a Destino");
    await expect(bar).toHaveCount(0);
    await expect(page.getByRole("region", { name: "Destino" })).toContainText(
      "alfa-app",
    );
    await expect(page.getByRole("region", { name: "Destino" })).toContainText(
      "beta-app",
    );
    await expect(
      page.getByRole("region", { name: "Sin proyecto" }),
    ).toContainText("gamma-app");

    // On the project page, moving out removes them from the view.
    await page.goto(`${base}/projects/${project.id}`);
    await expect(page.locator(".app-row")).toHaveCount(2);
    await page.getByRole("button", { name: "Seleccionar" }).click();
    await alfa.check();
    await bar.getByRole("button", { name: "Mover a proyecto…" }).click();
    await bar
      .getByLabel("Proyecto de destino")
      .selectOption({ label: "Sin proyecto" });
    await bar.getByRole("button", { name: "Mover", exact: true }).click();
    await expect(
      page.getByRole("region", { name: "Notificaciones" }),
    ).toContainText("1 aplicación movida a Sin proyecto");
    await expect(page.locator(".app-row")).toHaveCount(1);
    await expect(page.locator(".app-row")).toContainText("beta-app");
  } finally {
    await stack.close();
  }
});

test("persistent worker new definition from a project page lands in that project", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const stack = await startStack();
  const { base } = stack;
  const api = `${base}/api/v1`;
  try {
    const created = await page.request.post(`${api}/worker/projects`, {
      data: { name: "Destino", color: "green" },
    });
    expect(created.ok()).toBeTruthy();
    const project = (await created.json()) as { id: string };

    await page.goto(`${base}/projects/${project.id}`);
    await page
      .getByRole("main")
      .getByRole("link", { name: "Nueva aplicación" })
      .first()
      .click();
    await expect(page).toHaveURL(
      new RegExp(`/apps/new\\?kind=definition&project=${project.id}$`),
    );
    await expect(page.getByLabel("Proyecto", { exact: true })).toHaveValue(
      project.id,
    );
    await page.getByLabel("Nombre", { exact: true }).fill("nueva-app");
    await page
      .getByLabel("Imagen por digest")
      .fill(`ghcr.io/example/nueva@sha256:${"a".repeat(64)}`);
    await page.getByRole("button", { name: "Guardar definición" }).click();
    // No unsaved-draft prompt on the post-create navigation.
    await expect(page).toHaveURL(/\/apps\/definition\/[^/]+$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);

    await page.goto(`${base}/projects/${project.id}`);
    await expect(page.locator(".app-row")).toHaveCount(1);
    await expect(page.locator(".app-row")).toContainText("nueva-app");

    // Unknown project ids are ignored.
    await page.goto(`${base}/apps/new?kind=definition&project=desconocido`);
    await expect(page.getByLabel("Proyecto", { exact: true })).toHaveValue("");
  } finally {
    await stack.close();
  }
});

test("persistent worker history: restore a secret without exposing it, and report conflicts", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const stack = await startStack();
  const api = `${stack.base}/api/v1`;
  const secretA = "valor-secreto-alfa-7f3a91";
  const secretB = "valor-secreto-beta-c4d208";
  const bodies: string[] = [];
  page.on("response", async (response) => {
    if (
      !response.url().includes("/api/v1/") ||
      !(response.headers()["content-type"] ?? "").includes("json")
    )
      return;
    try {
      bodies.push(await response.text());
    } catch {
      /* Redirects and aborted requests have no body. */
    }
  });
  const put = async (id: string, value: string) => {
    const current = (await (
      await page.request.get(`${api}/worker/config/${id}`)
    ).json()) as { revision: number };
    const response = await page.request.put(
      `${api}/worker/config/${id}/variables/API_KEY`,
      { data: { revision: current.revision, value, secret: true } },
    );
    expect(response.ok()).toBeTruthy();
    bodies.push(await response.text());
  };
  try {
    const created = await page.request.post(`${api}/worker/applications`, {
      data: {
        name: "historial-app",
        image: `ghcr.io/example/historial@sha256:${"cd".repeat(32)}`,
        port: 8080,
      },
    });
    const app = (await created.json()) as { id: string };
    await page.goto(`${stack.base}/apps/definition/${app.id}/history`);
    await expect(page.getByText("Todavía no hay revisiones")).toBeVisible();

    await put(app.id, secretA);
    await put(app.id, secretB);
    await page.reload();
    await expect(page.getByRole("link", { name: "Historial" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    const rev2 = page
      .getByRole("listitem")
      .filter({ has: page.getByRole("heading", { name: "Revisión 2" }) });
    await expect(rev2).toContainText("API_KEY modificada");
    await expect(rev2.getByText("secreto:")).toBeAttached();
    const rev1 = page
      .getByRole("listitem")
      .filter({ has: page.getByRole("heading", { name: "Revisión 1" }) });
    await expect(rev1).toContainText("Revisión más antigua conservada");
    await expect(rev1.getByRole("button", { name: /Restaurar/ })).toHaveCount(
      0,
    );
    await expect(rev2.locator("time")).toHaveAttribute("datetime", /.+/);
    expect((await violations(page))).toEqual([]);

    // Restore: the dialog explains the effect and never shows the value.
    await rev2
      .getByRole("button", { name: "Restaurar valor anterior de API_KEY" })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Se creará una revisión nueva");
    await expect(dialog).toContainText("El valor no se muestra.");
    await expect(dialog).toContainText(
      "Las revisiones antiguas pueden conservar secretos.",
    );
    await expect(dialog).not.toContainText("No modifica el servicio existente");
    await expect(dialog).toHaveCSS("opacity", "1");
    expect((await violations(page))).toEqual([]);
    await dialog.getByRole("button", { name: "Restaurar valor" }).click();
    await expect(
      page.getByRole("region", { name: "Notificaciones" }),
    ).toContainText("API_KEY restaurada");
    await expect(
      page.getByRole("heading", { name: "Revisión 3" }),
    ).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);

    // A change made after the dialog was opened is reported; nothing is retried automatically.
    const rev3 = page
      .getByRole("listitem")
      .filter({ has: page.getByRole("heading", { name: "Revisión 3" }) });
    await rev3
      .getByRole("button", { name: "Restaurar valor anterior de API_KEY" })
      .click();
    await put(app.id, "valor-secreto-gamma-19be55");
    await dialog.getByRole("button", { name: "Restaurar valor" }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      "La configuración ha cambiado",
    );
    await expect(
      dialog.getByRole("button", { name: "Restaurar valor" }),
    ).toHaveCount(0);
    expect(
      (
        (await (
          await page.request.get(`${api}/worker/config/${app.id}`)
        ).json()) as { revision: number }
      ).revision,
    ).toBe(4);
    await dialog.getByRole("button", { name: "Actualizar" }).click();
    await expect(page.locator("h3", { hasText: "Revisión 4" })).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "Restaurar valor" }),
    ).toBeVisible();
    await dialog.getByRole("button", { name: "Cancelar" }).click();

    for (const secret of [secretA, secretB, "valor-secreto-gamma-19be55"]) {
      expect(await page.content()).not.toContain(secret);
      expect(bodies.length).toBeGreaterThan(5);
      expect(bodies.some((body) => body.includes(secret))).toBe(false);
    }
  } finally {
    await stack.close();
  }
});

const copy = {
  es: {
    newProject: "Nuevo proyecto", create: "Crear proyecto", cancel: "Cancelar",
    delete: "Eliminar", deleteProject: "Eliminar proyecto", notifications: "Notificaciones", undo: "Deshacer",
    select: "Seleccionar", bar: "Acciones de selección", move: "Mover a proyecto…",
    settings: "Ajustes", deleteDefinition: "Eliminar definición", history: "Historial",
    restore: "Restaurar valor anterior de API_KEY", name: "Nombre", navOpen: "Abrir navegación",
  },
  en: {
    newProject: "New project", create: "Create project", cancel: "Cancel",
    delete: "Delete", deleteProject: "Delete project", notifications: "Notifications", undo: "Undo",
    select: "Select", bar: "Selection actions", move: "Move to project…",
    settings: "Settings", deleteDefinition: "Delete definition", history: "History",
    restore: "Restore previous value of API_KEY", name: "Name", navOpen: "Open navigation",
  },
} as const;

for (const locale of ["es", "en"] as const) {
  test(`persistent worker coverage (${locale}): project, dialogs, selection, settings, history and toast`, async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const c = copy[locale];
    const stack = await startStack();
    const { base } = stack;
    const api = `${base}/api/v1`;
    const post = async (path: string, data: unknown) => {
      const response = await page.request.post(`${api}${path}`, { data });
      expect(response.ok()).toBeTruthy();
      return response.json();
    };
    // Every screen must be free of axe violations; English must also leave no Spanish text behind.
    const check = async () => {
      expect(await violations(page)).toEqual([]);
      if (locale === "en") await expectEnglish(page);
    };
    try {
      await page.addInitScript(
        (l) => localStorage.setItem("darsena.locale", l),
        locale,
      );
      const project = await post("/worker/projects", { name: "Plataforma", color: "teal" });
      await post("/worker/projects", { name: "Destino", color: "green" });
      const apps: { id: string }[] = [];
      for (const name of ["alfa-app", "beta-app"])
        apps.push(
          await post("/worker/applications", {
            name,
            image: `ghcr.io/example/${name}@sha256:${"ab".repeat(32)}`,
            port: 8080,
          }),
        );
      await post("/worker/applications/assign", {
        project_id: project.id,
        application_ids: [apps[0].id],
      });
      for (const value of ["valor-uno", "valor-dos"]) {
        const current = (await (await page.request.get(`${api}/worker/config/${apps[0].id}`)).json()) as { revision: number };
        const response = await page.request.put(
          `${api}/worker/config/${apps[0].id}/variables/API_KEY`,
          { data: { revision: current.revision, value, secret: true } },
        );
        expect(response.ok()).toBeTruthy();
      }

      // List with groups, then the project page.
      await page.goto(base);
      await expect(page.locator(".app-row").first()).toBeVisible();
      await check();
      await page.goto(`${base}/projects/${project.id}`);
      await expect(page.getByRole("heading", { name: "Plataforma", level: 1 })).toBeVisible();
      await check();

      // New-project dialog (with its validation error) and delete-project dialog.
      const toggle = page.getByRole("button", { name: c.navOpen });
      if (await toggle.isVisible()) await toggle.click();
      await page.locator("#sidebar-navigation").getByRole("button", { name: c.newProject }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("button", { name: c.create }).click();
      await expect(dialog.getByLabel(c.name, { exact: true })).toHaveAttribute("aria-invalid", "true");
      await check();
      await dialog.getByRole("button", { name: c.cancel }).click();
      await expect(dialog).toHaveCount(0);
      await page.getByRole("button", { name: c.delete, exact: true }).click();
      await expect(dialog).toBeVisible();
      await check();
      await dialog.getByRole("button", { name: c.deleteProject }).click();
      const toast = page.getByRole("region", { name: c.notifications });
      await expect(toast.getByRole("button", { name: c.undo })).toBeVisible();
      await check();
      await toast.getByRole("button", { name: c.undo }).click();
      await expect(toast.getByRole("button", { name: c.undo })).toHaveCount(0);

      // Selection mode with its action bar.
      await page.goto(base);
      await page.getByRole("button", { name: c.select, exact: true }).click();
      await page.getByRole("checkbox", { name: "beta-app" }).check();
      await expect(page.getByRole("group", { name: c.bar })).toBeVisible();
      await check();
      await page.keyboard.press("Escape");

      // Definition settings with its delete dialog.
      await page.goto(`${base}/apps/definition/${apps[1].id}/settings`);
      await expect(page.getByRole("button", { name: c.deleteDefinition })).toBeVisible();
      await check();
      await page.getByRole("button", { name: c.deleteDefinition }).click();
      await expect(dialog).toBeVisible();
      await check();
      await page.keyboard.press("Escape");

      // History and the restore dialog.
      await page.goto(`${base}/apps/definition/${apps[0].id}/history`);
      await page.getByRole("button", { name: c.restore }).click();
      await expect(dialog).toBeVisible();
      await check();
    } finally {
      await stack.close();
    }
  });
}
