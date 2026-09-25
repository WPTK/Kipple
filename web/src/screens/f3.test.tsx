import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { HealthResponse, SettingMeta } from "@/api/admin";
import { DEFAULT_PREFS, applyPrefs, parsePrefs, prefsStore } from "@/lib/prefs";
import { FONTS } from "@/lib/fonts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { healthFilter, healthSort } from "./HealthScreen";
import { diffForm } from "./feeds/FeedEditor";

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
  meta({ key: "ui.mark_read_on_scroll", kind: "bool", label: "Mark articles read as I scroll", value: false, default: false }),
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
  meta({ key: "ui.layouts", kind: "json", group: "advanced", surface: "hidden", value: {}, default: {} }),
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
  it("draws every kind from the metadata, groups them, and collapses Advanced", async () => {
    base();
    const { container } = go("/settings");
    await screen.findByRole("heading", { name: "Sync" });
    for (const h of ["Appearance", "Accessibility", "Lists", "Keyboard", "Reading", "Sync", "Library", "Account", "Advanced"]) {
      expect(screen.getByRole("heading", { name: h })).toBeInTheDocument();
    }
    expect(screen.getByRole("switch", { name: /Remove tracking from links/ })).toBeChecked();
    expect(screen.getByRole("spinbutton", { name: "How often to check feeds" })).toHaveValue(30);
    expect(screen.getByRole("radio", { name: "Always identify as Kipple" })).toBeInTheDocument(); // enum with 3 options: segmented
    expect(screen.getByRole("combobox", { name: "Articles to keep per feed" })).toHaveValue("500"); // enum with 6: select
    expect(screen.getByRole("textbox", { name: "Time zone" })).toHaveValue("America/New_York");
    expect(screen.queryByText("Remembered list layouts")).toBeNull(); // json is never shown
    // Advanced is collapsed until opened.
    expect(screen.queryByRole("switch", { name: /Send feed icons/ })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Show advanced settings" }));
    expect(screen.getByRole("switch", { name: /Send feed icons/ })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
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
    go("/settings");
    const user = userEvent.setup();
    const sw = await screen.findByRole("switch", { name: /Remove tracking from links/ });
    await user.click(sw);
    expect(sw).not.toBeChecked(); // optimistic
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH" && body(c)["links.strip_tracking"] === false)).toBe(true));
    // A non-default value offers a per-setting reset.
    const reset = await screen.findByRole("button", { name: "Reset Remove tracking from links to default" });
    await user.click(reset);
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH").some((c) => body(c)["links.strip_tracking"] === null)).toBe(true));

    await user.click(screen.getByRole("button", { name: "Increase How often to check feeds" }));
    const alert = await screen.findByText("must be an integer from 5 to 1440");
    expect(alert).toHaveAttribute("role", "alert");
    expect(interval).toBe(30);
  });
});

describe("Accessibility section", () => {
  it("has the six controls plus large targets and titles-only, and they drive the document", async () => {
    base();
    go("/settings");
    const user = userEvent.setup();
    const section = await screen.findByRole("region", { name: "Accessibility" });
    const w = within(section);
    expect(w.getByRole("group", { name: "Text size" })).toBeInTheDocument();
    expect(w.getByRole("switch", { name: /Easy-to-read font/ })).toBeInTheDocument();
    expect(w.getByRole("group", { name: "Reading spacing" })).toBeInTheDocument();
    expect(w.getByRole("group", { name: "Reduce motion" })).toBeInTheDocument();
    expect(await w.findByRole("switch", { name: /Mark articles read as I scroll/ })).not.toBeChecked();
    expect(w.getByRole("switch", { name: /Listen to articles/ })).toBeDisabled(); // jsdom has no speechSynthesis

    await user.click(w.getByRole("switch", { name: /Easy-to-read font/ }));
    expect(prefsStore.get().font).toBe("easy");
    await user.click(w.getByRole("radio", { name: "Roomy" }));
    await user.click(w.getByRole("radio", { name: "On" }));
    await user.click(w.getByRole("switch", { name: /Larger buttons/ }));
    applyPrefs(prefsStore.get());
    const root = document.documentElement;
    expect(root.dataset.spacing).toBe("roomy");
    expect(root.dataset.motion).toBe("on");
    expect(root.dataset.targets).toBe("large");
    expect(root.style.getPropertyValue("--kp-reading-font")).toContain("Atkinson Hyperlegible Next");
    await user.click(w.getByRole("switch", { name: /Titles only in lists/ }));
    expect(JSON.parse(localStorage.getItem("kipple.device.v1") ?? "{}").layout).toBe("headlines");
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
    expect(diffForm(d, { title: "", url: "https://example.com/feed.xml", folder: "1", interval: "", retention: "", fulltext: false, enabled: true, dedup: "auto", userAgent: "", auth: "", clearAuth: false, ignoreCache: false, noHttp2: false, insecureTls: false, privateNet: false })).toEqual({});
    expect(diffForm(d, { title: "Mine", url: "https://example.com/feed.xml", folder: "1", interval: "60", retention: "0", fulltext: true, enabled: false, dedup: "link", userAgent: "x", auth: "u:p", clearAuth: false, ignoreCache: true, noHttp2: true, insecureTls: true, privateNet: true })).toEqual({
      custom_title: "Mine",
      interval_minutes: 60,
      retention: 0,
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
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "New folder" }));
    await user.type(await screen.findByLabelText("Name"), "Fun");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/folders")).toBe(true));

    await user.click(screen.getByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Reorder folders and feeds" }));
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
    await user.click(await screen.findByRole("button", { name: "Feed actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Reorder folders and feeds" }));
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
    expect(within(done).getByText(/already in Kipple/)).toBeInTheDocument();
    expect(within(done).getByText(/more than one folder/)).toBeInTheDocument();
    const post = calls.find((c) => c.url.pathname === "/api/opml");
    expect(post?.url.searchParams.get("mark_read_older_than_days")).toBe("7");
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
  clients: [],
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
});

describe("Account and backup", () => {
  it("shows a generated API password once with Copy and the sync app instructions", async () => {
    const { calls } = base({ "POST /api/account/api-password": () => json({ api_password: "maple river lantern 42" }) });
    go("/settings");
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
    go("/settings");
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
          contents: { kipple_version: "0.2.0", schema_version: 6, created_at: 1_790_000_000, feeds: 120, items: 30000, starred: 42, db_bytes: 4_000_000 },
        }),
    });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Export backup" }));
    const dlg = await screen.findByRole("dialog", { name: "Download backup" });
    expect(within(dlg).getByText(/password hashes and secrets/)).toBeInTheDocument();
    expect(within(dlg).getByText(/30000 \(42 starred\)/)).toBeInTheDocument();
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
          : json({ status: "ready", token: "t2", url: "/api/backup/t2", filename: "kipple-backup-x.zip", bytes: 10, expires_at: 0, expires_in: 300, warning: "Keep it private.", contents: { kipple_version: "1", schema_version: 1, created_at: 1_790_000_000, feeds: 1, items: 2, starred: 0, db_bytes: 5 } });
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
    go("/settings");
    await userEvent.click(await screen.findByRole("button", { name: "Export backup" }));
    await waitFor(() => expect(screen.getAllByRole("alert").some((a) => re.test(a.textContent ?? ""))).toBe(true));
  });
});
