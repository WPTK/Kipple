import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { authStore } from "@/api/client";
import { keys } from "@/api/queryKeys";
import { bootstrap } from "@/test/mockApi";
import { wipeOfflineData } from "./offline";
import { offlineStore } from "./offlineState";
import {
  FIT_AFTER_SECONDS,
  FLUSH_EVERY_MS,
  FLUSH_TIMEOUT_MS,
  clearStatsQueue,
  IDLE_CUTOFF_MS,
  ITEM_EVENT_TTL_MS,
  QUEUE_MAX_EVENTS,
  SESSION_TTL_MS,
  flushStatsQueue,
  initStatsQueue,
  newEventId,
  queuedStatsForTests,
  resetStatsForTests,
  sendItemEvent,
  sendStats,
  sessionStateForTests,
  setStatsClient,
  startReadingSession,
  useReadingStats,
  useStatsEnabled,
  type StatsEvent,
} from "./statsSender";

let visible = true;
let focused = true;

/** A query client whose bootstrap answer holds the setting; `undefined` settings leaves the answer out (unknown). */
function client(settings?: Record<string, unknown>): QueryClient {
  // No garbage-collection timer: the tests count timers.
  const qc = new QueryClient({ defaultOptions: { queries: { gcTime: Infinity } } });
  if (settings) qc.setQueryData(keys.bootstrap, { ...bootstrap, settings });
  return qc;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
  visible = true;
  focused = true;
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (visible ? "visible" : "hidden") });
  vi.spyOn(document, "hasFocus").mockImplementation(() => focused);
  resetStatsForTests();
  setStatsClient(client({})); // known on
  authStore.set("in");
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
  localStorage.clear();
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete (document as { visibilityState?: unknown }).visibilityState;
  document.body.innerHTML = "";
  authStore.set("unknown");
  resetStatsForTests();
});

const sec = (n: number) => vi.advanceTimersByTime(n * 1000);
const setVisible = (v: boolean) => {
  visible = v;
  document.dispatchEvent(new Event("visibilitychange"));
};
const ID = /^[0-9a-f]{32}$/;
/** An event without its id, for comparing the rest. */
const bare = ({ event_id, ...rest }: StatsEvent) => {
  expect(event_id).toMatch(ID);
  return rest;
};

function session(over: Partial<Parameters<typeof startReadingSession>[0]> = {}) {
  const send = vi.fn<(e: StatsEvent[]) => void>();
  const stop = startReadingSession({ itemId: 1001, sessionKey: "k1", send, ...over });
  return { send, stop };
}
const sent = (send: ReturnType<typeof session>["send"]) => send.mock.calls.flatMap(([e]) => e);
const seconds = (send: ReturnType<typeof session>["send"]) => sent(send).filter((e) => e.kind === "read_time").reduce((n, e) => n + (e.value ?? 0), 0);

/** The article pane and, beside it, the list (the wide layout). */
function layout() {
  const pane = document.createElement("div");
  const scroller = document.createElement("div");
  const content = document.createElement("article");
  scroller.append(content);
  pane.append(scroller);
  const list = document.createElement("div");
  const row = document.createElement("a");
  row.href = "#";
  list.append(row);
  document.body.append(list, pane);
  return { pane, scroller, content, list, row };
}

describe("event ids", () => {
  it("are 128 random bits in hex, and fall back to Math.random without crypto", () => {
    const a = newEventId();
    expect(a).toMatch(ID);
    expect(newEventId()).not.toBe(a);
    vi.stubGlobal("crypto", undefined);
    const b = newEventId();
    expect(b).toMatch(ID);
    expect(newEventId()).not.toBe(b);
  });
});

describe("reading time", () => {
  it("counts whole active seconds and flushes every 15 s, each event with its own id, never resending", () => {
    const { send, stop } = session();
    sec(14);
    expect(send).not.toHaveBeenCalled();
    sec(1);
    expect(send.mock.lastCall?.[0].map(bare)).toEqual([{ kind: "read_time", item_id: 1001, session_key: "k1", value: 15 }]);
    sec(15);
    expect(send.mock.lastCall?.[0].map(bare)).toEqual([{ kind: "read_time", item_id: 1001, session_key: "k1", value: 15 }]);
    sec(5);
    stop(); // the rest goes out when the session ends
    expect(send.mock.lastCall?.[0].map(bare)).toEqual([{ kind: "read_time", item_id: 1001, session_key: "k1", value: 5 }]);
    expect(send).toHaveBeenCalledTimes(3);
    expect(new Set(sent(send).map((e) => e.event_id)).size).toBe(3);
    sec(60); // stopped: nothing more
    expect(send).toHaveBeenCalledTimes(3);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("a background tab (hidden) adds no time, flushes on the way out, and resumes when visible", () => {
    const { send, stop } = session();
    sec(5);
    setVisible(false);
    expect(send).toHaveBeenCalledTimes(1);
    expect(seconds(send)).toBe(5);
    sec(60);
    expect(seconds(send)).toBe(5);
    setVisible(true);
    sec(4);
    stop();
    expect(seconds(send)).toBe(9);
  });

  it("an unfocused window adds no time, and focus starts it", () => {
    focused = false;
    const { send, stop } = session();
    sec(45);
    expect(send).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0); // no tick while it cannot count
    focused = true;
    window.dispatchEvent(new Event("focus"));
    sec(3);
    stop();
    expect(seconds(send)).toBe(3);
  });

  it("stops at 2 minutes without activity and resumes on a key press with nothing focused", () => {
    const { send, stop } = session();
    sec(IDLE_CUTOFF_MS / 1000 + 100);
    expect(seconds(send)).toBe(IDLE_CUTOFF_MS / 1000 - 1);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
    sec(10);
    stop();
    expect(seconds(send)).toBe(IDLE_CUTOFF_MS / 1000 - 1 + 10);
  });

  it("the tick stops when idle (sending what it held) and starts again on activity", () => {
    const { pane } = layout();
    const { send, stop } = session({ pane });
    expect(vi.getTimerCount()).toBe(1);
    sec(IDLE_CUTOFF_MS / 1000 + 5);
    expect(vi.getTimerCount()).toBe(0);
    expect(seconds(send)).toBe(IDLE_CUTOFF_MS / 1000 - 1); // flushed when it paused
    sec(600);
    expect(vi.getTimerCount()).toBe(0);
    pane.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(vi.getTimerCount()).toBe(1);
    pane.dispatchEvent(new Event("pointermove", { bubbles: true })); // already running: no second timer
    expect(vi.getTimerCount()).toBe(1);
    sec(15);
    stop();
    expect(seconds(send)).toBe(IDLE_CUTOFF_MS / 1000 - 1 + 15);
  });

  it("touch and pointer events in the article pane keep it going", () => {
    const { pane, content } = layout();
    const { send, stop } = session({ pane });
    for (let i = 0; i < 10; i++) {
      sec(60);
      content.dispatchEvent(new Event(i % 2 ? "touchstart" : "pointerdown", { bubbles: true }));
    }
    stop();
    expect(seconds(send)).toBe(600);
  });

  it("activity over the list beside the article (the wide layout) does not count", () => {
    const { pane, scroller, list, row } = layout();
    const input = document.createElement("input");
    list.append(input);
    const { send, stop } = session({ pane, scroller });
    for (let i = 0; i < 5; i++) {
      sec(60);
      row.dispatchEvent(new Event("pointerdown", { bubbles: true }));
      row.dispatchEvent(new Event("pointermove", { bubbles: true }));
      row.dispatchEvent(new Event("touchstart", { bubbles: true }));
      row.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true })); // a key on a focused list row
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
      list.dispatchEvent(new Event("scroll")); // the list's own scrolling
      window.dispatchEvent(new Event("scroll"));
    }
    stop();
    expect(seconds(send)).toBe(IDLE_CUTOFF_MS / 1000 - 1);
  });

  it("the article's own scrolling counts as activity", () => {
    const { scroller } = layout();
    const { send, stop } = session({ scroller });
    for (let i = 0; i < 4; i++) {
      sec(60);
      scroller.dispatchEvent(new Event("scroll"));
    }
    stop();
    expect(seconds(send)).toBe(240);
  });

  it("splits what has piled up into events of at most 60 s", () => {
    const { send, stop } = session();
    sessionStateForTests("k1").pending = 130;
    stop();
    expect(sent(send).map(bare)).toEqual([
      { kind: "read_time", item_id: 1001, session_key: "k1", value: 60 },
      { kind: "read_time", item_id: 1001, session_key: "k1", value: 60 },
      { kind: "read_time", item_id: 1001, session_key: "k1", value: 10 },
    ]);
    expect(new Set(sent(send).map((e) => e.event_id)).size).toBe(3);
  });

  it("coming back to the tab without focus yet looks again at about 1 s and 3 s (no touch needed)", () => {
    const { send, stop } = session();
    sec(5);
    setVisible(false);
    focused = false; // on return, focus lands a moment after the visibilitychange
    setVisible(true);
    sec(0.5);
    focused = true; // no focus event, no touch
    sec(0.5); // the 1 s look finds it
    sec(10);
    stop();
    expect(seconds(send)).toBe(15);

    // Focus later than the first look: the 3 s one catches it.
    const b = session({ sessionKey: "k2" });
    setVisible(false);
    focused = false;
    setVisible(true);
    sec(2);
    expect(vi.getTimerCount()).toBe(1); // only the 3 s look is left, no tick
    focused = true;
    sec(1);
    sec(10);
    b.stop();
    expect(seconds(b.send)).toBe(10);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("the looks after coming back are cancelled by hiding again and by stop", () => {
    const { send, stop } = session();
    setVisible(false);
    focused = false;
    setVisible(true);
    expect(vi.getTimerCount()).toBe(2);
    setVisible(false); // hidden again before either look
    expect(vi.getTimerCount()).toBe(0);
    focused = true;
    sec(10);
    expect(send).not.toHaveBeenCalled();

    focused = false;
    setVisible(true);
    expect(vi.getTimerCount()).toBe(2);
    stop();
    expect(vi.getTimerCount()).toBe(0);
    focused = true;
    sec(10);
    expect(send).not.toHaveBeenCalled();
  });

  it("a session picked up again goes on counting, with fresh ids", () => {
    const a = session();
    sec(15);
    a.stop();
    const b = session();
    sec(15);
    b.stop();
    expect(seconds(a.send)).toBe(15);
    expect(seconds(b.send)).toBe(15);
    expect(sent(b.send)[0]?.event_id).not.toBe(sent(a.send)[0]?.event_id);
  });

  it("counts nothing once the session is older than the server accepts", () => {
    const { send, stop } = session();
    sessionStateForTests("k1").startedAt = Date.now() - SESSION_TTL_MS - 1000;
    sec(30);
    stop();
    expect(send).not.toHaveBeenCalled();
  });

  it("stops for good when the session passes its 12 h while running", () => {
    const { pane } = layout();
    const { send, stop } = session({ pane });
    sessionStateForTests("k1").startedAt = Date.now() - SESSION_TTL_MS + 5000;
    sec(10);
    expect(seconds(send)).toBe(5);
    expect(vi.getTimerCount()).toBe(0);
    pane.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(vi.getTimerCount()).toBe(0);
    sec(30);
    stop();
    expect(seconds(send)).toBe(5);
  });

  it("a hidden tab that stays hidden never sends anything", () => {
    const { send, stop } = session();
    setVisible(false);
    sec((FLUSH_EVERY_MS / 1000) * 3);
    stop();
    expect(send).not.toHaveBeenCalled();
  });
});

describe("scroll depth", () => {
  function scrollerAt(top: number, client = 500, height = 1000, attach = true) {
    const el = document.createElement("div");
    el.append(document.createElement("article"));
    Object.defineProperty(el, "clientHeight", { configurable: true, value: client });
    Object.defineProperty(el, "scrollHeight", { configurable: true, value: height });
    el.scrollTop = top;
    if (attach) document.body.append(el);
    return el;
  }
  const scrolls = (send: ReturnType<typeof session>["send"]) => sent(send).filter((e) => e.kind === "scroll").map((e) => e.value);
  const scrollTo = (el: HTMLElement, top: number) => {
    el.scrollTop = top;
    el.dispatchEvent(new Event("scroll"));
  };

  it("moves only on real scrolls, sends the deepest point once, and again only when it grows", () => {
    const el = scrollerAt(0);
    const { send, stop } = session({ scroller: el });
    sec(15);
    expect(scrolls(send)).toEqual([]); // nothing measured at start
    scrollTo(el, 250);
    sec(1);
    scrollTo(el, 0); // back up: the maximum stays
    sec(14);
    expect(scrolls(send)).toEqual([75]);
    sec(15);
    expect(scrolls(send)).toEqual([75]); // nothing grew
    scrollTo(el, 500);
    sec(1);
    stop();
    expect(scrolls(send)).toEqual([75, 100]);
    expect(sent(send).filter((e) => e.kind === "scroll").every((e) => ID.test(e.event_id))).toBe(true);
  });

  it("ignores the article's scroll events in a session's first 500 ms (the scroll to the top on an article change)", () => {
    const el = scrollerAt(500); // the previous article's position, about to be reset
    const { send, stop } = session({ scroller: el });
    vi.advanceTimersByTime(400);
    scrollTo(el, 0); // the view's own scrollTo({ top: 0 })
    scrollTo(el, 500);
    vi.advanceTimersToNextFrame();
    // Not activity: the idle cutoff runs from the open, so the clock stops one tick short of 120 s.
    sec(IDLE_CUTOFF_MS / 1000 + 5);
    expect(seconds(send)).toBe(IDLE_CUTOFF_MS / 1000 - 1);
    // Not depth either.
    expect(scrolls(send)).toEqual([]);
    stop();
    expect(scrolls(send)).toEqual([]);

    // Past the first 500 ms a scroll is the reader's: it counts and it measures.
    const el2 = scrollerAt(0);
    const b = session({ scroller: el2, sessionKey: "k2" });
    vi.advanceTimersByTime(600);
    scrollTo(el2, 250);
    vi.advanceTimersToNextFrame();
    sec(IDLE_CUTOFF_MS / 1000 + 5);
    b.stop();
    expect(seconds(b.send)).toBe(IDLE_CUTOFF_MS / 1000); // activity at 0.6 s keeps the 120th tick
    expect(scrolls(b.send)).toEqual([75]);
  });

  it("reads the layout once a frame, however many scroll events came in it", () => {
    const el = scrollerAt(0);
    let reads = 0;
    let top = 0;
    Object.defineProperty(el, "scrollTop", {
      configurable: true,
      get: () => (reads++, top),
      set: (v: number) => void (top = v),
    });
    const { stop } = session({ scroller: el });
    sec(1); // past the opening scroll-to-top grace
    for (let i = 1; i <= 10; i++) {
      top = i * 10;
      el.dispatchEvent(new Event("scroll"));
    }
    expect(reads).toBe(0);
    vi.advanceTimersToNextFrame();
    expect(reads).toBe(1);
    stop();
    expect(reads).toBe(1); // nothing measured at stop
  });

  it("never measures at stop: the scroller may already show the next article", () => {
    // The wide pane keeps its scroller: when the article changes, the old session stops with the next article's
    // (short) content in it. Measuring then would record 100 for an article that was never scrolled.
    const el = scrollerAt(0, 800, 3000);
    const { send, stop } = session({ scroller: el });
    sec(2);
    scrollTo(el, 400); // a real scroll, its frame not run yet
    Object.defineProperty(el, "scrollHeight", { configurable: true, value: 300 }); // the next article, clamped
    el.scrollTop = 0;
    stop();
    vi.advanceTimersToNextFrame();
    sec(1);
    expect(scrolls(send)).toEqual([]);
  });

  it("an article that fits the screen counts as seen in full, but only after 3 active seconds", () => {
    const el = scrollerAt(0, 800, 600);
    const early = session({ scroller: el, sessionKey: "early" });
    sec(FIT_AFTER_SECONDS - 1);
    early.stop();
    expect(scrolls(early.send)).toEqual([]); // not before 3 s

    const { send, stop } = session({ scroller: el });
    sec(FIT_AFTER_SECONDS);
    stop();
    expect(sent(send).map(bare)).toEqual([
      { kind: "read_time", item_id: 1001, session_key: "k1", value: 3 },
      { kind: "scroll", item_id: 1001, session_key: "k1", value: 100 },
    ]);
  });

  it("idle seconds do not count toward the 3", () => {
    const el = scrollerAt(0, 800, 600);
    focused = false;
    const { send, stop } = session({ scroller: el });
    sec(30);
    stop();
    expect(scrolls(send)).toEqual([]);
  });

  it("a scroller no longer in the document never counts as fitting", () => {
    const el = scrollerAt(0, 800, 600, false);
    const { send, stop } = session({ scroller: el });
    sec(20);
    stop();
    expect(scrolls(send)).toEqual([]);
  });

  it("looks again when the content changes size, and a later scroll only adds", () => {
    let fire: (() => void) | undefined;
    class RO {
      constructor(cb: () => void) {
        fire = cb;
      }
      observe() {}
      disconnect() {}
      unobserve() {}
    }
    vi.stubGlobal("ResizeObserver", RO);
    const el = scrollerAt(0, 800, 1200); // taller than the screen while it loads
    const { send, stop } = session({ scroller: el });
    sec(5);
    Object.defineProperty(el, "scrollHeight", { configurable: true, value: 700 }); // settled shorter
    sec(10); // the 15 s flush
    expect(scrolls(send)).toEqual([]); // no size change reported yet: not looked at again
    fire?.();
    sec(15);
    expect(scrolls(send)).toEqual([100]);
    scrollTo(el, 0);
    sec(1);
    stop();
    expect(scrolls(send)).toEqual([100]);
  });
});

describe("sending", () => {
  const ev: StatsEvent[] = [
    { kind: "read_time", item_id: 7, session_key: "s", value: 12, event_id: "a".repeat(32) },
    { kind: "scroll", item_id: 7, session_key: "s", value: 60, event_id: "b".repeat(32) },
  ];

  it("uses sendBeacon with a plain string body carrying the client", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    sendStats(ev);
    expect(beacon).toHaveBeenCalledTimes(1);
    expect(beacon.mock.calls[0]?.[0]).toBe("/api/stats/events");
    const body = beacon.mock.calls[0]?.[1];
    expect(typeof body).toBe("string");
    expect(JSON.parse(body as string)).toEqual({ client: "web", events: ev });
    expect(queuedStatsForTests()).toEqual([]);
  });

  it("queues the events, ids intact, when the browser refuses the beacon", () => {
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: vi.fn().mockReturnValue(false) });
    sendStats(ev);
    const q = queuedStatsForTests();
    expect(q).toHaveLength(1);
    expect(q[0]?.events).toEqual(ev);
    expect(q[0]?.client).toBe("web");
  });

  it("queues without trying while offline", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    offlineStore.set((s) => ({ ...s, online: false }));
    sendStats(ev);
    expect(beacon).not.toHaveBeenCalled();
    expect(queuedStatsForTests()).toHaveLength(1);
  });

  it.each([
    ["a network failure", () => Promise.reject(new TypeError("down")), true],
    ["429", () => Promise.resolve(new Response("", { status: 429 })), true],
    ["503", () => Promise.resolve(new Response("", { status: 503 })), true],
    ["an opaque redirect", () => Promise.resolve({ type: "opaqueredirect", status: 0 } as Response), true],
    ["401", () => Promise.resolve(new Response("", { status: 401 })), false],
    ["400", () => Promise.resolve(new Response("", { status: 400 })), false],
    ["204", () => Promise.resolve(new Response(null, { status: 204 })), false],
  ])("without sendBeacon, a keepalive fetch answered with %s %s", async (_, answer, queued) => {
    const f = vi.fn(answer);
    vi.stubGlobal("navigator", { onLine: true });
    vi.stubGlobal("fetch", f);
    sendStats(ev);
    expect(f).toHaveBeenCalledWith("/api/stats/events", expect.objectContaining({ method: "POST", keepalive: true, redirect: "manual" }));
    await vi.advanceTimersByTimeAsync(0);
    expect(queuedStatsForTests().flatMap((b) => b.events)).toEqual(queued ? ev : []);
  });

  it("open_original and share carry the item alone, with an id", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    sendItemEvent("share", "1001");
    const body = JSON.parse(beacon.mock.calls[0]?.[1] as string) as { client: string; events: StatsEvent[] };
    expect(body.client).toBe("web");
    expect(body.events.map(bare)).toEqual([{ kind: "share", item_id: 1001 }]);
  });

  it("with stats off sends nothing and queues nothing", () => {
    setStatsClient(client({ "stats.enabled": false }));
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    sendStats(ev);
    sendItemEvent("open_original", "5");
    expect(beacon).not.toHaveBeenCalled();
    expect(queuedStatsForTests()).toEqual([]);
  });

  it("while the setting is unknown nothing is sent: the events wait in the queue", () => {
    setStatsClient(client());
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    sendStats(ev);
    expect(beacon).not.toHaveBeenCalled();
    expect(queuedStatsForTests().flatMap((b) => b.events)).toEqual(ev);
  });
});

describe("the offline queue", () => {
  const ev = (n: number, over: Partial<StatsEvent> = {}): StatsEvent => ({ kind: "share", item_id: n, event_id: newEventId(), ...over });
  const refuseBeacon = () => vi.stubGlobal("navigator", { onLine: true, sendBeacon: vi.fn().mockReturnValue(false) });
  const ok = () => vi.fn(async () => new Response(null, { status: 204 }));
  const bodyOf = (f: ReturnType<typeof vi.fn>, i: number) => JSON.parse((f.mock.calls[i] as [string, RequestInit])[1].body as string) as { client: string; events: StatsEvent[] };
  const qKeys = () => Object.keys(localStorage).filter((k) => k.startsWith("kipple.stats.q.")).sort();

  it("stores each batch under its own key", () => {
    refuseBeacon();
    sendStats([ev(1)]);
    sendStats([ev(2), ev(3)]);
    expect(qKeys()).toHaveLength(2);
    expect(queuedStatsForTests().map((b) => b.events.map((e) => e.item_id))).toEqual([[1], [2, 3]]);
    expect(Number(qKeys()[0]?.split(".").pop())).toBeLessThan(Number(qKeys()[1]?.split(".").pop()));
  });

  it("deletes malformed entries and the old single-key queue on read", () => {
    localStorage.setItem("kipple.stats.queue.v1", JSON.stringify([{ id: 1, at: Date.now(), client: "web", events: [{ kind: "share", item_id: 1 }] }]));
    localStorage.setItem("kipple.stats.q.5", "{not json");
    localStorage.setItem("kipple.stats.q.6", JSON.stringify({ at: Date.now(), client: "web", events: [{ kind: "share", item_id: 1 }] })); // no event_id
    localStorage.setItem("kipple.stats.q.7", JSON.stringify({ at: Date.now(), client: "fever", events: [ev(1)] }));
    localStorage.setItem("kipple.stats.q.8", JSON.stringify({ at: Date.now(), client: "web", events: [] }));
    localStorage.setItem("kipple.stats.q.abc", JSON.stringify({ at: Date.now(), client: "web", events: [ev(1)] }));
    localStorage.setItem("kipple.stats.q.9", JSON.stringify({ at: Date.now(), client: "pwa", events: [ev(9)] }));
    localStorage.setItem("unrelated", "x");
    expect(queuedStatsForTests().map((b) => b.id)).toEqual([9]);
    expect(qKeys()).toEqual(["kipple.stats.q.9"]);
    expect(localStorage.getItem("kipple.stats.queue.v1")).toBeNull();
    expect(localStorage.getItem("unrelated")).toBe("x");
  });

  it("caps the stored events, dropping the oldest batches", () => {
    refuseBeacon();
    for (let i = 0; i < 30; i++) sendStats(Array.from({ length: 50 }, () => ev(i + 1)));
    const q = queuedStatsForTests();
    expect(q.reduce((n, b) => n + b.events.length, 0)).toBe(QUEUE_MAX_EVENTS);
    expect(q[0]?.events[0]?.item_id).toBe(11);
    expect(q[q.length - 1]?.events[0]?.item_id).toBe(30);
  });

  it("drops session events after 12 h and the others after 24 h", async () => {
    refuseBeacon();
    sendStats([ev(1, { kind: "read_time", session_key: "s", value: 5 }), ev(2)]);
    vi.setSystemTime(Date.now() + SESSION_TTL_MS + 1000);
    expect(queuedStatsForTests().flatMap((b) => b.events.map((e) => e.item_id))).toEqual([2]);
    const f = ok();
    vi.stubGlobal("fetch", f);
    await flushStatsQueue();
    expect(bodyOf(f, 0).events.map((e) => e.item_id)).toEqual([2]);

    sendStats([ev(3)]);
    vi.setSystemTime(Date.now() + ITEM_EVENT_TTL_MS + 1000);
    expect(queuedStatsForTests()).toEqual([]);
    expect(qKeys()).toEqual([]);
    await flushStatsQueue();
    expect(f).toHaveBeenCalledTimes(1);
  });

  it("is wiped by an explicit sign-out", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    sendStats([ev(2)]);
    expect(qKeys()).toHaveLength(2);
    await wipeOfflineData();
    expect(qKeys()).toEqual([]);
  });

  it("clearStatsQueue drops queued batches and unsent counts but keeps sending on", () => {
    refuseBeacon();
    sendStats([ev(1)]);
    sessionStateForTests("s").pending = 4;
    localStorage.setItem("kipple.stats.queue.v1", "[]");
    clearStatsQueue();
    expect(qKeys()).toEqual([]);
    expect(localStorage.getItem("kipple.stats.queue.v1")).toBeNull();
    expect(sessionStateForTests("s").pending).toBe(0);
    sendStats([ev(2)]); // not wiped: later events still queue
    expect(queuedStatsForTests().flatMap((b) => b.events.map((e) => e.item_id))).toEqual([2]);
  });

  it("joins queued batches into one request per client on reconnect, in order, then empties", async () => {
    const f = ok();
    vi.stubGlobal("fetch", f);
    const stop = initStatsQueue(client({}));
    offlineStore.set((s) => ({ ...s, online: false }));
    refuseBeacon();
    const a = [ev(1), ev(2)];
    const b = [ev(3)];
    sendStats(a);
    sendStats(b);
    expect(f).not.toHaveBeenCalled();
    offlineStore.set((s) => ({ ...s, online: true }));
    await vi.advanceTimersByTimeAsync(0);
    expect(f).toHaveBeenCalledTimes(1);
    expect(bodyOf(f, 0)).toEqual({ client: "web", events: [...a, ...b] });
    expect(queuedStatsForTests()).toEqual([]);
    stop();
  });

  it("keeps requests to 200 events and apart per client", async () => {
    const at = Date.now();
    const put = (id: number, client: string, n: number) =>
      localStorage.setItem(`kipple.stats.q.${id}`, JSON.stringify({ at, client, events: Array.from({ length: n }, () => ev(id)) }));
    put(1, "web", 100);
    put(2, "pwa", 10);
    put(3, "web", 100);
    put(4, "web", 150);
    put(5, "web", 30);
    const f = ok();
    vi.stubGlobal("fetch", f);
    await flushStatsQueue();
    const bodies = f.mock.calls.map((_, i) => bodyOf(f, i));
    expect(bodies.map((b) => [b.client, b.events.length])).toEqual([
      ["web", 200],
      ["pwa", 10],
      ["web", 180],
    ]);
    expect(bodies.every((b) => b.events.length <= 200)).toBe(true);
    expect(qKeys()).toEqual([]);
  });

  it.each([
    ["a network failure", () => Promise.reject(new TypeError("down"))],
    ["503", () => Promise.resolve(new Response("", { status: 503 }))],
    ["429", () => Promise.resolve(new Response("", { status: 429 }))],
    ["401", () => Promise.resolve(new Response("", { status: 401 }))],
    ["an opaque redirect", () => Promise.resolve({ type: "opaqueredirect", status: 0, headers: new Headers() } as Response)],
  ])("stops on %s and keeps every batch", async (_, answer) => {
    refuseBeacon();
    sendStats([ev(1)]);
    sendStats([ev(2)]);
    const f = vi.fn(answer);
    vi.stubGlobal("fetch", f);
    await flushStatsQueue();
    expect(f).toHaveBeenCalledTimes(1);
    expect(qKeys()).toHaveLength(2);
  });

  it("drops what the server refuses for good", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 400 })));
    await flushStatsQueue();
    expect(qKeys()).toEqual([]);
  });

  it("gives up a request after 20 s without an answer and keeps the batch", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    const f = vi.fn(
      (_: string, init: RequestInit) =>
        new Promise<Response>((_res, rej) => init.signal?.addEventListener("abort", () => rej(new DOMException("aborted", "AbortError")))),
    );
    vi.stubGlobal("fetch", f);
    let done = false;
    void flushStatsQueue().then(() => (done = true));
    await vi.advanceTimersByTimeAsync(FLUSH_TIMEOUT_MS - 100);
    expect(done).toBe(false);
    await vi.advanceTimersByTimeAsync(200);
    expect(done).toBe(true);
    expect(qKeys()).toHaveLength(1);
  });

  it("keeps each event's id through the queue and every retry", async () => {
    refuseBeacon();
    sendItemEvent("share", "42");
    const [queued] = queuedStatsForTests().flatMap((b) => b.events);
    expect(queued?.event_id).toMatch(ID);
    const fail = vi.fn(async () => new Response("", { status: 503 }));
    vi.stubGlobal("fetch", fail);
    await flushStatsQueue();
    const f = ok();
    vi.stubGlobal("fetch", f);
    await flushStatsQueue();
    const first = bodyOf(fail, 0).events;
    const second = bodyOf(f, 0).events;
    expect(first.map((e) => e.event_id)).toEqual([queued?.event_id]);
    expect(second.map((e) => e.event_id)).toEqual([queued?.event_id]);
  });

  it("sends nothing while signed out", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    authStore.set("out");
    const f = ok();
    vi.stubGlobal("fetch", f);
    await flushStatsQueue();
    expect(f).not.toHaveBeenCalled();
    expect(qKeys()).toHaveLength(1);
  });

  it("empties the queue unsent when stats are off", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    const f = vi.fn();
    vi.stubGlobal("fetch", f);
    const stop = initStatsQueue(client({ "stats.enabled": false }));
    await vi.advanceTimersByTimeAsync(0);
    expect(f).not.toHaveBeenCalled();
    expect(queuedStatsForTests()).toEqual([]);
    stop();
  });

  it("at launch waits for the setting to be known: unknown keeps the queue, and it goes once known", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    const f = ok();
    vi.stubGlobal("fetch", f);
    const qc = client();
    const stop = initStatsQueue(qc);
    await vi.advanceTimersByTimeAsync(0);
    await flushStatsQueue();
    expect(f).not.toHaveBeenCalled();
    expect(qKeys()).toHaveLength(1);
    qc.setQueryData(keys.bootstrap, { ...bootstrap, settings: {} });
    await vi.advanceTimersByTimeAsync(0);
    expect(f).toHaveBeenCalledTimes(1);
    expect(qKeys()).toEqual([]);
    stop();
  });

  it("a known off arriving later empties the queue", async () => {
    refuseBeacon();
    setStatsClient(client());
    sendStats([ev(1)]);
    const qc = client();
    const stop = initStatsQueue(qc);
    await vi.advanceTimersByTimeAsync(0);
    expect(qKeys()).toHaveLength(1);
    qc.setQueryData(keys.bootstrap, { ...bootstrap, settings: { "stats.enabled": false } });
    await vi.advanceTimersByTimeAsync(0);
    expect(qKeys()).toEqual([]);
    stop();
  });

  it("two calls in one tab share one run", async () => {
    refuseBeacon();
    sendStats([ev(1)]);
    const f = ok();
    vi.stubGlobal("fetch", f);
    await Promise.all([flushStatsQueue(), flushStatsQueue()]);
    expect(f).toHaveBeenCalledTimes(1);
  });
});

describe("two tabs", () => {
  /** A minimal Web Locks API shared by both "tabs". */
  function locks() {
    const held = new Set<string>();
    return {
      async request(name: string, opts: { ifAvailable?: boolean }, cb: (lock: { name: string } | null) => Promise<void>) {
        if (held.has(name)) {
          if (opts.ifAvailable) return cb(null);
          throw new Error("not modelled");
        }
        held.add(name);
        try {
          return await cb({ name });
        } finally {
          held.delete(name);
        }
      },
    };
  }

  it("flushing at the same time send the queue once (the lock)", async () => {
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: vi.fn().mockReturnValue(false), locks: locks() });
    // Two copies of the module, as two tabs would have, sharing localStorage and the lock manager.
    vi.resetModules();
    const tabA = await import("./statsSender");
    const clientA = await import("@/api/client");
    vi.resetModules();
    const tabB = await import("./statsSender");
    const clientB = await import("@/api/client");
    clientA.authStore.set("in");
    clientB.authStore.set("in");
    tabA.setStatsClient(client({}));
    tabB.setStatsClient(client({}));
    tabA.sendStats([{ kind: "share", item_id: 1, event_id: newEventId() }]);
    tabB.sendStats([{ kind: "share", item_id: 2, event_id: newEventId() }]);
    let answer: ((r: Response) => void) | undefined;
    const f = vi.fn(() => new Promise<Response>((r) => (answer = r)));
    vi.stubGlobal("fetch", f);
    const a = tabA.flushStatsQueue();
    await vi.advanceTimersByTimeAsync(0);
    const b = tabB.flushStatsQueue(); // A holds the lock: B skips its run
    await vi.advanceTimersByTimeAsync(0);
    expect(f).toHaveBeenCalledTimes(1);
    answer?.(new Response(null, { status: 204 }));
    await Promise.all([a, b]);
    expect(f).toHaveBeenCalledTimes(1);
    const sentIds = (JSON.parse((f.mock.calls[0] as unknown as [string, RequestInit])[1].body as string) as { events: StatsEvent[] }).events.map((e) => e.item_id);
    expect([...sentIds].sort()).toEqual([1, 2]); // both tabs' batches, in one request (same millisecond: either order)
    expect(queuedStatsForTests()).toEqual([]);
  });
});

describe("the hook", () => {
  function wrap(settings?: Record<string, unknown>) {
    const qc = client(settings);
    setStatsClient(qc);
    const W = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    return { qc, W };
  }
  const bodies = (beacon: ReturnType<typeof vi.fn>) => beacon.mock.calls.map(([, b]) => JSON.parse(b as string) as { events: StatsEvent[] });
  const events = (beacon: ReturnType<typeof vi.fn>) => bodies(beacon).flatMap((b) => b.events);

  it("sends a session's time on the timer and when the article changes", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { W } = wrap({});
    const { pane, scroller } = layout();
    const { rerender } = renderHook((p: { id: string; key: string }) => useReadingStats({ itemId: p.id, sessionKey: p.key, enabled: true, scroller, pane }), {
      wrapper: W,
      initialProps: { id: "1001", key: "a" },
    });
    act(() => void sec(20));
    rerender({ id: "1002", key: "b" }); // the route moved to another article: the first one is flushed
    expect(events(beacon).map(bare)).toEqual([
      { kind: "read_time", item_id: 1001, session_key: "a", value: 15 },
      { kind: "read_time", item_id: 1001, session_key: "a", value: 5 },
    ]);
  });

  it("follows a new scroller element (the error screen's Try again): listeners move to it", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { W } = wrap({});
    const first = document.createElement("div");
    const second = document.createElement("div");
    document.body.append(first, second);
    const { rerender } = renderHook((p: { el: HTMLElement | null }) => useReadingStats({ itemId: "1001", sessionKey: "a", enabled: true, scroller: p.el, pane: p.el }), {
      wrapper: W,
      initialProps: { el: null as HTMLElement | null },
    });
    rerender({ el: first });
    first.remove(); // the error block replaced the article
    rerender({ el: null });
    rerender({ el: second }); // Try again: a new scroller
    for (let i = 0; i < 4; i++) {
      act(() => void sec(60));
      act(() => void second.dispatchEvent(new Event("scroll")));
    }
    act(() => void sec(15));
    const counted = () => events(beacon).filter((e) => e.kind === "read_time").reduce((n, e) => n + (e.value ?? 0), 0);
    expect(counted()).toBe(255);
    // The old element is not listened to: its scrolling keeps nothing alive.
    act(() => void sec(IDLE_CUTOFF_MS / 1000)); // idle now (the last activity was at 240 s)
    const before = counted();
    expect(before).toBe(IDLE_CUTOFF_MS / 1000 - 1 + 240);
    for (let i = 0; i < 3; i++) {
      act(() => void first.dispatchEvent(new Event("scroll")));
      act(() => void sec(60));
    }
    expect(counted()).toBe(before);
  });

  it("starts nothing and sends nothing when stats are off", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { W } = wrap({ "stats.enabled": false });
    const { result } = renderHook(
      () => {
        const on = useStatsEnabled();
        useReadingStats({ itemId: "1001", sessionKey: "a", enabled: on, scroller: null });
        return on;
      },
      { wrapper: W },
    );
    expect(result.current).toBe(false);
    act(() => void sec(60));
    expect(beacon).not.toHaveBeenCalled();
  });

  it("is off while the setting is unknown", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { W } = wrap();
    const { result } = renderHook(
      () => {
        const on = useStatsEnabled();
        useReadingStats({ itemId: "1001", sessionKey: "a", enabled: on, scroller: null });
        return on;
      },
      { wrapper: W },
    );
    expect(result.current).toBe(false);
    act(() => void sec(60));
    expect(beacon).not.toHaveBeenCalled();
    expect(queuedStatsForTests()).toEqual([]);
  });

  it("follows the setting being switched off and on", async () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { qc, W } = wrap({});
    const { pane, scroller } = layout();
    const { result } = renderHook(
      () => {
        const on = useStatsEnabled();
        useReadingStats({ itemId: "1001", sessionKey: "a", enabled: on, scroller, pane });
        return on;
      },
      { wrapper: W },
    );
    expect(result.current).toBe(true);
    act(() => void sec(5));
    // Switched off (the settings PATCH updates this cache): the session ends and its last seconds are not sent.
    await act(async () => {
      qc.setQueryData(keys.bootstrap, { ...bootstrap, settings: { "stats.enabled": false } });
      await vi.advanceTimersByTimeAsync(0); // react-query notifies from a timer
    });
    expect(result.current).toBe(false);
    act(() => void sec(60));
    expect(beacon).not.toHaveBeenCalled();
    expect(queuedStatsForTests()).toEqual([]);
    await act(async () => {
      qc.setQueryData(keys.bootstrap, { ...bootstrap, settings: { "stats.enabled": true } });
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => void sec(15));
    expect(events(beacon).map(bare)).toEqual([{ kind: "read_time", item_id: 1001, session_key: "a", value: 15 }]);
  });

  it("without a session key (an offline open) counts nothing", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { W } = wrap({});
    const { pane, scroller } = layout();
    renderHook(() => useReadingStats({ itemId: "1001", sessionKey: "", enabled: true, scroller, pane }), { wrapper: W });
    act(() => void sec(60));
    expect(beacon).not.toHaveBeenCalled();
  });

  it("counts nothing without both the scroller and the pane, and the error screen ends a running session", () => {
    const beacon = vi.fn().mockReturnValue(true);
    vi.stubGlobal("navigator", { onLine: true, sendBeacon: beacon });
    const { W } = wrap({});
    const { pane, scroller } = layout();
    type P = { scroller: HTMLElement | null; pane: HTMLElement | null };
    const { rerender } = renderHook((p: P) => useReadingStats({ itemId: "1001", sessionKey: "a", enabled: true, ...p }), {
      wrapper: W,
      initialProps: { scroller: null, pane: null } as P,
    });
    act(() => void sec(30));
    expect(vi.getTimerCount()).toBe(0);
    rerender({ scroller, pane: null });
    act(() => void sec(30));
    rerender({ scroller: null, pane });
    act(() => void sec(30));
    expect(beacon).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);

    // The article is on screen: it counts.
    rerender({ scroller, pane });
    act(() => void sec(5));
    // A background refetch fails: the error screen replaces the article and the callback refs go null.
    rerender({ scroller: null, pane: null });
    const counted = () => events(beacon).filter((e) => e.kind === "read_time").reduce((n, e) => n + (e.value ?? 0), 0);
    expect(counted()).toBe(5); // sent when it ended
    act(() => void sec(IDLE_CUTOFF_MS / 1000));
    expect(counted()).toBe(5);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("sign-out", () => {
  const ev = (n: number): StatsEvent => ({ kind: "share", item_id: n, event_id: newEventId() });
  const refuseBeacon = () => vi.stubGlobal("navigator", { onLine: true, sendBeacon: vi.fn().mockReturnValue(false) });

  it("a session ended by the cleared cache after the wipe queues nothing (the sign-out order)", async () => {
    refuseBeacon();
    const qc = client({});
    setStatsClient(qc);
    const W = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { pane, scroller } = layout();
    renderHook(
      () => {
        const on = useStatsEnabled();
        useReadingStats({ itemId: "1001", sessionKey: "a", enabled: on, scroller, pane });
      },
      { wrapper: W },
    );
    act(() => void sec(20)); // 15 s flushed (refused beacon: queued), 5 s still held by the session
    expect(queuedStatsForTests()).toHaveLength(1);
    // AccountSection's signOut: clear the cache, wipe, then the sign-in screen. React has not re-rendered yet, so
    // the session is still running when the wipe happens and only stops afterwards.
    qc.clear();
    await wipeOfflineData();
    authStore.set("out");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    act(() => void sec(30));
    expect(queuedStatsForTests()).toEqual([]);
    expect(Object.keys(localStorage).filter((k) => k.startsWith("kipple.stats.q."))).toEqual([]);
  });

  it("between the wipe and the next sign-in nothing is queued, even with auth still 'in' and the setting unknown", async () => {
    refuseBeacon();
    setStatsClient(client()); // the cleared cache: unknown
    await wipeOfflineData(); // auth is still "in" here (signOut sets "out" after)
    sendStats([ev(1)]);
    expect(queuedStatsForTests()).toEqual([]);
    authStore.set("out");
    sendStats([ev(2)]);
    expect(queuedStatsForTests()).toEqual([]);
    // Signing in again ends the wipe: unknown at that point queues as at launch.
    authStore.set("in");
    sendStats([ev(3)]);
    expect(queuedStatsForTests().flatMap((b) => b.events.map((e) => e.item_id))).toEqual([3]);
  });

  it("a keepalive answered after the wipe is not queued", async () => {
    let answer: ((r: Response) => void) | undefined;
    vi.stubGlobal("navigator", { onLine: true });
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((r) => (answer = r))));
    sendStats([ev(1)]);
    await wipeOfflineData();
    answer?.(new Response("", { status: 503 }));
    await vi.advanceTimersByTimeAsync(0);
    expect(queuedStatsForTests()).toEqual([]);
  });

  it("signed out (an expired session too), new events are dropped, not queued; what was queued stays", () => {
    refuseBeacon();
    sendStats([ev(1)]);
    authStore.set("out");
    sendStats([ev(2)]);
    setStatsClient(client()); // unknown as well
    sendStats([ev(3)]);
    expect(queuedStatsForTests().flatMap((b) => b.events.map((e) => e.item_id))).toEqual([1]);
  });

  it("not signed out, an unknown setting at launch still queues", () => {
    refuseBeacon();
    authStore.set("unknown");
    setStatsClient(client());
    sendStats([ev(1)]);
    expect(queuedStatsForTests().flatMap((b) => b.events.map((e) => e.item_id))).toEqual([1]);
  });
});
