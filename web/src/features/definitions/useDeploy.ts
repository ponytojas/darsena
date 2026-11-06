import { useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import { api, ApiError, type Access, type ImageCheck, type Project } from "@/api/client";
import { accents } from "@/lib/theme";
import { rememberExit, removeServiceAttempt, writeServiceAttempt } from "@/lib/service-flags";

export type DeployStep =
  "image" | "download" | "project" | "save" | "variables" | "service" | "start";
export type StepState = "pending" | "active" | "done" | "skipped" | "failed";
export type Variable = { name: string; value: string; secret: boolean };
export type ProjectChoice =
  | { kind: "none" }
  | { kind: "existing"; id: string }
  | { kind: "new"; name: string };

export type DeployInput = {
  name: string;
  image: string;
  port: number;
  project: ProjectChoice;
  variables: Variable[];
  projects: Project[];
  /** Network access; omitted (isolated) sends nothing, as before. */
  access?: Access;
  shared?: boolean;
  domain?: string;
  /** Configuration revision the person saw, for an application that already exists. */
  revision?: number;
};

/** Why a step failed: an API error to translate, or a known image problem. */
export type DeployFailure =
  | { kind: "api"; error: Error }
  | { kind: "image"; check: ImageCheck }
  | { kind: "timeout" };

/** What earlier steps produced; kept so a retry resumes instead of repeating. */
type Progress = {
  check?: ImageCheck;
  projectId?: string;
  appId?: string;
  assigned?: boolean;
  variablesDone: number;
  revision: number;
  serviceCreated?: boolean;
  exitCode?: number;
};

const sleep = (ms: number) =>
  new Promise((resolve) => window.setTimeout(resolve, ms));
const preDispatch = new Set([
  "revision_conflict_before_dispatch",
  "image_missing",
  "image_incompatible",
  "invalid_image",
  "runtime_unavailable",
  // Networks and the domain are ensured before the single send is claimed.
  "network_unavailable",
  "network_conflict",
  "invalid_domain",
]);
const PULL_LIMIT_MS = 11 * 60_000;
const START_LIMIT_MS = 20_000;

class StepError extends Error {
  readonly failure: DeployFailure;
  constructor(failure: DeployFailure) {
    super("deploy step failed");
    this.failure = failure;
  }
}

/**
 * Runs creation as one guided action. Every step is safe to resume: what was
 * already created is kept and skipped on retry. Safety checks are steps of
 * the action, not extra work for the person.
 */
export function useDeploy(initial?: { appId: string; revision: number }, onServiceUncertain?: () => void,
  /** Returns true when the person must first fix a form field: the list is cleared, what was created is kept. */
  onFailure?: (step: DeployStep, error: unknown) => boolean,
) {
  const cache = useQueryClient();
  const { t } = useI18n();
  const progress = useRef<Progress>({
    variablesDone: 0,
    revision: initial?.revision ?? 0,
    appId: initial?.appId,
  });
  const [steps, setSteps] = useState<{ step: DeployStep; state: StepState }[]>(
    [],
  );
  const [failure, setFailure] = useState<{
    step: DeployStep;
    failure: DeployFailure;
  }>();
  const [running, setRunning] = useState(false);
  const [finished, setFinished] = useState(false);

  const mark = (step: DeployStep, state: StepState) =>
    setSteps((current) =>
      current.map((item) => (item.step === step ? { ...item, state } : item)),
    );

  async function perform(step: DeployStep, input: DeployInput) {
    const p = progress.current;
    switch (step) {
      case "image": {
        p.check = await api.workerImage(input.image);
        if (p.check.status === "incompatible")
          throw new StepError({ kind: "image", check: p.check });
        return "done";
      }
      case "download": {
        const check = p.check!;
        if (
          check.status !== "missing" &&
          check.status !== "pull_failed" &&
          check.status !== "pulling"
        )
          return "skipped";
        if (check.status !== "pulling")
          await api.workerImagePull(check.reference);
        const deadline = Date.now() + PULL_LIMIT_MS;
        while (Date.now() < deadline) {
          await sleep(1_500);
          const next = await api.workerImage(check.reference);
          if (next.status === "available") return "done";
          if (next.status === "pull_failed" || next.status === "incompatible")
            throw new StepError({ kind: "image", check: next });
        }
        throw new StepError({ kind: "timeout" });
      }
      case "project": {
        if (input.project.kind === "existing") p.projectId = input.project.id;
        if (input.project.kind !== "new") return "skipped";
        if (p.projectId) return "done";
        const used = new Set(input.projects.map((project) => project.color));
        const color = accents.find((accent) => !used.has(accent)) ?? "blue";
        p.projectId = (
          await api.workerProjectCreate({
            name: input.project.name.trim(),
            color,
          })
        ).id;
        void cache.invalidateQueries({ queryKey: ["projects"] });
        return "done";
      }
      case "save": {
        if (!p.appId) {
          p.appId = (
            await api.workerAppCreate({
              name: input.name,
              image: p.check?.reference ?? input.image.trim(),
              port: input.port,
              ...(input.access && input.access !== "isolated"
                ? { access: input.access, shared_network: !!input.shared, ...(input.access === "local" ? { domain: input.domain } : {}) }
                : {}),
            })
          ).id;
          void cache.invalidateQueries({ queryKey: ["worker-apps"] });
        }
        if (p.projectId && !p.assigned) {
          try {
            await api.workerAssign({
              project_id: p.projectId,
              application_ids: [p.appId],
            });
            p.assigned = true;
          } catch (error) {
            // The definition exists; a missing project is reported, not fatal.
            toast.warning(t.definitions.assignFailed(errorMessage(error as Error, t)));
          }
        }
        return "done";
      }
      case "variables": {
        if (input.variables.length === 0) return "skipped";
        for (; p.variablesDone < input.variables.length; p.variablesDone++) {
          const variable = input.variables[p.variablesDone];
          const snapshot = await api.workerSetVariable(
            p.appId!,
            variable.name,
            {
              revision: p.revision,
              value: variable.value,
              secret: variable.secret,
            },
          );
          p.revision = snapshot.revision;
        }
        void cache.invalidateQueries({ queryKey: ["worker-config", p.appId] });
        return "done";
      }
      case "service": {
        if (p.serviceCreated) return "done";
        writeServiceAttempt(p.appId!);
        try {
          await api.createService(p.appId!, p.revision);
        } catch (error) {
          // Rejected before anything was persisted: retrying is safe. Any other
          // failure stays an attempt the detail page asks to verify.
          if (
            error instanceof ApiError &&
            error.code &&
            preDispatch.has(error.code)
          )
            removeServiceAttempt(p.appId!);
          else onServiceUncertain?.();
          // The configuration changed meanwhile: a retry, which the person
          // chooses after reading why, uses the current revision.
          if (error instanceof ApiError && error.code === "revision_conflict_before_dispatch")
            p.revision = (await api.workerConfig(p.appId!)).revision;
          throw error;
        }
        p.serviceCreated = true;
        removeServiceAttempt(p.appId!);
        void cache.invalidateQueries({ queryKey: ["worker-config", p.appId] });
        return "done";
      }
      case "start": {
        const deadline = Date.now() + START_LIMIT_MS;
        while (Date.now() < deadline) {
          try {
            const service = await api.refreshService(p.appId!);
            rememberExit(p.appId!, service);
            cache.setQueryData(["isolated-service", p.appId], service);
            if (service.observation === "running") return "done";
            if (service.observation === "stopped") {
              p.exitCode = service.exit_code;
              return "done";
            }
          } catch (error) {
            // Another runtime operation holds the gate; keep waiting.
            if (!(error instanceof ApiError && error.status === 409))
              throw error;
          }
          await sleep(1_000);
        }
        throw new StepError({ kind: "timeout" });
      }
    }
  }

  async function run(plan: DeployStep[], input: DeployInput) {
    if (running) return;
    setRunning(true);
    const p = progress.current;
    // The first attempt uses what the person saw; a retry keeps what a conflict re-read.
    if (input.revision !== undefined && steps.length === 0) p.revision = input.revision;
    setFailure(undefined);
    setFinished(false);
    // A retry of the same plan resumes; a different plan starts its list afresh.
    const samePlan = steps.map((item) => item.step).join() === plan.join();
    const states = new Map(samePlan ? steps.map((item) => [item.step, item.state]) : []);
    if (!samePlan) setSteps(plan.map((step) => ({ step, state: "pending" })));
    for (const step of plan) {
      if (states.get(step) === "done" || states.get(step) === "skipped")
        continue;
      mark(step, "active");
      try {
        mark(step, await perform(step, input));
      } catch (error) {
        if (onFailure?.(step, error)) {
          setSteps([]);
          setRunning(false);
          return undefined;
        }
        mark(step, "failed");
        setFailure({
          step,
          failure:
            error instanceof StepError
              ? error.failure
              : { kind: "api", error: error as Error },
        });
        setRunning(false);
        void cache.invalidateQueries({ queryKey: ["worker-apps"] });
        // One plain read (never a resend) shows a service that was created after all.
        if (progress.current.appId) void cache.invalidateQueries({ queryKey: ["isolated-service", progress.current.appId] });
        return undefined;
      }
    }
    void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    void cache.invalidateQueries({
      queryKey: ["isolated-service", progress.current.appId],
    });
    setRunning(false);
    setFinished(true);
    return progress.current.appId;
  }

  return {
    steps,
    failure,
    running,
    finished,
    run,
    appId: () => progress.current.appId,
    exitCode: () => progress.current.exitCode,
  };
}
export type DeployState = ReturnType<typeof useDeploy>;
