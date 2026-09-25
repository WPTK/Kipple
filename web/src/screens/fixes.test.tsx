import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
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
