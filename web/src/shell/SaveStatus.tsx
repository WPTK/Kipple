import { retrySave, syncStore } from "@/lib/deviceSync";
import { useStore } from "@/lib/store";

/**
 * A small notice when a device setting could not be saved to the server. The setting keeps its value on this
 * device; Retry sends it again (it is also retried when the network returns and on the next change). Silent
 * otherwise. The announcement itself is made by the sync engine through the polite live region.
 */
export function DeviceSaveStatus() {
  const s = useStore(syncStore);
  if (s.status !== "error" && s.refused === 0) return null;
  return (
    <div
      data-testid="save-status"
      className="pointer-events-auto fixed top-[calc(env(safe-area-inset-top)+0.5rem)] right-3 z-50 flex max-w-[calc(100vw-1.5rem)] items-center gap-2 rounded-full border border-line bg-surface py-0.5 pr-1 pl-3 text-xs text-fg shadow-md"
    >
      <span>{s.status === "error" ? "Couldn't save your settings" : `${s.refused} setting${s.refused === 1 ? "" : "s"} not saved`}</span>
      <button type="button" onClick={() => void retrySave()} className="hit inline-flex items-center rounded-full px-2 font-bold underline underline-offset-2">
        Retry
      </button>
    </div>
  );
}
