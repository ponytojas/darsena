import { useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { Check, Copy } from "lucide-react";
import { api, sessionSnapshot, subscribeSession } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Failure, Loading, PageTitle, Status } from "@/components/shared";
import { formatDateTime } from "@/lib/format";
import { useCapabilities } from "@/lib/capabilities";
import type { Health } from "@/lib/status";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";

const SOCKET = "/tmp/darsena-worker/worker.sock";
const commands = {
  demoServe: `go run ./cmd/darsena serve --demo --worker-socket ${SOCKET}`,
  demoWorker: `go run ./cmd/darsena worker --demo --state /tmp/darsena-worker/state.db --socket ${SOCKET}`,
  demoVault: `age-keygen -o /tmp/darsena-worker/identity.age
go run ./cmd/darsena worker --demo --state /tmp/darsena-worker/state.db --socket ${SOCKET} --config-db /tmp/darsena-worker/config.db --age-identity /tmp/darsena-worker/identity.age`,
  experimentalWorker:
    "go run ./cmd/darsena worker --experimental --installation <stable-id> --state <state.db> --socket <worker.sock> --config-db <config.db> --age-identity <identity.age> --intent-db <intent.db> --docker-socket <dedicated-rootless.sock>",
  experimentalServe:
    "go run ./cmd/darsena serve --experimental --installation <same-id> --worker-socket <worker.sock> --owner-credential <owner.credential> --origin http://127.0.0.1:7300",
  ownerInit: "go run ./cmd/darsena owner-init --owner-credential /private/path/owner.credential",
};

type Step = { caption: string; text: string };
type Row = { id: string; label: string; health: Health; text: string; hint?: string; steps?: Step[]; docs?: string };

function CommandBlock({ step, onCopied }: { step: Step; onCopied: (message: string) => void }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const timer = useRef<number>(undefined);
  return (
    <div className="mt-2 flex items-start gap-2 rounded-md border border-border bg-muted p-2">
      <pre className="mono min-w-0 flex-1 overflow-x-auto whitespace-pre text-xs leading-relaxed" tabIndex={0} aria-label={step.caption}>
        {step.text}
      </pre>
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-label={copied ? `${t.diag.copied}: ${step.caption}` : t.diag.copyAria(step.caption)}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(step.text);
            setCopied(true);
            onCopied(t.diag.copied);
            window.clearTimeout(timer.current);
            timer.current = window.setTimeout(() => setCopied(false), 2000);
          } catch {
            onCopied(t.diag.copyFailed);
          }
        }}
      >
        {copied ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}
        {copied ? t.diag.copied : t.diag.copy}
      </Button>
    </div>
  );
}

export default function Diagnostics() {
  const { t } = useI18n();
  const d = t.diag;
  const capabilities = useCapabilities();
  const cache = useQueryClient();
  const signedIn = useSyncExternalStore(subscribeSession, sessionSnapshot);
  const [announcement, setAnnouncement] = useState("");
  const data = capabilities.data;
  const experimental = data?.mode === "experimental";
  const demo = data?.mode === "demo";
  const unreachable = capabilities.isError;
  const serverUp = !!data && !unreachable;

  const workerStatus = data?.worker.configured ? data.worker.status : "disabled";
  const workerStep: Step[] = experimental
    ? [{ caption: d.cmdWorkerExperimental, text: commands.experimentalWorker }]
    : [{ caption: d.cmdWorker, text: commands.demoWorker }];
  const rows: Row[] = [
    {
      id: "server",
      label: d.server,
      health: capabilities.isPending ? "progress" : serverUp ? "ok" : "attention",
      text: capabilities.isPending ? d.serverChecking : serverUp ? d.serverOk : d.serverDown,
      hint: serverUp || capabilities.isPending ? undefined : d.serverDownNext,
      steps: serverUp || capabilities.isPending ? undefined : [{ caption: d.cmdServe, text: commands.demoServe }],
    },
    {
      id: "mode",
      label: d.mode,
      health: data ? "ok" : "idle",
      text: experimental ? d.modeExperimentalText : demo ? d.modeDemoText : d.modeUnknown,
    },
  ];
  if (data) {
    const notConfigured = workerStatus === "disabled";
    rows.push({
      id: "worker",
      label: d.worker,
      health: workerStatus === "ready" ? "ok" : notConfigured ? "idle" : "attention",
      text: workerStatus === "ready" ? d.workerReadyText : notConfigured ? d.workerNotConfiguredText : d.workerUnavailableText,
      hint: notConfigured ? undefined : workerStatus === "ready" ? undefined : d.workerUnavailableNext,
      steps:
        workerStatus === "ready"
          ? undefined
          : notConfigured
            ? [...workerStep, { caption: experimental ? d.cmdServeExperimental : d.cmdServe, text: experimental ? commands.experimentalServe : commands.demoServe }]
            : workerStep,
      docs: workerStatus === "ready" ? undefined : "docs/operations.md",
    });
    const stateText = { ready: d.stateReadyText, disabled: d.stateDisabledText, unavailable: d.stateUnavailableText, unknown: d.stateUnknownText }[data.state];
    rows.push({
      id: "state",
      label: d.jobsState,
      health: data.state === "ready" ? "ok" : data.state === "disabled" ? "idle" : "attention",
      text: stateText,
      hint: data.state === "ready" ? undefined : d.stateNext,
      docs: data.state === "ready" ? undefined : "docs/operations.md",
    });
    const vaultText = { ready: d.vaultReadyText, disabled: d.vaultDisabledText, unavailable: d.vaultUnavailableText, unknown: d.vaultUnknownText }[data.vault];
    rows.push({
      id: "vault",
      label: d.vault,
      health: data.vault === "ready" ? "ok" : data.vault === "disabled" ? "idle" : "attention",
      text: vaultText,
      hint: data.vault === "ready" ? undefined : d.vaultNext,
      steps: data.vault === "disabled" && !experimental ? [{ caption: d.cmdWorkerVault, text: commands.demoVault }] : undefined,
      docs: data.vault === "ready" ? undefined : "docs/operations.md",
    });
    if (experimental) {
      const real = data.real_execution;
      const ok = real.enabled && real.provider === "ready";
      rows.push({
        id: "real",
        label: d.realExecution,
        health: ok ? "ok" : real.enabled ? "attention" : "idle",
        text: real.enabled ? d.realReadyText(d.providerLabels[real.provider]) : d.realOffText,
        hint: ok ? undefined : d.realNext,
        steps: ok ? undefined : [{ caption: d.cmdWorkerExperimental, text: commands.experimentalWorker }],
        docs: ok ? undefined : "docs/runtime.md",
      });
      rows.push({
        id: "owner",
        label: d.owner,
        health: signedIn ? "ok" : "attention",
        text: signedIn ? d.ownerSignedIn : d.ownerSignedOut,
        hint: signedIn ? undefined : d.ownerNext,
        steps: signedIn ? undefined : [{ caption: d.cmdOwnerInit, text: commands.ownerInit }],
      });
    }
  }

  return (
    <div className="page">
      <PageTitle
        eyebrow={t.overview}
        title={t.diagnosticsTitle}
        description={t.diagnosticsDescription}
        action={
          <Button
            variant="outline"
            disabled={capabilities.isFetching}
            onClick={() =>
              void capabilities.refetch().then((result) => {
                if (!result.isError) setAnnouncement(d.refreshed);
              })
            }
          >
            {t.refreshStatus}
          </Button>
        }
      />
      <p role="status" aria-live="polite" className="sr-only">
        {announcement}
      </p>
      <section aria-labelledby="checklist-title" className="mt-6">
        <h2 id="checklist-title" className="text-sm font-semibold">
          {d.checklist}
        </h2>
        <ul className="mt-3 border-t border-border">
          {rows.map((row) => (
            <li key={row.id} data-testid={`check-${row.id}`} className="grid grid-cols-[minmax(0,1fr)] gap-1 border-b border-border py-3.5">
              <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
                <span className="min-w-0">
                  <strong className="text-sm font-semibold">{row.label}</strong>
                  <span className="block text-sm text-muted-foreground">{row.text}</span>
                </span>
                <Status health={row.health} label={d.status[row.health === "progress" ? "idle" : row.health]} />
              </div>
              {(row.hint || row.steps || row.docs) && (
                <div className="mt-1 text-xs">
                  <p className="font-medium">{d.nextStep}</p>
                  {row.hint && <p className="text-muted-foreground">{row.hint}</p>}
                  {row.steps?.map((step) => (
                    <CommandBlock key={step.caption} step={step} onCopied={setAnnouncement} />
                  ))}
                  {row.docs && (
                    <p className="mt-2 text-muted-foreground">
                      {d.docs}{" "}<code className="mono">{row.docs}</code>
                    </p>
                  )}
                </div>
              )}
            </li>
          ))}
        </ul>
      </section>
      {demo && data?.worker.configured && <WorkerJobs cache={cache} />}
    </div>
  );
}

function WorkerJobs({ cache }: { cache: ReturnType<typeof useQueryClient> }): ReactNode {
  const { t, locale } = useI18n();
  const d = t.diag;
  const capabilities = useCapabilities();
  const [resource, setResource] = useState("");
  const pendingRequest = useRef<{ resource: string; key: string } | null>(null);
  const worker = useQuery({
    queryKey: ["worker-jobs"],
    queryFn: api.workerJobs,
    enabled: capabilities.workerReady,
    refetchInterval: (query) =>
      query.state.data?.some((job) => job.status === "queued" || job.status === "running") ? 1000 : false,
  });
  const submit = useMutation({
    mutationFn: (id: string) => {
      if (!pendingRequest.current || pendingRequest.current.resource !== id) pendingRequest.current = { resource: id, key: crypto.randomUUID() };
      return api.workerCreate(id, pendingRequest.current.key);
    },
    onSuccess: () => {
      pendingRequest.current = null;
      setResource("");
      void cache.invalidateQueries({ queryKey: ["worker-jobs"] });
    },
  });
  return (
    <section className="mt-10" aria-labelledby="persistent-jobs-title">
      <h2 id="persistent-jobs-title" className="text-sm font-semibold">
        {d.jobsTitle}
      </h2>
      <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{d.jobsIntro}</p>
      {!capabilities.workerReady && (
        <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
          {t.workerTemporarilyUnavailable}
        </p>
      )}
      <form
        className="my-5 flex max-w-[620px] flex-col items-start gap-4 sm:flex-row"
        onSubmit={(event) => {
          event.preventDefault();
          if (capabilities.workerReady && resource.trim()) submit.mutate(resource.trim());
        }}
      >
        <Field className="sm:max-w-[350px]">
          <FieldLabel htmlFor="worker-resource">{t.resourceId}</FieldLabel>
          <Input id="worker-resource" value={resource} maxLength={128} onChange={(event) => setResource(event.target.value)} placeholder={t.resourcePlaceholder} />
          <FieldDescription className="text-xs">{t.workerNoChanges}</FieldDescription>
        </Field>
        <Button type="submit" className="sm:mt-[23px]" disabled={!capabilities.workerReady || !resource.trim() || submit.isPending}>
          {t.startSimulation}
        </Button>
      </form>
      {submit.isError && (
        <p role="alert" className="mb-4 text-destructive">
          {errorMessage(submit.error, t)}
        </p>
      )}
      {!capabilities.workerReady && !worker.data ? null : worker.isPending ? (
        <Loading />
      ) : worker.isError ? (
        <Failure error={worker.error} retry={() => void worker.refetch()} />
      ) : worker.data.length ? (
        <ul className="border-t border-border">
          {worker.data.map((job) => (
            <li className="flex items-center justify-between gap-4 border-b border-border py-3.5" key={job.id} data-testid="worker-job">
              <span className="min-w-0">
                <strong className="break-words text-sm font-semibold">{job.resource_id}</strong>
                <small className="mt-1 block break-words text-xs text-muted-foreground">
                  {formatDateTime(job.created_at, locale)} · <span className="mono">{job.id.slice(0, 8)}</span>
                </small>
                {job.error && <small className="mt-1 block text-xs text-muted-foreground">{d.jobFailed}</small>}
              </span>
              <Status status={job.status} />
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs leading-relaxed text-muted-foreground">{t.noWorkerJobs}</p>
      )}
    </section>
  );
}
