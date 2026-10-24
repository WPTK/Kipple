import { useState } from "react";
import { Plus, Sparkles, Upload } from "lucide-react";
import { useSetupActions } from "@/setup/actions";
import { Button } from "@/ui/button";
import { EmptyState } from "@/ui/EmptyState";
import { AddFeedDialog } from "./feeds/AddFeedDialog";

/**
 * The one welcome for a library with no feeds: wherever the list is empty for that reason, this is what shows. "Add your
 * first feed" opens the Add feed dialog right here, so Cancel leaves the reader where they were; a screen that opens its
 * own dialog passes `onAdd` instead.
 */
export function FirstRun({ onAdd, onImport }: { onAdd?: () => void; onImport: () => void }) {
  const [adding, setAdding] = useState(false);
  const setup = useSetupActions();
  // The recommended feeds are step 5 of setup: opening them starts setup again at that step (it changes nothing else).
  const recommended = () => setup.restart("feeds");
  return (
    <>
      <EmptyState
        title="No feeds yet"
        why="Add a feed by its address, import an OPML file from another reader, or start from a few recommended feeds."
        action={
          <div className="flex flex-wrap justify-center gap-2">
            <Button variant="solid" onClick={onAdd ?? (() => setAdding(true))}>
              <Plus aria-hidden="true" />
              Add your first feed
            </Button>
            <Button onClick={onImport}>
              <Upload aria-hidden="true" />
              Import OPML
            </Button>
            <Button onClick={recommended}>
              <Sparkles aria-hidden="true" />
              Recommended feeds
            </Button>
          </div>
        }
      />
      {adding ? <AddFeedDialog onClose={() => setAdding(false)} /> : null}
    </>
  );
}
