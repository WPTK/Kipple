import { useState } from "react";
import { Plus, Sparkles, Upload } from "lucide-react";
import { errorMessage } from "@/api/client";
import { useSetupActions } from "@/setup/actions";
import { toast } from "@/shell/toasts";
import { Button } from "@/ui/button";

export function FirstRun({ onAdd, onImport }: { onAdd: () => void; onImport: () => void }) {
  const setup = useSetupActions();
  const [busy, setBusy] = useState(false);
  // The recommended feeds are step 5 of setup: opening them starts setup again at that step (it changes nothing else).
  const recommended = async () => {
    setBusy(true);
    try {
      await setup.restart("feeds");
    } catch (e) {
      toast(errorMessage(e), "error");
      setBusy(false);
    }
  };
  return (
    <div role="status" className="mx-auto flex max-w-sm flex-col items-center gap-3 px-6 py-16 text-center">
      <h2 className="text-lg font-semibold">No feeds yet</h2>
      <p className="text-sm text-fg2">Add a feed by its address, import an OPML file from another reader, or start from a few recommended feeds.</p>
      <div className="flex flex-wrap justify-center gap-2">
        <Button variant="solid" onClick={onAdd}>
          <Plus aria-hidden="true" />
          Add your first feed
        </Button>
        <Button onClick={onImport}>
          <Upload aria-hidden="true" />
          Import OPML
        </Button>
        <Button disabled={busy} onClick={() => void recommended()}>
          <Sparkles aria-hidden="true" />
          Recommended feeds
        </Button>
      </div>
    </div>
  );
}
