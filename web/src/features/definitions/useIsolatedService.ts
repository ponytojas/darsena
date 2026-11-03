import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { useI18n } from "@/i18n/context";
import { readServiceAction, writeServiceAction, removeServiceAction, readServiceAttempt, writeServiceAttempt, removeServiceAttempt } from "@/lib/service-flags";

const preDispatch = new Set(["revision_conflict_before_dispatch", "image_missing", "image_incompatible", "invalid_image", "runtime_unavailable"]);

export function useIsolatedService(id: string, revision: number, { available, readable }: { available: boolean; readable: boolean }) {
  const cache = useQueryClient();
  const [acknowledged, setAcknowledged] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [actionUncertain, setActionUncertain] = useState(() => readServiceAction(id));
  const [revisionConflict, setRevisionConflict] = useState(false);
  const [attempted, setAttempted] = useState(() => readServiceAttempt(id));
  const service = useQuery({ queryKey: ["isolated-service", id], queryFn: () => api.service(id), retry: false, enabled: readable });
  const [noServiceConfirmed, setNoServiceConfirmed] = useState(false);
  const clearAttempt = () => { setAttempted(false); setAcknowledged(false); removeServiceAttempt(id); };
  const create = useMutation({ mutationFn: () => api.createService(id, revision), onMutate: () => { setRevisionConflict(false); setNoServiceConfirmed(false); setAttempted(true); writeServiceAttempt(id); }, onError: async (error) => {
    // Only these codes are rejected before anything is persisted or sent.
    if (!(error instanceof ApiError) || !error.code || !preDispatch.has(error.code)) return;
    // A concurrent request may have created the operation even if this request did not dispatch.
    try { await api.service(id); } catch (lookupError) {
      if (lookupError instanceof ApiError && lookupError.status === 404) {
        clearAttempt();
        if (error.code === "revision_conflict_before_dispatch") {
          setRevisionConflict(true);
          void cache.invalidateQueries({ queryKey: ["worker-config", id] });
        } else void cache.invalidateQueries({ queryKey: ["image-check"] });
      }
    }
  }, onSettled: () => {
    void cache.invalidateQueries({ queryKey: ["isolated-service", id] });
    void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    // Creating without variables saves an empty first revision.
    if (revision === 0) void cache.invalidateQueries({ queryKey: ["worker-config", id] });
  } });
  const clearActionUncertain = () => { setActionUncertain(false); removeServiceAction(id); };
  const refresh = useMutation({ mutationFn: () => api.refreshService(id), onSuccess: (result) => { cache.setQueryData(["isolated-service", id], result); clearActionUncertain(); void cache.invalidateQueries({ queryKey: ["worker-apps"] }); } });
  const action = useMutation({ mutationFn: ({ kind, version }: { kind: "stop" | "remove"; version: number }) => kind === "stop" ? api.stopService(id, version) : api.removeService(id, version), onMutate: () => { setActionUncertain(true); writeServiceAction(id); setConfirmRemove(false); }, onSuccess: (result) => { cache.setQueryData(["isolated-service", id], result); clearActionUncertain(); void cache.invalidateQueries({ queryKey: ["worker-apps"] }); }, onError: () => { void cache.invalidateQueries({ queryKey: ["isolated-service", id] }); void cache.invalidateQueries({ queryKey: ["worker-apps"] }); } });
  const missing = service.error instanceof ApiError && service.error.status === 404;
  const { t } = useI18n();
  const stateLabel = (phase: string, observation: string) => (observation ? t.serviceObservationLabels[observation as keyof typeof t.serviceObservationLabels] : t.servicePhaseLabels[phase as keyof typeof t.servicePhaseLabels]) || t.serviceObservationLabels.unknown;
  const state = service.data?.observation || service.data?.phase;
  const lastAction = service.data?.action;
  const pendingAction = actionUncertain || !!lastAction && (lastAction.phase !== "observed" || !["stopped", "absent"].includes(lastAction.observation) || lastAction.observation !== state);
  const canChange = available && !service.isError && !service.isFetching && !action.isPending && !refresh.isPending && !pendingAction && Number.isSafeInteger(service.data?.version) && service.data!.version! > 0;
  const configFetching = cache.isFetching({ queryKey: ["worker-config", id] }) > 0;
  const stop = () => action.mutate({ kind: "stop", version: service.data!.version! });
  const confirmRemoval = () => action.mutate({ kind: "remove", version: service.data!.version! });
  // Intentions are persisted before anything is sent, so an explicit query that
  // finds no service proves nothing was sent; repeating a create is idempotent.
  const query = async () => {
    if (service.data) { refresh.mutate(); return; }
    const result = await service.refetch();
    if (attempted && result.error instanceof ApiError && result.error.status === 404) { clearAttempt(); create.reset(); setNoServiceConfirmed(true); }
  };
  return { service, create, refresh, missing, stateLabel, state, pendingAction, canChange, configFetching, attempted, acknowledged, setAcknowledged, confirmRemove, setConfirmRemove, revisionConflict, noServiceConfirmed, stop, confirmRemoval, query: () => void query() };
}
