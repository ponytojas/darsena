import { Loading } from "@/components/shared";
import { useI18n } from "@/i18n/context";
import type { IsolatedServiceState } from "./serviceState";

/** Read-only facts about the isolated service. Actions live in NextStep. */
export function ServicePanel({ svc }: { svc: IsolatedServiceState }) {
  const { t } = useI18n();
  const d = t.definitions;
  const s = d.service;
  const { service, missing } = svc;
  const action = service.data?.action;
  return <div className="grid gap-2 text-sm">
    <p className="text-muted-foreground">{s.intro}</p>
    {service.isPending ? <Loading /> : service.data ? <>
      <dl className="grid gap-1"><div className="flex gap-2"><dt className="text-muted-foreground">{d.serviceRevision}</dt><dd>{service.data.revision}</dd></div></dl>
      {action && <p>{action.kind === "stop" ? s.actionStop : action.kind === "remove" ? s.actionRemove : s.actionOther}: {t.serviceObservationLabels[action.observation] || (action.phase === "dispatched" ? s.resultPending : s.resultUnknown)}</p>}
      <p className="text-muted-foreground">{s.futureConfig}</p>
    </> : missing ? <p>{s.none}</p> : <p role="alert">{s.queryFailed}</p>}
  </div>;
}
