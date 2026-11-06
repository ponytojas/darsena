import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Failure, Loading } from "@/components/shared";
import { api, ApiError, type HistoryEntry } from "@/api/client";
import { useI18n } from "@/i18n/context";
import { errorMessage } from "@/i18n/errors";
import { formatDateTime, formatRelative } from "@/lib/format";

type Target = { name: string; secret: boolean; from: number };

const stale = (error: unknown) => error instanceof ApiError && (error.status === 409 || error.code === "revision_not_retained");

export function HistoryPanel({ id, revision, vaultReady, experimental }: { id: string; revision: number; vaultReady: boolean; experimental: boolean }) {
  const { t, locale } = useI18n();
  const h = t.definitions.history;
  const cache = useQueryClient();
  const history = useQuery({ queryKey: ["worker-config-history", id], queryFn: () => api.workerConfigHistory(id), enabled: vaultReady });
  const [target, setTarget] = useState<Target | null>(null);
  const restore = useMutation({
    mutationFn: (t: Target) => api.workerRestoreVariable(id, t.name, { revision, from_revision: t.from }),
    onSuccess: (_data, t2) => {
      setTarget(null);
      toast.success(h.restored(t2.name));
      void cache.invalidateQueries({ queryKey: ["worker-config", id] });
      void cache.invalidateQueries({ queryKey: ["worker-config-history", id] });
      void cache.invalidateQueries({ queryKey: ["worker-apps"] });
    },
  });
  const outdated = stale(restore.error);
  const close = () => { setTarget(null); restore.reset(); };
  const refresh = async () => {
    await Promise.all([cache.refetchQueries({ queryKey: ["worker-config", id] }), cache.refetchQueries({ queryKey: ["worker-config-history", id] })]);
    restore.reset();
  };
  const entries: HistoryEntry[] = history.data?.entries ?? [];
  let body;
  if (!vaultReady && !history.data) body = <p className="worker-intro py-5">{t.definitions.vaultNotAvailable}</p>;
  else if (history.isPending) body = <Loading />;
  else if (history.isError && !history.data) body = <Failure error={history.error} retry={() => void history.refetch()} />;
  else if (entries.length === 0) body = <p className="worker-intro py-5">{h.empty}</p>;
  else body = <ol className="mt-4 border-t border-border">{entries.map((entry, index) => {
    const older = entries[index + 1];
    return <li key={entry.revision} className="grid gap-2 border-b border-border py-3.5">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h3 className="text-sm font-semibold">{h.revisionN(entry.revision)}</h3>
        {entry.created_at ? <time className="text-xs text-muted-foreground" dateTime={entry.created_at} title={formatDateTime(entry.created_at, locale)}>{formatRelative(entry.created_at, locale)}</time> : <span className="text-xs text-muted-foreground">{h.unknownDate}</span>}
        {entry.base && <span className="text-xs font-medium text-muted-foreground">{h.baseLabel}</span>}
      </div>
      <ul className="grid gap-1.5">{entry.changes.map((change) => {
        const canRestore = !entry.base && change.kind !== "added" && !!older;
        return <li key={change.name} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-sm">
          <span className="inline-flex min-w-0 items-center gap-1.5 break-all">
            {change.secret && <><Lock aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" /><span className="sr-only">{h.secretMarker}: </span></>}
            <span className={change.secret ? undefined : "pl-5"}><strong className="font-semibold">{change.name}</strong>{" "}{h.kind[change.kind]}</span>
          </span>
          {canRestore && <Button size="sm" variant="ghost" disabled={!vaultReady} aria-label={h.restoreOf(change.name)} onClick={() => { restore.reset(); setTarget({ name: change.name, secret: change.secret, from: older.revision }); }}>{h.restore}</Button>}
        </li>;
      })}</ul>
    </li>;
  })}</ol>;
  return <section aria-labelledby="history-title" className="mt-8">
    <h2 id="history-title" className="text-sm font-semibold">{h.title}</h2>
    <p className="worker-intro pt-1">{h.intro}</p>
    {!vaultReady && history.data && <p className="worker-intro pt-2">{h.vaultBlocked}</p>}
    {body}
    <Dialog open={!!target} onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent>
        {target && <>
          <DialogHeader><DialogTitle>{h.restoreTitle(target.name)}</DialogTitle><DialogDescription>{h.restoreIntro(target.name, target.from)}</DialogDescription></DialogHeader>
          <ul className="grid list-disc gap-1 pl-5 text-sm">
            <li>{h.restoreNew}</li>
            <li>{h.restoreHidden}</li>
            <li>{h.restoreSecrets}</li>
            {experimental && <li>{h.restoreExperimental}</li>}
          </ul>
          {!vaultReady && <p role="alert" className="worker-error">{h.vaultBlocked}</p>}
          {restore.isError && <p role="alert" className="worker-error">{errorMessage(restore.error, t)}{outdated && <> {h.stale}</>}</p>}
          <div className="flex flex-wrap gap-2">
            {outdated ? <Button onClick={() => void refresh()}>{h.refresh}</Button> : <Button disabled={!vaultReady || restore.isPending} onClick={() => restore.mutate(target)}>{restore.isPending ? h.restoring : h.restoreConfirm}</Button>}
            <Button variant="outline" onClick={close}>{t.definitions.cancel}</Button>
          </div>
        </>}
      </DialogContent>
    </Dialog>
  </section>;
}
