import { Link } from "react-router";
import { ChevronDown, ChevronRight, Folder as FolderIcon } from "lucide-react";
import { useBootstrap } from "@/api/queries";
import type { Feed, Folder } from "@/api/types";
import { updateDevicePrefs, useDevicePrefs } from "@/lib/devicePrefs";
import { useFavorites } from "@/lib/favorites";
import { listTo } from "@/lib/routes";
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

/** The chevron that collapses or expands a folder's feeds; the state is per device, shared by every place the folder shows. */
function CollapseToggle({ folder, collapsed, listId, collapsedIds }: { folder: Folder; collapsed: boolean; listId: string; collapsedIds: readonly string[] }) {
  return (
    <button
      type="button"
      aria-expanded={!collapsed}
      aria-controls={listId}
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

/**
 * Favorites first (folders and feeds pinned with the star), then every folder with its feeds. Folders
 * collapse (remembered per device); each entry links to that scope's unread list.
 */
export function FeedTree({ onNavigate }: { onNavigate?: () => void }) {
  const boot = useBootstrap();
  const dp = useDevicePrefs();
  const favs = useFavorites();
  if (boot.isPending) return <p className="px-3 py-2 text-sm text-fg2" role="status">Loading feeds</p>;
  if (boot.isError || !boot.data) return <p className="px-3 py-2 text-sm text-danger" role="alert">Couldn't load feeds.</p>;
  const { folders, feeds } = boot.data;
  if (feeds.length === 0) {
    return (
      <div role="status" className="px-3 py-8 text-center">
        <h2 className="text-lg font-semibold">No feeds yet</h2>
        <p className="mt-1 text-sm text-fg2">Add a feed by its address, or import an OPML file from another reader.</p>
      </div>
    );
  }
  const folderById = new Map<string, Folder>(folders.map((f) => [f.id, f]));
  const feedById = new Map<string, Feed>(feeds.map((f) => [f.id, f]));

  const feedRow = (f: Feed) => (
    <li key={f.id} className="group/row flex items-center">
      <Link to={listTo({ view: "unread", feed: f.id })} onClick={onNavigate} className={`${row} min-w-0 flex-1 pl-6 text-sm`}>
        <FeedIcon feed={f} />
        <span className="truncate">{f.title}</span>
        <span className="ml-auto" />
        <UnreadCount n={f.unread} />
      </Link>
      <FavStar on={favs.has("feed", f.id)} name={f.title} onToggle={() => favs.toggle("feed", f.id)} className={reveal} />
    </li>
  );

  const favoriteRows = favs.favorites.map((fav) => {
    if (fav.t === "folder") {
      const fo = folderById.get(fav.id);
      if (!fo) return null;
      const inFolder = feeds.filter((f) => f.folder_id === fo.id);
      const collapsed = dp.collapsedFolders.includes(fo.id);
      const listId = `fav-folder-${fo.id}-feeds`;
      return (
        <li key={`folder:${fo.id}`}>
          <div className="group/row flex items-center">
            {inFolder.length > 0 ? <CollapseToggle folder={fo} collapsed={collapsed} listId={listId} collapsedIds={dp.collapsedFolders} /> : null}
            <Link to={listTo({ view: "unread", folder: fo.id })} onClick={onNavigate} className={`${row} min-w-0 flex-1 text-sm font-semibold`}>
              <FolderIcon aria-hidden="true" className="size-4 shrink-0 text-fg2" />
              <span className="truncate">{fo.name}</span>
              <span className="ml-auto" />
              <UnreadCount n={fo.unread} />
            </Link>
            <FavStar on name={fo.name} onToggle={() => favs.toggle("folder", fo.id)} className={reveal} />
          </div>
          {collapsed || inFolder.length === 0 ? null : <ul id={listId}>{inFolder.map(feedRow)}</ul>}
        </li>
      );
    }
    const f = feedById.get(fav.id);
    if (!f) return null;
    return (
      <li key={`feed:${f.id}`} className="group/row flex items-center">
        <Link to={listTo({ view: "unread", feed: f.id })} onClick={onNavigate} className={`${row} min-w-0 flex-1 text-sm`}>
          <FeedIcon feed={f} />
          <span className="truncate">{f.title}</span>
          <span className="ml-auto" />
          <UnreadCount n={f.unread} />
        </Link>
        <FavStar on name={f.title} onToggle={() => favs.toggle("feed", f.id)} className={reveal} />
      </li>
    );
  });

  return (
    <>
      {favs.favorites.length > 0 ? (
        <section aria-label="Favorites" className="mb-2">
          <h2 className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Favorites</h2>
          <ul className="flex flex-col gap-1">{favoriteRows}</ul>
        </section>
      ) : null}
      <SavedSearchesNav onNavigate={onNavigate} />
      <h2 className="mt-3 px-3 text-xs font-semibold tracking-wide text-fg2 uppercase">Feeds</h2>
      <ul className="flex flex-col gap-1">
        {folders.map((fo) => {
          const inFolder = feeds.filter((f) => f.folder_id === fo.id);
          if (inFolder.length === 0) return null;
          const collapsed = dp.collapsedFolders.includes(fo.id);
          const listId = `folder-${fo.id}-feeds`;
          return (
            <li key={fo.id}>
              <div className="group/row flex items-center">
                <CollapseToggle folder={fo} collapsed={collapsed} listId={listId} collapsedIds={dp.collapsedFolders} />
                <Link to={listTo({ view: "unread", folder: fo.id })} onClick={onNavigate} className={cn(row, "min-w-0 flex-1 pl-1 text-sm font-semibold")}>
                  <span className="truncate">{fo.name}</span>
                  <span className="ml-auto" />
                  {/* A collapsed folder still shows what is inside it. */}
                  <UnreadCount n={fo.unread} />
                </Link>
                <FavStar on={favs.has("folder", fo.id)} name={fo.name} onToggle={() => favs.toggle("folder", fo.id)} className={reveal} />
              </div>
              {collapsed ? null : (
                <ul id={listId}>{inFolder.map(feedRow)}</ul>
              )}
            </li>
          );
        })}
      </ul>
    </>
  );
}
