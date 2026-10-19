import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore, setSignedIn } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { INSTANCE_KEY } from "@/setup/api";
import { bootstrap, card, json, mockFetch, pageOf, setSessionMarker } from "@/test/mockApi";
import { hasSessionMarker, PROBE_MS } from "./sessionProbe";

// A request that answers 401 is logged to the console by the browser, so a signed-out launch must not send one. A
// launch with the session marker asks for the bootstrap at once; one without it first asks GET /api/instance (always
// 200) whether it is signed in, for at most PROBE_MS.

class NoES {
  addEventListener() {}
  close() {}
}

function launch(qc = makeQueryClient({ retry: false })) {
  window.history.replaceState({ idx: 0 }, "", "/");
  render(<App client={qc} />);
  return qc;
}

const paths = (calls: { url: URL }[]) => calls.map((c) => c.url.pathname);
const setupAnswer = { setup: true, auth: null, signed_in: false, access: { enabled: false, verified: false }, open: { reason: null } };

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => vi.unstubAllGlobals());

describe("the session marker", () => {
  it("is read from the cookie the server sets", () => {
    expect(hasSessionMarker()).toBe(true);
    setSessionMarker(false);
    expect(hasSessionMarker()).toBe(false);
  });

  it("with it, a launch asks for the bootstrap at once and nothing else first", async () => {
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    launch();
    await waitFor(() => expect(authStore.get()).toBe("in"));
    expect(paths(calls)[0]).toBe("/api/bootstrap");
    expect(paths(calls)).not.toContain("/api/instance");
    expect(paths(calls).filter((p) => p === "/api/bootstrap")).toHaveLength(1);
  });
});

describe("a launch without the marker", () => {
  beforeEach(() => setSessionMarker(false));

  it("signed out: shows the sign-in form and never requests anything that answers 401", async () => {
    const { calls } = mockFetch({ "GET /api/instance": () => json({ setup: false, auth: "password", signed_in: false }) });
    launch();
    await screen.findByRole("button", { name: "Sign in" });
    expect(paths(calls)).not.toContain("/api/bootstrap");
    expect(authStore.get()).toBe("out");
  });

  it("with no account yet: shows setup, also without a 401", async () => {
    const { calls } = mockFetch({ "GET /api/instance": () => json(setupAnswer) });
    launch();
    await screen.findByRole("heading", { level: 1, name: /create your account/i });
    expect(paths(calls)).not.toContain("/api/bootstrap");
  });

  it("signed in (a session the marker does not cover yet): asks for the bootstrap after the probe", async () => {
    const { calls } = mockFetch({
      "GET /api/instance": () => json({ setup: false, auth: "access", signed_in: true }),
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

  it("when the probe gets no answer (a stalled connection) the bootstrap is asked after PROBE_MS", async () => {
    let aborted = false;
    const { calls } = mockFetch({
      "GET /api/instance": (_u, init) =>
        new Promise<Response>((_, reject) =>
          init?.signal?.addEventListener("abort", () => {
            aborted = true;
            reject(init.signal!.reason as Error);
          }),
        ),
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    const start = Date.now();
    launch();
    await waitFor(() => expect(paths(calls)).toContain("/api/bootstrap"), { timeout: PROBE_MS + 3000 });
    expect(Date.now() - start).toBeGreaterThanOrEqual(PROBE_MS - 50);
    expect(aborted).toBe(true);
    await waitFor(() => expect(authStore.get()).toBe("in"));
  });

  it("after setup and a sign-out, the signed-out screen never shows the old setup answer", async () => {
    let setup = true;
    mockFetch({
      "GET /api/instance": () => json(setup ? setupAnswer : { setup: false, auth: "password", signed_in: false }),
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    const qc = launch();
    await screen.findByRole("heading", { level: 1, name: /create your account/i });
    // The account is made (the wizard signs in), then the reader signs out.
    setup = false;
    act(() => setSignedIn());
    await waitFor(() => expect(qc.getQueryData(INSTANCE_KEY)).toBeUndefined());
    act(() => authStore.set("out"));
    expect(screen.queryByRole("heading", { name: /create your account/i })).toBeNull();
    expect(screen.queryByText("Loading setup")).toBeNull();
    await screen.findByRole("button", { name: "Sign in" });
    expect(screen.queryByRole("heading", { name: /create your account/i })).toBeNull();
  });
});
