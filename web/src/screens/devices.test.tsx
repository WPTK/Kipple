import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import type { DeviceRow } from "@/api/devices";
import { initialLive, liveStore } from "@/api/events";
import type { Bootstrap, DeviceView } from "@/api/types";
import { devicePrefsStore, resetDevicePrefs } from "@/lib/devicePrefs";
import { SYNC_FLAG_KEY, resetDeviceSync, syncStore } from "@/lib/deviceSync";
import { DEFAULT_PREFS, prefsStore, updatePrefs } from "@/lib/prefs";
import { clearToasts } from "@/shell/toasts";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { themeStore } from "@/theme/theme";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

const NOW = Math.floor(Date.now() / 1000);
const row = (id: string, over: Partial<DeviceRow> = {}): DeviceRow => ({
  id,
  name: "",
  current: false,
  user_agent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1",
  client: "web",
  created_at: NOW - 86400 * 30,
  last_seen_at: NOW - 3 * 86400,
  overrides: 0,
  ...over,
});

const DEFAULTS = { "ui.theme": "system", "ui.theme_day": "paper", "ui.theme_night": "midnight", "ui.font_body": "", "client.layout": "magazine" };
const deviceView = (over: Partial<DeviceView> = {}): DeviceView => ({ id: "me", name: "", profile: {}, merged: { ...DEFAULTS }, ...over });
const boot: Bootstrap = { ...bootstrap, device: deviceView() };

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json(boot),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    ...extra,
  });
}

const me = row("me", { name: "the owner's laptop", current: true, user_agent: "Mozilla/5.0 (Windows NT 10.0) Chrome/130.0 Safari/537.36", last_seen_at: NOW });
const phone = row("phone", { overrides: 4 });
const tablet = row("tablet", { name: "Living room iPad", overrides: 0, user_agent: "Mozilla/5.0 (iPad) Safari/604.1", client: "pwa" });

function go() {
  window.history.replaceState({ idx: 0 }, "", "/settings");
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  clearToasts();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDeviceSync();
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  prefsStore.set({ ...DEFAULT_PREFS });
  resetDevicePrefs();
  localStorage.setItem(SYNC_FLAG_KEY, "1");
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  resetDeviceSync();
});

const body = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body));

describe("Settings > Devices", () => {
  it("lists the devices with when each was last seen and how many settings it has of its own", async () => {
    routes({ "GET /api/devices": () => json({ devices: [me, phone, tablet] }) });
    const { container } = go();
    const list = await screen.findByRole("list", { name: "Your devices" });
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(items[0]).toHaveTextContent("the owner's laptop");
    expect(items[0]).toHaveTextContent("This device");
    expect(items[1]).toHaveTextContent("Safari on iPhone"); // no name: described from the user agent
    expect(items[1]).toHaveTextContent("Last seen 3 days ago");
    expect(items[1]).toHaveTextContent("4 settings of its own");
    expect(items[2]).toHaveTextContent("Living room iPad");
    expect(items[2]).toHaveTextContent("uses the defaults");
    expect(items[2]).toHaveTextContent("Safari on iPad (installed app)");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("renames this device with PUT /api/device/name", async () => {
    const { calls } = routes({
      "GET /api/devices": () => json({ devices: [me] }),
      "PUT /api/device/name": () => json(deviceView({ name: "Desk" })),
    });
    go();
    const user = userEvent.setup();
    const input = await screen.findByLabelText("This device's name");
    expect(screen.getByRole("button", { name: "Save name" })).toBeDisabled();
    await user.clear(input);
    await user.type(input, "  Desk  ");
    await user.click(screen.getByRole("button", { name: "Save name" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.pathname === "/api/device/name")).toBe(true));
    expect(body(calls.find((c) => c.method === "PUT") as never)).toEqual({ name: "Desk" });
  });

  it("copies another device's settings here after a confirm, and adopts them", async () => {
    updatePrefs({ font: "literata" });
    const { calls } = routes({
      "GET /api/devices": () => json({ devices: [me, phone] }),
      "POST /api/device/copy-from/phone": () => json(deviceView({ profile: { "client.layout": "inbox", "ui.font_body": "Inter" }, merged: { ...DEFAULTS, "client.layout": "inbox", "ui.font_body": "Inter" } })),
    });
    go();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Copy settings from Safari on iPhone" }));
    const dlg = within(await screen.findByRole("dialog", { name: "Copy settings here?" }));
    expect(dlg.getByText(/replaces this device's own settings/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false); // nothing until confirmed
    await user.click(dlg.getByRole("button", { name: "Copy settings" }));
    await waitFor(() => expect(devicePrefsStore.get().layout).toBe("inbox"));
    expect(prefsStore.get().font).toBe("inter");
    expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/device/copy-from/phone")).toBe(true);
    expect(syncStore.get().status).toBe("idle");
  });

  it("makes this device the default for new devices, with a confirm", async () => {
    const { calls } = routes({
      "GET /api/devices": () => json({ devices: [me] }),
      "POST /api/device/make-default": () => json(deviceView()),
    });
    go();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /Use this device's settings as the default for new devices/ }));
    const dlg = within(await screen.findByRole("dialog", { name: "Use these settings as the default?" }));
    await user.click(dlg.getByRole("button", { name: "Use as default" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/device/make-default")).toBe(true));
  });

  it("forgets another device after a confirm, and never offers it for this one", async () => {
    const { calls } = routes({
      "GET /api/devices": () => json({ devices: [me, phone] }),
      "DELETE /api/devices/phone": () => new Response(null, { status: 204 }),
    });
    go();
    const user = userEvent.setup();
    await screen.findByRole("list", { name: "Your devices" });
    expect(screen.queryByRole("button", { name: /Forget the owner's laptop/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Forget Safari on iPhone" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Forget this device?" })).getByRole("button", { name: "Forget device" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url.pathname === "/api/devices/phone")).toBe(true));
  });

  it("resets this device to the defaults with a confirm", async () => {
    updatePrefs({ font: "literata" });
    const { calls } = routes({
      "GET /api/devices": () => json({ devices: [me] }),
      "POST /api/device/copy-from/defaults": () => json(deviceView()),
    });
    go();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Reset this device to defaults" }));
    await user.click(within(await screen.findByRole("dialog", { name: "Reset this device to defaults?" })).getByRole("button", { name: "Reset this device" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/device/copy-from/defaults")).toBe(true));
    await waitFor(() => expect(prefsStore.get().font).toBe("default"));
  });
});

describe("the bootstrap device profile", () => {
  it("is applied over the local cache when the app loads, and a failed save shows Couldn't save with Retry", async () => {
    updatePrefs({ font: "inter" });
    routes({
      "GET /api/devices": () => json({ devices: [me] }),
      "GET /api/bootstrap": () => json({ ...boot, device: deviceView({ profile: { "ui.theme": "linen" }, merged: { ...DEFAULTS, "ui.theme": "linen" } }) }),
      "PATCH /api/device": () => new Response("no", { status: 500 }),
    });
    go();
    await screen.findByRole("heading", { name: "Settings" });
    await waitFor(() => expect(themeStore.get()).toMatchObject({ mode: "fixed", fixed: "linen" }));
    expect(prefsStore.get().font).toBe("default"); // the server wins
    const user = userEvent.setup();
    await user.click(screen.getByRole("radio", { name: "Larger" }));
    const status = await screen.findByTestId("save-status", {}, { timeout: 4000 });
    expect(status).toHaveTextContent("Couldn't save your settings");
    expect(within(status).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(prefsStore.get().textSize).toBe(1.25); // the local value stays
  }, 15000);
});
