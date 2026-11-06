import { useId, useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertCircle, Pencil, Trash2 } from "lucide-react";
import { api, ApiError, type Project } from "@/api/client";
import { ColorPicker } from "@/components/ColorPicker";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import type { Messages } from "@/i18n/es";

const NAME_MAX = 40;
const UNDO_MS = 10_000;
const nameLength = (value: string) => [...value.trim()].length;

function ErrorNote({ message }: { message: string }) {
  return (
    <Alert variant="destructive" role="alert">
      <AlertCircle />
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  );
}

function refresh(queryClient: QueryClient) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ["projects"] }),
    queryClient.invalidateQueries({ queryKey: ["worker-apps"] }),
  ]);
}

/** Creates a project, or edits `project` (sending its version). */
export function ProjectFormDialog({
  open,
  onOpenChange,
  project,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project?: Project;
}) {
  const { t } = useI18n();
  const p = t.projects;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="project-dialog">
        <DialogHeader>
          <DialogTitle>{project ? p.editTitle : p.createTitle}</DialogTitle>
          <DialogDescription>{project ? p.editDescription : p.createDescription}</DialogDescription>
        </DialogHeader>
        <ProjectForm project={project} close={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  );
}

function ProjectForm({ project, close }: { project?: Project; close: () => void }) {
  const { t } = useI18n();
  const p = t.projects;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const id = useId();
  const [name, setName] = useState(project?.name ?? "");
  const [color, setColor] = useState<Project["color"]>(project?.color ?? "blue");
  const [shown, setShown] = useState(false);
  const mutation = useMutation({
    mutationFn: () =>
      project
        ? api.workerProjectUpdate(project.id, { version: project.version, name: name.trim(), color })
        : api.workerProjectCreate({ name: name.trim(), color }),
    onSuccess: async (saved) => {
      await queryClient.invalidateQueries({ queryKey: ["projects"] });
      close();
      if (!project) void navigate(`/projects/${encodeURIComponent(saved.id)}`);
    },
  });
  const length = nameLength(name);
  const problem = length >= 1 && length <= NAME_MAX ? "" : p.nameInvalid;
  const conflict = mutation.error instanceof ApiError && mutation.error.code === "update_conflict";
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setShown(true);
    if (!problem) mutation.mutate();
  };
  return (
    <form onSubmit={submit} noValidate className="grid gap-4">
      <FieldGroup>
        <Field data-invalid={shown && !!problem}>
          <FieldLabel htmlFor={`${id}-name`}>{p.name}</FieldLabel>
          <Input
            id={`${id}-name`}
            value={name}
            autoComplete="off"
            aria-invalid={shown && !!problem}
            aria-describedby={shown && problem ? `${id}-name-error` : undefined}
            onChange={(event) => setName(event.target.value)}
            onBlur={() => setShown(true)}
          />
          {shown && problem && <FieldError id={`${id}-name-error`}>{problem}</FieldError>}
        </Field>
        <Field>
          <FieldLabel>{p.color}</FieldLabel>
          <ColorPicker label={p.color} value={color} onChange={setColor} />
        </Field>
      </FieldGroup>
      {mutation.error && <ErrorNote message={errorMessage(mutation.error, t)} />}
      <DialogFooter>
        {conflict && (
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              void queryClient.refetchQueries({ queryKey: ["projects"] }).then(() => mutation.reset());
            }}
          >
            {p.reload}
          </Button>
        )}
        <Button type="button" variant="outline" onClick={close}>
          {t.cancel}
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? (project ? p.saving : p.creating) : project ? p.save : p.create}
        </Button>
      </DialogFooter>
    </form>
  );
}

/** Recreates a deleted project and gives back only the applications that are still unassigned. */
async function undoDelete(
  deleted: { project: Project; application_ids: string[] },
  queryClient: QueryClient,
  t: Messages,
) {
  try {
    const created = await api.workerProjectCreate({ name: deleted.project.name, color: deleted.project.color });
    if (deleted.application_ids.length)
      await api.workerAssign({ project_id: created.id, application_ids: deleted.application_ids, only_unassigned: true });
    toast.success(t.projects.restored);
  } catch (error) {
    toast.error(errorMessage(error as Error, t), { duration: UNDO_MS });
  } finally {
    await refresh(queryClient);
  }
}

export function DeleteProjectDialog({
  open,
  onOpenChange,
  project,
  count,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project: Project;
  count: number;
}) {
  const { t } = useI18n();
  const p = t.projects;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: () => api.workerProjectDelete(project.id),
    onSuccess: (deleted) => {
      onOpenChange(false);
      void navigate("/");
      void refresh(queryClient);
      toast(p.deleted, {
        duration: UNDO_MS,
        action: { label: p.undo, onClick: () => void undoDelete(deleted, queryClient, t) },
      });
    },
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) mutation.reset();
        onOpenChange(next);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{p.removeTitle}</DialogTitle>
          <DialogDescription>{p.removeDescription(project.name, count)}</DialogDescription>
        </DialogHeader>
        {mutation.error && <ErrorNote message={errorMessage(mutation.error, t)} />}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t.cancel}
          </Button>
          <Button type="button" variant="destructive" disabled={mutation.isPending} onClick={() => mutation.mutate()}>
            {mutation.isPending ? p.removing : p.removeConfirm}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Header actions of a project page: edit and delete, each with its own dialog. */
export function ProjectActions({ project, count }: { project: Project; count: number }) {
  const { t } = useI18n();
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  return (
    <div className="project-actions">
      <Button variant="outline" onClick={() => setEditing(true)}>
        <Pencil data-icon="inline-start" />
        {t.projects.edit}
      </Button>
      <Button variant="outline" onClick={() => setDeleting(true)}>
        <Trash2 data-icon="inline-start" />
        {t.projects.remove}
      </Button>
      <ProjectFormDialog open={editing} onOpenChange={setEditing} project={project} />
      <DeleteProjectDialog open={deleting} onOpenChange={setDeleting} project={project} count={count} />
    </div>
  );
}
