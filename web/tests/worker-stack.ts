import { spawn, execFileSync, type ChildProcess } from "node:child_process";
import { mkdtempSync, chmodSync, rmSync, existsSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.listen(0, "127.0.0.1", () => {
      const address = s.address();
      if (!address || typeof address === "string") return reject(new Error("no address"));
      s.close(() => resolve(address.port));
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

/** A real demo worker and server on private temporary files and a free port; `close` stops only these processes. */
export async function startStack() {
  // Short socket paths are required on macOS.
  const dir = mkdtempSync(join(tmpdir().startsWith("/var/") ? "/tmp" : tmpdir(), "darsena-e2e-"));
  chmodSync(dir, 0o700);
  const binary = join(dir, "darsena");
  const socket = join(dir, "worker.sock");
  let worker: ChildProcess | undefined;
  let server: ChildProcess | undefined;
  const close = async () => {
    if (server) await stop(server);
    if (worker) await stop(worker);
    rmSync(dir, { recursive: true, force: true });
  };
  try {
    execFileSync("go", ["build", "-o", binary, "../cmd/darsena"], { timeout: 60000 });
    const keySource = join(dir, "keygen.go");
    const identity = join(dir, "identity.age");
    writeFileSync(
      keySource,
      `package main
import ("os"; "filippo.io/age")
func main(){ key,err:=age.GenerateX25519Identity();if err!=nil{panic(err)};if err=os.WriteFile(os.Args[1],[]byte("# test identity\\n"+key.String()+"\\n"),0600);err!=nil{panic(err)} }`,
    );
    execFileSync("go", ["run", keySource, identity], { timeout: 60000 });
    worker = spawn(binary, [
      "worker", "--demo", "--state", join(dir, "state.db"), "--socket", socket,
      "--config-db", join(dir, "config.db"), "--age-identity", identity,
    ]);
    await until(async () => existsSync(socket), "worker did not start");
    const listen = await freePort();
    server = spawn(binary, ["serve", "--demo", "--addr", `127.0.0.1:${listen}`, "--web", "dist", "--worker-socket", socket]);
    const base = `http://127.0.0.1:${listen}`;
    await until(async () => (await fetch(`${base}/api/v1/status`)).ok, "server did not start");
    await until(async () => ((await (await fetch(`${base}/api/v1/capabilities`)).json()) as { state: string }).state === "ready", "worker not ready");
    return { base, close };
  } catch (error) {
    await close();
    throw error;
  }
}
