import { useEffect, useRef } from "react";

// Keymap per docs/research/ui-layouts-keymap-density-round2.md section 4.
// Rules: never bind Ctrl/Cmd/Alt; single keys are off while typing, during IME
// composition and behind the global "Single-key shortcuts" setting; `g` starts
// a two-key chord that expires after 1.2 s. Bind by event.key (layout-aware), not code, but read the
// Shift state from event.shiftKey: with CapsLock on, `key` for a plain "a" is "A", so the letter's case
// says nothing about Shift. Letters are matched lower-case; Shift+A and Shift+G are explicit entries.

export type Action =
  | "next"
  | "prev"
  | "open"
  | "original"
  | "toggleRead"
  | "star"
  | "refresh"
  | "up"
  | "top"
  | "bottom"
  | "goUnread"
  | "goAll"
  | "goStarred"
  | "goFeeds"
  | "goSettings"
  | "search"
  | "help"
  | "fulltext"
  | "background"
  | "markAll"
  | "markAbove"
  | "markBelow"
  | "select"
  | "undo"
  | "compact"
  | "prevFeed"
  | "nextFeed";

export const CHORD_TIMEOUT_MS = 1200;

export interface KeyLike {
  key: string;
  /** Real key events always carry it; when absent (tests), an upper-case letter counts as shifted. */
  shiftKey?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
  isComposing?: boolean;
}

/** True for inputs, textareas, selects, contenteditable and textbox-like roles. */
export function isTypingTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  if (t.isContentEditable) return true;
  const tag = t.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  const role = t.getAttribute("role");
  return role === "textbox" || role === "searchbox" || role === "combobox";
}

const SINGLE: Record<string, Action> = {
  j: "next",
  k: "prev",
  Enter: "open",
  o: "original",
  v: "background",
  m: "toggleRead",
  s: "star",
  r: "refresh",
  u: "up",
  Escape: "up",
  Home: "top",
  f: "fulltext",
  "{": "markAbove",
  "}": "markBelow",
  x: "select",
  z: "undo",
  c: "compact",
  "[": "prevFeed",
  "]": "nextFeed",
  "/": "search",
  "?": "help",
};

/** Letters that need an explicit Shift. */
const SHIFTED: Record<string, Action> = {
  g: "bottom",
  a: "markAll",
};

const CHORD: Record<string, Action> = {
  g: "top",
  i: "goUnread",
  a: "goAll",
  s: "goStarred",
  f: "goFeeds",
  ",": "goSettings",
};

/**
 * Map a key press to an action. Pure so it can be tested without a DOM.
 * `pendingG` says a `g` chord is waiting; the result carries the next value.
 * Arrow keys stay unbound so the browser keeps scrolling.
 */
export function interpretKey(
  e: KeyLike,
  pendingG: boolean,
  opts: { typing: boolean; singleKeys: boolean },
): { action: Action | null; pendingG: boolean } {
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return { action: null, pendingG: false };
  if (opts.typing) return { action: null, pendingG: false };
  // Escape and ? stay available with single-key shortcuts off (2.1.4): Esc closes, ? opens help.
  const always = e.key === "Escape" || e.key === "?";
  if (!opts.singleKeys && !always) return { action: null, pendingG: false };

  // A letter is its lower-case self plus an explicit Shift flag (CapsLock cannot fake either).
  const letter = e.key.length === 1 && /[a-z]/i.test(e.key);
  const k = letter ? e.key.toLowerCase() : e.key;
  const shift = letter && (e.shiftKey ?? e.key !== e.key.toLowerCase());

  if (pendingG) {
    const a = shift ? undefined : CHORD[k];
    return { action: a ?? null, pendingG: false };
  }
  if (k === "g" && letter && !shift) return { action: null, pendingG: true };
  const a = shift ? SHIFTED[k] : SINGLE[k];
  return { action: a ?? null, pendingG: false };
}

export type Handlers = Partial<Record<Action, () => void>>;

/** Global key handling. Only actions present in `handlers` are consumed. */
export function useHotkeys(handlers: Handlers, opts: { singleKeys: boolean; enabled?: boolean }): void {
  const ref = useRef({ handlers, opts });
  useEffect(() => {
    ref.current = { handlers, opts };
  });
  useEffect(() => {
    let pendingG = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onKey = (e: KeyboardEvent) => {
      const { handlers: h, opts: o } = ref.current;
      if (o.enabled === false) return;
      // Someone already handled this key. Radix closes a menu or dialog on Escape from a document capture
      // listener with preventDefault, and React may have removed the content before this bubble listener
      // runs, so the DOM check below alone would let the same Esc also go "back".
      if (e.defaultPrevented) return;
      // A modal dialog owns the keyboard (Radix handles Esc itself).
      if (document.querySelector('[role="dialog"],[role="menu"]')) return;
      // Enter on a focused control activates that control, not the list selection.
      if (e.key === "Enter" && e.target instanceof HTMLElement && e.target.closest('a,button,summary,[role="button"],[role="link"]')) return;
      const res = interpretKey(e, pendingG, { typing: isTypingTarget(e.target), singleKeys: o.singleKeys });
      pendingG = res.pendingG;
      if (timer) clearTimeout(timer);
      if (pendingG) timer = setTimeout(() => (pendingG = false), CHORD_TIMEOUT_MS);
      if (!res.action) return;
      // Held keys repeat only for moving through the list; a held z, m, s or A must act once.
      if (e.repeat && res.action !== "next" && res.action !== "prev") return;
      const fn = h[res.action];
      if (!fn) return;
      e.preventDefault();
      fn();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      if (timer) clearTimeout(timer);
    };
  }, []);
}

export interface KeyDoc {
  keys: string;
  desc: string;
  scope: "List" | "Article" | "Sidebar" | "Everywhere";
}

/** The shortcut overlay's source of truth, kept beside the maps above. */
export const KEYMAP: KeyDoc[] = [
  { keys: "↑ / ↓", desc: "Previous / next folder or feed (the sidebar's Favorites and Feeds trees)", scope: "Sidebar" },
  { keys: "→ / ←", desc: "Expand or step into / collapse or step out of a folder", scope: "Sidebar" },
  { keys: "Enter", desc: "Open the folder's or feed's list", scope: "Sidebar" },
  { keys: "p", desc: "Add to favorites or take out", scope: "Sidebar" },
  { keys: "j / k", desc: "Next / previous article", scope: "List" },
  { keys: "Enter", desc: "Open the selected article", scope: "List" },
  { keys: "x", desc: "Select or deselect the row (then m, s, { and } act on the selection; { and } anchor on its first and last row)", scope: "List" },
  { keys: "m", desc: "Mark read or unread", scope: "List" },
  { keys: "s", desc: "Star or unstar", scope: "List" },
  { keys: "{ / }", desc: "Mark above / below as read", scope: "List" },
  { keys: "Shift+A", desc: "Mark everything in this list as read", scope: "List" },
  { keys: "g g / Shift+G", desc: "Jump to top / bottom", scope: "List" },
  { keys: "[ / ]", desc: "Previous / next feed", scope: "List" },
  { keys: "c", desc: "Switch to the Compact layout and back", scope: "List" },
  { keys: "o / v", desc: "Open the original in a new tab", scope: "Everywhere" },
  { keys: "j / k", desc: "Next / previous article", scope: "Article" },
  { keys: "m / s", desc: "Mark read or unread / star", scope: "Article" },
  { keys: "f", desc: "Toggle full text", scope: "Article" },
  { keys: "u or Esc", desc: "Back to the list (beside the list: focus returns to it)", scope: "Article" },
  { keys: "r", desc: "Refresh all feeds", scope: "Everywhere" },
  { keys: "z", desc: "Undo the last action (60 seconds, 2 minutes for bulk)", scope: "Everywhere" },
  { keys: "g i / a / s / f / ,", desc: "Go to Unread / All / Starred / Feeds / Settings", scope: "Everywhere" },
  { keys: "/", desc: "Search", scope: "Everywhere" },
  { keys: "?", desc: "This overlay", scope: "Everywhere" },
];
