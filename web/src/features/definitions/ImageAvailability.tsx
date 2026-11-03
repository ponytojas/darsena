import { CircleCheck, Loader2, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import type { ImageCheckState } from "./useImageCheck";

/**
 * What the runtime knows about an image: its canonical form, whether it is
 * downloaded, and the explicit «Download» action. Announced politely so a
 * screen reader hears the result of a check or a finished download.
 */
export function ImageAvailability({
  image,
  typed,
  showError = false,
  reserve = false,
}: {
  image: ImageCheckState;
  typed?: string;
  showError?: boolean;
  /** Keep one line while checking is possible, so a result appearing does not move the controls below under the pointer. */
  reserve?: boolean;
}) {
  const { t } = useI18n();
  const m = t.definitions.image;
  const { check, pull } = image;
  const data = image.current ? check.data : undefined;
  const reference = data?.reference ?? "";
  const download = (label: string) => (
    <Button
      type="button"
      variant="outline"
      size="sm"
      disabled={pull.isPending}
      onClick={() => pull.mutate(reference)}
    >
      {label}
    </Button>
  );
  let body: React.ReactNode = null;
  if (image.settling) {
    body = (
      <p className="flex items-center gap-1.5 text-muted-foreground">
        <Loader2 aria-hidden className="size-3.5 motion-safe:animate-spin" />
        {m.checking}
      </p>
    );
  } else if (showError && image.current && check.isError) {
    body = <p className="text-destructive">{errorMessage(check.error, t)}</p>;
  } else if (data) {
    const changed = typed !== undefined && reference !== typed.trim();
    const status = data.status;
    body = (
      <>
        {changed && (
          <p className="text-muted-foreground">
            {data.resolved_from
              ? m.resolvedFrom(
                  data.resolved_from.replace(/^docker\.io\/(library\/)?/, ""),
                )
              : m.savedAs}{" "}
            <code className="mt-0.5 block font-mono text-xs break-all text-foreground">
              {reference}
            </code>
          </p>
        )}
        {status === "available" && (
          <p className="flex items-center gap-1.5 text-success">
            <CircleCheck aria-hidden className="size-3.5 shrink-0" />
            {m.available(data.platform ?? "")}
          </p>
        )}
        {status === "missing" && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <p>{m.missing}</p>
            {download(m.download)}
          </div>
        )}
        {status === "pulling" && (
          <p className="flex items-center gap-1.5">
            <Loader2
              aria-hidden
              className="size-3.5 shrink-0 motion-safe:animate-spin"
            />
            {m.pulling}
          </p>
        )}
        {status === "pull_failed" && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <p className="flex items-center gap-1.5 text-destructive">
              <TriangleAlert aria-hidden className="size-3.5 shrink-0" />
              {m.pullFailed[
                (data.reason as keyof typeof m.pullFailed) ?? "unavailable"
              ] ?? m.pullFailed.unavailable}
            </p>
            {data.reason !== "platform" && download(m.retry)}
          </div>
        )}
        {status === "incompatible" && (
          <p className="flex items-center gap-1.5 text-destructive">
            <TriangleAlert aria-hidden className="size-3.5 shrink-0" />
            {data.reason === "volumes"
              ? m.volumes
              : m.platform(data.platform ?? "?", data.daemon_platform ?? "?")}
          </p>
        )}
        {status === "unknown" && (
          <p className="text-muted-foreground">{m.unknown}</p>
        )}
        {(status === "missing" || status === "pull_failed") && (
          <p className="text-muted-foreground">{m.publicOnly}</p>
        )}
        {pull.isError && (
          <p className="text-destructive">{errorMessage(pull.error, t)}</p>
        )}
      </>
    );
  }
  return (
    <div aria-live="polite" className={`grid gap-1.5 text-sm ${reserve ? "min-h-5" : "empty:hidden"}`}>
      {body}
    </div>
  );
}
