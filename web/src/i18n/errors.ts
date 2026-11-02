import { clientErrors } from "@/api/client";
import type { Operation } from "@/api/client";
import type { Messages } from "./es";

type ErrorLike = Error & { status?: number; code?: string };

/** User-facing text for an error: by code, then by HTTP status. Server and worker text is never shown. */
export function errorMessage(error: Error | null | undefined, t: Messages): string {
  const { status, code } = (error ?? {}) as ErrorLike;
  switch (error?.message) {
    case clientErrors.loginFailed:
      return t.loginFailed;
    case clientErrors.invalidSession:
      return t.errorInvalidSession;
    case clientErrors.serviceMissing:
      return t.errorServiceMissing;
    case clientErrors.serviceConflict:
      return t.errorServiceConflict;
    case clientErrors.serviceUnverified:
      return t.errorServiceUnverified;
    case clientErrors.noData:
      return t.errorNoData;
  }
  if (!error) return t.connectionError;
  if (code && code in t.apiErrors) return t.apiErrors[code as keyof Messages["apiErrors"]];
  if (status === undefined) return t.connectionError;
  return status in t.statusErrors ? t.statusErrors[status as keyof Omit<Messages["statusErrors"], "other">] : t.statusErrors.other;
}

/** Operation failure text, translated by its stable code. */
export function operationError(op: Pick<Operation, "error" | "error_code">, t: Messages): string {
  const code = op.error_code;
  return code && code in t.apiErrors ? t.apiErrors[code as keyof Messages["apiErrors"]] : op.error;
}

/** Stage name translated by key; falls back to the server name when there is no known key. */
export function stepName(step: Operation["steps"][number], t: Messages): string {
  return step.key && step.key in t.stepNames ? t.stepNames[step.key as keyof Messages["stepNames"]] : step.name;
}
