import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import { usePatchSettings, type SettingMeta } from "@/api/admin";
import { keys } from "@/api/queries";
import type { Bootstrap } from "@/api/types";
import { DISMISSED_STORAGE_NAME, deviceZone, isUtcName, resetZoneOffer } from "@/lib/deviceZone";
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

/**
 * The server with the saved zone `tz` ("" is none chosen): the bootstrap, the settings list and a PATCH that saves.
 * `setLive` changes what the server holds without the app knowing (another device chose a zone); `refuse` makes the save fail
 * the way the server does for a zone its own time-zone data lacks.
 */
function server(tz: string, o: { refuse?: boolean } = {}) {
  let saved = tz;
  let live: string | null = null;
  const body = (v: string) => ({ settings: [tzMeta(v)], values: { tz: v } });
  const m = mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings: { tz: saved } }),
    "GET /api/settings": () => json(body(live ?? saved)),
    "PATCH /api/settings": (_u, init) => {
      if (o.refuse) return json({ keys: ["tz"], issues: [{ key: "tz", message: "Kipple does not know that time zone." }] }, 400);
      saved = JSON.parse(String(init!.body)).tz;
      return json(body(saved));
    },
    "GET /api/stats/summary": () => json(richStats),
    "GET /api/items": () => json({ items: [], next_cursor: null }),
  });
  const patches = () => m.calls.filter((c) => c.init?.method === "PATCH").map((c) => JSON.parse(String(c.init?.body)));
  return { patches, setLive: (v: string) => (live = v) };
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

  it("does not write over a zone another device chose meanwhile, and shows that zone", async () => {
    deviceIn(NY);
    const s = server("");
    go("/settings/statistics");
    await screen.findByRole("textbox", { name: "Time zone" }, { timeout: 15000 });
    const button = await screen.findByRole("button", { name: OFFER });
    s.setLive("Europe/Paris");
    await userEvent.setup().click(button);
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Time zone" })).toHaveValue("Europe/Paris"));
    expect(screen.queryByRole("button", { name: OFFER })).toBeNull();
    expect(s.patches()).toEqual([]);
  });

  it("says in words when the server does not know the zone, and the button stays", async () => {
    deviceIn(NY);
    const s = server("", { refuse: true });
    go("/settings/statistics");
    await userEvent.setup().click(await screen.findByRole("button", { name: OFFER }, { timeout: 15000 }));
    expect(await screen.findByText("Kipple does not know that time zone.")).toBeInTheDocument();
    expect(s.patches()).toEqual([{ tz: NY }]);
    expect(screen.getByRole("button", { name: OFFER })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Time zone" })).toHaveValue("");
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

  it("is drawn in the same pass as the first chart, never after it", async () => {
    deviceIn(NY);
    server("");
    const seen: string[] = [];
    const look = () => {
      if (!seen.includes("line") && line()) seen.push("line");
      if (!seen.includes("chart") && screen.queryByRole("heading", { name: "Daily activity" })) seen.push("chart");
    };
    const watch = new MutationObserver(look);
    watch.observe(document.body, { childList: true, subtree: true });
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    watch.disconnect();
    look();
    expect(seen).toEqual(["line", "chart"]);
  });

  it("does not write over a zone another device chose meanwhile, and the offer goes", async () => {
    deviceIn(NY);
    const s = server("");
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    s.setLive("Europe/Paris");
    await userEvent.setup().click(screen.getByRole("button", { name: `${OFFER}?` }));
    await waitFor(() => expect(line()).toBeNull());
    expect(s.patches()).toEqual([]);
  });

  it("says in words when the server does not know the zone, and the offer stays and can be declined", async () => {
    deviceIn(NY);
    const s = server("", { refuse: true });
    const user = userEvent.setup();
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    await user.click(screen.getByRole("button", { name: `${OFFER}?` }));
    expect(await screen.findByText("Kipple does not know that time zone.")).toBeInTheDocument();
    expect(s.patches()).toEqual([{ tz: NY }]);
    expect(line()).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Not now" }));
    expect(line()).toBeNull();
  });

  it("closes the offer when another tab says Not now", async () => {
    deviceIn(NY);
    server("");
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    localStorage.setItem(DISMISSED_STORAGE_NAME, "1"); // what the other tab wrote
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key: DISMISSED_STORAGE_NAME, newValue: "1" }));
    });
    await waitFor(() => expect(line()).toBeNull());
  });

  it("remembers Not now on this device without saving anything", async () => {
    deviceIn(NY);
    const s = server("");
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    await userEvent.setup().click(screen.getByRole("button", { name: "Not now" }));
    expect(line()).toBeNull();
    expect(s.patches()).toEqual([]);
    expect(localStorage.getItem(DISMISSED_STORAGE_NAME)).toBe("1");
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

describe("saving a setting", () => {
  it("is not undone by a bootstrap answer asked for before the save", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    qc.setQueryData<Bootstrap>(keys.bootstrap, { ...bootstrap, settings: { tz: "" } });
    let answer!: (b: Bootstrap) => void;
    const asked = qc.fetchQuery({ queryKey: keys.bootstrap, staleTime: 0, queryFn: () => new Promise<Bootstrap>((r) => (answer = r)) });
    asked.catch(() => {});
    mockFetch({ "PATCH /api/settings": () => json({ settings: [tzMeta(NY)], values: { tz: NY } }) });
    const { result } = renderHook(() => usePatchSettings(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ tz: NY });
    });
    answer({ ...bootstrap, settings: { tz: "" } }); // the old answer arrives after the save
    await act(async () => {
      await Promise.resolve();
    });
    expect(qc.getQueryData<Bootstrap>(keys.bootstrap)?.settings.tz).toBe(NY);
  });
});
