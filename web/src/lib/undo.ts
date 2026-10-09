import { createStore } from "./store";
import { announce } from "@/shell/toasts";

// Undo model (docs/maintainers/ui-decisions.md, "Gestures and keys"):
// - one toast slot, 15 s, repeated same-kind actions merge into one toast;
// - a stack of 20 undoable groups, reachable with `z` for 60 s;
// - bulk batches (mark above/below, Shift+A) coalesce with nothing and stay undoable for 2 minutes.

export type UndoKind = "read" | "unread" | "star" | "unstar";

export const TOAST_MS = 15_000;
export const STACK_MAX = 20;
export const STACK_TTL_MS = 60_000;
export const BULK_TTL_MS = 120_000;

export interface UndoRequest {
  kind: UndoKind;
  ids: string[];
  /** A bulk batch: its own toast text, never merged, longer window. */
  bulk?: boolean;
  /** Reverse the change for exactly these ids. Resolve `false` (or throw) when the request failed: the group is kept. */
  undo: (ids: string[]) => void | boolean | Promise<void | boolean>;
  /** UI-only reversal (for example un-hiding a swiped row). */
  restore?: () => void;
}

interface Group {
  id: number;
  kind: UndoKind;
  ids: string[];
  bulk: boolean;
  at: number;
  undo: (ids: string[]) => void | boolean | Promise<void | boolean>;
  restores: (() => void)[];
}

export interface UndoView {
  toast: { id: number; text: string } | null;
  canUndo: boolean;
}

export const undoStore = createStore<UndoView>({ toast: null, canUndo: false });

let stack: Group[] = [];
let toastGroup: Group | null = null;
let timer: ReturnType<typeof setTimeout> | undefined;
let held = false;
let seq = 0;

const ttl = (g: Group): number => (g.bulk ? BULK_TTL_MS : STACK_TTL_MS);
const alive = (g: Group): boolean => Date.now() - g.at < ttl(g);

export function undoText(kind: UndoKind, n: number, bulk: boolean): string {
  if (bulk) return `Marked ${n} as ${kind === "read" ? "read" : "unread"}`;
  const noun = `${n} articles`;
  switch (kind) {
    case "read":
      return n === 1 ? "Marked read" : `${noun} marked read`;
    case "unread":
      return n === 1 ? "Marked unread" : `${noun} marked unread`;
    case "star":
      return n === 1 ? "Starred" : `${noun} starred`;
    case "unstar":
      return n === 1 ? "Unstarred" : `${noun} unstarred`;
  }
}

function prune(): void {
  stack = stack.filter(alive).slice(-STACK_MAX);
}

function publish(): void {
  prune();
  if (!toastGroup) held = false; // a hovered or focused toast that went away no longer holds the clock
  undoStore.set({
    toast: toastGroup ? { id: toastGroup.id, text: undoText(toastGroup.kind, toastGroup.ids.length, toastGroup.bulk) } : null,
    canUndo: stack.length > 0,
  });
}

function armTimer(): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  if (!toastGroup || held) return;
  timer = setTimeout(() => {
    timer = undefined;
    toastGroup = null;
    publish();
  }, TOAST_MS);
}

/** Record an undoable action and show (or merge into) the toast. */
export function pushUndo(req: UndoRequest): void {
  const bulk = req.bulk === true;
  if (!bulk && toastGroup && !toastGroup.bulk && toastGroup.kind === req.kind && alive(toastGroup) && stack.includes(toastGroup)) {
    const g = toastGroup;
    for (const id of req.ids) if (!g.ids.includes(id)) g.ids.push(id);
    g.at = Date.now();
    if (req.restore) g.restores.push(req.restore);
  } else {
    const g: Group = { id: ++seq, kind: req.kind, ids: [...new Set(req.ids)], bulk, at: Date.now(), undo: req.undo, restores: req.restore ? [req.restore] : [] };
    stack.push(g);
    toastGroup = g;
  }
  armTimer();
  publish();
}

async function run(g: Group): Promise<void> {
  const wasToast = toastGroup === g;
  const at = stack.indexOf(g);
  stack = stack.filter((x) => x !== g);
  if (wasToast) {
    toastGroup = null;
    armTimer();
  }
  publish();
  // The UI restore goes first so a hidden row is back at once; if the request then fails the row simply
  // shows its real (read) state and the group stays undoable.
  for (const r of g.restores) r();
  const ok = await (async () => g.undo([...g.ids]))().then(
    (r) => r !== false,
    () => false,
  );
  if (!ok) {
    // The reversal did not happen: keep the group so `z` or the toast can try again, and say so.
    if (!stack.includes(g)) stack.splice(Math.min(at < 0 ? stack.length : at, stack.length), 0, g);
    if (wasToast && !toastGroup) toastGroup = g;
    armTimer();
    publish();
    announce("Couldn't undo. Try again.");
    return;
  }
  announce("Undone");
}

/** `z`, and the Undo item in the list menu: the most recent group still inside its window. */
export async function undoLast(): Promise<boolean> {
  prune();
  const g = stack[stack.length - 1];
  if (!g) return false;
  await run(g);
  return true;
}

/** The toast's Undo button: the whole coalesced group it shows. */
export async function undoToast(): Promise<void> {
  if (toastGroup) await run(toastGroup);
}

export function dismissUndoToast(): void {
  toastGroup = null;
  armTimer();
  publish();
}

/** Pause the 15 s countdown while the pointer is over the toast or its button has focus. */
export function holdUndoToast(hold: boolean): void {
  held = hold;
  armTimer();
}

export function resetUndo(): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  stack = [];
  toastGroup = null;
  held = false;
  publish();
}
