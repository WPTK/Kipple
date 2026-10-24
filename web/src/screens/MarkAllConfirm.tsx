import { useCallback, useState, type ReactNode } from "react";
import { Button } from "@/ui/button";
import { Modal } from "@/ui/kit";

/**
 * Mark all as read always asks first, naming how far it reaches (lib/itemActionTable.ts markAllLabel). `ask` opens the
 * question for what it marks (lib/itemActionTable.ts markAllReach); the dialog is `dialog`, to be rendered once by whoever offers the control.
 * Confirming runs `run`; the Undo toast that follows is the bulk mark's own.
 */
export function useMarkAllConfirm(): { ask: (label: string, what: string, run: () => void) => void; dialog: ReactNode } {
  const [pending, setPending] = useState<{ label: string; what: string; run: () => void } | null>(null);
  const ask = useCallback((label: string, what: string, run: () => void) => setPending({ label, what, run }), []);
  const dialog = pending ? (
    <Modal
      open
      onOpenChange={(o) => !o && setPending(null)}
      title={pending.label}
      description={`All ${pending.what} are marked read.`}
      footer={
        <>
          <Button onClick={() => setPending(null)}>Cancel</Button>
          <Button
            variant="solid"
            onClick={() => {
              const { run } = pending;
              setPending(null);
              run();
            }}
          >
            {pending.label}
          </Button>
        </>
      }
    />
  ) : null;
  return { ask, dialog };
}
