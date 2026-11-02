import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AlertCircle, ChevronDown, ChevronRight, Plus, Search } from "lucide-react";
import { useI18n } from "@/i18n/context";
import { useCapabilities } from "@/lib/capabilities";
import { useApplications, type AppItem } from "@/lib/applications";
import { formatDateTime, formatRelative } from "@/lib/format";
import type { Health } from "@/lib/status";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
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

const visibleRows = (root: ParentNode) =>
  Array.from(root.querySelectorAll<HTMLAnchorElement>(".app-row")).filter((row) => row.offsetParent !== null);

type Kind = "all" | "example" | "definition";

export default function Applications() {
  const { t, locale } = useI18n();
  const capabilities = useCapabilities();
  const queryClient = useQueryClient();
  const { items, groups, isPending, error } = useApplications();
  const [params, setParams] = useSearchParams();
  const term = params.get("q") ?? "";
  const kindParam = params.get("kind");
  const kind: Kind = kindParam === "example" || kindParam === "definition" ? kindParam : "all";
  const attentionOnly = params.get("health") === "attention";
  const searchRef = useRef<HTMLInputElement>(null);
  const [collapsed, setCollapsed] = useState(readCollapsed);

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
    ...(demo ? [["example", t.filterExamples, items.filter((i) => i.kind === "example").length] as [Kind, string, number]] : []),
    ...(workerConfigured ? [["definition", t.filterDefinitions, items.filter((i) => i.kind === "definition").length] as [Kind, string, number]] : []),
  ];

  const showNotice = workerDown && kind !== "example" && !needle && !attentionOnly;
  const refetchAll = () => {
    void queryClient.refetchQueries({ queryKey: ["applications"] });
    void queryClient.refetchQueries({ queryKey: ["operations"] });
    void queryClient.refetchQueries({ queryKey: ["worker-apps"] });
  };
  const newApp = canCreate ? (
    <Button render={<Link to="/apps/new" />}>
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
    const rows = visibleRows(event.currentTarget);
    const current = rows.indexOf(document.activeElement as HTMLAnchorElement);
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
        {!workerDown && (
          <Blank title={t.noApps} description={t.noAppsDescription} action={newApp} />
        )}
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
          const open = !collapsed.includes(group.key);
          const panel = `apps-group-${group.key}`;
          return (
            <section key={group.key} className="apps-group" aria-label={group.label}>
              <h2 className="apps-group-heading">
                <button type="button" aria-expanded={open} aria-controls={panel} onClick={() => toggleGroup(group.key)}>
                  {open ? <ChevronDown aria-hidden="true" /> : <ChevronRight aria-hidden="true" />}
                  <span>{group.label}</span>
                  <span className="apps-group-count">{group.items.length}</span>
                </button>
              </h2>
              <div id={panel} hidden={!open}>
                <div className="apps-header" aria-hidden="true">
                  <span />
                  <span>{t.application}</span>
                  <span>{t.columnKind}</span>
                  <span>{t.state}</span>
                  <span>{t.columnLastChange}</span>
                  <span />
                </div>
                {group.items.map((item) => (
                  <Link key={`${item.kind}:${item.id}`} className="app-row" to={item.href} data-kind={item.kind}>
                    <Berth health={item.health} />
                    <span className="app-main">
                      <strong>{item.name}</strong>
                      <small>{item.subtitle}</small>
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
                      {item.lastChangeKind === "deployed" ? t.changeDeployed : t.changeCreated}{" "}
                      {formatRelative(item.lastChange, locale)}
                    </time>
                    <ChevronRight className="row-chevron" aria-hidden="true" />
                  </Link>
                ))}
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
      <PageTitle title={t.applications} description={t.applicationsDescription} action={newApp} />
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
                visibleRows(document)[0]?.focus();
              }
            }}
          />
          <kbd aria-hidden="true" className="search-key">
            /
          </kbd>
        </label>
      </div>
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
