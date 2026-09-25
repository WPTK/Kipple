import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("list paging errors", () => {
  it("keeps the list on a failed next page, shows an inline Retry and does not auto-retry", async () => {
    let second = 0;
    let fail = true;
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": (url) => {
        if (url.searchParams.get("cursor")) {
          second++;
          return fail ? json({ error: "down" }, 500) : json(pageOf([card(9)]));
        }
        return json(pageOf([card(1), card(2)], "c1"));
      },
    });
    go("/l/unread");
    await screen.findByText("Article number 1");
    await screen.findByText(/Couldn.t load more/);
    expect(screen.getByText("Article number 1")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load articles")).toBeNull();
    const seen = second;
    await new Promise((r) => setTimeout(r, 300));
    expect(second).toBe(seen);
    expect(calls.filter((c) => c.url.searchParams.get("cursor")).length).toBe(seen);

    fail = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("Article number 9");
    await waitFor(() => expect(screen.queryByText(/Couldn.t load more/)).toBeNull());
  });
});
