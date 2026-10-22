import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import type { Bootstrap } from "@/api/types";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { resetUndo } from "@/lib/undo";
import { bootstrap, json, mockFetch } from "@/test/mockApi";

// The Feeds screen and the sidebar draw feed and folder rows with one component: a count, the "Unread badge" setting
// and the dimming of a read feed are the same wherever the row shows, Favorites included.

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const feedBase = bootstrap.feeds[0] as Bootstrap["feeds"][number];
const feed = (id: string, folder: string, title: string, unread: number) => ({ ...feedBase, id, folder_id: folder, title, unread });
const lib: Bootstrap = {
  ...bootstrap,
  counts: { unread: 7, starred: 2, muted: 0 },
  settings: { "library.favorites": [{ t: "folder", id: "2" }, { t: "feed", id: "1" }] },
  folders: [
    { id: "1", name: "News", position: 0, is_default: true, unread: 3 },
    { id: "2", name: "Tech", position: 1, is_default: false, unread: 4 },
    { id: "3", name: "Quiet", position: 2, is_default: false, unread: 0 },
  ],
  feeds: [feed("1", "1", "Alpha", 3), feed("2", "2", "Delta", 4), feed("3", "3", "Echo", 0), feed("4", "1", "Bravo", 0)],
};

const routes = (extra: Parameters<typeof mockFetch>[0] = {}) =>
  mockFetch({
    "GET /api/bootstrap": () => json(lib),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  resetDevicePrefs();
  resetUndo();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Feeds screen rows", () => {
  it("Favorites show the unread count of a favorite folder, its feeds and a favorite feed", async () => {
    routes();
    go("/feeds");
    const fav = await screen.findByRole("region", { name: "Favorites" });
    const rows = within(fav).getAllByRole("link");
    // The folder, the feed under it, then the favorite feed.
    expect(rows.map((l) => l.textContent)).toEqual([expect.stringMatching(/Tech\s*4/), expect.stringMatching(/Delta\s*4/), expect.stringMatching(/Alpha\s*3/)]);
    expect(within(fav).queryByText("Folder")).toBeNull();
  });

  it("the Unread badge setting applies to every row on the screen", async () => {
    updateDevicePrefs({ unreadBadge: "dot" });
    routes();
    go("/feeds");
    const fav = await screen.findByRole("region", { name: "Favorites" });
    expect(within(fav).queryByTestId("unread-count")).toBeNull();
    expect(within(fav).getAllByTestId("unread-dot").length).toBeGreaterThan(0);
    updateDevicePrefs({ unreadBadge: "off" });
    await waitFor(() => expect(screen.queryAllByTestId("unread-dot")).toHaveLength(0));
    expect(screen.queryAllByTestId("unread-count")).toHaveLength(0);
  });

  it("a feed with nothing unread is dimmed", async () => {
    routes();
    go("/feeds");
    const echo = await screen.findByText("Echo");
    expect(echo.closest("span.min-w-0")).toHaveClass("text-fg2");
    expect(screen.getAllByText("Alpha")[0]!.closest("span.min-w-0")).not.toHaveClass("text-fg2");
  });

  it("the plain visit shows no help text and no edit controls", async () => {
    routes();
    go("/feeds");
    await screen.findByText("Echo");
    expect(screen.queryByText(/arrow beside a folder/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Select" })).toBeNull();
  });

  it("Show only unread feeds leaves out read feeds and folders with none, and Show all brings them back", async () => {
    routes();
    go("/feeds");
    const user = userEvent.setup();
    await screen.findByText("Echo");
    await user.click(screen.getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show only unread feeds" }));
    expect(screen.getByText("Showing only feeds with unread articles.")).toBeInTheDocument();
    expect(screen.queryByText("Echo")).toBeNull(); // read feed
    expect(screen.queryByText("Quiet")).toBeNull(); // folder with no unread feed
    expect(screen.queryByText("Bravo")).toBeNull();
    expect(screen.getAllByText("Delta").length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "Show all" }));
    expect(await screen.findByText("Echo")).toBeInTheDocument();
    expect(screen.getByText("Quiet")).toBeInTheDocument();
  });

  it("Edit shows every feed even while Show only unread feeds is on", async () => {
    routes();
    go("/feeds");
    const user = userEvent.setup();
    await screen.findByText("Echo");
    await user.click(screen.getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show only unread feeds" }));
    expect(screen.queryByText("Echo")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(await screen.findByText("Echo")).toBeInTheDocument();
  });

  it("a folder's menu marks its unread articles read, and is left out where there are none", async () => {
    const { calls } = routes({ "POST /api/items/mark-read": () => json({ changed: [], count: 0 }) });
    go("/feeds");
    const user = userEvent.setup();
    await screen.findByText("Echo");
    await user.click(screen.getByRole("button", { name: "Folder actions for Quiet" }));
    expect(screen.queryByRole("menuitem", { name: "Mark all as read" })).toBeNull();
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Folder actions for Tech" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark all as read" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/mark-read")).toBe(true));
    const sent = calls.find((c) => c.url.pathname === "/api/items/mark-read");
    expect(JSON.parse(String(sent?.init?.body)).scope).toMatchObject({ folder_id: "2", view: "unread" });
  });
});
