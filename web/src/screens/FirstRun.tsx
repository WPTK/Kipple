import { Plus, Upload } from "lucide-react";
import { Button } from "@/ui/button";

export function FirstRun({ onAdd, onImport }: { onAdd: () => void; onImport: () => void }) {
  return (
    <div role="status" className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center">
      <h2 className="text-lg font-semibold">No feeds yet</h2>
      <p className="text-sm text-fg2">Add a feed by its address, or import an OPML file from another reader.</p>
      <div className="flex flex-wrap justify-center gap-2">
        <Button variant="solid" onClick={onAdd}>
          <Plus aria-hidden="true" />
          Add your first feed
        </Button>
        <Button onClick={onImport}>
          <Upload aria-hidden="true" />
          Import OPML
        </Button>
      </div>
    </div>
  );
}
