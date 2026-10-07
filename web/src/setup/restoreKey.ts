// The owner key of a setup-wizard restore (docs/design.md §7.1e). An uploaded backup belongs to the page that sent
// it: the page makes this random key and sends it with the upload and with every later restore call, in a header
// that only this origin's own script can set. It is kept in this origin's localStorage, so a reload or another tab
// of the same browser still owns the upload; where storage is unavailable it lives for this page only. Once read or
// made, a page keeps using the same key, so clearing storage meanwhile cannot cut it off from its own upload.

const STORAGE_KEY = "kipple.restoreKey";
/** 32 bytes as unpadded base64url, the form the server accepts (its last character carries 4 bits and 2 zero bits). */
const KEY_FORM = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/;
let memory: string | null = null;

function makeKey(): string {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/** The key this browser already has, without making one: "" when there is none (or the stored one is malformed). */
function existingKey(): string {
  if (memory) return memory;
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && KEY_FORM.test(stored)) memory = stored;
  } catch {
    /* storage blocked */
  }
  return memory ?? "";
}

/** This browser's owner key, made on first need. */
export function restoreKey(): string {
  const k = existingKey();
  if (k) return k;
  memory = makeKey();
  try {
    localStorage.setItem(STORAGE_KEY, memory);
  } catch {
    /* storage blocked: it lives in memory for this page */
  }
  return memory;
}

/** The header that carries the owner key, made if this browser has none yet. */
export const restoreKeyHeaders = (): Record<string, string> => ({ "X-Kipple-Restore-Key": restoreKey() });

/**
 * The header with the key this browser already has, for GET /api/instance: a page that has never restored makes no
 * key just to load, and without one the server reports no restore for it, which is true.
 */
export function existingRestoreKeyHeaders(): Record<string, string> {
  const k = existingKey();
  return k ? { "X-Kipple-Restore-Key": k } : {};
}

/** Forgets the key held in memory (tests only). */
export function forgetRestoreKeyForTests(): void {
  memory = null;
}
