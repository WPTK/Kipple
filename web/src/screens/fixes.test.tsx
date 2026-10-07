import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import { act } from "@testing-library/react";
import { clearToasts, toast } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

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
  clearToasts();
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

describe("toasts", () => {
  it("announces through the persistent live regions and is not itself a live node", async () => {
    mockFetch({ "GET /api/bootstrap": () => json(bootstrap), "GET /api/items": () => json(pageOf([card(1)])) });
    go("/l/unread");
    await screen.findByText("Article number 1");
    act(() => void toast("Saved the thing"));
    expect(screen.getByTestId("live-region")).toHaveTextContent("Saved the thing");
    act(() => void toast("Broke the thing", "error"));
    expect(screen.getByTestId("alert-region")).toHaveTextContent("Broke the thing");
    const visible = screen.getAllByText("Saved the thing").find((n) => n.closest("[data-kind]"));
    expect(visible?.closest("[role]")?.getAttribute("role") ?? null).not.toBe("alert");
    expect(visible?.closest("[data-kind]")?.hasAttribute("role")).toBe(false);
    expect(screen.getByTestId("toast-region")).toHaveAttribute("data-inset", "tabbar");
  });

  it("sits above the article toolbar, not the hidden tab bar, in the article view on a phone", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/items/1001": () => json(detail(1)),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    });
    go("/i/1001");
    await screen.findByRole("toolbar", { name: "Article actions" });
    expect(screen.getByTestId("toast-region")).toHaveAttribute("data-inset", "toolbar");
  });
});

describe("row collapse in the Unread view", () => {
  it("marks the row as leaving, then removes it after the collapse", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1), card(2)])),
      "POST /api/items/mark-read": () => json({ changed: ["1001", "1002"], restored: [], count: 2, undoable: true }),
    });
    const { container } = go("/l/unread");
    await screen.findByText("Article number 1");
    // Mark-all hides at once: the row is "leaving" for only COLLAPSE_MS (180 ms). A real-time waitFor poll can miss that short
    // window when the machine is loaded (both timers fire in one starved slice), so the clock is faked from here
    // and stepped by hand: 50 ms steps can never skip over the 180 ms leaving state.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const step = async (ms: number) => {
        await act(async () => {
          vi.advanceTimersByTime(ms);
          await Promise.resolve();
        });
      };
      const leaving = () => container.querySelector('.kp-row[data-leaving="true"]');
      // fireEvent, not userEvent: user-event's async wrapper waits on a real setTimeout, which is faked here.
      await act(async () => {
        fireEvent.keyDown(document.body, { key: "A", code: "KeyA", shiftKey: true });
      });
      let waited = 0;
      while (!leaving() && waited < 5000) {
        await step(50);
        waited += 50;
        await new Promise<void>((r) => setImmediate(r));
      }
      expect(leaving()).not.toBeNull();
      expect(screen.getByText("Article number 1")).toBeInTheDocument();
      await step(200);
      expect(screen.queryByText("Article number 1")).toBeNull();
      expect(screen.queryByText("Article number 2")).toBeNull();
      expect(leaving()).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("selection when the selected row leaves the Unread list", () => {
  it("moves to the next row, so j carries on from there instead of jumping to the top", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1), card(2), card(3)])),
      "POST /api/items/mark-read": (_u, init) => json({ changed: JSON.parse(String(init?.body)).ids, restored: [] }),
    });
    const { container } = go("/l/unread");
    await screen.findByText("Article number 1");
    const selected = () => container.querySelector("[data-item-id][data-selected]")?.getAttribute("data-item-id");
    act(() => void fireEvent.keyDown(document.body, { key: "j" }));
    await waitFor(() => expect(selected()).toBe("1001"));
    act(() => void fireEvent.keyDown(document.body, { key: "m" }));
    // The row leaves after LEAVE_MS and the collapse; the selection moves to the row that took its place.
    await waitFor(() => expect(screen.queryByText("Article number 1")).toBeNull());
    await waitFor(() => expect(selected()).toBe("1002"));
    act(() => void fireEvent.keyDown(document.body, { key: "j" }));
    await waitFor(() => expect(selected()).toBe("1003"));
    act(() => void fireEvent.keyDown(document.body, { key: "k" }));
    await waitFor(() => expect(selected()).toBe("1002"));
  }, 10000);
});

describe("previous and next feed controls", () => {
  it("shows labelled buttons with key hints on a feed list and navigates", async () => {
    const two = {
      ...bootstrap,
      feeds: [bootstrap.feeds[0]!, { ...bootstrap.feeds[0]!, id: "2", title: "Second" }],
    };
    mockFetch({ "GET /api/bootstrap": () => json(two), "GET /api/items": () => json(pageOf([card(1)])) });
    go("/l/unread?feed=1");
    const next = await screen.findByRole("button", { name: "Next feed" });
    expect(next).toHaveAttribute("title", expect.stringContaining("]"));
    expect(screen.getByRole("button", { name: "Previous feed" })).toBeDisabled();
    await userEvent.setup().click(next);
    await waitFor(() => expect(window.location.search).toContain("feed=2"));
  });
});
