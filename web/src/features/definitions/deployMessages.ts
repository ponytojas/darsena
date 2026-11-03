import type { Messages } from "@/i18n/es";
import { errorMessage } from "@/i18n/errors";
import type { DeployFailure } from "./useDeploy";

/** Text for a failed step, translated by code or by the image problem found. */
export function failureMessage(failure: DeployFailure, t: Messages) {
  const m = t.definitions.image;
  if (failure.kind === "api") return errorMessage(failure.error, t);
  if (failure.kind === "timeout") return t.definitions.deploy.timeout;
  const { check } = failure;
  if (check.status === "pull_failed")
    return (
      m.pullFailed[
        (check.reason as keyof typeof m.pullFailed) ?? "unavailable"
      ] ?? m.pullFailed.unavailable
    );
  return check.reason === "volumes"
    ? m.volumes
    : m.platform(check.platform ?? "?", check.daemon_platform ?? "?");
}

