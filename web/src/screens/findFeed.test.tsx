import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { Bootstrap, Feed, Folder } from "@/api/types";
import { devicePrefsStore, resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";

// Find a feed and Collapse all folders, on the Feeds screen and in the sidebar tree.

class NoES {
  addEventListener() {}
  close() {}
}

const uncategorized: Folder = { id: "1", name: "Uncategorized", position: 0, is_default: true, unread: 0 };
const news: Folder = { id: "2", name: "News", position: 1, is_default: false, unread: 0 };
const tech: Folder = { id: "3", name: "Tech", position: 2, is_default: false, unread: 0 };
const feed = (id: number, title: string, folder_id: string, over: Partial<Feed> = {}): Feed => ({ ...bootstrap.feeds[0]!, id: String(id), title, folder_id, unread: 0, ...over });

/** 15 feeds in News and 5 in Tech: past the 12 where the field is offered. */
const library = [
  ...Array.from({ length: 15 }, (_, i) => feed(i + 1, `Wire ${i + 1}`, "2")),
  ...Array.from({ length: 5 }, (_, i) => feed(i + 101, `Byte ${i + 1}`, "3")),
];

function go(path: string, feeds: Feed[] = library, calls?: { current: number }) {
  const boot: Bootstrap = { ...bootstrap, folders: [uncategorized, news, tech], feeds, counts: { unread: 0, starred: 0 } };
  mockFetch({
    "GET /api/bootstrap": () => {
      if (calls) calls.current++;
      return json(boot);
    },
    "GET /api/items": () => json(pageOf([])),
  });
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const rows = (root: HTMLElement) => [...root.querySelectorAll<HTMLAnchorElement>('a[data-reveal-key^="feed:"]')].map((a) => a.textContent?.trim());

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => vi.unstubAllGlobals());

describe("Find a feed on the Feeds screen", () => {
  it("is offered only when there are more than a dozen feeds", async () => {
    go("/feeds", library.slice(0, 12));
    await screen.findByRole("main");
    await screen.findByText("Wire 1");
    expect(screen.queryByRole("searchbox", { name: "Find a feed" })).toBeNull();
  });

  it("keeps the feeds whose name holds the text, case-insensitively, with the folder above them", async () => {
    go("/feeds");
    const main = await screen.findByRole("main");
    const user = userEvent.setup();
    await user.type(await within(main).findByRole("searchbox", { name: "Find a feed" }), "BYTE 2");
    expect(rows(main)).toEqual(["Byte 2"]);
    expect(within(main).getByRole("button", { name: "Collapse Tech" })).toBeInTheDocument(); // its folder stays as context
    expect(within(main).queryByRole("button", { name: "Collapse News" })).toBeNull(); // a folder with no match goes
    expect(within(main).queryByRole("link", { name: /Feed index/ })).toBeNull();
  });

  it("finds a folder by its name and shows all its feeds", async () => {
    go("/feeds");
    const main = await screen.findByRole("main");
    await userEvent.setup().type(await within(main).findByRole("searchbox", { name: "Find a feed" }), "tech");
    expect(rows(main)).toEqual(["Byte 1", "Byte 2", "Byte 3", "Byte 4", "Byte 5"]);
  });

  it("announces how many feeds it shows, politely", async () => {
    go("/feeds");
    const main = await screen.findByRole("main");
    const user = userEvent.setup();
    const box = await within(main).findByRole("searchbox", { name: "Find a feed" });
    expect(within(main).queryByText(/^Showing/)).toBeNull();
    await user.type(box, "wire 1");
    // Wire 1 and Wire 10 to 15.
    expect(within(main).getByRole("status", { name: "" })).toHaveTextContent("Showing 7 of 20 feeds");
    await user.clear(box);
    await user.type(box, "zzz");
    expect(within(main).getByText("Showing 0 of 20 feeds")).toBeInTheDocument();
    expect(within(main).getByText("No feeds match.")).toBeInTheDocument();
  });

  it("clears with Escape and shows every feed again", async () => {
    go("/feeds");
    const main = await screen.findByRole("main");
    const user = userEvent.setup();
    const box = await within(main).findByRole("searchbox", { name: "Find a feed" });
    await user.type(box, "byte");
    expect(rows(main)).toHaveLength(5);
    await user.keyboard("{Escape}");
    expect(box).toHaveValue("");
    expect(rows(main)).toHaveLength(20);
  });

  it("opens a collapsed folder to show its matches without changing what is saved", async () => {
    updateDevicePrefs({ collapsedFolders: ["3"] });
    go("/feeds");
    const main = await screen.findByRole("main");
    await within(main).findByText("Wire 1");
    expect(rows(main).some((r) => r?.startsWith("Byte"))).toBe(false);
    const user = userEvent.setup();
    await user.type(within(main).getByRole("searchbox", { name: "Find a feed" }), "byte 3");
    expect(rows(main)).toEqual(["Byte 3"]);
    expect(devicePrefsStore.get().collapsedFolders).toEqual(["3"]);
    await user.keyboard("{Escape}");
    expect(rows(main).some((r) => r?.startsWith("Byte"))).toBe(false); // the folder is collapsed again
  });

  it("does not ask the server again for each keystroke", async () => {
    const calls = { current: 0 };
    const many = Array.from({ length: 500 }, (_, i) => feed(i + 1, `Feed number ${i}`, i % 2 ? "2" : "3"));
    go("/feeds", many, calls);
    const main = await screen.findByRole("main");
    const box = await within(main).findByRole("searchbox", { name: "Find a feed" });
    const before = calls.current;
    // Each change is what a keystroke does; one draw each, no request.
    for (const text of ["f", "fe", "feed n", "feed number 4"]) fireEvent.change(box, { target: { value: text } });
    expect(calls.current).toBe(before);
    expect(within(main).getByText("Showing 111 of 500 feeds")).toBeInTheDocument();
  });
});

describe("Collapse all folders", () => {
  it("is in the Feeds screen's menu: it collapses every folder, then offers to expand them, and remembers", async () => {
    go("/feeds");
    const main = await screen.findByRole("main");
    await within(main).findByText("Wire 1");
    const user = userEvent.setup();
    await user.click(within(main).getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Collapse all folders" }));
    expect(rows(main)).toEqual([]);
    expect(devicePrefsStore.get().collapsedFolders.sort()).toEqual(["2", "3"]);
    await user.click(within(main).getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Expand all folders" }));
    expect(rows(main)).toHaveLength(20);
    expect(devicePrefsStore.get().collapsedFolders).toEqual([]);
  });
});

describe("Find a feed and Collapse all folders in the sidebar", () => {
  const sidebar = async () => {
    wide();
    go("/l/unread");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("tree", { name: "Feeds" });
    return nav;
  };

  it("opens a field from a button, filters the tree as you type and says how many it shows", async () => {
    const nav = await sidebar();
    const user = userEvent.setup();
    expect(within(nav).queryByRole("searchbox")).toBeNull(); // closed until asked for
    await user.click(within(nav).getByRole("button", { name: "Find a feed" }));
    const box = within(nav).getByRole("searchbox", { name: "Find a feed" });
    expect(box).toHaveFocus();
    await user.type(box, "byte 2");
    const tree = within(nav).getByRole("tree", { name: "Feeds" });
    expect(within(tree).getAllByRole("treeitem").map((t) => t.getAttribute("aria-label"))).toEqual(["Tech", "Byte 2"]);
    expect(within(nav).getByText("Showing 1 of 20 feeds")).toBeInTheDocument();
  });

  it("Escape clears the text, and on an empty field closes it and returns to the button", async () => {
    const nav = await sidebar();
    const user = userEvent.setup();
    await user.click(within(nav).getByRole("button", { name: "Find a feed" }));
    await user.type(within(nav).getByRole("searchbox"), "wire");
    await user.keyboard("{Escape}");
    expect(within(nav).getByRole("searchbox")).toHaveValue("");
    expect(within(within(nav).getByRole("tree", { name: "Feeds" })).getAllByRole("treeitem")).toHaveLength(22); // 2 folders and 20 feeds
    await user.keyboard("{Escape}");
    expect(within(nav).queryByRole("searchbox")).toBeNull();
    expect(within(nav).getByRole("button", { name: "Find a feed" })).toHaveFocus();
  });

  it("collapses every folder from a button and expands them again", async () => {
    const nav = await sidebar();
    const tree = within(nav).getByRole("tree", { name: "Feeds" });
    expect(within(tree).getAllByRole("treeitem")).toHaveLength(22);
    const user = userEvent.setup();
    await user.click(within(nav).getByRole("button", { name: "Collapse all folders" }));
    expect(within(tree).getAllByRole("treeitem").map((t) => t.getAttribute("aria-label"))).toEqual(["News", "Tech"]);
    expect(devicePrefsStore.get().collapsedFolders.sort()).toEqual(["2", "3"]);
    await user.click(within(nav).getByRole("button", { name: "Expand all folders" }));
    await waitFor(() => expect(within(tree).getAllByRole("treeitem")).toHaveLength(22));
    expect(devicePrefsStore.get().collapsedFolders).toEqual([]);
  });

  it("is not offered a field for a small library", async () => {
    wide();
    go("/l/unread", library.slice(0, 5));
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("tree", { name: "Feeds" });
    expect(within(nav).queryByRole("button", { name: "Find a feed" })).toBeNull();
    act(() => undefined);
  });
});
