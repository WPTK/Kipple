import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { arrivedAfter, itemActions, restoredMessage } from "./itemActions";
import { UNDO_WAIT_MS, resetUndo, undoLast, undoStore, undoToast } from "./undo";
import type { InfiniteData } from "@tanstack/react-query";
import { keys } from "@/api/queries";
import type { ItemsPage } from "@/api/types";
import { card, json, mockFetch, pageOf } from "@/test/mockApi";
import * as toasts from "@/shell/toasts";

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

describe("undo never un-mutes (the server leaves articles muted since alone)", () => {
  const body = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body)) as Record<string, unknown>;

  it("the undo of a mark read, an unstar and a bulk mark says it is an undo; the actions themselves do not", async () => {
    const { calls } = mockFetch({
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }),
      "PUT /api/items/1002/star": () => json({}),
    });
    const act = itemActions(new QueryClient());
    await act.setRead(["1001"], true, "key");
    await undoLast();
    await act.toggleStar(card(2, { starred: true }), true);
    await undoLast();
    await act.markAll({ view: "unread" }, "1001", ["1001"]);
    await undoLast();
    expect(calls.map((c) => [`${c.method} ${c.url.pathname}`, body(c).undo])).toEqual([
      ["POST /api/items/mark-read", undefined],
      ["POST /api/items/mark-read", true],
      ["PUT /api/items/1002/star", undefined],
      ["PUT /api/items/1002/star", true],
      ["POST /api/items/mark-read", undefined],
      ["POST /api/items/mark-read", true],
    ]);
  });

  it("an undo of a mark unread does not need it: marking read never un-mutes", async () => {
    const { calls } = mockFetch({ "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }) });
    const act = itemActions(new QueryClient());
    await act.setRead(["1001"], false, "key");
    await undoLast();
    expect(calls.map(body)).toEqual([expect.objectContaining({ read: false }), expect.objectContaining({ read: true })]);
    expect(calls.map((c) => body(c).undo)).toEqual([undefined, undefined]);
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
    await itemActions(qc).markAll(scope, "1002", ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).map((r) => r.read)).toEqual([true, true, false]);
    expect(unhide).toHaveBeenCalledWith(["1003"]);
  });

  /** GET /api/items?ids=: the server's current state of those rows. */
  const byIds = (read: (id: string) => boolean) => (u: URL) =>
    json(pageOf((u.searchParams.get("ids") ?? "").split(",").map((id) => card(Number(id) - 1000, { read: read(id) }))));

  it("keeps read and hidden a row the server skipped because it was already read elsewhere (at or below as_of)", async () => {
    // 1002 was read on another device after the list loaded: the server has nothing to change for it.
    const { calls } = mockFetch({
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }),
      "GET /api/items": byIds(() => true),
    });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1002", ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).map((r) => r.read)).toEqual([true, true, false]);
    expect(unhide).toHaveBeenCalledTimes(1);
    expect(unhide).toHaveBeenCalledWith(["1003"]);
    expect(calls[1]?.url.searchParams.get("ids")).toBe("1002"); // asked, not guessed
  });

  it("brings back a row at or below as_of that the server left unread (its scope differed from the list)", async () => {
    // Full text replaced 1002's content, so the search scope no longer matched it: the server did not mark it.
    mockFetch({
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }),
      "GET /api/items": byIds(() => false),
    });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1003", ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).map((r) => r.read)).toEqual([true, false, false]);
    expect(unhide).toHaveBeenCalledWith(["1002", "1003"]);
  });

  it("when the rows cannot be rechecked, reloads the lists and puts the rows back instead of guessing", async () => {
    mockFetch({
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }),
      "GET /api/items": () => json({ error: "internal" }, 500),
    });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1003", ["1001", "1002", "1003"], undefined, unhide);
    expect(qc.getQueryState(keys.items(scope))?.isInvalidated).toBe(true);
    expect(unhide).toHaveBeenCalledWith(["1002", "1003"]);
  });

  it("arrivedAfter compares ids as numbers, and an unparsable id counts as late", () => {
    expect(arrivedAfter("1000", "999")).toBe(true);
    expect(arrivedAfter("999", "1000")).toBe(false);
    expect(arrivedAfter("1000", "1000")).toBe(false);
    expect(arrivedAfter("1000", undefined)).toBe(false);
    expect(arrivedAfter("x", "1")).toBe(true);
  });

  it("Nothing to mark reverts every optimistic row", async () => {
    // A realistic bound: every row is at or below as_of, and the server changed none of them (its scope matched
    // nothing). They are still unread, so none may vanish while the app says "Nothing to mark".
    const announce = vi.spyOn(toasts, "announce");
    mockFetch({
      "POST /api/items/mark-read": () => json({ changed: [], restored: [], count: 0, undoable: true }),
      "GET /api/items": byIds(() => false),
    });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markAll(scope, "1003", ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).every((r) => !r.read)).toBe(true);
    expect(unhide).toHaveBeenCalledWith(["1001", "1002", "1003"]);
    expect(announce).toHaveBeenCalledWith("Nothing to mark");
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
      { scope, order: "date", side: "below", anchor: card(1), maxId: "1001" },
      ["1002", "1003"].concat(["1001"]),
    );
    expect(rows(qc).map((r) => r.read)).toEqual([true, false, false]);
  });

  it("markSide keeps a skipped row at or below as_of read when the server says it is read", async () => {
    mockFetch({
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [], count: 1, undoable: true }),
      "GET /api/items": byIds(() => true),
    });
    const qc = seeded();
    const unhide = vi.fn();
    await itemActions(qc).markSide({ scope, order: "date", side: "below", anchor: card(1), maxId: "1500" }, ["1001", "1002", "1003"], undefined, unhide);
    expect(rows(qc).map((r) => r.read)).toEqual([true, true, true]);
    expect(unhide).not.toHaveBeenCalled();
  });
});

describe("z while an action is still in flight", () => {
  const all = { view: "all" as const };
  const gate = () => {
    let open!: () => void;
    const p = new Promise<void>((r) => (open = r));
    return { p, open };
  };
  const tick = async (n = 20) => {
    for (let i = 0; i < n; i++) await Promise.resolve();
  };
  /** Each write as "<what> <ids or scope> <value>", in the order it was sent. */
  const asked = (calls: { method: string; url: URL; init?: RequestInit }[]) =>
    calls
      .filter((c) => c.method !== "GET")
      .map((c) => {
        const b = JSON.parse(String(c.init?.body ?? "{}"));
        return `${c.url.pathname.replace("/api/items/", "")} ${b.ids?.join(",") ?? "scope"} ${b.read ?? b.starred}`;
      });
  function seeded() {
    const qc = new QueryClient();
    qc.setQueryData<InfiniteData<ItemsPage>>(keys.items(all), { pages: [pageOf([card(1), card(2), card(3)])], pageParams: [""] });
    return qc;
  }
  const changedBoth = () => json({ changed: ["1002", "1003"], restored: [], count: 2, undoable: true });
  /** Id marks answer at once; a scope (bulk) mark waits for `bulk`, then answers `bulkReply`; a star of 1001 waits for `star`. */
  function server(bulkReply: () => Response | Promise<Response> = changedBoth, heldId?: string) {
    const bulk = gate();
    const star = gate();
    const id = gate();
    const { calls } = mockFetch({
      "POST /api/items/mark-read": async (_u, init) => {
        const b = JSON.parse(String(init?.body));
        if (!b.scope) {
          if (b.read && heldId && b.ids.includes(heldId)) await id.p;
          return json({ changed: b.ids, restored: [], count: b.ids.length });
        }
        await bulk.p;
        return bulkReply();
      },
      "PUT /api/items/1001/star": async () => {
        await star.p;
        return json({});
      },
      "GET /api/items": (u) => json(pageOf((u.searchParams.get("ids") ?? "").split(",").map((id) => card(Number(id) - 1000)))),
    });
    return { bulk, star, id, calls };
  }

  it("z during Shift+A undoes the Shift+A once it lands, not the older mark below it", async () => {
    const { bulk, calls } = server();
    const act = itemActions(seeded());
    await act.setRead(["1001"], true, "key");
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    const undoing = undoLast();
    await tick();
    bulk.open();
    await marking;
    await undoing;
    expect(asked(calls)).toEqual(["mark-read 1001 true", "mark-read scope true", "mark-read 1002,1003 false"]);
    expect(undoStore.get().canUndo).toBe(true); // the older mark is still there for the next z
  });

  it("Shift+A (slow) then m (fast) then z undoes the m, the newer action, and leaves the Shift+A", async () => {
    const { bulk, calls } = server();
    const act = itemActions(seeded());
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    await act.setRead(["1001"], true, "key");
    await undoLast();
    bulk.open();
    await marking;
    expect(asked(calls)).toEqual(["mark-read scope true", "mark-read 1001 true", "mark-read 1001 false"]);
    expect(undoStore.get().canUndo).toBe(true); // the Shift+A, now landed
  });

  it("z waits only for the action that was newest when it was pressed, not one started after", async () => {
    const { bulk, star, calls } = server();
    const act = itemActions(seeded());
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    let done = false;
    const undoing = undoLast().then(() => (done = true));
    await tick();
    const starring = act.toggleStar(card(1), true); // started after z, still in flight
    bulk.open();
    await marking;
    await vi.waitFor(() => expect(done).toBe(true));
    expect(asked(calls)).toEqual(["mark-read scope true", "1001/star scope true", "mark-read 1002,1003 false"]);
    star.open();
    await Promise.all([starring, undoing]);
  });

  it.each([
    ["fails", () => json({ error: "x" }, 500)],
    ["marks nothing", () => json({ changed: [], restored: [], count: 0, undoable: true })],
    ["is over the undo cap", () => json({ changed: [], restored: [], count: 5000, undoable: false })],
  ])("when the in-flight Shift+A %s, z undoes nothing and says so, never the older mark", async (_name, reply) => {
    const say = vi.spyOn(toasts, "toast");
    const { bulk, calls } = server(reply);
    const act = itemActions(seeded());
    await act.setRead(["1001"], true, "key");
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    const undoing = undoLast();
    await tick();
    bulk.open();
    await marking;
    await undoing;
    expect(asked(calls)).toEqual(["mark-read 1001 true", "mark-read scope true"]);
    expect(say).toHaveBeenCalledWith("Nothing to undo");
    expect(undoStore.get().canUndo).toBe(true); // the older mark is still undoable, by a deliberate second z
  });

  it("a request that never answers: z gives up after UNDO_WAIT_MS and says so; a second z meanwhile is ignored", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const say = vi.spyOn(toasts, "toast");
      const { calls } = server();
      const act = itemActions(seeded());
      await act.setRead(["1001"], true, "key");
      void act.markAll(all, "1003", ["1002", "1003"]);
      let first = false;
      let second = false;
      const z1 = undoLast().then(() => (first = true));
      await tick();
      expect(undoStore.get().toast?.text).toBe("Undoing…");
      const z2 = undoLast().then(() => (second = true));
      await tick();
      expect(second).toBe(true); // ignored at once, not queued behind the first
      expect(first).toBe(false);
      await vi.advanceTimersByTimeAsync(UNDO_WAIT_MS);
      await Promise.all([z1, z2]);
      expect(first).toBe(true);
      expect(say).toHaveBeenCalledWith("Still saving. Try Undo again in a moment.");
      expect(asked(calls)).toEqual(["mark-read 1001 true", "mark-read scope true"]); // nothing undone
    } finally {
      vi.useRealTimers();
    }
  });

  it("the toast's Undo during Shift+A undoes the Shift+A, not the older mark whose toast was showing", async () => {
    const { bulk, calls } = server();
    const act = itemActions(seeded());
    await act.setRead(["1001"], true, "key");
    expect(undoStore.get().toast?.text).toBe("Marked read");
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    expect(undoStore.get().toast).toBeNull(); // the older toast no longer offers an Undo that would skip the new action
    const undoing = undoToast();
    await tick();
    bulk.open();
    await marking;
    await undoing;
    expect(asked(calls)).toEqual(["mark-read 1001 true", "mark-read scope true", "mark-read 1002,1003 false"]);
  });

  it("a star that cannot be undone does not make Undo available while it is in flight", async () => {
    const { star } = server();
    const starring = itemActions(seeded()).toggleStar(card(1));
    await tick();
    expect(undoStore.get().canUndo).toBe(false);
    star.open();
    await starring;
    expect(undoStore.get().canUndo).toBe(false);
  });

  it("a second z while the first waits, then the mark lands: exactly one undo request", async () => {
    const { bulk, calls } = server();
    const act = itemActions(seeded());
    await act.setRead(["1001"], true, "key");
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    const z1 = undoLast();
    await tick();
    const z2 = undoLast();
    await tick();
    bulk.open();
    await Promise.all([marking, z1, z2]);
    expect(asked(calls)).toEqual(["mark-read 1001 true", "mark-read scope true", "mark-read 1002,1003 false"]);
  });

  it("m, z, m on another row while the z waits: exactly the first m is undone", async () => {
    const { id, calls } = server(changedBoth, "1001");
    const act = itemActions(seeded());
    const first = act.setRead(["1001"], true, "key"); // held
    const z = undoLast();
    await tick();
    await act.setRead(["1002"], true, "key"); // must not join the group z is waiting for
    id.open();
    await Promise.all([first, z]);
    expect(asked(calls)).toEqual(["mark-read 1001 true", "mark-read 1002 true", "mark-read 1001 false"]);
    expect(undoStore.get().toast?.text).toBe("Marked read"); // the second m, still undoable
  });

  it("a Shift+A slower than 15 s still shows its toast, for 15 s from when it lands", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { bulk } = server();
      const marking = itemActions(seeded()).markAll(all, "1003", ["1002", "1003"]);
      await vi.advanceTimersByTimeAsync(16_000);
      bulk.open();
      await marking;
      expect(undoStore.get().toast?.text).toBe("Marked 2 as read");
      await vi.advanceTimersByTimeAsync(14_000);
      expect(undoStore.get().toast?.text).toBe("Marked 2 as read");
      await vi.advanceTimersByTimeAsync(2_000);
      expect(undoStore.get().toast).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it.each([
    ["fails", () => json({ error: "x" }, 500)],
    ["marks nothing", () => json({ changed: [], restored: [], count: 0, undoable: true })],
  ])("a Shift+A that %s gives the older mark's toast back", async (_name, reply) => {
    const { bulk } = server(reply);
    const act = itemActions(seeded());
    await act.setRead(["1001"], true, "key");
    const marking = act.markAll(all, "1003", ["1002", "1003"]);
    expect(undoStore.get().toast).toBeNull();
    bulk.open();
    await marking;
    expect(undoStore.get().toast?.text).toBe("Marked read");
  });

  it("Undo is available as soon as an undoable mark starts", async () => {
    const { bulk } = server();
    const marking = itemActions(seeded()).markAll(all, "1003", ["1002", "1003"]);
    expect(undoStore.get().canUndo).toBe(true);
    bulk.open();
    await marking;
    expect(undoStore.get().canUndo).toBe(true);
  });
});

describe("restoredMessage", () => {
  it("says which restored articles are unread again and which stay read", () => {
    expect(restoredMessage(1, 1)).toBe("Restored. It is unread again.");
    expect(restoredMessage(1, 0)).toBe("Restored. It stays read, as it was before it was muted.");
    expect(restoredMessage(3, 3)).toBe("Restored 3 articles. They are unread again.");
    expect(restoredMessage(3, 0)).toBe("Restored 3 articles. They stay read, as they were before they were muted.");
    expect(restoredMessage(3, 1)).toBe("Restored 3 articles. 1 of them is unread again; the others stay read, as they were before they were muted.");
    expect(restoredMessage(0, 0)).toBe("Nothing to restore");
  });
});
