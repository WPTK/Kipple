import { useState } from "react";
import { Plus, Sparkles, Upload } from "lucide-react";
import { useSetupActions } from "@/setup/actions";
import { Button } from "@/ui/button";
import { AddFeedDialog } from "./feeds/AddFeedDialog";

/**
 * The empty library's first steps. "Add your first feed" opens the Add feed dialog right here, so Cancel leaves the
 * reader where they were; a screen that opens its own dialog passes `onAdd` instead.
 */
export function FirstRun({ onAdd, onImport }: { onAdd?: () => void; onImport: () => void }) {
  const setup = useSetupActions();
  const [adding, setAdding] = useState(false);
  // The recommended feeds are step 5 of setup: opening them starts setup again at that step (it changes nothing else).
  const recommended = () => setup.restart("feeds");
  return (
    <div className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center">
      <h2 className="text-lg font-semibold">No feeds yet</h2>
      <p className="text-sm text-fg2">Add a feed by its address, import an OPML file from another reader, or start from a few recommended feeds.</p>
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
      {adding ? <AddFeedDialog onClose={() => setAdding(false)} /> : null}
    </div>
  );
}
