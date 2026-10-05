import { Suspense, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { lazyScreen } from "@/lib/lazyScreen";
import { Link, useLocation, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { DropdownMenu } from "radix-ui";
import { ArrowDown, ArrowUp, Check, CheckSquare, Download, FolderInput, FolderPlus, GripVertical, HeartPulse, MoreVertical, Pencil, Plus, Trash2, Upload } from "lucide-react";
import { createFolder, deleteFolder, invalidateFeeds, patchFolder, reorder as reorderApi } from "@/api/admin";
import { ApiError, errorMessage } from "@/api/client";
import { keys, useBootstrap } from "@/api/queries";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import { LAYOUT_IDS, LAYOUT_LABELS, setLayoutOverride, useDevicePrefs, type LayoutId } from "@/lib/devicePrefs";
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
import { MAX_FOLDER_DEPTH, childrenOf, depthOf, feedOrder, folderPath, folderTree, parentChoices, parentOf, rollUp, subtreeFeeds, subtreeOf, type FolderTree } from "@/lib/folderTree";
import { FolderSelect } from "@/ui/FolderSelect";
import { useFavorites } from "@/lib/favorites";
import { clickRow, groupState, toggleGroup } from "@/lib/selection";
import { cn } from "@/lib/cn";
import { listTo } from "@/lib/routes";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { FavStar } from "@/ui/FavStar";
import { MutedCount } from "@/ui/UnreadCount";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";
import { FirstRun } from "./FirstRun";
import { StatusChip } from "./StatusChip";
import { SavedSearchesNav } from "./SavedSearchesNav";
import { CollapseToggle } from "./FeedTree";
import { DeleteDialog, MoveDialog } from "./feeds/BulkActions";

// The dialogs load when first opened, not with the Feeds screen.
const AddFeedDialog = lazyScreen(() => import("./feeds/AddFeedDialog").then((m) => ({ default: m.AddFeedDialog })));
const FeedEditor = lazyScreen(() => import("./feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));
const OpmlImportDialog = lazyScreen(() => import("./feeds/OpmlDialog").then((m) => ({ default: m.OpmlImportDialog })));

const row = "flex min-h-11 items-center gap-2 rounded-lg px-3 hover:bg-selection";
const menuItem = "flex min-h-11 cursor-default items-center gap-3 rounded-lg px-3 text-sm outline-none select-none data-[highlighted]:bg-selection";

function Badge({ n }: { n: number }) {
  if (n <= 0) return null;
  return (
    <span className="ml-auto shrink-0 rounded-full bg-surface px-2 py-0.5 text-xs font-semibold text-fg2 tabular-nums">
      <span className="sr-only-live">Unread </span>
      {n > 9999 ? "9999+" : n}
    </span>
  );
}

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
  const dp = useDevicePrefs();
  const [name, setName] = useState(dialog.kind === "rename" ? dialog.folder.name : "");
  const [parent, setParent] = useState(dialog.kind === "new" ? (dialog.parent ?? "") : dialog.kind === "move" ? (parentOf(tree, dialog.folder.id) ?? "") : "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => Promise<unknown>, done: string, focusFolder?: string) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
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
      <Modal
        open
        onOpenChange={(o) => !o && onClose()}
        title={`Delete ${pathOf(f.id)}?`}
        description={deleteFolderText(tree, f.id, feeds)}
        footer={
          <>
            <Button onClick={() => onClose()}>Cancel</Button>
            <Button variant="solid" disabled={busy} onClick={() => void run(() => deleteFolder(f.id), `Deleted folder ${f.name}`, focusNext)}>
              Delete folder
            </Button>
          </>
        }
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
      </Modal>
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
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={isNew ? (dialog.parent ? "New subfolder" : "New folder") : "Rename folder"}
      footer={
        <>
          <Button onClick={() => onClose()}>Cancel</Button>
          <Button
            variant="solid"
            disabled={busy || !name.trim()}
            onClick={() =>
              void run(
                () => (isNew ? createFolder(name.trim(), parent || null) : patchFolder(dialog.folder.id, { name: name.trim() })),
                isNew ? "Folder created" : "Folder renamed",
              )
            }
          >
            {isNew ? "Create" : "Save"}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      <form
        onSubmit={(e) => {
          e.preventDefault();
        }}
        className="flex flex-col gap-4"
      >
        <Field label="Name">
          {(a) => <input {...a} type="text" maxLength={100} autoFocus value={name} onChange={(e) => setName(e.target.value)} className={inputCls} />}
        </Field>
        {isNew ? (
          <Field label="Inside">{(a) => <FolderSelect {...a} value={parent} onChange={setParent} only={parentChoices(tree, null)} none="Top level" />}</Field>
        ) : (
          <Field label="Layout on this device" help="Overrides the device default for this folder and its subfolders, unless a subfolder or a feed has its own.">
            {(a) => (
              <select
                {...a}
                value={dp.overrides.folder[dialog.folder.id] ?? "default"}
                onChange={(e) => setLayoutOverride("folder", dialog.folder.id, e.target.value === "default" ? null : (e.target.value as LayoutId))}
                className={inputCls}
              >
                <option value="default">Use device default</option>
                {LAYOUT_IDS.map((l) => (
                  <option key={l} value={l}>
                    {LAYOUT_LABELS[l]}
                  </option>
                ))}
              </select>
            )}
          </Field>
        )}
      </form>
    </Modal>
  );
}

type Sel = ReadonlySet<string>;

const folderActionsId = (folder: string) => `folder-actions-${folder}`;

/**
 * After a move or a delete, put the focus on a folder row's actions button. The row re-mounts (moved) or the dialog's
 * own focus return lands on a button that is about to go (deleted), so this keeps trying for a moment, without taking
 * the focus from anything the person moved it to since.
 */
function focusFolderActions(folder: string) {
  let tries = 0;
  const tick = () => {
    const el = document.getElementById(folderActionsId(folder));
    const now = document.activeElement;
    const adrift = !now || now === document.body || !now.isConnected || (now instanceof HTMLElement && now.dataset.folderActions !== undefined);
    if (el && now !== el && adrift) el.focus();
    if (++tries < 20) setTimeout(tick, 50);
  };
  setTimeout(tick, 0);
}

/** Feeds: where you open a folder or feed, and where feeds and folders are added, edited, ordered, favorited and removed. */
export function FeedsScreen() {
  const boot = useBootstrap();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const favs = useFavorites();
  const dp = useDevicePrefs();
  const c = boot.data?.counts;
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
  const [sel, setSel] = useState<Sel>(new Set());
  const [anchor, setAnchor] = useState<string | null>(null);
  const [bulk, setBulk] = useState<null | "move" | "delete">(null);
  const [saved, setSaved] = useState<"saving" | "saved" | null>(null);
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const scroller = useRef<HTMLDivElement>(null);

  const feeds = useMemo(() => boot.data?.feeds ?? [], [boot.data?.feeds]);
  const folders = useMemo(() => boot.data?.folders ?? [], [boot.data?.folders]);
  const realFeeds = useMemo(() => visibleFeeds(feeds), [feeds]);
  const byFolder = useMemo(() => {
    const m: Record<string, Feed[]> = {};
    for (const fo of folders) m[fo.id] = [];
    for (const f of realFeeds) (m[f.folder_id] ??= []).push(f);
    return m;
  }, [folders, realFeeds]);
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

  const drop = (from: DragSource, to: DropTarget) => {
    if (from.kind === "folder") void applyTree(planFolderDrop(tree, from.id, { parent: to.group || null, before: to.before }));
    else if (from.kind === "feed") void applyTree(planFeedDrop(tree, from.id, { folder: to.group, before: to.before }));
    else {
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
      announce(`Moved ${ftree.byId.get(src.id)?.name ?? "folder"} to position ${i + delta + 1} of ${siblings.length}`);
    } else if (src.kind === "feed") {
      const list = tree.feeds[src.group] ?? [];
      const i = list.indexOf(src.id);
      if (i + delta < 0 || i + delta >= list.length) return;
      void applyTree({ ...tree, feeds: { ...tree.feeds, [src.group]: arrayMove(list, i, i + delta) } });
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

  // ---- selection ----
  const selected = realFeeds.filter((f) => sel.has(f.id));
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
      <GripVertical className="size-4" aria-hidden="true" />
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
    const atEnd = dropAt?.kind === "feed" && dropAt.group === fo.id && dropAt.before === null && list.filter((x) => x.id !== dragging?.id).at(-1)?.id === f.id;
    return (
      <li
        key={f.id}
        {...(selecting || !editMode ? {} : dnd.rowProps(src))}
        style={rowStyle("feed", f.id)}
        className={cn("flex items-center", DND_ROW_CLASS, dragCls("feed", f.id), before && "border-t-2 border-accent", atEnd && "border-b-2 border-accent")}
      >
        {selecting ? (
          <input
            type="checkbox"
            aria-label={`Select ${f.title}`}
            checked={sel.has(f.id)}
            onChange={() => undefined}
            onClick={(e) => onCheck(f.id, e.shiftKey)}
            className="mx-3 size-5 shrink-0 accent-[var(--kp-accent)]"
          />
        ) : editMode ? (
          grip(src, f.title)
        ) : null}
        <Link
          to={listTo({ view: "unread", feed: f.id })}
          draggable={false}
          onClick={(e) => {
            if (selecting) {
              e.preventDefault();
              onCheck(f.id, e.shiftKey);
            }
          }}
          className={`${row} min-w-0 flex-1 text-sm`}
        >
          <span className="min-w-0">
            <span className="block truncate">{f.title}</span>
            {f.status !== "ok" ? <StatusChip status={f.status} /> : null}
          </span>
          <Badge n={f.unread} />
        </Link>
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
    const name = favFolder ? folderPath(ftree, favFolder.id) : feeds.find((f) => f.id === fav.id)?.title;
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
            to={listTo(fav.t === "folder" ? { view: "unread", folder: fav.id } : { view: "unread", feed: fav.id })}
            draggable={false}
            className={`${row} min-w-0 flex-1 text-sm`}
          >
            <span className="truncate">{name}</span>
            <span className="ml-auto shrink-0 text-xs text-fg2">{fav.t === "folder" ? "Folder" : "Feed"}</span>
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
                <Link to={listTo({ view: "unread", feed: f.id })} draggable={false} className={`${row} text-sm`}>
                  <span className="truncate">{f.title}</span>
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
  const topLevel = childrenOf(ftree, null);

  /** A folder row, then (unless collapsed) its subfolders and its own feeds. */
  const folderNode = (fo: Folder): React.ReactNode => {
    const depth = depthOf(ftree, fo.id);
    const parent = parentOf(ftree, fo.id);
    const siblings = childrenOf(ftree, parent);
    const subfolders = childrenOf(ftree, fo.id);
    const inFolder = byFolder[fo.id] ?? [];
    const inSubtree = subtreeIds.get(fo.id) ?? [];
    const fsrc: DragSource = { kind: "folder", id: fo.id, group: parent ?? "" };
    const folderBefore = dropAt?.kind === "folder" && dropAt.before === fo.id && !isDragged("folder", fo.id);
    const folderEnd = dropAt?.kind === "folder" && dropAt.before === null && dropAt.group === "" && parent === null && topLevel.filter((x) => x !== dragging?.id).at(-1) === fo.id;
    const intoFolder =
      (dropAt?.kind === "feed" && dropAt.group === fo.id && inFolder.length === 0) || (dropAt?.kind === "folder" && dropAt.before === null && dropAt.group === fo.id);
    const state = groupState(inSubtree, sel);
    // Edit and Select need every feed on screen and reachable (a collapsed folder's chevron is replaced by
    // the grip or checkbox in those modes, and Select all / shift-click range over every feed), so the
    // saved collapsed state only applies outside them; it is not changed, and returns when the mode ends.
    const collapsed = !selecting && !editMode && dp.collapsedFolders.includes(fo.id);
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
            <input
              type="checkbox"
              aria-label={`Select all feeds in ${label}`}
              checked={state === "all"}
              ref={(el) => {
                if (el) el.indeterminate = state === "some";
              }}
              onChange={() => {
                setSel(toggleGroup(inSubtree, sel));
                setAnchor(null);
              }}
              className="mx-3 size-5 shrink-0 accent-[var(--kp-accent)]"
            />
          ) : editMode ? (
            grip(fsrc, `folder ${label}`)
          ) : (
            <CollapseToggle folder={fo} collapsed={collapsed} listId={listId} collapsedIds={dp.collapsedFolders} />
          )}
          <Link to={listTo({ view: "unread", folder: fo.id })} draggable={false} className={`${row} min-w-0 flex-1 text-sm font-semibold`}>
            <span className="truncate">{fo.name}</span>
            <Badge n={fo.unread} />
          </Link>
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
                  <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-48 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
                    <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "rename", folder: fo })}>
                      <Pencil className="size-5" aria-hidden="true" />
                      Rename or set layout
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
            {inFolder.map((f, i) => feedRow(f, i, inFolder, fo))}
          </ul>
        )}
      </li>
    );
  };

  return (
    <div className="ui-font mx-auto flex h-full min-h-0 w-full max-w-3xl flex-col">
      <header className="pt-safe shrink-0 border-b border-line px-4 pb-2">
        <div className="flex items-center gap-1 pt-2">
          <h1 className="min-w-0 flex-1 truncate text-xl font-bold" tabIndex={-1} data-route-heading>
            Feeds
          </h1>
          <span role="status" aria-live="polite" className="mr-1 inline-flex items-center gap-1 text-xs text-fg2" data-testid="saved-note">
            {saved === "saved" ? (
              <>
                <Check className="size-4" aria-hidden="true" />
                Saved
              </>
            ) : saved === "saving" ? (
              "Saving"
            ) : null}
          </span>
          {/* The text hides below 400px, so the name comes from aria-label, matching the text when shown. The name
              itself carries the state (Select/Edit, then Done), so there is no aria-pressed to announce it twice. */}
          {!selecting ? (
            <Button variant="ghost" onClick={() => setEditMode((e) => !e)} aria-label={editMode ? "Done editing" : "Edit"}>
              <Pencil aria-hidden="true" />
              <span className="hidden min-[400px]:inline">{editMode ? "Done" : "Edit"}</span>
            </Button>
          ) : null}
          <Button variant="ghost" onClick={() => (selecting ? exitSelect() : setSelecting(true))} aria-label={selecting ? "Done" : "Select"}>
            <CheckSquare aria-hidden="true" />
            <span className="hidden min-[400px]:inline">{selecting ? "Done" : "Select"}</span>
          </Button>
          <Button variant="ghost" onClick={() => setAdding(true)} aria-label="Add feed">
            <Plus aria-hidden="true" />
            <span className="hidden min-[400px]:inline">Add feed</span>
          </Button>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button variant="ghost" size="icon" aria-label="Feed actions">
                <MoreVertical aria-hidden="true" />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content align="end" sideOffset={4} collisionPadding={8} className="z-50 min-w-56 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
                <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "new", parent: null })}>
                  <FolderPlus className="size-5" aria-hidden="true" />
                  New folder
                </DropdownMenu.Item>
                <DropdownMenu.Item className={menuItem} onSelect={() => setImporting(true)}>
                  <Upload className="size-5" aria-hidden="true" />
                  Import OPML
                </DropdownMenu.Item>
                <DropdownMenu.Item asChild className={menuItem}>
                  <a href="/api/opml" download>
                    <Download className="size-5" aria-hidden="true" />
                    Export OPML
                  </a>
                </DropdownMenu.Item>
                <DropdownMenu.Item className={menuItem} onSelect={() => setButtons((r) => !r)}>
                  <ArrowUp className="size-5" aria-hidden="true" />
                  {buttons ? "Hide move buttons" : "Show move buttons"}
                </DropdownMenu.Item>
                <DropdownMenu.Item className={menuItem} onSelect={() => navigate("/health")}>
                  <HeartPulse className="size-5" aria-hidden="true" />
                  Feed health
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
        <p className="pb-1 text-xs text-fg2">
          {selecting
            ? "Tick feeds to move or delete them. Shift-click ticks a range."
            : editMode
              ? "Drag a feed or folder to reorder it. Changes are saved as you drop."
              : "Tap a folder to collapse it. Tap Edit to reorder, rename or delete a feed."}
        </p>
      </header>
      <div ref={scroller} className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {boot.isPending ? <Skeleton rows={5} label="Loading feeds" /> : null}
        {boot.isError ? <Notice tone="error">Couldn't load your feeds. Check your connection and try again.</Notice> : null}
        {boot.data && realFeeds.length === 0 ? (
          <FirstRun onAdd={() => setAdding(true)} onImport={() => setImporting(true)} />
        ) : boot.data ? (
          <>
            <ul className="mb-2 flex flex-col gap-1">
              <li>
                <Link to={listTo({ view: "unread" })} className={row}>
                  Unread
                  <Badge n={c?.unread ?? 0} />
                </Link>
              </li>
              <li>
                <Link to={listTo({ view: "all" })} className={row}>
                  All articles
                </Link>
              </li>
              <li>
                <Link to={listTo({ view: "starred" })} className={row}>
                  Starred
                  <Badge n={c?.starred ?? 0} />
                </Link>
              </li>
              <li>
                <Link to={listTo({ view: "muted" })} className={row}>
                  Muted
                  <MutedCount n={c?.muted ?? 0} className="ml-auto" />
                </Link>
              </li>
            </ul>
            {favs.favorites.length > 0 ? (
              <section aria-label="Favorites" className="mb-3">
                <h2 className="px-3 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">Favorites</h2>
                <ul className="flex flex-col">{favRows}</ul>
              </section>
            ) : null}
            <SavedSearchesNav />
            <ul className="flex flex-col gap-1">{topLevel.map((id) => folderNode(ftree.byId.get(id) as Folder))}</ul>
          </>
        ) : null}
      </div>
      {selecting ? (
        <div role="region" aria-label="Selected feeds" className="pb-safe flex shrink-0 flex-wrap items-center gap-2 border-t border-line bg-surface px-4 py-2">
          <span className="mr-auto text-sm font-semibold" aria-live="polite">
            {selected.length} selected
          </span>
          <Button onClick={() => setSel(new Set(visibleOrder))} disabled={selected.length === visibleOrder.length}>
            Select all
          </Button>
          <Button onClick={() => setBulk("move")} disabled={selected.length === 0}>
            Move to folder
          </Button>
          <Button variant="solid" onClick={() => setBulk("delete")} disabled={selected.length === 0}>
            Delete
          </Button>
        </div>
      ) : null}
      <Suspense fallback={null}>
        {adding ? <AddFeedDialog onClose={() => setAdding(false)} onOpenFeed={(id) => { setAdding(false); navigate(listTo({ view: "unread", feed: id })); }} /> : null}
        {importing ? <OpmlImportDialog onClose={() => setImporting(false)} /> : null}
        {editing ? <FeedEditor feed={editing} onClose={() => setEditing(null)} /> : null}
      </Suspense>
      {bulk === "move" ? <MoveDialog feeds={selected} folders={folders} allFeeds={feeds} onClose={() => setBulk(null)} onDone={exitSelect} /> : null}
      {bulk === "delete" ? (
        <DeleteDialog
          feeds={selected}
          onClose={() => setBulk(null)}
          onDone={(ids) => setSel((s) => new Set([...s].filter((x) => !ids.includes(x))))}
        />
      ) : null}
      {folderDialog ? (
        <FolderDialogs
          dialog={folderDialog}
          tree={ftree}
          feeds={realFeeds}
          onMove={moveFolder}
          onClose={(focusFolder) => {
            setFolderDialog(null);
            if (focusFolder) focusFolderActions(focusFolder);
          }}
        />
      ) : null}
    </div>
  );
}
