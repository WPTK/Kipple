import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import type { InfiniteData } from "@tanstack/react-query";
import { FINISHED_KEEP_MAX, FINISHED_KEEP_MS, isRefreshKind, announcementFor, applyCounts, clearPending, pendingFor, handleServerEvent, initialLive, liveStore, parseServerEvent, pollInterval, reduceEvent } from "./events";
import { keys, scopeKey } from "./queries";
import { savedSearchesKey } from "./savedSearches";
import { noteFilterTouched, resetFilterTouched } from "./filterEdits";
import type { ItemsPage, ServerEvent } from "./types";
import { bootstrap, card, detail, pageOf } from "@/test/mockApi";

beforeEach(() => liveStore.set(initialLive));

describe("reduceEvent", () => {
  it("tracks run progress and clears it on run.done", () => {
    let s = reduceEvent(initialLive, { type: "run.start", data: { run_id: "9", kind: "refresh", total: 4 } });
    expect(s.runs["9"]?.total).toBe(4);
    s = reduceEvent(s, { type: "run.progress", data: { run_id: "9", done: 2, total: 4, new_items: 5, errors: 0 } });
    expect(s.runs["9"]).toMatchObject({ done: 2, new_items: 5 });
    s = reduceEvent(s, { type: "run.done", data: { run_id: "9", new_items: 7, errors: 0 } });
    expect(s.runs).toEqual({});
  });

  it("keeps how a run ended (run.done's changed and scanned), bounded and for a few minutes", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
    let s = reduceEvent(initialLive, { type: "run.start", data: { run_id: "1", kind: "auto_read", total: 1200 } });
    s = reduceEvent(s, { type: "run.done", data: { run_id: "1", kind: "auto_read", new_items: 0, errors: 0, changed: 1200, scanned: 1200 } });
    expect(s.runs).toEqual({});
    expect(s.finished["1"]).toMatchObject({ kind: "auto_read", changed: 1200, scanned: 1200, errors: 0 });
    for (let i = 2; i < 40; i++) s = reduceEvent(s, { type: "run.done", data: { run_id: String(i), new_items: 0, errors: 0 } });
    expect(Object.keys(s.finished).length).toBeLessThanOrEqual(FINISHED_KEEP_MAX);
    vi.advanceTimersByTime(FINISHED_KEEP_MS + 1000);
    s = reduceEvent(s, { type: "run.done", data: { run_id: "99", new_items: 0, errors: 0 } });
    expect(Object.keys(s.finished)).toEqual(["99"]);
    vi.useRealTimers();
  });

  it("accumulates new items from fetch.done and clears them on resync", () => {
    let s = reduceEvent(initialLive, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 3 } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "2", outcome: "ok", new_items: 2 } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "3", outcome: "not_modified", new_items: 0 } });
    expect(s.pendingByFeed).toEqual({ "1": 3, "2": 2 });
    s = reduceEvent(s, { type: "resync", data: {} });
    expect(s).toMatchObject({ pendingByFeed: {}, pendingIds: {} });
  });

  it("tracks the new item ids so the pill can skip ones the list already has", () => {
    const s = reduceEvent(initialLive, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, new_item_ids: ["7", "8"] } });
    expect(s.pendingIds).toEqual({ "1": ["7", "8"] });
    expect(s.pendingByFeed).toEqual({ "1": 2 });
  });

  it("accepts numeric run ids from an older server", () => {
    let s = reduceEvent(initialLive, { type: "run.start", data: { run_id: 9, kind: "manual", total: 4 } });
    expect(Object.keys(s.runs)).toEqual(["9"]);
    expect(s.runs["9"]?.id).toBe("9");
    s = reduceEvent(s, { type: "run.progress", data: { run_id: 9, done: 1, total: 4, new_items: 0, errors: 0 } });
    expect(s.runs["9"]).toMatchObject({ id: "9", kind: "manual", done: 1 });
    s = reduceEvent(s, { type: "run.done", data: { run_id: "9", new_items: 0, errors: 0 } });
    expect(s.runs).toEqual({});
  });

  it("leaves state alone for cache-only events", () => {
    expect(reduceEvent(initialLive, { type: "counts", data: { unread_total: 1, feeds: {} } })).toBe(initialLive);
    expect(reduceEvent(initialLive, { type: "items.state", data: { ids: ["1"], read: true, source: "web" } })).toBe(initialLive);
  });
});

describe("announcementFor", () => {
  it("says what a manual refresh did", () => {
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 12, errors: 0 } })).toBe("12 new articles");
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 1, errors: 0 } })).toBe("1 new article");
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } })).toBe("No new articles");
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 3, errors: 2 } })).toMatch(/Couldn't refresh 2 feeds/);
  });

  it("does not announce the retention sweep as a refresh", () => {
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "retention")).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "manual")).toBe("No new articles");
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "import")).toBe("No new articles");
  });

  it("announces a scheduled fetch but not one inside a run", () => {
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2 } })).toBe("2 new articles");
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, run_ids: ["4"] } })).toBeNull();
    expect(announcementFor({ type: "counts", data: { unread_total: 0, feeds: {} } })).toBeNull();
  });
});

describe("cache reconciliation", () => {
  function seeded() {
    const qc = new QueryClient();
    const data: InfiniteData<ItemsPage> = { pages: [pageOf([card(1), card(2)], "c"), pageOf([card(3)])], pageParams: ["", "c"] };
    qc.setQueryData(keys.items({ view: "unread" }), data);
    qc.setQueryData(keys.item("1001"), detail(1));
    qc.setQueryData(keys.bootstrap, bootstrap);
    return qc;
  }
  const rows = (qc: QueryClient) => qc.getQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "unread" }))!.pages.flatMap((p) => p.items);

  it("items.state patches read and starred in lists and details, without refetching", () => {
    const qc = seeded();
    handleServerEvent(qc, { type: "items.state", data: { ids: ["1001", "1003"], read: true, source: "reeder" } });
    expect(rows(qc).map((i) => i.read)).toEqual([true, false, true]);
    expect(qc.getQueryData<{ read: boolean }>(keys.item("1001"))?.read).toBe(true);
    handleServerEvent(qc, { type: "items.state", data: { ids: ["1002"], starred: true, source: "web" } });
    expect(rows(qc)[1]?.starred).toBe(true);
    expect(qc.isFetching()).toBe(0);
  });

  it("counts updates the badges from per-feed numbers", () => {
    const qc = seeded();
    applyCounts(qc, { unread_total: 9, feeds: { "1": 9 } });
    const b = qc.getQueryData<typeof bootstrap>(keys.bootstrap)!;
    expect(b.counts.unread).toBe(9);
    expect(b.feeds[0]?.unread).toBe(9);
    expect(b.folders[0]?.unread).toBe(9);
    applyCounts(qc, { unread_total: 0, feeds: {} });
    expect(qc.getQueryData<typeof bootstrap>(keys.bootstrap)!.feeds[0]?.unread).toBe(0);
  });

  it("resync invalidates the lists and the bootstrap", () => {
    const qc = seeded();
    handleServerEvent(qc, { type: "resync", data: {} });
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(keys.bootstrap)?.isInvalidated).toBe(true);
  });

  it("folder.changed refetches the bootstrap and saved searches, not the item lists", () => {
    const qc = seeded();
    qc.setQueryData(savedSearchesKey, []);
    handleServerEvent(qc, { type: "folder.changed", data: { folder_id: "10" } });
    handleServerEvent(qc, { type: "folder.changed", data: {} });
    expect(qc.getQueryState(keys.bootstrap)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(savedSearchesKey)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(false);
    expect(parseServerEvent("folder.changed", "{\"folder_id\":\"3\"}")).toEqual({ type: "folder.changed", data: { folder_id: "3" } });
  });

  it("fulltext.ready invalidates just those items", () => {
    const qc = seeded();
    handleServerEvent(qc, { type: "fulltext.ready", data: { ids: ["1001"], source: "ingest" } });
    expect(qc.getQueryState(keys.item("1001"))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(false);
  });

  it("scopeKey stays stable", () => {
    expect(scopeKey({ view: "unread" })).toBe("unread");
  });
});

describe("retention runs", () => {
  it("do not count as refreshing", () => {
    const st = reduceEvent(initialLive, { type: "run.start", data: { run_id: "5", kind: "retention", total: 1 } });
    expect(Object.values(st.runs).some((r) => isRefreshKind(r.kind))).toBe(false);
    expect(isRefreshKind("manual")).toBe(true);
    expect(isRefreshKind("import")).toBe(true);
  });
});

describe("transport helpers", () => {
  it("parses only known event types", () => {
    expect(parseServerEvent("counts", '{"unread_total":1,"feeds":{}}')).toEqual({ type: "counts", data: { unread_total: 1, feeds: {} } } satisfies ServerEvent);
    expect(parseServerEvent("mystery", "{}")).toBeNull();
    expect(parseServerEvent("counts", "{oops")).toBeNull();
  });

  it("polls every 2 s during a run and every 60 s otherwise", () => {
    expect(pollInterval(true)).toBe(2000);
    expect(pollInterval(false)).toBe(60000);
  });
});

describe('pendingFor (the "n new" pill)', () => {
  const feeds = [
    { id: "1", folder_id: "a" },
    { id: "2", folder_id: "a" },
    { id: "3", folder_id: "b" },
  ];
  const pending = { "1": 3, "2": 2, "3": 4 };
  it("counts only feeds inside the list's scope", () => {
    expect(pendingFor(pending, { view: "unread" }, feeds)).toBe(9);
    expect(pendingFor(pending, { view: "all", feed: "3" }, feeds)).toBe(4);
    expect(pendingFor(pending, { view: "unread", folder: "a" }, feeds)).toBe(5);
    expect(pendingFor(pending, { view: "unread", feed: "9" }, feeds)).toBe(0);
  });
  it("shows nothing for starred, search and oldest-first lists", () => {
    expect(pendingFor(pending, { view: "starred" }, feeds)).toBe(0);
    expect(pendingFor(pending, { view: "all", q: "x" }, feeds)).toBe(0);
    expect(pendingFor(pending, { view: "unread", order: "oldest" }, feeds)).toBe(0);
  });
  it("does not count ids the list already holds", () => {
    const loaded = { ids: new Set(["a", "b"]), pendingIds: { "1": ["a", "b", "c"], "2": ["z"] } };
    // Feed 1: 3 pending, a and b are already loaded, so 1 is new; feed 2: 2 pending, z not loaded.
    expect(pendingFor({ "1": 3, "2": 2 }, { view: "unread" }, feeds, loaded)).toBe(3);
    expect(pendingFor({ "1": 3 }, { view: "unread", feed: "1" }, feeds, { ...loaded, ids: new Set(["a", "b", "c"]) })).toBe(0);
    // Without ids from the server the count stands.
    expect(pendingFor({ "3": 4 }, { view: "unread" }, feeds, loaded)).toBe(4);
  });
  it("clears only the covered feeds", () => {
    expect(clearPending(pending, { view: "unread", folder: "a" }, feeds)).toEqual({ "3": 4 });
    expect(clearPending(pending, { view: "unread", feed: "3" }, feeds)).toEqual({ "1": 3, "2": 2 });
    expect(clearPending(pending, { view: "unread" }, feeds)).toEqual({});
  });
});

describe("run kinds (review findings 7 and 8)", () => {
  it("classifies a run by its kind only: a progress event with `changed` for an unseen run is not a filter apply", () => {
    const st = reduceEvent(initialLive, { type: "run.progress", data: { run_id: "8", done: 1, total: 5, new_items: 0, errors: 0, changed: 1 } });
    expect(st.runs["8"]?.kind).toBe("unknown");
    expect(Object.values(st.runs).some((r) => isRefreshKind(r.kind))).toBe(false);
  });

  it("auto_read is not a refresh, and finishing it is quiet", () => {
    let st = reduceEvent(initialLive, { type: "run.start", data: { run_id: "9", kind: "auto_read", total: 40 } });
    expect(Object.values(st.runs).some((r) => isRefreshKind(r.kind))).toBe(false);
    st = reduceEvent(st, { type: "run.progress", data: { run_id: "9", done: 10, total: 40, new_items: 0, errors: 0, changed: 10 } });
    expect(st.runs["9"]?.kind).toBe("auto_read");
    expect(announcementFor({ type: "run.done", data: { run_id: "9", kind: "auto_read", new_items: 0, errors: 0, changed: 40 } })).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "9", kind: "auto_read", new_items: 0, errors: 1, error: "auto_read_failed" } })).toMatch(/Couldn't finish marking/);
  });

  it("a filter apply cancelled by the user's own edit or delete is announced neutrally", () => {
    resetFilterTouched();
    const done = { type: "run.done", data: { run_id: "3", kind: "filter_apply", filter_id: "5", new_items: 0, errors: 1, error: "apply_failed", changed: 2 } } as const;
    expect(announcementFor(done)).toBe("Couldn't finish applying the filter");
    noteFilterTouched("5");
    expect(announcementFor(done)).toBe("Stopped because the rule changed");
    expect(announcementFor({ type: "run.done", data: { ...done.data, filter_id: "6" } })).toBe("Couldn't finish applying the filter");
    resetFilterTouched();
  });

  it("pollStatus reconciles the muted count", async () => {
    const { pollStatus } = await import("./events");
    const qc = new QueryClient();
    qc.setQueryData(keys.bootstrap, { ...bootstrap, counts: { ...bootstrap.counts, muted: 1 } });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ runs: [], inflight: 0, unread_total: 4, muted: 12 }), { status: 200, headers: { "content-type": "application/json" } })));
    await pollStatus(qc);
    vi.unstubAllGlobals();
    expect(qc.getQueryData<typeof bootstrap>(keys.bootstrap)?.counts).toMatchObject({ unread: 4, muted: 12 });
  });
});
