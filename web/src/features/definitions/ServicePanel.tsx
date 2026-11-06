import { Loading } from "@/components/shared";
import { useI18n } from "@/i18n/context";
import type { Access } from "@/api/client";
import { useCapabilities } from "@/lib/capabilities";
import { serviceAccess } from "./serviceAccess";
import type { IsolatedServiceState } from "./serviceState";

/** Read-only facts about the service. Actions live in NextStep. */
export function ServicePanel({ svc, app }: { svc: IsolatedServiceState; app: { name: string; port: number; access: Access; domain: string | null } }) {
  const appsPort = useCapabilities().data?.apps_port;
  const { t } = useI18n();
  const d = t.definitions;
  const s = d.service;
  const { service, missing } = svc;
  const action = service.data?.action;
  const where = serviceAccess(app, service.data, appsPort);
  return <div className="grid gap-2 text-sm">
    <p className="text-muted-foreground">{s.intro[where.access]}</p>
    {service.isPending ? <Loading /> : service.data ? <>
      <dl className="grid gap-1"><div className="flex gap-2"><dt className="text-muted-foreground">{d.serviceRevision}</dt><dd>{service.data.revision}</dd></div></dl>
      {action && <p>{action.kind === "stop" ? s.actionStop : action.kind === "remove" ? s.actionRemove : s.actionOther}: {t.serviceObservationLabels[action.observation] || (action.phase === "dispatched" ? s.resultPending : s.resultUnknown)}</p>}
      {svc.exited && svc.exitCode !== undefined && <p>{d.deploy.exited(svc.exitCode)}</p>}
      <p className="text-muted-foreground">{s.futureConfig}</p>
    </> : missing ? <p>{s.none}</p> : <p role="alert">{s.queryFailed}</p>}
  </div>;
}
