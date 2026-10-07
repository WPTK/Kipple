import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { handleServerEvent, initialLive, liveStore } from "@/api/events";
import { filterStatus, type Filter } from "@/api/filters";
import type { Bootstrap } from "@/api/types";
import { devicePrefsStore } from "@/lib/devicePrefs";
import { closeFilterEditor } from "@/lib/similar";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { QueryClient } from "@tanstack/react-query";

class NoES {
  addEventListener() {}
  close() {}
}

const filter = (n: number, over: Partial<Filter> = {}): Filter => ({
  id: String(n),
  name: `Rule ${n}`,
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
  position: n,
  hits: 0,
  last_hit_at: null,
  created_at: 1,
  updated_at: 1,
  muted_items: 0,
  ...over,
});

const settingsBody = { settings: [], values: {} };
let qc: QueryClient;

function routes(extra: Parameters<typeof mockFetch>[0] = {}, boot: Bootstrap = bootstrap) {
  return mockFetch({
    "GET /api/bootstrap": () => json(boot),
    "GET /api/items": () => json(pageOf([card(1), card(2)])),
    "GET /api/settings": () => json(settingsBody),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  qc = makeQueryClient({ retry: false });
  return render(<App client={qc} />);
}

const body = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body));

beforeEach(() => {
  clearToasts();
  authStore.set("unknown");
  liveStore.set(initialLive);
  closeFilterEditor();
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  closeFilterEditor();
  vi.unstubAllGlobals();
});

const previewOf = (matches: number, over: Record<string, unknown> = {}) => ({
  matches,
  scanned: 500,
  truncated: false,
  sample: Array.from({ length: Math.min(matches, 20) }, (_, i) => card(i + 1, { title: `Sample article ${i + 1}` })),
  warnings: [],
  ...over,
});

describe("Settings > Filters list", () => {
  it("shows each rule with its scope, action, hits and last hit, and an on/off switch that PATCHes", async () => {
    const rules = [
      filter(1, { name: "No giveaways", hits: 12, last_hit_at: Math.floor(Date.now() / 1000) - 3 * 86400, muted_items: 7 }),
      filter(2, { name: "Star Go", scope: "feed", feed_id: "1", action: "star", terms: ["golang", "rust"], enabled: false }),
    ];
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: rules }),
      "PATCH /api/filters/2": (_u, init) => {
        (rules[1] as Filter).enabled = JSON.parse(String(init?.body)).enabled;
        return json({ filter: rules[1] });
      },
    });
    const { container } = go("/settings/filters");
    const list = await screen.findByRole("list", { name: "Your filters" });
    const first = within(list).getAllByRole("listitem")[0] as HTMLElement;
    expect(within(first).getByText("No giveaways")).toBeInTheDocument();
    expect(first).toHaveTextContent("Mute");
    expect(first).toHaveTextContent("Everywhere");
    expect(first).toHaveTextContent("Matched 12 articles, last 3 days ago");
    expect(first).toHaveTextContent("7 muted now");
    const second = within(list).getAllByRole("listitem")[1] as HTMLElement;
    expect(second).toHaveTextContent("Star");
    expect(second).toHaveTextContent("Feed: Example Feed");
    expect(second).toHaveTextContent("Hasn't matched anything yet");
    const sw = within(second).getByRole("switch", { name: /Star Go/ });
    expect(sw).not.toBeChecked();
    await userEvent.setup().click(sw);
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH" && c.url.pathname === "/api/filters/2")).toBe(true));
    expect(body(calls.find((c) => c.method === "PATCH" && c.url.pathname === "/api/filters/2") as never)).toEqual({ enabled: true });
    await waitFor(() => expect(within(list).getAllByRole("switch")[1]).toBeChecked());
    expect(await axe(container)).toHaveNoViolations();
  });

  it("a highlight rule never claims it has not matched: its matches are not counted (UAT Suite 2, TC-R6)", async () => {
    routes({
      "GET /api/filters": () =>
        json({
          filters: [
            filter(3, { name: "Hl lemur", action: "highlight", terms: ["lemur"], hits: 0 }),
            filter(4, { name: "Hl off", action: "highlight", terms: ["otter"], hits: 0, enabled: false }),
          ],
        }),
    });
    go("/settings/filters");
    const list = await screen.findByRole("list", { name: "Your filters" });
    const [on, off] = within(list).getAllByRole("listitem") as [HTMLElement, HTMLElement];
    expect(on).toHaveTextContent("Marks matching words as you read");
    expect(on).not.toHaveTextContent("Hasn't matched anything yet");
    expect(off).not.toHaveTextContent("Marks matching words as you read"); // switched off: it marks nothing
    expect(off).not.toHaveTextContent("Hasn't matched anything yet");
    expect(off.querySelectorAll("p:empty")).toHaveLength(0);
  });

  it("filterStatus: counts for rules that act, a description for highlights, nothing for a switched-off highlight", () => {
    const base = { enabled: true, disabled_reason: null, hits: 0, last_hit_at: null, muted_items: 0 };
    expect(filterStatus({ ...base, action: "mute" }, true)).toBe("Hasn't matched anything yet");
    expect(filterStatus({ ...base, action: "mute", hits: 1, last_hit_at: Math.floor(Date.now() / 1000), muted_items: 1 }, true)).toMatch(/^Matched 1 article, last .* · 1 muted now$/);
    expect(filterStatus({ ...base, action: "highlight" }, true)).toBe("Marks matching words as you read");
    expect(filterStatus({ ...base, action: "highlight" }, false)).toMatch(/^Highlighting is off on this device/);
    expect(filterStatus({ ...base, action: "highlight", enabled: false }, true)).toBe("");
    expect(filterStatus({ ...base, action: "highlight", disabled_reason: "bad pattern" }, true)).toBe("");
  });

  it("a highlight rule says so when this device has highlighting turned off", async () => {
    const before = devicePrefsStore.get();
    devicePrefsStore.set({ ...before, highlightKeywords: false });
    try {
      routes({ "GET /api/filters": () => json({ filters: [filter(3, { name: "Hl lemur", action: "highlight", terms: ["lemur"], hits: 0 })] }) });
      go("/settings/filters");
      const list = await screen.findByRole("list", { name: "Your filters" });
      const row = within(list).getAllByRole("listitem")[0] as HTMLElement;
      expect(row).toHaveTextContent("Highlighting is off on this device");
      expect(row).not.toHaveTextContent("Marks matching words as you read");
    } finally {
      devicePrefsStore.set(before);
    }
  });

  it("names what each rule does in words, separated from its scope and parts", async () => {
    routes({
      "GET /api/filters": () =>
        json({
          filters: [
            filter(5, { scope: "feed", feed_id: "1", invert: true }),
            filter(6, { action: "star", invert: true }),
            filter(7, { action: "mark_read", fields: ["title", "author"] }),
          ],
        }),
    });
    go("/settings/filters");
    const list = await screen.findByRole("list", { name: "Your filters" });
    expect(within(list).getByText("Only show matching · Feed: Example Feed · Words in title")).toBeInTheDocument();
    expect(within(list).getByText("Star when it does not match · Everywhere · Words in title")).toBeInTheDocument();
    expect(within(list).getByText("Mark as read · Everywhere · Words in title, author")).toBeInTheDocument();
  });

  it("says so when there are none", async () => {
    routes({ "GET /api/filters": () => json({ filters: [] }) });
    go("/settings/filters");
    expect(await screen.findByText(/No filters yet/)).toBeInTheDocument();
  });
});

describe("filter editor", () => {
  async function openNew() {
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New filter" }));
    return { user, dialog: await screen.findByRole("dialog", { name: "New filter" }) };
  }

  it("builds a rule with chips, fields, options (with the inverted labels) and actions, and previews it live", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [] }),
      "POST /api/filters/preview": () =>
        json(previewOf(25, { warnings: [{ code: "category_new_items_only", message: "Articles without stored categories are skipped by category rules." }] })),
      "POST /api/filters": () => json({ filter: filter(9), applied: null }, 201),
    });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);

    // Terms are chips: added with Enter, removable, counted against the API limit.
    await user.type(w.getByLabelText("Words or phrases"), "giveaway{Enter}");
    await user.type(w.getByLabelText("Words or phrases"), "sponsored post{Enter}");
    const chips = w.getByRole("list", { name: "Terms in this filter" });
    expect(within(chips).getAllByRole("listitem")).toHaveLength(2);
    expect(w.getByText(/2 of 50 used/)).toBeInTheDocument();
    expect(w.getByRole("button", { name: "Remove sponsored post" })).toHaveClass("hit-row"); // 44 px on coarse pointers
    await user.click(w.getByRole("button", { name: "Remove sponsored post" }));
    expect(within(chips).getAllByRole("listitem")).toHaveLength(1);

    // Fields have friendly labels; the option labels are the inverse of the API flags.
    await user.click(w.getByRole("checkbox", { name: "Article text" }));
    await user.click(w.getByRole("checkbox", { name: "Author" }));
    const caps = w.getByRole("checkbox", { name: /Ignore capitalization/ });
    expect(caps).toBeChecked(); // case_sensitive is false
    await user.click(caps);
    expect(caps).not.toBeChecked();
    expect(w.getByRole("checkbox", { name: /Ignore accents/ })).toBeChecked(); // fold_diacritics true
    expect(w.getByRole("checkbox", { name: /Match whole words only/ })).toBeChecked();
    // For Mute the inversion is the "Only show matching" mode, not the checkbox (one control for one stored state).
    expect(w.getByRole("checkbox", { name: /Act when it does NOT match/ })).toBeDisabled();
    await user.click(w.getByRole("radio", { name: /Only show matching/ }));
    expect(w.getByRole("checkbox", { name: /Act when it does NOT match/ })).not.toBeChecked();

    // The actions explain themselves.
    expect(w.getByText(/Hides matching articles from Unread, All and search/)).toBeInTheDocument();
    expect(w.getByText(/Draws the matching words in a colored mark/)).toBeInTheDocument();

    // The preview runs 600 ms after the last edit and shows count, warning and sample.
    const panel = within(await w.findByRole("region", { name: "Preview" }));
    const status = await panel.findByText("25 articles would be muted");
    expect(status.closest("[role=status]")).toHaveAttribute("aria-live", "polite");
    expect(w.getByText(/Articles without stored categories are skipped/)).toBeInTheDocument();
    const sample = w.getByRole("list", { name: "Sample of matching articles" });
    expect(within(sample).getAllByRole("listitem")).toHaveLength(20);
    const previews = () => calls.filter((c) => c.url.pathname === "/api/filters/preview");
    await waitFor(() =>
      expect(body(previews().at(-1) as never)).toMatchObject({
        include_read: false,
        filter: { terms: ["giveaway"], fields: ["title", "content", "author"], case_sensitive: true, invert: true, action: "mute", scope: "global" },
      }),
    );
    // Fewer requests than edits: the debounce collapses them.
    expect(previews().length).toBeLessThan(8);

    // Including read articles asks again with include_read.
    await user.click(w.getByRole("switch", { name: /Include already-read articles/ }));
    await waitFor(() => expect(body(calls.filter((c) => c.url.pathname === "/api/filters/preview").at(-1) as never).include_read).toBe(true));

    await user.click(w.getByRole("button", { name: "Save filter" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/filters")).toBe(true));
    const create = body(calls.find((c) => c.method === "POST" && c.url.pathname === "/api/filters") as never);
    expect(create).toMatchObject({ terms: ["giveaway"], action: "mute", invert: true, case_sensitive: true, whole_word: true, fold_diacritics: true, scope: "global" });
    expect(create.name).toBe("Only show: giveaway"); // an unnamed rule is given one
    expect(create.apply_existing).toBeUndefined();
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "New filter" })).toBeNull());
  }, 20000);

  it("the invert option of Mark as read survives a pass through Mute and Only show matching", async () => {
    routes({ "GET /api/filters": () => json({ filters: [] }), "POST /api/filters/preview": () => json(previewOf(0)) });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    const invert = () => w.getByRole("checkbox", { name: /Act when it does NOT match/ });
    await user.click(w.getByRole("radio", { name: /Mark as read/ }));
    await user.click(invert());
    expect(invert()).toBeChecked();
    await user.click(w.getByRole("radio", { name: /^Mute/ }));
    expect(invert()).toBeDisabled();
    await user.click(w.getByRole("radio", { name: /Only show matching/ }));
    expect(w.getByText(/for several topics put all the words in one rule/)).toBeInTheDocument();
    await user.click(w.getByRole("radio", { name: /Mark as read/ }));
    expect(invert()).toBeChecked();
    expect(invert()).toBeEnabled();
  });

  it("picks a folder or a feed for the scope", async () => {
    const boot: Bootstrap = { ...bootstrap, folders: [{ id: "1", name: "News", position: 0, is_default: true, unread: 0 }, { id: "2", name: "Tech", position: 1, is_default: false, unread: 0 }] };
    const { calls } = routes({ "GET /api/filters": () => json({ filters: [] }), "POST /api/filters": () => json({ filter: filter(3), applied: null }, 201), "POST /api/filters/preview": () => json(previewOf(0)) }, boot);
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    await user.click(w.getByRole("radio", { name: /A folder/ }));
    await user.selectOptions(w.getByLabelText("Folder"), "2");
    await user.type(w.getByLabelText("Words or phrases"), "x{Enter}");
    await user.click(w.getByRole("button", { name: "Save filter" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/filters")).toBe(true));
    expect(body(calls.find((c) => c.method === "POST" && c.url.pathname === "/api/filters") as never)).toMatchObject({ scope: "folder", folder_id: "2", feed_id: null });
  });

  it("regular expressions: only patterns, no whole-word or accent options, highlight not offered a word list", async () => {
    routes({ "GET /api/filters": () => json({ filters: [] }) });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    await user.click(w.getByRole("radio", { name: /Regular expression/ }));
    expect(w.getByLabelText("Patterns")).toBeInTheDocument();
    expect(w.queryByRole("checkbox", { name: /Match whole words only/ })).toBeNull();
    expect(w.queryByRole("checkbox", { name: /Ignore accents/ })).toBeNull();
    expect(w.getByText(/0 of 5 used/)).toBeInTheDocument();
  });

  it("enforces the API's limits before sending", async () => {
    routes({ "GET /api/filters": () => json({ filters: [] }) });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    const input = w.getByLabelText("Words or phrases");
    await user.click(input);
    await user.paste("x".repeat(101));
    await user.keyboard("{Enter}");
    expect(await w.findByText(/at most 100 characters/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("list", { name: "Terms in this filter" })).toBeNull();
  });

  it("shows a server validation error next to its field", async () => {
    routes({
      "GET /api/filters": () => json({ filters: [] }),
      "POST /api/filters/preview": () => json(previewOf(0)),
      "POST /api/filters": () => json({ error: "bad_filter", field: "terms[0]", message: "must not be empty after trimming" }, 400),
    });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    await user.type(w.getByLabelText("Words or phrases"), "abc{Enter}");
    await user.click(w.getByRole("button", { name: "Save filter" }));
    const alert = await w.findByText("must not be empty after trimming");
    expect(alert).toHaveAttribute("role", "alert");
    expect(alert.closest("div")).toContainElement(w.getByLabelText("Words or phrases"));
    expect(screen.getByRole("dialog", { name: "New filter" })).toBeInTheDocument(); // stays open
  });

  it("apply to existing: sends apply_existing and follows the run through the events, with a progress bar", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [] }),
      "POST /api/filters/preview": () => json(previewOf(3)),
      "POST /api/filters": () => json({ filter: filter(5), applied: { id: "77", kind: "filter_apply", filter_id: "5", done: 0, total: 400, changed: 0, errors: 0 } }, 201),
    });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    await user.type(w.getByLabelText("Words or phrases"), "giveaway{Enter}");
    await within(w.getByRole("region", { name: "Preview" })).findByText("3 articles would be muted");
    await user.click(w.getByRole("checkbox", { name: /Apply to existing articles/ }));
    await user.click(w.getByRole("button", { name: "Save filter" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/filters")).toBe(true));
    expect(body(calls.find((c) => c.method === "POST" && c.url.pathname === "/api/filters") as never).apply_existing).toEqual({ include_read: false });

    act(() => handleServerEvent(qc, { type: "run.start", data: { run_id: "77", kind: "filter_apply", total: 400, filter_id: "5" } }));
    act(() => handleServerEvent(qc, { type: "run.progress", data: { run_id: "77", done: 100, total: 400, new_items: 0, errors: 0, changed: 12 } }));
    const bar = await screen.findByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "100");
    expect(bar).toHaveAttribute("aria-valuemax", "400");
    expect(screen.getByTestId("apply-progress")).toHaveTextContent("100 of 400 articles checked, 12 changed");
    // The finish is announced once, and the bar goes away.
    act(() => handleServerEvent(qc, { type: "run.done", data: { run_id: "77", kind: "filter_apply", filter_id: "5", new_items: 0, errors: 0, changed: 31, scanned: 400 } }));
    await waitFor(() => expect(screen.queryByRole("progressbar")).toBeNull());
    expect(screen.getByTestId("live-region")).toHaveTextContent("Filter applied: 31 articles changed");
  }, 20000);

  it("apply to existing is not offered for Highlight, and a disabled rule cannot be applied", async () => {
    routes({ "GET /api/filters": () => json({ filters: [] }) });
    go("/settings/filters");
    const { user, dialog } = await openNew();
    const w = within(dialog);
    expect(w.getByRole("checkbox", { name: /Apply to existing articles/ })).toBeInTheDocument();
    await user.click(w.getByRole("radio", { name: /^Highlight/ }));
    expect(w.queryByRole("checkbox", { name: /Apply to existing articles/ })).toBeNull();
    await user.click(w.getByRole("radio", { name: /^Mute/ }));
    await user.click(w.getByRole("switch", { name: /Turn this filter on/ }));
    expect(w.queryByRole("checkbox", { name: /Apply to existing articles/ })).toBeNull();
    expect(w.getByText(/turned off does nothing/)).toBeInTheDocument();
  });
});

describe("deleting a filter", () => {
  const withMuted = (over: Partial<Filter> = {}) => filter(4, { name: "No giveaways", muted_items: 7, ...over });

  it("offers to restore the muted articles: keep them read by default, unread only when chosen", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [withMuted()] }),
      "DELETE /api/filters/4": () => json({ changed: 7 }),
    });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete No giveaways" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete this filter?" });
    const w = within(dialog);
    expect(w.getByText(/This filter has muted 7 articles/)).toBeInTheDocument();
    expect(w.getByRole("radio", { name: /Restore them and keep them read/ })).toBeChecked();
    await user.click(w.getByRole("button", { name: "Delete filter" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(calls.find((c) => c.method === "DELETE")?.url.search).toBe("?unmute=read");
  });

  it("marks them unread only as an explicit choice", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [withMuted()] }),
      "DELETE /api/filters/4": () => json({ changed: 7 }),
    });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete No giveaways" }));
    const w = within(await screen.findByRole("dialog", { name: "Delete this filter?" }));
    await user.click(w.getByRole("radio", { name: /Restore them and mark them unread/ }));
    await user.click(w.getByRole("button", { name: "Delete filter" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(calls.find((c) => c.method === "DELETE")?.url.search).toBe("?unmute=unread");
  });

  it("repeats the delete while the server is still restoring, and says how many went back to unread", async () => {
    const answers = [
      { changed: 500, made_unread: 450, done: false },
      { changed: 200, made_unread: 150, done: true },
    ];
    let i = 0;
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [withMuted({ muted_items: 700 })] }),
      "DELETE /api/filters/4": () => json(answers[Math.min(i++, answers.length - 1)], i === 1 ? 202 : 200),
    });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete No giveaways" }));
    const w = within(await screen.findByRole("dialog", { name: "Delete this filter?" }));
    expect(w.getByText(/the ones you had already read stay read/)).toBeInTheDocument();
    await user.click(w.getByRole("radio", { name: /Restore them and mark them unread/ }));
    await user.click(w.getByRole("button", { name: "Delete filter" }));
    expect(await screen.findByText("Filter deleted. 700 articles restored, 600 of them marked unread (the rest you had already read).")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(2);
  });

  it("repeats a round that restored nothing but is not done, and gives up after three idle rounds", async () => {
    const { deleteUntilDone } = await import("./FiltersSection");
    const answers = [
      { changed: 0, made_unread: 0, done: false },
      { changed: 0, made_unread: 0, done: true },
    ];
    let i = 0;
    const { calls } = routes({ "DELETE /api/filters/4": () => json(answers[Math.min(i++, answers.length - 1)]) });
    expect(await deleteUntilDone("4", "read")).toEqual({ restored: 0, madeUnread: 0 });
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(2);

    const stuck = routes({ "DELETE /api/filters/5": () => json({ changed: 0, made_unread: 0, done: false }, 202) });
    await deleteUntilDone("5", "read");
    expect(stuck.calls.filter((c) => c.method === "DELETE")).toHaveLength(3);
  });

  it("the toast only claims unread for what the server made unread", async () => {
    const { deletedMessage } = await import("./FiltersSection");
    expect(deletedMessage(0, 0, "read")).toBe("Filter deleted");
    expect(deletedMessage(7, 0, "read")).toBe("Filter deleted. 7 articles restored.");
    expect(deletedMessage(7, 7, "unread")).toBe("Filter deleted. 7 articles restored, all marked unread.");
    expect(deletedMessage(1, 1, "unread")).toBe("Filter deleted. 1 article restored as unread.");
    expect(deletedMessage(7, 0, "unread")).toBe("Filter deleted. 7 articles restored; none were unread when muted, so they stay read.");
    expect(deletedMessage(7, 3, "unread")).toBe("Filter deleted. 7 articles restored, 3 of them marked unread (the rest you had already read).");
  });

  it("leaving them muted sends keep, and a filter that muted nothing has no choice to make", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [withMuted(), filter(5, { name: "Plain", muted_items: 0 })] }),
      "DELETE /api/filters/4": () => json({ changed: 0 }),
      "DELETE /api/filters/5": () => json({ changed: 0 }),
    });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete No giveaways" }));
    let w = within(await screen.findByRole("dialog", { name: "Delete this filter?" }));
    await user.click(w.getByRole("radio", { name: /Leave them muted/ }));
    await user.click(w.getByRole("button", { name: "Delete filter" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(1));
    expect(calls.find((c) => c.method === "DELETE")?.url.search).toBe("?unmute=keep");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(await screen.findByRole("button", { name: "Delete Plain" }));
    w = within(await screen.findByRole("dialog", { name: "Delete this filter?" }));
    expect(w.queryByRole("radio")).toBeNull();
  });
});

describe("background runs in the app shell (review findings 8 and 12)", () => {
  it("does not fetch the filters until a filter apply runs, and shows auto-read quietly", async () => {
    const { calls } = routes({ "GET /api/filters": () => json({ filters: [filter(5)] }) });
    go("/");
    await screen.findByRole("main").catch(() => undefined);
    await new Promise((r) => setTimeout(r, 200));
    expect(calls.some((c) => c.url.pathname === "/api/filters")).toBe(false);
    act(() => handleServerEvent(qc, { type: "run.start", data: { run_id: "40", kind: "auto_read", total: 10 } }));
    expect(await screen.findByTestId("auto-read-status")).toHaveTextContent("Marking old articles as read");
    expect(screen.queryByTestId("apply-progress")).toBeNull();
    expect(calls.some((c) => c.url.pathname === "/api/filters")).toBe(false);
    act(() => handleServerEvent(qc, { type: "run.start", data: { run_id: "41", kind: "filter_apply", total: 10, filter_id: "5" } }));
    expect(await screen.findByTestId("apply-progress")).toBeInTheDocument();
    await waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/filters")).toBe(true));
  }, 20000);
});

describe("review findings 5, 6 and 9", () => {
  it("autoName never exceeds the server's 200 bytes, even for CJK terms (finding 5)", async () => {
    const { autoName } = await import("./FilterEditor");
    const cjk = "新闻联播今日要闻".repeat(9);
    const name = autoName({ action: "mute", terms: [cjk, cjk, cjk] });
    expect(new TextEncoder().encode(name).length).toBeLessThanOrEqual(200);
    expect([...name].length).toBeLessThanOrEqual(80);
    expect(name.startsWith("Mute: ")).toBe(true);
  });

  it("the name field checks bytes, not characters, and blocks Save (finding 5)", async () => {
    routes({ "GET /api/filters": () => json({ filters: [] }), "POST /api/filters/preview": () => json(previewOf(0)) });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New filter" }));
    const w = within(await screen.findByRole("dialog", { name: "New filter" }));
    await user.type(w.getByLabelText("Words or phrases"), "giveaway{Enter}");
    // 70 CJK characters are 210 bytes: under 200 characters, over 200 bytes.
    await user.click(w.getByLabelText("Name"));
    await user.paste("中".repeat(70));
    expect(await w.findByText(/at most 200 bytes/)).toBeInTheDocument();
    expect(w.getByRole("button", { name: "Save filter" })).toBeDisabled();
  });

  it("deleting a rule always sends the chosen mode, even when the row's count was stale (finding 6)", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [filter(4, { name: "Stale", muted_items: 0 })] }),
      "DELETE /api/filters/4": () => json({ changed: 2 }),
    });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete Stale" }));
    const w = within(await screen.findByRole("dialog", { name: "Delete this filter?" }));
    await user.click(w.getByRole("button", { name: "Delete filter" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(calls.find((c) => c.method === "DELETE")?.url.search).toBe("?unmute=read");
  });

  it("the dialog looks at the count again when it opens (finding 6)", async () => {
    let n = 0;
    routes({ "GET /api/filters": () => json({ filters: [filter(4, { name: "Stale", muted_items: n })] }) });
    go("/settings/filters");
    const user = userEvent.setup();
    const trash = await screen.findByRole("button", { name: "Delete Stale" });
    n = 3; // three articles were muted since the list loaded
    await user.click(trash);
    const w = within(await screen.findByRole("dialog", { name: "Delete this filter?" }));
    expect(await w.findByText(/has muted 3 articles/)).toBeInTheDocument();
  });

  it("Apply waits for the preview of the current rule, and a 409 from apply after a saved edit is explained (finding 9)", async () => {
    const { calls } = routes({
      "GET /api/filters": () => json({ filters: [filter(5, { name: "Editable" })] }),
      "POST /api/filters/preview": () => json(previewOf(3)),
      "PATCH /api/filters/5": () => json({ filter: filter(5) }),
      "POST /api/filters/5/apply": () => json({ error: "busy" }, 409),
    });
    go("/settings/filters");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit Editable" }));
    const w = within(await screen.findByRole("dialog", { name: "Edit filter" }));
    await within(w.getByRole("region", { name: "Preview" })).findByText("3 articles would be muted");
    expect(w.getByRole("checkbox", { name: /Apply to existing articles/ })).not.toBeDisabled();
    await user.type(w.getByLabelText("Words or phrases"), "extra{Enter}");
    expect(w.getByRole("checkbox", { name: /Apply to existing articles/ })).toBeDisabled(); // the count on screen is for the old rule
    await waitFor(() => expect(w.getByRole("checkbox", { name: /Apply to existing articles/ })).not.toBeDisabled());
    await user.click(w.getByRole("checkbox", { name: /Apply to existing articles/ }));
    await user.click(w.getByRole("button", { name: "Save filter" }));
    expect(await w.findByText(/Saved\. Another apply is running/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(true);
    expect(screen.getByRole("dialog", { name: "Edit filter" })).toBeInTheDocument();
  }, 20000);
});

