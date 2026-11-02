import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";

type Variable = { name: string; value?: string; secret: boolean };

export function VariableList({ variables, vaultReady, confirmDelete, deleteConflict, remove, onEdit, onAskDelete, onCancelDelete, onConfirmDelete, onRefresh }: {
  variables: Variable[];
  vaultReady: boolean;
  confirmDelete: string;
  deleteConflict: boolean;
  remove: { isError: boolean; isPending: boolean; error: Error | null };
  onEdit: (variable: Variable) => void;
  onAskDelete: (name: string) => void;
  onCancelDelete: () => void;
  onConfirmDelete: (name: string) => void;
  onRefresh: () => void;
}) {
  const { t } = useI18n();
  const d = t.definitions;
  return <ul className="mt-4 border-t border-border">{variables.map((item) => <li className="border-b border-border py-3.5" key={item.name}><div className="flex flex-wrap items-start justify-between gap-4"><span className="min-w-0"><strong className="text-sm font-semibold">{item.name}</strong><small className="mt-1 block break-words text-xs text-muted-foreground">{item.secret ? d.secretStored : item.value || d.emptyValue}</small></span><div className="flex flex-wrap gap-1.5"><Button disabled={!vaultReady} size="sm" variant="ghost" onClick={() => onEdit(item)}>{item.secret ? d.replaceSecret : d.edit}<span className="sr-only"> {item.name}</span></Button><Button disabled={!vaultReady} size="sm" variant="ghost" aria-label={d.deleteVariable(item.name)} onClick={() => onAskDelete(item.name)}>{d.delete}</Button></div></div>
    {confirmDelete === item.name && <div className="mt-3 grid justify-items-start gap-2 rounded-md bg-muted p-3 text-xs"><p>{d.deleteNotice}</p>{remove.isError && <p role="alert" className="worker-error">{deleteConflict ? d.deleteConflict : errorMessage(remove.error, t)}</p>}{deleteConflict && <Button size="sm" variant="outline" onClick={onRefresh}>{d.refreshConfig}</Button>}<div className="flex gap-1.5"><Button size="sm" variant="destructive" disabled={!vaultReady || remove.isPending || deleteConflict} onClick={() => onConfirmDelete(item.name)}>{d.confirmDelete}</Button><Button size="sm" variant="ghost" onClick={onCancelDelete}>{d.cancel}</Button></div></div>}</li>)}</ul>;
}
