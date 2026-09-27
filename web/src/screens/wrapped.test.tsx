import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readFileSync } from "node:fs";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { StatsSummary } from "@/api/types";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import * as wshare from "@/lib/wrappedShare";
import { offStats } from "./stats.fixtures";

class NoES {
  addEventListener() {}
  close() {}
}

const daily = ["2026-03-01", "2026-03-02", "2026-03-03", "2026-05-04"].map((date) => ({ date, items_read: 3, active_seconds: 300 }));
const yearData: StatsSummary = {
  enabled: true, tz: "UTC", week_start: "sunday",
  range: { key: "all", from: "2026-01-01", to: "2026-09-27", days: 270 },
  first_event_date: "2025-06-01",
  totals: { items_read: 12, opens: 20, active_seconds: 5400, days_active: 4 },
  daily,
  streaks: { current: 0, longest: 3, longest_end: "2026-03-03" },
  heatmap: [{ weekday: 0, hour: 19, active_seconds: 900, opens: 5 }],
  behavior: { busiest_weekday: null, busiest_hour: null, avg_read_seconds: 150, longest_read: { item_id: "1", title: "Private Title", feed_title: "Secret Feed", seconds: 1320, date: "2026-03-01" } },
  sources: [{ feed_id: "1", feed_title: "Secret Feed", folder_id: null, folder_name: null, items_read: 12, opens: 12, active_seconds: 0, timed_seconds: 0, timed_items: 0, avg_read_seconds: null, bounce_rate: null, open_original_rate: null, tracked_opens: 0, bounces: 0, items_opened: 0, items_original: 0, stars: 0, subscribed: true }],
};

function setup(settings: Record<string, unknown> = {}, data: StatsSummary = yearData) {
  return mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, settings }),
    "GET /api/stats/summary": () => json(data),
    "GET /api/items": () => json({ items: [], next_cursor: null }),
  });
}
function go(path = "/stats/wrapped") {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
const calls = (m: ReturnType<typeof mockFetch>) => m.calls.filter((c) => c.url.pathname === "/api/stats/summary");

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date(2026, 8, 27, 12));
  localStorage.clear();
});
afterEach(() => {
  for (const k of ["share", "canShare", "clipboard"]) Reflect.deleteProperty(navigator, k);
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Wrapped screen", () => {
  it("asks for the current year through the summary from/to and shows each card", async () => {
    const m = setup();
    go();
    await screen.findByText("You read 12 items in 2026");
    const q = calls(m)[0]!.url.searchParams;
    expect([q.get("from"), q.get("to"), q.get("range")]).toEqual(["2026-01-01", "2026-09-27", null]);
    expect(screen.getByText("1.5 hours of active reading")).toBeInTheDocument();
    expect(screen.getByText("4 days with reading")).toBeInTheDocument();
    expect(screen.getByText("Longest streak: 3 days.")).toBeInTheDocument();
    expect(screen.getByText("Your busiest day was Sunday; your busiest hour 7 pm")).toBeInTheDocument();
    expect(screen.getByText("Busiest month: March")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Top sources" })).getByText("Secret Feed")).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Longest read" })).getByText("Private Title")).toBeInTheDocument();
    expect(screen.queryByText(/Only \d+ days? of reading/)).toBeNull();
  });

  it("offers the years from the first event and refetches the chosen one", async () => {
    const user = userEvent.setup();
    const m = setup();
    go();
    await screen.findByText("You read 12 items in 2026");
    const sel = screen.getByLabelText("Year") as HTMLSelectElement;
    expect([...sel.options].map((o) => o.value)).toEqual(["2026", "2025"]);
    await user.selectOptions(sel, "2025");
    await waitFor(() => expect(calls(m).some((c) => c.url.searchParams.get("to") === "2025-12-31")).toBe(true));
    expect(calls(m).find((c) => c.url.searchParams.get("to") === "2025-12-31")!.url.searchParams.get("from")).toBe("2025-01-01");
  });

  it("notes a young year gently and still shows what exists", async () => {
    setup({}, { ...yearData, first_event_date: "2026-09-25" });
    go();
    expect(await screen.findByText(/Only 3 days of reading in 2026 so far/)).toBeInTheDocument();
    expect(screen.getByText("You read 12 items in 2026")).toBeInTheDocument();
  });

  it("has an empty state per card", async () => {
    setup({}, { ...yearData, totals: { items_read: 0, opens: 0, active_seconds: 0, days_active: 0 }, daily: [], heatmap: [], behavior: undefined, sources: [] });
    go();
    expect(await screen.findByText("No items read in 2026.")).toBeInTheDocument();
    expect(screen.getByText("No reading time recorded in 2026.")).toBeInTheDocument();
    expect(screen.getByText("No month stands out yet.")).toBeInTheDocument();
    expect(screen.getByText(/Sources appear here/)).toBeInTheDocument();
    expect(screen.getByText("No timed reads yet.")).toBeInTheDocument();
  });

  it("says Wrapped is off, without fetching", async () => {
    const m = setup({ "stats.wrapped_enabled": false });
    go();
    expect(await screen.findByText(/Wrapped is off\./)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings > Statistics" })).toHaveAttribute("href", "/settings");
    expect(calls(m)).toHaveLength(0);
  });

  it("says Wrapped is off when statistics are off", async () => {
    setup({ "stats.enabled": false }, offStats);
    go();
    expect(await screen.findByText(/Wrapped is off\./)).toBeInTheDocument();
  });

  it("shows the Your year entry on Stats", async () => {
    setup();
    go("/stats");
    expect(await screen.findByRole("link", { name: /Your year/ })).toHaveAttribute("href", "/stats/wrapped");
  });

  it("hides the entry on Stats when Wrapped is off", async () => {
    setup({ "stats.wrapped_enabled": false });
    go("/stats");
    await screen.findByRole("heading", { name: "Daily activity" });
    expect(screen.queryByRole("link", { name: /Your year/ })).toBeNull();
  });
});

describe("Wrapped share", () => {
  async function open() {
    const user = userEvent.setup();
    setup();
    go();
    await screen.findByText("You read 12 items in 2026");
    await user.click(screen.getByRole("button", { name: "Share" }));
    return { user, dlg: await screen.findByRole("dialog") };
  }
  const withNav = (extra: Record<string, unknown>) => {
    for (const [k, v] of Object.entries(extra)) Object.defineProperty(navigator, k, { configurable: true, value: v });
  };

  it("previews aggregates only until each toggle is turned on", async () => {
    const { user, dlg } = await open();
    const prev = within(dlg).getByTestId("wrapped-preview");
    expect(prev.textContent).toContain("12");
    expect(prev.textContent).not.toContain("Secret Feed");
    expect(prev.textContent).not.toContain("Private Title");
    expect(within(dlg).getByRole("switch", { name: /Include my top sources/ })).not.toBeChecked();
    expect(within(dlg).getByRole("switch", { name: /Include my longest read/ })).not.toBeChecked();
    await user.click(within(dlg).getByRole("switch", { name: /Include my top sources/ }));
    expect(prev.textContent).toContain("Secret Feed");
    expect(prev.textContent).not.toContain("Private Title");
    await user.click(within(dlg).getByRole("switch", { name: /Include my longest read/ }));
    expect(prev.textContent).toContain("Private Title");
  });

  it("forgets the toggles when the dialog is opened again", async () => {
    const { user, dlg } = await open();
    await user.click(within(dlg).getByRole("switch", { name: /Include my top sources/ }));
    await user.click(within(dlg).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await user.click(screen.getByRole("button", { name: "Share" }));
    expect(within(await screen.findByRole("dialog")).getByRole("switch", { name: /Include my top sources/ })).not.toBeChecked();
  });

  it("without a share sheet offers Download image and Copy as text, never Share", async () => {
    vi.spyOn(wshare, "canRenderImage").mockReturnValue(true);
    const dl = vi.spyOn(wshare, "downloadWrappedImage").mockResolvedValue("downloaded");
    const { user, dlg } = await open();
    expect(within(dlg).queryByRole("button", { name: "Share" })).toBeNull();
    await user.click(within(dlg).getByRole("button", { name: "Download image" }));
    expect(dl).toHaveBeenCalledTimes(1);
  });

  it("copies the text summary to the clipboard", async () => {
    const { user, dlg } = await open(); // user-event installs its own clipboard
    await user.click(within(dlg).getByRole("button", { name: "Copy as text" }));
    const copied = await navigator.clipboard.readText();
    expect(copied).toContain("I read 12 items in 2026.");
    expect(copied).not.toContain("Secret Feed");
  });

  it("shares the image file when the sheet takes files", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    withNav({ share, canShare: () => true });
    vi.stubGlobal("CanvasRenderingContext2D", class {});
    const drawn = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ fillRect: drawn, fillText: drawn, beginPath() {}, moveTo() {}, arcTo() {}, closePath() {}, fill() {} } as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((cb) => cb(new Blob(["x"], { type: "image/png" })));
    const { user, dlg } = await open();
    await user.click(within(dlg).getByRole("button", { name: "Share" }));
    await waitFor(() => expect(share).toHaveBeenCalled());
    const arg = share.mock.calls[0]![0] as { files?: File[]; text: string };
    expect(arg.files?.[0]?.name).toBe("kipple-2026.png");
    expect(arg.text).not.toContain("Secret Feed");
    expect(drawn).toHaveBeenCalled();
  });

  it("shares text when files are refused", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    withNav({ share, canShare: () => false });
    const { user, dlg } = await open();
    await user.click(within(dlg).getByRole("button", { name: "Share" }));
    await waitFor(() => expect(share).toHaveBeenCalled());
    const arg = share.mock.calls[0]![0] as { files?: File[]; text: string };
    expect(arg.files).toBeUndefined();
    expect(arg.text).toContain("12 items");
  });

  it("treats a dismissed sheet as nothing and a failed one as a short message", async () => {
    const share = vi.fn().mockRejectedValueOnce(new DOMException("x", "AbortError")).mockRejectedValueOnce(new Error("boom"));
    withNav({ share });
    const { user, dlg } = await open();
    await user.click(within(dlg).getByRole("button", { name: "Share" }));
    await waitFor(() => expect(share).toHaveBeenCalledTimes(1));
    expect(within(dlg).queryByRole("alert")).toBeNull();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(within(dlg).getByRole("button", { name: "Share" }));
    expect(await within(dlg).findByRole("alert")).toHaveTextContent(/Couldn't share/);
  });
});

describe("reduced motion", () => {
  it("defines the card fade only for readers who have not asked for less motion", () => {
    const css = readFileSync("src/index.css", "utf8");
    const i = css.indexOf("animation: kp-wrapped-in");
    expect(i).toBeGreaterThan(0);
    const before = css.slice(0, i);
    expect(before.lastIndexOf("@media (prefers-reduced-motion: no-preference)")).toBeGreaterThan(before.lastIndexOf("@media (prefers-reduced-motion: reduce)"));
    expect(before.slice(before.lastIndexOf("@media (prefers-reduced-motion: no-preference)"))).not.toContain("}\n}");
  });
});
