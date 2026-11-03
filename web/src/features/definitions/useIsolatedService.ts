import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";
import { useI18n } from "@/i18n/context";
import { readServiceAction, writeServiceAction, removeServiceAction, readServiceAttempt, writeServiceAttempt, removeServiceAttempt, rememberExit, lastExit } from "@/lib/service-flags";

export function useIsolatedService(id: string, { available, readable }: { available: boolean; readable: boolean }) {
  const cache = useQueryClient();
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [actionUncertain, setActionUncertain] = useState(() => readServiceAction(id));
  const [attempted, setAttempted] = useState(() => readServiceAttempt(id));
  const service = useQuery({ queryKey: ["isolated-service", id], queryFn: () => api.service(id), retry: false, enabled: readable });
  const [noServiceConfirmed, setNoServiceConfirmed] = useState(false);
  const clearAttempt = () => { setAttempted(false); removeServiceAttempt(id); };
  const clearActionUncertain = () => { setActionUncertain(false); removeServiceAction(id); };
  const refresh = useMutation({ mutationFn: () => api.refreshService(id), onSuccess: (result) => { rememberExit(id, result); cache.setQueryData(["isolated-service", id], result); clearActionUncertain(); void cache.invalidateQueries({ queryKey: ["worker-apps"] }); } });
  const action = useMutation({ mutationFn: ({ kind, version }: { kind: "stop" | "remove"; version: number }) => kind === "stop" ? api.stopService(id, version) : api.removeService(id, version), onMutate: () => { setActionUncertain(true); writeServiceAction(id); setConfirmRemove(false); }, onSuccess: (result) => { cache.setQueryData(["isolated-service", id], result); clearActionUncertain(); void cache.invalidateQueries({ queryKey: ["worker-apps"] }); }, onError: () => { void cache.invalidateQueries({ queryKey: ["isolated-service", id] }); void cache.invalidateQueries({ queryKey: ["worker-apps"] }); } });
  const missing = service.error instanceof ApiError && service.error.status === 404;
  const { t } = useI18n();
  const state = service.data?.observation || service.data?.phase;
  // Stopped with no stop action from Dársena means the process ended by itself.
  const exited = state === "stopped" && !service.data?.action;
  const stateLabel = (phase: string, observation: string) => (observation === "stopped" && exited ? t.definitions.service.exitedLabel : observation ? t.serviceObservationLabels[observation as keyof typeof t.serviceObservationLabels] : t.servicePhaseLabels[phase as keyof typeof t.servicePhaseLabels]) || t.serviceObservationLabels.unknown;
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
    if (attempted && result.error instanceof ApiError && result.error.status === 404) { clearAttempt(); setNoServiceConfirmed(true); }
  };
  // While a service is alive its state is re-read quietly, so a process that
  // ends by itself is noticed without asking. Busy or failed reads are ignored.
  const live = readable && !pendingAction && !refresh.isPending && (state === "running" || state === "created");
  useEffect(() => {
    if (!live) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "visible") return;
      api.refreshService(id).then((result) => {
        rememberExit(id, result);
        cache.setQueryData(["isolated-service", id], result);
        if (result.observation !== state) void cache.invalidateQueries({ queryKey: ["worker-apps"] });
      }, () => undefined);
    }, 5_000);
    return () => window.clearInterval(timer);
  }, [live, id, state, cache]);
  const markAttempted = () => { setAttempted(true); writeServiceAttempt(id); };
  return { exited, exitCode: service.data?.exit_code ?? lastExit(id), markAttempted, service, refresh, missing, stateLabel, state, pendingAction, canChange, configFetching, attempted, confirmRemove, setConfirmRemove, noServiceConfirmed, stop, confirmRemoval, query: () => void query() };
}
