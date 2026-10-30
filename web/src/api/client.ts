import createClient from "openapi-fetch";
import type { components, paths } from "./schema";
export type Application = components["schemas"]["Application"];
export type Operation = components["schemas"]["Operation"];
export type CreateInput = components["schemas"]["CreateInput"];
export type Scenario = components["schemas"]["Scenario"];
const client = createClient<paths>({ baseUrl: "/api/v1" });
function unwrap<T>(result: { data?: T; error?: { error: string } }): T {
  if (result.error) throw new Error(result.error.error);
  if (result.data === undefined)
    throw new Error("El servidor no ha devuelto datos.");
  return result.data;
}
export const api = {
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
