import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { itemActions } from "./itemActions";
import { resetUndo, undoLast, undoStore } from "./undo";
import type { InfiniteData } from "@tanstack/react-query";
import { keys } from "@/api/queries";
import type { ItemsPage } from "@/api/types";
import { card, json, mockFetch, pageOf } from "@/test/mockApi";

beforeEach(() => resetUndo());

describe("star undo", () => {
  it("two merged swipe-stars undo both items with one Undo", async () => {
    const { calls } = mockFetch({ "PUT /api/items/1001/star": () => json({}), "PUT /api/items/1002/star": () => json({}) });
    const act = itemActions(new QueryClient());
    await act.toggleStar(card(1), true);
    await act.toggleStar(card(2), true);
    expect(undoStore.get().toast?.text).toBe("2 articles starred");
    await undoLast();
    const undone = calls.slice(2).map((c) => `${c.url.pathname}:${JSON.parse(String(c.init?.body)).starred}`).sort();
    expect(undone).toEqual(["/api/items/1001/star:false", "/api/items/1002/star:false"]);
  });

  it("a failed undo keeps the group and does not announce Undone", async () => {
    let fail = false;
    mockFetch({ "PUT /api/items/1001/star": () => (fail ? json({ error: "x" }, 500) : json({})) });
    const act = itemActions(new QueryClient());
    await act.toggleStar(card(1), true);
    fail = true;
    await undoLast();
    expect(undoStore.get().canUndo).toBe(true);
    expect(undoStore.get().toast).not.toBeNull();
    vi.restoreAllMocks();
  });
});

describe("bulk marks reconcile the optimistic rows", () => {
  const rows = (qc: QueryClient) => qc.getQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "unread" }))!.pages.flatMap((p) => p.items);
  function seeded() {
    const qc = new QueryClient();
    qc.setQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "unread" }), { pages: [pageOf([card(1), card(2), card(3)])], pageParams: [""] });
    return qc;
  }
  const scope = { view: "unread" as const };

  it("puts back the rows the server skipped (above as_of) and unhides them", async () => {
    // Row 1003 arrived after the list loaded: the server leaves it alone.
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1001", "1002"], restored: [], count: 2, undoable: true }) });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1500", ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).map((r) => r.read)).toEqual([true, true, false]);
    expect(unhide).toHaveBeenCalledWith(["1003"]);
  });

  it("Nothing to mark reverts every optimistic row", async () => {
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: [], restored: [], count: 0, undoable: true }) });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1", ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).every((r) => !r.read)).toBe(true);
    expect(unhide).toHaveBeenCalledWith(["1001", "1002", "1003"]);
  });

  it("over the server cap (no ids listed) refetches the lists instead of guessing", async () => {
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: [], restored: [], count: 5000, undoable: false }) });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1500", ["1001"], undefined, unhide);
    expect(qc.getQueryState(keys.items(scope))?.isInvalidated).toBe(true);
    await vi.waitFor(() => expect(unhide).toHaveBeenCalledWith(["1001"]));
  });

  it("markSide reverts the skipped rows the same way", async () => {
    mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }) });
    const qc = seeded();
    await itemActions(qc).markSide(
      { scope, order: "date", side: "below", anchor: card(1), maxId: "1500" },
      ["1002", "1003"].concat(["1001"]),
    );
    expect(rows(qc).map((r) => r.read)).toEqual([true, false, false]);
  });
});
