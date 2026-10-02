import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { exportUrl, rangeProblem } from "@/lib/statsExport";
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
  localStorage.clear();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    hrefs.push(this.getAttribute("href") ?? "");
  });
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function Harness({ which }: { which: "export" | "range" | "all" }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>Open</button>
      {which === "export" ? <StatsExportDialog open={open} onOpenChange={setOpen} defaultRange="month" /> : null}
      {which === "range" ? <DeleteRangeDialog open={open} onOpenChange={setOpen} onSettled={() => {}} /> : null}
      {which === "all" ? <DeleteAllDialog open={open} onOpenChange={setOpen} onSettled={() => {}} /> : null}
    </>
  );
}

type U = ReturnType<typeof userEvent.setup>;
const closers: [string, (u: U) => Promise<void>][] = [
  ["Cancel", (u) => u.click(screen.getByRole("button", { name: "Cancel" }))],
  ["Esc", (u) => u.keyboard("{Escape}")],
];
const gone = () => waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

describe("dialogs start clean on every opening", () => {
  for (const [name, close] of closers) {
    it(`delete range after ${name}: never pre-armed, dates empty, count fetched again`, async () => {
      const user = userEvent.setup();
      const m = mockFetch({ "POST /api/stats/delete": () => json({ count: 4, deleted: 0 }) });
      wrap(<Harness which="range" />);
      await user.click(screen.getByText("Open"));
      await user.type(screen.getByLabelText("From"), "2026-09-01");
      await user.type(screen.getByLabelText("To"), "2026-09-05");
      await screen.findByText("4 records will be deleted");
      await user.click(screen.getByRole("button", { name: "Delete" }));
      expect(screen.getByRole("button", { name: "Yes, delete 4 records" })).toBeEnabled();
      await close(user);
      await gone();
      const before = m.calls.length;
      await user.click(screen.getByText("Open"));
      expect(screen.getByLabelText("From")).toHaveValue("");
      expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
      await user.type(screen.getByLabelText("From"), "2026-09-01");
      await user.type(screen.getByLabelText("To"), "2026-09-05");
      await screen.findByText("4 records will be deleted");
      expect(m.calls.length).toBeGreaterThan(before);
      expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled();
      expect(screen.queryByRole("button", { name: /Yes, delete/ })).toBeNull();
    });

    it(`delete all after ${name}: typed phrase and error are gone`, async () => {
      const user = userEvent.setup();
      mockFetch({ "POST /api/stats/delete": () => json({ error: "boom" }, 500) });
      wrap(<Harness which="all" />);
      await user.click(screen.getByText("Open"));
      await user.type(screen.getByLabelText(/Type DELETE ALL/), "DELETE ALL");
      await user.click(screen.getByRole("button", { name: "Delete all statistics" }));
      await screen.findByText("The server returned an error. Try again.");
      await close(user);
      await gone();
      await user.click(screen.getByText("Open"));
      expect(screen.getByLabelText(/Type DELETE ALL/)).toHaveValue("");
      expect(screen.getByRole("button", { name: "Delete all statistics" })).toBeDisabled();
      expect(screen.queryByText("The server returned an error. Try again.")).toBeNull();
    });

    it(`export after ${name}: options reset`, async () => {
      const user = userEvent.setup();
      wrap(<Harness which="export" />);
      await user.click(screen.getByText("Open"));
      await user.click(screen.getByRole("radio", { name: "Summary" }));
      await user.click(screen.getByRole("radio", { name: "Custom" }));
      await close(user);
      await gone();
      await user.click(screen.getByText("Open"));
      expect(screen.getByRole("radio", { name: "Raw data" })).toBeChecked();
      expect(screen.getByRole("radio", { name: "Month" })).toBeChecked();
    });
  }

  it("after a successful range delete a reopened dialog is clean", async () => {
    const user = userEvent.setup();
    mockFetch({ "POST /api/stats/delete": (_u, init) => json({ count: 2, deleted: JSON.parse(String(init!.body)).dry_run ? 0 : 2 }) });
    wrap(<Harness which="range" />);
    await user.click(screen.getByText("Open"));
    await user.type(screen.getByLabelText("From"), "2026-09-01");
    await user.type(screen.getByLabelText("To"), "2026-09-05");
    await screen.findByText("2 records will be deleted");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Yes, delete 2 records" }));
    await gone();
    await user.click(screen.getByText("Open"));
    expect(screen.getByLabelText("From")).toHaveValue("");
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("after a successful delete-all a reopened dialog is clean", async () => {
    const user = userEvent.setup();
    mockFetch({ "POST /api/stats/delete": () => json({ count: 2, deleted: 2 }) });
    wrap(<Harness which="all" />);
    await user.click(screen.getByText("Open"));
    await user.type(screen.getByLabelText(/Type DELETE ALL/), "DELETE ALL");
    await user.click(screen.getByRole("button", { name: "Delete all statistics" }));
    await gone();
    await user.click(screen.getByText("Open"));
    expect(screen.getByLabelText(/Type DELETE ALL/)).toHaveValue("");
    expect(screen.getByRole("button", { name: "Delete all statistics" })).toBeDisabled();
  });
});

describe("second-review items", () => {
  it("caps a custom export range at 3660 days inclusive; delete has no cap", () => {
    expect(rangeProblem("2016-01-01", "2026-01-07", true)).toBeNull(); // 3659 days apart = 3660 inclusive
    expect(rangeProblem("2016-01-01", "2026-01-08", true)).toMatch(/at most 3,660 days/);
    expect(rangeProblem("2016-01-01", "2026-01-08")).toBeNull();
  });

  it("shows the cap message and the time zone note in the export dialog", async () => {
    const user = userEvent.setup();
    wrap(<StatsExportDialog open onOpenChange={() => {}} defaultRange="month" />);
    await user.click(screen.getByRole("radio", { name: "Custom" }));
    const from = screen.getByLabelText("From");
    await user.clear(from);
    await user.type(from, "2000-01-01");
    expect(await screen.findByText(/at most 3,660 days/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download" })).toBeDisabled();
    expect(screen.getByText(/statistics time zone/)).toBeInTheDocument();
  });

  it("delete-all trims the phrase, stays case-sensitive and hints on a mismatch", async () => {
    const user = userEvent.setup();
    wrap(<DeleteAllDialog open onOpenChange={() => {}} onSettled={() => {}} />);
    const input = screen.getByLabelText(/Type DELETE ALL/);
    expect(input).toHaveAttribute("autocorrect", "off");
    expect(input).toHaveAttribute("autocapitalize", "characters");
    expect(screen.queryByText("Type DELETE ALL exactly.")).toBeNull();
    await user.type(input, "delete all");
    expect(screen.getByText("Type DELETE ALL exactly.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete all statistics" })).toBeDisabled();
    await user.clear(input);
    await user.type(input, "DELETE ALL ");
    expect(screen.getByRole("button", { name: "Delete all statistics" })).toBeEnabled();
    expect(screen.queryByText("Type DELETE ALL exactly.")).toBeNull();
  });

  it("re-seeds custom dates on every opening from the statistics time zone's today", async () => {
    const user = userEvent.setup();
    client.setQueryData(["stats", "month"], { enabled: true, tz: "America/New_York", week_start: "sunday", range: { key: "month", from: "2026-08-14", to: "2026-09-12", days: 30 } });
    wrap(<Harness which="export" />);
    await user.click(screen.getByText("Open"));
    await user.click(screen.getByRole("radio", { name: "Custom" }));
    expect(screen.getByLabelText("To")).toHaveValue("2026-09-12");
    expect(screen.getByLabelText("From")).toHaveValue("2026-08-13");
    await user.clear(screen.getByLabelText("To"));
    await user.type(screen.getByLabelText("To"), "2026-09-13");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByText("Open"));
    await user.click(screen.getByRole("radio", { name: "Custom" }));
    expect(screen.getByLabelText("To")).toHaveValue("2026-09-12");
  });

  it("clears the unsent queue after a delete and notes late events", async () => {
    const user = userEvent.setup();
    wrap(<DeleteAllDialog open onOpenChange={() => {}} onSettled={() => {}} />);
    expect(screen.getByText(/that hasn.t synced yet can still show up later/)).toBeInTheDocument();
    await user.type(screen.getByLabelText(/Type DELETE ALL/), "x");
  });

  it("has the new titles help text and a CSV-only byte-order-mark switch", async () => {
    const user = userEvent.setup();
    wrap(<StatsExportDialog open onOpenChange={() => {}} defaultRange="week" />);
    expect(screen.getByText(/Feed and folder names, times and your time zone stay in./)).toBeInTheDocument();
    const bom = screen.getByRole("switch", { name: /byte-order mark/ });
    expect(bom).not.toBeChecked();
    await user.click(bom);
    await user.click(screen.getByRole("button", { name: "Download" }));
    expect(hrefs).toEqual(["/api/stats/export?format=csv&content=raw&range=week&titles=1&bom=1"]);
  });

  it("offers no byte-order mark for JSON or Summary, and never sends it there", async () => {
    const user = userEvent.setup();
    wrap(<StatsExportDialog open onOpenChange={() => {}} defaultRange="week" />);
    await user.click(screen.getByRole("switch", { name: /byte-order mark/ }));
    await user.click(screen.getByRole("radio", { name: "JSON" }));
    expect(screen.queryByRole("switch", { name: /byte-order mark/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Download" }));
    expect(hrefs).toEqual(["/api/stats/export?format=json&content=raw&range=week&titles=1"]);
    expect(exportUrl({ format: "csv", content: "summary", range: "all", from: "", to: "", titles: true, bom: true })).toBe("/api/stats/export?format=json&content=summary&range=all&titles=1");
  });
});
