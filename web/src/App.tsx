import { lazy, Suspense, useEffect, useRef, useState } from "react";
import {
  BrowserRouter,
  Link,
  NavLink,
  Route,
  Routes,
  useLocation,
} from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  Activity,
  ArrowUpRight,
  Box,
  ChevronRight,
  Menu,
  Monitor,
  Moon,
  Plus,
  Sun,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Separator } from "@/components/ui/separator";
import { Loading, Blank } from "@/components/shared";
import { useTheme, type Theme } from "@/lib/theme";
import { es as t } from "@/i18n/es";
import Home from "@/pages/Home";
const NewApplication = lazy(() => import("@/pages/NewApplication"));
const ApplicationDetail = lazy(() => import("@/pages/ApplicationDetail"));
const OperationDetail = lazy(() => import("@/pages/OperationDetail"));
const ActivityPage = lazy(() => import("@/pages/Activity"));
const client = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 5_000, retry: 1, refetchOnWindowFocus: true },
  },
});
function Shell() {
  const location = useLocation();
  const [theme, setTheme] = useTheme();
  const [menuOpen, setMenuOpen] = useState(false);
  const mainRef = useRef<HTMLElement>(null);
  const menuRef = useRef<HTMLButtonElement>(null);
  const previousPath = useRef(location.pathname);
  useEffect(() => {
    if (previousPath.current !== location.pathname) {
      previousPath.current = location.pathname;
      const frame = requestAnimationFrame(() => mainRef.current?.focus());
      return () => cancelAnimationFrame(frame);
    }
  }, [location.pathname]);
  const closeMenu = () => {
    setMenuOpen(false);
    menuRef.current?.focus();
  };
  const section =
    location.pathname === "/activity" ? t.activity : t.applications;
  return (
    <div className="app-shell">
      <a href="#main-content" className="skip-link">
        {t.skip}
      </a>
      <aside
        className={`sidebar ${menuOpen ? "is-open" : ""}`}
        aria-label={t.navigation}
        onKeyDown={(e) => {
          if (e.key === "Escape") closeMenu();
        }}
      >
        <Link to="/" className="brand" onClick={() => setMenuOpen(false)}>
          <span className="brand-mark" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
          <span>
            {t.brand}
            <small>{t.demoVersion}</small>
          </span>
        </Link>
        <div className="workspace">
          <span className="workspace-avatar">D</span>
          <span>
            <strong>{t.workspace}</strong>
            <small>{t.local}</small>
          </span>
          <span className="local-indicator" aria-hidden="true" />
        </div>
        <p className="nav-label">{t.overview}</p>
        <nav>
          <NavLink to="/" end onClick={() => setMenuOpen(false)}>
            <Box />
            {t.applications}
          </NavLink>
          <NavLink to="/activity" onClick={() => setMenuOpen(false)}>
            <Activity />
            {t.activity}
          </NavLink>
          <NavLink to="/applications/new" onClick={() => setMenuOpen(false)}>
            <Plus />
            {t.newApp}
          </NavLink>
        </nav>
        <div className="sidebar-bottom">
          <div className="local-note">
            <Monitor />
            <span>
              {t.localOnly}
              <small>{t.noPersistence}</small>
            </span>
          </div>
          <Separator />
          <div className="theme-control">
            <span>{t.theme}</span>
            <ToggleGroup
              aria-label={t.theme}
              value={[theme]}
              onValueChange={(values) => {
                if (values[0]) setTheme(values[0] as Theme);
              }}
              spacing={0}
            >
              {(
                [
                  { value: "system", Icon: Monitor },
                  { value: "light", Icon: Sun },
                  { value: "dark", Icon: Moon },
                ] as const
              ).map(({ value, Icon }) => (
                <ToggleGroupItem
                  key={value}
                  value={value}
                  aria-label={t.themeLabels[value]}
                  title={t.themeLabels[value]}
                >
                  <Icon />
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>
          <a
            className="source-link"
            href="https://github.com/ponytojas/darsena"
            target="_blank"
            rel="noreferrer"
          >
            Dársena en GitHub
            <ArrowUpRight />
          </a>
        </div>
      </aside>
      <div className="main-shell">
        <header className="topbar">
          <div className="breadcrumbs">
            <Button
              ref={menuRef}
              size="icon"
              variant="ghost"
              className="mobile-menu"
              aria-label={menuOpen ? t.closeMenu : t.menu}
              aria-expanded={menuOpen}
              onClick={() => setMenuOpen(!menuOpen)}
            >
              {menuOpen ? <X /> : <Menu />}
            </Button>
            <span>{t.workspace}</span>
            <ChevronRight />
            <Link to={section === t.activity ? "/activity" : "/"}>
              {section}
            </Link>
          </div>
          <Badge variant="outline">{t.demo}</Badge>
        </header>
        <div className="demo-banner" role="region" aria-label={t.demo}>
          <span className="demo-dot" aria-hidden="true" />
          <p>{t.demoNotice}</p>
        </div>
        <main id="main-content" tabIndex={-1} ref={mainRef}>
          <Suspense fallback={<Loading />}>
            <Routes>
              <Route path="/" element={<Home />} />
              <Route path="/activity" element={<ActivityPage />} />
              <Route path="/applications/new" element={<NewApplication />} />
              <Route path="/applications/:id" element={<ApplicationDetail />} />
              <Route path="/operations/:id" element={<OperationDetail />} />
              <Route
                path="*"
                element={
                  <Blank
                    title={t.notFound}
                    description={t.notFoundDescription}
                    action={
                      <Button render={<Link to="/" />}>{t.backHome}</Button>
                    }
                  />
                }
              />
            </Routes>
          </Suspense>
        </main>
        <footer className="main-footer">
          <span>{t.tagline}</span>
          <span>{t.demoShort}</span>
        </footer>
      </div>
    </div>
  );
}
export default function App() {
  return (
    <QueryClientProvider client={client}>
      <BrowserRouter>
        <Shell />
      </BrowserRouter>
    </QueryClientProvider>
  );
}
