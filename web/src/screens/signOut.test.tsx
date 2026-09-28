import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

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

describe("sign-out", () => {
  it("waits for the device's offline copies to be deleted before showing the sign-in screen", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1)])),
      "GET /api/settings": () => json({ settings: [], values: {} }),
      "GET /api/filters": () => json({ filters: [] }),
      "GET /api/devices": () => json({ devices: [] }),
      "POST /api/auth/logout": () => new Response(null, { status: 204 }),
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
    await user.click(await screen.findByRole("button", { name: "Sign out" }, { timeout: 5000 }));
    await new Promise((r) => setTimeout(r, 20));
    expect(authStore.get()).toBe("in"); // still wiping
    release();
    await waitFor(() => expect(authStore.get()).toBe("out"));
    expect(deleted.sort()).toEqual(["kipple-data", "kipple-images"]);
  });
});
