import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

// A request that answers 401 is logged to the console by the browser, so a signed-out launch must not send one:
// the app asks GET /api/instance (always 200) whether it is signed in before it asks for the bootstrap.

class NoES {
  addEventListener() {}
  close() {}
}

function launch() {
  window.history.replaceState({ idx: 0 }, "", "/");
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const paths = (calls: { url: URL }[]) => calls.map((c) => c.url.pathname);

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => vi.unstubAllGlobals());

describe("the signed-in probe", () => {
  it("a signed-out launch shows the sign-in form and never requests anything that answers 401", async () => {
    const { calls } = mockFetch({
      "GET /api/instance": () => json({ setup: false, auth: "password", signed_in: false }),
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
    });
    launch();
    await screen.findByRole("button", { name: "Sign in" });
    expect(paths(calls)).not.toContain("/api/bootstrap");
    expect(authStore.get()).toBe("out");
  });

  it("a signed-out launch with no account yet shows setup, also without a 401", async () => {
    const { calls } = mockFetch({
      "GET /api/instance": () => json({ setup: true, auth: null, signed_in: false, access: { enabled: false, verified: false }, open: { reason: null } }),
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
    });
    launch();
    await screen.findByRole("heading", { level: 1, name: /create your account/i });
    expect(paths(calls)).not.toContain("/api/bootstrap");
  });

  it("a signed-in launch asks for the bootstrap after the probe", async () => {
    const { calls } = mockFetch({
      "GET /api/instance": () => json({ setup: false, auth: "password", signed_in: true }),
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    launch();
    await waitFor(() => expect(authStore.get()).toBe("in"));
    await waitFor(() => expect(paths(calls)).toContain("/api/bootstrap"));
    expect(paths(calls).indexOf("/api/instance")).toBeLessThan(paths(calls).indexOf("/api/bootstrap"));
  });

  it("when the probe cannot be answered (offline) the bootstrap decides, as it always did", async () => {
    const { calls } = mockFetch({
      "GET /api/instance": () => {
        throw new TypeError("Failed to fetch");
      },
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    launch();
    await waitFor(() => expect(paths(calls)).toContain("/api/bootstrap"));
    await waitFor(() => expect(authStore.get()).toBe("in"));
  });
});
