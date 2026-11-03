import { useMemo } from "react";
import { imageSummary } from "./image";
import { useQuery } from "@tanstack/react-query";
import { api, type Operation, type Project } from "@/api/client";
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
  lastChangeKind: "created" | "deployed" | "changed";
  /** Owning project of a definition; null for examples and unassigned definitions. */
  projectId: string | null;
  /** Why the item needs attention, when the state itself is not enough. */
  note?: string;
  /** Statuses of the last deployments of an example, oldest first. */
  deployments?: Operation["status"][];
  href: string;
};
export type AppGroup = { id: string; label: string; color?: Project["color"]; items: AppItem[] };

const rank: Record<Health, number> = { attention: 0, progress: 1, ok: 2, idle: 2 };
const isActive = (status: string) => status === "queued" || status === "running";

/** Unified, read-only view over demo examples and persistent definitions. Never issues POST requests. */
export function useApplications() {
  const { t, locale } = useI18n();
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
  const projects = useQuery({
    queryKey: ["projects"],
    queryFn: api.workerProjects,
    enabled: workerConfigured && readable,
  });

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
          deployments: ops
            .filter((op) => op.application_id === app.id)
            .sort((a, b) => (a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0))
            .slice(-10)
            .map((op) => op.status),
          projectId: null,
          href: `/apps/example/${encodeURIComponent(app.id)}`,
        });
      }
    }
    for (const app of definitions.data ?? []) {
      const service = app.service ?? undefined;
      const unavailable = app.service_unavailable || app.config_unavailable;
      const uncertain =
        experimental && (readServiceAction(app.id) || (readServiceAttempt(app.id) && !service));
      const configChange = app.config?.updated_at;
      list.push({
        kind: "definition",
        id: app.id,
        name: app.name,
        subtitle: imageSummary(app.image),
        health: unavailable ? "attention" : definitionHealth(service, uncertain),
        note: unavailable ? t.serviceStateUnavailable : undefined,
        lastChange: configChange && configChange > app.updated_at ? configChange : app.updated_at,
        lastChangeKind: "changed",
        projectId: app.project_id,
        href: `/apps/definition/${encodeURIComponent(app.id)}`,
      });
    }
    return list.sort((a, b) => rank[a.health] - rank[b.health] || a.name.localeCompare(b.name));
  }, [demo, experimental, operations.data, examples.data, definitions.data, t]);

  const groups = useMemo<AppGroup[]>(() => {
    const definitionItems = items.filter((item) => item.kind === "definition");
    const known = [...(projects.data ?? [])].sort((a, b) => a.name.localeCompare(b.name, locale));
    const byProject: AppGroup[] = known.map((project) => ({
      id: project.id,
      label: project.name,
      color: project.color,
      items: definitionItems.filter((item) => item.projectId === project.id),
    }));
    const ids = new Set(known.map((project) => project.id));
    // Without the project list, grouping is unknown: keep definitions neutral instead of calling them unassigned.
    const rest =
      projects.isError ?
        { id: "definitions", label: t.groupDefinitions, items: definitionItems }
      : { id: "ungrouped", label: t.groupUngrouped, items: definitionItems.filter((item) => !item.projectId || !ids.has(item.projectId)) };
    return [
      ...(projects.isError ? [] : byProject),
      rest,
      { id: "examples", label: t.groupExamples, items: items.filter((item) => item.kind === "example") },
    ].filter((group) => group.items.length);
  }, [items, projects.data, projects.isError, locale, t]);

  const projectList = useMemo(
    () => [...(projects.data ?? [])].sort((a, b) => a.name.localeCompare(b.name, locale)),
    [projects.data, locale],
  );
  const enabled = [demo, workerConfigured && readable];
  const pending = !!(
    !capabilities.data ? capabilities.isPending
    : (enabled[0] && (examples.isPending || operations.isPending)) || (enabled[1] && (definitions.isPending || projects.isPending)));
  const error = (enabled[0] && (examples.error ?? operations.error)) || (enabled[1] && definitions.error) || null;
  return {
    items,
    groups,
    projects: projectList,
    projectsFailed: enabled[1] && projects.isError,
    projectsError: projects.error,
    projectsPending: enabled[1] && projects.isPending,
    refetchProjects: () => void projects.refetch(),
    isPending: pending,
    error,
  };
}
