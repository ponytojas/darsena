import { useId, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import type { api } from "@/api/client";
import { useCapabilities } from "@/lib/capabilities";
import { ImageAvailability } from "./ImageAvailability";
import { imageProblem, useImageCheck } from "./useImageCheck";
import type { DefinitionSettings as Settings } from "./useDefinitionSettings";

type WorkerApplication = Awaited<ReturnType<typeof api.workerApps>>[number];

const selectClass =
  "h-8 w-full rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30";

/** Settings tab: project, image and port, and the danger zone. Nothing here starts or changes a service. */
export function DefinitionSettings({ app, settings }: { app: WorkerApplication; settings: Settings }) {
  const { t } = useI18n();
  const s = t.definitions.settings;
  const d = t.definitions;
  const navigate = useNavigate();
  const ids = useId();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const { save, reload, assign, remove, projects } = settings;
  const locked = app.service_used;
  const conflict = save.isError && (save.error as { code?: string }).code === "update_conflict";
  const capabilities = useCapabilities();
  const experimental = capabilities.data?.mode === "experimental";
  const imageCheck = useImageCheck(settings.image, capabilities.workerReady && !locked);
  const imageInvalid = locked ? "" : imageProblem(settings.image, imageCheck, t);
  const imageError = settings.shown ? imageInvalid : "";
  const portError = settings.shown && settings.problems.port ? d.portInvalid : "";

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (imageInvalid || settings.problems.port) {
      settings.showProblems();
      document.getElementById(`${ids}-${imageInvalid ? "image" : "port"}`)?.focus();
      return;
    }
    save.mutate(locked ? undefined : imageCheck.resolved, { onSuccess: () => toast.success(s.saved) });
  };
  const changeProject = (value: string) =>
    assign.mutate(value || null, { onSuccess: () => toast.success(value ? s.projectMoved : s.projectCleared) });
  const closeConfirm = (open: boolean) => { if (!remove.isPending) { setConfirmOpen(open); if (!open) { setTyped(""); remove.reset(); } } };
  const confirmRemove = () =>
    remove.mutate(undefined, {
      onSuccess: () => { setConfirmOpen(false); toast.success(s.deleted(app.name)); void navigate("/"); },
    });

  return (
    <div className="mt-8 grid max-w-xl gap-10">
      <section aria-labelledby={`${ids}-project`} className="grid gap-3">
        <h2 id={`${ids}-project`} className="text-sm font-semibold">{s.projectTitle}</h2>
        <Field>
          <FieldLabel htmlFor={`${ids}-project-select`}>{s.project}</FieldLabel>
          <select id={`${ids}-project-select`} className={selectClass} value={app.project_id ?? ""} disabled={assign.isPending || projects.isPending} onChange={(event) => changeProject(event.target.value)}>
            <option value="">{t.groupUngrouped}</option>
            {(projects.data ?? []).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
          </select>
          <FieldDescription>{s.projectHint}</FieldDescription>
          {(assign.isError || projects.isError) && <p role="alert" className="worker-error">{errorMessage((assign.error ?? projects.error) as Error, t)}</p>}
        </Field>
      </section>

      <section aria-labelledby={`${ids}-image-title`}>
        <h2 id={`${ids}-image-title`} className="text-sm font-semibold">{s.imageTitle}</h2>
        <p className="pt-1 pb-4 text-sm text-muted-foreground">{s.futureOnly} {s.portMetadata}</p>
        <form onSubmit={submit} noValidate>
          <FieldGroup>
            <Field data-invalid={!!imageError}>
              <FieldLabel htmlFor={`${ids}-image`}>{experimental ? d.imageLabel : d.imageByDigest}</FieldLabel>
              <Input id={`${ids}-image`} value={settings.image} disabled={locked} autoComplete="off" autoCapitalize="none" spellCheck={false} aria-invalid={!!imageError} aria-describedby={`${ids}-image-help ${ids}-image-status`} onChange={(event) => settings.setImage(event.target.value)} />
              <FieldDescription id={`${ids}-image-help`}>{locked ? s.imageLocked : experimental ? d.image.hint : d.imageHint}</FieldDescription>
              {!locked && <div id={`${ids}-image-status`}><ImageAvailability image={imageCheck} typed={settings.image} showError={!imageError} reserve={experimental} /></div>}
              {imageError && <FieldError>{imageError}</FieldError>}
            </Field>
            <Field data-invalid={!!portError}>
              <FieldLabel htmlFor={`${ids}-port`}>{d.port}</FieldLabel>
              <Input id={`${ids}-port`} type="number" inputMode="numeric" min={1} max={65535} value={settings.port} aria-invalid={!!portError} onChange={(event) => settings.setPort(event.target.value)} />
              {portError && <FieldError>{portError}</FieldError>}
            </Field>
            {save.isError && <div role="alert" className="worker-error grid justify-items-start gap-2">
              <p>{errorMessage(save.error, t)}</p>
              {conflict && <Button type="button" variant="outline" size="sm" disabled={reload.isPending} onClick={() => reload.mutate()}>{s.reload}</Button>}
            </div>}
            {reload.isError && <p role="alert" className="worker-error">{errorMessage(reload.error, t)}</p>}
            <div><Button type="submit" disabled={save.isPending || !settings.dirty}>{s.save}</Button></div>
          </FieldGroup>
        </form>
      </section>

      <section aria-labelledby={`${ids}-danger`} className="grid gap-3 rounded-lg border border-destructive/40 p-5">
        <h2 id={`${ids}-danger`} className="text-sm font-semibold text-destructive">{s.dangerTitle}</h2>
        <p className="text-sm text-muted-foreground">{s.deleteSummary}</p>
        {locked && <p id={`${ids}-delete-reason`} className="text-sm">{s.deleteBlocked}</p>}
        <div><Button variant="destructive" disabled={locked} aria-describedby={locked ? `${ids}-delete-reason` : undefined} onClick={() => setConfirmOpen(true)}>{s.deleteAction}</Button></div>
      </section>

      <Dialog open={confirmOpen} onOpenChange={closeConfirm}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{s.deleteTitle}</DialogTitle>
            <DialogDescription>{s.deleteDescription(app.name)}</DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor={`${ids}-confirm`}>{s.typeName(app.name)}</FieldLabel>
            <Input id={`${ids}-confirm`} value={typed} autoComplete="off" spellCheck={false} onChange={(event) => setTyped(event.target.value)} />
          </Field>
          {remove.isError && <p role="alert" className="worker-error">{errorMessage(remove.error, t)}</p>}
          <div className="flex flex-wrap gap-2">
            <Button variant="destructive" disabled={typed !== app.name || remove.isPending} onClick={confirmRemove}>{s.deleteConfirm}</Button>
            <Button variant="outline" onClick={() => closeConfirm(false)}>{d.cancel}</Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
