import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertCircle, CheckSquare, ChevronDown, ChevronRight, Plus, Search } from "lucide-react";
import { useI18n } from "@/i18n/context";
import { useCapabilities } from "@/lib/capabilities";
import { api, type Project } from "@/api/client";
import { errorMessage } from "@/i18n/errors";
import { useApplications, type AppItem } from "@/lib/applications";
import { formatDateTime, formatRelative } from "@/lib/format";
import type { Health } from "@/lib/status";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { ProjectActions } from "@/components/ProjectDialogs";
import { Berth, Blank, Failure, PageTitle, Status } from "@/components/shared";
import "./applications.css";

const COLLAPSED_KEY = "darsena.groups.collapsed";
const FIELD = 'input, textarea, select, [contenteditable="true"], [role="dialog"]';

function readCollapsed(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? "[]");
    return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];
  } catch {
    return [];
  }
}
function writeCollapsed(keys: string[]) {
  try {
    localStorage.setItem(COLLAPSED_KEY, JSON.stringify(keys));
  } catch {
    /* storage unavailable: the state just won't persist */
  }
}

/** Focus targets: row links, or the enabled checkboxes while selecting. */
const visibleRows = (root: ParentNode, selecting: boolean) =>
  Array.from(root.querySelectorAll<HTMLElement>(selecting ? ".app-row input:enabled" : ".app-row")).filter(
    (row) => row.offsetParent !== null,
  );

type Kind = "all" | "example" | "definition";

/** The unified list; with `project` it shows only that project's applications and its own summary. */
const dotHealth = (status: string): Health =>
  status === "succeeded" ? "ok" : status === "failed" || status === "interrupted" ? "attention" : "progress";

/** Last deployments as small semantic dots; the summary text is what assistive technology reads. */
function DeploymentDots({ statuses }: { statuses: string[] }) {
  const { t } = useI18n();
  const health = statuses.map(dotHealth);
  const count = (h: Health) => health.filter((value) => value === h).length;
  const summary = t.history(statuses.length, count("ok"), count("attention"), count("progress"));
  return (
    <span className="app-history" title={summary}>
      <span aria-hidden="true" className="app-history-dots">
        {health.map((h, index) => (
          <i key={index} data-health={h} />
        ))}
      </span>
      <span className="sr-only">{summary}</span>
    </span>
  );
}

export default function Applications({ project }: { project?: Project } = {}) {
  const { t, locale } = useI18n();
  const capabilities = useCapabilities();
  const queryClient = useQueryClient();
  const all = useApplications();
  const { isPending, error, projectsFailed, refetchProjects } = all;
  const items = project ? all.items.filter((item) => item.projectId === project.id) : all.items;
  const groups = project ? all.groups.filter((group) => group.id === project.id) : all.groups;
  const [params, setParams] = useSearchParams();
  const term = params.get("q") ?? "";
  const kindParam = params.get("kind");
  const kind: Kind = project ? "all" : kindParam === "example" || kindParam === "definition" ? kindParam : "all";
  const attentionOnly = params.get("health") === "attention";
  const searchRef = useRef<HTMLInputElement>(null);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [moving, setMoving] = useState(false);
  const [target, setTarget] = useState("");
  const targetId = useId();
  const nameIds = useId();

  const mode = capabilities.data?.mode;
  const demo = mode === "demo";
  const workerConfigured = !!capabilities.data?.worker.configured;
  const canCreate = demo || capabilities.workerReady;
  const workerDown = workerConfigured && !capabilities.workerReady;

  useEffect(() => {
    const shortcut = (event: globalThis.KeyboardEvent) => {
      const target = event.target as HTMLElement;
      if (target.closest(FIELD) || event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key === "/") {
        event.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener("keydown", shortcut);
    return () => window.removeEventListener("keydown", shortcut);
  }, []);

  const leaveSelection = () => {
    move.reset();
    setSelecting(false);
    setSelected([]);
    setMoving(false);
    setTarget("");
  };
  useEffect(() => {
    if (!selecting) return;
    const escape = (event: globalThis.KeyboardEvent) => {
      const el = event.target as HTMLElement;
      if (event.key !== "Escape" || event.defaultPrevented || el.closest('[role="dialog"], select, input[type="search"]')) return;
      setSelecting(false);
      setSelected([]);
      setMoving(false);
      setTarget("");
    };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [selecting]);

  const update = (changes: Record<string, string>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(changes)) {
      if (value && value !== "all") next.set(key, value);
      else next.delete(key);
    }
    setParams(next, { replace: true });
  };
  const toggleGroup = (key: string) =>
    setCollapsed((current) => {
      const next = current.includes(key) ? current.filter((k) => k !== key) : [...current, key];
      writeCollapsed(next);
      return next;
    });

  const needle = term.trim().toLowerCase();
  const matches = (item: AppItem) =>
    (kind === "all" || item.kind === kind) &&
    (!attentionOnly || item.health === "attention") &&
    (!needle || `${item.name} ${item.subtitle}`.toLowerCase().includes(needle));
  const visible = groups
    .map((group) => ({ ...group, items: group.items.filter(matches) }))
    .filter((group) => group.items.length);
  // Only what is shown counts: definitions in an open group that match the filters. Anything a filter or a
  // collapsed group hides is neither counted nor moved; it is checked again if it comes back into view.
  const shown = new Set(
    visible.flatMap((group) =>
      !project && collapsed.includes(group.id) ? [] : group.items.filter((item) => item.kind === "definition").map((item) => item.id),
    ),
  );
  const chosen = selected.filter((id) => shown.has(id));
  const hasDefinitions = items.some((item) => item.kind === "definition");
  const canSelect = canCreate && hasDefinitions && !isPending && !error;
  const destinations = [...all.projects.filter((candidate) => candidate.id !== project?.id)];
  const move = useMutation({
    mutationFn: (ids: string[]) => api.workerAssign({ project_id: target === "none" ? null : target, application_ids: ids }),
    onSuccess: async (_, ids) => {
      const name = target === "none" ? t.selection.none : (all.projects.find((p) => p.id === target)?.name ?? "");
      toast.success(t.selection.moved(ids.length, name));
      leaveSelection();
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["worker-apps"] }),
        queryClient.invalidateQueries({ queryKey: ["projects"] }),
      ]);
    },
  });
  const filtering = !!needle || kind !== "all" || attentionOnly;
  const clearAll = () => update({ q: "", kind: "all", health: "all" });

  const count = (health: Health) => items.filter((item) => item.health === health).length;
  const summary: [string, number, Health | "total"][] = [
    [t.summaryTotal, items.length, "total"],
    [t.summaryOk, count("ok"), "ok"],
    [t.summaryProgress, count("progress"), "progress"],
    [t.summaryAttention, count("attention"), "attention"],
    [t.summaryIdle, count("idle"), "idle"],
  ];
  const kinds: [Kind, string, number][] = [
    ["all", t.filterAll, items.length],
    ...(project ? [] : demo ? [["example", t.filterExamples, items.filter((i) => i.kind === "example").length] as [Kind, string, number]] : []),
    ...(project ? [] : workerConfigured ? [["definition", t.filterDefinitions, items.filter((i) => i.kind === "definition").length] as [Kind, string, number]] : []),
  ];

  const showNotice = workerDown && kind !== "example" && !needle && !attentionOnly;
  const refetchAll = () => {
    void queryClient.refetchQueries({ queryKey: ["applications"] });
    void queryClient.refetchQueries({ queryKey: ["operations"] });
    void queryClient.refetchQueries({ queryKey: ["worker-apps"] });
    void queryClient.refetchQueries({ queryKey: ["projects"] });
  };
  const newApp = canCreate ? (
    <Button render={<Link to={project && capabilities.workerReady ? `/apps/new?kind=definition&project=${encodeURIComponent(project.id)}` : "/apps/new"} />}>
      <Plus data-icon="inline-start" />
      {t.newApp}
    </Button>
  ) : undefined;
  const notice = (
    <Alert data-testid="definitions-unavailable">
      <AlertCircle />
      <AlertTitle>{t.definitionsUnavailableTitle}</AlertTitle>
      <AlertDescription>
        <p>{t.definitionsUnavailableDescription}</p>
        <Button variant="outline" size="sm" onClick={() => void capabilities.refetch()}>
          {t.refreshStatus}
        </Button>
      </AlertDescription>
    </Alert>
  );

  const onListKey = (event: KeyboardEvent<HTMLElement>) => {
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) || event.metaKey || event.ctrlKey || event.altKey) return;
    const rows = visibleRows(event.currentTarget, selecting);
    const current = rows.indexOf(document.activeElement as HTMLElement);
    if (current < 0) return;
    event.preventDefault();
    const next =
      event.key === "Home" ? 0
      : event.key === "End" ? rows.length - 1
      : Math.max(0, Math.min(rows.length - 1, current + (event.key === "ArrowDown" ? 1 : -1)));
    rows[next]?.focus();
  };

  let body;
  if (isPending) {
    body = (
      <div className="apps-loading" role="status" aria-label={t.loading}>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    );
  } else if (error) {
    body = <Failure error={error} retry={refetchAll} />;
  } else if (!items.length) {
    body = (
      <>
        {showNotice && notice}
        {!workerDown &&
          (project ? (
            <Blank title={t.projects.emptyTitle} description={t.projects.emptyDescription} />
          ) : (
            <Blank title={t.noApps} description={t.noAppsDescription} action={newApp} />
          ))}
      </>
    );
  } else if (!visible.length) {
    body = (
      <>
        {showNotice && notice}
        <Blank
          title={t.noMatches}
          description={t.noMatchesDescription}
          action={
            <Button variant="outline" onClick={clearAll}>
              {t.clearFilters}
            </Button>
          }
        />
      </>
    );
  } else {
    body = (
      <div className="apps-groups" onKeyDown={onListKey}>
        {visible.map((group) => {
          const open = !!project || !collapsed.includes(group.id);
          const panel = `apps-group-${group.id}`;
          return (
            <section key={group.id} className="apps-group" aria-label={group.label}>
              <h2 className="apps-group-heading" hidden={!!project}>
                <button type="button" aria-expanded={open} aria-controls={panel} onClick={() => toggleGroup(group.id)}>
                  {open ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
                  {group.color && <span className="project-swatch" data-color={group.color} aria-hidden="true" />}
                  <span>{group.label}</span>
                  <span className="apps-group-count">{group.items.length}</span>
                </button>
              </h2>
              <div id={panel} hidden={!open}>
                <div className="apps-header" aria-hidden="true" data-selecting={selecting || undefined}>
                  {selecting && <span />}
                  <span />
                  <span>{t.application}</span>
                  <span>{t.columnKind}</span>
                  <span>{t.state}</span>
                  <span>{t.columnLastChange}</span>
                  <span />
                </div>
                {group.items.map((item) => {
                  const key = `${item.kind}:${item.id}`;
                  const content = (
                    <>
                      <Berth health={item.health} />
                      <span className="app-main">
                        <strong id={`${nameIds}-${item.id}`}>{item.name}</strong>
                        <small>{item.subtitle}</small>
                        {item.note && <span className="app-note">{item.note}</span>}
                        {!!item.deployments?.length && <DeploymentDots statuses={item.deployments} />}
                      </span>
                      <Badge variant="secondary" className="app-kind">
                        {item.kind === "example" ? t.kindExample : t.kindDefinition}
                      </Badge>
                      <span className="app-status">
                        <Status health={item.health} label={t.healthLabels[item.health]} />
                      </span>
                      <time
                        className="app-time"
                        dateTime={item.lastChange}
                        title={formatDateTime(item.lastChange, locale)}
                      >
                        {item.lastChangeKind === "deployed" ? t.changeDeployed : item.lastChangeKind === "changed" ? t.changeChanged : t.changeCreated}{" "}
                        {formatRelative(item.lastChange, locale)}
                      </time>
                      <ChevronRight className="row-chevron" aria-hidden="true" />
                    </>
                  );
                  if (!selecting)
                    return (
                      <Link key={key} className="app-row" to={item.href} data-kind={item.kind}>
                        {content}
                      </Link>
                    );
                  if (item.kind === "example")
                    return (
                      <div key={key} className="app-row" data-kind={item.kind} data-selecting title={t.selection.exampleDisabled}>
                        <span className="app-check" aria-hidden="true" />
                        {content}
                        <span className="sr-only">{t.selection.exampleDisabled}</span>
                      </div>
                    );
                  return (
                    <label key={key} className="app-row" data-kind={item.kind} data-selecting data-selected={selected.includes(item.id)}>
                      <input
                        type="checkbox"
                        className="app-check"
                        aria-labelledby={`${nameIds}-${item.id}`}
                        checked={selected.includes(item.id)}
                        onChange={(event) =>
                          setSelected((current) =>
                            event.target.checked ? [...current, item.id] : current.filter((id) => id !== item.id),
                          )
                        }
                      />
                      {content}
                    </label>
                  );
                })}
              </div>
            </section>
          );
        })}
        {showNotice && notice}
      </div>
    );
  }

  return (
    <div className="page applications-page">
      <PageTitle
        title={project ? project.name : t.applications}
        description={project ? undefined : t.applicationsDescription}
        action={
          project ? (
            <div className="project-header-actions">
              <ProjectActions project={project} count={items.length} />
              {newApp}
            </div>
          ) : (
            newApp
          )
        }
      />
      {projectsFailed && !project && (
        <Alert data-testid="projects-unavailable">
          <AlertCircle />
          <AlertTitle>{t.projects.loadFailed}</AlertTitle>
          <AlertDescription>
            <p>{t.projects.loadFailedDescription}</p>
            <Button variant="outline" size="sm" onClick={refetchProjects}>
              {t.retry}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {!isPending && !error && items.length > 0 && (
        <div className="apps-summary" role="group" aria-label={t.appsSummary}>
          <dl>
            {summary.map(([label, value, tone]) => (
              <div key={tone} data-tone={tone} data-zero={value === 0}>
                <dt>{label}</dt>
                <dd>{value}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}
      <div className="apps-toolbar">
        <div className="apps-chips" role="group" aria-label={t.appsFilters}>
          {kinds.map(([value, label, total]) => (
            <button
              key={value}
              type="button"
              aria-pressed={kind === value && (value !== "all" || !attentionOnly)}
              onClick={() => update(value === "all" ? { kind: "all", health: "all" } : { kind: value })}
            >
              {label}
              <span>{total}</span>
            </button>
          ))}
          <button
            type="button"
            aria-pressed={attentionOnly}
            onClick={() => update({ health: attentionOnly ? "all" : "attention" })}
          >
            {t.filterAttention}
            <span>{count("attention")}</span>
          </button>
        </div>
        {canSelect && (
          <Button
            variant={selecting ? "secondary" : "outline"}
            aria-pressed={selecting}
            title={t.selection.selectHint}
            onClick={() => (selecting ? leaveSelection() : setSelecting(true))}
          >
            <CheckSquare data-icon="inline-start" />
            {t.selection.select}
          </Button>
        )}
        <label className="search-control">
          <Search aria-hidden="true" />
          <span className="sr-only">{t.search}</span>
          <Input
            ref={searchRef}
            aria-keyshortcuts="/"
            type="search"
            placeholder={t.searchPlaceholder}
            value={term}
            onChange={(event) => update({ q: event.target.value })}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                if (term) update({ q: "" });
                else searchRef.current?.blur();
              }
              if (event.key === "ArrowDown") {
                event.preventDefault();
                visibleRows(document, selecting)[0]?.focus();
              }
            }}
          />
          <kbd aria-hidden="true" className="search-key">
            /
          </kbd>
        </label>
      </div>
      {selecting && canSelect && (
        <div className="apps-selection-bar" role="group" aria-label={t.selection.barLabel}>
          <span className="apps-selection-count" role="status" aria-live="polite">
            {t.selection.count(chosen.length)}
          </span>
          {moving && (
            <>
              <label htmlFor={targetId} className="sr-only">
                {t.selection.target}
              </label>
              <select
                id={targetId}
                className="apps-selection-select"
                value={target}
                autoFocus
                disabled={move.isPending}
                onChange={(event) => setTarget(event.target.value)}
              >
                <option value="" disabled>
                  {t.selection.choose}
                </option>
                {destinations.map((candidate) => (
                  <option key={candidate.id} value={candidate.id}>
                    {candidate.name}
                  </option>
                ))}
                <option value="none">{t.selection.none}</option>
              </select>
              <Button disabled={!target || !chosen.length || move.isPending} onClick={() => move.mutate(chosen)}>
                {move.isPending ? t.selection.moving : t.selection.confirm}
              </Button>
            </>
          )}
          {!moving && (
            <Button disabled={!chosen.length} onClick={() => setMoving(true)}>
              {t.selection.move}
            </Button>
          )}
          <Button variant="outline" onClick={leaveSelection}>
            {t.selection.cancel}
          </Button>
          {move.error && (
            <p className="apps-selection-error" role="alert">
              {errorMessage(move.error, t)}
            </p>
          )}
        </div>
      )}
      {body}
      {filtering && !isPending && !error && visible.length > 0 && (
        <p className="apps-filter-note">
          <Button variant="link" size="sm" onClick={clearAll}>
            {t.clearFilters}
          </Button>
        </p>
      )}
    </div>
  );
}
