import type { KeyboardEvent } from "react";
import { ROW_KEYSHORTCUTS } from "./rowTabStop";

/** The keys that move focus between rows (and Space, which opens a row's link). */
const ROW_KEYS = new Set([...ROW_KEYSHORTCUTS.split(" "), " "]);

/**
 * What the arrow keys visit on a page of stories, in the order it is drawn, which is its DOM order: its stories
 * (article ids) and the buttons it marks `data-stop-id` (the Digest's "N more" and Retry), which are not Tab stops of
 * their own.
 */
export function pageOrder(container: HTMLElement | null): string[] {
  const els = container?.querySelectorAll<HTMLElement>("article[data-item-id],[data-stop-id]") ?? [];
  return [...els].map((e) => e.dataset.itemId ?? e.dataset.stopId ?? "");
}

/** Scroll a page's story into view, as little as needed. */
export function storyIntoView(container: HTMLElement | null, id: string): void {
  container?.querySelector(`[data-item-id="${CSS.escape(id)}"],[data-stop-id="${CSS.escape(id)}"]`)?.scrollIntoView?.({ block: "nearest" });
}

/** Put keyboard focus on a row's link (or a stop's button). */
export function focusRow(container: HTMLElement | null, id: string): void {
  const link = container?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"] a,[data-stop-id="${CSS.escape(id)}"] :is(a,button)`);
  link?.focus({ preventScroll: true });
}

/**
 * ArrowUp and ArrowDown move focus between rows from any control in a row (its link, star or menu button), Home and End
 * to the first and last, PageUp and PageDown by a screenful; they only move focus. Space on a row's link opens it, as
 * Enter does (a link alone would scroll the page). A screen with many rows is one Tab stop, and these keys are how the
 * keyboard reaches the others. Taken in the capture phase so a closed menu button does not turn ArrowDown into "open the
 * menu". Left alone: text fields, an open menu button, and anything inside an open menu or dialog.
 *
 * Returns null when the key is not for the list. Otherwise the key was taken, and `to` is the id to move focus to (undefined
 * when focus stays: Space, or already at the end).
 */
export function rowKeyMove(e: KeyboardEvent, order: () => string[], pageRows: (row: HTMLElement) => number): { to: string | undefined } | null {
  if (!ROW_KEYS.has(e.key)) return null;
  if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isDefaultPrevented()) return null;
  const target = e.target instanceof HTMLElement ? e.target : null;
  const row = target?.closest<HTMLElement>("[data-item-id],[data-stop-id]") ?? null;
  if (!target || !row) return null;
  if (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName)) return null;
  if (target.getAttribute("aria-expanded") === "true") return null;
  if (target.closest('[role="menu"],[role="dialog"]')) return null;
  if (e.key === " ") {
    if (target.tagName !== "A") return null; // a row's buttons take Space themselves
    e.preventDefault();
    target.click();
    return { to: undefined };
  }
  const ids = order();
  const at = ids.indexOf(row.dataset.itemId ?? row.dataset.stopId ?? "");
  const last = ids.length - 1;
  const page = pageRows(row);
  const to =
    e.key === "ArrowDown" ? at + 1
    : e.key === "ArrowUp" ? at - 1
    : e.key === "Home" ? 0
    : e.key === "End" ? last
    : Math.min(last, Math.max(0, at + (e.key === "PageDown" ? page : -page)));
  e.preventDefault();
  e.stopPropagation();
  return { to: to === at ? undefined : ids[to] };
}
