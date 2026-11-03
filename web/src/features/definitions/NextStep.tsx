import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Loading } from "@/components/shared";
import { useI18n } from "@/i18n/context";
import { DeployProgress } from "./DeployProgress";
import { ImageAvailability } from "./ImageAvailability";
import { createUncertain, type IsolatedServiceState } from "./serviceState";
import { useDeploy, type DeployStep } from "./useDeploy";
import type { ImageCheckState } from "./useImageCheck";

const plan: DeployStep[] = ["image", "download", "service", "start"];

/**
 * One primary action per state. Without a service it is «Deploy», which
 * downloads the image when needed. Uncertain results only ever offer a status
 * query; nothing is re-sent automatically.
 */
export function NextStep({
  app,
  svc,
  image,
  experimental,
  available,
  readable,
  revision,
  configReady,
  onAddVariable,
  onChangeImage,
}: {
  app: { id: string; name: string; image: string; port: number };
  image: ImageCheckState;
  onChangeImage: () => void;
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
  const x = d.deploy;
  const deploy = useDeploy({ appId: app.id, revision }, svc.markAttempted);
  const {
    service,
    refresh,
    missing,
    pendingAction,
    canChange,
    attempted,
    confirmRemove,
    setConfirmRemove,
    noServiceConfirmed,
    state,
    stop,
    confirmRemoval,
    query,
  } = svc;
  if (!experimental)
    return (
      <div className="grid justify-items-start gap-3">
        <p>{d.demoNoServices}</p>
        {configReady && revision === 0 && (
          <Button onClick={onAddVariable}>{d.addVariable}</Button>
        )}
      </div>
    );
  if (!available) return <p>{s.unavailable}</p>;
  if (!readable) return null;
  if (service.isPending) return <Loading />;
  const queryButton = (primary: boolean) => (
    <Button
      variant={primary ? "default" : "outline"}
      disabled={refresh.isPending}
      onClick={query}
    >
      {s.query}
    </Button>
  );
  const start = async () => {
    await deploy.run(plan, {
      name: app.name,
      image: app.image,
      port: app.port,
      project: { kind: "none" },
      variables: [],
      projects: [],
      revision,
    });
  };
  const incompatible = image.check.data?.status === "incompatible";
  return (
    <div className="grid justify-items-start gap-3">
      {service.data ? (
        pendingAction ? (
          <>
            <p role="alert">{s.uncertainAction}</p>
            {queryButton(true)}
          </>
        ) : (
          <div className="flex flex-wrap items-center gap-2">
            {canChange && state === "running" && (
              <Button onClick={stop}>{s.stop}</Button>
            )}
            {canChange && (state === "stopped" || state === "created") && (
              <Button onClick={() => setConfirmRemove(true)}>{s.remove}</Button>
            )}
            {queryButton(
              !(
                canChange &&
                (state === "running" ||
                  state === "stopped" ||
                  state === "created")
              ),
            )}
          </div>
        )
      ) : createUncertain(svc) ? (
        <>
          <p role="alert">{s.uncertainCreate}</p>
          {queryButton(true)}
        </>
      ) : missing && !attempted ? (
        incompatible ? (
          <>
            <ImageAvailability image={image} />
            <Button variant="outline" onClick={onChangeImage}>
              {d.image.changeInSettings}
            </Button>
          </>
        ) : (
          <>
            {noServiceConfirmed && <p role="status">{s.noServiceAfterQuery}</p>}
            <p className="text-sm text-muted-foreground">
              {x.startDescription} {x.isolation}
              {configReady && revision === 0 ? ` ${s.noVariables}` : ""}
            </p>
            {!deploy.steps.length && (
              <Button
                disabled={!configReady || deploy.running}
                onClick={() => void start()}
              >
                {x.start}
              </Button>
            )}
            <div className="w-full max-w-md">
              <DeployProgress deploy={deploy} onRetry={() => void start()} />
            </div>
          </>
        )
      ) : (
        queryButton(true)
      )}
      {refresh.isError && <p role="alert">{s.refreshFailed}</p>}
      <Dialog open={confirmRemove} onOpenChange={setConfirmRemove}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{s.remove}</DialogTitle>
            <DialogDescription>{s.removeDescription}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="destructive"
              disabled={!canChange}
              onClick={confirmRemoval}
            >
              {s.confirmRemove}
            </Button>
            <Button variant="outline" onClick={() => setConfirmRemove(false)}>
              {d.cancel}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
