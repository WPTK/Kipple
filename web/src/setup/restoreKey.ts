// The owner key of a setup-wizard restore (docs/design.md §7.1e). An uploaded backup belongs to the page that sent
// it: the page makes this random key and sends it with the upload and with every later restore call, in a header
// that only this origin's own script can set. It is kept in this origin's localStorage, so a reload or another tab
// of the same browser still owns the upload; where storage is unavailable it lives for this page only.

const STORAGE_KEY = "kipple.restoreKey";
let memory: string | null = null;

function makeKey(): string {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/** This browser's owner key, made on first use. */
export function restoreKey(): string {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored) return stored;
  } catch {
    /* storage blocked: keep it in memory */
  }
  memory ??= makeKey();
  try {
    localStorage.setItem(STORAGE_KEY, memory);
  } catch {
    /* storage blocked */
  }
  return memory;
}

/** The header that carries the owner key. */
export const restoreKeyHeaders = (): Record<string, string> => ({ "X-Kipple-Restore-Key": restoreKey() });
