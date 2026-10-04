import { useId, useState, type KeyboardEvent } from "react";
import { Link } from "react-router";
import { ChevronDown, ChevronRight, Folder as FolderIcon } from "lucide-react";
import { useBootstrap } from "@/api/queries";
import type { Feed, Folder } from "@/api/types";
import { updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { useFavorites } from "@/lib/favorites";
import { childrenOf, folderPath, folderTree, subtreeOf, type FolderTree } from "@/lib/folderTree";
import { listTo } from "@/lib/routes";
import { visibleFeeds } from "@/lib/visibleFeeds";
import { cn } from "@/lib/cn";
import { FavStar } from "@/ui/FavStar";
import { SavedSearchesNav } from "./SavedSearchesNav";
import { UnreadCount } from "@/ui/UnreadCount";

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

function FeedIcon({ feed }: { feed: Feed }) {
  return feed.icon ? (
    <img src={feed.icon} alt="" width={16} height={16} loading="lazy" className="size-4 shrink-0 rounded-sm" />
  ) : (
    <span className="size-4 shrink-0" aria-hidden="true" />
  );
}

/** How far a level of the sidebar tree is indented: 12 px a level, less past the fourth so deep trees fit a phone. */
export const indentCls = (depth: number) => (depth < 4 ? "pl-3" : "pl-1");

/**
 * Keyboard for a navigation tree (WAI-ARIA tree pattern): one tab stop (the last focused item), Up and Down move,
 * Right expands or goes to the first child, Left collapses or goes to the parent, Home and End, Enter opens the
 * item's list. Items are the rendered `[role=treeitem]` elements, so collapsed branches are skipped by construction.
 */
function useTreeKeys(collapsedIds: readonly string[]) {
  const [active, setActive] = useState<string | null>(null);
  const onKeyDown = (e: KeyboardEvent<HTMLUListElement>) => {
    const item = e.target as HTMLElement;
    if (item.getAttribute("role") !== "treeitem" || e.altKey || e.ctrlKey || e.metaKey) return;
    const items = [...e.currentTarget.querySelectorAll<HTMLElement>('[role="treeitem"]')];
    const at = items.indexOf(item);
    const level = Number(item.getAttribute("aria-level"));
    const expanded = item.getAttribute("aria-expanded");
    const folder = item.dataset.folderId;
    let to: HTMLElement | undefined;
    switch (e.key) {
      case "ArrowDown":
        to = items[at + 1];
        break;
      case "ArrowUp":
        to = items[at - 1];
        break;
      case "Home":
        to = items[0];
        break;
      case "End":
        to = items.at(-1);
        break;
      case "ArrowRight":
        if (expanded === "false" && folder) updateDevicePrefs({ collapsedFolders: toggleCollapsed(collapsedIds, folder) });
        else if (expanded === "true" && Number(items[at + 1]?.getAttribute("aria-level")) === level + 1) to = items[at + 1];
        break;
      case "ArrowLeft":
        if (expanded === "true" && folder) updateDevicePrefs({ collapsedFolders: toggleCollapsed(collapsedIds, folder) });
        else to = items.slice(0, at).findLast((x) => Number(x.getAttribute("aria-level")) === level - 1);
        break;
      case "Enter":
        item.querySelector<HTMLAnchorElement>("a")?.click();
        break;
      default:
        return;
    }
    // Handled here: the app's own shortcuts (Enter opens, Home goes to the top of the list) must not see it.
    e.preventDefault();
    if (to) {
      setActive(to.dataset.treeKey ?? null);
      to.focus();
    }
  };
  const onFocus = (e: React.FocusEvent<HTMLUListElement>) => {
    const key = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]')?.dataset.treeKey;
    if (key) setActive(key);
  };
  return { active, onKeyDown, onFocus };
}

interface TreeCtx {
  tree: FolderTree;
  /** Visible feeds by folder, in order. */
  feedsOf: (folder: string) => Feed[];
  /** Whether a folder's subtree holds a visible feed (only those folders show). */
  shown: (folder: string) => boolean;
  collapsedIds: readonly string[];
  favs: ReturnType<typeof useFavorites>;
  onNavigate?: () => void;
  /** The tree's own prefix for keys and element ids (one tree per section). */
  prefix: string;
  /** The key of the item that holds the tab stop, or the first item's when none was focused yet. */
  tabStop: string;
}

function FeedItem({ f, level, ctx, fav }: { f: Feed; level: number; ctx: TreeCtx; fav?: boolean }) {
  const key = `${ctx.prefix}feed-${f.id}`;
  return (
    <li
      role="treeitem"
      aria-level={level}
      aria-labelledby={`${key}-link`}
      tabIndex={ctx.tabStop === key ? 0 : -1}
      data-tree-key={key}
      className="group/row rounded-lg focus-visible:outline-2 focus-visible:outline-accent"
    >
      <div className="flex items-center">
        <Link id={`${key}-link`} tabIndex={-1} to={listTo({ view: "unread", feed: f.id })} onClick={ctx.onNavigate} className={cn(row, "min-w-0 flex-1 text-sm", !fav && "pl-3")}>
          <FeedIcon feed={f} />
          <span className="truncate">{f.title}</span>
          <span className="ml-auto" />
          <UnreadCount n={f.unread} />
        </Link>
        <FavStar on={ctx.favs.has("feed", f.id)} name={f.title} onToggle={() => ctx.favs.toggle("feed", f.id)} className={fav ? undefined : reveal} />
      </div>
    </li>
  );
}

/** A folder, then (unless collapsed) its shown subfolders and its feeds, one level deeper. */
function FolderItem({ fo, level, ctx, fav }: { fo: Folder; level: number; ctx: TreeCtx; fav?: boolean }) {
  const key = `${ctx.prefix}folder-${fo.id}`;
  const collapsed = ctx.collapsedIds.includes(fo.id);
  const groupId = `${key}-group`;
  const subs = childrenOf(ctx.tree, fo.id).filter(ctx.shown);
  const own = ctx.feedsOf(fo.id);
  const name = fav ? folderPath(ctx.tree, fo.id) : fo.name;
  const branch = subs.length > 0 || own.length > 0;
  return (
    <li
      role="treeitem"
      aria-level={level}
      aria-expanded={branch ? !collapsed : undefined}
      aria-labelledby={`${key}-link`}
      tabIndex={ctx.tabStop === key ? 0 : -1}
      data-tree-key={key}
      data-folder-id={fo.id}
      className="rounded-lg focus-visible:outline-2 focus-visible:outline-accent"
    >
      <div className="group/row flex items-center">
        {branch ? <CollapseToggle folder={fo} collapsed={collapsed} listId={groupId} collapsedIds={ctx.collapsedIds} tabIndex={-1} /> : null}
        <Link id={`${key}-link`} tabIndex={-1} to={listTo({ view: "unread", folder: fo.id })} onClick={ctx.onNavigate} className={cn(row, "min-w-0 flex-1 pl-1 text-sm font-semibold")}>
          {fav ? <FolderIcon aria-hidden="true" className="size-4 shrink-0 text-fg2" /> : null}
          <span className="truncate">{name}</span>
          <span className="ml-auto" />
          {/* A collapsed folder still shows what is inside it. */}
          <UnreadCount n={fo.unread} />
        </Link>
        <FavStar on={ctx.favs.has("folder", fo.id)} name={folderPath(ctx.tree, fo.id)} onToggle={() => ctx.favs.toggle("folder", fo.id)} className={fav ? undefined : reveal} />
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

/**
 * Favorites first (folders and feeds pinned with the star), then the folder tree with its feeds. Both are navigation
 * trees: folders collapse (remembered per device) and each entry links to that scope's unread list.
 */
export function FeedTree({ onNavigate }: { onNavigate?: () => void }) {
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const favs = useFavorites();
  const favKeys = useTreeKeys(dp.collapsedFolders);
  const allKeys = useTreeKeys(dp.collapsedFolders);
  const headingId = useId();
  if (boot.isPending) return <p className="px-3 py-2 text-sm text-fg2" role="status">Loading feeds</p>;
  if (boot.isError || !boot.data) return <p className="px-3 py-2 text-sm text-danger" role="alert">Couldn't load feeds.</p>;
  const feeds = visibleFeeds(boot.data.feeds);
  if (feeds.length === 0) {
    return (
      <div role="status" className="px-3 py-8 text-center">
        <h2 className="text-lg font-semibold">No feeds yet</h2>
        <p className="mt-1 text-sm text-fg2">Add a feed by its address, or import an OPML file from another reader.</p>
      </div>
    );
  }
  const tree = folderTree(boot.data.folders);
  const byFolder = new Map<string, Feed[]>();
  for (const f of feeds) byFolder.set(f.folder_id, [...(byFolder.get(f.folder_id) ?? []), f]);
  const withFeeds = new Set(byFolder.keys());
  const shownIds = new Set(tree.preorder.filter((id) => [...subtreeOf(tree, id)].some((x) => withFeeds.has(x))));
  const feedById = new Map(feeds.map((f) => [f.id, f]));
  const base = { tree, feedsOf: (id: string) => byFolder.get(id) ?? [], shown: (id: string) => shownIds.has(id), collapsedIds: dp.collapsedFolders, favs, onNavigate };

  const favList = favs.favorites.filter((fav) => (fav.t === "folder" ? shownIds.has(fav.id) || tree.byId.has(fav.id) : feedById.has(fav.id)));
  const firstFav = favList[0];
  const favCtx: TreeCtx = {
    ...base,
    prefix: "fav-",
    tabStop: favKeys.active ?? (firstFav ? `fav-${firstFav.t}-${firstFav.id}` : ""),
  };
  const top = childrenOf(tree, null).filter((id) => shownIds.has(id));
  const allCtx: TreeCtx = { ...base, prefix: "", tabStop: allKeys.active ?? (top[0] ? `folder-${top[0]}` : "") };

  return (
    <>
      {favList.length > 0 ? (
        <section aria-label="Favorites" className="mb-2">
          <h2 id={`${headingId}-fav`} className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Favorites</h2>
          <ul role="tree" aria-labelledby={`${headingId}-fav`} className="flex flex-col gap-1" onKeyDown={favKeys.onKeyDown} onFocus={favKeys.onFocus}>
            {favList.map((fav) => {
              if (fav.t === "folder") {
                const fo = tree.byId.get(fav.id) as Folder;
                return <FolderItem key={`folder:${fo.id}`} fo={fo} level={1} ctx={favCtx} fav />;
              }
              return <FeedItem key={`feed:${fav.id}`} f={feedById.get(fav.id) as Feed} level={1} ctx={favCtx} fav />;
            })}
          </ul>
        </section>
      ) : null}
      <SavedSearchesNav onNavigate={onNavigate} />
      <h2 id={`${headingId}-feeds`} className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Feeds</h2>
      <ul role="tree" aria-labelledby={`${headingId}-feeds`} className="flex flex-col gap-1" onKeyDown={allKeys.onKeyDown} onFocus={allKeys.onFocus}>
        {top.map((id) => (
          <FolderItem key={id} fo={tree.byId.get(id) as Folder} level={1} ctx={allCtx} />
        ))}
      </ul>
    </>
  );
}
