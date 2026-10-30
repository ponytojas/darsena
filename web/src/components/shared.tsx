import type { ReactNode } from "react";
import { Link } from "react-router";
import {
  AlertCircle,
  ArrowUpRight,
  Box,
  Check,
  ChevronRight,
  Circle,
  LoaderCircle,
  Plus,
} from "lucide-react";
import { shortDate } from "@/lib/format";
import { es as t } from "@/i18n/es";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
  EmptyMedia,
} from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import type { Application, Operation } from "@/api/client";

export function Status({ status }: { status: keyof typeof t.statusLabels }) {
  const Icon =
    status === "healthy" || status === "succeeded"
      ? Check
      : status === "failed" || status === "interrupted"
        ? AlertCircle
        : status === "running"
          ? LoaderCircle
          : Circle;
  return (
    <Badge variant="outline" data-status={status}>
      <Icon aria-hidden="true" />
      {t.statusLabels[status]}
    </Badge>
  );
}
export function PageTitle({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="page-title">
      <div>
        {eyebrow && <p className="eyebrow">{eyebrow}</p>}
        <h1 tabIndex={-1}>{title}</h1>
        {description && <p className="page-description">{description}</p>}
      </div>
      {action}
    </div>
  );
}
export function NewAppButton() {
  return (
    <Button render={<Link to="/applications/new" />}>
      <Plus data-icon="inline-start" />
      {t.newApp}
    </Button>
  );
}
export function Failure({
  error,
  retry,
}: {
  error: Error | null;
  retry: () => void;
}) {
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>{t.unavailable}</AlertTitle>
      <AlertDescription>
        <p>{error?.message || t.connectionError}</p>
        <Button variant="outline" onClick={retry}>
          {t.retry}
        </Button>
      </AlertDescription>
    </Alert>
  );
}
export function Loading() {
  return (
    <div className="loading-state" role="status" aria-label="Cargando">
      <Skeleton className="h-8 w-48" />
      <Skeleton className="h-16 w-full" />
      <Skeleton className="h-16 w-full" />
    </div>
  );
}
export function Blank({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Box />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
      {action}
    </Empty>
  );
}
export function ApplicationRows({ apps }: { apps: Application[] }) {
  return (
    <div className="resource-list">
      <div className="resource-header">
        <span>{t.application}</span>
        <span>{t.state}</span>
        <span>{t.version}</span>
        <span />
      </div>
      {apps.map((app) => (
        <Link
          key={app.id}
          className="resource-row"
          to={`/applications/${app.id}`}
        >
          <span className="resource-identity">
            <span className="app-icon">
              <Box aria-hidden="true" />
            </span>
            <span className="min-w-0">
              <strong>{app.name}</strong>
              <small>
                {app.repository} <span aria-hidden="true">/</span> {app.branch}
              </small>
            </span>
          </span>
          <Status status={app.status} />
          <span className="mono version">
            {app.version ? `v${app.version}` : "—"}
          </span>
          <ChevronRight className="row-chevron" aria-hidden="true" />
        </Link>
      ))}
    </div>
  );
}
export function OperationRows({
  operations,
  apps = [],
}: {
  operations: Operation[];
  apps?: Application[];
}) {
  return (
    <div className="operation-list">
      {operations.map((op) => (
        <Link className="operation-row" to={`/operations/${op.id}`} key={op.id}>
          <span className="operation-icon">
            <ArrowUpRight aria-hidden="true" />
          </span>
          <span className="operation-name">
            <strong>
              {apps.find((a) => a.id === op.application_id)?.name ||
                t.demoDeploy}
            </strong>
            <small>
              {shortDate(op.created_at)} ·{" "}
              <span className="mono">{op.id.slice(0, 8)}</span>
            </small>
          </span>
          <Status status={op.status} />
          <ChevronRight className="row-chevron" aria-hidden="true" />
        </Link>
      ))}
    </div>
  );
}
