import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import type { About } from "@/api/about";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { BUNDLE } from "@/lib/buildInfo";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { offlineStore } from "@/lib/offlineState";
import type { Release } from "@/lib/whatsNewParse";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { themeStore } from "@/theme/theme";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

// Build info in the UI: Settings > About and its debug text, the "Kipple was updated" banner, and What's new.

vi.mock("@/lib/whatsNewData", () => ({
  loadReleases: async (): Promise<Release[]> => [
    { version: "0.5.0", date: "2026-10-20", intro: "The setup wizard.", groups: [{ kind: "Added", items: ["A wizard for a first run."] }] },
    { version: "0.4.0", date: "2026-10-10", intro: "", groups: [{ kind: "Fixed", items: ["A fix from the old version."] }] },
  ],
}));

class NoES {
  addEventListener() {}
  close() {}
}

const ABOUT: About = {
  version: "v0.5.0-beta.1",
  commit: "0123456789abcdef0123456789abcdef01234567",
  build_date: "2026-10-01T12:00:00Z",
  go_version: "go1.27.0",
  os_arch: "linux/amd64",
  schema_version: 9,
  schema_latest: 9,
  sqlite_version: "3.50.0",
  started_at: "2026-10-01T13:00:00Z",
  uptime_s: 7200,
  data_dir_writable: true,
  tz: "America/New_York",
  auth_mode: "password",
  access_enabled: false,
  public_url_set: false,
  web_build: "dev",
};

const settingsBody = { settings: [], values: {} };

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json(settingsBody),
    "GET /api/about": () => json(ABOUT),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const realBundle = { ...BUNDLE };

function setClipboard(c: { writeText: (t: string) => Promise<void> }) {
  Object.defineProperty(navigator, "clipboard", { value: c, configurable: true });
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  prefsStore.set({ ...DEFAULT_PREFS });
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  Object.assign(BUNDLE, realBundle);
  Reflect.deleteProperty(navigator, "clipboard");
  vi.unstubAllGlobals();
});

describe("Settings > About", () => {
  it("shows the server's and this page's facts and passes axe", async () => {
    routes();
    const { container } = go("/settings/about");
    expect(await screen.findByText("v0.5.0-beta.1")).toBeInTheDocument();
    expect(screen.getByText("0123456789abcdef0123456789abcdef01234567")).toBeInTheDocument();
    expect(screen.getByText("go1.27.0 (linux/amd64)")).toBeInTheDocument();
    expect(screen.getByText("Database schema")).toBeInTheDocument();
    expect(screen.getByText("Web build in this page")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("Copy debug info shows the block, then copies it", async () => {
    routes();
    const writeText = vi.fn().mockResolvedValue(undefined);
    go("/settings/about");
    const user = userEvent.setup();
    setClipboard({ writeText }); // after setup(): user-event installs a clipboard of its own
    await user.click(await screen.findByRole("button", { name: "Copy debug info" }));
    const box = (await screen.findByRole("textbox", { name: "Debug info" })) as HTMLTextAreaElement;
    expect(box.value.split("\n")[0]).toBe("Kipple debug info");
    expect(box.value).toContain("Version: v0.5.0-beta.1");
    expect(box.value).toContain("Commit: 0123456789abcdef0123456789abcdef01234567");
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(box.value));
    expect(await screen.findByText(/Copied\./)).toBeInTheDocument();
    // Nothing that identifies the person or the place.
    expect(box.value).not.toMatch(/admin|password:|@/i);
  });

  it("when the browser refuses to copy, the block stays on screen to copy by hand", async () => {
    routes();
    go("/settings/about");
    const user = userEvent.setup();
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error("denied")) });
    await user.click(await screen.findByRole("button", { name: "Copy debug info" }));
    expect(await screen.findByText(/would not copy it/)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Debug info" })).toBeInTheDocument();
  });

  it("What's new opens the latest releases from the changelog", async () => {
    routes();
    go("/settings/about");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "What's new" }));
    expect(await screen.findByRole("dialog", { name: "What's new in Kipple" })).toBeInTheDocument();
    expect(screen.getByText("A wizard for a first run.")).toBeInTheDocument();
    expect(screen.getByText("A fix from the old version.")).toBeInTheDocument();
  });
});

describe("the update banner", () => {
  it("appears when the network bootstrap names a different web build than this bundle carries", async () => {
    BUNDLE.build = "aaaaaaaaaa";
    routes({ "GET /api/bootstrap": () => json({ ...bootstrap, web_build: "bbbbbbbbbb" }) });
    go("/");
    await waitFor(() => expect(screen.getByTestId("offline-notice")).toHaveTextContent("A newer version of Kipple is ready."));
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("stays away when the builds match, when the server reports none, and for a development bundle", async () => {
    BUNDLE.build = "aaaaaaaaaa";
    routes({ "GET /api/bootstrap": () => json({ ...bootstrap, web_build: "aaaaaaaaaa" }) });
    const first = go("/");
    await screen.findByRole("main");
    await waitFor(() => expect(offlineStore.get().updateReady).toBe(false));
    first.unmount();

    routes({ "GET /api/bootstrap": () => json(bootstrap) }); // an older server: no web_build
    const second = go("/");
    await screen.findByRole("main");
    expect(offlineStore.get().updateReady).toBe(false);
    second.unmount();

    BUNDLE.build = "dev";
    routes({ "GET /api/bootstrap": () => json({ ...bootstrap, web_build: "bbbbbbbbbb" }) });
    go("/");
    await screen.findByRole("main");
    expect(offlineStore.get().updateReady).toBe(false);
  });

  it("ignores the worker's stored copy of the bootstrap", async () => {
    BUNDLE.build = "aaaaaaaaaa";
    routes({ "GET /api/bootstrap": () => json({ ...bootstrap, web_build: "bbbbbbbbbb" }, 200, { "X-Kipple-Cache": "1" }) });
    go("/");
    await screen.findByRole("main");
    expect(offlineStore.get().updateReady).toBe(false);
  });
});

describe("What's new after an upgrade", () => {
  const withSeen = (seen: string, feeds = bootstrap.feeds) => ({ ...bootstrap, feeds, settings: { ...bootstrap.settings, "ui.whats_new_seen": seen } });

  it("shows the releases since the last one seen, once, and remembers the running version on close", async () => {
    BUNDLE.version = "v0.5.0";
    const r = routes({
      "GET /api/bootstrap": () => json(withSeen("0.3.0")),
      "PATCH /api/settings": () => json(settingsBody),
    });
    go("/");
    const dialog = await screen.findByRole("dialog", { name: "What's new in Kipple" });
    expect(dialog).toHaveTextContent("A wizard for a first run.");
    expect(dialog).toHaveTextContent("A fix from the old version.");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => {
      const patch = r.calls.find((c) => c.method === "PATCH" && c.url.pathname === "/api/settings");
      expect(JSON.parse(String(patch?.init?.body))).toEqual({ "ui.whats_new_seen": "v0.5.0" });
    });
    expect(screen.queryByRole("dialog", { name: "What's new in Kipple" })).toBeNull();
  });

  it("does not show when the running version was already seen, or for a development bundle", async () => {
    BUNDLE.version = "v0.5.0";
    const r = routes({ "GET /api/bootstrap": () => json(withSeen("v0.5.0")) });
    const first = go("/");
    await screen.findByRole("main");
    expect(screen.queryByRole("dialog")).toBeNull();
    first.unmount();

    BUNDLE.version = "dev";
    r.fn.mockClear();
    routes({ "GET /api/bootstrap": () => json(withSeen("0.3.0")) });
    go("/");
    await screen.findByRole("main");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("marks a first run (no feeds yet) as seen without showing anything", async () => {
    BUNDLE.version = "v0.5.0";
    const r = routes({
      "GET /api/bootstrap": () => json(withSeen("", [])),
      "PATCH /api/settings": () => json(settingsBody),
    });
    go("/");
    await waitFor(() => {
      const patch = r.calls.find((c) => c.method === "PATCH" && c.url.pathname === "/api/settings");
      expect(JSON.parse(String(patch?.init?.body))).toEqual({ "ui.whats_new_seen": "v0.5.0" });
    });
    expect(screen.queryByRole("dialog", { name: "What's new in Kipple" })).toBeNull();
  });
});
