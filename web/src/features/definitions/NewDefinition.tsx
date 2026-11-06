import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useBlocker, useNavigate, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { api, ApiError, type Access } from "@/api/client";
import { useCapabilities } from "@/lib/capabilities";
import { useI18n } from "@/i18n/context";
import { AccessFields } from "./AccessFields";
import { DeployProgress } from "./DeployProgress";
import { ImageAvailability } from "./ImageAvailability";
import { imageProblem, useImageCheck } from "./useImageCheck";
import {
  useDeploy,
  type DeployStep,
  type ProjectChoice,
  type Variable,
} from "./useDeploy";
import { isDomainCode, useDomainCheck, type DomainCode } from "./useDomainCheck";
import { isValidDomain, isValidName, isValidPort } from "./validation";

const initialPort = "8080";
const NEW_PROJECT = "__new__";
const variableName = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
type Field = "name" | "image" | "port" | "project" | "domain" | `variable-${number}`;
type Row = Variable & { key: number };

const deployPlan: DeployStep[] = [
  "image",
  "download",
  "project",
  "save",
  "variables",
  "service",
  "start",
];
const savePlan: DeployStep[] = ["image", "project", "save", "variables"];

/**
 * Creation as one action. In experimental mode «Deploy» checks the image,
 * downloads it if missing, saves and starts the service; «Save without
 * deploying» stops after saving. Demo mode only saves.
 */
export function NewDefinition() {
  const { t } = useI18n();
  const d = t.definitions;
  const x = d.deploy;
  const capabilities = useCapabilities();
  const navigate = useNavigate();
  const ids = useId();
  const [name, setName] = useState("");
  const [image, setImage] = useState("");
  const [portChosen, setPortChosen] = useState<string>();
  const [accessChosen, setAccessChosen] = useState<Access>();
  const [shared, setShared] = useState(false);
  const [domainChosen, setDomainChosen] = useState<string>();
  const [serverDomain, setServerDomain] = useState<{ value: string; code: DomainCode }>();
  const [suggested, setSuggested] = useState(0);
  const [moreOpen, setMoreOpen] = useState(false);
  const [rows, setRows] = useState<Row[]>([]);
  const nextKey = useRef(0);
  const [newProjectName, setNewProjectName] = useState("");
  const [shown, setShown] = useState(false);
  const [focusField, setFocusField] = useState<{ field: Field }>();
  useEffect(() => {
    if (focusField)
      document.getElementById(`${ids}-${focusField.field}`)?.focus();
  }, [focusField, ids]);
  const leaving = useRef(false);
  const [params] = useSearchParams();
  const configured = !!capabilities.data?.worker.configured;
  const ready = capabilities.workerReady;
  const experimental = capabilities.data?.mode === "experimental";
  const current = capabilities.current;
  const canDeploy =
    experimental &&
    capabilities.vaultReady &&
    !!current?.real_execution.enabled &&
    current.real_execution.provider === "ready";
  const imageCheck = useImageCheck(image, configured && ready);
  // The image's EXPOSE is kept until a newer answer replaces it, so typing does not make the form jump.
  const exposed = imageCheck.current && imageCheck.check.data ? (imageCheck.check.data.exposed_port ?? 0) : imageCheck.current && imageCheck.check.isError ? 0 : undefined;
  if (exposed !== undefined && exposed !== suggested) setSuggested(exposed);
  const suggestion = experimental && suggested ? suggested : undefined;
  const access: Access = !experimental ? "isolated" : (accessChosen ?? (suggestion ? "local" : "isolated"));
  const port = portChosen ?? (suggestion ? String(suggestion) : initialPort);
  const domain = domainChosen ?? name;
  const projects = useQuery({
    queryKey: ["projects"],
    queryFn: api.workerProjects,
    enabled: configured && ready,
  });
  // Unknown ids in `?project=` are ignored; a user choice overrides the preselection.
  const requested = params.get("project") ?? "";
  const initialProject = projects.data?.some((p) => p.id === requested)
    ? requested
    : "";
  const [chosenProject, setChosenProject] = useState<string>();
  const projectValue = chosenProject ?? initialProject;
  // The domain is the one field the server can reject at save time: go back to it with the draft kept.
  const deploy = useDeploy(undefined, undefined, (step, failure) => {
    const code = failure instanceof ApiError ? failure.code : undefined;
    if (step !== "save" || !isDomainCode(code) || access !== "local") return false;
    setServerDomain({ value: domain, code });
    setShown(true);
    setFocusField({ field: "domain" });
    return true;
  });
  const appsPort = capabilities.data?.apps_port;
  const a = d.access;
  // A name that is not valid yet is already reported on its own field.
  const domainVisible = access === "local" && (domainChosen !== undefined || isValidName(name));
  const domainCheck = useDomainCheck(domain, domainVisible && ready);
  const started = deploy.steps.some((step) => step.state === "done");
  const locked = deploy.running || started;

  const names = rows.map((row) => row.name.trim());
  const problems: Partial<Record<Field, string>> = {
    name: isValidName(name) ? "" : d.nameInvalid,
    image: imageProblem(image, imageCheck, t),
    port: isValidPort(port) ? "" : d.portInvalid,
    domain: !domainVisible
      ? ""
      : !isValidDomain(domain)
        ? a.domainInvalid
        : domainCheck
          ? t.apiErrors[domainCheck]
          : serverDomain?.value === domain
            ? t.apiErrors[serverDomain.code]
            : "",
    project:
      projectValue === NEW_PROJECT && !/^.{1,40}$/u.test(newProjectName.trim())
        ? x.newProjectInvalid
        : "",
  };
  rows.forEach((row, index) => {
    const n = row.name.trim();
    problems[`variable-${index}`] = !variableName.test(n)
      ? x.variableInvalid
      : names.indexOf(n) !== index
        ? x.variableDuplicate
        : "";
  });
  const error = (field: Field) => (shown ? (problems[field] ?? "") : "");
  // Availability answers appear while typing; format problems wait until the field is touched or sent.
  const domainError = shown || domainChosen !== undefined || domainCheck || serverDomain?.value === domain ? (problems.domain ?? "") : "";

  const dirty =
    (name !== "" ||
      image !== "" ||
      port !== initialPort ||
      access !== "isolated" ||
      shared ||
      domainChosen !== undefined ||
      rows.length > 0 ||
      projectValue !== initialProject) &&
    !deploy.finished;
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty &&
      !leaving.current &&
      (currentLocation.pathname !== nextLocation.pathname ||
        currentLocation.search !== nextLocation.search),
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

  const project: ProjectChoice =
    projectValue === NEW_PROJECT
      ? { kind: "new", name: newProjectName }
      : projectValue
        ? { kind: "existing", id: projectValue }
        : { kind: "none" };
  const open = (id: string) => {
    leaving.current = true;
    void navigate(`/apps/definition/${encodeURIComponent(id)}`);
  };
  const [plan, setPlan] = useState<DeployStep[]>(deployPlan);
  const start = async (chosen: DeployStep[]) => {
    setPlan(chosen);
    const id = await deploy.run(chosen, {
      name,
      image,
      port: Number(port),
      project,
      access,
      shared: access !== "isolated" && shared,
      domain,
      variables: rows.map(({ name, value, secret }) => ({
        name: name.trim(),
        value,
        secret,
      })),
      projects: projects.data ?? [],
    });
    if (id) open(id);
  };
  const submit = (chosen: DeployStep[]) => (event?: FormEvent) => {
    event?.preventDefault();
    const first = (Object.keys(problems) as Field[]).find(
      (field) => problems[field],
    );
    if (first) {
      setShown(true);
      setFocusField({ field: first });
      return;
    }
    if (ready) void start(chosen);
  };
  const setRow = (key: number, patch: Partial<Row>) =>
    setRows((all) =>
      all.map((row) => (row.key === key ? { ...row, ...patch } : row)),
    );
  const addRow = () => {
    const key = nextKey.current++;
    setRows((all) => [...all, { key, name: "", value: "", secret: true }]);
    setFocusField({ field: `variable-${rows.length}` });
  };

  return (
    <section aria-labelledby={`${ids}-title`} className="max-w-xl">
      <h2 id={`${ids}-title`} className="sr-only">
        {d.newDefinition}
      </h2>
      {!configured ? (
        <p>{d.startWorker}</p>
      ) : (
        <form onSubmit={submit(canDeploy ? deployPlan : savePlan)} noValidate>
          <fieldset disabled={locked} className="contents">
            <FieldGroup>
              <Field data-invalid={!!error("name")}>
                <FieldLabel htmlFor={`${ids}-name`}>{d.name}</FieldLabel>
                <Input
                  id={`${ids}-name`}
                  value={name}
                  autoComplete="off"
                  autoCapitalize="none"
                  aria-invalid={!!error("name")}
                  aria-describedby={`${ids}-name-help`}
                  onChange={(e) => setName(e.target.value)}
                />
                <FieldDescription id={`${ids}-name-help`}>
                  {d.nameHint}
                </FieldDescription>
                {error("name") && <FieldError>{error("name")}</FieldError>}
              </Field>

              <Field data-invalid={!!error("image")}>
                <FieldLabel htmlFor={`${ids}-image`}>
                  {experimental ? d.imageLabel : d.imageByDigest}
                </FieldLabel>
                <Input
                  id={`${ids}-image`}
                  value={image}
                  autoComplete="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  aria-invalid={!!error("image")}
                  aria-describedby={`${ids}-image-help ${ids}-image-status`}
                  placeholder={
                    experimental
                      ? d.image.placeholder
                      : "registry/image@sha256:…"
                  }
                  onChange={(e) => setImage(e.target.value)}
                />
                <FieldDescription id={`${ids}-image-help`}>
                  {experimental ? d.image.hint : d.imageHint}
                </FieldDescription>
                <div id={`${ids}-image-status`}>
                  <ImageAvailability
                    image={imageCheck}
                    typed={image}
                    showError={!error("image")}
                    reserve={experimental}
                  />
                </div>
                {error("image") && <FieldError>{error("image")}</FieldError>}
              </Field>

              {ready && (
                <Field data-invalid={!!error("project")}>
                  <FieldLabel htmlFor={`${ids}-project-select`}>
                    {d.project}
                  </FieldLabel>
                  <select
                    id={`${ids}-project-select`}
                    className="h-9 w-full rounded-lg border bg-background px-2 text-sm focus-visible:outline-2 focus-visible:outline-ring"
                    value={projectValue}
                    onChange={(e) => {
                      setChosenProject(e.target.value);
                      if (e.target.value === NEW_PROJECT)
                        setFocusField({ field: "project" });
                    }}
                  >
                    <option value="">{d.noProject}</option>
                    {(projects.data ?? []).map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                    <option value={NEW_PROJECT}>{x.newProject}</option>
                  </select>
                  {projectValue === NEW_PROJECT && (
                    <>
                      <FieldLabel
                        htmlFor={`${ids}-project`}
                        className="sr-only"
                      >
                        {x.newProjectName}
                      </FieldLabel>
                      <Input
                        id={`${ids}-project`}
                        value={newProjectName}
                        placeholder={x.newProjectName}
                        autoComplete="off"
                        maxLength={40}
                        aria-invalid={!!error("project")}
                        onChange={(e) => setNewProjectName(e.target.value)}
                      />
                    </>
                  )}
                  {error("project") && (
                    <FieldError>{error("project")}</FieldError>
                  )}
                </Field>
              )}

              <fieldset className="grid gap-2">
                <legend className="text-sm font-medium">
                  {x.variablesTitle}
                </legend>
                <p className="text-sm text-muted-foreground">
                  {x.variablesHint}
                </p>
                {rows.map((row, index) => {
                  const problem = error(`variable-${index}`);
                  return (
                    <div key={row.key} className="grid gap-1">
                      <div className="grid gap-2 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] sm:items-center">
                        <Input
                          id={`${ids}-variable-${index}`}
                          aria-label={`${x.variableName} ${index + 1}`}
                          placeholder={x.variableName}
                          value={row.name}
                          autoComplete="off"
                          autoCapitalize="none"
                          spellCheck={false}
                          aria-invalid={!!problem}
                          className="font-mono"
                          onChange={(e) =>
                            setRow(row.key, { name: e.target.value })
                          }
                        />
                        <Input
                          aria-label={`${x.variableValue} ${row.name || index + 1}`}
                          placeholder={x.variableValue}
                          type={row.secret ? "password" : "text"}
                          value={row.value}
                          autoComplete="off"
                          spellCheck={false}
                          onChange={(e) =>
                            setRow(row.key, { value: e.target.value })
                          }
                        />
                        <div className="flex items-center justify-between gap-2">
<label className="flex items-center gap-1.5 text-sm">
                          <input
                            type="checkbox"
                            checked={row.secret}
                            onChange={(e) =>
                              setRow(row.key, { secret: e.target.checked })
                            }
                          />
                          {x.variableSecret}
                        </label>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={x.removeVariable(row.name.trim())}
                          onClick={() =>
                            setRows((all) =>
                              all.filter((r) => r.key !== row.key),
                            )
                          }
                        >
                          <X aria-hidden />
                        </Button>
</div>
                      </div>
                      {problem && (
                        <p className="text-sm text-destructive">{problem}</p>
                      )}
                    </div>
                  );
                })}
                <div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={addRow}
                  >
                    <Plus data-icon="inline-start" />
                    {x.addVariable}
                  </Button>
                </div>
              </fieldset>

              <details
                className="group text-sm"
                open={moreOpen || access !== "isolated" || !!error("port")}
                onToggle={(event) => setMoreOpen(event.currentTarget.open)}
              >
                <summary className="cursor-pointer font-medium">
                  {x.moreOptions}
                </summary>
                <div className="mt-3 grid gap-4">
                {experimental && (
                  <AccessFields
                    ids={ids}
                    access={access}
                    onAccess={setAccessChosen}
                    shared={shared}
                    onShared={setShared}
                    domain={domain}
                    onDomain={(value) => {
                      setDomainChosen(value);
                      setServerDomain(undefined);
                    }}
                    domainError={domainError}
                    appsPort={appsPort}
                    suggestedPort={accessChosen === undefined ? suggestion : undefined}
                  />
                )}
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
                    onChange={(e) => setPortChosen(e.target.value)}
                  />
                  <FieldDescription>{a.portHint[access === "isolated" ? "isolated" : "network"]}</FieldDescription>
                  {error("port") && <FieldError>{error("port")}</FieldError>}
                </Field>
                </div>
              </details>

              {!ready && <p>{d.workerTemporarilyDown}</p>}
              {canDeploy && access === "isolated" && (
                <p className="text-sm text-muted-foreground">{a.willRun.isolated}</p>
              )}
              {!locked && (
                <div className="flex flex-wrap gap-2">
                  {canDeploy ? (
                    <>
                      <Button type="submit" disabled={!ready}>
                        {x.action}
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        disabled={!ready}
                        onClick={() => submit(savePlan)()}
                      >
                        {x.saveOnly}
                      </Button>
                    </>
                  ) : (
                    <Button type="submit" disabled={!ready}>
                      {d.saveDefinition}
                    </Button>
                  )}
                </div>
              )}
            </FieldGroup>
          </fieldset>
          <div className="mt-6">
            <DeployProgress
              deploy={deploy}
              onRetry={() => void start(plan)}
              onOpen={deploy.appId() ? () => open(deploy.appId()!) : undefined}
            />
          </div>
        </form>
      )}
      <Dialog
        open={blocker.state === "blocked"}
        onOpenChange={(isOpen) => {
          if (!isOpen && blocker.state === "blocked") blocker.reset();
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{d.discardTitle}</DialogTitle>
            <DialogDescription>{d.discardDraft}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="destructive"
              onClick={() => blocker.state === "blocked" && blocker.proceed()}
            >
              {d.discardAction}
            </Button>
            <Button
              variant="outline"
              onClick={() => blocker.state === "blocked" && blocker.reset()}
            >
              {d.keepEditing}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
