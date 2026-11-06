import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router";
import {
  AlertCircle,
  ArrowLeft,
  Check,
  CheckCircle,
  Circle,
  LoaderCircle,
  Pause,
  Play,
  Radio,
  RefreshCw,
  Unplug,
} from "lucide-react";
import { api, terminal, type Operation } from "@/api/client";
import { formatDateTime, formatTime } from "@/lib/format";
import { errorMessage, operationError, stepName } from "@/i18n/errors";
import { useI18n } from "@/i18n/context";
import { PageTitle, Status, Loading, Failure } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
function Logs({
  operation,
  connection,
}: {
  operation: Operation;
  connection?: { paused: boolean; toggle: () => void };
}) {
  const { t, locale } = useI18n();
  const [paused, setPaused] = useState(false);
  const [snapshot, setSnapshot] = useState(operation.logs);
  const [search, setSearch] = useState("");
  const region = useRef<HTMLDivElement>(null);
  const logs = (paused ? snapshot : operation.logs)
    .slice(-200)
    .filter((line) =>
      line.message.toLowerCase().includes(search.toLowerCase()),
    );
  const following = useRef(true);
  useEffect(() => {
    if (!paused && following.current && region.current)
      region.current.scrollTop = region.current.scrollHeight;
  }, [operation.revision, paused]);
  return (
    <section className="log-section">
      <div className="log-toolbar">
        <h2>{t.logs}</h2>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            if (!paused) setSnapshot(operation.logs);
            setPaused(!paused);
          }}
        >
          {paused ? (
            <Play data-icon="inline-start" />
          ) : (
            <Pause data-icon="inline-start" />
          )}
          {paused ? t.resume : t.pause}
        </Button>
      </div>
      <label className="log-search">
        <span className="sr-only">{t.searchLogs}</span>
        <Input
          type="search"
          placeholder={t.searchLogs}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </label>
      <div
        className="log-view"
        ref={region}
        tabIndex={0}
        role="region"
        aria-label={t.logs}
        onScroll={() => {
          const el = region.current;
          if (el)
            following.current =
              el.scrollHeight - el.scrollTop - el.clientHeight < 32;
        }}
      >
        <ol>
          {logs.map((line) => (
            <li key={line.sequence}>
              <span className="log-number" aria-hidden="true">
                {String(line.sequence).padStart(2, "0")}
              </span>
              <time dateTime={line.time}>
                {formatTime(line.time, locale)}
              </time>
              <span>{line.message}</span>
            </li>
          ))}
        </ol>
        {!logs.length && <p>{t.noLogs}</p>}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border px-3.5 py-1.5">
        <p className="text-xs text-muted-foreground">{t.logBuffer}</p>
        {connection && (
          <Button size="sm" variant="ghost" onClick={connection.toggle}>
            <Unplug data-icon="inline-start" />
            {connection.paused ? t.resumeConnection : t.pauseConnection}
          </Button>
        )}
      </div>
    </section>
  );
}
export default function OperationDetail() {
  const { t, locale } = useI18n();
  const params = useParams();
  const id = params.opId ?? params.id ?? "";
  const cache = useQueryClient();
  const navigate = useNavigate();
  const operation = useQuery({
    queryKey: ["operation", id],
    queryFn: () => api.operation(id),
  });
  const [pausedConnection, setPausedConnection] = useState(false);
  const [connection, setConnection] = useState("connecting");
  const app = useQuery({
    queryKey: ["application", operation.data?.application_id],
    queryFn: () => api.app(operation.data!.application_id),
    enabled: !!operation.data,
  });
  const hasData = !!operation.data;
  const done = !!operation.data && terminal(operation.data.status);
  useEffect(() => {
    if (pausedConnection || done || !hasData) return;
    const events = new EventSource(`/api/v1/operations/${id}/events`);
    events.onopen = () => {
      setConnection("connected");
      void cache.invalidateQueries({ queryKey: ["operation", id] });
    };
    events.addEventListener("snapshot", (event) => {
      try {
        const data = JSON.parse((event as MessageEvent).data) as Operation;
        if (
          data.id !== id ||
          !Array.isArray(data.steps) ||
          !Array.isArray(data.logs)
        )
          return;
        cache.setQueryData<Operation>(["operation", id], (previous) =>
          !previous || data.revision >= previous.revision ? data : previous,
        );
        if (terminal(data.status)) {
          events.close();
          void cache.invalidateQueries({ queryKey: ["applications"] });
          void cache.invalidateQueries({
            queryKey: ["application", data.application_id],
          });
          void cache.invalidateQueries({ queryKey: ["operations"] });
        }
      } catch {
        setConnection("reconnecting");
      }
    });
    events.onerror = () => {
      setConnection("reconnecting");
    };
    return () => events.close();
  }, [id, pausedConnection, done, hasData, cache]);
  const retry = useMutation({
    mutationFn: () => api.deploy(operation.data!.application_id),
    onSuccess: (op) => {
      setPausedConnection(false);
      setConnection("connecting");
      void cache.invalidateQueries({ queryKey: ["operations"] });
      navigate(`/apps/example/${encodeURIComponent(op.application_id)}/deployments/${op.id}`);
    },
  });
  if (operation.isPending) return <Loading />;
  if (operation.isError)
    return (
      <div className="page">
        <Failure
          error={operation.error}
          retry={() => void operation.refetch()}
        />
      </div>
    );
  const op = operation.data;
  const appBase = `/apps/example/${encodeURIComponent(op.application_id)}`;
  return (
    <div className="page operation-page">
      <Link className="back-link" to={`${appBase}/deployments`}>
        <ArrowLeft />
        {t.tabDeployments}
      </Link>
      <PageTitle
        eyebrow={`${t.operation} / ${op.id.slice(0, 8)}`}
        title={t.deploymentTitle}
        description={
          app.data ? `${app.data.name} · ${app.data.branch}` : t.demoDeploy
        }
        action={<Status status={op.status} />}
      />
      <div className="deployment-summary" aria-live="polite">
        <span>{t.statusDescription[op.status]}</span>
        <time dateTime={op.created_at}>{formatDateTime(op.created_at, locale)}</time>
      </div>
      {!done && (
        <div className="connection-bar">
          <span>
            <Radio />
            {pausedConnection
              ? t.disconnected
              : connection === "connected"
                ? t.connected
                : t.reconnecting}
          </span>
        </div>
      )}
      {pausedConnection && !done && (
        <Alert>
          <AlertTitle>{t.disconnected}</AlertTitle>
          <AlertDescription>{t.reconnectHelp}</AlertDescription>
        </Alert>
      )}
      {op.status === "failed" || op.status === "interrupted" ? (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>{t.statusLabels[op.status]}</AlertTitle>
          <AlertDescription>
            <p>{operationError(op, t)}</p>
            <p>{t.priorVersion}</p>
            <Button
              variant="outline"
              disabled={retry.isPending}
              onClick={() => retry.mutate()}
            >
              <RefreshCw data-icon="inline-start" />
              {t.retry}
            </Button>
            {retry.isError && <p role="alert">{errorMessage(retry.error, t)}</p>}
          </AlertDescription>
        </Alert>
      ) : null}
      {op.status === "succeeded" && (
        <Alert className="success-alert">
          <CheckCircle />
          <AlertTitle>{t.successTitle}</AlertTitle>
          <AlertDescription>
            <p><strong>{t.simulatedURL}:</strong> <code>{app.data?.url || "—"}</code></p>
            <p>{t.nonNavigable}</p>
            <p><Link to={appBase}>{t.backApp}</Link></p>
          </AlertDescription>
        </Alert>
      )}
      <div className="deployment-layout">
        <section className="stage-panel">
          <div className="section-heading">
            <h2>{t.stages}</h2>
            <span>
              {op.steps.filter((s) => s.status === "succeeded").length}/
              {op.steps.length}
            </span>
          </div>
          <ol className="stage-list">
            {op.steps.map((step, index) => {
              const Icon =
                step.status === "succeeded"
                  ? Check
                  : step.status === "failed"
                    ? AlertCircle
                    : step.status === "running"
                      ? LoaderCircle
                      : Circle;
              return (
                <li key={step.key || step.name} data-status={step.status}>
                  <span className="stage-icon">
                    <Icon aria-hidden="true" />
                  </span>
                  <span>
                    <strong>{stepName(step, t)}</strong>
                    <small>
                      {step.status === "failed"
                        ? operationError(op, t)
                        : step.status === "succeeded"
                          ? t.stepDone
                          : step.status === "running"
                            ? t.statusLabels.running
                            : t.statusLabels.pending}
                    </small>
                  </span>
                  <span className="stage-number">
                    {String(index + 1).padStart(2, "0")}
                  </span>
                </li>
              );
            })}
          </ol>
        </section>
        <Logs
          operation={op}
          connection={
            done
              ? undefined
              : {
                  paused: pausedConnection,
                  toggle: () => {
                    setPausedConnection(!pausedConnection);
                    if (pausedConnection) {
                      setConnection("connecting");
                      void operation.refetch();
                    }
                  },
                }
          }
        />
      </div>
    </div>
  );
}
