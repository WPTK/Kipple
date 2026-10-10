import { useId, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, GripVertical, Pencil, Trash2 } from "lucide-react";
import { useBootstrap, useFolderTree } from "@/api/queries";
import { deleteSavedSearch, invalidateSavedSearches, patchSavedSearch, savedSearchesKey, useReorderSavedSearches, useSavedSearches } from "@/api/savedSearches";
import type { Bootstrap, SavedSearch } from "@/api/types";
import { errorMessage } from "@/api/client";
import { DND_ROW_CLASS, arrayMove, insertBefore, useRowDnd, type DragSource } from "@/lib/dnd";
import { SEARCH_ORDER_LABELS } from "@/lib/searchPrefs";
import { cn } from "@/lib/cn";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { FolderOptions } from "@/ui/FolderSelect";
import { folderPath, folderTree } from "@/lib/folderTree";
import { scrollParent } from "@/ui/segmented";
import { savedSearchError } from "./search/SaveSearchDialog";

/** Where a saved search looks, in words. */
export function scopeText(scope: SavedSearch["scope"], boot: Bootstrap | undefined): string {
  if (scope?.feed_id) return `the feed ${boot?.feeds.find((f) => f.id === scope.feed_id)?.title ?? "(removed)"}`;
  if (scope?.folder_id) {
    const tree = folderTree(boot?.folders ?? []);
    return `the folder ${tree.byId.has(scope.folder_id) ? folderPath(tree, scope.folder_id) : "(removed)"}`;
  }
  if (scope?.view === "unread") return "unread articles";
  if (scope?.view === "starred") return "starred articles";
  return "the whole library";
}

const orderText = (o: SavedSearch["order"]): string => (o === "date" ? SEARCH_ORDER_LABELS.date : o === "oldest" ? SEARCH_ORDER_LABELS.oldest : SEARCH_ORDER_LABELS.rank);

/** A scope as a select value, and back. */
const scopeValue = (s: SavedSearch["scope"]): string => (s?.feed_id ? `feed:${s.feed_id}` : s?.folder_id ? `folder:${s.folder_id}` : s?.view === "unread" || s?.view === "starred" ? `view:${s.view}` : "all");
function scopeFromValue(v: string): SavedSearch["scope"] | null {
  const [k, id] = [v.slice(0, v.indexOf(":")), v.slice(v.indexOf(":") + 1)];
  if (v === "all") return null;
  if (k === "feed") return { feed_id: id };
  if (k === "folder") return { folder_id: id };
  return { view: id as "unread" | "starred" };
}

function EditDialog({ search, onClose }: { search: SavedSearch; onClose: () => void }) {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const [name, setName] = useState(search.name);
  const [q, setQ] = useState(search.q);
  const [scope, setScope] = useState(scopeValue(search.scope));
  const [order, setOrder] = useState<string>(search.order ?? "rank");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const feeds = visibleFeeds(boot.data?.feeds);
  const tree = useFolderTree();

  const save = async () => {
    const body: Record<string, unknown> = {};
    if (name.trim() !== search.name) body.name = name.trim();
    if (q.trim() !== search.q) body.q = q.trim();
    if (scope !== scopeValue(search.scope)) body.scope = scopeFromValue(scope);
    if (order !== (search.order ?? "rank")) body.order = order;
    if (Object.keys(body).length === 0) return onClose();
    setBusy(true);
    setError(null);
    try {
      await patchSavedSearch(search.id, body);
      invalidateSavedSearches(qc);
      announce(`Saved search ${name.trim() || search.name} updated`);
      onClose();
    } catch (e) {
      setError(savedSearchError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title="Edit saved search"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="solid" onClick={() => void save()} disabled={busy || !name.trim() || !q.trim()}>
            {busy ? "Saving" : "Save"}
          </Button>
        </>
      }
    >
      <Field label="Name" error={name.trim() ? undefined : "Enter a name to save."}>{(a) => <input {...a} value={name} maxLength={60} onChange={(e) => setName(e.target.value)} className={inputCls} />}</Field>
      <Field label="Search for" error={q.trim() ? undefined : "Enter something to search for."} help={'Same syntax as the search box: "exact phrase", -exclude, title:word, author:name, word*.'}>
        {(a) => <input {...a} value={q} autoCapitalize="none" spellCheck={false} onChange={(e) => setQ(e.target.value)} className={inputCls} />}
      </Field>
      <Field label="Where">
        {(a) => (
          <select {...a} value={scope} onChange={(e) => setScope(e.target.value)} className={inputCls}>
            <option value="all">The whole library</option>
            <option value="view:unread">Unread articles</option>
            <option value="view:starred">Starred articles</option>
            {tree.preorder.length ? (
              <optgroup label="A folder">
                <FolderOptions tree={tree} optionValue={(id) => `folder:${id}`} />
              </optgroup>
            ) : null}
            {feeds.length ? (
              <optgroup label="A feed">
                {feeds.map((f) => (
                  <option key={f.id} value={`feed:${f.id}`}>
                    {f.title}
                  </option>
                ))}
              </optgroup>
            ) : null}
          </select>
        )}
      </Field>
      <Field label="Sort by">
        {(a) => (
          <select {...a} value={order} onChange={(e) => setOrder(e.target.value)} className={inputCls}>
            <option value="rank">{SEARCH_ORDER_LABELS.rank}</option>
            <option value="date">{SEARCH_ORDER_LABELS.date}</option>
            <option value="oldest">{SEARCH_ORDER_LABELS.oldest}</option>
          </select>
        )}
      </Field>
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}

/** Settings > Saved searches: rename, edit the query, scope and order, delete, and reorder (drag, or arrow keys on the grip). */
export function SavedSearchesSection() {
  const qc = useQueryClient();
  const boot = useBootstrap();
  const { searches, loading, error, refetch } = useSavedSearches();
  const reorder = useReorderSavedSearches();
  const currentIds = () => (qc.getQueryData<SavedSearch[]>(savedSearchesKey) ?? []).map((x) => x.id);
  const [editing, setEditing] = useState<SavedSearch | null>(null);
  const [deleting, setDeleting] = useState<SavedSearch | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const box = useRef<HTMLUListElement>(null);
  const listId = useId();

  const ids = searches.map((s) => s.id);
  const commit = (next: string[]) => {
    reorder(next, (e) => announce(`Couldn't reorder: ${errorMessage(e)}`));
  };
  // Moves read the order from the cache, not from this render: a second key press before React re-rendered
  // must build on the first move, not on the list as it was.
  const step = (id: string, delta: -1 | 1) => {
    const ids = currentIds();
    const i = ids.indexOf(id);
    if (i < 0 || i + delta < 0 || i + delta >= ids.length) return;
    commit(arrayMove(ids, i, i + delta));
    announce(`Moved ${searches.find((x) => x.id === id)?.name ?? "saved search"} to position ${i + delta + 1} of ${ids.length}`);
  };
  const dnd = useRowDnd({
    enabled: true,
    scroller: () => scrollParent(box.current),
    onDrop: (from, to) => commit(insertBefore(currentIds(), from.id, to.before)),
    onKeyMove: (src, delta) => step(src.id, delta),
  });
  const dragging = dnd.state.source;

  const remove = async () => {
    if (!deleting) return;
    setBusy(true);
    setFailure(null);
    try {
      await deleteSavedSearch(deleting.id);
      invalidateSavedSearches(qc);
      announce(`Saved search ${deleting.name} deleted`);
      setDeleting(null);
    } catch (e) {
      setFailure(savedSearchError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <p className="text-sm text-fg2">
        Searches you saved from the Search screen. They appear under Favorites in the navigation menu with their unread count. Drag the grip, or focus it and use the arrow keys, to change the order.
      </p>
      {loading ? <Skeleton rows={2} label="Loading saved searches" /> : null}
      {error ? (
        <Notice tone="error">
          Couldn't load your saved searches.{" "}
          <Button variant="link" onClick={refetch}>
            Try again
          </Button>
        </Notice>
      ) : null}
      {!loading && !error && searches.length === 0 ? <p className="text-sm text-fg2">None yet. Search for something, then choose Save this search.</p> : null}
      {searches.length ? (
        <ul id={listId} ref={box} aria-label="Saved searches" className="flex flex-col gap-2">
          {searches.map((s, i) => {
            const src: DragSource = { kind: "saved", id: s.id, group: "saved" };
            const dragged = dragging?.id === s.id;
            const before = dnd.state.target?.before === s.id && !dragged;
            const atEnd = dnd.state.target?.before === null && !!dragging && ids.filter((x) => x !== dragging.id).at(-1) === s.id;
            return (
              <li
                key={s.id}
                {...dnd.rowProps(src)}
                style={dragged ? { transform: `translateY(${dnd.state.dy}px)` } : undefined}
                className={cn(
                  "flex flex-col gap-1 rounded-xl border border-line bg-surface p-3",
                  DND_ROW_CLASS,
                  dragged && "relative z-10 opacity-90 shadow-lg",
                  before && "border-t-2 border-t-accent",
                  atEnd && "border-b-2 border-b-accent",
                )}
              >
                <div className="flex items-start gap-2">
                  <button
                    type="button"
                    aria-label={`Reorder ${s.name}. Drag, or use the up and down arrow keys.`}
                    {...dnd.gripProps(src)}
                    className="hit-row inline-flex shrink-0 cursor-grab touch-none items-center justify-center rounded-lg text-fg2 hover:bg-selection active:cursor-grabbing"
                  >
                    <GripVertical className="size-4" aria-hidden="true" />
                  </button>
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-semibold">{s.name}</p>
                    <p className="text-sm break-words text-fg2">
                      <code className="font-mono">{s.q}</code>
                    </p>
                    <p className="text-xs text-fg2">
                      In {scopeText(s.scope, boot.data)}, sorted by {orderText(s.order).toLowerCase()}
                    </p>
                  </div>
                </div>
                <div className="flex flex-wrap gap-1">
                  <Button variant="ghost" onClick={() => setEditing(s)} aria-label={`Edit ${s.name}`}>
                    <Pencil aria-hidden="true" />
                    Edit
                  </Button>
                  <Button variant="ghost" onClick={() => setDeleting(s)} aria-label={`Delete ${s.name}`}>
                    <Trash2 aria-hidden="true" />
                    Delete
                  </Button>
                  <Button variant="ghost" size="icon" aria-label={`Move ${s.name} up`} disabled={i === 0} onClick={() => step(s.id, -1)}>
                    <ArrowUp aria-hidden="true" />
                  </Button>
                  <Button variant="ghost" size="icon" aria-label={`Move ${s.name} down`} disabled={i === searches.length - 1} onClick={() => step(s.id, 1)}>
                    <ArrowDown aria-hidden="true" />
                  </Button>
                </div>
              </li>
            );
          })}
        </ul>
      ) : null}
      {editing ? <EditDialog key={editing.id} search={editing} onClose={() => setEditing(null)} /> : null}
      {deleting ? (
        <Modal
          open
          onOpenChange={(o) => {
            if (!o && !busy) {
              setDeleting(null);
              setFailure(null);
            }
          }}
          title="Delete this saved search?"
          description={`"${deleting.name}" is removed from Favorites. Your articles are not touched.`}
          footer={
            <>
              <Button variant="ghost" onClick={() => setDeleting(null)} disabled={busy}>
                Cancel
              </Button>
              <Button variant="solid" onClick={() => void remove()} disabled={busy}>
                {busy ? "Deleting" : "Delete"}
              </Button>
            </>
          }
        >
          {failure ? <Notice tone="error">{failure}</Notice> : null}
        </Modal>
      ) : null}
    </>
  );
}
