import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { readFileSync } from "node:fs";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { StatsSummary } from "@/api/types";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { offStats } from "./stats.fixtures";
import { WrappedScreen } from "./WrappedScreen";

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
  behavior: { busiest_weekday: { weekday: 0, active_seconds: 900, opens: 5 }, busiest_hour: { hour: 19, active_seconds: 900, opens: 5 }, avg_read_seconds: 150, longest_read: { item_id: "1", title: "Private Title", feed_title: "Secret Feed", seconds: 1320, date: "2026-03-01" } },
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

  it("shows a loading state, not the off message, while the bootstrap answer is still unknown", () => {
    // Mounted directly (not through the App gate, which itself waits on the bootstrap query) so the bootstrap
    // answer is genuinely absent from the cache — the exact state a direct load or reload of /stats/wrapped can be
    // in for a moment. "Off" is a specific setting value, not "we don't know yet", and must never be shown for it.
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/stats/wrapped"]}>
          <WrappedScreen />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(screen.getByText("Loading your year")).toBeInTheDocument();
    expect(screen.queryByText(/Wrapped is off/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Share" })).toBeNull();
  });

  it("does not derive a model or enable Share from a previous year's cached data while the new year is loading", async () => {
    const user = userEvent.setup();
    let resolve2025!: (r: Response) => void;
    const pending2025 = new Promise<Response>((res) => {
      resolve2025 = res;
    });
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/stats/summary": (url) => (url.searchParams.get("to") === "2025-12-31" ? pending2025 : json(yearData)),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    go();
    await screen.findByText("You read 12 items in 2026");
    const sel = screen.getByLabelText("Year") as HTMLSelectElement;
    await user.selectOptions(sel, "2025");
    await screen.findByText("Loading your year");
    expect(screen.queryByText("You read 12 items in 2026")).toBeNull();
    expect(screen.getByRole("button", { name: "Share" })).toBeDisabled();
    resolve2025(
      json({
        ...yearData,
        range: { key: "all", from: "2025-01-01", to: "2025-12-31", days: 365 },
        totals: { items_read: 3, opens: 5, active_seconds: 900, days_active: 2 },
      }),
    );
    await screen.findByText("You read 3 items in 2025");
    expect(screen.getByRole("button", { name: "Share" })).not.toBeDisabled();
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
  /** Makes canRenderImage() genuinely true (both the copy WrappedScreen reads and the one renderCardPng itself
   * calls), by stubbing the canvas primitives rather than the exported function, so the pre-render effect really
   * produces a blob. `onToBlob`, if given, replaces the default (immediate) toBlob so a test can hold the render
   * open to inspect the "Preparing…" state. */
  function stubCanvas(onToBlob?: (cb: (b: Blob | null) => void) => void) {
    vi.stubGlobal("CanvasRenderingContext2D", class {});
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({
      fillRect() {},
      fillText() {},
      beginPath() {},
      moveTo() {},
      arcTo() {},
      closePath() {},
      fill() {},
    } as unknown as CanvasRenderingContext2D);
    vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation(onToBlob ?? ((cb) => cb(new Blob(["x"], { type: "image/png" }))));
  }
  const ready = async (dlg: HTMLElement, name: string) => waitFor(() => expect(within(dlg).getByRole("button", { name })).not.toBeDisabled());

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

  it("shows Preparing… and disables the image actions until the card has actually rendered", async () => {
    let finish!: (b: Blob | null) => void;
    stubCanvas((cb) => {
      finish = cb;
    });
    const { dlg } = await open();
    expect(within(dlg).getByRole("status", { name: "" })).toHaveTextContent("Preparing…");
    expect(within(dlg).getByRole("button", { name: "Download image" })).toBeDisabled();
    expect(within(dlg).getByRole("button", { name: "Copy as text" })).not.toBeDisabled();
    finish(new Blob(["x"], { type: "image/png" }));
    await waitFor(() => expect(within(dlg).queryByText("Preparing…")).toBeNull());
    await ready(dlg, "Download image");
  });

  it("shows Download image whenever an image can be rendered, with or without native share", async () => {
    stubCanvas();
    const { dlg } = await open();
    expect(within(dlg).queryByRole("button", { name: "Share" })).toBeNull();
    await ready(dlg, "Download image");
    expect(within(dlg).getByRole("button", { name: "Copy as text" })).toBeInTheDocument();
  });

  it("shows Download image alongside Share when the device has native share but no file support", async () => {
    stubCanvas();
    withNav({ share: vi.fn(), canShare: undefined });
    const { dlg } = await open();
    await ready(dlg, "Download image");
    expect(within(dlg).getByRole("button", { name: "Share" })).toBeInTheDocument();
    expect(within(dlg).getByRole("button", { name: "Download image" })).toBeInTheDocument();
  });

  it("downloads the pre-rendered image without re-rendering on click", async () => {
    stubCanvas();
    const toBlob = vi.mocked(HTMLCanvasElement.prototype.toBlob);
    const createUrl = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:x");
    const click = vi.fn();
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(click);
    const { user, dlg } = await open();
    await ready(dlg, "Download image");
    const calls = toBlob.mock.calls.length;
    await user.click(within(dlg).getByRole("button", { name: "Download image" }));
    expect(toBlob).toHaveBeenCalledTimes(calls); // acted on the blob already in memory, not a fresh render
    expect(createUrl).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
  });

  it("copies the text summary to the clipboard", async () => {
    const { user, dlg } = await open(); // user-event installs its own clipboard
    await user.click(within(dlg).getByRole("button", { name: "Copy as text" }));
    const copied = await navigator.clipboard.readText();
    expect(copied).toContain("I read 12 items in 2026.");
    expect(copied).not.toContain("Secret Feed");
  });

  it("shows the full text to copy by hand when the clipboard refuses", async () => {
    const { user, dlg } = await open(); // user-event installs its own clipboard first
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: () => Promise.reject(new Error("no")) } });
    await user.click(within(dlg).getByRole("button", { name: "Copy as text" }));
    expect(await within(dlg).findByRole("alert")).toHaveTextContent(/Couldn't copy/);
    const box = within(dlg).getByRole("textbox", { name: /Your year, as text/i }) as HTMLTextAreaElement;
    expect(box.value).toContain("I read 12 items in 2026.");
  });

  it("shares the image file when the sheet takes files", async () => {
    const share = vi.fn().mockResolvedValue(undefined);
    withNav({ share, canShare: () => true });
    stubCanvas();
    const { user, dlg } = await open();
    await ready(dlg, "Share");
    await user.click(within(dlg).getByRole("button", { name: "Share" }));
    await waitFor(() => expect(share).toHaveBeenCalled());
    const arg = share.mock.calls[0]![0] as { files?: File[]; text: string };
    expect(arg.files?.[0]?.name).toBe("kipple-2026.png");
    expect(arg.text).not.toContain("Secret Feed");
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

  it("retries as text-only when the file share itself fails for a reason other than a cancel", async () => {
    const share = vi.fn().mockRejectedValueOnce(new DOMException("nope", "NotAllowedError")).mockResolvedValueOnce(undefined);
    withNav({ share, canShare: () => true });
    stubCanvas();
    const { user, dlg } = await open();
    await ready(dlg, "Share");
    await user.click(within(dlg).getByRole("button", { name: "Share" }));
    await waitFor(() => expect(share).toHaveBeenCalledTimes(2));
    const second = share.mock.calls[1]![0] as { files?: File[]; text: string };
    expect(second.files).toBeUndefined();
    expect(second.text).toContain("12 items");
    // Succeeded on the retry, so the dialog closes and no failure is reported.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
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

  it("lets the in-app 'always animate' override force the fade even under reduced motion, like the rest of the app", () => {
    const css = readFileSync("src/index.css", "utf8");
    // Same convention as .kp-row and .swipe-content above it: data-motion="off" means "always animate".
    expect(css).toMatch(/:root\[data-motion="off"\]\s*\.wrapped-card\s*\{\s*animation: kp-wrapped-in/);
  });
});
