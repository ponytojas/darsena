import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router";
import { AlertCircle, ArrowLeft, GitBranch, Play } from "lucide-react";
import { api } from "@/api/client";
import { es as t } from "@/i18n/es";
import {
  PageTitle,
  Loading,
  Failure,
  Blank,
  Status,
  OperationRows,
} from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
export default function ApplicationDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const cache = useQueryClient();
  const app = useQuery({
    queryKey: ["application", id],
    queryFn: () => api.app(id),
  });
  const operations = useQuery({
    queryKey: ["operations"],
    queryFn: api.operations,
    refetchInterval: (query) =>
      query.state.data?.some(
        (op) => op.status === "queued" || op.status === "running",
      )
        ? 2000
        : false,
  });
  const deploy = useMutation({
    mutationFn: () => api.deploy(id),
    onSuccess: (op) => {
      void cache.invalidateQueries({ queryKey: ["operations"] });
      navigate(`/operations/${op.id}`);
    },
  });
  if (app.isPending) return <Loading />;
  if (app.isError)
    return (
      <div className="page">
        <Failure error={app.error} retry={() => void app.refetch()} />
      </div>
    );
  const a = app.data;
  const history =
    operations.data?.filter((o) => o.application_id === a.id) || [];
  const active = history.some(
    (o) => o.status === "queued" || o.status === "running",
  );
  return (
    <div className="page">
      <Link className="back-link" to="/">
        <ArrowLeft />
        {t.applications}
      </Link>
      <PageTitle
        eyebrow={t.application}
        title={a.name}
        description={a.repository}
        action={
          <Button
            disabled={deploy.isPending || active}
            onClick={() => deploy.mutate()}
          >
            <Play data-icon="inline-start" />
            {t.deploy}
          </Button>
        }
      />
      {deploy.isError && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>{t.unavailable}</AlertTitle>
          <AlertDescription>{deploy.error.message}</AlertDescription>
        </Alert>
      )}
      <div className="app-overview">
        <Status status={a.status} />
        <span className="mono">{a.version ? `v${a.version}` : "—"}</span>
        <span className="branch-label">
          <GitBranch />
          {a.branch}
        </span>
      </div>
      <div className="detail-layout">
        <section>
          <div className="section-heading">
            <h2>{t.appHistory}</h2>
          </div>
          {operations.isPending ? (
            <Loading />
          ) : operations.isError ? (
            <Failure
              error={operations.error}
              retry={() => void operations.refetch()}
            />
          ) : history.length ? (
            <OperationRows operations={history} apps={[a]} />
          ) : (
            <Blank
              title={t.noDeployments}
              description={t.noDeploymentsDescription}
            />
          )}
        </section>
        <aside className="inspector">
          <h2>{t.details}</h2>
          <dl>
            <div>
              <dt>{t.simulatedURL}</dt>
              <dd className="mono">{a.url}</dd>
              <dd className="muted">{t.nonNavigable}</dd>
            </div>
            <div>
              <dt>{t.repository}</dt>
              <dd>{a.repository}</dd>
            </div>
            <div>
              <dt>{t.branch}</dt>
              <dd>{a.branch}</dd>
            </div>
            <div>
              <dt>{t.context}</dt>
              <dd className="mono">{a.context}</dd>
            </div>
            <div>
              <dt>{t.port}</dt>
              <dd className="mono">{a.port}</dd>
            </div>
          </dl>
        </aside>
      </div>
    </div>
  );
}
