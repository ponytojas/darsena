import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { AlertCircle, ChevronRight, Play } from "lucide-react";
import { api } from "@/api/client";
import { errorMessage } from "@/i18n/errors";
import { useI18n } from "@/i18n/context";
import { formatRelative } from "@/lib/format";
import { exampleHealth } from "@/lib/status";
import { Blank, Failure, Loading, OperationRows, Status } from "@/components/shared";
import { DetailTabs } from "@/components/detail";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

export default function ExampleDetail() {
  const { t, locale } = useI18n();
  const { id = "", tab } = useParams();
  const navigate = useNavigate();
  const cache = useQueryClient();
  const base = `/apps/example/${encodeURIComponent(id)}`;
  const app = useQuery({ queryKey: ["application", id], queryFn: () => api.app(id) });
  const operations = useQuery({
    queryKey: ["operations"],
    queryFn: api.operations,
    refetchInterval: (query) =>
      query.state.data?.some((op) => op.status === "queued" || op.status === "running") ? 2000 : false,
  });
  const deploy = useMutation({
    mutationFn: () => api.deploy(id),
    onSuccess: (op) => {
      void cache.invalidateQueries({ queryKey: ["operations"] });
      navigate(`${base}/deployments/${op.id}`);
    },
  });
  if (tab !== undefined && tab !== "deployments") return <Navigate to={base} replace />;
  if (app.isPending) return <Loading />;
  if (app.isError)
    return (
      <div className="page">
        <Failure error={app.error} retry={() => void app.refetch()} />
      </div>
    );
  const a = app.data;
  const history = (operations.data ?? [])
    .filter((o) => o.application_id === a.id)
    .sort((x, y) => (x.created_at < y.created_at ? 1 : -1));
  const latest = history[0];
  const active = history.some((o) => o.status === "queued" || o.status === "running");
  const health = exampleHealth(a, history);
  const healthLabel = t.healthLabels[health];
  const historyState = operations.isPending ? (
    <Loading />
  ) : operations.isError ? (
    <Failure error={operations.error} retry={() => void operations.refetch()} />
  ) : null;
  const details: [string, string, boolean?][] = [
    [t.repository, a.repository],
    [t.branch, a.branch],
    [t.context, a.context, true],
    [t.port, String(a.port), true],
  ];

  return (
    <div className="page">
      <header className="grid gap-2">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <h1 tabIndex={-1} className="text-2xl font-semibold tracking-tight">
            {a.name}
          </h1>
          <Badge variant="secondary">{t.kindExample}</Badge>
          <Status health={health} label={healthLabel} />
        </div>
        <p className="font-mono text-xs break-all text-muted-foreground">
          {a.repository} · {a.branch}
        </p>
      </header>
      <DetailTabs
        label={t.definitions.tabsLabel}
        tabs={[
          { to: base, label: t.definitions.tabSummary },
          { to: `${base}/deployments`, label: t.tabDeployments },
        ]}
      />
      {tab === "deployments" ? (
        <section aria-labelledby="history-title" className="mt-8 grid gap-3">
          <h2 id="history-title" className="text-sm font-semibold">
            {t.appHistory}
          </h2>
          {historyState ??
            (history.length ? (
              <OperationRows operations={history} apps={[a]} />
            ) : (
              <Blank title={t.noDeployments} description={t.noDeploymentsDescription} />
            ))}
        </section>
      ) : (
        <div className="mt-8 grid gap-8">
          <div className="flex flex-wrap items-center gap-3">
            <Button disabled={deploy.isPending || active} onClick={() => deploy.mutate()}>
              <Play data-icon="inline-start" />
              {t.deploy}
            </Button>
          </div>
          {deploy.isError && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>{t.unavailable}</AlertTitle>
              <AlertDescription>{errorMessage(deploy.error, t)}</AlertDescription>
            </Alert>
          )}
          <section aria-labelledby="latest-title" className="grid gap-3">
            <h2 id="latest-title" className="text-sm font-semibold">
              {t.latestDeployment}
            </h2>
            {historyState ??
              (latest ? (
                <Link
                  to={`${base}/deployments/${latest.id}`}
                  className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-4 text-sm outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Status status={latest.status} />
                  <span className="text-muted-foreground">{formatRelative(latest.created_at, locale)}</span>
                  <span className="ml-auto inline-flex items-center gap-1">
                    {t.viewDeployment}
                    <ChevronRight className="size-4" aria-hidden="true" />
                  </span>
                </Link>
              ) : (
                <p className="text-sm text-muted-foreground">{t.noDeploymentsDescription}</p>
              ))}
          </section>
          <section aria-labelledby="details-title" className="grid gap-3">
            <h2 id="details-title" className="text-sm font-semibold">
              {t.details}
            </h2>
            <dl className="grid gap-4 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs text-muted-foreground">{t.simulatedURL}</dt>
                <dd className="font-mono break-all">{a.url}</dd>
                <dd className="text-xs text-muted-foreground">{t.nonNavigable}</dd>
              </div>
              {details.map(([label, value, mono]) => (
                <div key={label}>
                  <dt className="text-xs text-muted-foreground">{label}</dt>
                  <dd className={mono ? "font-mono break-all" : "break-all"}>{value}</dd>
                </div>
              ))}
            </dl>
          </section>
        </div>
      )}
    </div>
  );
}
