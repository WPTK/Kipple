import { tapVerb } from "@/lib/pointer";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { useFocusActions } from "./focusActions";
import { Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { lazyScreen } from "@/lib/lazyScreen";
import { Link, useLocation, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Check, CheckCheck, CheckSquare, Download, FolderInput, FolderPlus, GripVertical, ListFilter, MoreVertical, Pencil, Plus, Settings2, Trash2, Upload } from "lucide-react";
import { createFolder, invalidateFeeds, patchFolder, reorder as reorderApi } from "@/api/admin";
import { ApiError, errorMessage } from "@/api/client";
import { keys, useBootstrap } from "@/api/queries";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import { useDevicePrefs } from "@/lib/devicePrefs";
import { ListOverrideFields, useListOverrideDraft } from "./feeds/ListOverrideFields";
import type { Favorite } from "@/lib/devicePrefs";
import {
  arrayMove,
  insertBefore,
  planFeedDrop,
  planFolderDrop,
  reorderBody,
  stepFolder,
  useRowDnd,
  DND_ROW_CLASS,
  type DragKind,
  type DragSource,
  type DropTarget,
  type Tree,
} from "@/lib/dnd";
import { MAX_FOLDER_DEPTH, childrenOf, depthOf, feedOrder, folderPath, folderTree, parentChoices, parentOf, rollUp, subtreeFeeds, subtreeOf, walkRows, type FolderTree, type RowRules } from "@/lib/folderTree";
import { FolderSelect } from "@/ui/FolderSelect";
import { useFavorites } from "@/lib/favorites";
import { clickRow, groupState, toggleGroup } from "@/lib/selection";
import { cn } from "@/lib/cn";
import { openListTo } from "@/lib/routes";
import { DESTINATIONS } from "@/lib/destinations";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { feedLabel } from "./feeds/FeedRowName";
import { FeedLine, FolderLine } from "./feeds/RowLine";
import { useItemActions } from "@/lib/itemActions";
import { RevealMore, useReveal } from "@/lib/reveal";
import { FavStar } from "@/ui/FavStar";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";
import { FirstRun } from "./FirstRun";
import { SavedSearchesNav } from "./SavedSearchesNav";
import { CollapseToggle } from "./FeedTree";
import { DeleteDialog, DeleteFolderDialog, MoveDialog } from "./feeds/BulkActions";
import { BottomBar } from "@/ui/BottomBar";
import { DropdownMenu } from "@/ui/menu";

// The dialogs load when first opened, not with the Feeds screen.
const AddFeedDialog = lazyScreen(() => import("./feeds/AddFeedDialog").then((m) => ({ default: m.AddFeedDialog })));
const FeedEditor = lazyScreen(() => import("./feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));
const OpmlImportDialog = lazyScreen(() => import("./feeds/OpmlDialog").then((m) => ({ default: m.OpmlImportDialog })));

const row = "flex min-h-11 items-center gap-2 rounded-lg px-3 hover:bg-selection";
const menuItem = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";
const menuLabel = "px-3 pt-2 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase";

type FolderDialog =
  | { kind: "new"; parent: string | null }
  | { kind: "rename"; folder: Folder }
  | { kind: "move"; folder: Folder }
  | { kind: "delete"; folder: Folder };

/** The message for a folder change the server refused. */
export function folderError(e: unknown): string {
  const code = e instanceof ApiError ? e.code : undefined;
  if (code === "folder_exists") return "A folder with that name is already there.";
  if (code === "folder_too_deep") return `Folders nest at most ${MAX_FOLDER_DEPTH} levels deep.`;
  if (code === "folder_cycle") return "A folder can't move inside itself or one of its subfolders.";
  if (code === "default_folder") return "The default folder stays at the top level and holds no subfolders.";
  return errorMessage(e);
}

/** What deleting a folder takes with it: its subfolders (deleted) and the feeds of the whole subtree (moved). */
export function deleteFolderText(tree: FolderTree, folder: string, feeds: readonly Pick<Feed, "folder_id">[]): string {
  const inside = subtreeOf(tree, folder);
  const subfolders = inside.size - 1;
  const n = feeds.filter((f) => inside.has(f.folder_id)).length;
  const fallback = tree.preorder.map((id) => tree.byId.get(id)).find((f) => f?.is_default)?.name ?? "the default folder";
  const subs = subfolders > 0 ? `${subfolders === 1 ? "Its subfolder is" : `Its ${subfolders} subfolders are`} deleted too. ` : "";
  const moved = n === 0 ? "No feeds are in it." : `${n === 1 ? "The feed in it is" : `The ${n} feeds in it are`} not deleted: ${n === 1 ? "it moves" : "they move"} to ${fallback}.`;
  return subs + moved;
}

function FolderDialogs({
  dialog,
  tree,
  feeds,
  onMove,
  onClose,
}: {
  dialog: FolderDialog;
  tree: FolderTree;
  feeds: readonly Feed[];
  /** Move a folder to the end of `parent` (null: the top level); rejects with the server's refusal. */
  onMove: (folder: string, parent: string | null) => Promise<void>;
  /** Closes the dialog; after a move or a delete, `focusFolder` is the folder whose row takes the focus. */
  onClose: (focusFolder?: string) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState(dialog.kind === "rename" ? dialog.folder.name : "");
  const [parent, setParent] = useState(dialog.kind === "new" ? (dialog.parent ?? "") : dialog.kind === "move" ? (parentOf(tree, dialog.folder.id) ?? "") : "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Layout, order and opening view are written with the rename (Save), not as they are picked.
  const lists = useListOverrideDraft("folder", dialog.kind === "rename" ? dialog.folder.id : "");
  const run = async (fn: () => Promise<unknown>, done: string, focusFolder?: string) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      lists.apply();
      invalidateFeeds(qc);
      toast(done);
      onClose(focusFolder);
    } catch (e) {
      setError(folderError(e));
    } finally {
      setBusy(false);
    }
  };
  const pathOf = (id: string) => folderPath(tree, id);

  if (dialog.kind === "delete") {
    const f = dialog.folder;
    // The focus goes to the next folder row that stays (after the subtree), else the one above.
    const inside = subtreeOf(tree, f.id);
    const at = tree.preorder.indexOf(f.id);
    const focusNext = tree.preorder.slice(at).find((id) => !inside.has(id)) ?? tree.preorder.slice(0, Math.max(0, at)).at(-1);
    return (
      <DeleteFolderDialog
        folder={f}
        title={`Delete ${pathOf(f.id)}?`}
        keepText={deleteFolderText(tree, f.id, feeds)}
        feeds={feeds.filter((x) => inside.has(x.folder_id))}
        focusNext={focusNext}
        onClose={onClose}
      />
    );
  }
  if (dialog.kind === "move") {
    const f = dialog.folder;
    const from = parentOf(tree, f.id) ?? "";
    return (
      <Modal
        open
        onOpenChange={(o) => !o && onClose()}
        title={`Move ${pathOf(f.id)}`}
        description="Its subfolders and feeds move with it."
        footer={
          <>
            <Button onClick={() => onClose()}>Cancel</Button>
            <Button
              variant="solid"
              disabled={busy}
              onClick={() => (parent === from ? onClose() : void run(() => onMove(f.id, parent || null), `Moved ${f.name} to ${parent ? pathOf(parent) : "the top level"}`, f.id))}
            >
              Move
            </Button>
          </>
        }
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
        <Field label="Move into">{(a) => <FolderSelect {...a} value={parent} onChange={setParent} only={parentChoices(tree, f.id)} none="Top level" />}</Field>
      </Modal>
    );
  }
  const isNew = dialog.kind === "new";
  const submit = () => {
    if (busy || !name.trim()) return;
    void run(
      () => (isNew ? createFolder(name.trim(), parent || null) : patchFolder(dialog.folder.id, { name: name.trim() })),
      isNew ? "Folder created" : "Folder settings saved",
    );
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={isNew ? (dialog.parent ? "New subfolder" : "New folder") : "Folder settings"}
      footer={
        <>
          <Button onClick={() => onClose()}>Cancel</Button>
          {/* The form's submit button, so Enter in the name field saves too. */}
          <Button variant="solid" type="submit" form="folder-form" disabled={busy || !name.trim()}>
            {isNew ? "Create" : "Save"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <form
        id="folder-form"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
        className="flex flex-col gap-4"
      >
        <Field label="Name">
          {(a) => <input {...a} type="text" maxLength={100} autoFocus value={name} onChange={(e) => setName(e.target.value)} className={inputCls} />}
        </Field>
        {isNew ? (
          <Field label="Inside">{(a) => <FolderSelect {...a} value={parent} onChange={setParent} only={parentChoices(tree, null)} none="Top level" />}</Field>
        ) : (
          <ListOverrideFields kind="folder" id={dialog.folder.id} draft={lists} />
        )}
      </form>
    </Modal>
  );
}

type Sel = ReadonlySet<string>;

const folderActionsId = (folder: string) => `folder-actions-${folder}`;
const FEED_ACTIONS_ID = "feed-actions";

/** Feeds: where you open a folder or feed, and where feeds and folders are added, edited, ordered, favorited and removed. */
export function FeedsScreen() {
  useDocumentTitle("Feeds");
  const focusActions = useFocusActions();
  const boot = useBootstrap();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const favs = useFavorites();
  const dp = useDevicePrefs();
  const open = (useLocation().state as { open?: string } | null)?.open;
  const [adding, setAdding] = useState(open === "add");
  const [importing, setImporting] = useState(open === "import");
  const [editing, setEditing] = useState<Feed | null>(null);
  const [folderDialog, setFolderDialog] = useState<FolderDialog | null>(null);
  // Off by default: the drag handle and each feed's edit (pencil) button only show once the user asks for
  // them, so a plain visit to this screen is just a list of feeds, not a wall of reorder/edit affordances.
  const [editMode, setEditMode] = useState(false);
  // Up and down buttons: the keyboard and screen reader alternative to dragging, off the row by default.
  const [buttons, setButtons] = useState(false);
  const [selecting, setSelecting] = useState(false);
  // Only the feeds with something unread. Editing and selecting always show every feed (a reorder is saved against the whole tree).
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [sel, setSel] = useState<Sel>(new Set());
  const [anchor, setAnchor] = useState<string | null>(null);
  const [bulk, setBulk] = useState<null | "move" | "delete">(null);
  const [saved, setSaved] = useState<"saving" | "saved" | null>(null);
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const scroller = useRef<HTMLDivElement>(null);
  const act = useItemActions();

  const feeds = useMemo(() => boot.data?.feeds ?? [], [boot.data?.feeds]);
  const folders = useMemo(() => boot.data?.folders ?? [], [boot.data?.folders]);
  const realFeeds = useMemo(() => visibleFeeds(feeds), [feeds]);
  const filtering = unreadOnly && !editMode && !selecting;
  const byFolder = useMemo(() => {
    const m: Record<string, Feed[]> = {};
    for (const fo of folders) m[fo.id] = [];
    for (const f of realFeeds) if (!filtering || f.unread > 0) (m[f.folder_id] ??= []).push(f);
    return m;
  }, [folders, realFeeds, filtering]);
  const ftree = useMemo(() => folderTree(folders), [folders]);
  const tree: Tree = useMemo(
    () => ({
      folders: [...ftree.preorder],
      parents: Object.fromEntries(ftree.preorder.map((id) => [id, parentOf(ftree, id)])),
      feeds: Object.fromEntries(Object.entries(byFolder).map(([k, v]) => [k, v.map((f) => f.id)])),
    }),
    [ftree, byFolder],
  );
  // The order feeds appear on screen: what shift-click ranges are measured along.
  const visibleOrder = useMemo(() => feedOrder(ftree, (id) => tree.feeds[id] ?? []), [ftree, tree]);
  // Each folder's feeds over its subtree (its Select checkbox), built once per tree, not per row and drag move.
  const subtreeIds = useMemo(() => subtreeFeeds(ftree, (id) => tree.feeds[id] ?? []), [ftree, tree]);
  // The empty default folder only shows while editing, where it takes dropped feeds (issue #469).
  // Unread only leaves out every folder with no unread feed in its subtree.
  const hasFeeds = (id: string) => (subtreeIds.get(id)?.length ?? 0) > 0;
  const topLevel = useMemo(
    () => childrenOf(ftree, null).filter((id) => (filtering ? hasFeeds(id) : editMode || !ftree.byId.get(id)?.is_default || (byFolder[id]?.length ?? 0) > 0)),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ftree, byFolder, editMode, filtering, subtreeIds],
  );
  // Every folder and feed row in screen order (collapsed folders hide theirs outside Edit and Select): a library of
  // thousands of feeds draws them a step at a time (issue #463).
  // Edit and Select need every feed on screen and reachable (a collapsed folder's chevron is replaced by the grip or
  // checkbox in those modes, and Select all / shift-click range over every feed), so the saved collapsed state only
  // applies outside them; it is not changed, and returns when the mode ends. The key list and the render share this rule.
  const rules: RowRules = useMemo(
    () => ({
      subfolders: (id) => (filtering ? childrenOf(ftree, id).filter(hasFeeds) : childrenOf(ftree, id)),
      feeds: (id) => byFolder[id] ?? [],
      open: (id) => selecting || editMode || !dp.collapsedFolders.includes(id),
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ftree, byFolder, selecting, editMode, filtering, subtreeIds, dp.collapsedFolders],
  );
  const rowKeys = useMemo(() => {
    const out: string[] = [];
    walkRows(topLevel, rules, (kind, id) => out.push(`${kind}:${id}`));
    return out;
  }, [topLevel, rules]);
  const reveal = useReveal(rowKeys);
  // Feeds that share a title show their address under it (issue #484).

  const markSaved = () => {
    setSaved("saved");
    announce("Order saved");
    if (savedTimer.current) clearTimeout(savedTimer.current);
    savedTimer.current = setTimeout(() => setSaved(null), 2500);
  };
  useEffect(() => () => clearTimeout(savedTimer.current), []);

  // Saves run one after another: each one sends the whole folder order, so two in flight could commit out of order.
  const saveQueue = useRef<Promise<unknown>>(Promise.resolve());
  const saving = useRef(0);
  // Counts failed saves. A queued save was planned on top of the tree painted before it; once a save planned earlier
  // has failed, that tree was never the server's, so the queued save is dropped instead of sent.
  const failures = useRef(0);
  /**
   * Save a new tree: paint it at once, then send the moves and the new order as one POST /api/reorder (one server
   * transaction), after any save still running. Rejects with the server's refusal, which changed nothing; a save
   * queued behind a failed one is dropped. The bootstrap is refetched once the last queued save ends, so the screen
   * ends up showing what the server has.
   */
  const saveTree = async (next: Tree) => {
    const body = reorderBody(tree, next);
    if (!body) return;
    const planned = failures.current;
    setSaved("saving");
    // A bootstrap refetch in flight would paint the old tree over the new one.
    await qc.cancelQueries({ queryKey: keys.bootstrap });
    qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => {
      if (!old) return old;
      const byId = new Map(old.feeds.map((f) => [f.id, f]));
      const ordered: Feed[] = [];
      for (const fid of next.folders) for (const id of next.feeds[fid] ?? []) {
        const f = byId.get(id);
        if (f) ordered.push({ ...f, folder_id: fid });
      }
      const own = new Map<string, number>();
      for (const f of ordered) own.set(f.folder_id, (own.get(f.folder_id) ?? 0) + f.unread);
      const moved = next.folders.map((id, i) => ({ ...(old.folders.find((f) => f.id === id) as Folder), parent_id: next.parents[id] ?? null, position: i }));
      const unread = rollUp(folderTree(moved), own);
      return {
        ...old,
        folders: moved.map((f) => ({ ...f, unread: unread.get(f.id) ?? 0 })),
        feeds: [...ordered, ...old.feeds.filter((f) => f.is_archive)],
      };
    });
    saving.current++;
    const run = saveQueue.current.then(async () => {
      if (failures.current !== planned) return false;
      try {
        await reorderApi(body);
        return true;
      } catch (e) {
        failures.current++;
        throw e;
      }
    });
    saveQueue.current = run.catch(() => undefined);
    try {
      if (await run) markSaved();
      else setSaved(null);
    } catch (e) {
      setSaved(null);
      throw e;
    } finally {
      if (--saving.current === 0) invalidateFeeds(qc);
    }
  };
  /** Save a new tree from a drop or a move button: a refusal is a toast. */
  const applyTree = (next: Tree) => saveTree(next).catch((e: unknown) => toast(folderError(e), "error"));
  const moveFolder = (folder: string, parent: string | null) => saveTree(planFolderDrop(tree, folder, { parent, before: null }));

  const applyFavs = async (next: Favorite[]) => {
    setSaved("saving");
    if (await favs.set(next)) markSaved();
    else setSaved(null);
  };
  const favKey = (f: Favorite) => `${f.t}:${f.id}`;

  /** Mark every unread article in a folder and its subfolders read; the undo toast takes it back. */
  const markFolderRead = async (fo: Folder) => {
    await act.markAll({ view: "unread", folder: fo.id }, undefined, []);
    invalidateFeeds(qc);
  };

  // A drop after the last drawn row of a list that is not all drawn yet lands there, before its first row not drawn,
  // not after the whole list (the reveal says which rows are drawn).
  const endOf = (ids: readonly string[], kind: "feed" | "folder", moving: string) => reveal.firstPast(ids.filter((id) => id !== moving), (id) => `${kind}:${id}`);
  const drop = (from: DragSource, to: DropTarget) => {
    if (from.kind === "folder") {
      const before = to.before ?? endOf(childrenOf(ftree, to.group || null), "folder", from.id);
      void applyTree(planFolderDrop(tree, from.id, { parent: to.group || null, before }));
      reveal.ensure(`folder:${from.id}`);
    } else if (from.kind === "feed") {
      void applyTree(planFeedDrop(tree, from.id, { folder: to.group, before: to.before ?? endOf(tree.feeds[to.group] ?? [], "feed", from.id) }));
      reveal.ensure(`feed:${from.id}`);
    } else {
      const keys2 = favs.favorites.map(favKey);
      const order = insertBefore(keys2, from.id, to.before);
      void applyFavs(order.map((k) => favs.favorites.find((f) => favKey(f) === k) as Favorite));
    }
  };

  /**
   * Moving a row re-orders its keyed <li>, and the browser drops focus to the body when a focused node moves. Put it
   * back on the same row's control (the grip, or the Move button that was pressed; the grip when that one is now
   * disabled at the end of the list) after each commit for a short while, until the new order has settled.
   */
  const refocus = useRef<{ src: DragSource; want: string } | null>(null);
  const refocusTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const keepFocus = (src: DragSource, from: Element | null) => {
    const want = from instanceof HTMLElement && from.dataset.move ? from.dataset.move : "grip";
    refocus.current = { src, want };
    clearTimeout(refocusTimer.current);
    refocusTimer.current = setTimeout(() => (refocus.current = null), 400);
  };
  useLayoutEffect(() => {
    const r = refocus.current;
    if (!r) return;
    const row = document.querySelector<HTMLElement>(`[data-dnd-kind="${r.src.kind}"][data-dnd-id="${CSS.escape(r.src.id)}"]`);
    const scope = row?.closest("li") ?? row;
    const btn = r.want === "grip" ? null : scope?.querySelector<HTMLButtonElement>(`[data-move="${r.want}"]`);
    const target = btn && !btn.disabled ? btn : scope?.querySelector<HTMLElement>("[data-drag-handle]");
    if (target && document.activeElement !== target) target.focus();
  });
  useEffect(() => () => clearTimeout(refocusTimer.current), []);

  /** One place up or down inside the list the row lives in (arrow keys on the grip, and the buttons). */
  const step = (src: DragSource, delta: -1 | 1) => {
    const had = document.activeElement;
    if (had && had !== document.body) keepFocus(src, had);
    if (src.kind === "folder") {
      const siblings = childrenOf(ftree, parentOf(ftree, src.id));
      const i = siblings.indexOf(src.id);
      if (i + delta < 0 || i + delta >= siblings.length) return;
      void applyTree(stepFolder(tree, src.id, delta));
      reveal.ensure(`folder:${src.id}`);
      announce(`Moved ${ftree.byId.get(src.id)?.name ?? "folder"} to position ${i + delta + 1} of ${siblings.length}`);
    } else if (src.kind === "feed") {
      const list = tree.feeds[src.group] ?? [];
      const i = list.indexOf(src.id);
      if (i + delta < 0 || i + delta >= list.length) return;
      void applyTree({ ...tree, feeds: { ...tree.feeds, [src.group]: arrayMove(list, i, i + delta) } });
      reveal.ensure(`feed:${src.id}`);
      announce(`Moved ${feeds.find((f) => f.id === src.id)?.title ?? "feed"} to position ${i + delta + 1} of ${list.length}`);
    } else {
      const list = favs.favorites;
      const i = list.findIndex((f) => favKey(f) === src.id);
      if (i < 0 || i + delta < 0 || i + delta >= list.length) return;
      void applyFavs(arrayMove(list, i, i + delta));
      announce(`Moved favorite to position ${i + delta + 1} of ${list.length}`);
    }
  };

  const dnd = useRowDnd({ enabled: !selecting && editMode, onDrop: drop, scroller: () => scroller.current, onKeyMove: step });
  const dragging = dnd.state.source;
  const dropAt = dnd.state.target;
  // The row a drop at the end of its list marks: the last drawn row of that list, found once per drop target, not per row.
  const dropKind = dropAt?.kind;
  const dropGroup = dropAt?.group;
  const dropToEnd = dropAt?.before === null;
  const draggedId = dragging?.id;
  const endKey = useMemo(() => {
    if (!dropToEnd) return null;
    if (dropKind === "feed") {
      const last = reveal.lastDrawn((byFolder[dropGroup ?? ""] ?? []).filter((x) => x.id !== draggedId), (x) => `feed:${x.id}`);
      return last ? `feed:${last.id}` : null;
    }
    if (dropKind === "folder" && dropGroup === "") {
      const last = reveal.lastDrawn(topLevel.filter((x) => x !== draggedId), (x) => `folder:${x}`);
      return last ? `folder:${last}` : null;
    }
    return null;
  }, [reveal, byFolder, topLevel, dropKind, dropGroup, dropToEnd, draggedId]);

  // ---- selection ----
  const selected = useMemo(() => (sel.size === 0 ? [] : realFeeds.filter((f) => sel.has(f.id))), [realFeeds, sel]);
  const finePointer = typeof window.matchMedia === "function" && window.matchMedia("(any-pointer: fine)").matches;
  // Nothing left to select (every feed deleted): the action bar would only sit under "No feeds yet".
  if (selecting && boot.data && realFeeds.length === 0) {
    setSelecting(false);
    setSel(new Set());
    setAnchor(null);
  }
  const clearSel = () => {
    setSel(new Set());
    setAnchor(null);
  };
  const onCheck = (id: string, shift: boolean) => {
    const r = clickRow(visibleOrder, sel, anchor, id, shift);
    setSel(r.sel);
    setAnchor(r.anchor);
  };
  const exitSelect = () => {
    setSelecting(false);
    clearSel();
  };

  const rowStyle = (kind: DragKind, id: string): CSSProperties | undefined =>
    dragging && dragging.kind === kind && dragging.id === id ? { transform: `translateY(${dnd.state.dy}px)` } : undefined;
  const isDragged = (kind: DragKind, id: string) => !!dragging && dragging.kind === kind && dragging.id === id;
  const dragCls = (kind: DragKind, id: string) => (isDragged(kind, id) ? "relative z-10 rounded-lg bg-surface opacity-90 shadow-lg" : "");

  const grip = (src: DragSource, label: string) => (
    <button
      type="button"
      aria-label={`Reorder ${label}. Drag, or use the up and down arrow keys.`}
      {...dnd.gripProps(src)}
      className="hit-row inline-flex shrink-0 cursor-grab touch-none items-center justify-center rounded-lg text-fg2 hover:bg-selection active:cursor-grabbing"
    >
      <GripVertical className="size-5" aria-hidden="true" />
    </button>
  );

  const moves = (src: DragSource, i: number, n: number, label: string) =>
    buttons && editMode ? (
      <>
        <Button variant="ghost" size="icon" data-move="up" aria-label={`Move ${label} up`} disabled={i === 0} onClick={() => step(src, -1)}>
          <ArrowUp aria-hidden="true" />
        </Button>
        <Button variant="ghost" size="icon" data-move="down" aria-label={`Move ${label} down`} disabled={i === n - 1} onClick={() => step(src, 1)}>
          <ArrowDown aria-hidden="true" />
        </Button>
      </>
    ) : null;

  const feedRow = (f: Feed, i: number, list: Feed[], fo: Folder) => {
    const src: DragSource = { kind: "feed", id: f.id, group: fo.id };
    const before = dropAt?.kind === "feed" && dropAt.group === fo.id && dropAt.before === f.id && !isDragged("feed", f.id);
    const atEnd = endKey === `feed:${f.id}`;
    return (
      <li
        key={f.id}
        {...(selecting || !editMode ? {} : dnd.rowProps(src))}
        style={rowStyle("feed", f.id)}
        className={cn("flex items-center", DND_ROW_CLASS, dragCls("feed", f.id), before && "border-t-2 border-accent", atEnd && "border-b-2 border-accent")}
      >
        {selecting ? (
          // While selecting, the whole row is the checkbox's label: a tap anywhere on it ticks it and nothing opens.
          <label className={cn(row, "min-w-0 flex-1 cursor-pointer text-sm", sel.has(f.id) && "bg-selection")}>
            <input
              type="checkbox"
              aria-label={`Select ${feedLabel(f)}`}
              data-reveal-key={`feed:${f.id}`}
              checked={sel.has(f.id)}
              onChange={() => undefined}
              onClick={(e) => onCheck(f.id, e.shiftKey)}
              className="size-5 shrink-0 accent-[var(--kp-accent)]"
            />
            <FeedLine feed={f} status />
          </label>
        ) : (
          <>
            {editMode ? grip(src, f.title) : null}
            <Link to={openListTo({ feed: f.id })} draggable={false} data-reveal-key={`feed:${f.id}`} className={`${row} min-w-0 flex-1 text-sm`}>
              <FeedLine feed={f} status />
            </Link>
          </>
        )}
        {!selecting ? (
          <>
            {moves(src, i, list.length, f.title)}
            <FavStar on={favs.has("feed", f.id)} name={f.title} onToggle={() => favs.toggle("feed", f.id)} />
            {editMode ? (
              <Button variant="ghost" size="icon" aria-label={`Edit ${f.title}`} onClick={() => setEditing(f)}>
                <Pencil aria-hidden="true" />
              </Button>
            ) : null}
          </>
        ) : null}
      </li>
    );
  };

  const favRows = favs.favorites.map((fav, i) => {
    const key = favKey(fav);
    const src: DragSource = { kind: "fav", id: key, group: "fav" };
    const favFolder = fav.t === "folder" ? ftree.byId.get(fav.id) : undefined;
    const favFeed = fav.t === "feed" ? realFeeds.find((f) => f.id === fav.id) : undefined;
    // A favorite whose feed or folder is gone draws nothing (the sidebar does the same).
    if (!favFolder && !favFeed) return null;
    const name = favFolder ? folderPath(ftree, favFolder.id) : favFeed!.title;
    // A favorite folder lists the feeds of its whole subtree.
    const inFav = favFolder ? subtreeOf(ftree, favFolder.id) : null;
    const favFeeds = inFav ? realFeeds.filter((f) => inFav.has(f.folder_id)) : [];
    const favCollapsed = dp.collapsedFolders.includes(fav.id);
    const favListId = `manage-fav-folder-${fav.id}-feeds`;
    const before = dropAt?.kind === "fav" && dropAt.before === key && !isDragged("fav", key);
    const atEnd = dropAt?.kind === "fav" && dropAt.before === null && favs.favorites.filter((x) => favKey(x) !== dragging?.id).at(-1) === fav;
    return (
      <li
        key={key}
        {...(selecting || !editMode ? {} : dnd.rowProps(src))}
        style={rowStyle("fav", key)}
        className={cn(DND_ROW_CLASS, dragCls("fav", key), before && "border-t-2 border-accent", atEnd && "border-b-2 border-accent")}
      >
        <div className="flex items-center">
          {!selecting && editMode ? grip(src, `favorite ${name ?? ""}`) : null}
          {favFolder && favFeeds.length > 0 && !selecting && !editMode ? (
            <CollapseToggle folder={favFolder} collapsed={favCollapsed} listId={favListId} collapsedIds={dp.collapsedFolders} />
          ) : null}
          <Link
            to={openListTo(fav.t === "folder" ? { folder: fav.id } : { feed: fav.id })}
            draggable={false}
            className={`${row} min-w-0 flex-1 text-sm`}
          >
            {favFolder ? <FolderLine name={name} unread={favFolder.unread} icon /> : <FeedLine feed={favFeed!} status />}
          </Link>
          {!selecting ? (
            <>
              {moves(src, i, favs.favorites.length, `favorite ${name ?? ""}`)}
              <FavStar on name={name ?? "favorite"} onToggle={() => favs.toggle(fav.t, fav.id)} />
            </>
          ) : null}
        </div>
        {favFolder && favFeeds.length > 0 && !favCollapsed && !selecting && !editMode ? (
          <ul id={favListId} className="pl-6">
            {favFeeds.map((f) => (
              <li key={f.id}>
                <Link to={openListTo({ feed: f.id })} draggable={false} className={`${row} min-w-0 text-sm`}>
                  <FeedLine feed={f} />
                </Link>
              </li>
            ))}
          </ul>
        ) : null}
      </li>
    );
  });

  // While a folder is dragged: the folders it may go inside (not itself, not below itself, not too deep).
  const canHold = useMemo(() => (dragging?.kind === "folder" ? new Set(parentChoices(ftree, dragging.id)) : null), [dragging, ftree]);

  /** A folder row, then (unless collapsed) its subfolders and its own feeds. */
  const folderNode = (fo: Folder): React.ReactNode => {
    if (!reveal.shown(`folder:${fo.id}`)) return null;
    const depth = depthOf(ftree, fo.id);
    const parent = parentOf(ftree, fo.id);
    const siblings = childrenOf(ftree, parent);
    const subfolders = rules.subfolders(fo.id);
    const inFolder = rules.feeds(fo.id) as Feed[];
    const inSubtree = subtreeIds.get(fo.id) ?? [];
    const fsrc: DragSource = { kind: "folder", id: fo.id, group: parent ?? "" };
    const folderBefore = dropAt?.kind === "folder" && dropAt.before === fo.id && !isDragged("folder", fo.id);
    const folderEnd = endKey === `folder:${fo.id}`;
    const intoFolder =
      (dropAt?.kind === "feed" && dropAt.group === fo.id && inFolder.length === 0) || (dropAt?.kind === "folder" && dropAt.before === null && dropAt.group === fo.id);
    const state = selecting ? groupState(inSubtree, sel) : "none";
    const collapsed = !rules.open(fo.id);
    const listId = `manage-folder-${fo.id}-feeds`;
    const label = folderPath(ftree, fo.id);
    return (
      <li
        key={fo.id}
        style={rowStyle("folder", fo.id)}
        className={cn(dragCls("folder", fo.id), folderBefore && "border-t-2 border-accent", folderEnd && "border-b-2 border-accent")}
      >
        <div
          className={cn("flex items-center", DND_ROW_CLASS, intoFolder && "rounded-lg outline-2 outline-accent")}
          data-dnd-into={canHold && !canHold.has(fo.id) ? "no" : undefined}
          {...(selecting || !editMode ? {} : dnd.rowProps(fsrc))}
        >
          {selecting ? (
            <label className={cn(row, "min-w-0 flex-1 cursor-pointer text-sm font-semibold", state === "all" && "bg-selection")}>
              <input
                type="checkbox"
                aria-label={`Select all feeds in ${label}`}
                data-reveal-key={`folder:${fo.id}`}
                checked={state === "all"}
                ref={(el) => {
                  if (el) el.indeterminate = state === "some";
                }}
                onChange={() => {
                  setSel(toggleGroup(inSubtree, sel));
                  setAnchor(null);
                }}
                className="size-5 shrink-0 accent-[var(--kp-accent)]"
              />
              <FolderLine name={fo.name} unread={fo.unread} empty={inFolder.length === 0 && subfolders.length === 0} />
            </label>
          ) : (
            <>
              {editMode ? grip(fsrc, `folder ${label}`) : <CollapseToggle folder={fo} collapsed={collapsed} listId={listId} collapsedIds={dp.collapsedFolders} />}
              <Link to={openListTo({ folder: fo.id })} draggable={false} data-reveal-key={`folder:${fo.id}`} className={`${row} min-w-0 flex-1 text-sm font-semibold`}>
                <FolderLine name={fo.name} unread={fo.unread} empty={inFolder.length === 0 && subfolders.length === 0} />
              </Link>
            </>
          )}
          {!selecting ? (
            <>
              {moves(fsrc, siblings.indexOf(fo.id), siblings.length, `folder ${label}`)}
              <FavStar on={favs.has("folder", fo.id)} name={label} onToggle={() => favs.toggle("folder", fo.id)} />
              <DropdownMenu.Root>
                <DropdownMenu.Trigger asChild>
                  <Button variant="ghost" size="icon" id={folderActionsId(fo.id)} data-folder-actions="" aria-label={`Folder actions for ${label}`}>
                    <MoreVertical aria-hidden="true" />
                  </Button>
                </DropdownMenu.Trigger>
                <DropdownMenu.Portal>
                  <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-48">
                    {fo.unread > 0 ? (
                      <DropdownMenu.Item className={menuItem} onSelect={() => void markFolderRead(fo)}>
                        <CheckCheck className="size-5" aria-hidden="true" />
                        Mark all as read
                      </DropdownMenu.Item>
                    ) : null}
                    <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "rename", folder: fo })}>
                      <Settings2 className="size-5" aria-hidden="true" />
                      Folder settings
                    </DropdownMenu.Item>
                    {!fo.is_default && depth < MAX_FOLDER_DEPTH ? (
                      <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "new", parent: fo.id })}>
                        <FolderPlus className="size-5" aria-hidden="true" />
                        New subfolder
                      </DropdownMenu.Item>
                    ) : null}
                    {!fo.is_default ? (
                      <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "move", folder: fo })}>
                        <FolderInput className="size-5" aria-hidden="true" />
                        Move to…
                      </DropdownMenu.Item>
                    ) : null}
                    {!fo.is_default ? (
                      <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "delete", folder: fo })}>
                        <Trash2 className="size-5" aria-hidden="true" />
                        Delete folder
                      </DropdownMenu.Item>
                    ) : null}
                  </DropdownMenu.Content>
                </DropdownMenu.Portal>
              </DropdownMenu.Root>
            </>
          ) : null}
        </div>
        {collapsed ? null : (
          <ul id={listId} className={depth < 4 ? "pl-6" : "pl-2"}>
            {subfolders.map((id) => folderNode(ftree.byId.get(id) as Folder))}
            {inFolder.map((f, i) => (reveal.shown(`feed:${f.id}`) ? feedRow(f, i, inFolder, fo) : null))}
            {editMode && fo.is_default && inFolder.length === 0 ? <li className="px-3 py-1 text-xs text-fg2">Feeds added without a folder go here.</li> : null}
          </ul>
        )}
      </li>
    );
  };

  // The Feed actions menu, grouped by job: the view filter, then Organize, Files, and Feed health. The plain and the
  // edit modes both show it.
  const actionsMenu = (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button variant="ghost" size="icon" id={FEED_ACTIONS_ID} aria-label="Feed actions">
          <MoreVertical aria-hidden="true" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="end" sideOffset={4} popoverWidth="min-w-64">
          {realFeeds.length > 0 ? (
            <>
              <DropdownMenu.Item className={menuItem} onSelect={() => setUnreadOnly((u) => !u)}>
                <ListFilter className="size-5" aria-hidden="true" />
                {unreadOnly ? "Show all feeds" : "Show only unread feeds"}
              </DropdownMenu.Item>
              <DropdownMenu.Separator className="my-1 h-px bg-line" />
            </>
          ) : null}
          <DropdownMenu.Label className={menuLabel}>Organize</DropdownMenu.Label>
          {realFeeds.length > 0 ? (
            <DropdownMenu.Item className={menuItem} onSelect={() => setSelecting(true)}>
              <CheckSquare className="size-5" aria-hidden="true" />
              Select feeds
            </DropdownMenu.Item>
          ) : null}
          <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "new", parent: null })}>
            <FolderPlus className="size-5" aria-hidden="true" />
            New folder
          </DropdownMenu.Item>
          <DropdownMenu.Label className={menuLabel}>Files</DropdownMenu.Label>
          <DropdownMenu.Item className={menuItem} onSelect={() => setImporting(true)}>
            <Upload className="size-5" aria-hidden="true" />
            Import feeds from a file (OPML)
          </DropdownMenu.Item>
          {/* The browser saves the file without a word; the note says what happened and what the file is called. */}
          <DropdownMenu.Item asChild className={menuItem} onSelect={() => toast("Downloading your feeds as kipple.opml")}>
            <a href="/api/opml" download>
              <Download className="size-5" aria-hidden="true" />
              Export feeds to a file (OPML)
            </a>
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );

  return (
    <div className="ui-font mx-auto flex h-full min-h-0 w-full max-w-3xl flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        {/* As in the list header: the title keeps at least 6.5rem, and where the title and the controls do not fit on
            one row (a narrow phone at a large text size) the controls drop to a row of their own. */}
        <div className="flex flex-wrap items-center gap-1 pt-2">
          <h1 className="min-w-[min(100%,6.5rem)] flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
            Feeds
          </h1>
          {/* Each mode shows its own controls: selecting has Done; editing has the Saving/Saved note (a fixed width, so
              Done never moves as the note comes and goes) and Done; otherwise Edit, Add feed and the menu. The button's
              own text carries the state (Edit, then Done), so there is no aria-pressed to announce it twice. */}
          <div className="ml-auto flex flex-wrap items-center justify-end gap-1">
            {selecting ? (
              <Button variant="ghost" onClick={exitSelect}>
                Done
              </Button>
            ) : (
              <>
                {editMode ? (
                  <span role="status" aria-live="polite" className="inline-flex min-w-16 items-center justify-end gap-1 text-xs text-fg2" data-testid="saved-note">
                    {saved === "saved" ? (
                      <>
                        <Check className="size-4" aria-hidden="true" />
                        Saved
                      </>
                    ) : saved === "saving" ? (
                      "Saving"
                    ) : null}
                  </span>
                ) : null}
                {/* One button in one place for Edit and Done, so keyboard focus stays on it as the mode changes. */}
                <Button variant="ghost" onClick={() => setEditMode((m) => !m)} aria-label={editMode ? "Done editing" : "Edit"}>
                  {editMode ? "Done" : "Edit"}
                </Button>
                {editMode ? null : (
                  <Button variant="solid" onClick={() => setAdding(true)}>
                    <Plus aria-hidden="true" />
                    Add feed
                  </Button>
                )}
                {actionsMenu}
              </>
            )}
          </div>
        </div>
        {/* Help only where a mode needs it: a plain visit to this screen is the list and nothing else. */}
        {selecting ? (
          <p className="pb-1 text-xs text-fg2">
            {tapVerb()} feeds to select them, then move or delete them.{finePointer ? " Shift-click selects a range; with the keyboard, Space selects the focused feed." : ""}
          </p>
        ) : editMode ? (
          <p className="flex flex-wrap items-center gap-x-2 pb-1 text-xs text-fg2">
            {finePointer ? "Drag a feed or folder to reorder it. Changes are saved as you drop." : "Hold and drag a feed or folder to reorder it, or use the move buttons. Changes are saved as you drop."}
            <button type="button" className="min-h-11 font-semibold text-accent underline" onClick={() => setButtons((b) => !b)}>
              {buttons ? "Hide move buttons" : "Show move buttons"}
            </button>
          </p>
        ) : unreadOnly ? (
          <p className="flex items-center gap-2 pb-1 text-xs text-fg2">
            Showing only feeds with unread articles.
            <button type="button" className="min-h-11 font-semibold text-accent underline" onClick={() => setUnreadOnly(false)}>
              Show all
            </button>
          </p>
        ) : null}
      </header>
      <div ref={scroller} data-reveal-root="" className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {boot.isPending ? <Skeleton rows={5} label="Loading feeds" /> : null}
        {boot.isError ? <Notice tone="error">Couldn't load your feeds. Check your connection and try again.</Notice> : null}
        {boot.data && realFeeds.length === 0 ? <FirstRun onAdd={() => setAdding(true)} onImport={() => setImporting(true)} /> : null}
        {/* With no feeds the tree still shows the folders you made, so a new folder is never invisible. */}
        {boot.data && (realFeeds.length > 0 || folders.some((fo) => !fo.is_default)) ? (
          <>
            {selecting ? null : (
              <ul className="mb-2 flex flex-col gap-1">
                <li>
                  <Link to={DESTINATIONS.feedIndex.to} className={row}>
                    {DESTINATIONS.feedIndex.label}
                  </Link>
                </li>
                <li>
                  <Link to="/health" className={row}>
                    Feed health
                  </Link>
                </li>
              </ul>
            )}
            {favs.favorites.length > 0 && !selecting ? (
              <section aria-label="Favorites" className="mb-3">
                <h2 className="px-3 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">Favorites</h2>
                <ul className="flex flex-col">{favRows}</ul>
              </section>
            ) : null}
            {selecting ? null : <SavedSearchesNav />}
            {filtering && topLevel.length === 0 ? (
              <div className="px-3 py-6 text-center">
                <p className="text-sm text-fg2">Every feed is read.</p>
                <Button className="mt-2" onClick={() => setUnreadOnly(false)}>
                  Show all feeds
                </Button>
              </div>
            ) : (
              <ul className="flex flex-col gap-1">{topLevel.map((id) => folderNode(ftree.byId.get(id) as Folder))}</ul>
            )}
            <RevealMore reveal={reveal} noun="feeds" />
          </>
        ) : null}
      </div>
      {selecting ? (
        <BottomBar ariaLabel="Selected feeds">
          <span className="mr-auto text-sm font-semibold" aria-live="polite">
            {selected.length} selected
          </span>
          <Button onClick={() => setSel(new Set(visibleOrder))} disabled={selected.length === visibleOrder.length}>
            Select all
          </Button>
          <Button onClick={() => setBulk("move")} disabled={selected.length === 0}>
            Move
          </Button>
          <Button variant="danger" onClick={() => setBulk("delete")} disabled={selected.length === 0}>
            Delete
          </Button>
        </BottomBar>
      ) : null}
      <Suspense fallback={null}>
        {adding ? <AddFeedDialog onClose={() => setAdding(false)} onOpenFeed={(id) => { setAdding(false); navigate(openListTo({ feed: id })); }} /> : null}
        {importing ? (
          <OpmlImportDialog
            onClose={() => {
              setImporting(false);
              focusActions(FEED_ACTIONS_ID);
            }}
          />
        ) : null}
        {editing ? <FeedEditor feed={editing} onClose={() => setEditing(null)} /> : null}
      </Suspense>
      {bulk === "move" ? <MoveDialog feeds={selected} folders={folders} allFeeds={feeds} onClose={() => setBulk(null)} onDone={exitSelect} /> : null}
      {bulk === "delete" ? (
        <DeleteDialog
          feeds={selected}
          onClose={() => setBulk(null)}
          onDone={(ids, all) => {
            if (all) exitSelect();
            else setSel((s) => new Set([...s].filter((x) => !ids.includes(x))));
          }}
        />
      ) : null}
      {folderDialog ? (
        <FolderDialogs
          dialog={folderDialog}
          tree={ftree}
          feeds={realFeeds}
          onMove={moveFolder}
          onClose={(focusFolder) => {
            // The menu item the dialog opened from is gone by now: put the focus back on that menu's button.
            const from = folderDialog.kind === "new" ? folderDialog.parent : folderDialog.folder.id;
            setFolderDialog(null);
            if (focusFolder ?? from) reveal.ensure(`folder:${focusFolder ?? from}`);
            if (focusFolder) focusActions(folderActionsId(focusFolder));
            else if (from) focusActions(folderActionsId(from));
            else focusActions(FEED_ACTIONS_ID);
          }}
        />
      ) : null}
    </div>
  );
}
