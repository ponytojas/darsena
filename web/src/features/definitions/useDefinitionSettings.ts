import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, type Access } from "@/api/client";
import { imageInput } from "@/lib/image";
import { isValidDomain, isValidPort } from "./validation";

type WorkerApplication = Awaited<ReturnType<typeof api.workerApps>>[number];
type Base = { version: number; image: string; saved: string; port: string; access: Access; shared: boolean; domain: string };
const snapshot = (app: WorkerApplication): Base => ({ version: app.version, image: imageInput(app.image), saved: app.image, port: String(app.port), access: app.access, shared: app.shared_network, domain: app.domain ?? app.name });

/** Draft of the image and port form. The version it was loaded at is kept so a concurrent change is reported, never overwritten. */
export function useDefinitionSettings(app: WorkerApplication, enabled: boolean) {
  const cache = useQueryClient();
  const [base, setBase] = useState(() => snapshot(app));
  const [image, setImage] = useState(base.image);
  const [port, setPort] = useState(base.port);
  const [access, setAccessState] = useState<Access>(base.access);
  const [shared, setSharedState] = useState(base.shared);
  const [domain, setDomainState] = useState(base.domain);
  const [shown, setShown] = useState(false);
  const leaving = useRef(false);
  const projects = useQuery({ queryKey: ["projects"], queryFn: api.workerProjects, enabled });

  const load = (next: WorkerApplication) => {
    const fresh = snapshot(next);
    setBase(fresh);
    setImage(fresh.image);
    setPort(fresh.port);
    setAccessState(fresh.access);
    setSharedState(fresh.shared);
    setDomainState(fresh.domain);
    setShown(false);
  };
  // A service fixes access, network and domain at deployment: they are neither edited nor sent.
  const fixed = app.service_used;
  const networkChanged = !fixed && (access !== base.access || (access !== "isolated" && shared !== base.shared) || (access === "local" && domain !== base.domain));
  const dirty = image !== base.image || port !== base.port || networkChanged;
  const problems = { port: isValidPort(port) ? "" : "port", domain: access === "local" && networkChanged && !isValidDomain(domain) ? "domain" : "" } as const;

  const save = useMutation({
    // The caller passes the canonical reference from the image check when known.
    // An untouched image is sent exactly as saved, so a moving tag never re-pins it.
    mutationFn: (resolved?: string) => api.workerAppUpdate(app.id, { version: base.version, image: image === base.image ? base.saved : (resolved ?? image.trim()), port: Number(port),
      // Sent together or not at all: omitted fields keep what is stored.
      ...(networkChanged ? { access, shared_network: access !== "isolated" && shared, ...(access === "local" ? { domain } : {}) } : {}) }),
    onSuccess: (updated) => {
      load(updated as WorkerApplication);
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    },
    // The application turned out to have a service: re-read it so the fields lock.
    onError: (error) => { if (error instanceof ApiError && error.code === "access_locked") void cache.invalidateQueries({ queryKey: ["worker-apps"] }); },
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
    // A service that still exists is stopped and its container removed first;
    // the server deletes only once it confirms the container is gone.
    mutationFn: async () => {
      if (app.service_used) {
        let service = await api.service(app.id).catch((error: unknown) => {
          if (error instanceof ApiError && error.status === 404) return undefined;
          throw error;
        });
        if (service?.observation === "running") service = await api.stopService(app.id, service.version);
        if (service && (service.observation === "stopped" || service.observation === "created")) await api.removeService(app.id, service.version);
      }
      await api.workerAppDelete(app.id);
    },
    onSuccess: () => {
      leaving.current = true;
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    },
    onError: () => void cache.invalidateQueries({ queryKey: ["isolated-service", app.id] }),
  });

  return {
    image, port, access, shared, domain, networkChanged, shown, dirty, problems, imageChanged: image !== base.image, projects, save, reload, assign, remove, leaving,
    setImage: (value: string) => { setImage(value); save.reset(); },
    setPort: (value: string) => { setPort(value); save.reset(); },
    setAccess: (value: Access) => { setAccessState(value); if (value === "isolated") setSharedState(false); save.reset(); },
    setShared: (value: boolean) => { setSharedState(value); save.reset(); },
    setDomain: (value: string) => { setDomainState(value); save.reset(); },
    showProblems: () => setShown(true),
  };
}
export type DefinitionSettings = ReturnType<typeof useDefinitionSettings>;
