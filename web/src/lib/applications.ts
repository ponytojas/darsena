import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { api, ApiError, type ExperimentalService } from "@/api/client";
import { useI18n } from "@/i18n/context";
import { useCapabilities } from "@/lib/capabilities";
import { readServiceAction, readServiceAttempt } from "@/lib/service-flags";
import { definitionHealth, exampleHealth, type Health } from "@/lib/status";

export type AppItem = {
  kind: "example" | "definition";
  id: string;
  name: string;
  subtitle: string;
  health: Health;
  /** ISO timestamp of the latest known change. */
  lastChange: string;
  lastChangeKind: "created" | "deployed";
  href: string;
};
export type AppGroup = { key: "examples" | "ungrouped"; label: string; items: AppItem[] };

const rank: Record<Health, number> = { attention: 0, progress: 1, ok: 2, idle: 2 };
const isActive = (status: string) => status === "queued" || status === "running";

/** Unified, read-only view over demo examples and persistent definitions. Never issues POST requests. */
export function useApplications() {
  const { t } = useI18n();
  const capabilities = useCapabilities();
  const mode = capabilities.data?.mode;
  const demo = mode === "demo";
  const workerConfigured = !!capabilities.data?.worker.configured;
  const readable = capabilities.workerReady;
  const experimental = mode === "experimental";

  const operations = useQuery({
    queryKey: ["operations"],
    queryFn: api.operations,
    enabled: demo,
    refetchInterval: (query) => (query.state.data?.some((op) => isActive(op.status)) ? 2000 : false),
  });
  const active = operations.data?.some((op) => isActive(op.status));
  const examples = useQuery({
    queryKey: ["applications"],
    queryFn: api.apps,
    enabled: demo,
    refetchInterval: active ? 2000 : false,
  });
  const definitions = useQuery({
    queryKey: ["worker-apps"],
    queryFn: api.workerApps,
    enabled: workerConfigured && readable,
  });
  const services = useQueries({
    queries: (definitions.data ?? []).map((app) => ({
      queryKey: ["isolated-service", app.id],
      queryFn: () => api.service(app.id),
      retry: false,
      enabled: experimental && readable,
    })),
  });

  const serviceSignature = services.map((s) => `${s.dataUpdatedAt}:${s.errorUpdatedAt}`).join("|");
  const items = useMemo(() => {
    const list: AppItem[] = [];
    if (demo) {
      const ops = operations.data ?? [];
      for (const app of examples.data ?? []) {
        const latest = ops
          .filter((op) => op.application_id === app.id)
          .reduce<string>((a, op) => (op.created_at > a ? op.created_at : a), "");
        list.push({
          kind: "example",
          id: app.id,
          name: app.name,
          subtitle: app.repository,
          health: exampleHealth(app, ops),
          lastChange: latest || app.created_at,
          lastChangeKind: "deployed",
          href: `/apps/example/${encodeURIComponent(app.id)}`,
        });
      }
    }
    (definitions.data ?? []).forEach((app, index) => {
      const query = services[index];
      const missing = query?.error instanceof ApiError && query.error.status === 404;
      const service: ExperimentalService | undefined = missing ? undefined : query?.data;
      const uncertain =
        experimental && (readServiceAction(app.id) || (readServiceAttempt(app.id) && !service));
      list.push({
        kind: "definition",
        id: app.id,
        name: app.name,
        subtitle: app.image,
        health: definitionHealth(service, uncertain),
        lastChange: app.created_at,
        lastChangeKind: "created",
        href: `/apps/definition/${encodeURIComponent(app.id)}`,
      });
    });
    return list.sort((a, b) => rank[a.health] - rank[b.health] || a.name.localeCompare(b.name));
    // services is summarised by serviceSignature; the query result objects change identity every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [demo, experimental, operations.data, examples.data, definitions.data, serviceSignature]);

  const groups = useMemo<AppGroup[]>(() => {
    const examplesItems = items.filter((item) => item.kind === "example");
    const rest = items.filter((item) => item.kind === "definition");
    return [
      { key: "examples" as const, label: t.groupExamples, items: examplesItems },
      { key: "ungrouped" as const, label: t.groupUngrouped, items: rest },
    ].filter((group) => group.items.length);
  }, [items, t]);

  const enabled = [demo, workerConfigured && readable];
  const pending = !!(
    !capabilities.data ? capabilities.isPending
    : (enabled[0] && (examples.isPending || operations.isPending)) || (enabled[1] && definitions.isPending));
  const error = (enabled[0] && (examples.error ?? operations.error)) || (enabled[1] && definitions.error) || null;
  return { items, groups, isPending: pending, error };
}
