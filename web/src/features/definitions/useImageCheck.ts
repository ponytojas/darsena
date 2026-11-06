import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Messages } from "@/i18n/es";
import { errorMessage } from "@/i18n/errors";
import { isValidImage } from "./validation";

function useDebounced(value: string, delay: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

/**
 * Asks the worker for the canonical form of an image reference and, in
 * experimental mode, whether the runtime has it. The server is the single
 * source of truth for normalization; a running download is polled.
 */
export function useImageCheck(input: string, enabled: boolean) {
  const cache = useQueryClient();
  const typed = input.trim();
  const ref = useDebounced(typed, 400);
  const check = useQuery({
    queryKey: ["image-check", ref],
    queryFn: () => api.workerImage(ref),
    enabled: enabled && ref !== "",
    retry: false,
    staleTime: 5_000,
    refetchInterval: (query) =>
      query.state.data?.status === "pulling" ? 1_500 : false,
  });
  const status = check.data?.status;
  const previous = useRef(status);
  useEffect(() => {
    // A finished download changes the answer for every spelling of this image.
    if (previous.current === "pulling" && status !== "pulling")
      void cache.invalidateQueries({ queryKey: ["image-check"] });
    previous.current = status;
  }, [status, cache]);
  const current = enabled && ref === typed && typed !== "";
  return {
    check,
    /** The check reflects what is typed now. */
    current,
    /** Waiting for the debounce or the first answer. */
    settling: enabled && typed !== "" && (ref !== typed || check.isPending),
    /** Canonical pinned reference to save, once known for the current input. */
    resolved: current && check.data ? check.data.reference : undefined,
  };
}
export type ImageCheckState = ReturnType<typeof useImageCheck>;

/** Problem to show when saving, or "" when the image can be saved. */
export function imageProblem(input: string, image: ImageCheckState, t: Messages): string {
  if (image.resolved) return "";
  if (image.current && image.check.isError) return errorMessage(image.check.error, t);
  // A reference already pinned by digest can be saved at once: the server normalizes it.
  if (isValidImage(input)) return "";
  if (image.settling) return t.definitions.image.checking;
  return t.definitions.imageInvalid;
}
