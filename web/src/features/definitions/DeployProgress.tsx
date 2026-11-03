import { Check, Circle, Loader2, Minus, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n/context";
import { failureMessage } from "./deployMessages";
import type { DeployState, StepState } from "./useDeploy";

const icons: Record<StepState, typeof Check> = {
  pending: Circle,
  active: Loader2,
  done: Check,
  skipped: Minus,
  failed: TriangleAlert,
};

/**
 * The steps of one deploy, each with icon and text (never color alone). The
 * list is announced politely; a failure says which step and how to continue.
 */
export function DeployProgress({
  deploy,
  onRetry,
  onOpen,
}: {
  deploy: DeployState;
  onRetry: () => void;
  onOpen?: () => void;
}) {
  const { t } = useI18n();
  const x = t.definitions.deploy;
  if (!deploy.steps.length) return null;
  return (
    <div className="grid gap-3 rounded-lg border p-4">
      <ol aria-live="polite" className="grid gap-2 text-sm">
        {deploy.steps.map(({ step, state }) => {
          const Icon = icons[state];
          return (
            <li
              key={step}
              className={`flex items-center gap-2 ${state === "pending" || state === "skipped" ? "text-muted-foreground" : state === "failed" ? "text-destructive" : ""}`}
            >
              <Icon
                aria-hidden
                className={`size-4 shrink-0 ${state === "active" ? "motion-safe:animate-spin" : ""} ${state === "done" ? "text-success" : ""}`}
              />
              <span>{x.steps[step]}</span>
              <span className="sr-only">: {x.states[state]}</span>
              {state === "skipped" && (
                <span aria-hidden className="text-xs">
                  · {x.notNeeded}
                </span>
              )}
              {state === "active" && step === "download" && (
                <span className="text-xs text-muted-foreground">
                  · {x.downloadNote}
                </span>
              )}
            </li>
          );
        })}
      </ol>
      {deploy.failure && (
        <div role="alert" className="grid justify-items-start gap-2 text-sm">
          <p className="text-destructive">
            {failureMessage(deploy.failure.failure, t)}
          </p>
          <p className="text-muted-foreground">{x.kept}</p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" onClick={onRetry}>
              {x.retry}
            </Button>
            {onOpen && (
              <Button type="button" variant="outline" onClick={onOpen}>
                {x.openApp}
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
