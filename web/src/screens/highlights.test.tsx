import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { handleServerEvent, initialLive, liveStore } from "@/api/events";
import type { Bootstrap, Highlight } from "@/api/types";
import { rowMenuStore } from "@/gestures/rowMenu";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { highlightStore } from "@/lib/highlight";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

const hl = (terms: string[], over: Partial<Highlight> = {}): Highlight => ({
  id: "1",
  scope: "global",
  folder_id: null,
  feed_id: null,
  terms,
  fields: ["title", "content", "author"],
  case_sensitive: false,
  whole_word: true,
  fold_diacritics: true,
  ...over,
});

const boot = (highlights: Highlight[]): Bootstrap => ({
  ...bootstrap,
  highlights,
  feeds: [
    ...bootstrap.feeds,
    { ...(bootstrap.feeds[0] as Bootstrap["feeds"][number]), id: "2", folder_id: "1", title: "Other Feed" },
  ],
});

const ARTICLE = {
  content_html: '<p>Read the Article carefully. See <a href="https://example.com/article">the article link</a> and <code>article()</code>.</p>',
  author: "Ada Article",
};

/** The first list row, once it is drawn (its text is split by marks, so it is found by its id). */
const firstRow = () => waitFor(() => expect(document.querySelector('[data-item-id="1001"]')).not.toBeNull()).then(() => document.querySelector('[data-item-id="1001"]') as HTMLElement);

let qc: QueryClient;
function routes(highlights: Highlight[], extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(boot(highlights)),
    "GET /api/items": () => json(pageOf([card(1), card(2, { feed_id: "2", title: "Article number 2" })])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/items/1001": () => json(detail(1, ARTICLE)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true, ...ARTICLE }) }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  qc = makeQueryClient({ retry: false });
  return render(<App client={qc} />);
}

beforeEach(() => {
  clearToasts();
  clearListMemory();
  rowMenuStore.set(null);
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  highlightStore.set({ groups: [], feeds: new Map() });
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => vi.unstubAllGlobals());

const marks = (el: ParentNode) => Array.from(el.querySelectorAll("mark.kp-hl")).map((m) => m.textContent);

describe("highlights in lists", () => {
  it("marks the words in a title and an excerpt, and nothing changes without a rule", async () => {
    routes([hl(["article", "excerpt"])]);
    go("/l/unread");
    const row = (await screen.findByText("number 1", { exact: false })).closest("[data-item-id]") as HTMLElement;
    await waitFor(() => expect(marks(row)).toEqual(["Article", "Excerpt", "article"]));
    // The link's accessible name is the plain text, whatever is marked inside it.
    expect(within(row).getByRole("link")).toHaveAccessibleName(/Article number 1/);
  });

  it("only applies a feed's rule to that feed", async () => {
    routes([hl(["number"], { scope: "feed", feed_id: "2" })]);
    go("/l/unread");
    const one = await firstRow();
    const two = document.querySelector('[data-item-id="1002"]') as HTMLElement;
    await waitFor(() => expect(marks(two)).toEqual(["number"]));
    expect(marks(one)).toEqual([]);
  });

  it("a rule for a folder reaches the feeds in it", async () => {
    routes([hl(["number"], { scope: "folder", folder_id: "1" })]);
    go("/l/unread");
    const first = await firstRow();
    await waitFor(() => expect(marks(first)).toEqual(["number"]));
    expect(marks(document.querySelector('[data-item-id="1002"]') as HTMLElement)).toEqual(["number"]);
  });

  it("does not look at fields the rule does not name", async () => {
    routes([hl(["article"], { fields: ["title"] })]);
    go("/l/unread");
    const row = await firstRow();
    await waitFor(() => expect(marks(row)).toEqual(["Article"])); // the excerpt's "article" is left alone
  });

  it("the reading menu's Highlight keywords turns it off and on, and the choice is kept on the device", async () => {
    routes([hl(["article"])]);
    go("/l/unread");
    const row = await firstRow();
    await waitFor(() => expect(marks(row).length).toBeGreaterThan(0));
    const user = userEvent.setup();
    await user.click(screen.getAllByRole("button", { name: "Reading appearance" })[0] as HTMLElement);
    const sw = await screen.findByRole("switch", { name: /Highlight keywords/ });
    expect(sw).toBeChecked();
    await user.click(sw);
    await waitFor(() => expect(marks(row)).toEqual([]));
    await user.click(screen.getByRole("switch", { name: /Highlight keywords/ }));
    await waitFor(() => expect(marks(row).length).toBeGreaterThan(0));
  });

  it("follows a changed rule: filters.changed refetches the bootstrap and the marks update", async () => {
    let rules = [hl(["article"])];
    routes([], { "GET /api/bootstrap": () => json(boot(rules)) });
    go("/l/unread");
    const row = await firstRow();
    await waitFor(() => expect(marks(row).length).toBeGreaterThan(0));
    rules = [hl(["excerpt"])];
    act(() => handleServerEvent(qc, { type: "filters.changed", data: {} }));
    await waitFor(() => expect(marks(row)).toEqual(["Excerpt"]));
  });
});

describe("highlights in the article", () => {
  it("marks the title, the author and the body after sanitizing, but not inside links or code", async () => {
    routes([hl(["article"])]);
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    await waitFor(() => expect(marks(body)).toEqual(["Article"]));
    expect(body.querySelector("a mark, code mark")).toBeNull();
    expect(body.querySelector("a")?.textContent).toBe("the article link");
    const h1 = screen.getByRole("heading", { level: 1 });
    expect(marks(h1)).toEqual(["Article"]);
    expect(marks(h1.closest("header") as HTMLElement)).toEqual(["Article", "Article"]); // title and author
  });

  it("turning the setting off takes the marks out of the body again", async () => {
    routes([hl(["article"])]);
    go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    await waitFor(() => expect(marks(body).length).toBe(1));
    const html = body.innerHTML;
    act(() => updateDevicePrefs({ highlightKeywords: false }));
    await waitFor(() => expect(marks(body)).toEqual([]));
    expect(body.innerHTML).toBe(html.replace(/<mark class="kp-hl">Article<\/mark>/, "Article"));
    act(() => updateDevicePrefs({ highlightKeywords: true }));
    await waitFor(() => expect(marks(body)).toEqual(["Article"]));
  });
});
