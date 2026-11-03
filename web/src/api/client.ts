import createClient from "openapi-fetch";
import type { components, paths } from "./schema";
export type Application = components["schemas"]["Application"];
export type Operation = components["schemas"]["Operation"];
export type CreateInput = components["schemas"]["CreateInput"];
export type WorkerJob = components["schemas"]["WorkerJob"];
export type Scenario = components["schemas"]["Scenario"];
export type WorkerApplication = components["schemas"]["WorkerApplicationSummary"];
export type Project = components["schemas"]["WorkerProject"];
export type ProjectInput = components["schemas"]["WorkerProjectInput"];
export type HistoryEntry = components["schemas"]["ConfigHistoryEntry"];
export type ImageCheck = components["schemas"]["ImageCheck"];
/** Stable identifiers, not UI text: they are translated at render time (see i18n/errors.ts). */
export const clientErrors = {
  loginFailed: "client:login_failed",
  invalidSession: "client:invalid_session",
  serviceMissing: "client:service_missing",
  serviceConflict: "client:service_conflict",
  serviceUnverified: "client:service_unverified",
  noData: "client:no_data",
} as const;
const client = createClient<paths>({ baseUrl: "/api/v1" });
let sessionToken: string | null = null;
const sessionListeners = new Set<() => void>();
export function sessionSnapshot() { return sessionToken !== null; }
export function subscribeSession(listener: () => void) { sessionListeners.add(listener); return () => { sessionListeners.delete(listener); }; }
function setSession(token: string | null) { sessionToken = token; sessionListeners.forEach((listener) => listener()); }
client.use({
  onRequest({ request }) { if (sessionToken) request.headers.set("X-Darsena-CSRF", sessionToken); return request; },
  onResponse({ response }) { if (response.status === 401 && sessionToken) setSession(null); return response; },
});
export async function login(credential: string) {
  const response = await fetch("/api/v1/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ credential }) });
  if (!response.ok) throw new ApiError(clientErrors.loginFailed, response.status);
  const result: unknown = await response.json();
  if (!result || typeof result !== "object" || !("csrf" in result) || typeof result.csrf !== "string" || !result.csrf) throw new ApiError(clientErrors.invalidSession, response.status);
  setSession(result.csrf);
}
export async function logout() {
  try { await fetch("/api/v1/auth/logout", { method: "POST", headers: sessionToken ? { "X-Darsena-CSRF": sessionToken } : {} }); } finally { setSession(null); }
}
export type ExperimentalService = components["schemas"]["ExperimentalService"];
async function serviceRequest(id: string, action: "get" | "create" | "refresh" | "stop" | "remove", revision?: number): Promise<ExperimentalService> {
  const url = `/api/v1/worker/applications/${encodeURIComponent(id)}/service${action === "refresh" || action === "stop" || action === "remove" ? `/${action}` : ""}`;
  const headers: Record<string, string> = {};
  if (sessionToken) headers["X-Darsena-CSRF"] = sessionToken;
  if (action === "create" || action === "stop" || action === "remove") headers["Content-Type"] = "application/json";
  const body = action === "create" ? { revision, acknowledge_no_network: true } : action === "stop" ? { version: revision } : action === "remove" ? { version: revision, acknowledge_remove: true } : undefined;
  const response = await fetch(url, { method: action === "get" ? "GET" : "POST", headers, ...(body ? { body: JSON.stringify(body) } : {}) });
  if (response.status === 401) setSession(null);
  if (!response.ok) {
    let code: string | undefined;
    try { const error: unknown = await response.json(); if (error && typeof error === "object" && "code" in error && typeof error.code === "string") code = error.code; } catch { /* An unparseable error is never proof that an operation was not sent. */ }
    throw new ApiError(response.status === 404 ? clientErrors.serviceMissing : response.status === 409 ? clientErrors.serviceConflict : clientErrors.serviceUnverified, response.status, code);
  }
  return response.json() as Promise<ExperimentalService>;
}
export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  constructor(message: string, status: number, code?: string) {
    super(message);
    this.status = status;
    this.code = code;
    this.name = "ApiError";
  }
}
function unwrap<T>(result: { data?: T; error?: { error: string; code?: string }; response: Response }): T {
  if (result.error) throw new ApiError(result.error.error, result.response.status, result.error.code);
  if (result.data === undefined)
    throw new ApiError(clientErrors.noData, result.response.status);
  return result.data;
}
export const api = {
  service: (id: string) => serviceRequest(id, "get"),
  createService: (id: string, revision: number) => serviceRequest(id, "create", revision),
  refreshService: (id: string) => serviceRequest(id, "refresh"),
  stopService: (id: string, version: number) => serviceRequest(id, "stop", version),
  removeService: (id: string, version: number) => serviceRequest(id, "remove", version),
  status: async () => unwrap(await client.GET("/status")),
  capabilities: async () => unwrap(await client.GET("/capabilities", { cache: "no-store" })),
  workerHealth: async () => unwrap(await client.GET("/worker/health")),
  workerApps: async () => unwrap(await client.GET("/worker/applications")),
  workerAppCreate: async (
    body: components["schemas"]["WorkerApplicationInput"],
  ) => unwrap(await client.POST("/worker/applications", { body })),
  workerConfig: async (resource: string) =>
    unwrap(
      await client.GET("/worker/config/{resource}", {
        params: { path: { resource } },
      }),
    ),
  workerSetVariable: async (
    resource: string,
    name: string,
    body: components["schemas"]["SetConfigVariable"],
  ) =>
    unwrap(
      await client.PUT("/worker/config/{resource}/variables/{name}", {
        params: { path: { resource, name } },
        body,
      }),
    ),
  workerDeleteVariable: async (
    resource: string,
    name: string,
    revision: number,
  ) =>
    unwrap(
      await client.DELETE("/worker/config/{resource}/variables/{name}", {
        params: { path: { resource, name } },
        body: { revision },
      }),
    ),
  workerProjects: async () => unwrap(await client.GET("/worker/projects")),
  workerProjectCreate: async (body: ProjectInput) => unwrap(await client.POST("/worker/projects", { body })),
  workerProjectUpdate: async (id: string, body: components["schemas"]["WorkerProjectUpdate"]) =>
    unwrap(await client.PATCH("/worker/projects/{id}", { params: { path: { id } }, body })),
  workerProjectDelete: async (id: string) =>
    unwrap(await client.DELETE("/worker/projects/{id}", { params: { path: { id } } })),
  workerAssign: async (body: components["schemas"]["WorkerAssignInput"]) =>
    unwrap(await client.POST("/worker/applications/assign", { body })),
  workerImage: async (ref: string) => unwrap(await client.GET("/worker/image", { params: { query: { ref } } })),
  workerImagePull: async (reference: string) => unwrap(await client.POST("/worker/image/pull", { body: { reference } })),
  workerAppUpdate: async (id: string, body: components["schemas"]["WorkerApplicationUpdate"]) =>
    unwrap(await client.PATCH("/worker/applications/{id}", { params: { path: { id } }, body })),
  workerAppDelete: async (id: string) => {
    const result = await client.DELETE("/worker/applications/{id}", { params: { path: { id } } });
    if (result.error) throw new ApiError(result.error.error, result.response.status, result.error.code);
  },
  workerConfigHistory: async (resource: string) =>
    unwrap(await client.GET("/worker/config/{resource}/history", { params: { path: { resource } } })),
  workerRestoreVariable: async (resource: string, name: string, body: components["schemas"]["RestoreConfigVariable"]) =>
    unwrap(await client.POST("/worker/config/{resource}/variables/{name}/restore", { params: { path: { resource, name } }, body })),
  workerJobs: async () => unwrap(await client.GET("/worker/jobs")),
  workerCreate: async (resourceID: string, idempotencyKey: string) =>
    unwrap(
      await client.POST("/worker/jobs", {
        body: { resource_id: resourceID, idempotency_key: idempotencyKey },
      }),
    ),
  apps: async () => unwrap(await client.GET("/applications")),
  app: async (id: string) =>
    unwrap(
      await client.GET("/applications/{id}", { params: { path: { id } } }),
    ),
  operations: async () => unwrap(await client.GET("/operations")),
  operation: async (id: string) =>
    unwrap(await client.GET("/operations/{id}", { params: { path: { id } } })),
  create: async (body: CreateInput) =>
    unwrap(await client.POST("/applications", { body })),
  deploy: async (id: string, scenario: Scenario = "success") =>
    unwrap(
      await client.POST("/applications/{id}/deployments", {
        params: { path: { id } },
        body: { scenario },
      }),
    ),
};
export const terminal = (status: Operation["status"]) =>
  ["succeeded", "failed", "interrupted"].includes(status);
