import type { FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import type { Editor } from "./useDefinitionConfig";

export function VariableEditor({ editor, setEditor, editorSecret, matchedVariable, savingEditor, vaultReady, conflict, save, onSubmit, onRefresh, onCancel }: {
  editor: Editor;
  setEditor: (next: Editor | null) => void;
  editorSecret: boolean;
  matchedVariable: { secret: boolean } | undefined;
  savingEditor: boolean;
  vaultReady: boolean;
  conflict: boolean;
  save: { isError: boolean; error: Error | null };
  onSubmit: (e: FormEvent) => void;
  onRefresh: () => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const d = t.definitions;
  return <form className="mt-6 max-w-lg rounded-lg border border-border p-5" aria-label={editor.existing ? (editor.secret ? d.replaceSecret : d.editVariable) : d.addVariable} onSubmit={onSubmit}><h3 className="mb-5 text-sm font-semibold">{editor.existing ? (editor.secret ? d.replaceSecret : d.editVariable) : d.addVariable}</h3><FieldGroup className="gap-5">
    <Field><FieldLabel htmlFor="variable-name">{d.variableName}</FieldLabel><Input id="variable-name" required pattern="[A-Za-z_][A-Za-z0-9_]*" maxLength={128} readOnly={editor.existing} disabled={savingEditor} value={editor.name} onChange={(e) => setEditor({ ...editor, name: e.target.value })} /></Field>
    <Field><FieldLabel htmlFor="variable-value">{d.value}</FieldLabel><Input id="variable-value" type={editorSecret ? "password" : "text"} autoComplete="off" disabled={savingEditor} value={editor.value} onChange={(e) => setEditor({ ...editor, value: e.target.value })} /></Field>
    {!editor.existing && !matchedVariable && <Field><FieldLabel>{d.variableType}</FieldLabel><ToggleGroup disabled={savingEditor} aria-label={d.variableType} variant="outline" value={[editor.secret ? "secret" : "public"]} onValueChange={(v) => { if (v[0]) setEditor({ ...editor, secret: v[0] === "secret" }); }}><ToggleGroupItem value="secret">{d.secret}</ToggleGroupItem><ToggleGroupItem value="public">{d.normal}</ToggleGroupItem></ToggleGroup></Field>}
    {matchedVariable && <p className="worker-intro">{d.existingVariable(matchedVariable.secret)}</p>}
    {save.isError && <p role="alert" className="worker-error">{conflict ? d.saveConflict : errorMessage(save.error, t)}</p>}{conflict && <Button type="button" variant="outline" onClick={onRefresh}>{d.refreshConfig}</Button>}
    <div className="flex flex-wrap gap-1.5"><Button type="submit" disabled={!vaultReady || savingEditor || conflict}>{matchedVariable ? d.replaceVariable : d.saveVariable}</Button><Button type="button" variant="ghost" onClick={onCancel}>{d.cancel}</Button></div>
  </FieldGroup></form>;
}
