import { useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Link } from "react-router";
import { ChevronDown, ChevronRight } from "lucide-react";
import { useBootstrap } from "@/api/queries";
import type { Feed, Folder } from "@/api/types";
import { updateDevicePrefs, useDevicePrefs, type UnreadBadge } from "@/lib/devicePrefs";
import { useFavorites } from "@/lib/favorites";
import { childrenOf, folderPath, folderTree, subtreeFeeds, walkRows, type FolderTree, type RowRules } from "@/lib/folderTree";
import { openListTo } from "@/lib/routes";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { cn } from "@/lib/cn";
import { RevealMore, useReveal, type Reveal } from "@/lib/reveal";
import { FavStar } from "@/ui/FavStar";
import { SavedSearchesNav } from "./SavedSearchesNav";
import { feedLabel } from "./feeds/FeedRowName";
import { FeedLine, FolderLine } from "./feeds/RowLine";
import { unreadName } from "@/ui/UnreadCount";

export const row = "flex min-h-11 items-center gap-2 rounded-lg px-3 hover:bg-selection";

/** Collapse or expand one folder; remembered on this device. */
export function toggleCollapsed(collapsed: readonly string[], id: string): string[] {
  return collapsed.includes(id) ? collapsed.filter((x) => x !== id) : [...collapsed, id];
}

/** The star that shows on hover or focus for a plain entry, and always once it is a favorite. */
const reveal = "opacity-0 group-hover/row:opacity-100 group-focus-within/row:opacity-100 [&[aria-pressed=true]]:opacity-100 [@media(pointer:coarse)]:opacity-100";

/**
 * The chevron that collapses or expands a folder; the state is per device, shared by every place the folder shows.
 * Inside the sidebar tree it is a pointer target only (`tabIndex` -1): the arrow keys expand and collapse there.
 */
export function CollapseToggle({ folder, collapsed, listId, collapsedIds, tabIndex }: { folder: Folder; collapsed: boolean; listId: string; collapsedIds: readonly string[]; tabIndex?: number }) {
  return (
    <button
      type="button"
      tabIndex={tabIndex}
      aria-expanded={!collapsed}
      aria-controls={collapsed ? undefined : listId}
      aria-label={`${collapsed ? "Expand" : "Collapse"} ${folder.name}`}
      onClick={() => updateDevicePrefs({ collapsedFolders: toggleCollapsed(collapsedIds, folder.id) })}
      className="hit-row inline-flex shrink-0 items-center justify-center rounded-lg text-fg2 hover:bg-selection"
    >
      {collapsed ? <ChevronRight className="size-4" aria-hidden="true" /> : <ChevronDown className="size-4" aria-hidden="true" />}
    </button>
  );
}

/** How far a level of the sidebar tree is indented: 12 px a level, less past the fourth so deep trees fit a phone. */
export const indentCls = (depth: number) => (depth < 4 ? "pl-3" : "pl-1");

/**
 * Keyboard for a navigation tree (WAI-ARIA tree pattern): one tab stop, Up and Down move, Right expands or goes to the
 * first child, Left collapses or goes to the parent, Home and End, Enter or Space opens the item's list (focus stays in the tree), P adds the item to
 * the favorites or takes it out. Items are the rendered `[role=treeitem]` elements, so collapsed branches are skipped
 * by construction. When the focused item leaves the screen (taken out of Favorites, collapsed away, moved or deleted
 * elsewhere) the focus goes to the item now in its place, never to the page.
 */
function useTreeKeys(collapsedIds: readonly string[], onGone?: () => void, reveal?: Reveal) {
  const [active, setActive] = useState<string | null>(null);
  const ref = useRef<HTMLUListElement>(null);
  const hadFocus = useRef(false);
  const lastIndex = useRef(0);
  const items = () => [...(ref.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])];
  // After every render, not on a dependency: what removes the focused item (a favorite taken out, a folder collapsed,
  // moved or deleted) is any change to what the tree shows. It settles at once: `active` is then on screen again.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    const ul = ref.current;
    const adrift = () => !document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected;
    if (!ul) {
      // The whole tree went (the last favorite taken out) while it had the focus: the caller says where it goes.
      if (hadFocus.current && adrift()) onGone?.();
      hadFocus.current = false;
      return;
    }
    if (!hadFocus.current || !active) return;
    if (ul.querySelector(`[data-tree-key="${CSS.escape(active)}"]`)) return;
    if (!adrift()) return;
    const list = items();
    const to = list[Math.min(lastIndex.current, list.length - 1)];
    if (to) {
      setActive(to.dataset.treeKey ?? null);
      to.focus();
    }
  });
  const onKeyDown = (e: KeyboardEvent<HTMLUListElement>) => {
    const item = e.target as HTMLElement;
    if (item.getAttribute("role") !== "treeitem" || e.altKey || e.ctrlKey || e.metaKey) return;
    const list = items();
    const at = list.indexOf(item);
    const level = Number(item.getAttribute("aria-level"));
    const expanded = item.getAttribute("aria-expanded");
    const folder = item.dataset.folderId;
    let to: HTMLElement | undefined;
    switch (e.key) {
      case "ArrowDown":
        to = list[at + 1];
        // Past the drawn rows: the reveal draws the next step and focuses the first row it adds, asked for by its key.
        if (!to && reveal && reveal.left > 0) reveal.more(true);
        break;
      case "ArrowUp":
        to = list[at - 1];
        break;
      case "Home":
        to = list[0];
        break;
      case "End":
        if (reveal && reveal.left > 0) reveal.all(true);
        else to = list.at(-1);
        break;
      case "ArrowRight":
        if (expanded === "false" && folder) updateDevicePrefs({ collapsedFolders: toggleCollapsed(collapsedIds, folder) });
        else if (expanded === "true" && Number(list[at + 1]?.getAttribute("aria-level")) === level + 1) to = list[at + 1];
        break;
      case "ArrowLeft":
        if (expanded === "true" && folder) updateDevicePrefs({ collapsedFolders: toggleCollapsed(collapsedIds, folder) });
        else to = list.slice(0, at).findLast((x) => Number(x.getAttribute("aria-level")) === level - 1);
        break;
      case "Enter":
      case " ": {
        // A tree item is data-keep-focus: opening its list leaves focus on it, so the next feed is one arrow away.
        item.querySelector<HTMLAnchorElement>("a")?.click();
        break;
      }
      default:
        return;
    }
    // Handled here, so the browser's own key action (scrolling, following the link) does not also run.
    e.preventDefault();
    if (to) {
      setActive(to.dataset.treeKey ?? null);
      to.focus();
    }
  };
  const onFocus = (e: React.FocusEvent<HTMLUListElement>) => {
    const el = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]');
    if (!el?.dataset.treeKey) return;
    hadFocus.current = true;
    lastIndex.current = Math.max(0, items().indexOf(el));
    setActive(el.dataset.treeKey);
  };
  const onBlur = (e: React.FocusEvent<HTMLUListElement>) => {
    // Focus moved somewhere else on purpose. (An item that is removed while focused blurs with no related target.)
    if (e.relatedTarget && !e.currentTarget.contains(e.relatedTarget as Node)) hadFocus.current = false;
  };
  /** The item that holds the tab stop: the last focused one while it is on screen and drawn, else the first. */
  const tabStop = (has: (key: string) => boolean, first: string) => (active && has(active) && (!reveal || reveal.shown(active)) ? active : first);
  return { ref, tabStop, onKeyDown, onFocus, onBlur };
}

interface TreeCtx {
  tree: FolderTree;
  /** Visible feeds by folder, in order. */
  feedsOf: (folder: string) => Feed[];
  /** Whether a folder's subtree holds a visible feed (only those folders show). */
  shown: (folder: string) => boolean;
  collapsedIds: readonly string[];
  /** The device setting that decides what the unread badge shows, and so what the item's name says. */
  unreadBadge: UnreadBadge;
  favs: ReturnType<typeof useFavorites>;
  onNavigate?: () => void;
  /** The prefix of keys and element ids under one root (a favorite repeats folders and feeds the tree also shows). */
  prefix: string;
  /** The key of the item that holds the tab stop. */
  tabStop: string;
  /** Whether the item with this key is drawn (a long tree draws a step at a time). */
  drawn: (key: string) => boolean;
}

type Rules = Pick<TreeCtx, "tree" | "feedsOf" | "shown" | "collapsedIds">;

/** What a folder draws inside it, by one set of rules for the render and for the key list (`rowKeys`). */
const rulesOf = (ctx: Rules): RowRules => ({
  subfolders: (id) => childrenOf(ctx.tree, id).filter(ctx.shown),
  feeds: ctx.feedsOf,
  open: (id) => !ctx.collapsedIds.includes(id),
});

/** What a folder shows inside it: its shown subfolders and its feeds; none while collapsed. */
function contents(ctx: Rules, folder: string) {
  const rules = rulesOf(ctx);
  const subs = rules.subfolders(folder);
  const own = ctx.feedsOf(folder);
  return { subs, own, branch: subs.length > 0 || own.length > 0, collapsed: !rules.open(folder) };
}

/** The keys of the items the folders under one root render, in screen order (the same traversal the screen draws by). */
function rowKeys(ctx: Rules, prefix: string, roots: readonly string[], out: string[]) {
  walkRows(roots, rulesOf(ctx), (kind, id) => out.push(`${prefix}${kind}-${id}`));
}
// The li is role="presentation", not "none": WebKit only accepts presentation (besides treeitem and group) between a tree
// and its items, and otherwise drops the whole tree. The trailing space in the title keeps "Feed" and its count apart in the
// text a checker reads ("Feed 12", not "Feed12"); it is inside the title because a space in the zero-width spacer is skipped.
function FeedItem({ f, level, ctx, fav }: { f: Feed; level: number; ctx: TreeCtx; fav?: boolean }) {
  const key = `${ctx.prefix}feed-${f.id}`;
  if (!ctx.drawn(key)) return null;
  return (
    <li role="presentation">
      <div
        role="treeitem"
        data-keep-focus
        data-reveal-key={key}
        aria-level={level}
        aria-label={unreadName(feedLabel(f), f.unread, ctx.unreadBadge)}
        tabIndex={ctx.tabStop === key ? 0 : -1}
        data-tree-key={key}
        data-fav={`feed:${f.id}`}
        className="group/row flex items-center rounded-lg"
      >
        <Link tabIndex={-1} to={openListTo({ feed: f.id })} onClick={ctx.onNavigate} className={cn(row, "min-w-0 flex-1 text-sm", !fav && "pl-3")}>
          <FeedLine feed={f} />
        </Link>
        <FavStar on={ctx.favs.has("feed", f.id)} name={feedLabel(f)} onToggle={() => ctx.favs.toggle("feed", f.id)} className={fav ? undefined : reveal} tabIndex={-1} />
      </div>
    </li>
  );
}

/** A folder, then (unless collapsed) its shown subfolders and its feeds, one level deeper. */
function FolderItem({ fo, level, ctx, fav }: { fo: Folder; level: number; ctx: TreeCtx; fav?: boolean }) {
  const key = `${ctx.prefix}folder-${fo.id}`;
  if (!ctx.drawn(key)) return null;
  const groupId = `${key}-group`;
  const { subs, own, branch, collapsed } = contents(ctx, fo.id);
  const name = fav ? folderPath(ctx.tree, fo.id) : fo.name;
  return (
    <li role="presentation">
      {/* The treeitem is the row alone, and the subfolders sit beside it in the group it owns: a name that held the whole subtree could not contain the row's visible text (WCAG 2.5.3). */}
      <div
        role="treeitem"
        data-keep-focus
        aria-level={level}
        aria-expanded={branch ? !collapsed : undefined}
        aria-owns={branch && !collapsed ? groupId : undefined}
        data-reveal-key={key}
        aria-label={unreadName(name, fo.unread, ctx.unreadBadge)}
        tabIndex={ctx.tabStop === key ? 0 : -1}
        data-tree-key={key}
        data-folder-id={fo.id}
        data-fav={`folder:${fo.id}`}
        className="group/row flex items-center rounded-lg"
      >
        {branch ? <CollapseToggle folder={fo} collapsed={collapsed} listId={groupId} collapsedIds={ctx.collapsedIds} tabIndex={-1} /> : null}
        <Link tabIndex={-1} to={openListTo({ folder: fo.id })} onClick={ctx.onNavigate} className={cn(row, "min-w-0 flex-1 pl-1 text-sm font-semibold")}>
          <FolderLine name={name} unread={fo.unread} icon={fav} />
        </Link>
        <FavStar on={ctx.favs.has("folder", fo.id)} name={folderPath(ctx.tree, fo.id)} onToggle={() => ctx.favs.toggle("folder", fo.id)} className={fav ? undefined : reveal} tabIndex={-1} />
      </div>
      {collapsed || !branch ? null : (
        <ul role="group" id={groupId} className={indentCls(level)}>
          {subs.map((id) => (
            <FolderItem key={id} fo={ctx.tree.byId.get(id) as Folder} level={level + 1} ctx={ctx} />
          ))}
          {own.map((f) => (
            <FeedItem key={f.id} f={f} level={level + 1} ctx={ctx} />
          ))}
        </ul>
      )}
    </li>
  );
}

/** The key prefix of a favorite's items (a favorite repeats folders and feeds the tree also shows). */
const favPrefix = (fav: { t: string; id: string }) => `fav-${fav.t}-${fav.id}-`;

/** The library the tree draws, built once per bootstrap, not on every render. */
function useLibrary(data: ReturnType<typeof useBootstrap>["data"]) {
  return useMemo(() => {
    if (!data) return null;
    const feeds = visibleFeeds(data.feeds);
    const tree = folderTree(data.folders);
    const byFolder = new Map<string, Feed[]>();
    for (const f of feeds) {
      const list = byFolder.get(f.folder_id);
      if (list) list.push(f);
      else byFolder.set(f.folder_id, [f]);
    }
    const counts = subtreeFeeds(tree, (id) => (byFolder.get(id) ?? []).map((f) => f.id));
    const shownIds = new Set(tree.preorder.filter((id) => (counts.get(id)?.length ?? 0) > 0));
    return { feeds, tree, byFolder, shownIds, feedById: new Map(feeds.map((f) => [f.id, f])) };
  }, [data]);
}

/**
 * Favorites first (folders and feeds pinned with the star), then the folder tree with its feeds. Both are navigation
 * trees: folders collapse (remembered per device) and each entry opens that scope's list in its own view (Unread unless the feed or a folder above it says otherwise).
 */
export function FeedTree({ onNavigate }: { onNavigate?: () => void }) {
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const favs = useFavorites();
  const lib = useLibrary(boot.data);
  // What the tree draws and how: built once per library and collapsed set, not on every render (a focus move re-renders).
  const base = useMemo(
    () =>
      lib && {
        tree: lib.tree,
        feedsOf: (id: string) => lib.byFolder.get(id) ?? [],
        shown: (id: string) => lib.shownIds.has(id),
        collapsedIds: dp.collapsedFolders,
        unreadBadge: dp.unreadBadge,
        favs,
        onNavigate,
      },
    [lib, dp.collapsedFolders, dp.unreadBadge, favs, onNavigate],
  );
  const top = useMemo(() => (lib ? childrenOf(lib.tree, null).filter((id) => lib.shownIds.has(id)) : []), [lib]);
  // The Feeds tree's items in screen order: a library of thousands of feeds draws them a step at a time (issue #463).
  const allVisible = useMemo(() => {
    const out: string[] = [];
    if (base) rowKeys(base, "", top, out);
    return out;
  }, [base, top]);
  const reveal = useReveal(allVisible);
  const { ref: allRef, tabStop: allTabStop, onKeyDown: allKeyDown, onFocus: allFocus, onBlur: allBlur } = useTreeKeys(dp.collapsedFolders, undefined, reveal);
  // With Favorites gone, the focus goes to the Feeds tree's tab stop.
  const toFeeds = () => allRef.current?.querySelector<HTMLElement>('[role="treeitem"][tabindex="0"]')?.focus();
  const { ref: favRef, tabStop: favTabStop, onKeyDown: favKeyDown, onFocus: favFocus, onBlur: favBlur } = useTreeKeys(dp.collapsedFolders, toFeeds);
  const favList = useMemo(
    () => (lib ? favs.favorites.filter((fav) => (fav.t === "folder" ? lib.tree.byId.has(fav.id) : lib.feedById.has(fav.id))) : []),
    [lib, favs.favorites],
  );
  const favVisible = useMemo(() => {
    const out: string[] = [];
    for (const fav of favList) {
      if (fav.t === "folder" && base) rowKeys(base, favPrefix(fav), [fav.id], out);
      else out.push(`${favPrefix(fav)}feed-${fav.id}`);
    }
    return out;
  }, [favList, base]);
  const headingId = useId();
  if (boot.isPending) return <p className="px-3 py-2 text-sm text-fg2" role="status">Loading feeds</p>;
  if (boot.isError || !lib) return <p className="px-3 py-2 text-sm text-danger" role="alert">Couldn't load feeds.</p>;
  if (lib.feeds.length === 0) {
    return (
      <div className="px-3 py-8 text-center">
        <h2 className="text-lg font-semibold">No feeds yet</h2>
        <p className="mt-1 text-sm text-fg2">Add a feed by its address, or import an OPML file from another reader.</p>
      </div>
    );
  }
  const { tree, feedById } = lib;
  const favStop = favTabStop((k) => favVisible.includes(k), favVisible[0] ?? "");
  const allCtx: TreeCtx = { ...(base as NonNullable<typeof base>), prefix: "", tabStop: allTabStop(reveal.has, allVisible[0] ?? ""), drawn: reveal.shown };

  return (
    <>
      {favList.length > 0 ? (
        <section aria-label="Favorites" className="mb-2">
          <h2 id={`${headingId}-fav`} className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Favorites</h2>
          <ul
            ref={favRef}
            role="tree"
            aria-labelledby={`${headingId}-fav`}
            className="flex flex-col gap-1"
            onKeyDown={favKeyDown}
            onFocus={favFocus}
            onBlur={favBlur}
          >
            {favList.map((fav) => {
              const ctx: TreeCtx = { ...allCtx, prefix: favPrefix(fav), tabStop: favStop, drawn: () => true };
              if (fav.t === "folder") return <FolderItem key={`folder:${fav.id}`} fo={tree.byId.get(fav.id) as Folder} level={1} ctx={ctx} fav />;
              return <FeedItem key={`feed:${fav.id}`} f={feedById.get(fav.id) as Feed} level={1} ctx={ctx} fav />;
            })}
          </ul>
        </section>
      ) : null}
      <SavedSearchesNav onNavigate={onNavigate} />
      <h2 id={`${headingId}-feeds`} className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Feeds</h2>
      <ul
        ref={allRef}
        role="tree"
        aria-labelledby={`${headingId}-feeds`}
        className="flex flex-col gap-1"
        onKeyDown={allKeyDown}
        onFocus={allFocus}
        onBlur={allBlur}
      >
        {top.map((id) => (
          <FolderItem key={id} fo={tree.byId.get(id) as Folder} level={1} ctx={allCtx} />
        ))}
      </ul>
      <RevealMore reveal={reveal} noun="feeds" />
    </>
  );
}
