import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";

export function useCapabilities() {
  const query = useQuery({ queryKey: ["capabilities"], queryFn: api.capabilities, staleTime: 15_000, refetchInterval: 10_000, retry: false });
  const current = !query.isError && !query.isStale ? query.data : undefined;
  return { ...query, current, workerReady: current?.worker.status === "ready" && current.state === "ready", vaultReady: current?.worker.status === "ready" && current.state === "ready" && current.vault === "ready" };
}
