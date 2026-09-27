import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { exportUrl, rangeProblem, type ExportContent, type ExportFormat, type ExportRange } from "@/lib/statsExport";
import { json, mockFetch } from "@/test/mockApi";
import { DeleteAllDialog, DeleteRangeDialog, StatsExportDialog } from "./StatsDataDialogs";

const toasts = vi.hoisted(() => ({ toast: vi.fn(), announce: vi.fn() }));
vi.mock("@/shell/toasts", () => toasts);

let hrefs: string[] = [];
let client: QueryClient;

function wrap(ui: ReactNode) {
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

beforeEach(() => {
  hrefs = [];
  toasts.toast.mockClear();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    hrefs.push(this.getAttribute("href") ?? "");
  });
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const base = { from: "2026-09-01", to: "2026-09-10", titles: true } as const;

describe("exportUrl", () => {
  const formats: ExportFormat[] = ["csv", "json", "jsonl"];
  const ranges: ExportRange[] = ["week", "month", "year", "all", "custom"];
  it("builds the exact URL for every combination", () => {
    for (const content of ["raw", "summary"] as ExportContent[])
      for (const format of formats)
        for (const range of ranges)
          for (const titles of [true, false]) {
            const url = exportUrl({ ...base, format, content, range, titles });
            const f = content === "summary" ? "json" : format;
            const r = range === "custom" ? "from=2026-09-01&to=2026-09-10" : `range=${range}`;
            expect(url).toBe(`/api/stats/export?format=${f}&content=${content}&${r}&titles=${titles ? 1 : 0}`);
          }
  });
  it("validates custom dates", () => {
    expect(rangeProblem("2026-09-01", "2026-09-01")).toBeNull();
    expect(rangeProblem("2026-09-02", "2026-09-01")).toMatch(/on or before/);
    expect(rangeProblem("", "2026-09-01")).toMatch(/both/);
    expect(rangeProblem("2026-02-30", "2026-03-01")).toMatch(/both/);
  });
});

describe("export dialog", () => {
  const open = (range: "week" | "month" | "year" | "all" = "month") => wrap(<StatsExportDialog open onOpenChange={() => {}} defaultRange={range} />);

  it("defaults to the screen's range and downloads with a plain navigation, then announces", async () => {
    const user = userEvent.setup();
    const close = vi.fn();
    wrap(<StatsExportDialog open onOpenChange={close} defaultRange="year" />);
    expect(screen.getByRole("radio", { name: "Year" })).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Download" }));
    expect(hrefs).toEqual(["/api/stats/export?format=csv&content=raw&range=year&titles=1"]);
    expect(close).toHaveBeenCalledWith(false);
    expect(toasts.toast).toHaveBeenCalledWith("Download started");
  });

  it("builds the URL from the chosen options", async () => {
    const user = userEvent.setup();
    open();
    await user.click(screen.getByRole("radio", { name: "JSON Lines" }));
    await user.click(screen.getByRole("radio", { name: "All" }));
    await user.click(screen.getByRole("switch", { name: /Include article titles/ }));
    await user.click(screen.getByRole("button", { name: "Download" }));
    expect(hrefs).toEqual(["/api/stats/export?format=jsonl&content=raw&range=all&titles=0"]);
  });

  it("Summary forces JSON and disables the other formats", async () => {
    const user = userEvent.setup();
    open();
    await user.click(screen.getByRole("radio", { name: "JSON Lines" }));
    await user.click(screen.getByRole("radio", { name: "Summary" }));
    expect(screen.getByRole("radio", { name: "CSV" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "JSON Lines" })).toBeDisabled();
    expect(screen.getByRole("radio", { name: "JSON" })).toBeChecked();
    expect(screen.getByText(/only available as JSON/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Download" }));
    expect(hrefs).toEqual(["/api/stats/export?format=json&content=summary&range=month&titles=1"]);
  });

  it("checks a custom range before it allows the download", async () => {
    const user = userEvent.setup();
    open();
    await user.click(screen.getByRole("radio", { name: "Custom" }));
    const from = screen.getByLabelText("From");
    const to = screen.getByLabelText("To");
    await user.clear(from);
    await user.type(from, "2026-09-10");
    await user.clear(to);
    await user.type(to, "2026-09-01");
    expect(await screen.findByText(/start date must be on or before/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download" })).toBeDisabled();
    await user.clear(to);
    await user.type(to, "2026-09-12");
    expect(screen.getByRole("button", { name: "Download" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Download" }));
    expect(hrefs).toEqual(["/api/stats/export?format=csv&content=raw&from=2026-09-10&to=2026-09-12&titles=1"]);
  });

  it("links the Markdown data dictionary", () => {
    open();
    const a = screen.getByRole("link", { name: "Download data dictionary" });
    expect(a).toHaveAttribute("href", "/api/stats/dictionary?format=md");
    expect(screen.getByText(/Describes every column/)).toBeInTheDocument();
  });
});

describe("delete a date range", () => {
  async function fill(user: ReturnType<typeof userEvent.setup>, from: string, to: string) {
    await user.type(screen.getByLabelText("From"), from);
    await user.type(screen.getByLabelText("To"), to);
  }

  it("counts with a dry run as the dates change and disables Delete at 0", async () => {
    const user = userEvent.setup();
    let n = 7;
    const m = mockFetch({ "POST /api/stats/delete": () => json({ count: n, deleted: 0 }) });
    wrap(<DeleteRangeDialog open onOpenChange={() => {}} onSettled={() => {}} />);
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
    await fill(user, "2026-09-01", "2026-09-05");
    expect(await screen.findByText("7 events will be deleted")).toBeInTheDocument();
    expect(JSON.parse(String(m.calls.at(-1)!.init!.body))).toEqual({ from: "2026-09-01", to: "2026-09-05", dry_run: true });
    expect((m.calls.at(-1)!.init!.headers as Record<string, string>)["X-Kipple-Client"]).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled();
    n = 0;
    await user.clear(screen.getByLabelText("To"));
    await user.type(screen.getByLabelText("To"), "2026-09-06");
    expect(await screen.findByText("No events in that range")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("asks a second time with the count, deletes, refreshes stats and announces", async () => {
    const user = userEvent.setup();
    const m = mockFetch({
      "POST /api/stats/delete": (_u, init) => {
        const b = JSON.parse(String(init!.body));
        return json({ count: 3, deleted: b.dry_run ? 0 : 3 });
      },
    });
    const settled = vi.fn();
    const close = vi.fn();
    wrap(<DeleteRangeDialog open onOpenChange={close} onSettled={settled} />);
    await fill(user, "2026-09-01", "2026-09-05");
    await screen.findByText("3 events will be deleted");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    expect(m.calls.filter((c) => JSON.parse(String(c.init!.body)).dry_run === false)).toHaveLength(0); // first click only asks
    await user.click(screen.getByRole("button", { name: "Yes, delete 3 events" }));
    await waitFor(() => expect(settled).toHaveBeenCalledWith({ scope: { from: "2026-09-01", to: "2026-09-05" }, deleted: 3, ok: true }));
    expect(JSON.parse(String(m.calls.at(-1)!.init!.body))).toEqual({ from: "2026-09-01", to: "2026-09-05", dry_run: false });
    expect(close).toHaveBeenCalledWith(false);
  });

  it("shows the server's message for a rejected range", async () => {
    const user = userEvent.setup();
    mockFetch({ "POST /api/stats/delete": () => json({ error: "bad_range", message: "That range is too large." }, 400) });
    wrap(<DeleteRangeDialog open onOpenChange={() => {}} onSettled={() => {}} />);
    await fill(user, "2026-09-01", "2026-09-05");
    expect(await screen.findByText("That range is too large.")).toBeInTheDocument();
  });
});

describe("delete all statistics", () => {
  it("needs DELETE ALL typed exactly, then posts the exact body", async () => {
    const user = userEvent.setup();
    const m = mockFetch({ "POST /api/stats/delete": () => json({ count: 12, deleted: 12 }) });
    const settled = vi.fn();
    wrap(<DeleteAllDialog open onOpenChange={() => {}} onSettled={settled} />);
    const btn = screen.getByRole("button", { name: "Delete all statistics" });
    expect(screen.getByText(/does not remove articles or read state/)).toBeInTheDocument();
    expect(btn).toBeDisabled();
    const input = screen.getByLabelText(/Type DELETE ALL/);
    await user.type(input, "delete all");
    expect(btn).toBeDisabled();
    await user.clear(input);
    await user.type(input, "DELETE ALL");
    expect(btn).toBeEnabled();
    await user.click(btn);
    await waitFor(() => expect(settled).toHaveBeenCalledWith({ scope: { all: true }, deleted: 12, ok: true }));
    expect(m.calls).toHaveLength(1);
    expect(JSON.parse(String(m.calls[0]!.init!.body))).toEqual({ all: true, confirm: "DELETE ALL" });
  });

  it("shows an error and keeps the dialog", async () => {
    const user = userEvent.setup();
    mockFetch({ "POST /api/stats/delete": () => json({ error: "boom" }, 500) });
    const close = vi.fn();
    wrap(<DeleteAllDialog open onOpenChange={close} onSettled={() => {}} />);
    await user.type(screen.getByLabelText(/Type DELETE ALL/), "DELETE ALL");
    await user.click(screen.getByRole("button", { name: "Delete all statistics" }));
    expect(await screen.findByText("The server returned an error. Try again.")).toBeInTheDocument();
    expect(close).not.toHaveBeenCalled();
  });
});
