import { useEffect, useId, useMemo, useState, type KeyboardEvent, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useApplications } from "@/lib/applications";
import { locales, useI18n } from "@/i18n/context";
import type { Theme } from "@/lib/theme";

type Option = {
  id: string;
  group: "go" | "apps" | "actions";
  label: string;
  /** Extra text shown after the label and also matched by the filter. */
  detail?: ReactNode;
  search: string;
  run: () => void;
};

const normalize = (value: string) =>
  value.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase();

export type PaletteProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Whether the visible shell shows the Activity page (demo only). */
  showActivity: boolean;
  canCreate: boolean;
  /** False until the experimental owner is signed in: the palette then lists no applications. */
  loadApplications: boolean;
  /** Worker ready (and owner signed in): projects can be created from the palette. */
  canCreateProject: boolean;
  onNewProject: () => void;
  setTheme: (theme: Theme) => void;
};

export function CommandPalette(props: PaletteProps) {
  const { open, onOpenChange } = props;
  const { t } = useI18n();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="top-[20%] max-w-[calc(100%-2rem)] -translate-y-0 gap-2 p-2 sm:max-w-lg motion-reduce:data-closed:animate-none motion-reduce:data-open:animate-none"
      >
        <DialogTitle className="sr-only">{t.palette.title}</DialogTitle>
        <DialogDescription className="sr-only">{t.palette.description}</DialogDescription>
        <PaletteBody {...props} />
      </DialogContent>
    </Dialog>
  );
}

function AppOptions({ children }: { children: (options: Option[]) => ReactNode }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { items, projects } = useApplications();
  const options = useMemo<Option[]>(
    () => [
      ...projects.map((project) => ({
        id: `project-${project.id}`,
        group: "go" as const,
        label: project.name,
        detail: t.projects.crumb,
        search: `${project.name} ${t.projects.crumb}`,
        run: () => void navigate(`/projects/${encodeURIComponent(project.id)}`),
      })),
      ...items.map((app) => {
        const kind = app.kind === "example" ? t.kindExample : t.kindDefinition;
        const health = t.healthLabels[app.health];
        return {
          id: `app-${app.kind}-${app.id}`,
          group: "apps" as const,
          label: app.name,
          detail: `${kind} · ${health}`,
          search: `${app.name} ${kind} ${health}`,
          run: () => void navigate(app.href),
        };
      }),
    ],
    [items, projects, t, navigate],
  );
  return <>{children(options)}</>;
}

function PaletteBody(props: PaletteProps) {
  // Applications are queried only once the palette is open and, in experimental mode, the owner is signed in.
  return props.loadApplications ? (
    <AppOptions>{(apps) => <PaletteList {...props} apps={apps} />}</AppOptions>
  ) : (
    <PaletteList {...props} apps={[]} />
  );
}

function PaletteList({
  onOpenChange,
  showActivity,
  canCreate,
  canCreateProject,
  onNewProject,
  setTheme,
  apps,
}: PaletteProps & { apps: Option[] }) {
  const { t, setLocale, locale } = useI18n();
  const navigate = useNavigate();
  const listId = useId();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);

  const options = useMemo<Option[]>(() => {
    const go = (to: string) => () => void navigate(to);
    const list: Option[] = [
      { id: "go-apps", group: "go", label: t.applications, search: t.applications, run: go("/") },
    ];
    if (showActivity)
      list.push({ id: "go-activity", group: "go", label: t.activity, search: t.activity, run: go("/activity") });
    list.push({
      id: "go-diagnostics",
      group: "go",
      label: t.diagnosticsTitle,
      search: t.diagnosticsTitle,
      run: go("/diagnostics"),
    });
    list.push(...apps);
    if (canCreate)
      list.push({ id: "act-new", group: "actions", label: t.newApp, search: t.newApp, run: go("/apps/new") });
    if (canCreateProject)
      list.push({
        id: "act-new-project",
        group: "actions",
        label: t.projects.newProject,
        search: t.projects.newProject,
        run: onNewProject,
      });
    const themes: [Theme, string][] = [
      ["light", t.palette.themeLight],
      ["dark", t.palette.themeDark],
      ["system", t.palette.themeSystem],
    ];
    for (const [value, label] of themes)
      list.push({ id: `act-theme-${value}`, group: "actions", label, search: label, run: () => setTheme(value) });
    for (const value of locales) {
      if (value === locale) continue;
      const label = t.palette.language(t.languageNames[value]);
      list.push({
        id: `act-lang-${value}`,
        group: "actions",
        label,
        search: `${label} ${value}`,
        run: () => setLocale(value),
      });
    }
    return list;
  }, [t, locale, showActivity, canCreate, canCreateProject, onNewProject, apps, navigate, setTheme, setLocale]);

  const results = useMemo(() => {
    const needle = normalize(query.trim());
    return needle ? options.filter((o) => normalize(o.search).includes(needle)) : options;
  }, [options, query]);
  const index = results.length ? Math.min(active, results.length - 1) : -1;
  const activeId = index >= 0 ? `${listId}-${results[index].id}` : undefined;

  useEffect(() => {
    if (activeId) document.getElementById(activeId)?.scrollIntoView({ block: "nearest" });
  }, [activeId]);

  const choose = (option: Option) => {
    onOpenChange(false);
    option.run();
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const count = results.length;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (count) setActive((index + (e.key === "ArrowDown" ? 1 : -1) + count) % count);
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      setActive(e.key === "Home" ? 0 : Math.max(count - 1, 0));
    } else if (e.key === "Enter" && index >= 0) {
      e.preventDefault();
      choose(results[index]);
    }
  };

  const groups = [
    { key: "go" as const, label: t.palette.groupGo },
    { key: "apps" as const, label: t.palette.groupApps },
    { key: "actions" as const, label: t.palette.groupActions },
  ];
  return (
    <>
      <Input
        role="combobox"
        aria-expanded={results.length > 0}
        aria-controls={listId}
        aria-activedescendant={activeId}
        aria-autocomplete="list"
        aria-label={t.palette.title}
        placeholder={t.palette.placeholder}
        autoComplete="off"
        spellCheck={false}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
        }}
        onKeyDown={onKeyDown}
      />
      <div
        id={listId}
        role="listbox"
        aria-label={t.palette.listLabel}
        className="max-h-72 overflow-y-auto overscroll-contain"
      >
        {results.length === 0 && (
          <p role="presentation" className="px-2 py-6 text-center text-sm text-muted-foreground">
            {t.palette.empty}
          </p>
        )}
        {groups.map((group) => {
          const items = results.filter((o) => o.group === group.key);
          if (!items.length) return null;
          const headingId = `${listId}-${group.key}`;
          return (
            <div key={group.key} role="group" aria-labelledby={headingId}>
              <div id={headingId} className="px-2 pt-2 pb-1 text-xs font-medium text-muted-foreground">
                {group.label}
              </div>
              {items.map((option) => {
                const selected = results[index]?.id === option.id;
                return (
                  <div
                    key={option.id}
                    id={`${listId}-${option.id}`}
                    role="option"
                    aria-selected={selected}
                    data-active={selected || undefined}
                    className="flex min-h-9 cursor-pointer items-center justify-between gap-3 rounded-md px-2 py-1.5 text-sm data-active:bg-accent data-active:text-accent-foreground"
                    onMouseMove={() => {
                      if (!selected) setActive(results.indexOf(option));
                    }}
                    onClick={() => choose(option)}
                  >
                    <span className="truncate">{option.label}</span>
                    {option.detail && (
                      <span className="shrink-0 text-xs text-muted-foreground">{option.detail}</span>
                    )}
                  </div>
                );
              })}
            </div>
          );
        })}
      </div>
    </>
  );
}
