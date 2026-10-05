import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { HealthResponse, SettingMeta } from "@/api/admin";
import { DEFAULT_PREFS, applyPrefs, parsePrefs, prefsStore } from "@/lib/prefs";
import { FONTS } from "@/lib/fonts";
import { devicePrefsStore, parseDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { healthFilter, healthSort } from "./HealthScreen";
import * as toasts from "@/shell/toasts";
import { diffForm, grantNotice, grantsDropped, savedAddressMessage } from "./feeds/FeedEditor";

class NoES {
  addEventListener() {}
  close() {}
}

const meta = (m: Partial<SettingMeta> & Pick<SettingMeta, "key" | "kind">): SettingMeta => ({
  value: null,
  default: null,
  label: m.key,
  description: `About ${m.key}`,
  group: "reading",
  surface: "settings",
  ...m,
});

const SETTINGS: SettingMeta[] = [
  meta({ key: "ui.mark_read_on_scroll", kind: "bool", scope: "device", label: "Mark articles read as I scroll", value: false, default: false }),
  meta({ key: "ui.theme_day", kind: "enum", scope: "device", label: "Day theme", value: "paper", default: "paper", options: [{ value: "paper", label: "Paper" }] }),
  meta({ key: "ui.theme_night", kind: "enum", scope: "device", label: "Night theme", value: "midnight", default: "midnight", options: [{ value: "midnight", label: "Midnight" }] }),
  meta({
    key: "ui.list_density",
    kind: "enum",
    scope: "device",
    label: "List spacing",
    value: "standard",
    default: "standard",
    options: ["dense", "snug", "standard", "relaxed", "airy"].map((v) => ({ value: v, label: v })),
  }),
  meta({ key: "links.strip_tracking", kind: "bool", label: "Remove tracking from links", value: true, default: true }),
  meta({ key: "refresh.interval_minutes", kind: "int", group: "sync", label: "How often to check feeds", value: 30, default: 30, min: 5, max: 1440, step: 5, unit: "minutes" }),
  meta({
    key: "fetch.user_agent_mode",
    kind: "enum",
    group: "sync",
    label: "Browser identity for feeds",
    value: "on_failure",
    default: "on_failure",
    options: [
      { value: "on_failure", label: "Only when a feed refuses to load" },
      { value: "default", label: "Always identify as Kipple" },
      { value: "always", label: "Always look like a browser" },
    ],
  }),
  meta({
    key: "retention.default",
    kind: "enum",
    group: "library",
    label: "Articles to keep per feed",
    value: 500,
    default: 500,
    options: [50, 100, 250, 500, 1000, 0].map((n) => ({ value: n, label: n === 0 ? "Unlimited" : String(n) })),
  }),
  meta({ key: "tz", kind: "text", group: "account", label: "Time zone", value: "America/New_York", default: "America/New_York" }),
  meta({ key: "greader.icon_urls", kind: "bool", group: "advanced", label: "Send feed icons to sync apps", value: true, default: true }),
];

const settingsBody = (list = SETTINGS) => ({ settings: list, values: Object.fromEntries(list.map((s) => [s.key, s.value])) });

const feedDetail = (over: Record<string, unknown> = {}) => ({
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
  ...over,
});

function base(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json(settingsBody()),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

const body = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body));

describe("Settings renderer", () => {
  // One test per group, each opened by its own URL: moving between groups is covered in settingsNav.test.tsx.
  it("draws the reading settings under Appearance & Reading", async () => {
    base();
    const { container } = go("/settings/appearance");
    // Settings is a lazy chunk: under a busy full run its first import can take longer than findBy's default.
    await screen.findByRole("heading", { level: 1, name: "Appearance & Reading" }, { timeout: 5000 });
    for (const h of ["Appearance", "Accessibility", "Lists and reading", "Keyboard"]) expect(screen.getByRole("heading", { level: 2, name: h })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { level: 2, name: "Reading" })).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: /Remove tracking from links/ })).toBeChecked();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("does not list a device-scoped key a second time: this device's own control already shows it", async () => {
    base();
    go("/settings/appearance");
    await screen.findByRole("heading", { level: 1, name: "Appearance & Reading" }, { timeout: 5000 });
    expect(await screen.findByRole("switch", { name: /Remove tracking from links/ })).toBeInTheDocument(); // the server rows have loaded
    // A generic row would show the metadata description (the fixtures describe each key as "About <key>").
    for (const key of ["ui.theme_day", "ui.theme_night", "ui.list_density"]) expect(screen.queryByText("About " + key), key).toBeNull();
    expect(screen.getByText("About links.strip_tracking")).toBeInTheDocument(); // a global key in the same group still is drawn
  });

  it("draws every kind from the metadata, in the group each server group belongs to", async () => {
    base();
    const { container } = go("/settings/sync");
    await screen.findByRole("heading", { level: 2, name: "Sync" }, { timeout: 5000 });
    expect(screen.getByRole("heading", { level: 2, name: "Library" })).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: "How often to check feeds" })).toHaveValue(30);
    expect(screen.getByRole("radio", { name: "Always identify as Kipple" })).toBeInTheDocument(); // enum with 3 options: segmented
    expect(screen.getByRole("combobox", { name: "Articles to keep per feed" })).toHaveValue("500"); // enum with 6: select
    expect(screen.queryByRole("heading", { name: "Images" })).toBeNull(); // a server group with nothing in it is left out
    expect(await axe(container)).toHaveNoViolations();
    cleanup();

    go("/settings/account");
    expect(await screen.findByRole("heading", { level: 2, name: "Account" })).toBeInTheDocument();
    expect(await screen.findByRole("textbox", { name: "Time zone" })).toHaveValue("America/New_York");
    cleanup();

    // Advanced has a page of its own now, so its settings show without a second click.
    go("/settings/advanced");
    expect(await screen.findByRole("switch", { name: /Send feed icons/ })).toBeInTheDocument();
  });

  it("patches optimistically, and puts a 400 message next to the control", async () => {
    let interval = 30;
    const { calls } = base({
      "PATCH /api/settings": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, unknown>;
        if (b["refresh.interval_minutes"] === 35) {
          return json({ error: "invalid_settings", message: "invalid settings: refresh.interval_minutes", keys: ["refresh.interval_minutes"], issues: [{ key: "refresh.interval_minutes", message: "must be an integer from 5 to 1440" }] }, 400);
        }
        if (b["links.strip_tracking"] === false) return json(settingsBody(SETTINGS.map((s) => (s.key === "links.strip_tracking" ? { ...s, value: false } : s))));
        if ("refresh.interval_minutes" in b) interval = Number(b["refresh.interval_minutes"]);
        return json(settingsBody());
      },
    });
    go("/settings/appearance");
    const user = userEvent.setup();
    const sw = await screen.findByRole("switch", { name: /Remove tracking from links/ });
    await user.click(sw);
    expect(sw).not.toBeChecked(); // optimistic
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH" && body(c)["links.strip_tracking"] === false)).toBe(true));
    // A non-default value offers a per-setting reset.
    const reset = await screen.findByRole("button", { name: "Reset Remove tracking from links to default" });
    await user.click(reset);
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH").some((c) => body(c)["links.strip_tracking"] === null)).toBe(true));

    // The refresh interval is in another group: back to the list, then into Sync & Feeds.
    await user.click(screen.getByRole("button", { name: "Back to Settings" }));
    await user.click(await screen.findByRole("link", { name: /^Sync & Feeds/ }));
    await user.click(await screen.findByRole("button", { name: "Increase How often to check feeds" }));
    const alert = await screen.findByText("must be an integer from 5 to 1440", {}, { timeout: 3000 });
    expect(alert).toHaveAttribute("role", "alert");
    expect(interval).toBe(30);
  });
});

describe("Accessibility section", () => {
  it("has the six controls plus large targets and titles-only, and they drive the document", async () => {
    base();
    go("/settings/appearance");
    const user = userEvent.setup();
    const section = await screen.findByRole("region", { name: "Accessibility" });
    const w = within(section);
    // One font choice only (the Aa menu): the Accessibility section points at it instead of adding a second one.
    expect(w.queryByRole("switch", { name: /Easy-to-read font/ })).toBeNull();
    expect(w.queryByRole("combobox", { name: /font/i })).toBeNull();
    expect(w.getByText(/Atkinson Hyperlegible Next/)).toBeInTheDocument();
    expect(w.getByRole("group", { name: "Text spacing" })).toBeInTheDocument();
    expect(w.getByText("Adds extra space between letters, words and lines")).toBeInTheDocument();
    expect(w.getByRole("group", { name: "Reduce motion" })).toBeInTheDocument();
    expect(await w.findByRole("switch", { name: /Mark articles read as I scroll/ })).not.toBeChecked();
    expect(w.getByRole("switch", { name: /Listen to articles/ })).toBeDisabled(); // jsdom has no speechSynthesis

    await user.click(w.getByRole("radio", { name: "More" }));
    await user.click(w.getByRole("radio", { name: "On" }));
    await user.click(w.getByRole("switch", { name: /Larger buttons/ }));
    applyPrefs(prefsStore.get());
    const root = document.documentElement;
    expect(root.dataset.spacing).toBe("roomy");
    expect(root.dataset.motion).toBe("on");
    expect(root.dataset.targets).toBe("large");
    await user.click(w.getByRole("switch", { name: /Titles only in lists/ }));
    expect(JSON.parse(localStorage.getItem("kipple.device.v1") ?? "{}").layout).toBe("headlines");
    // The layout to return to survives a reload (it is in the device prefs, not a module variable).
    updateDevicePrefs({ layout: "headlines", layoutBeforeTitlesOnly: "cards" });
    devicePrefsStore.set(parseDevicePrefs(localStorage.getItem("kipple.device.v1")));
    await user.click(w.getByRole("switch", { name: /Titles only in lists/ }));
    expect(devicePrefsStore.get().layout).toBe("cards");
  });
});

describe("Prefs and fonts", () => {
  it("lists every CLAUDE.md font, with Atkinson labelled Easy to read", () => {
    const labels = FONTS.map((f) => f.label).join("|");
    for (const n of ["Literata", "Charter", "Vollkorn", "Gentium Book Plus", "Source Serif 4", "Arvo", "Inter", "Manrope", "Source Sans 3", "JetBrains Mono", "Source Code Pro", "New York", "SF Pro", "SF Mono", "Georgia", "Menlo"]) {
      expect(labels).toContain(n);
    }
    expect(FONTS.find((f) => f.id === "easy")?.group).toBe("Easy to read");
  });

  it("parses stored prefs defensively", () => {
    expect(parsePrefs('{"font":"vollkorn","spacing":"roomy","motion":"on","rate":1.5,"largeTargets":true,"listen":true}')).toMatchObject({
      font: "vollkorn",
      spacing: "roomy",
      motion: "on",
      rate: 1.5,
      largeTargets: true,
      listen: true,
    });
    expect(parsePrefs('{"font":"nope","spacing":"x","motion":"y","rate":9}')).toMatchObject({ font: "default", spacing: "normal", motion: "system", rate: 1 });
  });
});

describe("Reading menu", () => {
  it("opens from the list header and changes font, size and density live", async () => {
    base();
    go("/l/unread");
    const user = userEvent.setup();
    await screen.findByText("Article number 1");
    await user.click(screen.getByRole("button", { name: "Reading appearance" }));
    const panel = await screen.findByRole("dialog", { name: "Reading appearance" });
    expect(within(panel).queryByRole("slider")).toBeNull(); // no sliders
    await user.selectOptions(within(panel).getByRole("combobox", { name: /Reading font/ }), "vollkorn");
    await user.click(within(panel).getByRole("radio", { name: "Larger" }));
    await user.click(within(panel).getAllByRole("radio", { name: "Airy" })[0] as HTMLElement);
    expect(prefsStore.get()).toMatchObject({ font: "vollkorn", textSize: 1.25, listDensity: "airy", readingDensity: "airy" });
  });
});

describe("Add feed", () => {
  it("walks the choose flow and shows the first-fetch result", async () => {
    const posts: unknown[] = [];
    base({
      "POST /api/feeds": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as { url: string };
        posts.push(b);
        if (b.url === "https://site.test")
          return json({ status: "choose", candidates: [{ url: "https://site.test/a.xml", title: "Posts", type: "rss" }, { url: "https://site.test/b.xml", title: "Comments", type: "rss" }] });
        return json({ status: "ok", feed: feedDetail({ title: "Comments" }), fetch: { outcome: "ok", new_items: 12 } });
      },
    });
    const { container } = go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Add feed" });
    expect(await axe(container)).toHaveNoViolations();
    await user.type(within(dlg).getByLabelText("Feed or website address"), "https://site.test");
    await user.click(within(dlg).getByRole("button", { name: "Add feed" }));
    await user.click(await within(dlg).findByRole("radio", { name: /Comments/ }));
    await user.click(within(dlg).getByRole("button", { name: "Add selected feed" }));
    await screen.findByRole("dialog", { name: "Feed added" });
    expect(screen.getByText(/12 articles/)).toBeInTheDocument();
    expect(posts).toHaveLength(2);
  });

  it("leaves a blank title to the feed and shows the name the feed gives itself", async () => {
    const posts: Record<string, unknown>[] = [];
    let added = false;
    const fresh = { ...bootstrap.feeds[0], id: "99", title: "Daily News", unread: 0 };
    base({
      // Once added, the feed list has the name the first fetch gave the feed (it finished after the add answered).
      "GET /api/bootstrap": () => json(added ? { ...bootstrap, feeds: [...bootstrap.feeds, fresh] } : bootstrap),
      "POST /api/feeds": (_u, init) => {
        posts.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        added = true;
        return json({ status: "ok", feed: feedDetail({ id: "99", title: "news.example" }), fetch: { pending: true } });
      },
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Add feed" });
    const title = within(dlg).getByLabelText("Title (optional)");
    expect(title).toHaveAttribute("placeholder", "Filled in from the feed");
    expect(title).toHaveAccessibleDescription(/filled in from the feed/);
    await user.type(title, "Mine");
    expect(title).toHaveAccessibleDescription(/instead of the feed's own/);
    await user.clear(title);
    // The limit is the server's: 200 characters, counted as code points (an emoji is one, not two UTF-16 units).
    await user.type(within(dlg).getByLabelText("Feed or website address"), "https://news.example/feed.xml");
    await user.click(title);
    await user.paste("📰".repeat(200));
    expect(title).toHaveValue("📰".repeat(200));
    expect(within(dlg).getByRole("button", { name: "Add feed" })).toBeEnabled();
    await user.paste("x");
    expect(title).toHaveAccessibleDescription(/at most 200 characters; this one has 201/);
    expect(within(dlg).getByRole("button", { name: "Add feed" })).toBeDisabled();
    await user.clear(title);
    await user.clear(within(dlg).getByLabelText("Feed or website address"));
    await user.type(within(dlg).getByLabelText("Feed or website address"), "https://news.example/feed.xml");
    await user.click(within(dlg).getByRole("button", { name: "Add feed" }));
    const done = await screen.findByRole("dialog", { name: "Feed added" });
    expect(posts).toEqual([{ url: "https://news.example/feed.xml" }]);
    expect(await within(done).findByText("Daily News")).toBeInTheDocument();
  });

  it("says a feed already exists, and explains a failed discovery", async () => {
    base({
      "POST /api/feeds": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as { url: string };
        return b.url.includes("dup") ? json({ status: "exists", feed: feedDetail() }) : json({ error: "no_feed", message: "no feed found" }, 422);
      },
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Add feed" });
    await user.type(within(dlg).getByLabelText("Feed or website address"), "https://nothing.test");
    await user.click(within(dlg).getByRole("button", { name: "Add feed" }));
    expect(await within(dlg).findByRole("alert")).toHaveTextContent("couldn't find a feed");
    await user.clear(within(dlg).getByLabelText("Feed or website address"));
    await user.type(within(dlg).getByLabelText("Feed or website address"), "https://dup.test");
    await user.click(within(dlg).getByRole("button", { name: "Add feed" }));
    await screen.findByRole("dialog", { name: "You already have this feed" });
  });

  it("shows the first-run state when there are no feeds", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [], counts: { unread: 0, starred: 0 } }),
      "GET /api/items": () => json(pageOf([])),
    });
    go("/l/unread");
    expect(await screen.findByRole("button", { name: "Add your first feed" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import OPML" })).toBeInTheDocument();
  });
});

describe("Feed editor", () => {
  it("changes the feed URL, sending only what changed", async () => {
    const { calls } = base({
      "GET /api/feeds/1": () => json(feedDetail()),
      "PATCH /api/feeds/1": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return b.url === "https://example.com/dup" ? json({ error: "url_exists", message: "used", feed_id: "9" }, 409) : json(feedDetail({ url: b.url ?? "https://example.com/feed.xml" }));
      },
    });
    const { container } = go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Edit Example Feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit feed" });
    const url = await within(dlg).findByLabelText("Feed address");
    expect(await axe(container)).toHaveNoViolations();
    await user.clear(url);
    await user.type(url, "https://example.com/dup");
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    expect(await within(dlg).findByText("Another feed already uses that address.")).toBeInTheDocument();
    await user.clear(url);
    await user.type(url, "https://example.com/new.xml");
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit feed" })).toBeNull());
    const saves = calls.filter((c) => c.method === "PATCH").map(body);
    expect(saves[saves.length - 1]).toEqual({ url: "https://example.com/new.xml" });
    // Opening the editor is a GET, not a no-op PATCH: the first PATCH is the duplicate-URL attempt.
    expect(saves[0]).toEqual({ url: "https://example.com/dup" });
    expect(calls.some((c) => c.method === "GET" && c.url.pathname === "/api/feeds/1")).toBe(true);
  });

  it("keeps unsafe options behind a warned disclosure", async () => {
    base({ "GET /api/feeds/1": () => json(feedDetail()) });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Edit Example Feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit feed" });
    await within(dlg).findByLabelText("Feed address");
    expect(within(dlg).queryByRole("switch", { name: /invalid security certificate/ })).toBeNull();
    await user.click(within(dlg).getByRole("button", { name: "Unsafe options" }));
    expect(within(dlg).getByText(/weaken protections/)).toBeInTheDocument();
    expect(within(dlg).getByRole("switch", { name: /invalid security certificate/ })).not.toBeChecked();
  });

  it("confirms delete with the starred count and the delete-starred choice", async () => {
    const withStars = { ...bootstrap, feeds: [{ ...bootstrap.feeds[0], starred_count: 3 }] };
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(withStars),
      "GET /api/feeds/1": () => json(feedDetail({ starred_count: 3 })),
      "DELETE /api/feeds/1": () => new Response(null, { status: 204 }),
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Edit Example Feed" }));
    await user.click(await screen.findByRole("button", { name: "Delete feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Delete Example Feed?" });
    expect(within(dlg).getByText(/3 starred articles/)).toBeInTheDocument();
    await user.click(within(dlg).getByRole("switch", { name: /Delete starred articles too/ }));
    await user.click(within(dlg).getByRole("button", { name: "Delete feed" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url.searchParams.get("delete_starred") === "1")).toBe(true));
  });

  it("diffForm produces a minimal PATCH", () => {
    const d = feedDetail() as never;
    expect(diffForm(d, { title: "", url: "https://example.com/feed.xml", folder: "1", interval: "", retention: "", autoRead: "", fulltext: false, enabled: true, dedup: "auto", userAgent: "", auth: "", clearAuth: false, ignoreCache: false, noHttp2: false, insecureTls: false, privateNet: false, keepGrants: false })).toEqual({});
    expect(diffForm(d, { title: "Mine", url: "https://example.com/feed.xml", folder: "1", interval: "60", retention: "0", autoRead: "90", fulltext: true, enabled: false, dedup: "link", userAgent: "x", auth: "u:p", clearAuth: false, ignoreCache: true, noHttp2: true, insecureTls: true, privateNet: true, keepGrants: false })).toEqual({
      custom_title: "Mine",
      interval_minutes: 60,
      retention: 0,
      auto_read_days: 90,
      fulltext: true,
      enabled: false,
      dedup_mode: "link",
      user_agent: "x",
      http_auth: "u:p",
      ignore_http_cache: true,
      disable_http2: true,
      allow_insecure_tls: true,
      allow_private_net: true,
    });
  });

  it("a new address keeps the unsafe options only when asked to, and says when the server turned them off", () => {
    const d = feedDetail({ allow_private_net: true, allow_insecure_tls: true }) as never;
    const base = { title: "", folder: "1", interval: "", retention: "", autoRead: "", fulltext: false, enabled: true, dedup: "auto" as const, userAgent: "", auth: "", clearAuth: false, ignoreCache: false, noHttp2: false, insecureTls: true, privateNet: true };
    // Left to the server: kept on a move within the feed's site, cleared on a move to another site.
    expect(diffForm(d, { ...base, url: "http://nas.lan/feed", keepGrants: false })).toEqual({ url: "http://nas.lan/feed" });
    // Kept: both are sent as shown, so a URL-only edit of a LAN feed keeps its grant wherever it points.
    expect(diffForm(d, { ...base, url: "http://nas2/feed", keepGrants: true })).toEqual({ url: "http://nas2/feed", allow_insecure_tls: true, allow_private_net: true });
    expect(diffForm(d, { ...base, url: "http://nas2/feed", keepGrants: true, insecureTls: false })).toEqual({ url: "http://nas2/feed", allow_insecure_tls: false, allow_private_net: true });
    // Unchanged address: the keep switch sends nothing.
    expect(diffForm(d, { ...base, url: "https://example.com/feed.xml", keepGrants: true })).toEqual({});
    expect(grantNotice({ privateNet: true, insecureTls: false })).toBe("This feed can reach addresses on your own network. If the new address is on another site, that is turned off unless you keep it.");
    expect(grantNotice({ privateNet: true, insecureTls: true })).toMatch(/own network and accept an invalid security certificate\./);
    expect(grantsDropped({ privateNet: true, insecureTls: false }, { allow_private_net: false, allow_insecure_tls: false })).toBe(true);
    expect(grantsDropped({ privateNet: true, insecureTls: false }, { allow_private_net: true, allow_insecure_tls: false })).toBe(false);
    expect(savedAddressMessage({ has_http_auth: true }, { privateNet: false, insecureTls: false }, { allow_private_net: false, allow_insecure_tls: false, has_http_auth: false })).toBe(
      "Feed address updated. It is on another site or host, so its saved login was removed. Kipple is fetching it now.",
    );
    expect(savedAddressMessage({ has_http_auth: false }, { privateNet: false, insecureTls: false }, { allow_private_net: false, allow_insecure_tls: false, has_http_auth: false })).toBe("Feed address updated. Kipple is fetching it now.");
  });

  it("editing a granted feed's address offers Keep for the new address and sends the grant when it is on", async () => {
    const toast = vi.spyOn(toasts, "toast");
    const granted = feedDetail({ allow_private_net: true });
    const { calls } = base({
      "GET /api/feeds/1": () => json(granted),
      "PATCH /api/feeds/1": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return json({ ...granted, url: b.url, allow_private_net: b.allow_private_net === true });
      },
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Edit Example Feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Edit feed" });
    const url = await within(dlg).findByLabelText("Feed address");
    expect(within(dlg).queryByRole("switch", { name: /Keep for the new address/ })).toBeNull();
    await user.clear(url);
    await user.type(url, "http://nas2/feed");
    expect(within(dlg).getByText(/This feed can reach addresses on your own network/)).toBeInTheDocument();
    await user.click(within(dlg).getByRole("switch", { name: /Keep for the new address/ }));
    await user.click(within(dlg).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit feed" })).toBeNull());
    expect(calls.filter((c) => c.method === "PATCH").map(body).at(-1)).toEqual({ url: "http://nas2/feed", allow_private_net: true, allow_insecure_tls: false });
    expect(toast).toHaveBeenLastCalledWith("Feed address updated. Kipple is fetching it now.");

    // Without Keep, a server that turned the grant off (another site) is reported, not silent.
    await user.click(await screen.findByRole("button", { name: "Edit Example Feed" }));
    const dlg2 = await screen.findByRole("dialog", { name: "Edit feed" });
    const url2 = await within(dlg2).findByLabelText("Feed address");
    await user.clear(url2);
    await user.type(url2, "https://elsewhere.example/feed");
    await user.click(within(dlg2).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit feed" })).toBeNull());
    expect(calls.filter((c) => c.method === "PATCH").map(body).at(-1)).toEqual({ url: "https://elsewhere.example/feed" });
    expect(toast).toHaveBeenLastCalledWith("Feed address updated. It is on another site or host, so its unsafe options were turned off. Kipple is fetching it now.");
    toast.mockRestore();
  });
});

describe("Folders and OPML", () => {
  it("creates a folder and reorders with buttons (no drag needed)", async () => {
    const two = { ...bootstrap, folders: [...bootstrap.folders, { id: "2", name: "Tech", position: 1, is_default: false, unread: 0 }] };
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(two),
      "POST /api/folders": () => json({ id: "3", name: "Fun", position: 2, is_default: false, unread: 0 }),
      "POST /api/reorder": () => json({ changed_feeds: [], changed_folders: ["1", "2"] }),
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "New folder" }));
    await user.type(await screen.findByLabelText("Name"), "Fun");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/folders")).toBe(true));

    await user.click(screen.getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show move buttons" }));
    await user.click(await screen.findByRole("button", { name: "Move folder Tech up" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/reorder").map(body)).toEqual([{ folders: ["2", "1"] }]));
  });

  it("reorders a folder's feeds with one reorder call carrying the folder id", async () => {
    const two = { ...bootstrap, feeds: [bootstrap.feeds[0], { ...bootstrap.feeds[0], id: "2", title: "Second Feed" }] };
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(two),
      "POST /api/reorder": () => json({ changed_feeds: ["1", "2"], changed_folders: [] }),
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Show move buttons" }));
    await user.click(await screen.findByRole("button", { name: "Move Second Feed up" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === "/api/reorder").map(body)).toEqual([{ feeds: [{ folder_id: "1", ids: ["2", "1"] }] }]));
  });

  it("imports OPML with the mark-older option and shows the summary", async () => {
    const { calls } = base({
      "POST /api/opml": () =>
        json({ folders_created: 2, feeds_added: 5, feeds_existing: [{ url: "https://a", feed_id: "1" }], folders_merged_case: [], memberships_dropped: [{ url: "https://b", kept: "News", dropped: ["Tech"] }], run_id: "7" }),
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Import OPML" }));
    const dlg = await screen.findByRole("dialog", { name: "Import OPML" });
    await user.upload(within(dlg).getByLabelText("OPML file"), new File(["<opml/>"], "feeds.opml", { type: "text/xml" }));
    await user.type(within(dlg).getByLabelText(/Mark older articles as read/), "7");
    await user.click(within(dlg).getByRole("button", { name: "Import" }));
    const done = await screen.findByRole("dialog", { name: "Import finished" });
    expect(within(done).getByText(/5 feeds added/)).toBeInTheDocument();
    expect(within(done).getByText("1 feed was already in Kipple and was left as it is")).toBeInTheDocument();
    expect(within(done).getByText("1 feed was listed in more than one folder. It stays in the first.")).toBeInTheDocument();
    const post = calls.find((c) => c.url.pathname === "/api/opml");
    expect(post?.url.searchParams.get("mark_read_older_than_days")).toBe("7");
  });

  it("the import summary lists skipped feeds and settings that were not applied", async () => {
    base({
      "POST /api/opml": () =>
        json({
          folders_created: 0, feeds_added: 1, feeds_existing: [], memberships_dropped: [],
          folders_merged_case: [{ kept: "News", merged: "news" }],
          skipped: [{ url: "ftp://bad", reason: "not a valid http(s) URL" }],
          invalid_attrs: ["https://x/feed: kipple:interval must be a number"],
          ignored_attrs: ["https://x/feed: kipple:allow_private_net", "https://y/feed: kipple:allow_insecure_tls"],
        }),
    });
    go("/feeds");
    const user = userEvent.setup({ applyAccept: false });
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Import OPML" }));
    const dlg = await screen.findByRole("dialog", { name: "Import OPML" });
    await user.upload(within(dlg).getByLabelText("OPML file"), new File(["<opml/>"], "feeds.opml", { type: "text/xml" }));
    await user.click(within(dlg).getByRole("button", { name: "Import" }));
    const done = await screen.findByRole("dialog", { name: "Import finished" });
    expect(within(done).getByText(/1 feed was skipped/)).toBeInTheDocument();
    expect(within(done).getByText(/ftp:\/\/bad: not a valid/)).toBeInTheDocument();
    expect(within(done).getByText(/https:\/\/x\/feed: allowing private-network addresses was ignored/)).toBeInTheDocument();
    expect(within(done).getByText(/https:\/\/y\/feed: skipping certificate checks was ignored/)).toBeInTheDocument();
    expect(within(done).getByText(/kipple:interval must be a number/)).toBeInTheDocument();
    expect(within(done).getByText(/news into News/)).toBeInTheDocument();
  });

  it("does not restrict the picker to types iOS may not know, and checks the file itself", async () => {
    const { calls } = base({ "POST /api/opml": () => json({ folders_created: 0, feeds_added: 1, feeds_existing: [], folders_merged_case: [], memberships_dropped: [] }) });
    go("/feeds");
    // A real browser treats */* as anything; user-event does not, so it is told not to filter.
    const user = userEvent.setup({ applyAccept: false });
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Import OPML" }));
    const dlg = await screen.findByRole("dialog", { name: "Import OPML" });
    const pick = within(dlg).getByLabelText("OPML file");
    expect(pick.getAttribute("accept")).toContain("*/*");
    // Not OPML at all: a clear message, and Import stays off.
    await user.upload(pick, new File(["just some notes"], "notes.txt", { type: "text/plain" }));
    expect(await within(dlg).findByText(/doesn.t look like an OPML file/)).toBeInTheDocument();
    expect(within(dlg).getByRole("button", { name: "Import" })).toBeDisabled();
    // No extension but XML inside (iOS Files can hand over such a name): accepted.
    await user.upload(pick, new File(['<?xml version="1.0"?><opml version="2.0"/>'], "subscriptions", { type: "" }));
    await waitFor(() => expect(within(dlg).getByRole("button", { name: "Import" })).toBeEnabled());
    expect(within(dlg).queryByText(/doesn.t look like an OPML file/)).toBeNull();
    await user.click(within(dlg).getByRole("button", { name: "Import" }));
    await screen.findByRole("dialog", { name: "Import finished" });
    expect(calls.some((c) => c.url.pathname === "/api/opml")).toBe(true);
  });

  it("links OPML export as a plain download", async () => {
    base();
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    expect(await screen.findByRole("menuitem", { name: "Export OPML" })).toHaveAttribute("href", "/api/opml");
  });
});

const HEALTH: HealthResponse = {
  feeds: [
    { id: "1", title: "Zed Blog", url: "https://zed.test/feed", url_original: null, status: "ok", redirect_pending: false, notices: [], enabled: true, disabled_reason: null, last_success_at: 1000, last_fetch_at: 1000, last_error_at: null, last_error_class: null, last_error: null, last_status: 200, consecutive_failures: 0, current_delay_s: 1800, next_fetch_at: 5000, redirect_to: null, redirect_kind: null, redirect_count: 0, last_new_items_at: 1000, trimmed_unread_count: 0, trimmed_unread_since: null, host_throttled_until: null },
    { id: "2", title: "NPR", url: "https://npr.test/feed", url_original: null, status: "failing", redirect_pending: false, notices: [], enabled: true, disabled_reason: null, last_success_at: 500, last_fetch_at: 900, last_error_at: 900, last_error_class: "http", last_error: "HTTP 404", last_status: 404, consecutive_failures: 20, current_delay_s: 86400, next_fetch_at: 90000, redirect_to: null, redirect_kind: null, redirect_count: 0, last_new_items_at: null, trimmed_unread_count: 0, trimmed_unread_since: null, host_throttled_until: null },
    { id: "3", title: "Moved Site", url: "http://old.test/feed", url_original: null, status: "redirecting", redirect_pending: true, notices: ["moved permanently (301) to https://new.test/feed"], enabled: true, disabled_reason: null, last_success_at: 800, last_fetch_at: 900, last_error_at: null, last_error_class: null, last_error: null, last_status: 301, consecutive_failures: 0, current_delay_s: 1800, next_fetch_at: 4000, redirect_to: "https://new.test/feed", redirect_kind: "permanent", redirect_count: 1, last_new_items_at: 800, trimmed_unread_count: 4, trimmed_unread_since: 100, host_throttled_until: null },
  ],
  reader_last_seen_at: null,
  snapshot: { last_at: null, last_error: "disk full" },
  clock: { ahead_s: 600 },
  db: { db_bytes: 1000, wal_bytes: 0, backup_bytes: 0, imgcache_bytes: 0 },
  unread_total: 10,
};

describe("Feed health", () => {
  it("sorts worst first, filters, and uses plain-English statuses", () => {
    expect(healthSort(HEALTH.feeds, "status", 1).map((f) => f.title)).toEqual(["NPR", "Moved Site", "Zed Blog"]);
    expect(healthSort(HEALTH.feeds, "title", 1).map((f) => f.title)).toEqual(["Moved Site", "NPR", "Zed Blog"]);
    expect(healthFilter(HEALTH.feeds, "attention", "").map((f) => f.title)).toEqual(["NPR", "Moved Site"]);
    expect(healthFilter(HEALTH.feeds, "redirects", "").map((f) => f.title)).toEqual(["Moved Site"]);
    expect(healthFilter(HEALTH.feeds, "all", "zed").map((f) => f.title)).toEqual(["Zed Blog"]);
  });

  it("offers a one-tap Update to new URL, shows warnings, and opens the fetch log", async () => {
    const { calls } = base({
      "GET /api/health/feeds": () => json(HEALTH),
      "PATCH /api/feeds/3": () => json(feedDetail({ url: "https://new.test/feed" })),
      "GET /api/health/feeds/2/log": () => json({ log: [{ id: "55", trigger: "schedule", started_at: 900, duration_ms: 120, outcome: "error", http_status: 404, error_class: "http", error: "HTTP 404", new_items: 0, updated_items: 0, trimmed_items: 0, first_item_id: null, last_item_id: null, bytes: null, final_url: null, note: null, keep: false }] }),
    });
    const { container } = go("/health");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Feed health" });
    expect(await screen.findByText(/last automatic database snapshot failed: disk full/)).toBeInTheDocument();
    expect(screen.getByText(/clock is about 10 minutes ahead/)).toBeInTheDocument();
    expect(screen.getByText("Failing")).toBeInTheDocument();
    expect(screen.getAllByText("Moved").length).toBeGreaterThan(1); // the chip and the filter option
    expect(await axe(container)).toHaveNoViolations();

    await user.click(screen.getByRole("button", { name: "Update to new URL" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH" && c.url.pathname === "/api/feeds/3" && body(c).url === "https://new.test/feed")).toBe(true));

    await user.click(screen.getByRole("button", { name: "Actions for NPR" }));
    await user.click(await screen.findByRole("menuitem", { name: "Fetch log" }));
    const dlg = await screen.findByRole("dialog", { name: "Fetch log for NPR" });
    expect(await within(dlg).findByText("HTTP 404", { selector: "p.break-words" })).toBeInTheDocument();
  });

  it("says when a sync app last called, the same for every app, and nothing before one has", async () => {
    base({ "GET /api/health/feeds": () => json({ ...HEALTH, reader_last_seen_at: Math.floor(Date.now() / 1000) - 300 }) });
    go("/health");
    expect(await screen.findByText(/A sync app was last seen 5 minutes ago/)).toBeInTheDocument();
  });

  it("names no sync app when none has called", async () => {
    base({ "GET /api/health/feeds": () => json(HEALTH) });
    go("/health");
    await screen.findByRole("heading", { name: "Feed health" });
    expect(await screen.findByText(/of 3 feeds/)).toBeInTheDocument();
    expect(screen.queryByText(/sync app/i)).toBeNull();
  });

  it("Mark this fetch read also marks the loaded article lists stale, not just the counts", async () => {
    const { calls } = base({
      "GET /api/health/feeds": () => json(HEALTH),
      "GET /api/health/feeds/1/log": () => json({ log: [{ id: "77", trigger: "schedule", started_at: 900, duration_ms: 120, outcome: "ok", http_status: 200, error_class: null, error: null, new_items: 2, updated_items: 0, trimmed_items: 0, first_item_id: "1001", last_item_id: "1002", bytes: null, final_url: null, note: null, keep: false }] }),
      "POST /api/feeds/1/mark-fetch-read": () => json({ changed: 2 }),
    });
    const client = makeQueryClient({ retry: false });
    client.setQueryData(["items", "unread"], { pages: [pageOf([card(1), card(2)])], pageParams: [undefined] });
    window.history.replaceState({ idx: 0 }, "", "/health");
    render(<App client={client} />);
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Feed health" });
    await user.click(await screen.findByRole("button", { name: "Actions for Zed Blog" }));
    await user.click(await screen.findByRole("menuitem", { name: "Fetch log" }));
    const dlg = await screen.findByRole("dialog", { name: "Fetch log for Zed Blog" });
    expect(client.getQueryState(["items", "unread"])?.isInvalidated).toBe(false);
    await user.click(await within(dlg).findByRole("button", { name: "Mark this fetch read" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/feeds/1/mark-fetch-read")).toBe(true));
    await waitFor(() => expect(client.getQueryState(["items", "unread"])?.isInvalidated).toBe(true));
  });

  // Health's own rows come from HealthFeed (a diagnostics shape); the bulk dialogs need the real Feed, matched
  // by id from the bootstrap. This mirrors HEALTH's ids and titles so both fixtures agree.
  const HEALTH_BOOTSTRAP_FEEDS = HEALTH.feeds.map((f) => ({
    id: f.id,
    folder_id: "1",
    title: f.title,
    site_url: f.url,
    icon: null,
    unread: 0,
    status: f.status,
    fulltext: false,
    retention: null,
    interval_minutes: null,
    is_archive: false,
    starred_count: 0,
  }));

  it("selects feeds and bulk-deletes them, one bad feed does not stop the rest", async () => {
    const { calls } = base({
      "GET /api/health/feeds": () => json(HEALTH),
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: HEALTH_BOOTSTRAP_FEEDS }),
      "DELETE /api/feeds/1": () => new Response(null, { status: 204 }),
      "DELETE /api/feeds/2": () => json({ error: "server_error", message: "boom" }, 500),
    });
    go("/health");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Feed health" });
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Zed Blog" }));
    await user.click(screen.getByRole("checkbox", { name: "Select NPR" }));
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dlg = await screen.findByRole("dialog", { name: "Delete 2 feeds?" });
    await user.click(within(dlg).getByRole("button", { name: "Delete 2 feeds" }));
    await screen.findByRole("dialog", { name: "Some feeds were not deleted" });
    expect(screen.getByText("1 deleted, 1 failed.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "DELETE").map((c) => c.url.pathname)).toEqual(["/api/feeds/1", "/api/feeds/2"]);
  });

  it("a search that hides ticked feeds drops them from the count and from what a bulk action touches", async () => {
    const { calls } = base({
      "GET /api/health/feeds": () => json(HEALTH),
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: HEALTH_BOOTSTRAP_FEEDS }),
      "DELETE /api/feeds/1": () => new Response(null, { status: 204 }),
    });
    go("/health");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Feed health" });
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("button", { name: "Select all" }));
    expect(screen.getByText("3 selected")).toBeInTheDocument();
    // Only Zed Blog is still on screen: the two hidden feeds stay ticked underneath, but must not be counted or deleted.
    await user.type(screen.getByRole("searchbox", { name: "Search feeds" }), "zed");
    expect(screen.getByText("1 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dlg = await screen.findByRole("dialog", { name: "Delete 1 feed?" });
    await user.click(within(dlg).getByRole("button", { name: "Delete 1 feed" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(calls.filter((c) => c.method === "DELETE").map((c) => c.url.pathname)).toEqual(["/api/feeds/1"]);
  });

  it("after a bulk Delete Select mode ends and a feed ticked but hidden by the search is not left ticked (#96)", async () => {
    base({
      "GET /api/health/feeds": () => json(HEALTH),
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: HEALTH_BOOTSTRAP_FEEDS }),
      "DELETE /api/feeds/1": () => new Response(null, { status: 204 }),
    });
    go("/health");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Feed health" });
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("checkbox", { name: "Select NPR" }));
    await user.type(screen.getByRole("searchbox", { name: "Search feeds" }), "zed");
    await user.click(screen.getByRole("checkbox", { name: "Select Zed Blog" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dlg = await screen.findByRole("dialog", { name: "Delete 1 feed?" });
    await user.click(within(dlg).getByRole("button", { name: "Delete 1 feed" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // Same as Turn on/off: Select mode is over, so no selection bar is left for the toast to cover.
    expect(screen.getByRole("button", { name: "Select" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Selected feeds" })).toBeNull();
    await user.clear(screen.getByRole("searchbox", { name: "Search feeds" }));
    await user.click(screen.getByRole("button", { name: "Select" }));
    expect(screen.getByText("0 selected")).toBeInTheDocument();
  });

  it("bulk turns feeds off, then Done clears the selection", async () => {
    const { calls } = base({
      "GET /api/health/feeds": () => json(HEALTH),
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds: HEALTH_BOOTSTRAP_FEEDS }),
      "PATCH /api/feeds/1": () => json({ ...feedDetail(), enabled: false }),
      "PATCH /api/feeds/2": () => json({ ...feedDetail(), enabled: false }),
      "PATCH /api/feeds/3": () => json({ ...feedDetail(), enabled: false }),
    });
    go("/health");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Feed health" });
    await user.click(screen.getByRole("button", { name: "Select" }));
    await user.click(screen.getByRole("button", { name: "Select all" }));
    expect(screen.getByText("3 selected")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Turn off" }));
    const dlg = await screen.findByRole("dialog", { name: "Turn off 3 feeds?" });
    await user.click(within(dlg).getByRole("button", { name: "Turn off 3 feeds" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls.filter((c) => c.method === "PATCH").map((c) => c.url.pathname).sort()).toEqual(["/api/feeds/1", "/api/feeds/2", "/api/feeds/3"]);
    // Turning off a feed exits selection: Select is back to its starting state.
    expect(screen.getByRole("button", { name: "Select" })).toBeInTheDocument();
  });
});

describe("Account and backup", () => {
  it("shows a generated API password once with Copy and the sync app instructions", async () => {
    const { calls } = base({ "POST /api/account/api-password": () => json({ api_password: "maple river lantern 42" }) });
    go("/settings/account");
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await user.click(await screen.findByRole("button", { name: "Generate API password" }));
    const dlg = await screen.findByRole("dialog", { name: "Generate API password" });
    await user.type(within(dlg).getByLabelText("Your web password"), "old passphrase");
    await user.click(within(dlg).getByRole("button", { name: "Generate" }));
    const shown = await screen.findByRole("dialog", { name: "Your new API password" });
    expect(within(shown).getByTestId("api-password")).toHaveTextContent("maple river lantern 42");
    expect(within(shown).getByText(/Server URL/)).toHaveTextContent("/api/greader.php");
    await user.click(within(shown).getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith("maple river lantern 42");
    expect(body(calls.find((c) => c.url.pathname === "/api/account/api-password") as never)).toEqual({ current: "old passphrase", generate: true });
    await user.click(within(shown).getByRole("button", { name: "Done" }));
    expect(screen.queryByText("maple river lantern 42")).toBeNull();
  });

  it("changes the password with inline validation", async () => {
    const { calls } = base({ "POST /api/account/password": () => json({ error: "bad_password" }, 403) });
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Change web password" }));
    const dlg = await screen.findByRole("dialog", { name: "Change web password" });
    await user.type(within(dlg).getByLabelText("Current password"), "old");
    await user.type(within(dlg).getByLabelText("New password"), "abc");
    expect(within(dlg).getByText("Use at least 5 characters.")).toBeInTheDocument();
    expect(within(dlg).getByRole("button", { name: "Change password" })).toBeDisabled();
    await user.type(within(dlg).getByLabelText("New password"), "def");
    await user.type(within(dlg).getByLabelText("New password again"), "abcdeX");
    expect(within(dlg).getByText("The passwords don't match.")).toBeInTheDocument();
    await user.clear(within(dlg).getByLabelText("New password again"));
    await user.type(within(dlg).getByLabelText("New password again"), "abcdef");
    await user.click(within(dlg).getByRole("button", { name: "Change password" }));
    expect(await within(dlg).findByRole("alert")).toHaveTextContent("current password isn't right");
    expect(calls.some((c) => c.url.pathname === "/api/account/password")).toBe(true);
  });

  it("says busy, not locked out, when the password check could not get its turn", async () => {
    base({ "POST /api/account/password": () => json({ error: "busy" }, 503, { "Retry-After": "5" }) });
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Change web password" }));
    const dlg = await screen.findByRole("dialog", { name: "Change web password" });
    await user.type(within(dlg).getByLabelText("Current password"), "old");
    await user.type(within(dlg).getByLabelText("New password"), "abcdef");
    await user.type(within(dlg).getByLabelText("New password again"), "abcdef");
    await user.click(within(dlg).getByRole("button", { name: "Change password" }));
    expect(await within(dlg).findByRole("alert")).toHaveTextContent("Kipple is busy. Try again in a moment.");
  });

  it("confirms an export with its warning and contents, then offers the download link", async () => {
    base({
      "POST /api/backup": () =>
        json({
          token: "t",
          url: "/api/backup/t",
          filename: "kipple-backup-20260925-101500.zip",
          bytes: 5_000_000,
          expires_at: 0,
          expires_in: 300,
          warning: "This file contains your password hashes and secrets. Keep it private.",
          contents: { kipple_version: "0.2.0", schema_version: 6, created_at: "2026-09-25T10:15:00Z", feeds: 120, items: 30000, starred: 42, db_bytes: 4_000_000 },
        }),
    });
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Export backup" }));
    const dlg = await screen.findByRole("dialog", { name: "Download backup" });
    expect(within(dlg).getByText(/password hashes and secrets/)).toBeInTheDocument();
    expect(within(dlg).getByText(/30000 \(42 starred\)/)).toBeInTheDocument();
    // The manifest stamps created_at as RFC 3339 text, not Unix seconds (#92).
    expect(within(dlg).getByText("Made").nextElementSibling).toHaveTextContent(/2026/);
    expect(dlg).not.toHaveTextContent("Invalid Date");
    expect(within(dlg).getByRole("link", { name: /kipple-backup-20260925-101500\.zip/ })).toHaveAttribute("href", "/api/backup/t");
  });

  it("waits for a background export job, then shows the same confirmation", async () => {
    let polls = 0;
    base({
      "POST /api/backup": () => json({ job_id: "j1", status: "building" }, 202),
      "GET /api/backup/jobs/j1": () => {
        polls += 1;
        return polls < 2
          ? json({ status: "building" })
          : json({ status: "ready", token: "t2", url: "/api/backup/t2", filename: "kipple-backup-x.zip", bytes: 10, expires_at: 0, expires_in: 300, warning: "Keep it private.", contents: { kipple_version: "1", schema_version: 1, created_at: "2026-09-25T10:15:00Z", feeds: 1, items: 2, starred: 0, db_bytes: 5 } });
      },
    });
    const { exportBackup } = await import("@/api/admin");
    const info = await exportBackup({ intervalMs: 1 });
    expect(info.url).toBe("/api/backup/t2");
    expect(polls).toBe(2);
  });

  it("maps a failed export job to the disk-space message", async () => {
    base({
      "POST /api/backup": () => json({ job_id: "j2", status: "building" }, 202),
      "GET /api/backup/jobs/j2": () => json({ status: "failed", error: "no_space", message: "low" }),
    });
    const { exportBackup } = await import("@/api/admin");
    await expect(exportBackup({ intervalMs: 1 })).rejects.toMatchObject({ status: 507, code: "no_space" });
  });

  it.each([
    [409, { error: "busy", retry_after: 30, message: "busy" }, /busy with a database snapshot.*30 seconds/],
    [507, { error: "no_space", message: "x" }, /enough free disk space/],
    [413, { error: "too_large" }, /larger than 4 GiB/],
  ])("explains backup error %s", async (status, payload, re) => {
    base({ "POST /api/backup": () => json(payload, status as number) });
    go("/settings/account");
    await userEvent.click(await screen.findByRole("button", { name: "Export backup" }));
    await waitFor(() => expect(screen.getAllByRole("alert").some((a) => re.test(a.textContent ?? ""))).toBe(true));
  });
});
