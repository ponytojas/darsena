import { CircleCheck, Loader2, TriangleAlert } from "lucide-react";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import { imageLabel } from "@/lib/image";
import type { ImageCheckState } from "./useImageCheck";

/**
 * What the runtime knows about an image: whether it is downloaded (deploying
 * downloads it when missing) and whether it can run here. Announced politely so a
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
  const x = t.definitions.deploy;
  const { check } = image;
  const data = image.current ? check.data : undefined;
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
    const status = data.status;
    body = (
      <>
        {data.resolved_from && typed !== undefined && <p className="text-muted-foreground">{x.pinsTag(imageLabel(data.reference))}</p>}
        {status === "available" && (
          <p className="flex items-center gap-1.5 text-success">
            <CircleCheck aria-hidden className="size-3.5 shrink-0" />
            {m.available(data.platform ?? "")}
          </p>
        )}
        {(status === "missing" || status === "pull_failed") && <p className="text-muted-foreground">{x.willDownload}</p>}
        {status === "pulling" && (
          <p className="flex items-center gap-1.5">
            <Loader2 aria-hidden className="size-3.5 shrink-0 motion-safe:animate-spin" />
            {m.pulling}
          </p>
        )}
        {status === "incompatible" && (
          <p className="flex items-center gap-1.5 text-destructive">
            <TriangleAlert aria-hidden className="size-3.5 shrink-0" />
            {data.reason === "volumes" ? m.volumes : m.platform(data.platform ?? "?", data.daemon_platform ?? "?")}
          </p>
        )}
        {status === "unknown" && <p className="text-muted-foreground">{m.unknown}</p>}
      </>
    );
  }
  return (
    <div aria-live="polite" className={`grid gap-1.5 text-sm ${reserve ? "min-h-5" : "empty:hidden"}`}>
      {body}
    </div>
  );
}
