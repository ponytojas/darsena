const actionKey = (id: string) => `darsena-service-action:${id}`;
const attemptKey = (id: string) => `darsena-service-attempt:${id}`;

// Storage access is best effort: callers keep in-memory state authoritative when it fails.
function read(key: string) { try { return sessionStorage.getItem(key) === "1"; } catch { return false; } }
function write(key: string) { try { sessionStorage.setItem(key, "1"); } catch { /* In-memory state still applies. */ } }
function remove(key: string) { try { sessionStorage.removeItem(key); } catch { /* In-memory state still applies. */ } }

export const readServiceAction = (id: string) => read(actionKey(id));
export const writeServiceAction = (id: string) => write(actionKey(id));
export const removeServiceAction = (id: string) => remove(actionKey(id));
export const readServiceAttempt = (id: string) => read(attemptKey(id));
export const writeServiceAttempt = (id: string) => write(attemptKey(id));
export const removeServiceAttempt = (id: string) => remove(attemptKey(id));

// Exit codes come only from an inspection and are not persisted by the server;
// the last one seen is remembered for this tab so a later read can show it.
const exitCodes = new Map<string, number>();
export function rememberExit(id: string, result: { observation?: string; exit_code?: number }) {
  if (result.observation === "stopped" && result.exit_code !== undefined) exitCodes.set(id, result.exit_code);
  else if (result.observation && result.observation !== "stopped") exitCodes.delete(id);
}
export const lastExit = (id: string) => exitCodes.get(id);
