import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { SettingMeta } from "@/api/admin";
import { deviceZone, isUtcName, resetZoneOffer } from "@/lib/deviceZone";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { richStats } from "./stats.fixtures";

class NoES {
  addEventListener() {}
  close() {}
}

const NY = "America/New_York";
const OFFER = `Use this device's time zone (${NY})`;

/** What the browser reports as its own zone. */
function deviceIn(zone: string) {
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({ timeZone: zone } as Intl.ResolvedDateTimeFormatOptions);
}

const tzMeta = (value: string): SettingMeta => ({
  key: "tz",
  value,
  default: "",
  label: "Time zone",
  description: "The time zone for daily statistics. UTC until you choose one.",
  group: "account",
  kind: "text",
  surface: "settings",
});

/** The server with the saved zone `tz` ("" is none chosen): the bootstrap, the settings list and a PATCH that saves. */
function server(tz: string) {
  let saved = tz;
  const body = () => ({ settings: [tzMeta(saved)], values: { tz: saved } });
  const m = mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings: { tz: saved } }),
    "GET /api/settings": () => json(body()),
    "PATCH /api/settings": (_u, init) => {
      saved = JSON.parse(String(init!.body)).tz;
      return json(body());
    },
    "GET /api/stats/summary": () => json(richStats),
    "GET /api/items": () => json({ items: [], next_cursor: null }),
  });
  const patches = () => m.calls.filter((c) => c.init?.method === "PATCH").map((c) => JSON.parse(String(c.init?.body)));
  return { patches };
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  localStorage.clear();
  resetZoneOffer();
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("the device's time zone, as the browser reports it", () => {
  it("is offered only when it is a real zone other than UTC", () => {
    deviceIn(NY);
    expect(deviceZone()).toBe(NY);
    vi.restoreAllMocks();
    for (const utc of ["UTC", "Etc/UTC", "GMT", "Etc/GMT", "Zulu"]) {
      deviceIn(utc);
      expect(deviceZone(), utc).toBeNull();
      vi.restoreAllMocks();
    }
    deviceIn("Nowhere/City");
    expect(deviceZone()).toBeNull();
    expect(isUtcName("Europe/London")).toBe(false); // London is not UTC all year
  });
});

describe("Settings > Stats offers this device's zone", () => {
  it("shows a button while no zone is chosen, and pressing it saves that zone like typing it", async () => {
    deviceIn(NY);
    const s = server("");
    go("/settings/statistics");
    const box = await screen.findByRole("textbox", { name: "Time zone" }, { timeout: 15000 });
    expect(box).toHaveValue("");
    await userEvent.setup().click(await screen.findByRole("button", { name: OFFER }));
    await waitFor(() => expect(s.patches()).toEqual([{ tz: NY }]));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Time zone" })).toHaveValue(NY));
    expect(screen.queryByRole("button", { name: OFFER })).toBeNull();
  });

  it("shows nothing once a zone is chosen, UTC on purpose included", async () => {
    deviceIn(NY);
    for (const chosen of ["Europe/Paris", "UTC"]) {
      server(chosen);
      go("/settings/statistics");
      expect(await screen.findByRole("textbox", { name: "Time zone" }, { timeout: 15000 })).toHaveValue(chosen);
      expect(screen.queryByRole("button", { name: /Use this device's time zone/ })).toBeNull();
      cleanup();
    }
  });

  it("shows nothing on a device whose zone is UTC", async () => {
    deviceIn("UTC");
    server("");
    go("/settings/statistics");
    expect(await screen.findByRole("textbox", { name: "Time zone" }, { timeout: 15000 })).toHaveValue("");
    expect(screen.queryByRole("button", { name: /Use this device's time zone/ })).toBeNull();
  });
});

describe("the Stats screen offers it once", () => {
  const line = () => screen.queryByText("Days are shown in UTC.");

  it("says days are in UTC, saves the device's zone from the button, and then the line is gone", async () => {
    deviceIn(NY);
    const s = server("");
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(line()).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: `${OFFER}?` }));
    await waitFor(() => expect(s.patches()).toEqual([{ tz: NY }]));
    await waitFor(() => expect(line()).toBeNull());
    expect(screen.queryByRole("button", { name: "Not now" })).toBeNull();
  });

  it("is already there when the charts first draw", async () => {
    deviceIn(NY);
    server("");
    go("/stats");
    // The heading is the first chart section; the line is drawn in the same pass as it, never after it.
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(line()).toBeInTheDocument();
  });

  it("remembers Not now on this device without saving anything", async () => {
    deviceIn(NY);
    const s = server("");
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    await userEvent.setup().click(screen.getByRole("button", { name: "Not now" }));
    expect(line()).toBeNull();
    expect(s.patches()).toEqual([]);
    expect(localStorage.getItem("kipple.zoneOfferDismissed.v1")).toBe("1");
    cleanup();
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(line()).toBeNull();
  });

  it("is not shown once a zone is chosen, UTC on purpose included", async () => {
    deviceIn(NY);
    for (const chosen of ["Europe/Paris", "UTC"]) {
      server(chosen);
      go("/stats");
      await screen.findByRole("heading", { name: "Daily activity" });
      expect(line(), chosen).toBeNull();
      cleanup();
    }
  });

  it("is not shown on a device whose zone is UTC", async () => {
    deviceIn("Etc/UTC");
    server("");
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(line()).toBeNull();
  });
});
