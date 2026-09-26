import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { ErrorBoundary } from "./ErrorBoundary";

class NoES {
  addEventListener() {}
  close() {}
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

describe("a malformed address", () => {
  it("an article opened with a broken ?from= still shows, from the default list", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/items/1001": () => json(detail(1)),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    });
    // `%25` arrives as a lone `%` in the scope key's search part, which decodeURIComponent refuses.
    window.history.replaceState({ idx: 0 }, "", "/i/1001?from=all|q:%25");
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<App client={makeQueryClient({ retry: false })} />);
    expect((await screen.findAllByText("Article number 1")).length).toBeGreaterThan(0);
    expect(screen.queryByText("Something went wrong")).toBeNull();
  });
});

describe("<ErrorBoundary />", () => {
  it("shows a message with a way out instead of a blank page, and Try again re-renders", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    let boom = true;
    function Child() {
      if (boom) throw new Error("render failed");
      return <p>Back again</p>;
    }
    render(
      <ErrorBoundary>
        <Child />
      </ErrorBoundary>,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Something went wrong");
    expect(alert).toHaveFocus();
    expect(screen.getByRole("button", { name: "Go to Unread" })).toBeInTheDocument();
    boom = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByText("Back again")).toBeInTheDocument();
  });
});
