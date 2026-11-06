import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type FormEvent,
  type ReactElement,
  type ReactNode,
} from "react";
import {
  createBrowserRouter,
  Link,
  Navigate,
  NavLink,
  Outlet,
  RouterProvider,
  useParams,
  type Params,
  useLocation,
  useMatches,
  useSearchParams,
} from "react-router";
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from "@tanstack/react-query";
import {
  Activity,
  ArrowUpRight,
  Box,
  ChevronRight,
  Menu,
  PanelLeftClose,
  PanelLeftOpen,
  X,
  Settings2,
  Search,
  Plus,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  api,
  ApiError,
  login,
  logout,
  sessionSnapshot,
  subscribeSession,
} from "@/api/client";
import {
  Dialog,
  DialogTrigger,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Loading, Blank, Failure } from "@/components/shared";
import { useTheme, useAccent, useGithubLink } from "@/lib/theme";
import { SettingsPanels } from "@/components/SettingsDialog";
import { I18nProvider } from "@/i18n";
import { useI18n } from "@/i18n/context";
import type { Messages } from "@/i18n/es";
import { useCapabilities } from "@/lib/capabilities";
import Applications from "@/pages/Applications";
import { useApplications } from "@/lib/applications";
import { CommandPalette } from "@/components/CommandPalette";
import { ProjectFormDialog } from "@/components/ProjectDialogs";
import { Toaster } from "@/components/ui/sonner";
import ProjectPage from "@/pages/Project";
const NewApp = lazy(() => import("@/pages/NewApp"));
const ExampleDetail = lazy(() => import("@/pages/ExampleDetail"));
const OperationDetail = lazy(() => import("@/pages/OperationDetail"));
const DefinitionDetail = lazy(() => import("@/pages/DefinitionDetail"));
const ActivityPage = lazy(() => import("@/pages/Activity"));
const Diagnostics = lazy(() => import("@/pages/Diagnostics"));
const client = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 5_000, retry: 1, refetchOnWindowFocus: true },
  },
});
type Crumb = {
  /** `appName` is the example application named by the route's `:id`, once known. */
  label: (t: Messages, appName?: string) => string;
  to?: (context: { params: Params }) => string;
};
type RouteHandle = { crumb?: Crumb[] };
function crumbsOf(handle: unknown): Crumb[] {
  const crumb = (handle as RouteHandle | undefined)?.crumb;
  return Array.isArray(crumb) ? crumb : [];
}
function BrandMark() {
  return (
    <svg
      className="brand-mark"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.25"
      strokeLinecap="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M4.5 3.5v17M4.5 7h14M4.5 12h10M4.5 17h6" />
    </svg>
  );
}
function isEditable(target: EventTarget | null) {
  return (
    target instanceof HTMLElement &&
    (target.isContentEditable ||
      ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName))
  );
}
function RailTip({
  rail,
  label,
  children,
}: {
  rail: boolean;
  label: string;
  children: ReactElement;
}) {
  if (!rail) return children;
  return (
    <Tooltip>
      <TooltipTrigger render={children} />
      <TooltipContent side="right">{label}</TooltipContent>
    </Tooltip>
  );
}
function AppCount() {
  const { items, isPending } = useApplications();
  return isPending ? null : <span className="nav-count">{items.length}</span>;
}
function ProjectsNav({
  rail,
  onNavigate,
  onNew,
}: {
  rail: boolean;
  onNavigate: () => void;
  onNew: () => void;
}) {
  const { t } = useI18n();
  const { projects, items } = useApplications();
  const countOf = (id: string) => items.filter((item) => item.projectId === id).length;
  return (
    <div className="nav-projects">
      <p className="nav-label" hidden={rail}>
        {t.projects.section}
      </p>
      <nav aria-label={t.projects.navLabel}>
        {projects.map((project) => (
          <RailTip key={project.id} rail={rail} label={project.name}>
            <NavLink
              to={`/projects/${encodeURIComponent(project.id)}`}
              aria-label={rail ? project.name : undefined}
              data-testid="project-link"
              onClick={onNavigate}
            >
              {rail ? (
                <span className="project-initial project-swatch" data-color={project.color} aria-hidden="true">
                  {[...project.name][0]?.toUpperCase()}
                </span>
              ) : (
                <>
                  <span className="project-swatch" data-color={project.color} aria-hidden="true" />
                  <span className="nav-project-name">{project.name}</span>
                  <span className="nav-count">{countOf(project.id)}</span>
                </>
              )}
            </NavLink>
          </RailTip>
        ))}
        <RailTip rail={rail} label={t.projects.newProject}>
          <button
            type="button"
            className="nav-new-project"
            aria-label={rail ? t.projects.newProject : undefined}
            onClick={onNew}
          >
            <Plus />
            {!rail && t.projects.newProject}
          </button>
        </RailTip>
      </nav>
    </div>
  );
}
function Shell() {
  const { t } = useI18n();
  const capabilities = useCapabilities();
  const signedIn = useSyncExternalStore(subscribeSession, sessionSnapshot);
  const [credential, setCredential] = useState("");
  const [loginError, setLoginError] = useState("");
  const [loginPending, setLoginPending] = useState(false);
  const experimental = capabilities.data?.mode === "experimental";
  async function submitLogin(event: FormEvent) {
    event.preventDefault();
    setLoginPending(true);
    setLoginError("");
    try {
      await login(credential);
      setCredential("");
      await client.invalidateQueries();
    } catch {
      setCredential("");
      setLoginError("failed");
    } finally {
      setLoginPending(false);
    }
  }
  const location = useLocation();
  const [theme, setTheme] = useTheme();
  const [accent, setAccent] = useAccent();
  const [githubLink, setGithubLink] = useGithubLink();
  const [menuOpen, setMenuOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [isMobile, setIsMobile] = useState(
    () => window.matchMedia("(max-width: 600px)").matches,
  );
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try {
      return localStorage.getItem("darsena-sidebar-collapsed") === "true";
    } catch {
      return false;
    }
  });
  const sidebarOpen = isMobile ? menuOpen : !sidebarCollapsed;
  const rail = !isMobile && sidebarCollapsed;
  const mainRef = useRef<HTMLElement>(null);
  const menuRef = useRef<HTMLButtonElement>(null);
  const settingsRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const media = window.matchMedia("(max-width: 600px)");
    const update = () => {
      setIsMobile(media.matches);
      setMenuOpen(false);
    };
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const previousPath = useRef(location.pathname);
  useEffect(() => {
    if (previousPath.current !== location.pathname) {
      previousPath.current = location.pathname;
      setMenuOpen(false);
      const frame = requestAnimationFrame(() => mainRef.current?.focus());
      return () => cancelAnimationFrame(frame);
    }
  }, [location.pathname]);
  const closeMenu = () => {
    setMenuOpen(false);
    menuRef.current?.focus();
  };
  const toggleSidebar = () => {
    if (isMobile) {
      if (menuOpen) closeMenu();
      else setMenuOpen(true);
    } else {
      const collapsed = !sidebarCollapsed;
      setSidebarCollapsed(collapsed);
      try {
        localStorage.setItem("darsena-sidebar-collapsed", String(collapsed));
      } catch {
        /* Storage may be unavailable. */
      }
    }
  };
  const toggleRef = useRef(toggleSidebar);
  useEffect(() => {
    toggleRef.current = toggleSidebar;
  });
  useEffect(() => {
    if (isMobile) return;
    const onKey = (e: KeyboardEvent) => {
      if (
        e.key.toLowerCase() !== "b" ||
        !(e.metaKey || e.ctrlKey) ||
        e.altKey ||
        e.shiftKey ||
        e.defaultPrevented ||
        isEditable(e.target) ||
        document.querySelector('[role="dialog"], [role="alertdialog"]')
      )
        return;
      e.preventDefault();
      toggleRef.current();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [isMobile]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (
        e.key.toLowerCase() !== "k" ||
        !(e.metaKey || e.ctrlKey) ||
        e.altKey ||
        e.shiftKey ||
        e.defaultPrevented ||
        isEditable(e.target) ||
        document.querySelector('[role="dialog"], [role="alertdialog"]')
      )
        return;
      e.preventDefault();
      setPaletteOpen(true);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  const path = location.pathname;
  const matched = useMatches().flatMap((match) => crumbsOf(match.handle));
  const crumbParams = useMatches().reduce<Params>(
    (all, m) => ({ ...all, ...m.params }),
    {},
  );
  // Same key as the detail pages, so it is shared; never fetched before the owner signs in.
  const exampleApp = useQuery({
    queryKey: ["application", crumbParams.id],
    queryFn: () => api.app(crumbParams.id ?? ""),
    enabled:
      path.startsWith("/apps/example/") &&
      !!crumbParams.id &&
      capabilities.data?.mode === "demo",
  });
  const definitionApps = useQuery({
    queryKey: ["worker-apps"],
    queryFn: api.workerApps,
    enabled:
      path.startsWith("/apps/definition/") &&
      capabilities.workerReady &&
      (!experimental || signedIn),
  });
  const projectList = useQuery({
    queryKey: ["projects"],
    queryFn: api.workerProjects,
    enabled:
      path.startsWith("/projects/") &&
      capabilities.workerReady &&
      (!experimental || signedIn),
  });
  const appName = path.startsWith("/apps/definition/")
    ? definitionApps.data?.find((app) => app.id === crumbParams.id)?.name
    : path.startsWith("/projects/")
      ? projectList.data?.find((project) => project.id === crumbParams.id)?.name
      : exampleApp.data?.name;
  const projectsAvailable =
    capabilities.workerReady && (!experimental || signedIn);
  const crumbs = matched.map((crumb) => ({
    label: crumb.label(t, appName),
    to: crumb.to?.({ params: crumbParams }),
  }));
  const serverState = capabilities.isPending
    ? "checking"
    : capabilities.current
      ? "available"
      : "unavailable";
  const modeLabel = capabilities.data
    ? experimental
      ? t.modeExperimental
      : t.modeDemo
    : "";
  const serverLabel = {
    checking: t.serverChecking,
    available: t.serverAvailable,
    unavailable: t.serverUnavailable,
  }[serverState];
  return (
    <TooltipProvider>
      <div
        className={`app-shell ${rail ? "rail" : ""}`}
        onKeyDown={(e) => {
          if (
            e.key === "Escape" &&
            isMobile &&
            menuOpen &&
            !settingsOpen &&
            !paletteOpen
          )
            closeMenu();
        }}
      >
        <a href="#main-content" className="skip-link">
          {t.skip}
        </a>
        <aside
          className="sidebar"
          id="sidebar-navigation"
          hidden={isMobile && !menuOpen}
          aria-label={t.navigation}
        >
          <Link
            to="/"
            className="brand"
            onClick={() => {
              if (isMobile) setMenuOpen(false);
            }}
          >
            <BrandMark />
            <span className="brand-name">{t.brand}</span>
          </Link>
          <p className="nav-label" hidden={rail}>
            {t.overview}
          </p>
          <div className="px-2 pb-2 [.rail_&]:flex [.rail_&]:justify-center [.rail_&]:px-0">
            <RailTip rail={rail} label={t.palette.trigger}>
              <Button
                variant="ghost"
                className="sidebar-settings justify-between"
                aria-label={rail ? t.palette.trigger : undefined}
                aria-keyshortcuts="Control+K Meta+K"
                aria-haspopup="dialog"
                onClick={() => setPaletteOpen(true)}
              >
                <span className="flex items-center gap-2">
                  <Search data-icon="inline-start" />
                  {!rail && t.palette.trigger}
                </span>
                {!rail && (
                  <kbd className="rounded border px-1.5 font-sans text-xs text-muted-foreground max-[600px]:hidden">
                    {/Mac|iPhone|iPad/.test(navigator.platform)
                      ? "⌘K"
                      : "Ctrl K"}
                  </kbd>
                )}
              </Button>
            </RailTip>
          </div>
          <nav>
            <RailTip rail={rail} label={t.applications}>
              <NavLink
                to="/"
                end
                aria-label={rail ? t.applications : undefined}
                onClick={() => {
                  if (isMobile) setMenuOpen(false);
                }}
              >
                <Box />
                {!rail && t.applications}
                {!rail && (!experimental || signedIn) && <AppCount />}
              </NavLink>
            </RailTip>
            {!experimental && (
              <RailTip rail={rail} label={t.activity}>
                <NavLink
                  to="/activity"
                  aria-label={rail ? t.activity : undefined}
                  onClick={() => {
                    if (isMobile) setMenuOpen(false);
                  }}
                >
                  <Activity />
                  {!rail && t.activity}
                </NavLink>
              </RailTip>
            )}
          </nav>
          {projectsAvailable && (
            <ProjectsNav
              rail={rail}
              onNavigate={() => {
                if (isMobile) setMenuOpen(false);
              }}
              onNew={() => {
                if (isMobile) setMenuOpen(false);
                setNewProjectOpen(true);
              }}
            />
          )}
          <div className="sidebar-bottom">
            <Dialog open={settingsOpen} onOpenChange={setSettingsOpen}>
              <RailTip rail={rail} label={t.settingsTitle}>
                <DialogTrigger
                  render={
                    <Button
                      ref={settingsRef}
                      variant="ghost"
                      className="sidebar-settings"
                      aria-label={rail ? t.settingsTitle : undefined}
                    />
                  }
                >
                  <Settings2 data-icon="inline-start" />
                  {!rail && t.settingsTitle}
                </DialogTrigger>
              </RailTip>
              <DialogContent
                className="settings-dialog"
                finalFocus={() =>
                  settingsRef.current?.getClientRects().length
                    ? settingsRef.current
                    : menuRef.current
                }
              >
                <DialogHeader>
                  <DialogTitle>{t.settingsTitle}</DialogTitle>
                  <DialogDescription>{t.settingsDescription}</DialogDescription>
                </DialogHeader>
                <SettingsPanels
                  theme={theme}
                  setTheme={setTheme}
                  accent={accent}
                  setAccent={setAccent}
                  githubLink={githubLink}
                  setGithubLink={setGithubLink}
                  mobile={isMobile}
                  onNavigate={() => setSettingsOpen(false)}
                />
              </DialogContent>
            </Dialog>
            {githubLink && (
              <RailTip rail={rail} label={t.sourceLink}>
                <a
                  className="source-link"
                  href="https://github.com/ponytojas/darsena"
                  target="_blank"
                  rel="noreferrer"
                  aria-label={rail ? t.sourceLink : undefined}
                >
                  {!rail && t.sourceLink}
                  <ArrowUpRight />
                </a>
              </RailTip>
            )}
          </div>
        </aside>
        <div className="main-shell">
          <header className="topbar">
            <div className="breadcrumbs">
              <Button
                ref={menuRef}
                size="icon"
                variant="ghost"
                className="sidebar-toggle"
                aria-label={sidebarOpen ? t.closeMenu : t.menu}
                title={sidebarOpen ? t.closeMenu : t.menu}
                aria-controls="sidebar-navigation"
                aria-expanded={sidebarOpen}
                aria-keyshortcuts={isMobile ? undefined : "Control+B Meta+B"}
                onClick={toggleSidebar}
              >
                {isMobile ? (
                  sidebarOpen ? (
                    <X />
                  ) : (
                    <Menu />
                  )
                ) : sidebarOpen ? (
                  <PanelLeftClose />
                ) : (
                  <PanelLeftOpen />
                )}
              </Button>
              <nav aria-label={t.breadcrumbLabel} className="breadcrumb-trail">
                {crumbs.map((crumb, index) => (
                  <span className="breadcrumb-item" key={index}>
                    {index > 0 && <ChevronRight aria-hidden="true" />}
                    {index < crumbs.length - 1 && crumb.to ? (
                      <Link to={crumb.to}>{crumb.label}</Link>
                    ) : (
                      <span aria-current="page">{crumb.label}</span>
                    )}
                  </span>
                ))}
              </nav>
            </div>
            <div className="top-actions">
              <Link
                to="/diagnostics"
                className="mode-pill"
                data-state={serverState}
              >
                <span className="mode-pill-dot" aria-hidden="true" />
                {modeLabel
                  ? `${modeLabel} · ${serverLabel}`
                  : serverState === "checking"
                    ? t.serverStatusChecking
                    : t.serverStatusUnavailable}
              </Link>
              {experimental && signedIn && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    void logout().finally(() => client.clear());
                  }}
                >
                  {t.logout}
                </Button>
              )}
            </div>
          </header>
          {capabilities.data?.mode === "demo" && (
            <div className="demo-banner" role="region" aria-label={t.demo}>
              <p>{t.demoNotice}</p>
            </div>
          )}
          <main id="main-content" tabIndex={-1} ref={mainRef}>
            {!capabilities.data ? (
              capabilities.isPending ? (
                <Loading />
              ) : (
                <section className="page">
                  <p role="alert">{t.serverCheckFailed}</p>
                  <Button
                    variant="outline"
                    onClick={() => void capabilities.refetch()}
                  >
                    {t.recheck}
                  </Button>
                </section>
              )
            ) : experimental && !signedIn ? (
              <section className="page" aria-labelledby="owner-login">
                <PageTitleLogin />
                <form
                  onSubmit={submitLogin}
                  style={{ maxWidth: "26rem", display: "grid", gap: "1rem" }}
                >
                  <label htmlFor="owner-credential">{t.credentialLabel}</label>
                  <Input
                    id="owner-credential"
                    type="password"
                    autoComplete="off"
                    required
                    value={credential}
                    onChange={(e) => setCredential(e.target.value)}
                  />
                  {loginError && <p role="alert">{t.loginFailed}</p>}
                  <Button type="submit" disabled={loginPending}>
                    {loginPending ? t.loginPending : t.loginAction}
                  </Button>
                </form>
              </section>
            ) : (
              <Suspense fallback={<Loading />}>
                <Outlet />
              </Suspense>
            )}
          </main>
        </div>
        <CommandPalette
          open={paletteOpen}
          onOpenChange={setPaletteOpen}
          showActivity={!experimental}
          canCreate={
            capabilities.data?.mode === "demo" || capabilities.workerReady
          }
          loadApplications={!experimental || signedIn}
          canCreateProject={projectsAvailable}
          onNewProject={() => setNewProjectOpen(true)}
          setTheme={setTheme}
        />
        {projectsAvailable && (
          <ProjectFormDialog
            open={newProjectOpen}
            onOpenChange={setNewProjectOpen}
          />
        )}
        <Toaster
          theme={theme}
          position="bottom-right"
          duration={10_000}
          closeButton
          containerAriaLabel={t.projects.notifications}
        />
      </div>
    </TooltipProvider>
  );
}
function PageTitleLogin() {
  const { t } = useI18n();
  return (
    <div>
      <h1 id="owner-login">{t.loginTitle}</h1>
      <p>{t.loginDescription}</p>
    </div>
  );
}
function DemoUnavailable() {
  const { t } = useI18n();
  return (
    <div className="page">
      <Blank
        title={t.viewUnavailable}
        description={t.viewUnavailableDescription}
        action={<Button render={<Link to="/" />}>{t.backHome}</Button>}
      />
    </div>
  );
}
function DemoOnly({ children }: { children: ReactNode }) {
  const capabilities = useCapabilities();
  return capabilities.data?.mode === "experimental" ? (
    <DemoUnavailable />
  ) : (
    children
  );
}
/** Legacy saved-applications URLs now live in the unified list and the definition detail. */
function RegisteredRedirect() {
  const [params] = useSearchParams();
  const app = params.get("app");
  return (
    <Navigate
      to={app ? `/apps/definition/${encodeURIComponent(app)}` : "/"}
      replace
    />
  );
}
/** Legacy example URL. */
function ApplicationRedirect() {
  const { id = "" } = useParams();
  return <Navigate to={`/apps/example/${encodeURIComponent(id)}`} replace />;
}
/** Legacy operation URL: find the owning application, then move to its nested route. */
function OperationRedirect() {
  const { t } = useI18n();
  const { id = "" } = useParams();
  const operation = useQuery({
    queryKey: ["operation", id],
    queryFn: () => api.operation(id),
    retry: false,
  });
  if (operation.isPending) return <Loading />;
  if (operation.isError)
    return (
      <div className="page">
        {operation.error instanceof ApiError &&
        operation.error.status === 404 ? (
          <Blank
            title={t.operationNotFound}
            description={t.operationNotFoundDescription}
            action={<Button render={<Link to="/" />}>{t.backHome}</Button>}
          />
        ) : (
          <Failure
            error={operation.error}
            retry={() => void operation.refetch()}
          />
        )}
      </div>
    );
  return (
    <Navigate
      to={`/apps/example/${encodeURIComponent(operation.data.application_id)}/deployments/${encodeURIComponent(id)}`}
      replace
    />
  );
}
function NotFound() {
  const { t } = useI18n();
  return (
    <Blank
      title={t.notFound}
      description={t.notFoundDescription}
      action={<Button render={<Link to="/" />}>{t.backHome}</Button>}
    />
  );
}
const appsCrumb: Crumb = { label: (t) => t.applications, to: () => "/" };
const exampleCrumb: Crumb = {
  label: (t, name) => name ?? t.applicationDetailCrumb,
};
const activityCrumb: Crumb = {
  label: (t) => t.activity,
  to: () => "/activity",
};
const router = createBrowserRouter([
  {
    path: "/",
    element: <Shell />,
    children: [
      {
        index: true,
        element: <Applications />,
        handle: { crumb: [appsCrumb] },
      },
      {
        path: "activity",
        element: (
          <DemoOnly>
            <ActivityPage />
          </DemoOnly>
        ),
        handle: { crumb: [activityCrumb] },
      },
      {
        path: "registered",
        element: <RegisteredRedirect />,
      },
      {
        path: "diagnostics",
        element: <Diagnostics />,
        handle: { crumb: [{ label: (t) => t.diagnosticsTitle }] },
      },
      {
        path: "apps/new",
        element: <NewApp />,
        handle: { crumb: [appsCrumb, { label: (t) => t.newApp }] },
      },
      {
        path: "apps/example/:id/:tab?",
        element: (
          <DemoOnly>
            <ExampleDetail />
          </DemoOnly>
        ),
        handle: { crumb: [appsCrumb, exampleCrumb] },
      },
      {
        path: "apps/example/:id/deployments/:opId",
        element: (
          <DemoOnly>
            <OperationDetail />
          </DemoOnly>
        ),
        handle: {
          crumb: [
            appsCrumb,
            {
              ...exampleCrumb,
              to: ({ params }) =>
                `/apps/example/${encodeURIComponent(params.id ?? "")}`,
            },
            { label: (t) => t.deploymentCrumb },
          ],
        },
      },
      {
        path: "apps/definition/:id/:tab?",
        element: <DefinitionDetail />,
        handle: {
          crumb: [appsCrumb, { label: (t, name) => name ?? t.definitionCrumb }],
        },
      },
      {
        path: "projects/:id",
        element: <ProjectPage />,
        handle: {
          crumb: [appsCrumb, { label: (t, name) => name ?? t.projects.crumb }],
        },
      },
      {
        path: "applications/new",
        element: <Navigate to="/apps/new" replace />,
      },
      {
        path: "applications/:id",
        element: <ApplicationRedirect />,
      },
      {
        path: "operations/:id",
        element: (
          <DemoOnly>
            <OperationRedirect />
          </DemoOnly>
        ),
      },
      {
        path: "*",
        element: <NotFound />,
        handle: { crumb: [appsCrumb] },
      },
    ],
  },
]);
export default function App() {
  return (
    <I18nProvider>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nProvider>
  );
}
