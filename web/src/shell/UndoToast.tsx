import { holdUndoToast, undoStore, undoToast } from "@/lib/undo";
import { useStore } from "@/lib/store";

/**
 * The undo toast: one slot, 15 s, merged text ("4 articles marked read"). The wrapper is a
 * persistent role=status region so a screen reader announces each new message; the Undo button
 * is a real, focusable button and does not steal focus when it appears. The countdown pauses
 * while the pointer is over it or the button has focus.
 */
export function UndoToast() {
  const { toast } = useStore(undoStore);
  return (
    <div role="status" aria-live="polite" aria-atomic="true" data-testid="undo-region" className="contents">
      {toast ? (
        <div
          key={toast.id}
          className="pointer-events-auto flex min-h-11 w-full max-w-md items-center gap-3 rounded-xl border border-line bg-surface px-4 py-2 text-sm text-fg shadow-lg"
          onPointerEnter={() => holdUndoToast(true)}
          onPointerLeave={() => holdUndoToast(false)}
          onFocus={() => holdUndoToast(true)}
          onBlur={() => holdUndoToast(false)}
        >
          <span className="flex-1">{toast.text}</span>
          <button
            type="button"
            className="hit inline-flex items-center justify-center rounded-md px-2 font-semibold text-link underline"
            onClick={() => void undoToast()}
          >
            Undo
          </button>
        </div>
      ) : null}
    </div>
  );
}
