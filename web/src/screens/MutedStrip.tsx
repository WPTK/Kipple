import type { Card } from "@/api/types";
import { useRowTabIndex } from "@/layouts/rowTabStop";
import { Button } from "@/ui/button";

/** "Muted by <rule>" under a row of the Muted list, with the two things you can do about it. */
export function MutedStrip({ item, onRestore, onEditRule }: { item: Card; onRestore: () => void; onEditRule: () => void }) {
  const name = item.muted_by_name;
  const tabIndex = useRowTabIndex();
  return (
    <div data-testid="muted-strip" data-muted-for={item.id} className="flex flex-wrap items-center gap-x-3 border-b border-line bg-surface px-4 text-xs">
      <span className="min-w-0 flex-1 py-1 text-fg2">
        {name ? (
          <>
            Muted by <strong className="font-semibold text-fg">{name}</strong>
          </>
        ) : (
          "Muted by a filter that was deleted"
        )}
      </span>
      <Button variant="ghost" tabIndex={tabIndex} onClick={onRestore} aria-label={`Restore ${item.title || "article"}`}>
        Restore
      </Button>
      {name ? (
        <Button variant="ghost" tabIndex={tabIndex} onClick={onEditRule} aria-label={`Edit the rule that muted ${item.title || "this article"}`}>
          Edit rule
        </Button>
      ) : null}
    </div>
  );
}
