import type { useIsolatedService } from "./useIsolatedService";

export type IsolatedServiceState = ReturnType<typeof useIsolatedService>;

/** A create attempt was sent but no service has been observed: only a status query is safe. */
export function createUncertain(svc: IsolatedServiceState) {
  return svc.attempted && !svc.service.data && (svc.create.isError || svc.missing);
}
