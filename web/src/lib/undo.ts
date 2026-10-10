import { createStore } from "./store";
import { announce, toast } from "@/shell/toasts";

// Undo model (docs/maintainers/ui-decisions.md, "Gestures and keys"):
// - one toast slot, 15 s, repeated same-kind actions merge into one toast;
// - a stack of 20 undoable groups, reachable with `z` for 60 s;
// - bulk batches (mark above/below, Shift+A) coalesce with nothing and stay undoable for 2 minutes;
// - an action takes its place on the stack when the user acts (`beginUndo`), not when the server answers, so the
//   stack is in the order the user acted. Until its request settles the group is in flight and shows no toast (its
//   15 s start when it settles). `z`, the toast and the menu wait for that one group (at most UNDO_WAIT_MS, toast
//   "Undoing…", further presses ignored); if it comes to nothing they say "Nothing to undo" instead of undoing an
//   older one, and give the older toast back. docs/design.md §7.11.

export type UndoKind = "read" | "unread" | "star" | "unstar";

export const TOAST_MS = 15_000;
export const STACK_MAX = 20;
export const STACK_TTL_MS = 60_000;
export const BULK_TTL_MS = 120_000;
/** How long an undo waits for its action's request before giving up (the request itself is not cancelled). */
export const UNDO_WAIT_MS = 10_000;
export const NOTHING_TO_UNDO = "Nothing to undo";
export const STILL_SAVING = "Still saving. Try Undo again in a moment.";

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
  /** The toast this group took over when it started, shown again if the group comes to nothing. */
  before: Group | null;
}

export interface UndoView {
  /** `busy`: an undo is waiting for its action's request; the toast says so and its button does nothing. */
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
let shownId: number | null = null;
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

/** The toast's group while it has something to show: a group still in flight shows nothing yet. */
const visible = (): Group | null => (toastGroup && toastGroup.ids.length > 0 ? toastGroup : null);

function publish(): void {
  prune();
  const shown = visible();
  const view = waiting
    ? { id: waiting.id, text: "Undoing…", busy: true as const }
    : shown
      ? { id: shown.id, text: undoText(shown.kind, shown.ids.length, shown.bulk) }
      : null;
  // A hovered or focused toast that went away, or was replaced, no longer holds the clock.
  if ((view?.id ?? null) !== shownId) held = false;
  shownId = view?.id ?? null;
  undoStore.set({ toast: view, canUndo: stack.length > 0 });
}

/**
 * (Re)start the 15 s clock of the toast on screen. A toast not yet shown (in flight) has no clock, and the clock is
 * paused while it is held or while "Undoing…" stands in for it.
 */
function armTimer(): void {
  if (timer) clearTimeout(timer);
  timer = undefined;
  if (!visible() || held || waiting) return;
  timer = setTimeout(() => {
    timer = undefined;
    toastGroup = null;
    publish();
  }, TOAST_MS);
}

function newGroup(kind: UndoKind, bulk: boolean): Group {
  // The toast it takes over, shown or still in flight, so it can be given back.
  const g: Group = { id: ++seq, kind, ids: [], bulk, at: Date.now(), undo: null, restores: [], inflight: 0, waiters: [], before: toastGroup };
  stack.push(g);
  toastGroup = g;
  return g;
}

function addChange(g: Group, change: UndoChange, onStack: boolean): void {
  for (const id of change.ids) if (!g.ids.includes(id)) g.ids.push(id);
  // Merged actions share the first closure, so it must act on the ids it is handed.
  g.undo ??= change.undo;
  if (change.restore) g.restores.push(change.restore);
  if (onStack) g.at = Date.now(); // a group off the stack does not get a new window
}

/** The toast `g` took over, if that is still undoable. */
const givenBack = (g: Group): Group | null => (g.before && stack.includes(g.before) && alive(g.before) ? g.before : null);

/**
 * Take an action's place on the stack now, as the user acts, and fill it in when the server answers. A same-kind
 * action joins the toast's group (one toast, one undo), as `pushUndo` always has, whether that group is shown or
 * still in flight.
 */
export function beginUndo(kind: UndoKind, opts: { bulk?: boolean } = {}): UndoTicket {
  const bulk = opts.bulk === true;
  const t = toastGroup;
  // Never join the group an undo is waiting for: that undo is for what had happened when it was pressed.
  const joins = !bulk && t && t !== waiting && !t.bulk && t.kind === kind && alive(t) && stack.includes(t);
  const g = joins ? t : newGroup(kind, bulk);
  g.inflight++;
  if (!joins) armTimer(); // the toast it replaced stops counting; a joined toast keeps its clock until this lands
  publish();
  let settled = false;
  return {
    settle(change) {
      if (settled) return;
      settled = true;
      g.inflight--;
      prune();
      // A group whose window ended or that was pushed off the stack while in flight stays off it and shows no
      // toast; a waiting undo still sees what it changed.
      const onStack = stack.includes(g);
      const landed = change !== null && change.ids.length > 0;
      if (change && landed) addChange(g, change, onStack);
      // A joined action that lands after its toast's time ran out shows it again, like any action that lands, as long
      // as nothing newer has taken the toast or the stack.
      if (landed && onStack && !toastGroup && stack[stack.length - 1] === g) toastGroup = g;
      const empty = g.inflight === 0 && g.ids.length === 0;
      if (empty) stack = stack.filter((x) => x !== g);
      if (g.inflight === 0) for (const w of g.waiters.splice(0)) w();
      // Nothing of its own to show: give back the toast it took over.
      if (toastGroup === g && (empty || !onStack)) toastGroup = givenBack(g);
      // The clock starts when the toast shows, so a slow request does not use up its 15 s.
      if (toastGroup === g || toastGroup === g.before) armTimer();
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
    // Back in its own place; a group already off the stack (pushed off while in flight) stays off it.
    if (at >= 0 && !stack.includes(g)) {
      stack.splice(Math.min(at, stack.length), 0, g);
      if (wasToast && !toastGroup) toastGroup = g;
    }
    armTimer();
    publish();
    announce("Couldn't undo. Try again."); // the failed request has shown its own error toast
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
    armTimer(); // paused while "Undoing…" stands in for the toast
    publish();
    const inTime = await settledInTime(g);
    waiting = null;
    armTimer(); // the toast the wait replaced gets a full window back, with its Undo button
    publish();
    if (!inTime) {
      toast(STILL_SAVING);
      return;
    }
    // Its ids, not its place on the stack: a group pushed off while this undo waited is still the one meant.
    if (g.ids.length === 0) {
      toast(NOTHING_TO_UNDO);
      return;
    }
  }
  await run(g);
}

/** `z`, and the Undo item in the list menu: the most recent group still inside its window. */
export async function undoLast(): Promise<boolean> {
  if (waiting) return true; // already undoing; this press is ignored
  prune();
  const g = stack[stack.length - 1];
  if (!g) {
    toast(NOTHING_TO_UNDO);
    return false;
  }
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
