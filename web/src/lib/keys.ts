import { useEffect, useRef } from "react";

// Keymap per docs/research/ui-layouts-keymap-density-round2.md section 4.
// Rules: never bind Ctrl/Cmd/Alt; single keys are off while typing, during IME
// composition and behind the global "Single-key shortcuts" setting; `g` starts
// a two-key chord that expires after 1.2 s. Bind by event.key, not code.

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
  G: "bottom",
  Home: "top",
  f: "fulltext",
  A: "markAll",
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

  if (pendingG) {
    const a = CHORD[e.key];
    return { action: a ?? null, pendingG: false };
  }
  if (e.key === "g") return { action: null, pendingG: true };
  const a = SINGLE[e.key];
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
      // A modal dialog owns the keyboard (Radix handles Esc itself).
      if (document.querySelector('[role="dialog"],[role="menu"]')) return;
      // Enter on a focused control activates that control, not the list selection.
      if (e.key === "Enter" && e.target instanceof HTMLElement && e.target.closest('a,button,summary,[role="button"],[role="link"]')) return;
      const res = interpretKey(e, pendingG, { typing: isTypingTarget(e.target), singleKeys: o.singleKeys });
      pendingG = res.pendingG;
      if (timer) clearTimeout(timer);
      if (pendingG) timer = setTimeout(() => (pendingG = false), CHORD_TIMEOUT_MS);
      if (!res.action) return;
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
  scope: "List" | "Article" | "Everywhere";
}

/** The shortcut overlay's source of truth, kept beside the maps above. */
export const KEYMAP: KeyDoc[] = [
  { keys: "j / k", desc: "Next / previous article", scope: "List" },
  { keys: "Enter", desc: "Open the selected article", scope: "List" },
  { keys: "x", desc: "Select or deselect the row (then m, s, { and } act on the selection)", scope: "List" },
  { keys: "m", desc: "Mark read or unread", scope: "List" },
  { keys: "s", desc: "Star or unstar", scope: "List" },
  { keys: "{ / }", desc: "Mark above / below as read", scope: "List" },
  { keys: "Shift+A", desc: "Mark everything in this list as read", scope: "List" },
  { keys: "g g / G", desc: "Jump to top / bottom", scope: "List" },
  { keys: "[ / ]", desc: "Previous / next feed", scope: "List" },
  { keys: "c", desc: "Switch to the Compact layout and back", scope: "List" },
  { keys: "o / v", desc: "Open the original in a new tab", scope: "Everywhere" },
  { keys: "j / k", desc: "Next / previous article", scope: "Article" },
  { keys: "m / s", desc: "Mark read or unread / star", scope: "Article" },
  { keys: "f", desc: "Toggle full text", scope: "Article" },
  { keys: "u or Esc", desc: "Back to the list", scope: "Article" },
  { keys: "r", desc: "Refresh all feeds", scope: "Everywhere" },
  { keys: "z", desc: "Undo the last action (60 seconds, 2 minutes for bulk)", scope: "Everywhere" },
  { keys: "g i / a / s / f / ,", desc: "Go to Unread / All / Starred / Feeds / Settings", scope: "Everywhere" },
  { keys: "/", desc: "Search", scope: "Everywhere" },
  { keys: "?", desc: "This overlay", scope: "Everywhere" },
];
