import { Link } from "react-router";
import { useBootstrap } from "@/api/queries";
import { listTo } from "@/lib/routes";

function Badge({ n }: { n: number }) {
  if (n <= 0) return null;
  return (
    <span className="ml-auto shrink-0 rounded-full bg-surface px-2 py-0.5 text-xs font-semibold text-fg2 tabular-nums">
      <span className="sr-only-live">Unread </span>
      {n > 9999 ? "9999+" : n}
    </span>
  );
}

export const row = "flex min-h-11 items-center gap-2 rounded-lg px-3 hover:bg-selection";

/** Folders and their feeds, each linking to that scope's unread list. */
export function FeedTree({ onNavigate }: { onNavigate?: () => void }) {
  const boot = useBootstrap();
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
  return (
    <ul className="flex flex-col gap-1">
      {folders.map((fo) => {
        const inFolder = feeds.filter((f) => f.folder_id === fo.id);
        if (inFolder.length === 0) return null;
        return (
          <li key={fo.id}>
            <Link to={listTo({ view: "unread", folder: fo.id })} onClick={onNavigate} className={`${row} text-sm font-semibold`}>
              <span className="truncate">{fo.name}</span>
              <Badge n={fo.unread} />
            </Link>
            <ul>
              {inFolder.map((f) => (
                <li key={f.id}>
                  <Link to={listTo({ view: "unread", feed: f.id })} onClick={onNavigate} className={`${row} pl-6 text-sm`}>
                    {f.icon ? <img src={f.icon} alt="" width={16} height={16} loading="lazy" className="size-4 shrink-0 rounded-sm" /> : <span className="size-4 shrink-0" aria-hidden="true" />}
                    <span className="truncate">{f.title}</span>
                    <Badge n={f.unread} />
                  </Link>
                </li>
              ))}
            </ul>
          </li>
        );
      })}
    </ul>
  );
}
