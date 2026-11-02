import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useBlocker, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { api } from "@/api/client";
import { useCapabilities } from "@/lib/capabilities";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";

const initialPort = "8080";
const namePattern = /^[a-z][a-z0-9-]{1,39}$/;
const imagePattern = /^\S+@sha256:[0-9a-fA-F]{64}$/;
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
  const create = useMutation({
    mutationFn: (body: { name: string; image: string; port: number }) => api.workerAppCreate(body),
    onSuccess: (app) => {
      saved.current = true;
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
      void navigate(`/apps/definition/${encodeURIComponent(app.id)}`);
    },
  });
  const configured = !!capabilities.data?.worker.configured;
  const ready = capabilities.workerReady;

  const portNumber = Number(port);
  const problems: Record<Field, string> = {
    name: namePattern.test(name) ? "" : d.nameInvalid,
    image: imagePattern.test(image.trim()) ? "" : d.imageInvalid,
    port: Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535 ? "" : d.portInvalid,
  };
  const error = (field: Field) => (shown[field] ? problems[field] : "");

  const dirty = (name !== "" || image !== "" || port !== initialPort) && !create.isSuccess;
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
    if (ready) create.mutate({ name, image: image.trim(), port: portNumber });
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
              <FieldLabel htmlFor={`${ids}-image`}>{d.imageByDigest}</FieldLabel>
              <Input
                id={`${ids}-image`}
                value={image}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={!!error("image")}
                aria-describedby={`${ids}-image-help`}
                placeholder="registry/image@sha256:…"
                onChange={edit(setImage)}
              />
              <FieldDescription id={`${ids}-image-help`}>{d.imageHint}</FieldDescription>
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
