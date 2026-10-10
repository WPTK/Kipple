import { createStore } from "./store";
import { announce } from "@/shell/toasts";

// Undo model (docs/maintainers/ui-decisions.md, "Gestures and keys"):
// - one toast slot, 15 s, repeated same-kind actions merge into one toast;
// - a stack of 20 undoable groups, reachable with `z` for 60 s;
// - bulk batches (mark above/below, Shift+A) coalesce with nothing and stay undoable for 2 minutes;
// - an action takes its place on the stack when the user acts (`beginUndo`), not when the server answers, so the
//   stack is in the order the user acted. Until its request settles the group is in flight: `z`, the toast and the
//   menu wait for that one group (at most UNDO_WAIT_MS), and if it comes to nothing they say so instead of undoing
//   an older one.

export type UndoKind = "read" | "unread" | "star" | "unstar";

export const TOAST_MS = 15_000;
export const STACK_MAX = 20;
export const STACK_TTL_MS = 60_000;
export const BULK_TTL_MS = 120_000;
/** How long an undo waits for its action's request before giving up (the request itself is not cancelled). */
export const UNDO_WAIT_MS = 10_000;

type UndoFn = (ids: string[]) => void | boolean | Promise<void | boolean>;

/** What an action changed, once the server has said so. */
export interface UndoChange {
  ids: string[];
  /** Reverse the change for exactly these ids. Resolve `false` (or throw) when the request failed: the group is kept. */
  undo: UndoFn;
  /** UI-only reversal (for example un-hiding a swiped row). */
  restore?: () => void;
}

export interface UndoRequest extends UndoChange {
  kind: UndoKind;
  /** A bulk batch: its own toast text, never merged, longer window. */
  bulk?: boolean;
}

/** An action's place on the stack, taken when the user acts. Settle it exactly once. */
export interface UndoTicket {
  /** The server's answer: what changed (and how to reverse it), or null when nothing undoable happened. */
  settle(change: UndoChange | null): void;
}

interface Group {
  id: number;
  kind: UndoKind;
  /** What the settled actions changed. Empty while the first is still in flight. */
  ids: string[];
  bulk: boolean;
  at: number;
  undo: UndoFn | null;
  restores: (() => void)[];
  /** Actions in this group whose request has not settled. A group with none and no ids is removed. */
  inflight: number;
  waiters: (() => void)[];
}

export interface UndoView {
  /** `busy`: an undo is waiting for its action's request; the toast says so and offers no button. */
  toast: { id: number; text: string; busy?: true } | null;
  canUndo: boolean;
}

export const undoStore = createStore<UndoView>({ toast: null, canUndo: false });

let stack: Group[] = [];
let toastGroup: Group | null = null;
/** The group an undo is waiting for; while set, further undo presses are ignored. */
let waiting: Group | null = null;
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
  const shown = toastGroup && toastGroup.ids.length > 0 ? toastGroup : null;
  undoStore.set({
    toast: waiting
      ? { id: waiting.id, text: "Undoing…", busy: true }
      : shown
        ? { id: shown.id, text: undoText(shown.kind, shown.ids.length, shown.bulk) }
        : null,
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

function newGroup(kind: UndoKind, bulk: boolean): Group {
  const g: Group = { id: ++seq, kind, ids: [], bulk, at: Date.now(), undo: null, restores: [], inflight: 0, waiters: [] };
  stack.push(g);
  toastGroup = g;
  return g;
}

function addChange(g: Group, change: UndoChange): void {
  for (const id of change.ids) if (!g.ids.includes(id)) g.ids.push(id);
  // Merged actions share the first closure, so it must act on the ids it is handed.
  g.undo ??= change.undo;
  if (change.restore) g.restores.push(change.restore);
  g.at = Date.now();
}

/**
 * Take an action's place on the stack now, as the user acts, and fill it in when the server answers. A same-kind
 * action while that kind's toast is showing joins its group (one toast, one undo), as `pushUndo` always has.
 */
export function beginUndo(kind: UndoKind, opts: { bulk?: boolean } = {}): UndoTicket {
  const bulk = opts.bulk === true;
  const t = toastGroup;
  const g = !bulk && t && !t.bulk && t.kind === kind && alive(t) && stack.includes(t) ? t : newGroup(kind, bulk);
  g.inflight++;
  armTimer();
  publish();
  let settled = false;
  return {
    settle(change) {
      if (settled) return;
      settled = true;
      g.inflight--;
      if (change && change.ids.length > 0) {
        // Undone, expired or pushed off while in flight: what this action changed is still undoable, as its own group.
        addChange(stack.includes(g) ? g : newGroup(kind, bulk), change);
      }
      if (g.inflight === 0) {
        if (g.ids.length === 0) {
          stack = stack.filter((x) => x !== g);
          if (toastGroup === g) toastGroup = null;
        }
        for (const w of g.waiters.splice(0)) w();
      }
      armTimer();
      publish();
    },
  };
}

/** Record an action that has already settled, and show (or merge into) the toast. */
export function pushUndo(req: UndoRequest): void {
  beginUndo(req.kind, { bulk: req.bulk }).settle(req);
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
  const undo = g.undo;
  const ok = await (async () => undo?.([...g.ids]))().then(
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

/** Whether `g`'s in-flight actions settled within UNDO_WAIT_MS. */
function settledInTime(g: Group): Promise<boolean> {
  if (g.inflight === 0) return Promise.resolve(true);
  return new Promise((resolve) => {
    const t = setTimeout(() => resolve(false), UNDO_WAIT_MS);
    g.waiters.push(() => {
      clearTimeout(t);
      resolve(true);
    });
  });
}

/** Undo `g`, the newest group when the user pressed: at once if it has settled, else once it does. */
async function attempt(g: Group): Promise<void> {
  if (waiting) return; // one undo waits at a time; a second press does not queue a second undo
  if (g.inflight > 0) {
    waiting = g;
    publish();
    const inTime = await settledInTime(g);
    waiting = null;
    publish();
    if (!inTime) {
      announce("Still saving. Try Undo again in a moment.");
      return;
    }
    if (!stack.includes(g)) {
      announce("Nothing to undo");
      return;
    }
  }
  await run(g);
}

/** `z`, and the Undo item in the list menu: the most recent group still inside its window. */
export async function undoLast(): Promise<boolean> {
  prune();
  const g = stack[stack.length - 1];
  if (!g) return false;
  await attempt(g);
  return true;
}

/** The toast's Undo button: the whole coalesced group it shows. */
export async function undoToast(): Promise<void> {
  if (toastGroup) await attempt(toastGroup);
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
  waiting = null;
  held = false;
  publish();
}
