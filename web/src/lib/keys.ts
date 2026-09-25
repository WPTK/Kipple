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
  | "fulltext";

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
  m: "toggleRead",
  s: "star",
  r: "refresh",
  u: "up",
  Escape: "up",
  G: "bottom",
  Home: "top",
  f: "fulltext",
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
      if (document.querySelector('[role="dialog"]')) return;
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
