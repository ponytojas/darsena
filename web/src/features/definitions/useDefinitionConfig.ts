import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/api/client";

export type Editor = { id: number; name: string; value: string; secret: boolean; initialName: string; initialValue: string; initialSecret: boolean; existing: boolean };
type Change = { resource: string; revision: number; name: string; editorId: number };

export function useDefinitionConfig(selected: string, vaultReady: boolean) {
  const cache = useQueryClient();
  const selectedRef = useRef(selected);
  useEffect(() => { selectedRef.current = selected; }, [selected]);
  const nextEditor = useRef(0);
  const editorRef = useRef<Editor | null>(null);
  const [editor, setEditorState] = useState<Editor | null>(null);
  const setEditor = useCallback((next: Editor | null) => { editorRef.current = next; setEditorState(next); }, []);
  const [confirmDelete, setConfirmDelete] = useState("");
  const deleteIdentity = useRef(0);
  const [conflict, setConflict] = useState(false);
  const [deleteConflict, setDeleteConflict] = useState(false);
  const dirty = !!editor && (editor.name !== editor.initialName || editor.value !== editor.initialValue || editor.secret !== editor.initialSecret);
  const config = useQuery({ queryKey: ["worker-config", selected], queryFn: () => api.workerConfig(selected), enabled: !!selected && vaultReady });
  const save = useMutation({ mutationFn: (change: Change & { value: string; secret: boolean }) => api.workerSetVariable(change.resource, change.name, { revision: change.revision, value: change.value, secret: change.secret }), onSuccess: (_data, change) => {
    void cache.invalidateQueries({ queryKey: ["worker-config", change.resource] });
    if (selectedRef.current === change.resource && editorRef.current?.id === change.editorId) { setEditor(null); setConflict(false); }
  }, onError: (error, change) => { if (selectedRef.current === change.resource && editorRef.current?.id === change.editorId && error instanceof ApiError && error.status === 409) setConflict(true); } });
  const remove = useMutation({ mutationFn: (change: Change) => api.workerDeleteVariable(change.resource, change.name, change.revision), onSuccess: (_data, change) => {
    void cache.invalidateQueries({ queryKey: ["worker-config", change.resource] });
    if (selectedRef.current === change.resource && deleteIdentity.current === change.editorId) { setConfirmDelete(""); setDeleteConflict(false); }
  }, onError: (error, change) => { if (selectedRef.current === change.resource && deleteIdentity.current === change.editorId && error instanceof ApiError && error.status === 409) setDeleteConflict(true); } });
  const matchedVariable = editor && !editor.existing ? config.data?.variables.find((item) => item.name === editor.name) : undefined;
  const editorSecret = matchedVariable?.secret ?? editor?.secret ?? true;
  const savingEditor = save.isPending && save.variables?.resource === selected && save.variables.editorId === editor?.id;
  // A dirty draft is never discarded silently: the page shows a dialog and resumes the action on confirmation.
  const [pendingDiscard, setPendingDiscard] = useState<(() => void) | null>(null);
  const guardDiscard = (proceed: () => void) => { if (dirty) setPendingDiscard(() => proceed); else proceed(); };
  const confirmDiscard = () => { const proceed = pendingDiscard; setPendingDiscard(null); proceed?.(); };
  const keepEditing = () => setPendingDiscard(null);
  const resetDrafts = useCallback(() => { setEditor(null); setConfirmDelete(""); setConflict(false); setDeleteConflict(false); }, [setEditor]);
  const openEditor = (variable?: { name: string; value?: string; secret: boolean }) => guardDiscard(() => {
    const initialValue = variable && !variable.secret ? variable.value || "" : "";
    setEditor({ id: ++nextEditor.current, name: variable?.name || "", value: initialValue, secret: variable?.secret ?? true, initialName: variable?.name || "", initialValue, initialSecret: variable?.secret ?? true, existing: !!variable });
    setConflict(false); setConfirmDelete(""); save.reset();
  });
  const cancelEditor = () => guardDiscard(() => { setEditor(null); setConflict(false); save.reset(); });
  const submitVariable = (e: FormEvent) => { e.preventDefault(); if (vaultReady && config.data && editor && !conflict) save.mutate({ resource: selected, revision: config.data.revision, name: editor.name, editorId: editor.id, value: editor.value, secret: editorSecret }); };
  const askDelete = (name: string) => { deleteIdentity.current++; setConfirmDelete(name); setDeleteConflict(false); remove.reset(); };
  const cancelDelete = () => { deleteIdentity.current++; setConfirmDelete(""); };
  const confirmDeletion = (name: string) => remove.mutate({ resource: selected, revision: config.data!.revision, name, editorId: deleteIdentity.current });
  const refreshConflict = async (deletion = false) => {
    const resource = selected;
    const identity = deletion ? deleteIdentity.current : editor?.id;
    const result = await config.refetch();
    if (!result.isError && selectedRef.current === resource) {
      if (deletion && deleteIdentity.current === identity) setDeleteConflict(false);
      if (!deletion && editorRef.current?.id === identity) setConflict(false);
    }
  };
  return { config, save, remove, editor, setEditor, dirty, conflict, deleteConflict, confirmDelete, matchedVariable, editorSecret, savingEditor, discardPending: !!pendingDiscard, confirmDiscard, keepEditing, resetDrafts, openEditor, cancelEditor, submitVariable, askDelete, cancelDelete, confirmDeletion, refreshConflict };
}
