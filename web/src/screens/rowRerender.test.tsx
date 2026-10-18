import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

// Counts how often each row's body renders. SwipeRow sits directly inside the memoized ListRow, so it renders exactly
// when ListRow does.
const renders = vi.hoisted(() => new Map<string, number>());
vi.mock("@/gestures/SwipeRow", () => ({
  SwipeRow: ({ item, children }: { item: { id: string }; children: ReactNode }) => {
    renders.set(item.id, (renders.get(item.id) ?? 0) + 1);
    return <>{children}</>;
  },
}));

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  renders.clear();
  prefsStore.set(DEFAULT_PREFS);
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

const rowOf = (n: number) => document.querySelector(`[data-item-id="${1000 + n}"]`) as HTMLElement;

describe("row memoization", () => {
  it("marking one row read re-renders only that row", async () => {
    const cards = Array.from({ length: 5 }, (_, i) => card(i + 1));
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf(cards)),
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    });
    window.history.replaceState({ idx: 0 }, "", "/l/all");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(within(rowOf(1)).getByRole("button", { name: /^More actions: / }));
    const markRead = await screen.findByRole("menuitem", { name: "Mark as read" });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    renders.clear();
    await user.click(markRead);
    await waitFor(() => expect(renders.get("1001")).toBeGreaterThan(0));
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect([...renders.keys()]).toEqual(["1001"]);
  });
});

describe("range actions after the list changed", () => {
  it("mark above uses the list as it is now, not the one before a mark-read", async () => {
    const cards = Array.from({ length: 5 }, (_, i) => card(i + 1));
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf(cards)),
      "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    });
    window.history.replaceState({ idx: 0 }, "", "/l/all");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    await user.click(within(rowOf(1)).getByRole("button", { name: /^More actions: / }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" })); // changes the cached list: row 1001 is now read
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(1));
    await user.click(within(rowOf(2)).getByRole("button", { name: /^More actions: / }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark above as read" }));
    const bulk = () => calls.filter((c) => c.method === "POST" && String(c.init?.body).includes("\"bound\""));
    await waitFor(() => expect(bulk().length).toBeGreaterThan(0));
    const body = JSON.parse(String(bulk()[0]?.init?.body));
    expect(body.bound).toMatchObject({ side: "above", anchor: { id: "1002" } });
  });
});
