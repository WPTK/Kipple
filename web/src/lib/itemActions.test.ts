import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { itemActions } from "./itemActions";
import { resetUndo, undoLast, undoStore } from "./undo";
import { card, json, mockFetch } from "@/test/mockApi";

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
