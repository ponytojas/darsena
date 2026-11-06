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
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import type { StatusKey } from "@/i18n/es";
import type { Health } from "@/lib/status";
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

const healthStyle: Record<
  Health,
  { icon: typeof Check; text: string; bar: string }
> = {
  ok: { icon: Check, text: "text-success", bar: "bg-success" },
  progress: { icon: LoaderCircle, text: "text-primary", bar: "bg-primary" },
  attention: {
    icon: AlertCircle,
    text: "text-destructive",
    bar: "bg-destructive",
  },
  idle: { icon: Circle, text: "text-muted-foreground", bar: "bg-border" },
};
/** Pass either a schema `status` (label derived) or a `health` with its own `label`. */
export function Status(
  props: { status: StatusKey } | { health: Health; label: string },
) {
  const { t } = useI18n();
  if ("health" in props) {
    const { icon: Icon, text } = healthStyle[props.health];
    return (
      <Badge variant="outline" data-health={props.health} className={text}>
        <Icon aria-hidden="true" />
        {props.label}
      </Badge>
    );
  }
  const { status } = props;
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
/** Decorative vertical stripe; the status text always accompanies it. */
export function Berth({
  health,
  className = "",
}: {
  health: Health;
  className?: string;
}) {
  return (
    <span
      aria-hidden="true"
      data-health={health}
      className={`block w-1 self-stretch rounded-full ${healthStyle[health].bar} ${className}`}
    />
  );
}
export function PageTitle({
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
        <h1 tabIndex={-1}>{title}</h1>
        {description && <p className="page-description">{description}</p>}
      </div>
      {action}
    </div>
  );
}
export function NewAppButton() {
  const { t } = useI18n();
  return (
    <Button render={<Link to="/apps/new" />}>
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
  const { t } = useI18n();
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>{t.unavailable}</AlertTitle>
      <AlertDescription>
        <p>{errorMessage(error, t)}</p>
        <Button variant="outline" onClick={retry}>
          {t.retry}
        </Button>
      </AlertDescription>
    </Alert>
  );
}
export function Loading() {
  const { t } = useI18n();
  return (
    <div className="loading-state" role="status" aria-label={t.loading}>
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
export function OperationRows({
  operations,
  apps = [],
}: {
  operations: Operation[];
  apps?: Application[];
}) {
  const { t, locale } = useI18n();
  return (
    <div className="operation-list">
      {operations.map((op) => (
        <Link
          className="operation-row"
          to={`/apps/example/${encodeURIComponent(op.application_id)}/deployments/${op.id}`}
          key={op.id}
        >
          <span className="operation-icon">
            <ArrowUpRight aria-hidden="true" />
          </span>
          <span className="operation-name">
            <strong>
              {apps.find((a) => a.id === op.application_id)?.name ||
                t.demoDeploy}
            </strong>
            <small>
              {formatDateTime(op.created_at, locale)} ·{" "}
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
