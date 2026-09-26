import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { Suspense } from "react";
import { MemoryRouter, useLocation, useNavigate } from "react-router";
import { lazyScreen, resetLazyScreensForTests, retryFailedScreens } from "@/lib/lazyScreen";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { ErrorBoundary, RoutedErrorBoundary } from "./ErrorBoundary";

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

  it("clears itself when the address changes", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    function Child({ path }: { path: string }) {
      if (path === "/bad") throw new Error("render failed");
      return <p>Page {path}</p>;
    }
    const { rerender } = render(
      <ErrorBoundary resetKey="/bad">
        <Child path="/bad" />
      </ErrorBoundary>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Something went wrong");
    rerender(
      <ErrorBoundary resetKey="/feeds">
        <Child path="/feeds" />
      </ErrorBoundary>,
    );
    expect(screen.getByText("Page /feeds")).toBeInTheDocument();
  });

  it("the routed boundary clears itself on navigation (the browser's Back, say)", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    function Page() {
      const { pathname } = useLocation();
      if (pathname === "/bad") throw new Error("render failed");
      return <p>Page {pathname}</p>;
    }
    function Back() {
      const navigate = useNavigate();
      return <button onClick={() => void navigate("/l/unread")}>Back</button>;
    }
    render(
      <MemoryRouter initialEntries={["/bad"]}>
        <Back />
        <RoutedErrorBoundary>
          <Page />
        </RoutedErrorBoundary>
      </MemoryRouter>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Something went wrong");
    await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByText("Page /l/unread")).toBeInTheDocument();
  });

  describe("a screen that could not be downloaded", () => {
    beforeEach(() => resetLazyScreensForTests());

    it("Try again downloads it again instead of repeating the remembered failure", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      let fail = true;
      const load = vi.fn(async () => {
        if (fail) throw new TypeError("Failed to fetch dynamically imported module");
        return { default: () => <p>Settings loaded</p> };
      });
      const Screen = lazyScreen(load);
      render(
        <ErrorBoundary>
          <Suspense fallback={<p>Loading</p>}>
            <Screen />
          </Suspense>
        </ErrorBoundary>,
      );
      expect(await screen.findByRole("alert")).toHaveTextContent("Something went wrong");
      fail = false;
      await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
      expect(await screen.findByText("Settings loaded")).toBeInTheDocument();
      expect(load).toHaveBeenCalledTimes(2);
    });

    it("when the second download fails too, the next Try again reloads the app", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      const load = vi.fn(async (): Promise<{ default: () => null }> => {
        throw new TypeError("Failed to fetch dynamically imported module");
      });
      const Screen = lazyScreen(load);
      render(
        <ErrorBoundary>
          <Suspense fallback={<p>Loading</p>}>
            <Screen />
          </Suspense>
        </ErrorBoundary>,
      );
      await userEvent.setup().click(await screen.findByRole("button", { name: "Try again" }));
      await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
      expect(await screen.findByRole("alert")).toHaveTextContent("Something went wrong");
      const reload = vi.fn();
      retryFailedScreens(reload); // what the button does next
      expect(reload).toHaveBeenCalledTimes(1);
    });
  });
});
