import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Bootstrap } from "@/api/types";
import { rowMenuStore } from "@/gestures/rowMenu";
import { LAYOUT_LABELS, devicePrefsStore, resetDevicePrefs, setLayoutOverride, updateDevicePrefs } from "@/lib/devicePrefs";
import { QueryClient } from "@tanstack/react-query";
import { bumpUnread, keys } from "@/api/queries";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

function media(...on: string[]) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: on.some((q) => query.includes(q)),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}
const WIDE = "min-width: 900px";

const feedBase = bootstrap.feeds[0] as Bootstrap["feeds"][number];
const feed = (id: string, folder: string, title: string, unread = 1) => ({ ...feedBase, id, folder_id: folder, title, unread });
// Uncategorized; Tech > Apple > Mac; Tech > Empty; Sports. Tech itself holds no feed of its own.
const nested: Bootstrap = {
  ...bootstrap,
  settings: { "library.favorites": [] },
  folders: [
    { id: "1", name: "Uncategorized", parent_id: null, position: 0, is_default: true, unread: 1 },
    { id: "2", name: "Tech", parent_id: null, position: 1, is_default: false, unread: 3 },
    { id: "3", name: "Apple", parent_id: "2", position: 2, is_default: false, unread: 3 },
    { id: "4", name: "Mac", parent_id: "3", position: 3, is_default: false, unread: 2 },
    { id: "6", name: "Empty", parent_id: "2", position: 4, is_default: false, unread: 0 },
    { id: "5", name: "Sports", parent_id: null, position: 5, is_default: false, unread: 1 },
  ],
  feeds: [feed("1", "1", "Loose"), feed("2", "3", "Apple Daily"), feed("3", "4", "Mac Weekly", 2), feed("4", "5", "Scores")],
  counts: { unread: 5, starred: 0 },
};

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(nested),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/items/1001": () => json(detail(1)),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "PATCH /api/settings": (_u, init) => json({ settings: [], values: JSON.parse(String(init?.body)) }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    "POST /api/folders": (_u, init) => json({ id: "9", name: JSON.parse(String(init?.body)).name, position: 9, is_default: false, unread: 0 }),
    "PATCH /api/folders/3": () => json({ id: "3", name: "Apple", parent_id: "5", position: 2, is_default: false, unread: 3 }),
    "POST /api/reorder": () => json({ changed_feeds: [], changed_folders: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const sidebarTree = async () => {
  const nav = await screen.findByRole("navigation", { name: "Primary" });
  return within(nav).findByRole("tree", { name: "Feeds" });
};
const item = (tree: HTMLElement, name: RegExp) => within(tree).getAllByRole("treeitem").find((el) => name.test(el.getAttribute("data-tree-key") ? (el.querySelector("a")?.textContent ?? "") : "")) as HTMLElement;

describe("the sidebar folder tree", () => {
  it("nests folders, rolls counts up, and hides a branch with no feed", async () => {
    routes();
    media(WIDE);
    go("/l/unread");
    const tree = await sidebarTree();
    const tech = item(tree, /^Tech/);
    const mac = item(tree, /^Mac/);
    expect(tech).toHaveAttribute("aria-level", "1");
    expect(item(tree, /^Apple/)).toHaveAttribute("aria-level", "2");
    expect(mac).toHaveAttribute("aria-level", "3");
    expect(within(mac).getAllByRole("link")[0]).toHaveAttribute("href", "/l?folder=4");
    // Tech holds no feed itself but shows: its subtree does. Its badge is the subtree's.
    expect(within(tech).getAllByTestId("unread-count")[0]).toHaveTextContent("3");
    expect(within(tree).queryByText("Empty")).toBeNull();
    expect(await axe(tree.closest("nav") as HTMLElement)).toHaveNoViolations();
  });

  it("is one tab stop moved with the arrow keys; Left and Right collapse and expand; Enter opens", async () => {
    routes();
    media(WIDE);
    go("/l/unread");
    const tree = await sidebarTree();
    const user = userEvent.setup();
    const stops = within(tree).getAllByRole("treeitem").filter((el) => el.tabIndex === 0);
    expect(stops).toHaveLength(1);
    stops[0]!.focus();
    expect(document.activeElement).toBe(item(tree, /^Uncategorized/));
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(document.activeElement).toBe(item(tree, /^Tech/));
    await user.keyboard("{ArrowLeft}");
    expect(devicePrefsStore.get().collapsedFolders).toEqual(["2"]);
    await waitFor(() => expect(item(tree, /^Tech/)).toHaveAttribute("aria-expanded", "false"));
    expect(within(tree).queryByText("Apple Daily")).toBeNull();
    await user.keyboard("{ArrowRight}");
    await waitFor(() => expect(item(tree, /^Tech/)).toHaveAttribute("aria-expanded", "true"));
    await user.keyboard("{ArrowRight}");
    expect(document.activeElement).toBe(item(tree, /^Apple/));
    await user.keyboard("{ArrowLeft}{ArrowLeft}");
    expect(document.activeElement).toBe(item(tree, /^Tech/)); // collapse Apple, then up to its parent
    expect(devicePrefsStore.get().collapsedFolders).toEqual(["3"]);
    expect(item(tree, /^Tech/).tabIndex).toBe(0);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(window.location.search).toBe("?folder=2"));
  });

  it("a folder list's title shows the folders above it", async () => {
    routes();
    media(WIDE);
    go("/l/unread?folder=4");
    await screen.findByText("Article number 1");
    expect(await screen.findByRole("heading", { level: 1, name: "Tech › Apple › Mac" })).toBeInTheDocument();
  });
});

describe("managing nested folders", () => {
  const actions = async (user: ReturnType<typeof userEvent.setup>, name: string) => {
    await user.click(await screen.findByRole("button", { name: `Folder actions for ${name}` }));
  };

  it("deleting a folder says its subfolders go and its feeds move to the default folder", async () => {
    routes();
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await actions(user, "Tech");
    await user.click(await screen.findByRole("menuitem", { name: "Delete folder" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete Tech?" });
    expect(dialog).toHaveTextContent("Its 3 subfolders are deleted too. The 2 feeds in it are not deleted: they move to Uncategorized.");
  });

  it("Move to… offers only valid places, labelled by path, and saves the move", async () => {
    const { calls } = routes();
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await actions(user, "Tech › Apple");
    await user.click(await screen.findByRole("menuitem", { name: "Move to…" }));
    const dialog = await screen.findByRole("dialog", { name: "Move Tech › Apple" });
    const select = within(dialog).getByRole("combobox", { name: "Move into" });
    const options = within(select).getAllByRole("option").map((o) => o.textContent);
    // Not the default folder, not Apple itself or Mac inside it.
    expect(options).toEqual(["Top level", "Tech", "Tech › Empty", "Sports"]);
    expect(select).toHaveValue("2");
    await user.selectOptions(select, "5");
    await user.click(within(dialog).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/reorder")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH" && c.url.pathname === "/api/folders/3");
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ parent_id: "5" });
    const order = calls.find((c) => c.url.pathname === "/api/reorder");
    expect(JSON.parse(String(order?.init?.body)).folders).toEqual(["1", "2", "6", "5", "3", "4"]);
  });

  it("New subfolder creates the folder inside the chosen one", async () => {
    const { calls } = routes();
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await actions(user, "Sports");
    await user.click(await screen.findByRole("menuitem", { name: "New subfolder" }));
    const dialog = await screen.findByRole("dialog", { name: "New subfolder" });
    expect(within(dialog).getByRole("combobox", { name: "Inside" })).toHaveValue("5");
    await user.type(within(dialog).getByRole("textbox", { name: "Name" }), "Football");
    await user.click(within(dialog).getByRole("button", { name: "Create" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/folders")).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.url.pathname === "/api/folders");
    expect(JSON.parse(String(post?.init?.body))).toEqual({ name: "Football", parent_id: "5" });
  });

  it("a refused move says why", async () => {
    routes({ "PATCH /api/folders/3": () => json({ error: "folder_exists", message: "exists" }, 409) });
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await actions(user, "Tech › Apple");
    await user.click(await screen.findByRole("menuitem", { name: "Move to…" }));
    const dialog = await screen.findByRole("dialog", { name: "Move Tech › Apple" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Move into" }), "5");
    await user.click(within(dialog).getByRole("button", { name: "Move" }));
    expect(await within(dialog).findByText("A folder with that name is already there.")).toBeInTheDocument();
  });

  it("the add-feed folder picker lists folders by path in tree order", async () => {
    routes();
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add feed" }));
    const dialog = await screen.findByRole("dialog");
    const options = within(within(dialog).getByRole("combobox", { name: "Folder" })).getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["Default folder", "Uncategorized", "Tech", "Tech › Apple", "Tech › Apple › Mac", "Tech › Empty", "Sports"]);
  });
});

describe("nested folder review fixes", () => {
  it("taking the focused item out of Favorites with P leaves the focus on the item now in its place, one tab stop", async () => {
    routes({ "GET /api/bootstrap": () => json({ ...nested, settings: { "library.favorites": [{ t: "feed", id: "2" }, { t: "feed", id: "3" }] } }) });
    media(WIDE);
    go("/l/unread");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    const favTree = await within(nav).findByRole("tree", { name: "Favorites" });
    const user = userEvent.setup();
    const first = within(favTree).getAllByRole("treeitem")[0]!;
    expect(first).toHaveAttribute("tabindex", "0");
    first.focus();
    // The star is not a tab stop inside the tree: Tab leaves the tree.
    expect(within(first).getByRole("button", { name: "Favorite Apple Daily" })).toHaveAttribute("tabindex", "-1");
    await user.keyboard("p");
    await waitFor(() => expect(within(favTree).getAllByRole("treeitem")).toHaveLength(1));
    const left = within(favTree).getAllByRole("treeitem")[0]!;
    await waitFor(() => expect(document.activeElement).toBe(left));
    expect(left).toHaveAttribute("tabindex", "0");
    await user.tab();
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(left);
  });

  it("taking the last favorite out moves the focus to the Feeds tree, not the page", async () => {
    routes({ "GET /api/bootstrap": () => json({ ...nested, settings: { "library.favorites": [{ t: "feed", id: "2" }] } }) });
    media(WIDE);
    go("/l/unread");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    const favTree = await within(nav).findByRole("tree", { name: "Favorites" });
    within(favTree).getAllByRole("treeitem")[0]!.focus();
    await userEvent.setup().keyboard("p");
    await waitFor(() => expect(within(nav).queryByRole("tree", { name: "Favorites" })).toBeNull());
    const feeds = within(nav).getByRole("tree", { name: "Feeds" });
    await waitFor(() => expect(feeds.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).toHaveAttribute("tabindex", "0");
  });

  it("a move whose order fails to save after the folder moved says so", async () => {
    routes({ "POST /api/reorder": () => json({ error: "internal" }, 500) });
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Folder actions for Tech › Apple" }));
    await user.click(await screen.findByRole("menuitem", { name: "Move to…" }));
    const dialog = await screen.findByRole("dialog", { name: "Move Tech › Apple" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Move into" }), "5");
    await user.click(within(dialog).getByRole("button", { name: "Move" }));
    expect(await within(dialog).findByText("Moved, but the order couldn't be saved.")).toBeInTheDocument();
  });

  it("after Move to…, the moved folder's actions button has the focus", async () => {
    routes();
    media(WIDE);
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Folder actions for Tech › Apple" }));
    await user.click(await screen.findByRole("menuitem", { name: "Move to…" }));
    const dialog = await screen.findByRole("dialog", { name: "Move Tech › Apple" });
    await user.selectOptions(within(dialog).getByRole("combobox", { name: "Move into" }), "5");
    await user.click(within(dialog).getByRole("button", { name: "Move" }));
    await waitFor(() => expect(document.activeElement?.id).toBe("folder-actions-3"));
  });

  it("a subfolder's layout menu names the folder it inherits from", async () => {
    routes();
    media(WIDE);
    setLayoutOverride("folder", "2", "cards");
    go("/l/unread?folder=4");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /^List options,/ }));
    expect(await screen.findByRole("menuitemradio", { name: `Inherited from Tech (${LAYOUT_LABELS.cards})` })).toBeChecked();
  });

  it("bumpUnread moves the feed's folder and every folder above it", () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.bootstrap, nested);
    bumpUnread(qc, "3", -1); // Mac Weekly, in Mac, in Apple, in Tech
    const unread = Object.fromEntries(qc.getQueryData<Bootstrap>(keys.bootstrap)!.folders.map((f) => [f.name, f.unread]));
    expect(unread).toEqual({ Uncategorized: 1, Tech: 2, Apple: 2, Mac: 1, Empty: 0, Sports: 1 });
  });
});
