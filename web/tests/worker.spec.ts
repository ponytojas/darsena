import { test, expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
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

async function openListIn(page: Page) {
  const toggle = page.getByRole("button", { name: "Abrir navegación" });
  if (await toggle.isVisible()) await toggle.click();
  await page.locator("#sidebar-navigation").getByRole("link", { name: /^Aplicaciones/ }).click();
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
    await page.screenshot({ path: info.outputPath("registered-unknown.png"), fullPage: true, animations: "disabled" });
    const definitionForm = page.getByRole("region", { name: "Definición desde imagen" });
    // Type choice: keyboard selection, and ?kind= survives a reload and Back.
    await page.goto(`${base}/apps/new`);
    await expect(page.getByRole("heading", { name: "Nueva aplicación", level: 1 })).toBeVisible();
    await expect(page.getByRole("button", { name: "Continuar", exact: true })).toBeDisabled();
    await page.getByRole("radio", { name: /Ejemplo simulado/ }).focus();
    await page.keyboard.press("ArrowRight");
    await expect(page.getByRole("radio", { name: /Definición desde imagen/ })).toBeChecked();
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
    await definitionForm.getByLabel("Nombre", { exact: true }).fill("Mal Nombre");
    await definitionForm.getByLabel("Imagen por digest").fill("ghcr.io/example/app:latest");
    await definitionForm.getByRole("button", { name: "Guardar definición" }).click();
    await expect(definitionForm.getByText(/Usa de 2 a 40 caracteres/)).toBeVisible();
    await expect(definitionForm.getByText(/Indica la imagen con su digest/)).toBeVisible();
    await expect(definitionForm.getByLabel("Nombre", { exact: true })).toBeFocused();
    // Unsaved changes are protected when leaving.
    await openListIn(page);
    await expect(page.getByRole("dialog")).toContainText("Hay cambios sin guardar");
    await page.getByRole("button", { name: "Seguir editando" }).click();
    await expect(page).toHaveURL(/\/apps\/new\?kind=definition$/);
    await definitionForm.getByLabel("Nombre", { exact: true }).fill("");
    await definitionForm.getByLabel("Imagen por digest").fill("");
    await page.screenshot({ path: info.outputPath("creation-form.png"), fullPage: true, animations: "disabled" });
    await definitionForm.getByLabel("Nombre", { exact: true }).fill("prueba-app");
    await definitionForm
      .getByLabel("Imagen por digest")
      .fill(`ghcr.io/example/app@sha256:${"a".repeat(64)}`);
    await definitionForm.getByRole("button", { name: "Guardar definición" }).click();
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
      await route.fulfill({ response, json: { ...capabilities, worker: { ...capabilities.worker, status: "unavailable" }, state: "ready", vault: "unavailable" } });
    });
    await expect(page.getByText("Almacén cifrado no disponible.", { exact: false })).toBeVisible({ timeout: 20000 });
    await expect(page.getByText("Aunque el estado de trabajos está disponible", { exact: false })).toBeVisible();
    await expect(page.getByRole("button", { name: "Guardar variable" })).toBeDisabled();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue("test-secret-value");
    await page.unroute("**/api/v1/capabilities");
    await page.getByRole("button", { name: "Actualizar estado" }).click();
    await expect(page.getByRole("button", { name: "Guardar variable" })).toBeEnabled();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue("test-secret-value");
    await page.route("**/api/v1/capabilities", (route) => route.abort());
    await expect(page.getByRole("button", { name: "Guardar variable" })).toBeDisabled({ timeout: 20000 });
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue("test-secret-value");
    await page.unroute("**/api/v1/capabilities");
    await page.getByRole("button", { name: "Actualizar estado" }).click();
    await expect(page.getByRole("button", { name: "Guardar variable" })).toBeEnabled();
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
    await page.route("**/api/v1/worker/config/**/variables/PUBLIC_TEST", async (route) => {
      if (route.request().method() === "PUT" && conflictOnce) {
        conflictOnce = false;
        await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "revision conflict" }) });
      } else await route.continue();
    });
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue("private-test-value");
    await expect(page.getByRole("button", { name: "Guardar variable" })).toBeDisabled();
    await page.getByRole("button", { name: "Actualizar configuración" }).click();
    await expect(page.getByRole("button", { name: "Guardar variable" })).toBeEnabled();
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await expect(page.getByText("private-test-value")).toBeVisible();
    await page.getByRole("button", { name: "Editar PUBLIC_TEST" }).click();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue("private-test-value");
    await page.screenshot({ path: info.outputPath("editor-public.png"), fullPage: true, animations: "disabled" });
    await page.getByRole("button", { name: "Cancelar" }).click();
    // Adding an existing name explicitly announces replacement and retains its type.
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("TOKEN");
    await expect(page.getByRole("button", { name: "Sustituir variable" })).toBeVisible();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveAttribute("type", "password");
    await page.getByRole("button", { name: "Cancelar" }).click();
    await page.getByRole("button", { name: "Descartar borrador" }).click();
    await expect(page.getByLabel("Valor", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page.screenshot({
      path: info.outputPath("variables.png"),
      fullPage: true,
      animations: "disabled",
    });
    const savedURL = page.url();

    // A response for A must not close B's new draft or invalidate B's config.
    await page.goto(`${base}/apps/new?kind=definition`);
    await definitionForm.getByLabel("Nombre", { exact: true }).fill("segunda-app");
    await definitionForm.getByLabel("Imagen por digest").fill(`ghcr.io/example/second@sha256:${"b".repeat(64)}`);
    await definitionForm.getByRole("button", { name: "Guardar definición" }).click();
    await expect(page.getByRole("heading", { name: "segunda-app" })).toBeVisible();
    const secondID = new URL(page.url()).pathname.split("/")[3];
    const openList = () => openListIn(page);
    await openList();
    await page.getByRole("link", { name: /prueba-app/ }).click();
    await page.getByRole("link", { name: "Variables", exact: true }).click();
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("LATE_TEST");
    await page.getByLabel("Valor", { exact: true }).fill("test-late-value");
    let releaseResponse!: () => void;
    const responseGate = new Promise<void>((resolve) => { releaseResponse = resolve; });
    let requestStarted!: () => void;
    const requestSeen = new Promise<void>((resolve) => { requestStarted = resolve; });
    await page.route("**/api/v1/worker/config/**/variables/LATE_TEST", async (route) => {
      const response = await route.fetch();
      requestStarted();
      await responseGate;
      await route.fulfill({ response });
    });
    await page.getByRole("button", { name: "Guardar variable" }).click();
    await requestSeen;
    await openList();
    await page.getByRole("button", { name: "Descartar borrador" }).click();
    await expect(page.getByRole("link", { name: /segunda-app/ })).toBeVisible();
    await page.getByRole("link", { name: /segunda-app/ }).click();
    await expect(page.getByRole("heading", { name: "segunda-app" })).toBeVisible();
    await page.getByRole("button", { name: "Añadir variable" }).click();
    await page.getByLabel("Nombre de variable").fill("DRAFT_B");
    await page.getByLabel("Valor", { exact: true }).fill("test-draft-b");
    let secondFetches = 0;
    page.on("request", (request) => {
      if (request.method() === "GET" && request.url().includes(`/worker/config/${secondID}`)) secondFetches++;
    });
    const lateResponse = page.waitForResponse((response) => response.url().includes("/variables/LATE_TEST"));
    releaseResponse();
    await lateResponse;
    await expect(page.getByLabel("Valor", { exact: true })).toHaveValue("test-draft-b");
    expect(secondFetches).toBe(0);
    await page.getByRole("button", { name: "Cancelar" }).click();
    await page.getByRole("button", { name: "Descartar borrador" }).click();
    await openList();
    await page.getByRole("link", { name: /prueba-app/ }).click();
    await page.getByRole("link", { name: "Variables", exact: true }).click();
    await expect(page.getByRole("listitem").locator("strong").getByText("LATE_TEST", { exact: true })).toBeVisible();

    await page.goto(`${base}/diagnostics`);
    await page.getByLabel("Identificador del recurso").fill("borrador-conexion");
    await stop(worker);
    worker = undefined;
    await expect(page.getByText("Worker temporalmente no disponible", { exact: false })).toBeVisible({ timeout: 20000 });
    await expect(page.getByLabel("Identificador del recurso")).toHaveValue("borrador-conexion");
    await expect(page.getByRole("button", { name: "Iniciar simulación" })).toBeDisabled();
    worker = await startWorker();
    await page.getByRole("button", { name: "Actualizar estado" }).click();
    await expect(page.getByRole("button", { name: "Iniciar simulación" })).toBeEnabled();
    await expect(page.getByLabel("Identificador del recurso")).toHaveValue("borrador-conexion");
    await expect(row).toContainText("Completado", { timeout: 10000 });
    await page.goto(savedURL);
    await expect(
      page.getByRole("heading", { name: "prueba-app" }),
    ).toBeVisible();
    const tokenRow = page.getByRole("listitem").filter({ has: page.locator("strong", { hasText: /^TOKEN$/ }) });
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
