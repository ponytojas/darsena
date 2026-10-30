import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertCircle,
  ArrowLeft,
  ArrowRight,
  Check,
  GitBranch,
  LoaderCircle,
  Package,
  Play,
} from "lucide-react";
import { api, type CreateInput, type Scenario } from "@/api/client";
import { es as t } from "@/i18n/es";
import { PageTitle } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
const initial: CreateInput = {
  name: "",
  repository: "demo/mi-aplicacion",
  branch: "main",
  context: ".",
  port: 3000,
  scenario: "success",
};
function restore(): CreateInput {
  try {
    const raw = sessionStorage.getItem("darsena.draft");
    if (raw) return { ...initial, ...JSON.parse(raw) };
  } catch {
    /* Draft storage is optional. */
  }
  return initial;
}
export default function NewApplication() {
  const [draft, setDraft] = useState<CreateInput>(restore);
  const [step, setStep] = useState(0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const titleRef = useRef<HTMLHeadingElement>(null);
  const navigate = useNavigate();
  const cache = useQueryClient();
  const changed = JSON.stringify(draft) !== JSON.stringify(initial);
  useEffect(() => {
    try {
      sessionStorage.setItem("darsena.draft", JSON.stringify(draft));
    } catch {
      /* Optional storage. */
    }
  }, [draft]);
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (changed) e.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [changed]);
  useEffect(() => {
    titleRef.current?.focus();
  }, [step]);
  const mutation = useMutation({
    mutationFn: api.create,
    onSuccess: async (result) => {
      try {
        sessionStorage.removeItem("darsena.draft");
      } catch {
        /* Optional storage. */
      }
      await Promise.all([
        cache.invalidateQueries({ queryKey: ["applications"] }),
        cache.invalidateQueries({ queryKey: ["operations"] }),
      ]);
      navigate(`/operations/${result.operation.id}`);
    },
  });
  const update = <K extends keyof CreateInput>(
    key: K,
    value: CreateInput[K],
  ) => {
    setDraft((current) => ({ ...current, [key]: value }));
    setErrors((current) => ({ ...current, [key]: "" }));
    mutation.reset();
  };
  const validate = () => {
    const next: Record<string, string> = {};
    if (step === 0) {
      if (
        !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(draft.repository) ||
        draft.repository.length > 160
      )
        next.repository = t.repoInvalid;
      if (!draft.branch.trim() || draft.branch.length > 100)
        next.branch = t.branchInvalid;
    }
    if (step === 1) {
      if (!/^[a-z][a-z0-9-]{1,39}$/.test(draft.name)) next.name = t.nameInvalid;
      if (!Number.isInteger(draft.port) || draft.port < 1 || draft.port > 65535)
        next.port = t.portInvalid;
      if (
        !draft.context ||
        draft.context.startsWith("/") ||
        draft.context.includes("..") ||
        draft.context.length > 200
      )
        next.context = t.contextInvalid;
    }
    setErrors(next);
    if (Object.keys(next).length) {
      requestAnimationFrame(() =>
        document.getElementById(Object.keys(next)[0])?.focus(),
      );
      return false;
    }
    return true;
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!validate()) return;
    if (step < 2) setStep(step + 1);
    else mutation.mutate(draft);
  };
  const fields = (
    key: "repository" | "branch" | "name" | "context",
    label: string,
    hint?: string,
  ) => (
    <Field data-invalid={!!errors[key]}>
      <FieldLabel htmlFor={key}>{label}</FieldLabel>
      <Input
        id={key}
        value={draft[key]}
        autoComplete="off"
        aria-invalid={!!errors[key]}
        aria-describedby={`${key}-help`}
        onChange={(e) => update(key, e.target.value)}
      />
      <FieldDescription id={`${key}-help`}>{hint}</FieldDescription>
      {errors[key] && <FieldError>{errors[key]}</FieldError>}
    </Field>
  );
  return (
    <div className="page wizard-page">
      <PageTitle
        eyebrow={t.applications}
        title={t.newApp}
        description={t.wizardDescription}
      />
      <ol className="wizard-progress" aria-label={t.newApp}>
        {[t.originStep, t.configureStep, t.reviewStep].map((label, index) => (
          <li
            key={label}
            aria-current={step === index ? "step" : undefined}
            data-complete={step > index}
          >
            <span>
              {step > index ? <Check aria-hidden="true" /> : index + 1}
            </span>
            {label}
          </li>
        ))}
      </ol>
      <div className="wizard-layout">
        <form onSubmit={submit} noValidate>
          <div className="form-section">
            <span className="section-number">0{step + 1}</span>
            <h2 tabIndex={-1} ref={titleRef}>
              {[t.originTitle, t.configureTitle, t.reviewTitle][step]}
            </h2>
            <p className="form-description">
              {
                [
                  t.originDescription,
                  t.configureDescription,
                  t.reviewDescription,
                ][step]
              }
            </p>
            {step === 0 && (
              <FieldGroup>
                {fields("repository", t.repository, t.repositoryHint)}
                {fields("branch", t.branch)}
              </FieldGroup>
            )}
            {step === 1 && (
              <FieldGroup>
                {fields("name", t.name, t.nameHint)}
                <Field data-invalid={!!errors.port}>
                  <FieldLabel htmlFor="port">{t.port}</FieldLabel>
                  <Input
                    id="port"
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={65535}
                    value={draft.port || ""}
                    aria-invalid={!!errors.port}
                    onChange={(e) => update("port", Number(e.target.value))}
                  />
                  {errors.port && <FieldError>{errors.port}</FieldError>}
                </Field>
                {fields("context", t.context, t.contextHint)}
              </FieldGroup>
            )}
            {step === 2 && (
              <>
                <dl className="review-list">
                  <div>
                    <dt>{t.application}</dt>
                    <dd>{draft.name}</dd>
                  </div>
                  <div>
                    <dt>{t.repository}</dt>
                    <dd>{draft.repository}</dd>
                  </div>
                  <div>
                    <dt>{t.branch}</dt>
                    <dd>{draft.branch}</dd>
                  </div>
                  <div>
                    <dt>{t.context}</dt>
                    <dd className="mono">{draft.context}</dd>
                  </div>
                  <div>
                    <dt>{t.port}</dt>
                    <dd className="mono">{draft.port}</dd>
                  </div>
                </dl>
                <FieldSet>
                  <FieldLegend>{t.scenario}</FieldLegend>
                  <FieldDescription>{t.scenarioHelp}</FieldDescription>
                  <ToggleGroup
                    aria-label={t.scenario}
                    variant="outline"
                    value={[draft.scenario]}
                    onValueChange={(values) => {
                      if (values[0]) update("scenario", values[0] as Scenario);
                    }}
                    className="scenario-control"
                  >
                    {(Object.keys(t.scenarioLabels) as Scenario[]).map(
                      (value) => (
                        <ToggleGroupItem value={value} key={value}>
                          {t.scenarioLabels[value]}
                        </ToggleGroupItem>
                      ),
                    )}
                  </ToggleGroup>
                  <p className="scenario-description">
                    {t.scenarioDescriptions[draft.scenario]}
                  </p>
                </FieldSet>
              </>
            )}
            {mutation.isError && (
              <Alert variant="destructive">
                <AlertCircle />
                <AlertTitle>{t.unavailable}</AlertTitle>
                <AlertDescription>{mutation.error.message}</AlertDescription>
              </Alert>
            )}
          </div>
          <div className="form-actions">
            {step ? (
              <Button
                type="button"
                variant="outline"
                disabled={mutation.isPending}
                onClick={() => setStep(step - 1)}
              >
                <ArrowLeft data-icon="inline-start" />
                {t.previous}
              </Button>
            ) : (
              <Button variant="ghost" render={<Link to="/" />}>
                {t.cancel}
              </Button>
            )}
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending ? (
                <LoaderCircle data-icon="inline-start" className="spinner" />
              ) : step === 2 ? (
                <Play data-icon="inline-start" />
              ) : null}
              {mutation.isPending ? t.creating : step === 2 ? t.deploy : t.next}
              {step < 2 && <ArrowRight data-icon="inline-end" />}
            </Button>
          </div>
        </form>
        <aside className="wizard-preview" aria-label={t.details}>
          <span className="preview-icon">
            <Package />
          </span>
          <p className="eyebrow">{t.application}</p>
          <h3>{draft.name || "mi-aplicacion"}</h3>
          <p className="preview-repo">{draft.repository}</p>
          <div className="preview-branch">
            <GitBranch />
            {draft.branch}
          </div>
          <dl>
            <div>
              <dt>{t.simulatedURL}</dt>
              <dd>{draft.name || "mi-aplicacion"}.example.invalid</dd>
            </div>
            <div>
              <dt>{t.configuration}</dt>
              <dd>{t.dockerfile}</dd>
            </div>
          </dl>
          <p className="preview-footnote">{t.draftNotice}</p>
        </aside>
      </div>
    </div>
  );
}
