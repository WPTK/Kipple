import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { keys } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { resetDevicePrefs } from "./devicePrefs";
import { FAVORITES_KEY, useFavorites } from "./favorites";

const boot: Bootstrap = {
  ...bootstrap,
  settings: { [FAVORITES_KEY]: [] },
  feeds: [1, 2, 3].map((n) => ({ ...bootstrap.feeds[0]!, id: String(n), title: `Feed ${n}` })),
};

function setup(routes: Parameters<typeof mockFetch>[0]) {
  const { calls } = mockFetch({ "GET /api/bootstrap": () => json(boot), ...routes });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(keys.bootstrap, boot);
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const hook = renderHook(() => useFavorites(), { wrapper });
  return { qc, hook, calls };
}
const patches = (calls: { method: string }[]) => calls.filter((c) => c.method === "PATCH");
const rejected = (message: string) => json({ error: "invalid_settings", issues: [{ key: FAVORITES_KEY, message }], keys: [FAVORITES_KEY] }, 400);

beforeEach(() => {
  resetDevicePrefs();
  clearToasts();
});
afterEach(() => vi.unstubAllGlobals());

describe("favorites sync", () => {
  it("a refused save is an error, not a switch to this device: the next save still goes to the server", async () => {
    let n = 0;
    const { hook, calls } = setup({
      "PATCH /api/settings": (_u, init) => (++n === 1 ? rejected("too many items") : json({ values: { [FAVORITES_KEY]: JSON.parse(String(init?.body))[FAVORITES_KEY] } })),
    });
    let ok = true;
    await act(async () => void (ok = await hook.result.current.set([{ t: "feed", id: "1" }])));
    expect(ok).toBe(false);
    expect(hook.result.current.favorites).toEqual([]);
    await act(async () => void (ok = await hook.result.current.set([{ t: "feed", id: "2" }])));
    expect(ok).toBe(true);
    expect(patches(calls)).toHaveLength(2);
    expect(hook.result.current.favorites).toEqual([{ t: "feed", id: "2" }]);
  });

  it("an unknown-setting answer is just another refused save: undone, and the next save still goes to the server", async () => {
    const { hook, calls } = setup({ "PATCH /api/settings": () => rejected("unknown setting") });
    let ok = true;
    await act(async () => void (ok = await hook.result.current.set([{ t: "feed", id: "1" }])));
    expect(ok).toBe(false);
    expect(hook.result.current.favorites).toEqual([]);
    await act(async () => void (await hook.result.current.set([{ t: "feed", id: "1" }])));
    expect(patches(calls)).toHaveLength(2);
  });

  it("refuses the 501st favorite instead of silently dropping it", async () => {
    const { hook, calls } = setup({ "PATCH /api/settings": () => json({ values: {} }) });
    const many = Array.from({ length: 501 }, (_, i) => ({ t: "feed" as const, id: String(i + 1) }));
    let ok = true;
    await act(async () => void (ok = await hook.result.current.set(many)));
    expect(ok).toBe(false);
    expect(patches(calls)).toHaveLength(0);
    await act(async () => void (ok = await hook.result.current.set(many.slice(0, 500))));
    expect(ok).toBe(true);
  });

  it("the newest save wins when replies arrive out of order", async () => {
    const releases: (() => void)[] = [];
    const { hook, qc } = setup({
      "PATCH /api/settings": (_u, init) =>
        new Promise((resolve) => {
          const list = JSON.parse(String(init?.body))[FAVORITES_KEY];
          releases.push(() => resolve(json({ values: { [FAVORITES_KEY]: list } })));
        }),
    });
    let a!: Promise<boolean>, b!: Promise<boolean>;
    act(() => {
      a = hook.result.current.set([{ t: "feed", id: "1" }]);
      b = hook.result.current.set([{ t: "feed", id: "1" }, { t: "feed", id: "2" }]);
    });
    await waitFor(() => expect(releases).toHaveLength(2));
    // The newer reply first, then the older one late.
    await act(async () => {
      releases[1]!();
      await b;
      releases[0]!();
      await a;
    });
    const shown = qc.getQueryData<Bootstrap>(keys.bootstrap)?.settings[FAVORITES_KEY];
    expect(shown).toEqual([{ t: "feed", id: "1" }, { t: "feed", id: "2" }]);
  });

  it("a failed save puts back only the favorites, not a stale copy of the whole bootstrap", async () => {
    let fail!: () => void;
    const { hook, qc } = setup({
      "PATCH /api/settings": () =>
        new Promise((resolve) => {
          fail = () => resolve(json({ error: "boom" }, 500));
        }),
    });
    let p!: Promise<boolean>;
    act(() => void (p = hook.result.current.set([{ t: "feed", id: "3" }])));
    await waitFor(() => expect(fail).toBeTypeOf("function"));
    // Meanwhile the app learns something else (a new unread count).
    act(() => void qc.setQueryData<Bootstrap>(keys.bootstrap, (old) => (old ? { ...old, counts: { unread: 42, starred: 0 } } : old)));
    await act(async () => {
      fail();
      await p;
    });
    const now = qc.getQueryData<Bootstrap>(keys.bootstrap);
    expect(now?.counts.unread).toBe(42);
    expect(now?.settings[FAVORITES_KEY]).toEqual([]);
  });
});
