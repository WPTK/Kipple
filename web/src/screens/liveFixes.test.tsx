import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { handleServerEvent, initialLive, liveStore } from "@/api/events";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

const articleToCalls = vi.hoisted(() => ({ n: 0 }));
vi.mock("@/lib/routes", async (orig) => {
  const m = await orig<typeof import("@/lib/routes")>();
  return {
    ...m,
    articleTo: (...a: Parameters<typeof m.articleTo>) => {
      articleToCalls.n += 1;
      return m.articleTo(...a);
    },
  };
});

class NoES {
  addEventListener() {}
  close() {}
}

let client: ReturnType<typeof makeQueryClient>;
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  client = makeQueryClient({ retry: false });
  return render(<App client={client} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("live state in the list", () => {
  it("run progress and fetch ticks do not re-render the rows", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(pageOf([card(1), card(2), card(3)])) });
    go("/l/unread");
    await screen.findByText("Article number 3");
    await act(() => new Promise((r) => setTimeout(r, 50)));
    const before = articleToCalls.n;
    act(() => {
      handleServerEvent(client, { type: "run.start", data: { run_id: "1", kind: "manual", total: 20 } });
      for (let i = 1; i <= 20; i += 1) {
        handleServerEvent(client, { type: "run.progress", data: { run_id: "1", done: i, total: 20, new_items: 0, errors: 0 } });
        handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", outcome: "not_modified", new_items: 0 } });
      }
      handleServerEvent(client, { type: "fulltext.ready", data: { ids: ["1001"], source: "ingest" } });
    });
    expect(articleToCalls.n).toBe(before);
    act(() => handleServerEvent(client, { type: "run.done", data: { run_id: "1", new_items: 0, errors: 0 } }));
    expect(articleToCalls.n).toBe(before);
  });

  it("a run whose run.done was missed stops spinning once /api/status says nothing is running", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
    });
    go("/l/unread");
    await screen.findByText("Article number 1");
    act(() => handleServerEvent(client, { type: "run.start", data: { run_id: "5", kind: "manual", total: 3 } }));
    expect(await screen.findByRole("button", { name: "Refreshing" })).toBeInTheDocument();
    const { pollStatus } = await import("@/api/events");
    await act(async () => void (await pollStatus(client)));
    await screen.findByRole("button", { name: "Refresh all feeds" });
  });

  it("a retention run does not spin the refresh icon", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(pageOf([card(1)])) });
    go("/l/unread");
    await screen.findByText("Article number 1");
    act(() => handleServerEvent(client, { type: "run.start", data: { run_id: "8", kind: "retention", total: 1 } }));
    expect(screen.getByRole("button", { name: "Refresh all feeds" })).toBeInTheDocument();
  });

  it("the n new pill skips articles the list already has", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1), card(2)])),
    });
    go("/l/unread");
    await screen.findByText("Article number 1");
    // Two arrivals, but one of them is already in the loaded list (id 1001).
    act(() => handleServerEvent(client, { type: "fetch.done", data: { feed_id: "1", outcome: "ok", new_items: 2, new_item_ids: ["1001", "9999"], trigger: "feed_manual" } }));
    expect(await screen.findByRole("button", { name: "1 new article" })).toBeInTheDocument();
    // Both already present: no pill.
    act(() => liveStore.set((s) => ({ ...s, pendingByFeed: { "1": 2 }, pendingIds: { "1": ["1001", "1002"] } })));
    await waitFor(() => expect(screen.queryByRole("button", { name: /new article/ })).toBeNull());
  });
});
