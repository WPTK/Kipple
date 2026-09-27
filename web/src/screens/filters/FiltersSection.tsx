import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Pencil, Trash2 } from "lucide-react";
import { errorMessage } from "@/api/client";
import { actionLabel, deleteFilter, fieldLabel, filtersKey, invalidateFilterData, setFilterEnabled, useFilters, type Filter, type Unmute } from "@/api/filters";
import { useBootstrap } from "@/api/queries";
import { whenLabel } from "@/lib/format";
import { openFilterEditor } from "@/lib/similar";
import { emptyDraft } from "@/api/filters";
import { announce, toast } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice, Skeleton, Switch } from "@/ui/kit";

/** "Everywhere", "Folder: News" or "Feed: Example". */
export function scopeText(f: Pick<Filter, "scope" | "folder_id" | "feed_id">, folders: { id: string; name: string }[], feeds: { id: string; title: string }[]): string {
  if (f.scope === "folder") return `Folder: ${folders.find((x) => x.id === f.folder_id)?.name ?? "deleted folder"}`;
  if (f.scope === "feed") return `Feed: ${feeds.find((x) => x.id === f.feed_id)?.title ?? "deleted feed"}`;
  return "Everywhere";
}

/** Most DELETE calls one deletion makes: each restores at least 500 articles, so this covers any library. */
const MAX_DELETE_ROUNDS = 1000;
/** Rounds in a row that restore nothing and are not done before the loop gives up (the rule is off after the first). */
const MAX_IDLE_DELETE_ROUNDS = 3;

/**
 * Delete a filter, repeating the call while the server answers `done: false`: a large restore is done in
 * rounds of about 40 seconds each (the rule is already off after the first), and each round resumes.
 */
export async function deleteUntilDone(id: string, mode: Unmute): Promise<{ restored: number; madeUnread: number }> {
  let restored = 0;
  let madeUnread = 0;
  // A round can end with nothing restored and still not done (the final row delete, or a first batch, ran out of
  // budget on a busy writer): repeat it, but stop after a few rounds in a row that made no progress.
  let idle = 0;
  for (let i = 0; i < MAX_DELETE_ROUNDS; i++) {
    const res = await deleteFilter(id, mode);
    restored += res.changed;
    madeUnread += res.made_unread;
    if (res.done !== false) break;
    idle = res.changed === 0 ? idle + 1 : 0;
    if (idle >= MAX_IDLE_DELETE_ROUNDS) break;
  }
  return { restored, madeUnread };
}

/** The toast after a deletion: how many articles came back, and how many of them are unread again. */
export function deletedMessage(restored: number, madeUnread: number, mode: Unmute): string {
  if (restored === 0) return "Filter deleted";
  const n = (k: number) => `${k.toLocaleString()} article${k === 1 ? "" : "s"}`;
  let msg = `Filter deleted. ${n(restored)} restored`;
  if (mode === "unread") {
    if (madeUnread === restored) msg += restored === 1 ? " as unread" : ", all marked unread";
    else if (madeUnread === 0) msg += "; none were unread when muted, so they stay read";
    else msg += `, ${madeUnread.toLocaleString()} of them marked unread (the rest you had already read)`;
  }
  return `${msg}.`;
}

export function DeleteFilterDialog({ filter, onClose }: { filter: Filter; onClose: () => void }) {
  const qc = useQueryClient();
  const [mode, setMode] = useState<Unmute>("read");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The count on the row can be stale (articles muted since the list loaded): look again when the dialog opens.
  useEffect(() => {
    void qc.invalidateQueries({ queryKey: filtersKey });
  }, [qc]);
  const live = useFilters().data?.find((x) => x.id === filter.id);
  const n = live?.muted_items ?? filter.muted_items;
  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      const { restored, madeUnread } = await deleteUntilDone(filter.id, mode);
      invalidateFilterData(qc);
      onClose();
      toast(deletedMessage(restored, madeUnread, mode));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const opts: { value: Unmute; label: string; help: string }[] = [
    { value: "read", label: "Restore them and keep them read", help: "They come back to All and search, and stay marked read. This is the safe choice." },
    {
      value: "unread",
      label: "Restore them and mark them unread",
      help: "They come back to All. The ones that were unread when the filter muted them become unread again; the ones you had already read stay read.",
    },
    { value: "keep", label: "Leave them muted", help: "They stay in Muted, labelled as muted by a deleted filter." },
  ];
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title="Delete this filter?"
      description={`“${filter.name}” stops running. Articles it already changed are not touched unless you choose so below.`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="solid" onClick={() => void remove()} disabled={busy}>
            {busy ? "Deleting" : "Delete filter"}
          </Button>
        </>
      }
    >
      {n > 0 ? (
        <fieldset className="flex flex-col gap-2">
          <legend className="text-sm font-semibold">
            This filter has muted {n.toLocaleString()} article{n === 1 ? "" : "s"}. What should happen to {n === 1 ? "it" : "them"}?
          </legend>
          {opts.map((o) => (
            <label key={o.value} className="flex min-h-11 cursor-pointer items-start gap-3 rounded-xl border border-line bg-surface p-3 text-sm has-[:checked]:border-accent has-[:checked]:bg-selection">
              <input type="radio" name="unmute" checked={mode === o.value} onChange={() => setMode(o.value)} className="mt-0.5 size-5 shrink-0 accent-[var(--kp-accent)]" />
              <span>
                <span className="font-semibold">{o.label}</span>
                <span className="block text-xs text-fg2">{o.help}</span>
              </span>
            </label>
          ))}
        </fieldset>
      ) : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

function FilterRow({ f, folders, feeds, onDelete }: { f: Filter; folders: { id: string; name: string }[]; feeds: { id: string; title: string }[]; onDelete: (f: Filter) => void }) {
  const qc = useQueryClient();
  const toggle = async (enabled: boolean) => {
    qc.setQueryData<Filter[]>(filtersKey, (old) => old?.map((x) => (x.id === f.id ? { ...x, enabled } : x)));
    try {
      await setFilterEnabled(f.id, enabled);
      invalidateFilterData(qc);
      announce(`${f.name} turned ${enabled ? "on" : "off"}`);
    } catch (e) {
      qc.setQueryData<Filter[]>(filtersKey, (old) => old?.map((x) => (x.id === f.id ? { ...x, enabled: !enabled } : x)));
      toast(errorMessage(e), "error");
    }
  };
  const terms = f.terms.slice(0, 4).join(", ") + (f.terms.length > 4 ? ` and ${f.terms.length - 4} more` : "");
  return (
    <li data-filter-id={f.id} className="flex flex-col gap-2 rounded-xl border border-line bg-surface p-3">
      <Switch label={f.name || "Untitled filter"} checked={f.enabled} onChange={(v) => void toggle(v)} />
      <div className="min-w-0">
        <p className="text-xs text-fg2">
          {actionLabel(f.action)}
          {f.invert ? " when it does not match" : ""} · {scopeText(f, folders, feeds)} · {f.kind === "regex" ? "Regular expression" : "Words"} in {f.fields.map(fieldLabel).join(", ").toLowerCase()}
        </p>
        <p className="truncate text-xs text-fg2">{terms}</p>
        {f.disabled_reason ? (
          <p role="note" className="text-xs text-danger">
            Turned off: {f.disabled_reason}
          </p>
        ) : null}
        <p className="text-xs text-fg2">
          {f.hits === 0 ? "Hasn't matched anything yet" : `Matched ${f.hits.toLocaleString()} article${f.hits === 1 ? "" : "s"}, last ${whenLabel(f.last_hit_at).toLowerCase()}`}
          {f.action === "mute" && f.muted_items > 0 ? ` · ${f.muted_items.toLocaleString()} muted now` : ""}
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => openFilterEditor({ mode: "edit", id: f.id })} aria-label={`Edit ${f.name || "filter"}`}>
          <Pencil aria-hidden="true" />
          Edit
        </Button>
        <Button variant="ghost" onClick={() => onDelete(f)} aria-label={`Delete ${f.name || "filter"}`}>
          <Trash2 aria-hidden="true" />
          Delete
        </Button>
      </div>
    </li>
  );
}

/** Settings > Filters: every rule with its scope, action, hits and last hit, an on/off switch, edit and delete. */
export function FiltersSection() {
  const filters = useFilters();
  const boot = useBootstrap();
  const [deleting, setDeleting] = useState<Filter | null>(null);
  const folders = boot.data?.folders ?? [];
  const feeds = boot.data?.feeds ?? [];
  const list = filters.data ?? [];
  return (
    <>
      <p className="text-sm text-fg2">Filters run on every new article. They can hide noise, mark things read, star what you care about, or highlight words. Muted articles are kept in Muted, and you can restore them.</p>
      {filters.isPending ? <Skeleton rows={2} label="Loading filters" /> : null}
      {filters.isError ? (
        <Notice tone="error">
          Couldn't load your filters. {errorMessage(filters.error)}{" "}
          <Button variant="link" onClick={() => void filters.refetch()}>
            Try again
          </Button>
        </Notice>
      ) : null}
      {filters.data && list.length === 0 ? <p className="text-sm">No filters yet. Add one to mute a topic or a phrase you never want to see.</p> : null}
      {list.length ? (
        <ul aria-label="Your filters" className="flex flex-col gap-2">
          {list.map((f) => (
            <FilterRow key={f.id} f={f} folders={folders} feeds={feeds} onDelete={setDeleting} />
          ))}
        </ul>
      ) : null}
      <div>
        <Button variant="solid" onClick={() => openFilterEditor({ mode: "create", seed: { draft: emptyDraft() } })}>
          New filter
        </Button>
      </div>
      {deleting ? <DeleteFilterDialog filter={deleting} onClose={() => setDeleting(null)} /> : null}
    </>
  );
}
