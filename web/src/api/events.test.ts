import { beforeEach, describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import type { InfiniteData } from "@tanstack/react-query";
import { announcementFor, applyCounts, handleServerEvent, initialLive, liveStore, parseServerEvent, pollInterval, reduceEvent } from "./events";
import { keys, scopeKey } from "./queries";
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

  it("accumulates new items from fetch.done and clears them on resync", () => {
    let s = reduceEvent(initialLive, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 3 } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "2", outcome: "ok", new_items: 2 } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "3", outcome: "not_modified", new_items: 0 } });
    expect(s.pendingNew).toBe(5);
    s = reduceEvent(s, { type: "resync", data: {} });
    expect(s).toMatchObject({ pendingNew: 0, resyncTick: 1 });
  });

  it("records full-text readiness and feed changes", () => {
    let s = reduceEvent(initialLive, { type: "fulltext.ready", data: { ids: ["5", "6"], source: "ingest" } });
    expect(s.fulltextReady).toEqual(["5", "6"]);
    s = reduceEvent(s, { type: "feed.changed", data: { feed_id: "1" } });
    expect(s.feedsStaleTick).toBe(1);
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
    expect(liveStore.get().resyncTick).toBe(1);
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
