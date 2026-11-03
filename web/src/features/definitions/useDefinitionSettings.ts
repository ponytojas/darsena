import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import { isValidPort } from "./validation";

type WorkerApplication = Awaited<ReturnType<typeof api.workerApps>>[number];
type Base = { version: number; image: string; port: string };
const snapshot = (app: WorkerApplication): Base => ({ version: app.version, image: app.image, port: String(app.port) });

/** Draft of the image and port form. The version it was loaded at is kept so a concurrent change is reported, never overwritten. */
export function useDefinitionSettings(app: WorkerApplication, enabled: boolean) {
  const cache = useQueryClient();
  const [base, setBase] = useState(() => snapshot(app));
  const [image, setImage] = useState(base.image);
  const [port, setPort] = useState(base.port);
  const [shown, setShown] = useState(false);
  const leaving = useRef(false);
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.workerProjects, enabled });

  const load = (next: WorkerApplication) => {
    const fresh = snapshot(next);
    setBase(fresh);
    setImage(fresh.image);
    setPort(fresh.port);
    setShown(false);
  };
  const dirty = image !== base.image || port !== base.port;
  const problems = { port: isValidPort(port) ? "" : "port" } as const;

  const save = useMutation({
    // The caller passes the canonical reference from the image check when known.
    mutationFn: (resolved?: string) => api.workerAppUpdate(app.id, { version: base.version, image: resolved ?? image.trim(), port: Number(port) }),
    onSuccess: (updated) => {
      load(updated as WorkerApplication);
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    },
  });
  const reload = useMutation({
    mutationFn: () => cache.fetchQuery({ queryKey: ["worker-apps"], queryFn: api.workerApps, staleTime: 0 }),
    onSuccess: (list) => {
      const fresh = list.find((item) => item.id === app.id);
      if (fresh) load(fresh);
      save.reset();
    },
  });
  const assign = useMutation({
    mutationFn: (projectId: string | null) => api.workerAssign({ project_id: projectId, application_ids: [app.id] }),
    onSettled: () => void cache.invalidateQueries({ queryKey: ["worker-apps"] }),
  });
  const remove = useMutation({
    mutationFn: () => api.workerAppDelete(app.id),
    onSuccess: () => {
      leaving.current = true;
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    },
  });

  return {
    image, port, shown, dirty, problems, projects, save, reload, assign, remove, leaving,
    setImage: (value: string) => { setImage(value); save.reset(); },
    setPort: (value: string) => { setPort(value); save.reset(); },
    showProblems: () => setShown(true),
  };
}
export type DefinitionSettings = ReturnType<typeof useDefinitionSettings>;
