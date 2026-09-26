// Multi-select for Manage feeds: pure helpers so the rules (click, shift-click range, select all in a folder,
// the tri-state of a folder checkbox) are tested without a DOM.

/** Toggle one id in a set. */
export function toggleIn(sel: ReadonlySet<string>, id: string): Set<string> {
  const n = new Set(sel);
  if (n.has(id)) n.delete(id);
  else n.add(id);
  return n;
}

/** The ids from `a` to `b` inclusive, in list order (either may come first); empty when either is unknown. */
export function rangeBetween(order: readonly string[], a: string, b: string): string[] {
  const i = order.indexOf(a);
  const j = order.indexOf(b);
  if (i < 0 || j < 0) return [];
  return order.slice(Math.min(i, j), Math.max(i, j) + 1);
}

/**
 * A click on the row `id`. Plain: toggle it and make it the anchor. Shift with an anchor: every row between the
 * anchor and `id` takes the state the anchor row has now (so shift-clicking after ticking a row ticks the range,
 * and after unticking one clears it).
 */
export function clickRow(
  order: readonly string[],
  sel: ReadonlySet<string>,
  anchor: string | null,
  id: string,
  shift: boolean,
): { sel: Set<string>; anchor: string } {
  if (shift && anchor && anchor !== id && order.includes(anchor)) {
    const on = sel.has(anchor);
    const n = new Set(sel);
    for (const x of rangeBetween(order, anchor, id)) {
      if (on) n.add(x);
      else n.delete(x);
    }
    return { sel: n, anchor: id };
  }
  return { sel: toggleIn(sel, id), anchor: id };
}

/** "all" when every id is selected, "some" for a partial selection, "none" otherwise. */
export function groupState(ids: readonly string[], sel: ReadonlySet<string>): "none" | "some" | "all" {
  if (ids.length === 0) return "none";
  const n = ids.filter((i) => sel.has(i)).length;
  return n === 0 ? "none" : n === ids.length ? "all" : "some";
}

/** Select all of `ids`, or clear them when they are all selected already. */
export function toggleGroup(ids: readonly string[], sel: ReadonlySet<string>): Set<string> {
  const n = new Set(sel);
  if (groupState(ids, sel) === "all") for (const i of ids) n.delete(i);
  else for (const i of ids) n.add(i);
  return n;
}
