import { useEffect } from "react";
import { Link, Navigate, useBlocker, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Blank, Failure, Loading, Status } from "@/components/shared";
import { DetailTabs } from "@/components/detail";
import { api } from "@/api/client";
import { useCapabilities } from "@/lib/capabilities";
import { definitionHealth } from "@/lib/status";
import { useI18n } from "@/i18n/context";
import { NextStep } from "@/features/definitions/NextStep";
import { ServicePanel } from "@/features/definitions/ServicePanel";
import { createUncertain } from "@/features/definitions/serviceState";
import { VariableList } from "@/features/definitions/VariableList";
import { VariableEditor } from "@/features/definitions/VariableEditor";
import { DefinitionSettings } from "@/features/definitions/DefinitionSettings";
import { HistoryPanel } from "@/features/definitions/HistoryPanel";
import { useDefinitionSettings } from "@/features/definitions/useDefinitionSettings";
import { useDefinitionConfig } from "@/features/definitions/useDefinitionConfig";
import { useIsolatedService } from "@/features/definitions/useIsolatedService";
import { useImageCheck } from "@/features/definitions/useImageCheck";

type WorkerApplication = Awaited<ReturnType<typeof api.workerApps>>[number];

export default function DefinitionDetail() {
  const { t } = useI18n();
  const d = t.definitions;
  const { id = "", tab } = useParams();
  const capabilities = useCapabilities();
  const apps = useQuery({ queryKey: ["worker-apps"], queryFn: api.workerApps, enabled: capabilities.workerReady });
  const base = `/apps/definition/${encodeURIComponent(id)}`;
  if (tab !== undefined && tab !== "variables" && tab !== "settings" && tab !== "history") return <Navigate to={base} replace />;
  const back = <Button render={<Link to="/" />}>{t.backHome}</Button>;
  const app = apps.data?.find((item) => item.id === id);
  let body;
  if (capabilities.isPending) body = <Loading />;
  else if (capabilities.isError && !capabilities.data) body = <Failure error={capabilities.error} retry={() => void capabilities.refetch()} />;
  else if (!capabilities.data?.worker.configured) body = <Blank title={t.definitionsUnavailableTitle} description={d.startWorker} action={back} />;
  else if (!app && apps.isPending) body = capabilities.workerReady ? <Loading /> : <Blank title={t.definitionsUnavailableTitle} description={d.workerTemporarilyDown} action={<Button variant="outline" onClick={() => void capabilities.refetch()}>{d.refreshStatus}</Button>} />;
  else if (!app && apps.isError) body = <Failure error={apps.error} retry={() => void apps.refetch()} />;
  else if (!app) body = <Blank title={d.notFoundTitle} description={d.notFound} action={back} />;
  else body = <DefinitionView key={app.id} app={app} tab={tab === "variables" || tab === "settings" || tab === "history" ? tab : "summary"} base={base} />;
  return <div className="page">{body}</div>;
}

function DefinitionView({ app, tab, base }: { app: WorkerApplication; tab: "summary" | "variables" | "settings" | "history"; base: string }) {
  const { t } = useI18n();
  const d = t.definitions;
  const navigate = useNavigate();
  const capabilities = useCapabilities();
  const current = capabilities.current;
  const experimental = capabilities.data?.mode === "experimental";
  const readable = experimental && capabilities.workerReady;
  const available = capabilities.vaultReady && !!current?.real_execution.enabled && current.real_execution.provider === "ready";
  const cfg = useDefinitionConfig(app.id, capabilities.vaultReady);
  const { config, editor, dirty } = cfg;
  const revision = config.data?.revision ?? 0;
  const svc = useIsolatedService(app.id, revision, { available, readable });
  // Once a service was requested the image is fixed, so its availability no longer matters here.
  const imageCheck = useImageCheck(app.image, readable && !app.service_used);
  const { service } = svc;
  const settings = useDefinitionSettings(app, capabilities.workerReady);
  const guarded = dirty || settings.dirty;

  const inside = (pathname: string) => pathname === base || pathname.startsWith(`${base}/`);
  const blocker = useBlocker(({ nextLocation }) => guarded && !settings.leaving.current && !inside(nextLocation.pathname));
  useEffect(() => {
    if (!guarded) return;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [guarded]);
  const discardOpen = blocker.state === "blocked" || cfg.discardPending;
  const keepEditing = () => { if (blocker.state === "blocked") blocker.reset(); cfg.keepEditing(); };
  const discard = () => { if (blocker.state === "blocked") blocker.proceed(); else cfg.confirmDiscard(); };

  const uncertain = readable && (svc.pendingAction || createUncertain(svc));
  const health = definitionHealth(readable ? service.data : undefined, !!uncertain);
  const statusLabel = uncertain ? t.healthLabels.attention : readable && service.data ? svc.stateLabel(service.data.phase, service.data.observation) : experimental ? d.noService : t.healthLabels.idle;
  const addVariable = () => { cfg.openEditor(); void navigate(`${base}/variables`); };
  const statusText = [
    !current ? d.statusUnconfirmed : capabilities.workerReady ? "" : current.worker.configured ? d.workerTemporarilyDown : d.workerNotConfigured,
    current?.vault === "disabled" ? d.vaultDisabled : current?.vault === "unavailable" ? d.vaultUnavailable : !current ? d.vaultUnconfirmed : "",
    current?.vault === "unavailable" && current.state === "ready" ? d.vaultBlocksActions : "",
  ].filter(Boolean).join(" ");
  const revisionLine = config.data && `${d.revision} ${config.data.revision} · ${experimental ? d.futureChangesExperimental : d.futureChangesDemo}`;

  return <>
    <header className="grid gap-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h1 tabIndex={-1} className="text-2xl font-semibold tracking-tight">{app.name}</h1>
        <Badge variant="secondary">{t.kindDefinition}</Badge>
        <Status health={health} label={statusLabel} />
      </div>
      <p className="grid gap-0.5 text-xs text-muted-foreground"><span className="font-mono break-all">{app.image}</span><span>{d.portUnpublished(app.port)}</span></p>
    </header>
    <DetailTabs label={d.tabsLabel} tabs={[{ to: base, label: d.tabSummary }, { to: `${base}/variables`, label: d.variables }, { to: `${base}/settings`, label: d.tabSettings }, { to: `${base}/history`, label: d.tabHistory }]} />
    {statusText && <Alert className="mt-6"><AlertDescription>{statusText}{(!current || (current.worker.configured && !capabilities.workerReady)) && <> <Button variant="link" onClick={() => void capabilities.refetch()}>{d.refreshStatus}</Button></>}</AlertDescription></Alert>}
    {tab === "summary" ? <div className="mt-8 grid gap-8">
      <section aria-labelledby="next-step-title" className="grid gap-3 rounded-lg border border-border p-5">
        <h2 id="next-step-title" className="text-sm font-semibold">{d.nextStep}</h2>
        <NextStep svc={svc} experimental={experimental} available={available} readable={readable} revision={revision} configReady={!!config.data} onAddVariable={addVariable} image={imageCheck} onChangeImage={() => void navigate(`${base}/settings`)} />
      </section>
      {readable && <section aria-labelledby="service-title" className="grid gap-3">
        <h2 id="service-title" className="text-sm font-semibold">{d.service.title}</h2>
        <ServicePanel svc={svc} />
      </section>}
      {revisionLine && <section aria-labelledby="config-title" className="grid gap-1 text-sm">
        <h2 id="config-title" className="font-semibold">{d.configurationLine}</h2>
        <p className="text-muted-foreground">{revisionLine}</p>
      </section>}
    </div> : tab === "settings" ? <DefinitionSettings app={app} settings={settings} /> : tab === "history" ? <HistoryPanel id={app.id} revision={revision} vaultReady={capabilities.vaultReady} experimental={experimental} /> : <section aria-labelledby="variables-title" className="mt-8">
      <div className="flex items-start justify-between gap-4">
        <div><h2 id="variables-title" className="text-sm font-semibold">{d.variables}</h2>{revisionLine && <p className="worker-intro pt-1">{revisionLine}</p>}</div>
        {config.data && !editor && <Button disabled={!capabilities.vaultReady} size="sm" onClick={() => cfg.openEditor()}>{d.addVariable}</Button>}
      </div>
      {!config.data && !capabilities.vaultReady ? <p className="worker-intro py-5">{current?.vault === "disabled" ? d.vaultNotConfigured : d.vaultNotAvailable}</p> : config.isPending ? <Loading /> : config.isError && !config.data ? <Failure error={config.error} retry={() => void config.refetch()} /> : config.data && <>
        {config.data.variables.length === 0 && !editor && <p className="worker-intro py-5">{d.noVariables}</p>}
        <VariableList variables={config.data.variables} vaultReady={capabilities.vaultReady} confirmDelete={cfg.confirmDelete} deleteConflict={cfg.deleteConflict} remove={cfg.remove} onEdit={cfg.openEditor} onAskDelete={cfg.askDelete} onCancelDelete={cfg.cancelDelete} onConfirmDelete={cfg.confirmDeletion} onRefresh={() => void cfg.refreshConflict(true)} />
        {editor && <VariableEditor editor={editor} setEditor={cfg.setEditor} editorSecret={cfg.editorSecret} matchedVariable={cfg.matchedVariable} savingEditor={cfg.savingEditor} vaultReady={capabilities.vaultReady} conflict={cfg.conflict} save={cfg.save} onSubmit={cfg.submitVariable} onRefresh={() => void cfg.refreshConflict()} onCancel={cfg.cancelEditor} />}
      </>}
    </section>}
    <Dialog open={discardOpen} onOpenChange={(open) => { if (!open) keepEditing(); }}><DialogContent><DialogHeader><DialogTitle>{d.discardTitle}</DialogTitle><DialogDescription>{d.discardDraft}</DialogDescription></DialogHeader><div className="flex flex-wrap gap-2"><Button variant="destructive" onClick={discard}>{d.discardAction}</Button><Button variant="outline" onClick={keepEditing}>{d.keepEditing}</Button></div></DialogContent></Dialog>
  </>;
}
