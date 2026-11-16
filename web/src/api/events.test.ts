import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import type { InfiniteData } from "@tanstack/react-query";
import { FINISHED_KEEP_MAX, FINISHED_KEEP_MS, isRefreshKind, announcementFor, applyCounts, clearPending, pendingFor, handleServerEvent, initialLive, liveStore, noteArrivals, parseServerEvent, pollInterval, pollStatus, reduceEvent, resetCountsGuard, seedRun, seedRuns } from "./events";
import { keys, scopeKey } from "./queries";
import { savedSearchesKey } from "./savedSearches";
import { noteFilterTouched, resetFilterTouched } from "./filterEdits";
import type { ItemsPage, ServerEvent } from "./types";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

beforeEach(() => liveStore.set(initialLive));

describe("seedRuns", () => {
  const run = (id: string) => ({ id, kind: "import", done: 1, total: 5, new_items: 0, errors: 0 });

  it("does not bring back a run whose run.done already arrived (a stale bootstrap answer)", () => {
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "7", kind: "import", total: 5 } }));
    liveStore.set((s) => reduceEvent(s, { type: "run.done", data: { run_id: "7", new_items: 0, errors: 0 } }));
    seedRuns([run("7")]);
    expect(liveStore.get().runs).toEqual({});
  });

  it("seeds an unfinished run even when another run is already present", () => {
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "1", kind: "refresh", total: 3 } }));
    seedRuns([run("1"), run("2")]);
    expect(Object.keys(liveStore.get().runs).sort()).toEqual(["1", "2"]);
  });

  it("leaves a known run's live progress alone and keeps a run the bootstrap lacks", () => {
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "1", kind: "refresh", total: 3 } }));
    liveStore.set((s) => reduceEvent(s, { type: "run.progress", data: { run_id: "1", done: 2, total: 3, new_items: 0, errors: 0 } }));
    seedRuns([run("2")]);
    expect(liveStore.get().runs["1"]?.done).toBe(2);
    seedRuns([{ ...run("1"), done: 0 }]);
    expect(liveStore.get().runs["1"]?.done).toBe(2);
  });

  it("seeds a bootstrap that lists only new runs, and seedRun follows the same rule", () => {
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "1", kind: "refresh", total: 3 } }));
    seedRuns([run("5"), { ...run("6"), id: 6 as unknown as string }]);
    expect(Object.keys(liveStore.get().runs).sort()).toEqual(["1", "5", "6"]);
    liveStore.set((s) => reduceEvent(s, { type: "run.done", data: { run_id: "5", new_items: 0, errors: 0 } }));
    seedRun(run("5"));
    expect(liveStore.get().runs["5"]).toBeUndefined();
  });

  it("does not bring back a run a status poll stopped listing", async () => {
    mockFetch({ "GET /api/status": () => json({ runs: [], event_id: "50", inflight: 0, unread_total: 0 }) });
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "7", kind: "import", total: 5 } }));
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs).toEqual({});
    seedRuns([run("7")]);
    expect(liveStore.get().runs).toEqual({});
  });

  it("still guards a run after 19 later runs finished (the kept window holds FINISHED_KEEP_MAX)", () => {
    liveStore.set((s) => reduceEvent(s, { type: "run.done", data: { run_id: "0", new_items: 0, errors: 0 } }));
    for (let i = 1; i < FINISHED_KEEP_MAX; i++) liveStore.set((s) => reduceEvent(s, { type: "run.done", data: { run_id: String(i), new_items: 0, errors: 0 } }));
    seedRuns([run("0")]);
    expect(liveStore.get().runs).toEqual({});
  });
});

// Issue #536: an answer (bootstrap, /api/status, a 202 body) can reach the client after events it predates. Each one
// carries `event_id`, the last event the server had published before it looked, and each run remembers the id of the
// last event or answer about it, so an older answer is told from a newer one whichever path it came by.
describe("answers older or newer than the stream (issue #536)", () => {
  const run = (id: string, over: object = {}) => ({ id, kind: "import", done: 1, total: 5, new_items: 0, errors: 0, ...over });
  const event = (ev: ServerEvent, id: number) => liveStore.set((s) => reduceEvent(s, ev, id));
  const start = (id: string, at: number, kind = "import") => event({ type: "run.start", data: { run_id: id, kind, total: 5 } }, at);
  const done = (id: string, at: number, extra: object = {}) => event({ type: "run.done", data: { run_id: id, new_items: 0, errors: 0, ...extra } } as ServerEvent, at);
  const status = (event_id: string | undefined, runs: object[]) => mockFetch({ "GET /api/status": () => json({ runs, inflight: 0, unread_total: 0, ...(event_id ? { event_id } : {}) }) });

  it("a status answer built before run.done does not bring the finished run back", async () => {
    start("7", 10);
    done("7", 20, { errors: 1, error: "x" });
    status("15", [run("7")]);
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs).toEqual({});
    expect(liveStore.get().finished["7"]).toMatchObject({ errors: 1, error: "x", seq: 20 }); // the exact result is kept
    expect(liveStore.get().finished["7"]?.inferred).toBeUndefined();
  });

  it("a status answer built before a run the 202 announced does not end that run", async () => {
    seedRun(run("8", { kind: "manual", done: 0 }), "100"); // the 202 body: the run exists at event 100
    status("90", []);
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs["8"]).toBeDefined();
    expect(liveStore.get().finished["8"]).toBeUndefined();
    // An answer built after it: the run is over.
    status("110", []);
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs).toEqual({});
    expect(liveStore.get().finished["8"]).toMatchObject({ inferred: true, kind: "manual" });
  });

  it("a status answer built before the stream's later progress leaves the live numbers alone", async () => {
    start("9", 10);
    event({ type: "run.progress", data: { run_id: "9", done: 4, total: 5, new_items: 0, errors: 0 } }, 30);
    status("20", [run("9", { done: 1 })]);
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs["9"]?.done).toBe(4);
    status("40", [run("9", { done: 5 })]); // a newer answer (the stream is down) is taken
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs["9"]?.done).toBe(5);
  });

  it("a bootstrap built before run.done does not bring the run back, however many runs finished since", () => {
    for (let i = 0; i < FINISHED_KEEP_MAX + 5; i++) {
      start(String(i), 100 + 2 * i);
      done(String(i), 101 + 2 * i);
    }
    expect(Object.keys(liveStore.get().finished)).toHaveLength(FINISHED_KEEP_MAX); // run 0 is no longer remembered
    expect(liveStore.get().finished["0"]).toBeUndefined();
    seedRuns([run("0")], "100"); // built while run 0 was going
    expect(liveStore.get().runs).toEqual({});
    seedRun(run("1"), "103");
    expect(liveStore.get().runs).toEqual({});
    // A bootstrap built after all of them introduces a run that really is going.
    seedRuns([run("900")], "500");
    expect(Object.keys(liveStore.get().runs)).toEqual(["900"]);
  });

  it("a run that ended long ago is still ended: the ended set outlives the finished results", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-19T12:00:00Z"));
    start("1", 10);
    done("1", 20);
    vi.advanceTimersByTime(FINISHED_KEEP_MS + 1000);
    start("2", 30);
    done("2", 40); // makes room: run 1's result is dropped now
    expect(liveStore.get().finished["1"]).toBeUndefined();
    expect(liveStore.get().ended["1"]).toBe(true);
    seedRuns([run("1")], "15");
    expect(liveStore.get().runs).toEqual({});
    vi.useRealTimers();
  });

  it("run.done after a poll ended the run keeps the run's kind, and announces by it", async () => {
    const toasts = await import("@/shell/toasts");
    const spy = vi.spyOn(toasts, "announce");
    const qc = new QueryClient();
    handleServerEvent(qc, { type: "run.start", data: { run_id: "7", kind: "import", total: 5 } }, 10);
    status("15", []);
    await pollStatus(qc);
    expect(liveStore.get().finished["7"]).toMatchObject({ kind: "import", inferred: true });
    handleServerEvent(qc, { type: "run.done", data: { run_id: "7", new_items: 20, errors: 0 } }, 20);
    expect(liveStore.get().finished["7"]).toMatchObject({ kind: "import", errors: 0, seq: 20 });
    expect(liveStore.get().finished["7"]?.inferred).toBeUndefined();
    expect(spy).not.toHaveBeenCalled(); // an import's items arrive silently
    // A manual run is announced by its kind too.
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    handleServerEvent(qc, { type: "run.start", data: { run_id: "8", kind: "manual", total: 5 } }, 30);
    status("35", []);
    await pollStatus(qc);
    const listCalls = () => invalidate.mock.calls.filter((c) => JSON.stringify((c[0] as { queryKey?: unknown })?.queryKey) === JSON.stringify(keys.itemsAll)).length;
    expect(listCalls()).toBe(1);
    expect(liveStore.get().refreshes).toBe(1);
    handleServerEvent(qc, { type: "run.done", data: { run_id: "8", new_items: 0, errors: 0 } }, 40);
    expect(spy).toHaveBeenCalledWith("No new articles");
    // The poll already ended this run: its run.done does not make the lists stale, or count, a second time.
    expect(listCalls()).toBe(1);
    expect(liveStore.get().refreshes).toBe(1);
    spy.mockRestore();
  });

  it("an end only inferred from a poll is marked as inferred and carries no result", async () => {
    start("5", 10, "auto_read");
    event({ type: "run.progress", data: { run_id: "5", done: 3, total: 9, new_items: 0, errors: 1, changed: 3 } }, 12);
    status("20", []);
    await pollStatus(new QueryClient());
    const f = liveStore.get().finished["5"]!;
    expect(f).toMatchObject({ inferred: true, kind: "auto_read", errors: 1, seq: 20 });
    expect(f.changed).toBeUndefined();
    expect(f.error).toBeUndefined();
  });

  it("an end only inferred is undone by a newer answer or event that shows the run going (a poll made while the server was busy)", async () => {
    start("5", 10);
    status("20", []);
    await pollStatus(new QueryClient());
    expect(liveStore.get().finished["5"]?.inferred).toBe(true);
    status("30", [run("5", { done: 2 })]);
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs["5"]?.done).toBe(2);
    expect(liveStore.get().finished["5"]).toBeUndefined();
    // And the real end still replaces it.
    done("5", 40, { changed: 7 });
    expect(liveStore.get().finished["5"]).toMatchObject({ changed: 7, seq: 40 });
  });

  it("an exact end is not undone by any answer", () => {
    start("5", 10);
    done("5", 20);
    seedRuns([run("5")], "9999");
    expect(liveStore.get().runs).toEqual({});
  });

  it("a 202 read after its run already ended (event_id at or above the run.done) does not bring the run back", () => {
    start("5", 10);
    done("5", 20);
    seedRun(run("5"), "20");
    expect(liveStore.get().runs).toEqual({});
    seedRun(run("5"), "25");
    expect(liveStore.get().runs).toEqual({});
  });

  it("a status answer the server could not order (event_id 0: the scheduler did not answer) ends no run", async () => {
    start("5", 10);
    status("0", []);
    await pollStatus(new QueryClient());
    expect(liveStore.get().runs["5"]).toBeDefined();
    expect(liveStore.get().finished["5"]).toBeUndefined();
    expect(liveStore.get().ended).toEqual({});
  });

  it("a resync with an id below the last one seen (the server's ids restarted) clears the ordering state; a later one does not", () => {
    start("5", 100);
    start("6", 101);
    done("6", 102);
    event({ type: "resync", data: {} }, 103);
    expect(liveStore.get().ended["6"]).toBe(true);
    expect(liveStore.get().seq["5"]).toBe(100);
    event({ type: "resync", data: {} }, 7);
    expect(liveStore.get().ended).toEqual({});
    expect(liveStore.get().seq).toEqual({});
    expect(liveStore.get().finished["6"]?.seq).toBe(0);
    expect(liveStore.get().lastId).toBe(7);
    // The new numbering orders again: a run that starts at 8 is shown, and an answer built at 9 describes it.
    start("5", 8);
    expect(liveStore.get().seq["5"]).toBe(8);
  });
});

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

  it("accumulates new items from a manual fetch.done and clears them on resync", () => {
    let s = reduceEvent(initialLive, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 3, trigger: "feed_manual" } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "2", outcome: "ok", new_items: 2, trigger: "manual" } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "3", outcome: "not_modified", new_items: 0, trigger: "feed_manual" } });
    expect(s.pendingByFeed).toEqual({ "1": 3, "2": 2 });
    s = reduceEvent(s, { type: "resync", data: {} });
    expect(s).toMatchObject({ pendingByFeed: {}, pendingIds: {} });
  });

  it("counts new items from a scheduled poll and a subscribe fetch for the pill, but not an import, a retention trim or an unnamed trigger", () => {
    let s = reduceEvent(initialLive, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 3, new_item_ids: ["7", "8", "9"], trigger: "scheduled" } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "2", outcome: "ok", new_items: 2, trigger: "subscribe" } });
    expect(s.pendingByFeed).toEqual({ "1": 3, "2": 2 });
    expect(s.pendingIds).toEqual({ "1": ["7", "8", "9"] });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "3", outcome: "ok", new_items: 5, trigger: "import" } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "5", outcome: "ok", new_items: 1, trigger: "retention" } });
    s = reduceEvent(s, { type: "fetch.done", data: { feed_id: "4", outcome: "ok", new_items: 1 } });
    expect(s.pendingByFeed).toEqual({ "1": 3, "2": 2 });
  });

  it("a scheduled poll raises the pill count and says nothing aloud; an import or a retention trim does neither", async () => {
    const toasts = await import("@/shell/toasts");
    const spy = vi.spyOn(toasts, "announce");
    liveStore.set(() => initialLive);
    const qc = new QueryClient();
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: 2, new_item_ids: ["7", "8"], trigger: "scheduled" } });
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 2 });
    expect(pendingFor(liveStore.get().pendingByFeed, { view: "unread" }, [], [])).toBe(2);
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "2", run_ids: [], outcome: "ok", new_items: 4, trigger: "import" } });
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "3", run_ids: [], outcome: "trim_only", new_items: 0, trimmed_items: 9, trigger: "retention" } });
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 2 });
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });

  it("asks for the unread counts once after a burst of fetches that brought in articles, not after one that found none", () => {
    vi.useFakeTimers();
    resetCountsGuard();
    const qc = new QueryClient();
    const inval = vi.spyOn(qc, "invalidateQueries");
    const boots = () => inval.mock.calls.filter(([f]) => JSON.stringify(f?.queryKey) === JSON.stringify(keys.bootstrap)).length;
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "not_modified", new_items: 0, trigger: "scheduled" } });
    vi.advanceTimersByTime(5000);
    expect(boots()).toBe(0);
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: 2, trigger: "scheduled" } });
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "2", run_ids: [], outcome: "ok", new_items: 1, trigger: "scheduled" } });
    expect(boots()).toBe(0);
    vi.advanceTimersByTime(1500);
    expect(boots()).toBe(1);
    resetCountsGuard();
    vi.useRealTimers();
  });

  it("counts only the articles a list can show: muted ones are left out of the pill", () => {
    const done = (over: Record<string, unknown>) => ({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", trigger: "scheduled", ...over } }) as ServerEvent;
    expect(reduceEvent(initialLive, done({ new_items: 5, muted_items: 2, new_item_ids: ["7", "8", "9"] })).pendingByFeed).toEqual({ "1": 3 });
    const allMuted = reduceEvent(initialLive, done({ new_items: 4, muted_items: 4, new_item_ids: [] }));
    expect(allMuted.pendingByFeed).toEqual({});
    expect(allMuted.arrivals).toEqual({});
    // More new articles than the event lists ids for (it names the first 50): the count is the server's, not the ids'.
    const ids = Array.from({ length: 50 }, (_, i) => String(1000 + i));
    expect(reduceEvent(initialLive, done({ new_items: 120, muted_items: 20, new_item_ids: ids })).pendingByFeed).toEqual({ "1": 100 });
  });

  it("raises no pill for a scheduled fetch that an import run joined, and still does for one a manual run joined", () => {
    let s = reduceEvent(initialLive, { type: "run.start", data: { run_id: "9", kind: "import", total: 3 } });
    s = reduceEvent(s, { type: "run.start", data: { run_id: "10", kind: "manual", total: 3 } });
    const done = (run: string, trigger: string) => ({ type: "fetch.done", data: { feed_id: "1", run_ids: [run], outcome: "ok", new_items: 2, new_item_ids: ["7", "8"], trigger } }) as ServerEvent;
    const joined = reduceEvent(s, done("9", "scheduled"));
    expect(joined.pendingByFeed).toEqual({});
    expect(joined.arrivals).toEqual({ "1": 1 }); // an empty list still fills itself
    // Once the import has ended its kind is still known.
    const over = reduceEvent(reduceEvent(s, { type: "run.done", data: { run_id: "9", new_items: 2, errors: 0 } }), done("9", "scheduled"));
    expect(over.pendingByFeed).toEqual({});
    expect(reduceEvent(s, done("10", "manual")).pendingByFeed).toEqual({ "1": 2 });
    expect(reduceEvent(s, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: 2, trigger: "scheduled" } }).pendingByFeed).toEqual({ "1": 2 });
  });

  it("forgets what it held for a feed that was deleted, once the feeds say so", async () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.bootstrap, bootstrap);
    const keep = bootstrap.feeds![0]!.id;
    const gone = "98765";
    liveStore.set((s) => ({
      ...s,
      pendingByFeed: { [keep]: 2, [gone]: 3 },
      pendingIds: { [keep]: ["1"], [gone]: ["2", "3"] },
      arrivals: { [keep]: 1, [gone]: 1 },
    }));
    handleServerEvent(qc, { type: "feed.changed", data: { feed_id: gone } });
    await vi.waitFor(() => expect(liveStore.get().pendingByFeed).toEqual({ [keep]: 2 }));
    expect(liveStore.get().pendingIds).toEqual({ [keep]: ["1"] });
    expect(liveStore.get().arrivals).toEqual({ [keep]: 1 });
    expect(pendingFor(liveStore.get().pendingByFeed, { view: "unread" }, bootstrap.feeds!, bootstrap.folders!)).toBe(2);
  });

  it("keeps what it holds for a feed that was only edited", async () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.bootstrap, bootstrap);
    const keep = bootstrap.feeds![0]!.id;
    liveStore.set((s) => ({ ...s, pendingByFeed: { [keep]: 2 }, pendingIds: { [keep]: ["1"] }, arrivals: { [keep]: 1 } }));
    handleServerEvent(qc, { type: "feed.changed", data: { feed_id: keep } });
    await new Promise((r) => setTimeout(r, 20));
    expect(liveStore.get().pendingByFeed).toEqual({ [keep]: 2 });
  });

  it("waits for the end of a burst of fetches: each one moves the request on, up to a limit", () => {
    vi.useFakeTimers();
    resetCountsGuard();
    const qc = new QueryClient();
    const inval = vi.spyOn(qc, "invalidateQueries");
    const boots = () => inval.mock.calls.filter(([f]) => JSON.stringify(f?.queryKey) === JSON.stringify(keys.bootstrap)).length;
    const fetched = (feed: string) => handleServerEvent(qc, { type: "fetch.done", data: { feed_id: feed, run_ids: [], outcome: "ok", new_items: 1, trigger: "scheduled" } });
    // Fetches 800 ms apart: no request while they keep coming, one 1 s after the last.
    for (const f of ["1", "2", "3"]) {
      fetched(f);
      vi.advanceTimersByTime(800);
    }
    expect(boots()).toBe(0);
    vi.advanceTimersByTime(300);
    expect(boots()).toBe(1);
    // A stream that never pauses is still answered: 5 s after its first fetch.
    for (let i = 0; i < 8; i++) {
      fetched(String(10 + i));
      vi.advanceTimersByTime(800);
    }
    expect(boots()).toBe(2);
    resetCountsGuard();
    vi.useRealTimers();
  });

  it("asks again when the bootstrap request on its way began before the fetch finished", async () => {
    vi.useFakeTimers();
    resetCountsGuard();
    const qc = new QueryClient();
    qc.setQueryData(keys.bootstrap, bootstrap);
    let asked = 0;
    const obs = new QueryObserver(qc, {
      queryKey: keys.bootstrap,
      queryFn: () => {
        asked++;
        return new Promise(() => undefined);
      },
    });
    const off = obs.subscribe(() => undefined);
    await vi.advanceTimersByTimeAsync(0);
    expect(asked).toBe(1); // a request that never answers: it began before the fetch
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "1", run_ids: [], outcome: "ok", new_items: 1, trigger: "scheduled" } });
    await vi.advanceTimersByTimeAsync(1100);
    expect(asked).toBe(2);
    off();
    resetCountsGuard();
    vi.useRealTimers();
  });

  it("tracks the new item ids so the pill can skip ones the list already has", () => {
    const s = reduceEvent(initialLive, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, new_item_ids: ["7", "8"], trigger: "feed_manual" } });
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
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "manual")).toBe("No new articles");
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 3, errors: 2 } })).toMatch(/Couldn't refresh 2 feeds/);
  });

  it("stays quiet about an empty run whose kind is unknown (run.start missed)", () => {
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } })).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 2, errors: 0 } })).toBe("2 new articles");
  });

  it("does not announce the retention sweep as a refresh", () => {
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "retention")).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "manual")).toBe("No new articles");
  });

  // Issue #93: an OPML import run's run.done said "20 new articles" (its first fetches' items), although only a
  // person's own refresh announces new articles.
  it("does not announce an OPML import run, whatever it brought in", () => {
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 20, errors: 0 } }, "import")).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }, "import")).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "1", new_items: 3, errors: 1 } }, "import")).toBeNull();
    expect(announcementFor({ type: "run.done", data: { run_id: "1", kind: "import", new_items: 20, errors: 0 } })).toBeNull();
  });

  it("an import run's events raise no pill and no announcement, end to end", async () => {
    const toasts = await import("@/shell/toasts");
    const spy = vi.spyOn(toasts, "announce");
    liveStore.set(() => initialLive);
    const qc = new QueryClient();
    handleServerEvent(qc, { type: "run.start", data: { run_id: "77", kind: "import", total: 1 } });
    handleServerEvent(qc, { type: "fetch.done", data: { feed_id: "9", run_ids: ["77"], outcome: "ok", new_items: 20, trigger: "import" } });
    handleServerEvent(qc, { type: "run.done", data: { run_id: "77", new_items: 20, errors: 0 } });
    expect(liveStore.get().pendingByFeed).toEqual({});
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });

  it("announces a manual per-feed refresh but stays quiet about anything else", () => {
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, trigger: "feed_manual" } })).toBe("2 new articles");
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2 } })).toBeNull();
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, trigger: "scheduled" } })).toBeNull();
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, trigger: "subscribe" } })).toBeNull();
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, trigger: "feed_manual", run_ids: ["4"] } })).toBe("2 new articles");
    // A "Refresh all" run's own per-feed fetches are stamped "manual" (never "feed_manual"), with or without
    // run_ids: every one of them belongs to a run by construction, so its run.done announces the total once,
    // not once per feed (previously unstamped, this used to double-announce — issue found in review of #56).
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, trigger: "manual" } })).toBeNull();
    expect(announcementFor({ type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, trigger: "manual", run_ids: ["4"] } })).toBeNull();
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
    handleServerEvent(qc, { type: "items.state", data: { ids: ["1001", "1003"], read: true, source: "api" } });
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

  it("counts rolls each folder's number up over its subfolders, as the bootstrap does", () => {
    const qc = seeded();
    // Tech (2) > Apple (3) > Mac (4); Sports (5) beside it at the top level.
    qc.setQueryData<typeof bootstrap>(keys.bootstrap, {
      ...bootstrap,
      folders: [
        { id: "2", name: "Tech", parent_id: null, position: 0, is_default: false, unread: 0 },
        { id: "3", name: "Apple", parent_id: "2", position: 1, is_default: false, unread: 0 },
        { id: "4", name: "Mac", parent_id: "3", position: 2, is_default: false, unread: 0 },
        { id: "5", name: "Sports", parent_id: null, position: 3, is_default: false, unread: 0 },
      ],
      feeds: [
        { ...bootstrap.feeds[0]!, id: "10", folder_id: "2" },
        { ...bootstrap.feeds[0]!, id: "11", folder_id: "3" },
        { ...bootstrap.feeds[0]!, id: "12", folder_id: "4" },
        { ...bootstrap.feeds[0]!, id: "13", folder_id: "5" },
      ],
    });
    applyCounts(qc, { unread_total: 15, feeds: { "10": 1, "11": 2, "12": 4, "13": 8 } });
    const unread = Object.fromEntries(qc.getQueryData<typeof bootstrap>(keys.bootstrap)!.folders.map((f) => [f.name, f.unread]));
    expect(unread).toEqual({ Tech: 7, Apple: 6, Mac: 4, Sports: 8 });
  });

  it("resync invalidates the lists and the bootstrap", async () => {
    mockFetch({ "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }) });
    const qc = seeded();
    handleServerEvent(qc, { type: "resync", data: {} });
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(true);
    await vi.waitFor(() => expect(qc.getQueryState(keys.bootstrap)?.isInvalidated).toBe(true));
  });

  it("the end of a manual refresh makes the lists nothing shows stale, whoever fetched the articles; other runs leave them alone", () => {
    const qc = seeded();
    const unreadStale = () => qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated;
    for (const kind of ["import", "retention", "auto_read"]) {
      handleServerEvent(qc, { type: "run.start", data: { run_id: kind, kind, total: 1 } });
      handleServerEvent(qc, { type: "run.done", data: { run_id: kind, new_items: 0, errors: 0 } });
    }
    expect(unreadStale()).toBe(false);
    expect(liveStore.get().refreshes).toBe(0);
    handleServerEvent(qc, { type: "run.start", data: { run_id: "9", kind: "manual", total: 1 } });
    handleServerEvent(qc, { type: "run.done", data: { run_id: "9", new_items: 0, errors: 0 } });
    expect(unreadStale()).toBe(true);
    expect(liveStore.get().refreshes).toBe(1);
  });

  it("a list that is on screen is not made stale: it stays the snapshot the reader is in", () => {
    const qc = seeded();
    const unobserve = new QueryObserver(qc, { queryKey: keys.items({ view: "unread" }), enabled: false, staleTime: Infinity }).subscribe(() => {});
    handleServerEvent(qc, { type: "run.start", data: { run_id: "9", kind: "manual", total: 1 } });
    handleServerEvent(qc, { type: "run.done", data: { run_id: "9", new_items: 0, errors: 0 } });
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(false);
    expect(liveStore.get().refreshes).toBe(1); // the open list is told by the counter, and asks for itself
    unobserve();
  });

  it("a Digest's section lists are not marked stale (their cards belong to the page)", () => {
    const qc = seeded();
    const section = keys.section({ view: "unread", folder: "1" });
    qc.setQueryData(section, { pages: [pageOf([card(1)])], pageParams: [""] });
    handleServerEvent(qc, { type: "run.done", data: { run_id: "30", kind: "manual", new_items: 0, errors: 0 } });
    expect(qc.getQueryState(section)?.isInvalidated).toBe(false);
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(true);
  });

  it("noteArrivals counts the articles above the list's own newest id that it does not hold, once", async () => {
    mockFetch({ "GET /api/items": () => json(pageOf([card(9), card(8), card(2), card(1)])) });
    const held = new Set([card(1).id, card(2).id]);
    expect(await noteArrivals({ view: "unread" }, held, "1005")).toBe(2);
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 2 });
    expect(await noteArrivals({ view: "unread" }, held, "1005")).toBe(2); // asked again: the pill does not count them twice
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 2 });
  });

  it("noteArrivals does not count an older article that only entered the list (an unread one after the loaded ones were read)", async () => {
    mockFetch({ "GET /api/items": () => json(pageOf([card(4), card(3)])) });
    expect(await noteArrivals({ view: "unread" }, new Set([card(1).id]), "1005")).toBe(0);
    expect(liveStore.get().pendingByFeed).toEqual({});
  });

  it("noteArrivals compares ids as numbers, not as text", async () => {
    mockFetch({ "GET /api/items": () => json(pageOf([{ ...card(1), id: "10000" }, { ...card(2), id: "999" }])) });
    expect(await noteArrivals({ view: "unread" }, new Set(), "9999")).toBe(1);
  });

  it("noteArrivals does not add to a count that came without ids: a fetch.done count, then the same articles found", async () => {
    liveStore.set((s) => reduceEvent(s, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 3, trigger: "feed_manual" } }));
    expect(pendingFor(liveStore.get().pendingByFeed, { view: "unread" }, [], [])).toBe(3);
    mockFetch({ "GET /api/items": () => json(pageOf([card(9), card(8), card(7)])) });
    expect(await noteArrivals({ view: "unread" }, new Set(), "1005")).toBe(3);
    expect(liveStore.get().pendingByFeed).toEqual({ "1": 3 });
    expect(pendingFor(liveStore.get().pendingByFeed, { view: "unread" }, [], [])).toBe(3);
  });

  it("noteArrivals asks nothing for a list that shows no pill", async () => {
    const { calls } = mockFetch({ "GET /api/items": () => json(pageOf([card(1)])) });
    expect(await noteArrivals({ view: "starred" }, new Set(), undefined)).toBe(0);
    expect(await noteArrivals({ view: "unread", order: "oldest" }, new Set(), undefined)).toBe(0);
    expect(calls.length).toBe(0);
  });

  it("noteArrivals puts a time limit on its request, so a slow server is not waited on", async () => {
    const limit = new AbortController().signal;
    const timeout = vi.spyOn(AbortSignal, "timeout").mockReturnValue(limit);
    const { calls } = mockFetch({ "GET /api/items": () => json(pageOf([])) });
    await noteArrivals({ view: "unread" }, new Set(), "1005");
    expect(timeout).toHaveBeenCalledWith(3000);
    expect(calls[0]?.init?.signal).toBe(limit);
    timeout.mockRestore();
  });

  it("a manual run seen only in run.done (its start missed) makes the lists stale by the kind it carries", () => {
    const qc = seeded();
    handleServerEvent(qc, { type: "run.done", data: { run_id: "12", kind: "manual", new_items: 0, errors: 0 } });
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(true);
  });

  it("a status answer that ends a manual run makes the lists stale; one that ends an import does not", async () => {
    mockFetch({ "GET /api/status": () => json({ runs: [], event_id: "50", inflight: 0, unread_total: 0 }) });
    const qc = seeded();
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "7", kind: "import", total: 5 } }));
    await pollStatus(qc);
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(false);
    liveStore.set((s) => reduceEvent(s, { type: "run.start", data: { run_id: "8", kind: "manual", total: 5 } }));
    await pollStatus(qc);
    expect(qc.getQueryState(keys.items({ view: "unread" }))?.isInvalidated).toBe(true);
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
  const folders = [
    { id: "a", name: "A" },
    { id: "b", name: "B" },
  ];
  const pending = { "1": 3, "2": 2, "3": 4 };
  it("counts only feeds inside the list's scope", () => {
    expect(pendingFor(pending, { view: "unread" }, feeds, folders)).toBe(9);
    expect(pendingFor(pending, { view: "all", feed: "3" }, feeds, folders)).toBe(4);
    expect(pendingFor(pending, { view: "unread", folder: "a" }, feeds, folders)).toBe(5);
    expect(pendingFor(pending, { view: "unread", feed: "9" }, feeds, folders)).toBe(0);
  });
  it("shows nothing for starred, search and oldest-first lists", () => {
    expect(pendingFor(pending, { view: "starred" }, feeds, folders)).toBe(0);
    expect(pendingFor(pending, { view: "all", q: "x" }, feeds, folders)).toBe(0);
    expect(pendingFor(pending, { view: "unread", order: "oldest" }, feeds, folders)).toBe(0);
  });
  it("does not count ids the list already holds", () => {
    const loaded = { ids: new Set(["a", "b"]), pendingIds: { "1": ["a", "b", "c"], "2": ["z"] } };
    // Feed 1: 3 pending, a and b are already loaded, so 1 is new; feed 2: 2 pending, z not loaded.
    expect(pendingFor({ "1": 3, "2": 2 }, { view: "unread" }, feeds, folders, loaded)).toBe(3);
    expect(pendingFor({ "1": 3 }, { view: "unread", feed: "1" }, feeds, folders, { ...loaded, ids: new Set(["a", "b", "c"]) })).toBe(0);
    // Without ids from the server the count stands.
    expect(pendingFor({ "3": 4 }, { view: "unread" }, feeds, folders, loaded)).toBe(4);
  });
  it("a folder list covers the feeds of its subfolders", () => {
    // c sits inside a; its feed 4 counts for a's list, not for b's.
    const nested = [...folders, { id: "c", name: "C", parent_id: "a" }];
    const all = [...feeds, { id: "4", folder_id: "c" }];
    const p = { ...pending, "4": 6 };
    expect(pendingFor(p, { view: "unread", folder: "a" }, all, nested)).toBe(11);
    expect(pendingFor(p, { view: "unread", folder: "c" }, all, nested)).toBe(6);
    expect(clearPending(p, { view: "unread", folder: "a" }, all, nested)).toEqual({ "3": 4 });
  });
  it("clears only the covered feeds", () => {
    expect(clearPending(pending, { view: "unread", folder: "a" }, feeds, folders)).toEqual({ "3": 4 });
    expect(clearPending(pending, { view: "unread", feed: "3" }, feeds, folders)).toEqual({ "1": 3, "2": 2 });
    expect(clearPending(pending, { view: "unread" }, feeds, folders)).toEqual({});
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
