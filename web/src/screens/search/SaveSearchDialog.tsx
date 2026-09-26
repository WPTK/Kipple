import { useId, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/api/client";
import { createSavedSearch, invalidateSavedSearches, patchSavedSearch } from "@/api/savedSearches";
import type { SavedSearch } from "@/api/types";
import { announce } from "@/shell/toasts";
import { Button } from "@/ui/button";
import { Modal, Notice, inputCls } from "@/ui/kit";

/** The server's own message for a refused saved search, or something plain for the two errors people can hit. */
export function savedSearchError(e: unknown): string {
  if (e instanceof ApiError) {
    const m = e.body && typeof e.body.message === "string" ? e.body.message : null;
    if (e.status === 409 && e.code === "too_many") return "You already have 100 saved searches. Delete one first.";
    if (e.status === 400 && m) return m;
    if (e.status === 404) return "That saved search no longer exists.";
    if (e.status === 0) return "Kipple couldn't reach the server.";
  }
  return "Couldn't save the search. Try again.";
}

/** The first `max` characters of `s` counted as code points (as the server counts), never half an emoji; `…` when cut. */
function cutCodePoints(s: string, max: number): string {
  const cps = Array.from(s);
  return cps.length > max ? `${cps.slice(0, max).join("")}…` : s;
}

/** The suggested name for a new saved search: the query, cut to the server's 60 characters on a code point. */
export function defaultSearchName(q: string): string {
  return Array.from(q).slice(0, 60).join("");
}

/**
 * "Save this search": a name, the current text, scope and order. When the search on screen came from a saved one
 * (`existing`), the dialog also offers to update it instead of adding another.
 */
export function SaveSearchDialog({
  open,
  onOpenChange,
  q,
  scope,
  order,
  scopeLabel,
  existing,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  q: string;
  scope: SavedSearch["scope"] | undefined;
  order: SavedSearch["order"];
  /** "the whole library", a feed's title and so on, for the summary line. */
  scopeLabel: string;
  existing?: SavedSearch;
}) {
  const qc = useQueryClient();
  const id = useId();
  const trimmed = q.trim();
  const [name, setName] = useState(existing?.name ?? defaultSearchName(trimmed));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const orderLabel = order === "rank" ? "relevance" : order === "oldest" ? "oldest first" : "newest first";

  const save = async (update: boolean) => {
    const nm = name.trim();
    if (!nm) return setError("Give it a name.");
    setBusy(true);
    setError(null);
    try {
      // A patch clears a scope with null; a create just leaves it out (the whole library).
      if (update && existing) await patchSavedSearch(existing.id, { name: nm, q: trimmed, scope: scope ?? null, order });
      else await createSavedSearch({ name: nm, q: trimmed, ...(scope ? { scope } : {}), order });
      invalidateSavedSearches(qc);
      announce(update ? `Saved search ${nm} updated` : `Saved search ${nm} added`);
      onOpenChange(false);
    } catch (e) {
      setError(savedSearchError(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !busy && onOpenChange(o)}
      title="Save this search"
      description="It appears in the sidebar with its unread count."
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          {existing ? (
            <Button onClick={() => void save(true)} disabled={busy}>
              Update {cutCodePoints(existing.name, 24)}
            </Button>
          ) : null}
          <Button variant="solid" onClick={() => void save(false)} disabled={busy}>
            {existing ? "Save as new" : "Save"}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-1"
        onSubmit={(e) => {
          e.preventDefault();
          void save(false);
        }}
      >
        <label htmlFor={id} className="text-sm font-semibold">
          Name
        </label>
        <input id={id} value={name} maxLength={60} autoFocus onChange={(e) => setName(e.target.value)} className={inputCls} />
        <p className="text-xs text-fg2">
          Searches for <strong className="font-semibold text-fg">{trimmed}</strong> in {scopeLabel}, sorted by {orderLabel}.
        </p>
      </form>
      {error ? <Notice tone="error">{error}</Notice> : null}
    </Modal>
  );
}
