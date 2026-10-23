import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { offlineStore, setBrowserOnline } from "@/lib/offlineState";
import { json, mockFetch } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  setBrowserOnline(true);
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
  vi.stubGlobal("EventSource", NoES);
  window.history.replaceState({ idx: 0 }, "", "/l/unread");
});
afterEach(() => {
  vi.unstubAllGlobals();
  setBrowserOnline(true);
  document.body.innerHTML = "";
});

const refuse = () => {
  throw new TypeError("Failed to fetch");
};

describe("the launch failure screen names its cause", () => {
  it("says Kipple can't be reached, not offline, when the browser has a network and the request is refused", async () => {
    mockFetch({ "GET /api/bootstrap": refuse });
    render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("heading", { level: 1, name: "Kipple can't be reached" })).toBeInTheDocument();
    // The failed request marked the app offline in the store (any failed fetch does): the screen must not read that as "no network".
    expect(offlineStore.get().online).toBe(false);
    expect(screen.queryByRole("heading", { name: "You're offline" })).toBeNull();
  });

  it("says You're offline when the browser itself has no network", async () => {
    setBrowserOnline(false);
    mockFetch({ "GET /api/bootstrap": refuse });
    render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("heading", { level: 1, name: "You're offline" })).toBeInTheDocument();
  });

  it("calls a 502 from a proxy in front of a stopped Kipple unreachable, and Kipple's own 500 a server problem", async () => {
    mockFetch({ "GET /api/bootstrap": () => new Response("<html>Bad gateway</html>", { status: 502, headers: { "Content-Type": "text/html" } }) });
    const first = render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("heading", { level: 1, name: "Kipple can't be reached" })).toBeInTheDocument();
    first.unmount();

    authStore.set("unknown");
    mockFetch({ "GET /api/bootstrap": () => json({ error: "internal" }, 500) });
    render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("heading", { level: 1, name: "Kipple's server had a problem" })).toBeInTheDocument();
  });
});

describe("the launch failure screen asks again quietly", () => {
  it("stays mounted, with its alert and focus, while it tries again and says so", async () => {
    let release!: () => void;
    let calls = 0;
    mockFetch({
      "GET /api/bootstrap": () => {
        calls++;
        // The launch's own two retries (lib/offline.ts retryRead) fail too; the screen's automatic try is the fourth call.
        if (calls <= 3) throw new TypeError("Failed to fetch");
        // The slow try is slow: the screen shows it while it waits.
        return new Promise<Response>((resolve) => {
          release = () => resolve(new Response("<html>Bad gateway</html>", { status: 502 }));
        });
      },
    });
    render(<App client={makeQueryClient({ retry: false })} />);
    const alert = await screen.findByRole("alert");
    const button = screen.getByRole("button", { name: "Try again" });
    button.focus();
    expect(document.activeElement).toBe(button);

    await waitFor(() => expect(calls).toBe(4), { timeout: 8000 });
    // The query is pending again: the failed screen is still the one on screen, not "Loading Kipple".
    expect(screen.queryByText("Loading Kipple")).toBeNull();
    expect(alert.isConnected).toBe(true);
    expect(screen.getByRole("alert")).toBe(alert);
    expect(await screen.findByText("Trying again")).toBeInTheDocument();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Try again" }));

    release();
    await waitFor(() => expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Kipple can't be reached"));
    expect(screen.getByRole("alert")).toBe(alert);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Try again" }));
  });
});
