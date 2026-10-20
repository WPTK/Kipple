import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { api, ApiError, authStore, sessionLostStore, setSignedIn, signingOutStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  authStore.set("unknown");
  signingOutStore.set(false);
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("sign-out", () => {
  it("waits for the device's offline copies to be deleted before showing the sign-in screen", async () => {
    // After the logout the server answers 401 to everything that needs a session, as the real one does.
    let ended = false;
    const signedIn = (body: unknown) => () => (ended ? json({ error: "auth" }, 401) : json(body));
    const { calls } = mockFetch({
      "GET /api/bootstrap": signedIn(bootstrap),
      "GET /api/items": signedIn(pageOf([card(1)])),
      "GET /api/settings": signedIn({ settings: [], values: {} }),
      "GET /api/filters": signedIn({ filters: [] }),
      "GET /api/devices": signedIn({ devices: [] }),
      "POST /api/auth/logout": () => {
        ended = true;
        return new Response(null, { status: 204 });
      },
      "GET /api/late": () => json({ error: "auth" }, 401),
      "GET /api/instance": () => json({ setup: false, auth: "password", signed_in: false, open_refused: null }),
    });
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    const deleted: string[] = [];
    vi.stubGlobal("caches", {
      delete: async (name: string) => {
        await held;
        deleted.push(name);
        return true;
      },
    });
    window.history.replaceState({ idx: 0 }, "", "/settings/account");
    render(<App client={makeQueryClient({ retry: false })} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Sign out" }));
    await new Promise((r) => setTimeout(r, 20));
    expect(authStore.get()).toBe("in"); // still wiping
    const sinceLogout = () => calls.slice(calls.findIndex((c) => c.url.pathname === "/api/auth/logout") + 1).map((c) => c.url.pathname);
    // A request that was already on its way answers 401 now that the server has ended the session: that is the
    // sign-out you chose, not a lost session.
    await expect(api("/api/late")).rejects.toBeInstanceOf(ApiError);
    expect(sessionLostStore.get()).toBe(false);
    release();
    await waitFor(() => expect(authStore.get()).toBe("out"));
    await screen.findByRole("button", { name: "Sign in" });
    // Nothing that needs the ended session was asked for (#549): the cache clearing used to send the bootstrap again.
    expect(sinceLogout().filter((p) => p !== "/api/late" && p !== "/api/instance")).toEqual([]);
    expect(deleted.sort()).toEqual(["kipple-data", "kipple-images"]);
    expect(sessionLostStore.get()).toBe(false);
  });

  it("a sign-out the server could not complete says so, stays signed in and can be tried again", async () => {
    let logouts = 0;
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/settings": () => json({ settings: [], values: {} }),
      "GET /api/filters": () => json({ filters: [] }),
      "GET /api/devices": () => json({ devices: [] }),
      // The server keeps the session (and the cookie) when it cannot delete it.
      "POST /api/auth/logout": () => (++logouts === 1 ? json({ error: "internal" }, 500) : new Response(null, { status: 204 })),
    });
    vi.stubGlobal("caches", { delete: async () => true });
    window.history.replaceState({ idx: 0 }, "", "/settings/account");
    render(<App client={makeQueryClient({ retry: false })} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Sign out" }));
    expect(await screen.findByText("The server returned an error. Try again.")).toBeTruthy();
    expect(authStore.get()).toBe("in");
    expect(sessionLostStore.get()).toBe(false);
    // Still on the account screen, signed in: the same button tries again.
    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(authStore.get()).toBe("out"));
    expect(logouts).toBe(2);
  });

  it("any way of becoming signed in clears the lost-session note", () => {
    sessionLostStore.set(true);
    setSignedIn();
    expect(sessionLostStore.get()).toBe(false);
    expect(authStore.get()).toBe("in");
  });
});
