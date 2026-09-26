import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { card, json, mockFetch } from "@/test/mockApi";
import * as toasts from "@/shell/toasts";
import { offlineStore } from "@/lib/offlineState";
import { markScrolledPast, resetScrollReadForTests, SCROLL_RETRY_MS } from "./ListPane";

/** What fetch gives for `redirect: "manual"` when the access proxy sends the request to its login page. */
const opaqueRedirect = () => ({ type: "opaqueredirect", status: 0, ok: false, headers: new Headers() }) as unknown as Response;

beforeEach(() => {
  resetScrollReadForTests();
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
});

describe("mark read while scrolling", () => {
  it("sends each passed unread row once, and retries rows whose request failed after a pause", async () => {
    vi.spyOn(toasts, "toast").mockImplementation(() => 0);
    let fail = true;
    const { calls } = mockFetch({
      "POST /api/items/mark-read": (_u, init) => (fail ? json({ error: "internal" }, 500) : json({ changed: JSON.parse(String(init?.body)).ids, restored: [] })),
    });
    const qc = new QueryClient();
    const sent = new Set<string>();
    const passed = [card(1), card(2), card(3, { read: true })];

    await markScrolledPast(qc, passed, sent, 1_000);
    expect(sent.size).toBe(0); // the failure forgot them, so a later settle tries again

    fail = false;
    await markScrolledPast(qc, passed, sent, 1_000 + SCROLL_RETRY_MS);
    await markScrolledPast(qc, passed, sent, 2_000 + SCROLL_RETRY_MS); // already sent: nothing new
    const bodies = calls.map((c) => JSON.parse(String(c.init?.body)));
    expect(bodies.map((b) => b.ids)).toEqual([
      ["1001", "1002"],
      ["1001", "1002"],
    ]);
    expect(bodies.every((b) => b.reason === "scroll")).toBe(true);
    expect([...sent].sort()).toEqual(["1001", "1002"]);
  });

  it("a failing server is not asked again at every scroll pause, and the failure is told once", async () => {
    const toast = vi.spyOn(toasts, "toast").mockImplementation(() => 0);
    const { calls } = mockFetch({ "POST /api/items/mark-read": () => json({ error: "internal" }, 500) });
    const qc = new QueryClient();
    const sent = new Set<string>();
    // Five scroll pauses a second apart, then more after the pause, each with another row scrolled past.
    for (let i = 0; i < 5; i++) await markScrolledPast(qc, [card(1), card(2 + i)], sent, 10_000 + i * 1000);
    expect(calls).toHaveLength(1);
    await markScrolledPast(qc, [card(1)], sent, 10_000 + SCROLL_RETRY_MS);
    expect(calls).toHaveLength(2); // retried after the pause
    expect(toast).toHaveBeenCalledTimes(1); // one failure streak, one message

    // A success ends the streak: the next failure is told again.
    mockFetch({ "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }) });
    await markScrolledPast(qc, [card(1)], sent, 10_000 + 3 * SCROLL_RETRY_MS);
    mockFetch({ "POST /api/items/mark-read": () => json({ error: "internal" }, 500) });
    await markScrolledPast(qc, [card(9)], sent, 10_000 + 4 * SCROLL_RETRY_MS);
    expect(toast).toHaveBeenCalledTimes(2);
  });

  it("an expired sign-in is not retried: only a reload helps", async () => {
    const toast = vi.spyOn(toasts, "toast").mockImplementation(() => 0);
    const { calls } = mockFetch({ "POST /api/items/mark-read": opaqueRedirect });
    const qc = new QueryClient();
    const sent = new Set<string>();
    await markScrolledPast(qc, [card(1)], sent, 50_000);
    for (let i = 1; i <= 3; i++) await markScrolledPast(qc, [card(1), card(1 + i)], sent, 50_000 + i * 2 * SCROLL_RETRY_MS);
    expect(calls).toHaveLength(1);
    expect(toast).toHaveBeenCalledTimes(1);
  });

  it("does not send a row twice while its request is still out", async () => {
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    const { calls } = mockFetch({
      "POST /api/items/mark-read": async (_u, init) => {
        await held;
        return json({ changed: JSON.parse(String(init?.body)).ids, restored: [] });
      },
    });
    const qc = new QueryClient();
    const sent = new Set<string>();
    const first = markScrolledPast(qc, [card(1)], sent);
    await markScrolledPast(qc, [card(1)], sent);
    release();
    await first;
    expect(calls).toHaveLength(1);
  });
});
