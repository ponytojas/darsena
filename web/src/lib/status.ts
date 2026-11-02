import type { Application, ExperimentalService, Operation } from "@/api/client";

export type Health = "ok" | "progress" | "attention" | "idle";

/** Health of a demo example: an active operation wins, then failure, then the application status. */
export function exampleHealth(app: Application, operations: Operation[]): Health {
  const own = operations.filter((op) => op.application_id === app.id);
  if (own.some((op) => op.status === "queued" || op.status === "running")) return "progress";
  const latest = own.reduce<Operation | undefined>((a, b) => (!a || b.created_at > a.created_at ? b : a), undefined);
  if (app.status === "failed" || latest?.status === "failed" || latest?.status === "interrupted") return "attention";
  return app.status === "healthy" ? "ok" : "idle";
}

/** Health of a persistent definition from its isolated service; `uncertain` marks an unconfirmed action or attempt. */
export function definitionHealth(service: ExperimentalService | undefined, uncertain: boolean): Health {
  if (uncertain) return "attention";
  if (!service) return "idle";
  const state = service.observation || service.phase;
  if (state === "running") return "ok";
  if (state === "prepared" || state === "dispatched") return "progress";
  if (state === "conflict" || state === "unknown" || state === "unavailable") return "attention";
  return "idle";
}
