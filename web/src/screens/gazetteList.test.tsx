import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { DEFAULT_PAPER_NAME } from "@/layouts/gazette";
import { LEAD_WINDOW } from "@/layouts/gazettePlan";
import { devicePrefsStore, resetDevicePrefs, setListOverride, updateDevicePrefs } from "@/lib/devicePrefs";
import { profileOf } from "@/lib/deviceSync";
import { updatePrefs, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { themeStore } from "@/theme/theme";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { LEAVE_MS, clearListMemory } from "./ListPane";

// The Gazette as the list screen draws it: the fetch, the paper's name, reading in place, keys and loading more.

class NoES {
  addEventListener() {}
  close() {}
}

const IMG = "/img/x.jpg";
const BASE = 1_790_000_000;
const at = (n: number) => ({ published_at: BASE - n * 3600, sort_at: BASE - n * 3600 });
const many = (from: number, n: number) => Array.from({ length: n }, (_, i) => card(from + i, { image: (from + i) % 3 === 0 ? IMG : null, ...at(from + i) }));
const few = () => many(1, 8);

function routes(items: (url: URL) => ReturnType<typeof pageOf> = () => pageOf(few()), extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": (u) => json(items(u)),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

/** Story ids in DOM order, which is the paper's reading order. */
const storyIds = (root: ParentNode) => [...root.querySelectorAll<HTMLElement>("article[data-item-id]")].map((a) => a.dataset.itemId);
const itemCalls = (calls: { method: string; url: URL }[]) => calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/items");

beforeEach(() => {
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  updatePrefs({ shortcuts: true });
  updateDevicePrefs({ peekSeen: true, layout: "gazette" });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Gazette fetch order", () => {
  it("asks for newest first even when this device reads oldest first", async () => {
    updateDevicePrefs({ order: "oldest" });
    const { calls } = routes();
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const reqs = itemCalls(calls);
    expect(reqs.length).toBeGreaterThan(0);
    for (const r of reqs) expect(r.url.searchParams.get("order")).toBeNull();
    // The order toggle is not offered: it would do nothing here.
    expect(screen.queryByRole("button", { name: "Oldest first" })).toBeNull();
  });

  it("asks for newest first when the feed itself is set to oldest first", async () => {
    setListOverride("feed", "1", "order", "oldest");
    setListOverride("feed", "1", "layout", "gazette");
    updateDevicePrefs({ layout: "magazine" });
    const { calls } = routes();
    go("/l/all?feed=1");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    for (const r of itemCalls(calls)) expect(r.url.searchParams.get("order")).toBeNull();
  });

  it("the same settings in another layout still ask for oldest first", async () => {
    updateDevicePrefs({ order: "oldest", layout: "magazine" });
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(itemCalls(calls).every((r) => r.url.searchParams.get("order") === "oldest")).toBe(true);
    expect(screen.getByRole("button", { name: "Oldest first" })).toHaveAttribute("aria-pressed", "true");
  });

  it("the list options menu says the Gazette is always newest first", async () => {
    routes();
    go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    await userEvent.click(screen.getByRole("button", { name: /^List options, Gazette layout/ }));
    expect(await screen.findByText(/The Gazette is always newest first/)).toBeInTheDocument();
  });
});

describe("Gazette on the list screen", () => {
  it("loads more until the front page can be planned, then prints it", async () => {
    const first = many(1, 50);
    const second = many(51, LEAD_WINDOW - 50 + 30);
    const { calls } = routes((u) => (u.searchParams.get("cursor") === "c1" ? pageOf(second) : pageOf(first, "c1")));
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    expect(itemCalls(calls).filter((c) => c.url.searchParams.get("cursor") === "c1")).toHaveLength(1);
    expect(new Set(storyIds(container)).size).toBe(first.length + second.length);
    expect(screen.getByText("That's the Gazette.")).toBeInTheDocument();
  });

  it("prints the name this device gave the paper", async () => {
    updateDevicePrefs({ paperName: "Morning Notes" });
    routes();
    go("/l/unread");
    expect(await screen.findByRole("heading", { name: "Morning Notes" })).toBeInTheDocument();
    expect(screen.getByText("That's Morning Notes.")).toBeInTheDocument();
  });

  it("j and k follow the reading order, and a story marked read stays where it is", async () => {
    routes();
    const { container } = go("/l/unread");
    await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
    const order = storyIds(container);
    const list = screen.getByTestId("list-scroll");
    const press = (key: string) => act(() => void fireEvent.keyDown(list, { key }));
    press("j");
    press("j");
    await waitFor(() => expect(container.querySelector("[data-selected]")?.getAttribute("data-item-id")).toBe(order[1]));
    press("k");
    await waitFor(() => expect(container.querySelector("[data-selected]")?.getAttribute("data-item-id")).toBe(order[0]));
    press("m");
    const story = () => container.querySelector(`article[data-item-id="${order[0]}"]`);
    await waitFor(() => expect(story()).toHaveAttribute("data-read", "true"));
    // In the Unread list a row marked read leaves after LEAVE_MS; a story of the paper fades in place instead.
    await act(() => new Promise((r) => setTimeout(r, LEAVE_MS + 300)));
    expect(story()).not.toBeNull();
    expect(storyIds(container)).toEqual(order);
  });

  it("a search shows rows, not a paper", async () => {
    routes(() => pageOf(few()), { "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    const { container } = go("/search?q=article");
    // Relevance order: Editorial rows under one "Best matches first" header.
    await screen.findByText("Best matches first");
    expect(container.querySelectorAll("[data-index]").length).toBeGreaterThan(1);
    expect(container.querySelector("[data-gazette]")).toBeNull();
  });

  it("passes axe in a dark and a very light theme", async () => {
    routes();
    for (const scheme of ["midnight", "paper"]) {
      themeStore.set((t) => ({ ...t, mode: "fixed", fixed: scheme }));
      const { container, unmount } = go("/l/unread");
      await screen.findByRole("heading", { name: DEFAULT_PAPER_NAME });
      expect(await axe(container)).toHaveNoViolations();
      unmount();
    }
  });
});

describe("the paper's name setting", () => {
  it("is a text field in Settings, saved for this device and synced with its profile", async () => {
    routes();
    go("/settings/appearance");
    const field = await screen.findByRole("textbox", { name: "Name of the Gazette" });
    expect(field).toHaveAttribute("placeholder", DEFAULT_PAPER_NAME);
    await userEvent.type(field, "The Daily Kipple");
    expect(devicePrefsStore.get().paperName).toBe("The Daily Kipple");
    expect(profileOf({ theme: themeStore.get(), prefs: prefsStore.get(), dp: devicePrefsStore.get() })["client.paper_name"]).toBe("The Daily Kipple");
    expect(within(field.parentElement!).getByText(/Leave it empty for The Gazette/)).toBeInTheDocument();
  });
});
