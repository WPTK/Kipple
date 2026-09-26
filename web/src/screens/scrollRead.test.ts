import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { card, json, mockFetch } from "@/test/mockApi";
import * as toasts from "@/shell/toasts";
import { markScrolledPast } from "./ListPane";

describe("mark read while scrolling", () => {
  it("sends each passed unread row once, and retries rows whose request failed", async () => {
    vi.spyOn(toasts, "toast").mockImplementation(() => 0);
    let fail = true;
    const { calls } = mockFetch({
      "POST /api/items/mark-read": (_u, init) => (fail ? json({ error: "internal" }, 500) : json({ changed: JSON.parse(String(init?.body)).ids, restored: [] })),
    });
    const qc = new QueryClient();
    const sent = new Set<string>();
    const passed = [card(1), card(2), card(3, { read: true })];

    await markScrolledPast(qc, passed, sent);
    expect(sent.size).toBe(0); // the failure forgot them, so the next settle tries again

    fail = false;
    await markScrolledPast(qc, passed, sent);
    await markScrolledPast(qc, passed, sent); // already sent: nothing new
    const bodies = calls.map((c) => JSON.parse(String(c.init?.body)));
    expect(bodies.map((b) => b.ids)).toEqual([
      ["1001", "1002"],
      ["1001", "1002"],
    ]);
    expect(bodies.every((b) => b.reason === "scroll")).toBe(true);
    expect([...sent].sort()).toEqual(["1001", "1002"]);
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
