import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useBlocker, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { api } from "@/api/client";
import { useCapabilities } from "@/lib/capabilities";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import { ImageAvailability } from "./ImageAvailability";
import { imageProblem, useImageCheck } from "./useImageCheck";
import { isValidName, isValidPort } from "./validation";

const initialPort = "8080";
type Field = "name" | "image" | "port";

/** Definition form. Saving a definition never starts a service. */
export function NewDefinition() {
  const { t } = useI18n();
  const d = t.definitions;
  const capabilities = useCapabilities();
  const cache = useQueryClient();
  const navigate = useNavigate();
  const ids = useId();
  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [port, setPort] = useState(initialPort);
  const [shown, setShown] = useState<Partial<Record<Field, boolean>>>({});
  const [focusField, setFocusField] = useState<{ field: Field }>();
  useEffect(() => {
    if (focusField) document.getElementById(`${ids}-${focusField.field}`)?.focus();
  }, [focusField, ids]);
  const saved = useRef(false);
  const [params] = useSearchParams();
  const configured = !!capabilities.data?.worker.configured;
  const ready = capabilities.workerReady;
  const experimental = capabilities.data?.mode === "experimental";
  const imageCheck = useImageCheck(image, configured && ready);
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.workerProjects, enabled: configured && ready });
  // Unknown ids in `?project=` are ignored; a user choice overrides the preselection.
  const requested = params.get("project") ?? "";
  const initialProject = projects.data?.some((p) => p.id === requested) ? requested : "";
  const [chosenProject, setChosenProject] = useState<string>();
  const projectId = chosenProject ?? initialProject;
  const create = useMutation({
    // Create first, then assign: a failed assignment leaves the definition created without a project.
    mutationFn: async (body: { name: string; image: string; port: number }) => {
      const app = await api.workerAppCreate(body);
      if (!projectId) return { app };
      try {
        await api.workerAssign({ project_id: projectId, application_ids: [app.id] });
        return { app };
      } catch (error) {
        return { app, assignError: error };
      }
    },
    onSuccess: ({ app, assignError }) => {
      saved.current = true;
      if (assignError) toast.warning(d.assignFailed(errorMessage(assignError, t)));
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
      void cache.invalidateQueries({ queryKey: ["projects"] });
      void navigate(`/apps/definition/${encodeURIComponent(app.id)}`);
    },
  });

  const portNumber = Number(port);
  const problems: Record<Field, string> = {
    name: isValidName(name) ? "" : d.nameInvalid,
    image: imageProblem(image, imageCheck, t),
    port: isValidPort(port) ? "" : d.portInvalid,
  };
  const error = (field: Field) => (shown[field] ? problems[field] : "");

  const dirty = (name !== "" || image !== "" || port !== initialPort || projectId !== initialProject) && !create.isSuccess;
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty &&
      !saved.current &&
      (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search),
  );
  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const first = (["name", "image", "port"] as const).find((field) => problems[field]);
    if (first) {
      setShown({ name: true, image: true, port: true });
      setFocusField({ field: first });
      return;
    }
    if (ready) create.mutate({ name, image: imageCheck.resolved ?? image.trim(), port: portNumber });
  };
  const edit = (set: (value: string) => void) => (event: { target: { value: string } }) => {
    set(event.target.value);
    create.reset();
  };

  return (
    <section aria-labelledby={`${ids}-title`} className="max-w-lg">
      <h2 id={`${ids}-title`} className="text-base font-semibold">{d.newDefinition}</h2>
      <p className="pt-1 pb-5 text-sm text-muted-foreground">{d.newDescription}</p>
      {!configured ? (
        <p>{d.startWorker}</p>
      ) : (
        <form onSubmit={submit} noValidate>
          <FieldGroup>
            <Field data-invalid={!!error("name")}>
              <FieldLabel htmlFor={`${ids}-name`}>{d.name}</FieldLabel>
              <Input
                id={`${ids}-name`}
                value={name}
                autoComplete="off"
                aria-invalid={!!error("name")}
                aria-describedby={`${ids}-name-help`}
                onChange={edit(setName)}
              />
              <FieldDescription id={`${ids}-name-help`}>{d.nameHint}</FieldDescription>
              {error("name") && <FieldError>{error("name")}</FieldError>}
            </Field>
            <Field data-invalid={!!error("image")}>
              <FieldLabel htmlFor={`${ids}-image`}>{experimental ? d.imageLabel : d.imageByDigest}</FieldLabel>
              <Input
                id={`${ids}-image`}
                value={image}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                aria-invalid={!!error("image")}
                aria-describedby={`${ids}-image-help ${ids}-image-status`}
                placeholder={experimental ? d.image.placeholder : "registry/image@sha256:…"}
                onChange={edit(setImage)}
              />
              <FieldDescription id={`${ids}-image-help`}>{experimental ? d.image.hint : d.imageHint}</FieldDescription>
              <div id={`${ids}-image-status`}><ImageAvailability image={imageCheck} typed={image} showError={!error("image")} reserve={experimental} /></div>
              {error("image") && <FieldError>{error("image")}</FieldError>}
            </Field>
            <Field data-invalid={!!error("port")}>
              <FieldLabel htmlFor={`${ids}-port`}>{d.port}</FieldLabel>
              <Input
                id={`${ids}-port`}
                type="number"
                inputMode="numeric"
                min={1}
                max={65535}
                value={port}
                aria-invalid={!!error("port")}
                onChange={edit(setPort)}
              />
              {error("port") && <FieldError>{error("port")}</FieldError>}
            </Field>
            {!!projects.data?.length && (
              <Field>
                <FieldLabel htmlFor={`${ids}-project`}>{d.project}</FieldLabel>
                <select
                  id={`${ids}-project`}
                  className="h-9 w-full rounded-lg border bg-background px-2 text-sm focus-visible:outline-2 focus-visible:outline-ring"
                  value={projectId}
                  onChange={(event) => {
                    setChosenProject(event.target.value);
                    create.reset();
                  }}
                >
                  <option value="">{d.noProject}</option>
                  {projects.data.map((p) => (
                    <option key={p.id} value={p.id}>{p.name}</option>
                  ))}
                </select>
              </Field>
            )}
            {!ready && <p>{d.workerTemporarilyDown}</p>}
            {create.isError && <p role="alert" className="worker-error">{errorMessage(create.error, t)}</p>}
            <div>
              <Button type="submit" disabled={create.isPending || !ready}>{d.saveDefinition}</Button>
            </div>
          </FieldGroup>
        </form>
      )}
      <Dialog open={blocker.state === "blocked"} onOpenChange={(open) => { if (!open && blocker.state === "blocked") blocker.reset(); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{d.discardTitle}</DialogTitle>
            <DialogDescription>{d.discardDraft}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-wrap gap-2">
            <Button variant="destructive" onClick={() => blocker.state === "blocked" && blocker.proceed()}>{d.discardAction}</Button>
            <Button variant="outline" onClick={() => blocker.state === "blocked" && blocker.reset()}>{d.keepEditing}</Button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
