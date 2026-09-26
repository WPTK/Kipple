import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import { QueryClient } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import type { SettingMeta } from "@/api/admin";
import { handleServerEvent, initialLive, liveStore, resetSavedSearchCounts, SAVED_COUNTS_MIN_MS } from "@/api/events";
import { COUNTS_RETRY, invalidateSavedSearches, savedSearchCountsKey, savedSearchesKey, searchRoute, unreadLabel } from "@/api/savedSearches";
import type { SavedSearch } from "@/api/types";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetDeviceSync, syncStore, hydrateDevice, SYNC_DIRTY_KEY, SYNC_FLAG_KEY } from "@/lib/deviceSync";
import { devicePrefsStore, updateDevicePrefs } from "@/lib/devicePrefs";
import { setSavedOpen } from "@/lib/searchPrefs";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

const meta = (m: Partial<SettingMeta> & Pick<SettingMeta, "key" | "kind">): SettingMeta => ({
  value: null,
  default: null,
  label: m.key,
  description: `About ${m.key}`,
  group: "library",
  surface: "settings",
  ...m,
});

const SETTINGS: SettingMeta[] = [
  meta({
    key: "library.auto_read_days",
    kind: "int",
    label: "Mark old articles as read after…",
    value: 0,
    default: 0,
    min: 0,
    max: 365,
    step: 1,
    unit: "days",
    description: "Articles you have not read are marked read once they are this many days old.",
  }),
  meta({
    key: "imgproxy.mode",
    kind: "enum",
    group: "images",
    label: "Load images through Kipple",
    value: "all",
    default: "all",
    options: [
      { value: "all", label: "All images" },
      { value: "http_only", label: "Only insecure images" },
    ],
  }),
  meta({ key: "imgproxy.cache_mb", kind: "int", group: "images", label: "Image cache size", value: 1024, default: 1024, min: 0, max: 20480, step: 64, unit: "MB" }),
  meta({ key: "library.saved_searches", kind: "json", surface: "hidden", value: [], default: [] }),
];
const settingsBody = (list = SETTINGS) => ({ settings: list, values: Object.fromEntries(list.map((s) => [s.key, s.value])) });

const SAVED: SavedSearch[] = [
  { id: "s1", name: "Rust news", q: "rust", order: "rank" },
  { id: "s2", name: "Big feed cats", q: "cats", scope: { feed_id: "1" }, order: "date" },
  { id: "s3", name: "Starred go", q: "go*", scope: { view: "starred" }, order: "oldest" },
];
const withCounts = (l: SavedSearch[]): SavedSearch[] => l.map((s, i) => ({ ...s, unread: i === 0 ? 1200 : i === 1 ? null : 7, unread_capped: i === 0 }));

function base(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json(settingsBody()),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    "GET /api/imgcache": () => json(CACHE),
    "GET /api/saved-searches": (u) => json({ saved_searches: u.searchParams.get("counts") === "0" ? SAVED : withCounts(SAVED) }),
    ...extra,
  });
}

const CACHE = {
  enabled: true,
  mode: "all",
  cache_mb: 1024,
  max_bytes: 1024 * 1024 ** 2,
  used_bytes: 256 * 1024 ** 2,
  entries: 4321,
  neg_entries: 3,
  thumbnails: 1200,
  hits: 90,
  misses: 10,
  evictions: 5,
  failures: 2,
  since: Math.floor(Date.now() / 1000) - 3600,
  oldest_access_at: null,
  disk_free_bytes: 50 * 1024 ** 3,
  disk_floor_bytes: 2 * 1024 ** 3,
  low_disk: false,
};

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const body = (c: { init?: RequestInit } | undefined) => JSON.parse(String(c?.init?.body));

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  resetDeviceSync();
  resetSavedSearchCounts();
  setSavedOpen(true);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("Saved searches in the Feeds list", () => {
  it("lists them under Favorites with lazy counts: 999+ when capped, a dash when not counted", async () => {
    const { calls } = base();
    go("/feeds");
    const nav = await screen.findByRole("region", { name: "Saved searches" });
    expect(within(nav).getByRole("link", { name: /Rust news/ })).toBeInTheDocument();
    // The names paint from ?counts=0; the counts follow in a second request.
    await waitFor(() => expect(within(nav).getByText("999+")).toBeInTheDocument());
    expect(calls.filter((c) => c.url.pathname === "/api/saved-searches").map((c) => c.url.searchParams.get("counts"))).toEqual(["0", null]);
    expect(within(nav).getByText("7")).toBeInTheDocument();
    expect(within(nav).getByText("Unread count unavailable")).toBeInTheDocument(); // null unread: a dash
    expect(unreadLabel({ unread: 999, unread_capped: true })).toBe("999+");
    expect(unreadLabel({ unread: null })).toBeNull();
  });

  it("a tap runs the search in its scope and order, as a submitted search", () => {
    expect(searchRoute(SAVED[1] as SavedSearch)).toBe("/search?q=cats&feed=1&order=date&ss=s2");
    expect(searchRoute(SAVED[2] as SavedSearch)).toBe("/search?q=go*&view=starred&order=oldest&ss=s3");
    expect(searchRoute({ id: "s9", q: "a b" })).toBe("/search?q=a+b&ss=s9");
  });

  it("running one loads its scope and order without typing=1", async () => {
    const { calls } = base();
    go("/feeds");
    const nav = await screen.findByRole("region", { name: "Saved searches" });
    await userEvent.setup().click(within(nav).getByRole("link", { name: /Starred go/ }));
    await screen.findByRole("searchbox", { name: "Search articles" });
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items")).toBe(true));
    const p = calls.filter((c) => c.url.pathname === "/api/items").at(-1)?.url.searchParams;
    expect(p?.get("q")).toBe("go*");
    expect(p?.get("view")).toBe("starred");
    expect(p?.get("order")).toBe("oldest");
    expect(p?.has("typing")).toBe(false);
    expect(screen.getByRole("searchbox", { name: "Search articles" })).toHaveValue("go*");
  });

  it("collapses, and stays collapsed on this device", async () => {
    base();
    const { unmount } = go("/feeds");
    const nav = await screen.findByRole("region", { name: "Saved searches" });
    await userEvent.setup().click(within(nav).getByRole("button", { name: "Saved searches" }));
    expect(within(nav).queryByRole("link", { name: /Rust news/ })).toBeNull();
    unmount();
    go("/feeds");
    const again = await screen.findByRole("region", { name: "Saved searches" });
    expect(within(again).getByRole("button", { name: "Saved searches" })).toHaveAttribute("aria-expanded", "false");
  });

  it("is absent when there are none, and passes axe", async () => {
    base({ "GET /api/saved-searches": () => json({ saved_searches: [] }) });
    const { container } = go("/feeds");
    await screen.findByRole("link", { name: /Example Feed/ });
    expect(screen.queryByRole("region", { name: "Saved searches" })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });
});

describe("Save this search", () => {
  it("posts the name, text, scope and order, and shows a 400 message inline", async () => {
    let n = 0;
    const { calls } = base({
      "POST /api/saved-searches": () => {
        n++;
        return n === 1
          ? json({ error: "bad_saved_search", field: "name", message: "name is too long" }, 400)
          : json({ id: "s9", name: "Cats", q: "cats", order: "oldest", unread: 0, unread_capped: false }, 201);
      },
    });
    go("/search?q=cats%20&feed=1&order=oldest");
    const user = userEvent.setup();
    await screen.findByText("Article number 1");
    await user.click(screen.getByRole("button", { name: "Save this search" }));
    const dlg = await screen.findByRole("dialog", { name: "Save this search" });
    expect(within(dlg).getByText(/Example Feed/)).toBeInTheDocument();
    const name = within(dlg).getByRole("textbox", { name: "Name" });
    expect(name).toHaveValue("cats");
    await user.clear(name);
    await user.type(name, "Cats");
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    expect(await within(dlg).findByText("name is too long")).toBeInTheDocument();
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Save this search" })).toBeNull());
    const posts = calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/saved-searches");
    expect(body(posts[1])).toEqual({ name: "Cats", q: "cats", scope: { feed_id: "1" }, order: "oldest" });
  });

  it("updates the saved search that was run instead of adding another", async () => {
    const { calls } = base({ "PATCH /api/saved-searches/s2": () => json({ ...SAVED[1], name: "Cats again" }) });
    go("/search?q=cats&feed=1&order=date&ss=s2");
    const user = userEvent.setup();
    await screen.findByText("Article number 1");
    await user.click(screen.getByRole("button", { name: "Save this search" }));
    const dlg = await screen.findByRole("dialog", { name: "Save this search" });
    await user.click(within(dlg).getByRole("button", { name: /^Update/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toMatchObject({ q: "cats", scope: { feed_id: "1" }, order: "date" });
  });
});

describe("Saved search events", () => {
  it("saved_searches.changed and feed.changed refetch the list", async () => {
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    handleServerEvent(qc, { type: "saved_searches.changed", data: {} });
    expect(spy).toHaveBeenCalledWith({ queryKey: savedSearchesKey, exact: true });
    spy.mockClear();
    handleServerEvent(qc, { type: "feed.changed", data: { feed_id: "1" } });
    expect(spy).toHaveBeenCalledWith({ queryKey: savedSearchesKey, exact: true });
  });

  it("counts events refresh the counts at most every 10 s", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const counts = () => spy.mock.calls.filter((c) => JSON.stringify(c[0]?.queryKey) === JSON.stringify(savedSearchCountsKey)).length;
    const ev = { type: "counts", data: { unread_total: 1, feeds: {} } } as const;
    handleServerEvent(qc, ev);
    expect(counts()).toBe(1);
    vi.advanceTimersByTime(3000);
    handleServerEvent(qc, ev);
    handleServerEvent(qc, ev);
    expect(counts()).toBe(1); // throttled
    vi.advanceTimersByTime(SAVED_COUNTS_MIN_MS);
    expect(counts()).toBe(2); // one trailing refresh, not one per event
    vi.advanceTimersByTime(SAVED_COUNTS_MIN_MS * 3);
    expect(counts()).toBe(2);
  });
});

describe("Settings > Saved searches", () => {
  it("edits name, query, scope and order in one PATCH", async () => {
    const { calls } = base({ "PATCH /api/saved-searches/s1": () => json({ ...SAVED[0], name: "Rust!" }) });
    go("/settings");
    const user = userEvent.setup();
    const list = await screen.findByRole("list", { name: "Saved searches" }, { timeout: 5000 });
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    await user.click(within(list).getByRole("button", { name: "Edit Rust news" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit saved search" });
    const name = within(dlg).getByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "Rust!");
    await user.selectOptions(within(dlg).getByRole("combobox", { name: "Where" }), "feed:1");
    await user.selectOptions(within(dlg).getByRole("combobox", { name: "Sort by" }), "oldest");
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toEqual({ name: "Rust!", scope: { feed_id: "1" }, order: "oldest" });
  });

  it("clears a scope with null", async () => {
    const { calls } = base({ "PATCH /api/saved-searches/s2": () => json(SAVED[1]) });
    go("/settings");
    const user = userEvent.setup();
    const list = await screen.findByRole("list", { name: "Saved searches" }, { timeout: 5000 });
    await user.click(within(list).getByRole("button", { name: "Edit Big feed cats" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit saved search" });
    await user.selectOptions(within(dlg).getByRole("combobox", { name: "Where" }), "all");
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toEqual({ scope: null });
  });

  it("deletes after a confirm", async () => {
    const { calls } = base({ "DELETE /api/saved-searches/s3": () => new Response(null, { status: 204 }) });
    go("/settings");
    const user = userEvent.setup();
    const list = await screen.findByRole("list", { name: "Saved searches" }, { timeout: 5000 });
    await user.click(within(list).getByRole("button", { name: "Delete Starred go" }));
    const dlg = await screen.findByRole("dialog", { name: "Delete this saved search?" });
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(within(dlg).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url.pathname === "/api/saved-searches/s3")).toBe(true));
  });

  it("reorders with the buttons through POST /api/saved-searches/reorder", async () => {
    let order = SAVED;
    const { calls } = base({
      "GET /api/saved-searches": () => json({ saved_searches: order }),
      "POST /api/saved-searches/reorder": () => {
        order = [SAVED[1] as SavedSearch, SAVED[0] as SavedSearch, SAVED[2] as SavedSearch];
        return json({ saved_searches: order });
      },
    });
    go("/settings");
    const user = userEvent.setup();
    const list = await screen.findByRole("list", { name: "Saved searches" }, { timeout: 5000 });
    expect(within(list).getByRole("button", { name: "Move Rust news up" })).toBeDisabled();
    await user.click(within(list).getByRole("button", { name: "Move Rust news down" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/saved-searches/reorder")).toBe(true));
    expect(body(calls.find((c) => c.url.pathname === "/api/saved-searches/reorder"))).toEqual({ ids: ["s2", "s1", "s3"] });
    // The list shows the new order at once.
    await waitFor(() => expect(within(screen.getByRole("list", { name: "Saved searches" })).getAllByRole("listitem")[0]).toHaveTextContent("Big feed cats"));
  });

  it("reorders with the arrow keys on the grip", async () => {
    const { calls } = base({ "POST /api/saved-searches/reorder": () => json({ saved_searches: SAVED }) });
    go("/settings");
    const list = await screen.findByRole("list", { name: "Saved searches" }, { timeout: 5000 });
    const grip = within(list).getByRole("button", { name: /Reorder Starred go/ });
    grip.focus();
    await userEvent.setup().keyboard("{ArrowUp}");
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/saved-searches/reorder")).toBe(true));
    expect(body(calls.find((c) => c.url.pathname === "/api/saved-searches/reorder"))).toEqual({ ids: ["s1", "s3", "s2"] });
  });
});

// ---- auto-read ---------------------------------------------------------------------------------

const PREVIEW = {
  total: 250,
  feeds: [
    { feed_id: "1", title: "Example Feed", days: 90, count: 200 },
    { feed_id: "2", title: "Other", days: 30, count: 50 },
  ],
  global_days: 90,
  confirm_above: 100,
};

describe("Auto-read (Settings > Library)", () => {
  it("draws the global setting with presets and a Custom stepper, and saves a preset without marking anything", async () => {
    const { calls } = base({
      "PATCH /api/settings": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, number>;
        return json(settingsBody(SETTINGS.map((s) => (s.key in b ? { ...s, value: b[s.key] } : s))));
      },
    });
    go("/settings");
    const user = userEvent.setup();
    const field = (await screen.findByText("Mark old articles as read after…", { selector: "legend" }, { timeout: 5000 })).closest("fieldset") as HTMLElement;
    for (const l of ["Off", "30 days", "60 days", "90 days", "180 days", "365 days", "Custom"]) expect(within(field).getByRole("radio", { name: l })).toBeInTheDocument();
    expect(within(field).getByRole("radio", { name: "Off" })).toBeChecked();
    await user.click(within(field).getByRole("radio", { name: "90 days" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toEqual({ "library.auto_read_days": 90 });
    // Changing it never marks anything, and it offers the preview.
    expect(await screen.findByText(/Nothing was marked/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.startsWith("/api/library/auto-read"))).toBe(false);
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
  });

  it("previews the total and the per-feed list, biggest first, and asks before marking more than 100", async () => {
    const { calls } = base({
      "POST /api/library/auto-read/preview": () => json(PREVIEW),
      "POST /api/library/auto-read/run": (_u, init) =>
        JSON.parse(String(init?.body)).confirm === true
          ? json({ id: "77", kind: "auto_read", done: 0, total: 250, changed: 0, new_items: 0, errors: 0 }, 202)
          : json({ error: "confirm_required", total: 250, confirm_above: 100, message: "confirm" }, 409),
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    const box = await screen.findByTestId("auto-read-preview");
    expect(within(box).getByText("250")).toBeInTheDocument();
    const feeds = within(box).getAllByRole("listitem").map((li) => li.textContent);
    expect(feeds[0]).toContain("Example Feed");
    expect(feeds[1]).toContain("Other");
    expect(body(calls.find((c) => c.url.pathname.endsWith("/preview")))).toEqual({});
    expect(within(box).getByText(/There is no Undo for this. You can mark articles unread again from any list./)).toBeInTheDocument();

    await user.click(within(box).getByRole("button", { name: "Mark 250 older articles as read now" }));
    const dlg = await screen.findByRole("dialog", { name: "Mark 250 older articles as read?" });
    expect(within(dlg).getByText(/There is no Undo button for this/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith("/run"))).toBe(false); // nothing is marked before the confirm
    await user.click(within(dlg).getByRole("button", { name: "Mark 250 as read" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/run"))).toBe(true));
    expect(body(calls.find((c) => c.url.pathname.endsWith("/run")))).toEqual({ confirm: true, expect_total: 250 });
  });

  it("marks up to 100 without a dialog and shows the run's progress from run.* events", async () => {
    const { calls } = base({
      "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: 40, feeds: [{ feed_id: "1", title: "Example Feed", days: 90, count: 40 }] }),
      "POST /api/library/auto-read/run": () => json({ id: "78", kind: "auto_read", done: 0, total: 40, changed: 0, new_items: 0, errors: 0 }, 202),
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    await user.click(await screen.findByRole("button", { name: "Mark 40 older articles as read now" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/run"))).toBe(true));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(body(calls.find((c) => c.url.pathname.endsWith("/run")))).toEqual({ expect_total: 40 });
    act(() => {
      liveStore.set((s) => ({ ...s, runs: { "78": { id: "78", kind: "auto_read", done: 20, total: 40, changed: 12, new_items: 0, errors: 0 } } }));
    });
    expect(await screen.findByTestId("auto-read-progress")).toHaveTextContent("Marking old articles as read: 20 of 40 checked, 12 marked");
    act(() => {
      handleServerEvent(new QueryClient(), { type: "run.done", data: { run_id: "78", kind: "auto_read", new_items: 0, errors: 0, changed: 12, scanned: 12 } });
    });
    expect(await screen.findByText("Marked 12 older articles as read.")).toBeInTheDocument();
    expect(screen.queryByTestId("auto-read-preview")).toBeNull(); // the numbers are stale after a run
  });

  it("handles 409 confirm_required (the count moved) by asking again with the server's number, and 409 busy", async () => {
    let call = 0;
    base({
      "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: 60, feeds: [] }),
      "POST /api/library/auto-read/run": () => {
        call++;
        return call === 1
          ? json({ error: "confirm_required", total: 130, confirm_above: 100, message: "m" }, 409)
          : json({ error: "busy" }, 409);
      },
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    await user.click(await screen.findByRole("button", { name: "Mark 60 older articles as read now" }));
    const dlg = await screen.findByRole("dialog", { name: "Mark 130 older articles as read?" });
    await user.click(within(dlg).getByRole("button", { name: "Mark 130 as read" }));
    expect(await screen.findByText(/Another catch-up is already running/)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("handles 409 total_changed by showing the recount and sending it as expect_total on the next confirm", async () => {
    let call = 0;
    const { calls } = base({
      "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: 60, feeds: [] }),
      "POST /api/library/auto-read/run": () => {
        call++;
        return call === 1
          ? json({ error: "total_changed", total: 400, expect_total: 60, message: "m" }, 409)
          : json({ id: "79", kind: "auto_read", done: 0, total: 400, changed: 0, new_items: 0, errors: 0 }, 202);
      },
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    await user.click(await screen.findByRole("button", { name: "Mark 60 older articles as read now" }));
    const dlg = await screen.findByRole("dialog", { name: "Mark 400 older articles as read?" });
    expect(dlg).toHaveTextContent("The count grew from 60 to 400");
    await user.click(within(dlg).getByRole("button", { name: "Mark 400 as read" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith("/run"))).toHaveLength(2));
    const runs = calls.filter((c) => c.url.pathname.endsWith("/run")).map((c) => body(c));
    expect(runs[0]).toEqual({ expect_total: 60 });
    expect(runs[1]).toEqual({ confirm: true, expect_total: 400 });
  });

  it("previews a what-if number of days without saving it", async () => {
    const { calls } = base({ "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: 5, feeds: [] }) });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Show advanced settings" }).catch(() => screen.findByRole("button", { name: "Try a different number of days" }, { timeout: 5000 })));
    await user.type(await screen.findByRole("textbox", { name: /Preview as if it were set to/ }), "60");
    await user.click(screen.getByRole("button", { name: "Preview" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/preview"))).toBe(true));
    expect(body(calls.find((c) => c.url.pathname.endsWith("/preview")))).toEqual({ days: 60 });
    expect(await screen.findByText(/using 60 days/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });
});

describe("Auto-read per feed", () => {
  const detail = {
    ...bootstrap.feeds[0],
    url: "https://example.com/feed.xml",
    url_original: null,
    custom_title: null,
    position: 0,
    enabled: true,
    disabled_reason: null,
    dedup_mode: "auto",
    rekey_pending: false,
    user_agent: null,
    has_http_auth: false,
    ignore_http_cache: false,
    disable_http2: false,
    allow_insecure_tls: false,
    allow_private_net: false,
    next_fetch_at: 0,
    auto_read_days: null,
  };

  it("offers Use the global setting / Off / N days and PATCHes auto_read_days", async () => {
    const { calls } = base({
      "GET /api/feeds/1": () => json(detail),
      "PATCH /api/feeds/1": () => json({ ...detail, auto_read_days: 60 }),
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Edit Example Feed|More actions for Example Feed|Example Feed options/i }).catch(async () => (await screen.findAllByRole("button", { name: /Example Feed/ }))[0] as HTMLElement));
    const sel = await screen.findByRole("combobox", { name: "Mark as read after" }, { timeout: 5000 });
    expect(sel).toHaveValue("");
    const labels = within(sel).getAllByRole("option").map((o) => o.textContent);
    expect(labels).toEqual(["Use the global setting", "Off for this feed", "30 days", "60 days", "90 days", "180 days", "365 days"]);
    await user.selectOptions(sel, "60");
    await user.click(screen.getByRole("button", { name: /^Save/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toEqual({ auto_read_days: 60 });
  });
});

// ---- images ------------------------------------------------------------------------------------

describe("Images (Settings)", () => {
  it("draws the mode and the cache size from the metadata, with 256/512/1024/2048/off presets", async () => {
    const { calls } = base({
      "PATCH /api/settings": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, number>;
        return json(settingsBody(SETTINGS.map((s) => (s.key in b ? { ...s, value: b[s.key] } : s))));
      },
    });
    go("/settings");
    const user = userEvent.setup();
    const field = (await screen.findByText("Image cache size", { selector: "legend" }, { timeout: 5000 })).closest("fieldset") as HTMLElement;
    for (const l of ["256 MB", "512 MB", "1 GB", "2 GB", "Off", "Custom"]) expect(within(field).getByRole("radio", { name: l })).toBeInTheDocument();
    expect(within(field).getByRole("radio", { name: "1 GB" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Only insecure images" })).toBeInTheDocument();
    await user.click(within(field).getByRole("radio", { name: "512 MB" }));
    // Lowering the cap evicts at once: it asks first.
    await user.click(await screen.findByRole("button", { name: "Make it smaller" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toEqual({ "imgproxy.cache_mb": 512 });
    // A value that is not a preset opens the stepper.
    await user.click(within(field).getByRole("radio", { name: "Custom" }));
    expect(await screen.findByRole("spinbutton", { name: "Image cache size" })).toBeInTheDocument();
  });

  it("shows used against the cap, files, hit rate, and refreshes when the section opens", async () => {
    const { calls } = base();
    go("/settings");
    const card = await screen.findByTestId("imgcache-card", undefined, { timeout: 5000 });
    expect(within(card).getByText("256.0 MB")).toBeInTheDocument();
    expect(within(card).getByText(/of 1\.00 GB used/)).toBeInTheDocument();
    expect(within(card).getByRole("progressbar", { name: "Image cache used" })).toHaveAttribute("aria-valuenow", String(256 * 1024 ** 2));
    expect(within(card).getByText("4,321")).toBeInTheDocument();
    expect(within(card).getByText("90%")).toBeInTheDocument(); // 90 hits of 100 requests
    expect(calls.filter((c) => c.url.pathname === "/api/imgcache")).toHaveLength(1);
  });

  it("warns when the disk is low", async () => {
    base({ "GET /api/imgcache": () => json({ ...CACHE, low_disk: true, disk_free_bytes: 3 * 1024 ** 3 }) });
    go("/settings");
    const card = await screen.findByTestId("imgcache-card", undefined, { timeout: 5000 });
    expect(within(card).getByText(/disk is nearly full \(3\.00 GB free\)/)).toBeInTheDocument();
  });

  it("says when the cache is off", async () => {
    base({ "GET /api/imgcache": () => json({ ...CACHE, enabled: false, cache_mb: 0, max_bytes: 0, used_bytes: 0, entries: 0, neg_entries: 0 }) });
    go("/settings");
    const card = await screen.findByTestId("imgcache-card", undefined, { timeout: 5000 });
    expect(within(card).getByText(/The image cache is off/)).toBeInTheDocument();
  });

  it("clears the cache after a confirm, then reloads the stats", async () => {
    let cleared = false;
    const { calls } = base({
      "GET /api/imgcache": () => json(cleared ? { ...CACHE, used_bytes: 0, entries: 0, neg_entries: 0, thumbnails: 0 } : CACHE),
      "POST /api/imgcache/clear": () => {
        cleared = true;
        return json({ cleared: 4321 });
      },
    });
    go("/settings");
    const user = userEvent.setup();
    const card = await screen.findByTestId("imgcache-card", undefined, { timeout: 5000 });
    await user.click(within(card).getByRole("button", { name: "Clear image cache" }));
    const dlg = await screen.findByRole("dialog", { name: "Clear the image cache?" });
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await user.click(within(dlg).getByRole("button", { name: "Clear image cache" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/imgcache/clear")).toBe(true));
    await waitFor(() => expect(within(screen.getByTestId("imgcache-card")).getByText(/0 B/)).toBeInTheDocument());
  });
});

describe("Health shows the image cache size", () => {
  it("adds imgcache_bytes to the sizes line", async () => {
    base({
      "GET /api/health/feeds": () =>
        json({ feeds: [], clients: [], snapshot: { last_at: null, last_error: null }, clock: { ahead_s: 0 }, db: { db_bytes: 5 * 1024 ** 2, wal_bytes: 0, backup_bytes: 2 * 1024 ** 2, imgcache_bytes: 300 * 1024 ** 2 }, unread_total: 3 }),
    });
    go("/health");
    expect(await screen.findByText(/image cache 300\.0 MB/, undefined, { timeout: 5000 })).toBeInTheDocument();
  });
});

// ---- devices -----------------------------------------------------------------------------------

describe("The unsaved default device (id empty)", () => {
  const unsaved = { id: "", name: "", profile: {}, defaults: {}, merged: { "client.layout": "compact" } };

  it("keeps the local values, never PATCHes, and says so in Settings > Devices", async () => {
    const { calls } = base({
      "GET /api/bootstrap": () => json({ ...bootstrap, device: unsaved }),
      "PATCH /api/device": () => json({ error: "not_found" }, 404),
      "GET /api/devices": () => json({ devices: [{ id: "d1", name: "Old phone", current: false, user_agent: "", client: "web", created_at: 1, last_seen_at: 2, overrides: 3 }] }),
    });
    go("/settings");
    await screen.findByText(/This browser can't save its own settings yet\. Older browsers will be forgotten automatically/, undefined, { timeout: 5000 });
    expect(syncStore.get().status).toBe("unsaved");
    // No writes to the device, and no actions that would 404.
    expect(screen.queryByRole("button", { name: "Use this device's settings as the default for new devices" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reset this device to defaults" })).toBeNull();
    expect(screen.queryByRole("textbox", { name: "This device's name" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Copy settings from/ })).toBeNull();
    // A local change is kept and still sends nothing.
    await userEvent.setup().click(screen.getByRole("radio", { name: "Compact" }));
    await new Promise((r) => setTimeout(r, 700));
    expect(calls.filter((c) => c.url.pathname === "/api/device")).toHaveLength(0);
  });

  it("hydrateDevice leaves local prefs alone and sends nothing for an empty id", () => {
    const { calls } = base();
    hydrateDevice({ id: "", name: "", profile: {}, merged: { "client.layout": "cards" } });
    expect(syncStore.get().status).toBe("unsaved");
    expect(calls).toHaveLength(0);
    // A later bootstrap that does register the browser starts syncing normally.
    hydrateDevice({ id: "d7", name: "", profile: {}, merged: {} });
    expect(syncStore.get().status).not.toBe("unsaved");
  });

  it("a change made while unsaved is kept and sent once the browser registers, not overwritten by the server's values", async () => {
    localStorage.setItem(SYNC_FLAG_KEY, "1");
    localStorage.removeItem(SYNC_DIRTY_KEY);
    const { calls } = base({ "PATCH /api/device": () => json({ id: "d7", name: "", profile: { "client.layout": "compact" }, defaults: {}, merged: { "client.layout": "compact" } }) });
    updateDevicePrefs({ layout: "magazine" });
    hydrateDevice({ id: "", name: "", profile: {}, merged: { "client.layout": "cards" } });
    updateDevicePrefs({ layout: "compact" });
    expect(JSON.parse(localStorage.getItem(SYNC_DIRTY_KEY) ?? "{}")).toMatchObject({ "client.layout": "compact" });
    expect(calls).toHaveLength(0);
    hydrateDevice({ id: "d7", name: "", profile: {}, merged: { "client.layout": "cards" } });
    expect(devicePrefsStore.get().layout).toBe("compact");
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH" && c.url.pathname === "/api/device")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH"))).toMatchObject({ "client.layout": "compact" });
  });
});

// ---- review 3: counts, events, reorder -------------------------------------------------------------

/** What the real server sends for ?counts=0: every row, with `unread: null`. */
const nullBase = (l: SavedSearch[]): SavedSearch[] => l.map((s) => ({ ...s, unread: null }));

describe("Saved search counts (review 3)", () => {
  it("shows no dash while the counts are loading: the base list's null means not loaded", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    base({
      "GET /api/saved-searches": async (u) => {
        if (u.searchParams.get("counts") === "0") return json({ saved_searches: nullBase(SAVED) });
        await gate;
        return json({ saved_searches: withCounts(SAVED) });
      },
    });
    go("/feeds");
    const nav = await screen.findByRole("region", { name: "Saved searches" });
    expect(within(nav).getByRole("link", { name: /Rust news/ })).toBeInTheDocument();
    expect(within(nav).queryByText("Unread count unavailable")).toBeNull();
    expect(within(nav).queryAllByTestId("saved-count")).toHaveLength(0);
    release();
    await waitFor(() => expect(within(nav).getByText("999+")).toBeInTheDocument());
    expect(within(nav).getAllByText("Unread count unavailable")).toHaveLength(1); // only the entry the server timed out on
  });

  it("retries a failed counts request twice, then offers Try again instead of a dash on every entry", async () => {
    const saved = { ...COUNTS_RETRY };
    COUNTS_RETRY.baseMs = 1;
    let countCalls = 0;
    let fail = true;
    base({
      "GET /api/saved-searches": (u) => {
        if (u.searchParams.get("counts") === "0") return json({ saved_searches: nullBase(SAVED) });
        countCalls++;
        return fail ? json({ error: "boom" }, 500) : json({ saved_searches: withCounts(SAVED) });
      },
    });
    go("/feeds");
    const nav = await screen.findByRole("region", { name: "Saved searches" });
    await waitFor(() => expect(within(nav).getByRole("button", { name: "Try again" })).toBeInTheDocument(), { timeout: 5000 });
    expect(countCalls).toBe(3); // the first try and two retries
    expect(within(nav).queryByText("Unread count unavailable")).toBeNull();
    fail = false;
    await userEvent.setup().click(within(nav).getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(within(nav).getByText("999+")).toBeInTheDocument());
    Object.assign(COUNTS_RETRY, saved);
  });
});

describe("Saved search events and writes (review 3)", () => {
  it("a burst of feed.changed events does not restart the counts request: only the throttle touches it", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const counts = () => spy.mock.calls.filter((c) => JSON.stringify(c[0]?.queryKey) === JSON.stringify(savedSearchCountsKey)).length;
    for (let i = 0; i < 20; i++) handleServerEvent(qc, { type: "feed.changed", data: { feed_id: String(i) } });
    expect(counts()).toBe(1);
    // The list itself is only ever invalidated exactly, never by prefix (which would also hit the counts).
    for (const c of spy.mock.calls) if (JSON.stringify(c[0]?.queryKey) === JSON.stringify(savedSearchesKey)) expect(c[0]?.exact).toBe(true);
    vi.advanceTimersByTime(SAVED_COUNTS_MIN_MS);
    expect(counts()).toBe(2); // one trailing refresh for the whole burst
  });

  it("the saved_searches.changed echo of this tab's own write is dropped; another tab's is not", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-26T12:00:00Z"));
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const lists = () => spy.mock.calls.filter((c) => JSON.stringify(c[0]?.queryKey) === JSON.stringify(savedSearchesKey)).length;
    invalidateSavedSearches(qc); // the local write succeeded
    expect(lists()).toBe(1);
    handleServerEvent(qc, { type: "saved_searches.changed", data: {} }); // its echo
    expect(lists()).toBe(1);
    vi.advanceTimersByTime(1500);
    handleServerEvent(qc, { type: "saved_searches.changed", data: {} }); // someone else
    expect(lists()).toBe(2);
  });
});

describe("Saved search reorder (review 3)", () => {
  it("two quick moves build on each other and only the last order is sent after the first lands", async () => {
    let release: () => void = () => {};
    const first = new Promise<void>((r) => (release = r));
    let n = 0;
    const { calls } = base({
      "POST /api/saved-searches/reorder": async () => {
        if (n++ === 0) await first;
        return json({ saved_searches: SAVED });
      },
    });
    go("/settings");
    const user = userEvent.setup();
    const list = await screen.findByRole("list", { name: "Saved searches" }, { timeout: 5000 });
    await user.click(within(list).getByRole("button", { name: "Move Rust news down" }));
    // Rust news is now second; moving it down again must start from that optimistic order.
    await user.click(within(screen.getByRole("list", { name: "Saved searches" })).getByRole("button", { name: "Move Rust news down" }));
    expect(calls.filter((c) => c.url.pathname === "/api/saved-searches/reorder")).toHaveLength(1); // serialized
    release();
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/saved-searches/reorder")).toHaveLength(2));
    const sent = calls.filter((c) => c.url.pathname === "/api/saved-searches/reorder").map(body);
    expect(sent).toEqual([{ ids: ["s2", "s1", "s3"] }, { ids: ["s2", "s3", "s1"] }]);
  });
});

// ---- review 3: auto-read ------------------------------------------------------------------------

describe("Auto-read catch-up (review 3)", () => {
  const RUN = { id: "90", kind: "auto_read", done: 0, total: 40, changed: 0, new_items: 0, errors: 0 };
  const preview40 = { ...PREVIEW, total: 40, feeds: [{ feed_id: "1", title: "Example Feed", days: 90, count: 40 }] };

  it("says how many the run marked from run.done, not from the throttled last progress", async () => {
    base({ "POST /api/library/auto-read/preview": () => json(preview40), "POST /api/library/auto-read/run": () => json(RUN, 202) });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    await user.click(await screen.findByRole("button", { name: "Mark 40 older articles as read now" }));
    const qc = new QueryClient();
    act(() => {
      handleServerEvent(qc, { type: "run.start", data: { run_id: "90", kind: "auto_read", total: 1200 } });
      handleServerEvent(qc, { type: "run.progress", data: { run_id: "90", done: 500, total: 1200, new_items: 0, errors: 0, changed: 500 } });
    });
    expect(await screen.findByTestId("auto-read-progress")).toHaveTextContent("500 marked");
    act(() => {
      handleServerEvent(qc, { type: "run.done", data: { run_id: "90", kind: "auto_read", new_items: 0, errors: 0, changed: 1200, scanned: 1200 } });
    });
    expect(await screen.findByText("Marked 1,200 older articles as read.")).toBeInTheDocument();
  });

  it("shows the run at once from the 202 body, with no stream event: Preview is disabled and progress shows", async () => {
    base({ "POST /api/library/auto-read/preview": () => json(preview40), "POST /api/library/auto-read/run": () => json(RUN, 202) });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    await user.click(await screen.findByRole("button", { name: "Mark 40 older articles as read now" }));
    expect(await screen.findByTestId("auto-read-progress")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: /older articles as read now/ })).toBeNull();
  });

  it("a 409 busy learns the running catch-up from /api/status and shows it", async () => {
    base({
      "POST /api/library/auto-read/preview": () => json(preview40),
      "POST /api/library/auto-read/run": () => json({ error: "busy" }, 409),
      "GET /api/status": () => json({ unread_total: 3, runs: [{ id: "5", kind: "auto_read", done: 7, total: 90, changed: 7, new_items: 0, errors: 0 }] }),
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
    await user.click(await screen.findByRole("button", { name: "Mark 40 older articles as read now" }));
    expect(await screen.findByTestId("auto-read-progress")).toHaveTextContent("7 of 90");
    expect(screen.getByRole("button", { name: "Preview" })).toBeDisabled();
  });

  describe("a stale preview", () => {
    const realNow = Date.now.bind(Date);
    let skew = 0;
    beforeEach(() => {
      skew = 0;
      vi.spyOn(Date, "now").mockImplementation(() => realNow() + skew);
    });

    it("that has grown a lot is shown again with the new number and needs another confirmation", async () => {
      let n = 0;
      const { calls } = base({
        "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: n++ === 0 ? 60 : 200, feeds: [] }),
        "POST /api/library/auto-read/run": () => json(RUN, 202),
      });
      go("/settings");
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
      const mark = await screen.findByRole("button", { name: "Mark 60 older articles as read now" });
      skew = 61_000;
      await user.click(mark);
      const dlg = await screen.findByRole("dialog", { name: "Mark 200 older articles as read?" });
      expect(dlg).toHaveTextContent("The count grew from 60 to 200");
      expect(calls.some((c) => c.url.pathname.endsWith("/run"))).toBe(false);
      await user.click(within(dlg).getByRole("button", { name: "Mark 200 as read" }));
      await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/run"))).toBe(true));
      expect(body(calls.find((c) => c.url.pathname.endsWith("/run")))).toEqual({ confirm: true, expect_total: 200 });
    });

    it("that barely changed is counted again and then runs without asking", async () => {
      let n = 0;
      const { calls } = base({
        "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: n++ === 0 ? 60 : 62, feeds: [] }),
        "POST /api/library/auto-read/run": () => json(RUN, 202),
      });
      go("/settings");
      const user = userEvent.setup();
      await user.click(await screen.findByRole("button", { name: "Preview" }, { timeout: 5000 }));
      const mark = await screen.findByRole("button", { name: "Mark 60 older articles as read now" });
      skew = 61_000;
      await user.click(mark);
      await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/run"))).toBe(true));
      expect(calls.filter((c) => c.url.pathname.endsWith("/preview"))).toHaveLength(2);
      expect(screen.queryByRole("dialog")).toBeNull();
    });
  });

  it("will not mark with a what-if number that was never saved", async () => {
    base({ "POST /api/library/auto-read/preview": () => json({ ...PREVIEW, total: 5, feeds: [] }) });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Try a different number of days" }, { timeout: 5000 }));
    await user.type(await screen.findByRole("textbox", { name: /Preview as if it were set to/ }), "60");
    await user.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText(/using 60 days/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /older articles? as read now/ })).toBeNull();
    expect(screen.getByText(/only a preview/)).toBeInTheDocument();
  });
});
