import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import { todayString } from "@/lib/statsFormat";
import { bootstrap, json, mockFetch } from "@/test/mockApi";
import { StatsDataSection, StatsExportDialog } from "./StatsDataDialogs";

const toasts = vi.hoisted(() => ({ toast: vi.fn(), announce: vi.fn() }));
vi.mock("@/shell/toasts", async (orig) => ({ ...(await orig<typeof import("@/shell/toasts")>()), ...toasts }));

class NoES {
  addEventListener() {}
  close() {}
}

let client: QueryClient;
const QKEY = "kipple.stats.q.1";
const queue = () => localStorage.setItem(QKEY, JSON.stringify({ at: Date.now(), client: "web", events: [{ kind: "share", item_id: 1, event_id: "a" }] }));
const wrap = (ui: ReactNode) => render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);

beforeEach(() => {
  toasts.toast.mockClear();
  localStorage.clear();
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function openAll(user: ReturnType<typeof userEvent.setup>, phrase = "DELETE ALL") {
  await user.click(screen.getByRole("button", { name: "Delete all statistics…" }));
  await user.type(screen.getByLabelText(/Type DELETE ALL/), phrase);
}
async function openRange(user: ReturnType<typeof userEvent.setup>, from: string, to: string) {
  await user.click(screen.getByRole("button", { name: "Delete a date range…" }));
  await user.type(screen.getByLabelText("From"), from);
  await user.type(screen.getByLabelText("To"), to);
}

describe("what a delete does afterwards (run from the section)", () => {
  it("delete-all refreshes stats, clears this device's queue and announces", async () => {
    const user = userEvent.setup();
    queue();
    mockFetch({ "POST /api/stats/delete": () => json({ count: 3, deleted: 3 }) });
    const spy = vi.spyOn(client, "invalidateQueries");
    wrap(<StatsDataSection defaultRange="month" />);
    await openAll(user);
    await user.click(screen.getByRole("button", { name: "Delete all statistics" }));
    await waitFor(() => expect(toasts.toast).toHaveBeenCalledWith("Deleted 3 events"));
    expect(spy).toHaveBeenCalledWith({ queryKey: ["stats"] });
    expect(localStorage.getItem(QKEY)).toBeNull();
  });

  it("a range in the past leaves the queue alone; a range that includes today clears it", async () => {
    const user = userEvent.setup();
    queue();
    mockFetch({ "POST /api/stats/delete": (_u, init) => json({ count: 2, deleted: JSON.parse(String(init!.body)).dry_run ? 0 : 2 }) });
    wrap(<StatsDataSection defaultRange="month" />);
    await openRange(user, "2020-01-01", "2020-01-05");
    await screen.findByText("2 events will be deleted");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Yes, delete 2 events" }));
    await waitFor(() => expect(toasts.toast).toHaveBeenCalledWith("Deleted 2 events"));
    expect(localStorage.getItem(QKEY)).not.toBeNull();

    await openRange(user, "2020-01-01", todayString());
    await screen.findByText("2 events will be deleted");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Yes, delete 2 events" }));
    await waitFor(() => expect(localStorage.getItem(QKEY)).toBeNull());
  });

  it("uses the cached statistics day for 'today', not the browser's, when it is known", async () => {
    const user = userEvent.setup();
    queue();
    client.setQueryData(["stats", "month"], { enabled: true, tz: "Pacific/Auckland", week_start: "sunday", range: { key: "month", from: "2020-01-01", to: "2020-01-31", days: 31 } });
    // to <= browser today + 1 day, so it is trusted: a range ending 2020-01-05 does not include it
    mockFetch({ "POST /api/stats/delete": (_u, init) => json({ count: 1, deleted: JSON.parse(String(init!.body)).dry_run ? 0 : 1 }) });
    wrap(<StatsDataSection defaultRange="month" />);
    await openRange(user, "2020-01-01", "2020-01-05");
    await screen.findByText("1 event will be deleted");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: /Yes, delete/ }));
    await waitFor(() => expect(toasts.toast).toHaveBeenCalled());
    expect(localStorage.getItem(QKEY)).not.toBeNull();
  });
});

describe("partial failures", () => {
  it("delete-all shows the server message and the count already removed, and still refreshes", async () => {
    const user = userEvent.setup();
    queue();
    mockFetch({ "POST /api/stats/delete": () => json({ error: "unavailable", message: "The database was busy.", deleted: 5, complete: false }, 503) });
    const spy = vi.spyOn(client, "invalidateQueries");
    wrap(<StatsDataSection defaultRange="month" />);
    await openAll(user);
    await user.click(screen.getByRole("button", { name: "Delete all statistics" }));
    expect(await screen.findByText(/The database was busy\. Deleted 5 events before it stopped; run it again to finish\./)).toBeInTheDocument();
    expect(spy).toHaveBeenCalledWith({ queryKey: ["stats"] });
    expect(localStorage.getItem(QKEY)).toBeNull(); // 5 were removed
    expect(toasts.toast).not.toHaveBeenCalled();
  });

  it("a range delete that fails re-asks for the count and never keeps the stale one", async () => {
    const user = userEvent.setup();
    let dry = 0;
    mockFetch({
      "POST /api/stats/delete": (_u, init) => {
        if (JSON.parse(String(init!.body)).dry_run) return json({ count: dry++ === 0 ? 10 : 6, deleted: 0 });
        return json({ error: "internal", message: "Stopped early.", deleted: 4, complete: false }, 500);
      },
    });
    wrap(<StatsDataSection defaultRange="month" />);
    await openRange(user, "2020-01-01", "2020-01-05");
    await screen.findByText("10 events will be deleted");
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Yes, delete 10 events" }));
    expect(await screen.findByText(/Stopped early\. Deleted 4 events before it stopped/)).toBeInTheDocument();
    expect(await screen.findByText("6 events will be deleted")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled(); // back to the first step
  });
});

describe("a delete in flight", () => {
  it("cannot be closed over or submitted twice", async () => {
    const user = userEvent.setup();
    let release: (r: Response) => void = () => {};
    const m = mockFetch({ "POST /api/stats/delete": () => new Promise<Response>((r) => (release = r)) });
    wrap(<StatsDataSection defaultRange="month" />);
    await openAll(user);
    const btn = screen.getByRole("button", { name: "Delete all statistics" });
    await user.click(btn);
    const busy = await screen.findByRole("button", { name: "Deleting…" });
    expect(busy).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Delete all statistics" })).toBeInTheDocument();
    await user.click(busy);
    expect(m.calls.filter((c) => c.method === "POST")).toHaveLength(1);
    release(json({ count: 1, deleted: 1 }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(toasts.toast).toHaveBeenCalledTimes(1);
  });
});

describe("custom date seeding", () => {
  it("ignores a far-future end date from the cached All summary and any date past tomorrow", async () => {
    const user = userEvent.setup();
    const summary = (key: string, to: string) => ({ enabled: true, tz: "UTC", week_start: "sunday", range: { key, from: "2020-01-01", to, days: 1 } });
    client.setQueryData(["stats", "all"], summary("all", "2099-01-01"));
    client.setQueryData(["stats", "year"], summary("year", "2098-01-01"));
    wrap(<StatsExportDialog open onOpenChange={() => {}} defaultRange="week" />);
    await user.click(screen.getByRole("radio", { name: "Custom" }));
    expect(screen.getByLabelText("To")).toHaveValue(todayString());
  });
});

describe("typed phrase hint", () => {
  it("waits until the text is as long as the phrase, or the field is left", async () => {
    const user = userEvent.setup();
    wrap(<StatsDataSection defaultRange="month" />);
    await user.click(screen.getByRole("button", { name: "Delete all statistics…" }));
    const input = screen.getByLabelText(/Type DELETE ALL/);
    await user.type(input, "DELE");
    expect(screen.queryByText("Type DELETE ALL exactly.")).toBeNull();
    await user.tab();
    expect(screen.getByText("Type DELETE ALL exactly.")).toBeInTheDocument();
    await user.click(input);
    await user.type(input, "X");
    expect(screen.queryByText("Type DELETE ALL exactly.")).toBeNull();
    await user.clear(input);
    await user.type(input, "DELETE ALLL");
    expect(screen.getByText("Type DELETE ALL exactly.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete all statistics" })).toBeDisabled();
  });
});

describe("in the app", () => {
  function go(path: string) {
    window.history.replaceState({ idx: 0 }, "", path);
    return render(<App client={makeQueryClient({ retry: false })} />);
  }

  it("keeps an open dialog, with what was typed, when the settings request resolves", async () => {
    const user = userEvent.setup();
    let release: (r: Response) => void = () => {};
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/settings": () => new Promise<Response>((r) => (release = r)),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    go("/settings");
    await user.click(await screen.findByRole("button", { name: "Delete all statistics…" }));
    await user.type(screen.getByLabelText(/Type DELETE ALL/), "DELETE ALL");
    release(json({ settings: [] }));
    await waitFor(() => expect(screen.queryByText("Loading settings")).toBeNull());
    expect(screen.getByRole("dialog", { name: "Delete all statistics" })).toBeInTheDocument();
    expect(screen.getByLabelText(/Type DELETE ALL/)).toHaveValue("DELETE ALL");
    expect(screen.getByRole("button", { name: "Delete all statistics" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
  });

  it("keeps the status role on the off message alone, not around the buttons", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, settings: { "stats.enabled": false } }),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
    });
    go("/stats");
    const status = (await screen.findByText(/Statistics are off/)).closest("[role=status]") as HTMLElement;
    expect(status).not.toBeNull();
    expect(within(status).queryByRole("button")).toBeNull();
    expect(screen.getByRole("button", { name: "Export…" })).toBeInTheDocument();
  });
});
