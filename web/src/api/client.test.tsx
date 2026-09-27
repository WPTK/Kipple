import { describe, expect, it, beforeEach, vi } from "vitest";
import { renderHook, waitFor, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ApiError, api, authStore, buildPath, clientKind } from "./client";
import { flattenItems, parseScopeKey, scopeKey, useItems } from "./queries";
import { card, json, mockFetch, pageOf } from "@/test/mockApi";

beforeEach(() => authStore.set("unknown"));

describe("api()", () => {
  it("sends X-Kipple-Client: web and same-origin credentials", async () => {
    const { calls } = mockFetch({ "GET /api/status": () => json({ runs: [] }) });
    await api("/api/status");
    const headers = calls[0]?.init?.headers as Record<string, string>;
    expect(headers["X-Kipple-Client"]).toBe("web");
    expect(calls[0]?.init?.credentials).toBe("same-origin");
    expect(authStore.get()).toBe("in");
  });

  it("sends pwa when running standalone", () => {
    vi.spyOn(window, "matchMedia").mockImplementation(
      (q) => ({ matches: q.includes("standalone"), media: q, addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList,
    );
    expect(clientKind()).toBe("pwa");
  });

  it("serializes a JSON body and returns undefined for 204", async () => {
    const { calls } = mockFetch({ "POST /api/auth/login": () => new Response(null, { status: 204 }) });
    const out = await api("/api/auth/login", { method: "POST", body: { username: "a", password: "b" } });
    expect(out).toBeUndefined();
    expect(calls[0]?.init?.body).toBe('{"username":"a","password":"b"}');
    expect((calls[0]?.init?.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it("401 flips the auth state to out and throws ApiError", async () => {
    mockFetch({ "GET /api/bootstrap": () => json({ error: "auth" }, 401) });
    await expect(api("/api/bootstrap")).rejects.toMatchObject({ status: 401, code: "auth" });
    expect(authStore.get()).toBe("out");
  });

  it("retries a 503 maintenance answer after its Retry-After, and gives up after the wait budget", async () => {
    vi.useFakeTimers();
    try {
      let n = 0;
      const busy = () => new Response(JSON.stringify({ error: "maintenance" }), { status: 503, headers: { "Retry-After": "2" } });
      const { calls } = mockFetch({ "PUT /api/items/1/star": () => (n++ < 2 ? busy() : new Response(null, { status: 204 })) });
      const p = api("/api/items/1/star", { method: "PUT", body: { starred: true } });
      await vi.advanceTimersByTimeAsync(4_100);
      await expect(p).resolves.toBeUndefined();
      expect(calls).toHaveLength(3);

      mockFetch({ "PUT /api/items/2/star": busy });
      const q = api("/api/items/2/star", { method: "PUT", body: { starred: true } }).catch((e: unknown) => e);
      await vi.advanceTimersByTimeAsync(61_000);
      expect(await q).toMatchObject({ status: 503, code: "maintenance" });
    } finally {
      vi.useRealTimers();
    }
  });

  it("maps error bodies and network failures", async () => {
    mockFetch({ "GET /api/x": () => json({ error: "origin" }, 403) });
    await expect(api("/api/x")).rejects.toMatchObject({ status: 403, code: "origin" });
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("offline")));
    const e = await api("/api/x").catch((err: unknown) => err);
    expect(e).toBeInstanceOf(ApiError);
    expect((e as ApiError).status).toBe(0);
  });

  it("buildPath drops empty params", () => {
    expect(buildPath("/api/items", { view: "unread", feed: undefined, q: "", limit: 50 })).toBe("/api/items?view=unread&limit=50");
  });
});

describe("scope keys", () => {
  it("round-trips, including search text with separators", () => {
    const s = { view: "all" as const, feed: "12", q: "a|b c" };
    expect(parseScopeKey(scopeKey(s))).toEqual(s);
    expect(parseScopeKey(null)).toEqual({ view: "unread" });
  });

  it("a malformed search part falls back to the default scope instead of throwing", () => {
    expect(() => parseScopeKey("all|feed:3|q:%E0%A4")).not.toThrow();
    expect(parseScopeKey("all|feed:3|q:%")).toEqual({ view: "unread" });
  });
});

describe("useItems cursor paging", () => {
  it("follows next_cursor and stops when it is null", async () => {
    const { calls } = mockFetch({
      "GET /api/items": (url) => {
        const cursor = url.searchParams.get("cursor");
        if (!cursor) return json(pageOf([card(1), card(2)], "CUR1"));
        if (cursor === "CUR1") return json(pageOf([card(3)], null));
        throw new Error("unexpected cursor " + cursor);
      },
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useItems({ view: "unread", feed: "7" }), { wrapper });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(flattenItems(result.current.data)).toHaveLength(2);
    expect(result.current.hasNextPage).toBe(true);
    expect(calls[0]?.url.searchParams.get("view")).toBe("unread");
    expect(calls[0]?.url.searchParams.get("feed")).toBe("7");
    expect(calls[0]?.url.searchParams.get("limit")).toBe("50");

    await act(async () => {
      await result.current.fetchNextPage();
    });
    expect(calls[1]?.url.searchParams.get("cursor")).toBe("CUR1");
    await waitFor(() => expect(flattenItems(result.current.data).map((i) => i.id)).toEqual(["1001", "1002", "1003"]));
    expect(result.current.hasNextPage).toBe(false);
  });
});
