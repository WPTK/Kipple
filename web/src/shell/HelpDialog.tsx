import { useId, useState } from "react";
import { Dialog } from "radix-ui";
import { KEYMAP, type KeyDoc } from "@/lib/keys";
import { createStore, useStore } from "@/lib/store";
import { Button } from "@/ui/button";
import { useReturnFocus } from "@/ui/kit";

/** Open state of the overlay, so the `?` key and the "Keyboard shortcuts" menu items share it. */
export const helpStore = createStore(false);
export const openHelp = (): void => helpStore.set(true);

const SCOPES: KeyDoc["scope"][] = ["List", "Article", "Sidebar", "Everywhere"];

/** Searchable keyboard shortcut overlay (`?`). A real dialog: focus is trapped and Esc closes it. */
export function HelpDialog() {
  const open = useStore(helpStore);
  const onOpenChange = (o: boolean) => helpStore.set(o);
  const [q, setQ] = useState("");
  const inputId = useId();
  const needle = q.trim().toLowerCase();
  const match = (k: KeyDoc) => !needle || `${k.keys} ${k.desc} ${k.scope}`.toLowerCase().includes(needle);
  const any = KEYMAP.some(match);
  const returnFocus = useReturnFocus(open);
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        if (!o) setQ("");
        onOpenChange(o);
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/50" />
        <Dialog.Content {...returnFocus} className="fixed inset-x-4 top-[8vh] z-50 mx-auto max-h-[84dvh] max-w-lg overflow-y-auto rounded-2xl border border-line bg-bg p-5 text-fg shadow-xl">
          <Dialog.Title className="text-lg font-bold">Keyboard shortcuts</Dialog.Title>
          <Dialog.Description className="mb-3 text-sm text-fg2">
            Single-key shortcuts can be turned off in Settings. None of them use Ctrl, Cmd or Alt.
          </Dialog.Description>
          <label htmlFor={inputId} className="sr-only-live">
            Search shortcuts
          </label>
          <input
            id={inputId}
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search shortcuts"
            autoComplete="off"
            className="mb-3 min-h-11 w-full rounded-lg border border-line bg-surface px-3 text-base text-fg placeholder:text-fg2"
          />
          {SCOPES.map((scope) => {
            const rows = KEYMAP.filter((k) => k.scope === scope && match(k));
            if (rows.length === 0) return null;
            return (
              <section key={scope} className="mb-3">
                <h3 className="mb-1 text-xs font-semibold tracking-wide text-fg2 uppercase">{scope}</h3>
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                  {rows.map((k) => (
                    <div key={k.keys + k.desc} className="contents">
                      <dt className="font-mono font-semibold">{k.keys}</dt>
                      <dd className="text-fg2">{k.desc}</dd>
                    </div>
                  ))}
                </dl>
              </section>
            );
          })}
          {!any ? (
            <p role="status" className="py-4 text-sm text-fg2">
              No shortcut matches "{q}".
            </p>
          ) : null}
          <Dialog.Close asChild>
            <Button className="mt-2">Close</Button>
          </Dialog.Close>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
