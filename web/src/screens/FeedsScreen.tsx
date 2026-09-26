import { Suspense, lazy, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { DropdownMenu } from "radix-ui";
import { ArrowDown, ArrowUp, Check, CheckSquare, Download, FolderPlus, GripVertical, HeartPulse, MoreVertical, Pencil, Plus, Upload } from "lucide-react";
import { createFolder, deleteFolder, invalidateFeeds, patchFolder, reorder as reorderApi } from "@/api/admin";
import { errorMessage } from "@/api/client";
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
  useRowDnd,
  type DragKind,
  type DragSource,
  type DropTarget,
  type Tree,
} from "@/lib/dnd";
import { useFavorites } from "@/lib/favorites";
import { clickRow, groupState, toggleGroup } from "@/lib/selection";
import { cn } from "@/lib/cn";
import { listTo } from "@/lib/routes";
import { FavStar } from "@/ui/FavStar";
import { Button } from "@/ui/button";
import { Field, Modal, Notice, Skeleton, inputCls } from "@/ui/kit";
import { announce, toast } from "@/shell/toasts";
import { FirstRun } from "./FirstRun";
import { StatusChip } from "./StatusChip";
import { DeleteDialog, MoveDialog } from "./feeds/BulkActions";

// The dialogs load when first opened, not with the Feeds screen.
const AddFeedDialog = lazy(() => import("./feeds/AddFeedDialog").then((m) => ({ default: m.AddFeedDialog })));
const FeedEditor = lazy(() => import("./feeds/FeedEditor").then((m) => ({ default: m.FeedEditor })));
const OpmlImportDialog = lazy(() => import("./feeds/OpmlDialog").then((m) => ({ default: m.OpmlImportDialog })));

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

type FolderDialog = { kind: "new" } | { kind: "rename"; folder: Folder } | { kind: "delete"; folder: Folder };

function FolderDialogs({ dialog, onClose }: { dialog: FolderDialog; onClose: () => void }) {
  const qc = useQueryClient();
  const dp = useDevicePrefs();
  const [name, setName] = useState(dialog.kind === "rename" ? dialog.folder.name : "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const fail = (e: unknown) => {
    const code = (e as { code?: string }).code;
    setError(code === "folder_exists" ? "A folder with that name already exists." : errorMessage(e));
  };
  const run = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      invalidateFeeds(qc);
      toast(done);
      onClose();
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };

  if (dialog.kind === "delete") {
    const f = dialog.folder;
    return (
      <Modal
        open
        onOpenChange={(o) => !o && onClose()}
        title={`Delete ${f.name}?`}
        description="Its feeds are not deleted. They move to the default folder."
        footer={
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="solid" disabled={busy} onClick={() => void run(() => deleteFolder(f.id), `Deleted folder ${f.name}`)}>
              Delete folder
            </Button>
          </>
        }
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
      </Modal>
    );
  }
  const isNew = dialog.kind === "new";
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={isNew ? "New folder" : "Rename folder"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="solid"
            disabled={busy || !name.trim()}
            onClick={() => void run(() => (isNew ? createFolder(name.trim()) : patchFolder(dialog.folder.id, { name: name.trim() })), isNew ? "Folder created" : "Folder renamed")}
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
        {!isNew ? (
          <Field label="Layout on this device" help="Overrides the device default for every feed in this folder that has no layout of its own.">
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
        ) : null}
      </form>
    </Modal>
  );
}

type Sel = ReadonlySet<string>;

/** Feeds: where you open a folder or feed, and where feeds and folders are added, edited, ordered, favorited and removed. */
export function FeedsScreen() {
  const boot = useBootstrap();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const favs = useFavorites();
  const c = boot.data?.counts;
  const open = (useLocation().state as { open?: string } | null)?.open;
  const [adding, setAdding] = useState(open === "add");
  const [importing, setImporting] = useState(open === "import");
  const [editing, setEditing] = useState<Feed | null>(null);
  const [folderDialog, setFolderDialog] = useState<FolderDialog | null>(null);
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
  const realFeeds = useMemo(() => feeds.filter((f) => !f.is_archive), [feeds]);
  const byFolder = useMemo(() => {
    const m: Record<string, Feed[]> = {};
    for (const fo of folders) m[fo.id] = [];
    for (const f of realFeeds) (m[f.folder_id] ??= []).push(f);
    return m;
  }, [folders, realFeeds]);
  const tree: Tree = useMemo(
    () => ({ folders: folders.map((f) => f.id), feeds: Object.fromEntries(Object.entries(byFolder).map(([k, v]) => [k, v.map((f) => f.id)])) }),
    [folders, byFolder],
  );
  // The order feeds appear on screen: what shift-click ranges are measured along.
  const visibleOrder = useMemo(() => tree.folders.flatMap((id) => tree.feeds[id] ?? []), [tree]);

  const markSaved = () => {
    setSaved("saved");
    announce("Order saved");
    if (savedTimer.current) clearTimeout(savedTimer.current);
    savedTimer.current = setTimeout(() => setSaved(null), 2500);
  };
  useEffect(() => () => clearTimeout(savedTimer.current), []);

  /** Apply a new order: paint it at once, save it in one POST /api/reorder, show "Saved" (or put the old order back). */
  const applyTree = async (next: Tree) => {
    const body = reorderBody(tree, next);
    if (!body) return;
    setSaved("saving");
    qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => {
      if (!old) return old;
      const byId = new Map(old.feeds.map((f) => [f.id, f]));
      const ordered: Feed[] = [];
      for (const fid of next.folders) for (const id of next.feeds[fid] ?? []) {
        const f = byId.get(id);
        if (f) ordered.push({ ...f, folder_id: fid });
      }
      return {
        ...old,
        folders: next.folders.map((id, i) => ({ ...(old.folders.find((f) => f.id === id) as Folder), position: i })),
        feeds: [...ordered, ...old.feeds.filter((f) => f.is_archive)],
      };
    });
    try {
      await reorderApi(body);
      invalidateFeeds(qc);
      markSaved();
    } catch (e) {
      setSaved(null);
      toast(errorMessage(e), "error");
      invalidateFeeds(qc);
    }
  };

  const applyFavs = async (next: Favorite[]) => {
    setSaved("saving");
    if (await favs.set(next)) markSaved();
    else setSaved(null);
  };
  const favKey = (f: Favorite) => `${f.t}:${f.id}`;

  const drop = (from: DragSource, to: DropTarget) => {
    if (from.kind === "folder") void applyTree(planFolderDrop(tree, from.id, to.before));
    else if (from.kind === "feed") void applyTree(planFeedDrop(tree, from.id, { folder: to.group, before: to.before }));
    else {
      const keys2 = favs.favorites.map(favKey);
      const order = insertBefore(keys2, from.id, to.before);
      void applyFavs(order.map((k) => favs.favorites.find((f) => favKey(f) === k) as Favorite));
    }
  };

  /** One place up or down inside the list the row lives in (arrow keys on the grip, and the buttons). */
  const step = (src: DragSource, delta: -1 | 1) => {
    if (src.kind === "folder") {
      const i = tree.folders.indexOf(src.id);
      if (i + delta < 0 || i + delta >= tree.folders.length) return;
      void applyTree({ ...tree, folders: arrayMove(tree.folders, i, i + delta) });
      announce(`Moved ${folders.find((f) => f.id === src.id)?.name ?? "folder"} to position ${i + delta + 1} of ${tree.folders.length}`);
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

  const dnd = useRowDnd({ enabled: !selecting, onDrop: drop, scroller: () => scroller.current, onKeyMove: step });
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
    buttons ? (
      <>
        <Button variant="ghost" size="icon" aria-label={`Move ${label} up`} disabled={i === 0} onClick={() => step(src, -1)}>
          <ArrowUp aria-hidden="true" />
        </Button>
        <Button variant="ghost" size="icon" aria-label={`Move ${label} down`} disabled={i === n - 1} onClick={() => step(src, 1)}>
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
        {...(selecting ? {} : dnd.rowProps(src))}
        style={rowStyle("feed", f.id)}
        className={cn("flex items-center select-none", dragCls("feed", f.id), before && "border-t-2 border-accent", atEnd && "border-b-2 border-accent")}
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
        ) : (
          grip(src, f.title)
        )}
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
            <Button variant="ghost" size="icon" aria-label={`Edit ${f.title}`} onClick={() => setEditing(f)}>
              <Pencil aria-hidden="true" />
            </Button>
          </>
        ) : null}
      </li>
    );
  };

  const favRows = favs.favorites.map((fav, i) => {
    const key = favKey(fav);
    const src: DragSource = { kind: "fav", id: key, group: "fav" };
    const name = fav.t === "folder" ? folders.find((f) => f.id === fav.id)?.name : feeds.find((f) => f.id === fav.id)?.title;
    const before = dropAt?.kind === "fav" && dropAt.before === key && !isDragged("fav", key);
    const atEnd = dropAt?.kind === "fav" && dropAt.before === null && favs.favorites.filter((x) => favKey(x) !== dragging?.id).at(-1) === fav;
    return (
      <li
        key={key}
        {...(selecting ? {} : dnd.rowProps(src))}
        style={rowStyle("fav", key)}
        className={cn("flex items-center select-none", dragCls("fav", key), before && "border-t-2 border-accent", atEnd && "border-b-2 border-accent")}
      >
        {!selecting ? grip(src, `favorite ${name ?? ""}`) : null}
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
      </li>
    );
  });

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
          <Button variant="ghost" onClick={() => (selecting ? exitSelect() : setSelecting(true))} aria-pressed={selecting}>
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
                <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "new" })}>
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
          {selecting ? "Tick feeds to move or delete them. Shift-click ticks a range." : "Drag a feed or folder to reorder it. Changes are saved as you drop."}
          {favs.mode === "device" && favs.favorites.length > 0 ? " Favorites are kept on this device." : ""}
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
            </ul>
            {favs.favorites.length > 0 ? (
              <section aria-label="Favorites" className="mb-3">
                <h2 className="px-3 pb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">Favorites</h2>
                <ul className="flex flex-col">{favRows}</ul>
              </section>
            ) : null}
            <ul className="flex flex-col gap-1">
              {folders.map((fo, fi) => {
                const inFolder = byFolder[fo.id] ?? [];
                const fsrc: DragSource = { kind: "folder", id: fo.id, group: "folders" };
                const folderBefore = dropAt?.kind === "folder" && dropAt.before === fo.id && !isDragged("folder", fo.id);
                const folderEnd = dropAt?.kind === "folder" && dropAt.before === null && folders.filter((x) => x.id !== dragging?.id).at(-1)?.id === fo.id;
                const intoFolder = dropAt?.kind === "feed" && dropAt.group === fo.id && inFolder.length === 0;
                const state = groupState(inFolder.map((f) => f.id), sel);
                return (
                  <li
                    key={fo.id}
                    style={rowStyle("folder", fo.id)}
                    className={cn(dragCls("folder", fo.id), folderBefore && "border-t-2 border-accent", folderEnd && "border-b-2 border-accent")}
                  >
                    <div className={cn("flex items-center select-none", intoFolder && "rounded-lg outline-2 outline-accent")} {...(selecting ? {} : dnd.rowProps(fsrc))}>
                      {selecting ? (
                        <input
                          type="checkbox"
                          aria-label={`Select all feeds in ${fo.name}`}
                          checked={state === "all"}
                          ref={(el) => {
                            if (el) el.indeterminate = state === "some";
                          }}
                          onChange={() => {
                            setSel(toggleGroup(inFolder.map((f) => f.id), sel));
                            setAnchor(null);
                          }}
                          className="mx-3 size-5 shrink-0 accent-[var(--kp-accent)]"
                        />
                      ) : (
                        grip(fsrc, `folder ${fo.name}`)
                      )}
                      <Link to={listTo({ view: "unread", folder: fo.id })} draggable={false} className={`${row} min-w-0 flex-1 text-sm font-semibold`}>
                        <span className="truncate">{fo.name}</span>
                        <Badge n={fo.unread} />
                      </Link>
                      {!selecting ? (
                        <>
                          {moves(fsrc, fi, folders.length, `folder ${fo.name}`)}
                          <FavStar on={favs.has("folder", fo.id)} name={fo.name} onToggle={() => favs.toggle("folder", fo.id)} />
                          <DropdownMenu.Root>
                            <DropdownMenu.Trigger asChild>
                              <Button variant="ghost" size="icon" aria-label={`Folder actions for ${fo.name}`}>
                                <MoreVertical aria-hidden="true" />
                              </Button>
                            </DropdownMenu.Trigger>
                            <DropdownMenu.Portal>
                              <DropdownMenu.Content align="end" sideOffset={4} className="z-50 min-w-48 rounded-xl border border-line bg-bg p-1 text-fg shadow-xl">
                                <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "rename", folder: fo })}>
                                  <Pencil className="size-5" aria-hidden="true" />
                                  Rename or set layout
                                </DropdownMenu.Item>
                                {!fo.is_default ? (
                                  <DropdownMenu.Item className={menuItem} onSelect={() => setFolderDialog({ kind: "delete", folder: fo })}>
                                    Delete folder
                                  </DropdownMenu.Item>
                                ) : null}
                              </DropdownMenu.Content>
                            </DropdownMenu.Portal>
                          </DropdownMenu.Root>
                        </>
                      ) : null}
                    </div>
                    <ul className="pl-6">{inFolder.map((f, i) => feedRow(f, i, inFolder, fo))}</ul>
                  </li>
                );
              })}
            </ul>
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
      {folderDialog ? <FolderDialogs dialog={folderDialog} onClose={() => setFolderDialog(null)} /> : null}
    </div>
  );
}
