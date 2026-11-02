import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Loading } from "@/components/shared";
import { useI18n } from "@/i18n/context";
import { createUncertain, type IsolatedServiceState } from "./serviceState";

/** One primary action per state. Uncertain results only ever offer a status query; nothing is re-sent automatically. */
export function NextStep({ svc, experimental, available, readable, revision, configReady, onAddVariable }: {
  svc: IsolatedServiceState;
  experimental: boolean;
  available: boolean;
  readable: boolean;
  revision: number;
  configReady: boolean;
  onAddVariable: () => void;
}) {
  const { t } = useI18n();
  const d = t.definitions;
  const s = d.service;
  const { service, create, refresh, missing, pendingAction, canChange, configFetching, attempted, acknowledged, setAcknowledged, confirmRemove, setConfirmRemove, revisionConflict, state, stop, confirmRemoval, query } = svc;
  const addVariable = <div className="grid justify-items-start gap-3"><p>{d.firstVariable}</p><Button onClick={onAddVariable}>{d.addVariable}</Button></div>;
  if (!experimental) return <div className="grid justify-items-start gap-3"><p>{d.demoNoServices}</p>{configReady && revision === 0 && <Button onClick={onAddVariable}>{d.addVariable}</Button>}</div>;
  if (!available) return <p>{s.unavailable}</p>;
  if (!readable) return null;
  if (service.isPending) return <Loading />;
  const queryButton = (primary: boolean) => <Button variant={primary ? "default" : "outline"} disabled={refresh.isPending} onClick={query}>{s.query}</Button>;
  const uncertainCreate = createUncertain(svc);
  return <div className="grid justify-items-start gap-3">
    {revisionConflict && <p role="alert">{s.revisionConflict}</p>}
    {service.data ? pendingAction ? <>
      <p role="alert">{s.uncertainAction}</p>{queryButton(true)}
    </> : <div className="flex flex-wrap items-center gap-2">
      {canChange && state === "running" && <Button onClick={stop}>{s.stop}</Button>}
      {canChange && (state === "stopped" || state === "created") && <Button onClick={() => setConfirmRemove(true)}>{s.remove}</Button>}
      {queryButton(!(canChange && (state === "running" || state === "stopped" || state === "created")))}
    </div> : uncertainCreate ? <>
      <p role="alert">{s.uncertainCreate}</p>{queryButton(true)}
    </> : missing && !attempted ? revision > 0 ? <>
      <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={acknowledged} onChange={(event) => setAcknowledged(event.target.checked)} /> {s.acknowledge}</label>
      <Button disabled={!acknowledged || create.isPending || revisionConflict && configFetching} onClick={() => create.mutate()}>{s.create}</Button>
    </> : configReady ? addVariable : <p>{s.needsVariable}</p> : queryButton(true)}
    {refresh.isError && <p role="alert">{s.refreshFailed}</p>}
    <Dialog open={confirmRemove} onOpenChange={setConfirmRemove}><DialogContent><DialogHeader><DialogTitle>{s.remove}</DialogTitle><DialogDescription>{s.removeDescription}</DialogDescription></DialogHeader><div className="flex flex-wrap gap-2"><Button variant="destructive" disabled={!canChange} onClick={confirmRemoval}>{s.confirmRemove}</Button><Button variant="outline" onClick={() => setConfirmRemove(false)}>{d.cancel}</Button></div></DialogContent></Dialog>
  </div>;
}
