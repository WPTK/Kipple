import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, type InfiniteData } from "@tanstack/react-query";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { handleServerEvent, initialLive, liveStore } from "@/api/events";
import { emptyDraft, type Filter } from "@/api/filters";
import { keys } from "@/api/queries";
import type { Bootstrap, Card, ItemsPage } from "@/api/types";
import { rowMenuStore } from "@/gestures/rowMenu";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { closeFilterEditor, openFilterEditor } from "@/lib/similar";
import { closeFeedEditor } from "@/lib/feedEditor";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

let qc: QueryClient;
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  qc = makeQueryClient({ retry: false });
  return render(<App client={qc} />);
}

function wide() {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("min-width: 900px"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

const withMuted = (n: number): Bootstrap => ({ ...bootstrap, counts: { unread: 3, starred: 0, muted: n } });
const muted = (n: number, name: string | null = "No giveaways", over: Partial<Card> = {}) =>
  card(n, { read: true, muted_by: "4", muted_by_name: name, title: `Muted article ${n}`, ...over });

const rule: Filter = {
  id: "4",
  name: "No giveaways",
  enabled: true,
  scope: "global",
  folder_id: null,
  feed_id: null,
  kind: "text",
  terms: ["giveaway"],
  fields: ["title"],
  case_sensitive: false,
  whole_word: true,
  fold_diacritics: true,
  invert: false,
  action: "mute",
  position: 1,
  hits: 3,
  last_hit_at: 1,
  created_at: 1,
  updated_at: 1,
  muted_items: 2,
};

function routes(extra: Parameters<typeof mockFetch>[0] = {}, boot: Bootstrap = withMuted(2)) {
  return mockFetch({
    "GET /api/bootstrap": () => json(boot),
    "GET /api/items": (u) => json(pageOf(u.searchParams.get("view") === "muted" ? [muted(1), muted(2)] : [card(1), card(2)])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [rule] }),
    "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    "POST /api/items/unmute": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, made_unread: JSON.parse(String(init?.body)).ids }),
    ...extra,
  });
}

beforeEach(() => {
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  closeFilterEditor();
  closeFeedEditor();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  closeFilterEditor();
  closeFeedEditor();
  vi.unstubAllGlobals();
});

const body = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body));

describe("Muted entry and count", () => {
  it("the sidebar has Muted with the count, and a counts event moves it", async () => {
    wide();
    routes();
    go("/l/unread");
    const link = await within(await screen.findByRole("navigation", { name: "Primary" })).findByRole("link", { name: /Muted/ });
    expect(link).toHaveAttribute("href", "/l/muted");
    expect(within(link).getByTestId("muted-count")).toHaveTextContent("2");
    act(() => handleServerEvent(qc, { type: "counts", data: { unread_total: 3, muted: 9, feeds: { "1": 3 } } }));
    await waitFor(() => expect(within(link).getByTestId("muted-count")).toHaveTextContent("9"));
  });

  it("the Feeds screen on a phone has it too", async () => {
    routes();
    go("/feeds");
    const link = await screen.findByRole("link", { name: /Muted/ });
    expect(link).toHaveAttribute("href", "/l/muted");
    expect(link).toHaveTextContent("2");
  });

  it("a list header offers a Muted pill only when something is muted", async () => {
    routes({}, withMuted(0));
    go("/l/unread");
    await screen.findByText("Article number 1");
    expect(within(screen.getByRole("navigation", { name: "Show" })).queryByRole("link", { name: "Muted" })).toBeNull();
  });

  it("... and does when something is", async () => {
    routes({}, withMuted(3));
    go("/l/unread");
    expect(await within(await screen.findByRole("navigation", { name: "Show" })).findByRole("link", { name: "Muted" })).toBeInTheDocument();
  });
});

describe("the Muted list", () => {
  it("asks for view=muted and says which rule muted each article", async () => {
    const { calls } = routes();
    const { container } = go("/l/muted");
    await screen.findByText("Muted article 1");
    expect(calls.find((c) => c.url.pathname === "/api/items")?.url.searchParams.get("view")).toBe("muted");
    expect(screen.getByRole("heading", { name: "Muted" })).toBeInTheDocument();
    expect(screen.getByText(/Restore brings one back as it was before it was muted/)).toBeInTheDocument();
    const strips = screen.getAllByTestId("muted-strip");
    expect(strips).toHaveLength(2);
    expect(strips[0]).toHaveTextContent("Muted by No giveaways");
    expect(screen.getByRole("button", { name: "Restore Muted article 1" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /Edit the rule that muted/ })).toHaveLength(2);
    expect(await axe(container)).toHaveNoViolations();
  });

  it("Restore keeps an article read when it was read before the mute, and says so", async () => {
    routes({ "POST /api/items/unmute": () => json({ changed: ["1001"], made_unread: [] }) });
    wide();
    go("/l/muted");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Restore Muted article 1" }));
    await waitFor(() => expect(screen.getByTestId("live-region")).toHaveTextContent("Restored. It stays read, as it was before it was muted."));
    await waitFor(() => expect(screen.queryByText("Muted article 1")).toBeNull());
  });

  it("an article whose rule was deleted says so and has no Edit rule", async () => {
    routes({ "GET /api/items": () => json(pageOf([muted(1, null)])) });
    go("/l/muted");
    await screen.findByText("Muted article 1");
    expect(screen.getByTestId("muted-strip")).toHaveTextContent("Muted by a filter that was deleted");
    expect(screen.queryByRole("button", { name: /Edit the rule/ })).toBeNull();
  });

  it("Restore un-mutes the article with the read state it had before, takes its row out and lowers the count", async () => {
    const { calls } = routes();
    wide();
    go("/l/muted");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Restore Muted article 1" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/unmute")).toBe(true));
    expect(body(calls.find((c) => c.url.pathname === "/api/items/unmute") as never)).toEqual({ ids: ["1001"] });
    expect(calls.some((c) => c.url.pathname === "/api/items/mark-read")).toBe(false);
    await waitFor(() => expect(screen.queryByText("Muted article 1")).toBeNull());
    expect(screen.getByText("Muted article 2")).toBeInTheDocument();
    expect(qc.getQueryData<Bootstrap>(keys.bootstrap)?.counts.muted).toBe(1);
    expect(screen.getByTestId("live-region")).toHaveTextContent("Restored. It is unread again.");
    // No undo is offered: putting it back under its rule is not something the client can do.
    expect(screen.getByTestId("undo-region")).not.toHaveTextContent("Undo");
  });

  it("the row menu says Restore and Edit rule instead of the read actions, and Edit rule opens the rule", async () => {
    routes();
    go("/l/muted");
    const user = userEvent.setup();
    const row = (await screen.findByText("Muted article 2")).closest("[data-item-id]") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^More actions: / }));
    expect(await screen.findByRole("menuitem", { name: "Restore" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Mark above as read" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Mark as unread" })).toBeNull();
    await user.click(screen.getByRole("menuitem", { name: "Edit rule" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit filter" });
    expect(within(dialog).getByRole("checkbox", { name: "Title" })).toBeChecked();
    expect(within(dialog).getByText("giveaway")).toBeInTheDocument();
  });

  it("has nothing to mark all as read", async () => {
    routes();
    go("/l/muted");
    const user = userEvent.setup();
    await screen.findByText("Muted article 1");
    await user.click(screen.getByRole("button", { name: "List actions" }));
    expect(await screen.findByRole("menuitem", { name: /^Mark (all|everything)/ })).toHaveAttribute("aria-disabled", "true");
  });

  it("says so when nothing is muted", async () => {
    routes({ "GET /api/items": () => json(pageOf([])) }, withMuted(0));
    go("/l/muted");
    expect(await screen.findByText("Nothing muted")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a filter in Settings" })).toHaveAttribute("href", "/settings/filters");
  });

  it("a muted article opened shows Muted by its rule with Restore and Edit rule", async () => {
    const { calls } = routes({
      "GET /api/items/1001": () => json(detail(1, { read: true, muted_by: "4", muted_by_name: "No giveaways" })),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true, muted_by: "4", muted_by_name: "No giveaways" }) }),
    });
    go("/i/1001?from=muted");
    const banner = await screen.findByTestId("muted-banner");
    expect(banner).toHaveTextContent("Muted by No giveaways");
    expect(banner).not.toHaveAttribute("role"); // not a live region: the action that muted it says so itself
    await userEvent.setup().click(within(banner).getByRole("button", { name: "Restore" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/unmute")).toBe(true));
    expect(body(calls.find((c) => c.url.pathname === "/api/items/unmute") as never)).toEqual({ ids: ["1001"] });
    await waitFor(() => expect(screen.queryByTestId("muted-banner")).toBeNull());
  });
});

describe("events that carry muted", () => {
  const seed = (scope: string, items: Card[]) =>
    qc.setQueryData<InfiniteData<ItemsPage>>(keys.items({ view: scope as "all" }), { pageParams: [""], pages: [{ items, next_cursor: null }] });

  it("items.state muted:true takes the articles out of All and search and marks the Muted list stale", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    seed("all", [card(1), card(2)]);
    qc.setQueryData(keys.items({ view: "all", q: "x" }), { pageParams: [""], pages: [{ items: [card(1), card(2)], next_cursor: null }] } as InfiniteData<ItemsPage>);
    qc.setQueryData(keys.items({ view: "muted" }), { pageParams: [""], pages: [{ items: [], next_cursor: null }] } as InfiniteData<ItemsPage>);
    act(() => handleServerEvent(qc, { type: "items.state", data: { ids: ["1001"], read: true, muted: true, source: "web" } }));
    const ids = (k: Parameters<typeof keys.items>[0]) => qc.getQueryData<InfiniteData<ItemsPage>>(keys.items(k))?.pages[0]?.items.map((i) => i.id);
    expect(ids({ view: "all" })).toEqual(["1002"]);
    expect(ids({ view: "all", q: "x" })).toEqual(["1002"]);
    expect(qc.getQueryState(keys.items({ view: "muted" }))?.isInvalidated).toBe(true);
  });

  it("items.state muted:false (a filter deleted with un-muting) clears the mute and takes them out of Muted", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    qc.setQueryData(keys.items({ view: "muted" }), { pageParams: [""], pages: [{ items: [muted(1), muted(2)], next_cursor: null }] } as InfiniteData<ItemsPage>);
    qc.setQueryData(keys.items({ view: "unread" }), { pageParams: [""], pages: [{ items: [muted(1, "x")], next_cursor: null }] } as InfiniteData<ItemsPage>);
    act(() => handleServerEvent(qc, { type: "items.state", data: { ids: ["1001"], muted: false, source: "web" } }));
    expect(qc.getQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "muted" }))?.pages[0]?.items.map((i) => i.id)).toEqual(["1002"]);
    expect(qc.getQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "unread" }))?.pages[0]?.items[0]?.muted_by).toBeNull();
  });

  it("filters.changed refetches the filters and the bootstrap (highlights and counts)", async () => {
    const { calls } = routes();
    go("/settings/filters");
    await screen.findByRole("list", { name: "Your filters" });
    const before = calls.filter((c) => c.url.pathname === "/api/filters").length;
    const bootBefore = calls.filter((c) => c.url.pathname === "/api/bootstrap").length;
    act(() => handleServerEvent(qc, { type: "filters.changed", data: {} }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/filters").length).toBeGreaterThan(before));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/bootstrap").length).toBeGreaterThan(bootBefore));
  });
});

describe("Mute similar…", () => {
  it("opens the filter editor from a row's menu, scoped to the feed with no terms, and toggles suggestions", async () => {
    routes({ "POST /api/filters/preview": () => json({ matches: 0, scanned: 0, truncated: false, sample: [], warnings: [] }) });
    go("/l/unread");
    const user = userEvent.setup();
    const row = (await screen.findByText("Article number 1")).closest("[data-item-id]") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^More actions: / }));
    await user.click(await screen.findByRole("menuitem", { name: "Mute similar…" }));
    const dialog = within(await screen.findByRole("dialog", { name: "New filter" }));
    expect(dialog.getByRole("radio", { name: /^A feed/ })).toBeChecked();
    expect(dialog.getByLabelText("Feed")).toHaveValue("1");
    expect(dialog.getByRole("region", { name: "Suggestions from this article" })).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Author Ada" })).toBeInTheDocument();
    expect(dialog.queryByRole("list", { name: "Terms in this filter" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Save filter" })).toBeDisabled();
    expect(dialog.getByRole("radio", { name: /^Mute/ })).toBeChecked();
    // A suggestion adds the author as a term and ticks the author field; a second tap takes the term out.
    await user.click(dialog.getByRole("button", { name: "Author Ada" }));
    const chips = dialog.getByRole("list", { name: "Terms in this filter" });
    expect(within(chips).getByText("Ada")).toBeInTheDocument();
    expect(dialog.getByRole("checkbox", { name: "Author" })).toBeChecked();
    expect(dialog.getByRole("button", { name: "Save filter" })).toBeEnabled();
    expect(dialog.getByRole("button", { name: "Author Ada" })).toHaveAttribute("aria-pressed", "true");
    await user.click(dialog.getByRole("button", { name: "Author Ada" }));
    expect(dialog.getByRole("button", { name: "Author Ada" })).toHaveAttribute("aria-pressed", "false");
    expect(dialog.queryByRole("list", { name: "Terms in this filter" })).not.toBeInTheDocument();
    // The Author checkbox is the reader's to untick; the chip reads only the terms.
    expect(dialog.getByRole("checkbox", { name: "Author" })).toBeChecked();
    // Removing the term from the list turns the chip off too.
    await user.click(dialog.getByRole("button", { name: "Author Ada" }));
    await user.click(dialog.getByRole("button", { name: "Remove Ada" }));
    expect(dialog.getByRole("button", { name: "Author Ada" })).toHaveAttribute("aria-pressed", "false");
  });
});

describe("Mute similar… saves and says what it did to the article", () => {
  it.each([
    ["muted", "Filter saved. This article is muted."],
    ["no_match", "Filter saved. This article was not muted because the filter no longer matches it."],
    ["starred", "Filter saved. This article is starred, so it was not muted."],
    ["already_muted", "Filter saved. This article was already muted by another filter."],
    ["star_filter", "Filter saved. A star filter matches this article, so it was not muted."],
    ["archived", "Filter saved. This article was not muted because its feed is archived."],
    ["gone", "Filter saved. This article no longer exists."],
    ["not_mute", "Filter saved. This article was not muted because the filter does not mute."],
    ["disabled", "Filter saved. This article was not muted because the filter is turned off."],
  ] as const)("%s", async (result, message) => {
    const { calls } = routes({
      "POST /api/filters/preview": () => json({ matches: 0, scanned: 0, truncated: false, sample: [], warnings: [] }),
      "POST /api/filters": () => json({ filter: { ...rule, id: "9", name: "Ada" }, applied: null, source: { item_id: "1001", result } }, 201),
    });
    go("/l/unread");
    const user = userEvent.setup();
    const row = (await screen.findByText("Article number 1")).closest("[data-item-id]") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^More actions: / }));
    await user.click(await screen.findByRole("menuitem", { name: "Mute similar…" }));
    const dialog = within(await screen.findByRole("dialog", { name: "New filter" }));
    await user.click(dialog.getByRole("button", { name: "Author Ada" }));
    await user.click(dialog.getByRole("button", { name: "Save filter" }));
    await waitFor(() => expect(screen.getByTestId("live-region")).toHaveTextContent(message));
    expect(body(calls.find((c) => c.init?.method === "POST" && c.url.pathname === "/api/filters") as never)).toMatchObject({ source_item_id: "1001", action: "mute" });
    const cached = qc
      .getQueriesData<InfiniteData<ItemsPage>>({ queryKey: keys.itemsAll })
      .flatMap(([, d]) => d?.pages.flatMap((p) => p.items) ?? [])
      .find((i) => i.id === "1001");
    // The reader asked for this article to go: a muted one leaves the list on screen, Unread included.
    if (result === "muted") {
      expect(cached).toBeUndefined();
      await waitFor(() => expect(screen.queryByText("Article number 1")).toBeNull());
    } else {
      expect(cached).toMatchObject({ read: false, muted_by: null });
      expect(screen.getByText("Article number 1")).toBeInTheDocument();
    }
    // Said once: the toast is the announcement.
    expect(screen.getByTestId("live-region").textContent?.replace(/\u200b/g, "")).toBe(message);
  });
});

describe("Mute similar… from the open article", () => {
  // The muted article leaves the list it was opened from; Next and Previous go on from where it was.
  it.each(["phone", "wide"] as const)("%s: Previous and Next still work after the article leaves its list", async (layout) => {
    if (layout === "wide") wide();
    prefsStore.set({ ...DEFAULT_PREFS });
    const open = (n: number) => () => json({ session_key: "k", item: detail(n, { read: true }) });
    routes({
      "GET /api/items": () => json(pageOf([card(1), card(2), card(3)])),
      ...Object.fromEntries([1, 2, 3].map((n) => [`GET /api/items/${1000 + n}`, () => json(detail(n))])),
      ...Object.fromEntries([1, 2, 3].map((n) => [`POST /api/items/${1000 + n}/open`, open(n)])),
      "POST /api/filters/preview": () => json({ matches: 0, scanned: 0, truncated: false, sample: [], warnings: [] }),
      "POST /api/filters": () => json({ filter: { ...rule, id: "9", name: "Ada" }, applied: null, source: { item_id: "1002", result: "muted" } }, 201),
    });
    go("/i/1002?from=unread");
    const user = userEvent.setup();
    await screen.findByRole("heading", { level: 1, name: "Article number 2" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Next article" })).toBeEnabled());
    await user.click(within(screen.getByRole("toolbar", { name: "Article actions" })).getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mute similar…" }));
    const dialog = within(await screen.findByRole("dialog", { name: "New filter" }));
    await user.click(dialog.getByRole("button", { name: "Author Ada" }));
    await user.click(dialog.getByRole("button", { name: "Save filter" }));
    await waitFor(() => expect(screen.getByTestId("live-region")).toHaveTextContent("This article is muted."));
    const cached = qc.getQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "unread" }))?.pages[0]?.items.map((i) => i.id);
    expect(cached).toEqual(["1001", "1003"]);
    expect(screen.getByRole("button", { name: "Previous article" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Next article" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1003"));
    await user.click(screen.getByRole("button", { name: "Previous article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1001"));
  });
});

describe("Mute similar… and undo", () => {
  // The mute happened where this tab did not hear of it (another device, or while the live connection was down), so
  // only the server knows: it answers the undo with the article it left muted, and the article stays read and gone.
  it("Undo after an article marked read was muted elsewhere leaves it read and out of Unread", async () => {
    prefsStore.set({ ...DEFAULT_PREFS });
    const { calls } = routes({
      "POST /api/items/mark-read": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as { ids: string[]; undo?: boolean };
        return json(b.undo ? { changed: [], restored: [], kept: ["1001"] } : { changed: b.ids, restored: [] });
      },
    });
    go("/l/unread");
    const user = userEvent.setup();
    await screen.findByText("Article number 1");
    await user.click(screen.getAllByRole("button", { name: /^More actions: / })[0] as HTMLElement);
    await user.click(await screen.findByRole("menuitem", { name: "Mark as read" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/items/mark-read" && body(c).read === true)).toBe(true));
    await user.click(await screen.findByRole("button", { name: "Undo" }));
    // Marking unread is also the un-mute: the undo says it is one, and the server skips an article muted since.
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/items/mark-read" && body(c).read === false).map(body)).toEqual([expect.objectContaining({ ids: ["1001"], undo: true })]));
    await waitFor(() => expect(screen.queryByText("Article number 1")).toBeNull());
    const cached = qc.getQueryData<InfiniteData<ItemsPage>>(keys.items({ view: "unread" }))?.pages[0]?.items.map((i) => i.id);
    expect(cached).toEqual(["1002"]);
    expect(screen.getByText("Article number 2")).toBeInTheDocument();
  });
});

describe("Mute similar… apostrophes", () => {
  it("shows a suggestion pressed when the rule already holds it with a typographic apostrophe", async () => {
    routes({ "POST /api/filters/preview": () => json({ matches: 0, scanned: 0, truncated: false, sample: [], warnings: [] }) });
    go("/l/unread");
    const user = userEvent.setup();
    await screen.findByText("Article number 1");
    act(() => openFilterEditor({ mode: "create", seed: { draft: emptyDraft({ scope: "global", terms: ["Apple’s"], fields: ["title"], action: "mute" }), keywords: ["Apple's"], author: null, feedTitle: "X" } }));
    const dialog = within(await screen.findByRole("dialog", { name: "New filter" }));
    expect(dialog.getByRole("button", { name: "Word Apple's" })).toHaveAttribute("aria-pressed", "true");
    await user.click(dialog.getByRole("button", { name: "Word Apple's" }));
    expect(dialog.queryByRole("list", { name: "Terms in this filter" })).not.toBeInTheDocument();
  });
});

describe("Mute similar… term limit", () => {
  it("says so and adds nothing when a suggestion is tapped at the limit", async () => {
    routes({ "POST /api/filters/preview": () => json({ matches: 0, scanned: 0, truncated: false, sample: [], warnings: [] }) });
    go("/l/unread");
    const user = userEvent.setup();
    await screen.findByText("Article number 1");
    const terms = Array.from({ length: 50 }, (_, i) => `term${i}`);
    act(() => openFilterEditor({ mode: "create", seed: { draft: emptyDraft({ scope: "global", terms, fields: ["title"], action: "mute" }), keywords: ["Zebra"], author: null, feedTitle: "X" } }));
    const dialog = within(await screen.findByRole("dialog", { name: "New filter" }));
    await user.click(dialog.getByRole("button", { name: "Word Zebra" }));
    expect(dialog.getByText(/at most 50 words or phrases/)).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Word Zebra" })).toHaveAttribute("aria-pressed", "false");
    expect(within(dialog.getByRole("list", { name: "Terms in this filter" })).getAllByRole("listitem")).toHaveLength(50);
    // The message goes away once a term is removed, and the word can be added.
    await user.click(dialog.getByRole("button", { name: "Remove term0" }));
    expect(dialog.queryByText(/at most 50 words or phrases/)).not.toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "Word Zebra" }));
    expect(dialog.getByRole("button", { name: "Word Zebra" })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("Manage this feed", () => {
  it("opens the feed editor for the article's feed from a row's menu", async () => {
    routes({
      "GET /api/feeds/1": () =>
        json({
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
        }),
    });
    go("/l/unread");
    const user = userEvent.setup();
    const row = (await screen.findByText("Article number 1")).closest("[data-item-id]") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^More actions: / }));
    await user.click(await screen.findByRole("menuitem", { name: "Manage this feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit feed", description: "Example Feed" });
    expect(await within(dlg).findByLabelText("Feed address")).toHaveValue("https://example.com/feed.xml");
  });

  it("says the feed no longer exists rather than silently doing nothing", async () => {
    // A muted article whose feed was since deleted: not in the bootstrap feed list.
    routes({ "GET /api/items": (u) => json(pageOf(u.searchParams.get("view") === "muted" ? [muted(1, null, { feed_id: "gone" })] : [card(1)])) });
    go("/l/muted");
    const user = userEvent.setup();
    const row = (await screen.findByText("Muted article 1")).closest("[data-item-id]") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^More actions: / }));
    await user.click(await screen.findByRole("menuitem", { name: "Manage this feed" }));
    expect(await screen.findByText("This feed no longer exists.")).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Edit feed" })).toBeNull();
  });
});
