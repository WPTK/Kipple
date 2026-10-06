import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { resetting } from "@/setup/session";

class NoES {
  addEventListener() {}
  close() {}
}

const bodyOf = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body)) as Record<string, unknown>;

function server(env: boolean, extra: Parameters<typeof mockFetch>[0] = {}, publicUrl = false) {
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    "GET /api/auth/me": () => json({ username: "reader", api_enabled: false, password_set: true, access_enabled: false, access_email: null, auth_mode: "password" }),
    "GET /api/reset": () => json({ env_account: env, public_url_set: publicUrl }),
    "POST /api/reset": () => json({ restarting: true, estimate_seconds: 30 }, 202),
    ...extra,
  });
}

async function openDialog() {
  window.history.replaceState({ idx: 0 }, "", "/settings/account");
  render(<App client={makeQueryClient({ retry: false })} />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Reset Kipple and start over" }, { timeout: 5000 }));
  await screen.findByRole("dialog");
  return user;
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetting.set(null);
  vi.stubGlobal("EventSource", NoES);
  vi.stubGlobal("caches", { delete: async () => true });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("reset Kipple", () => {
  it("says what is erased and asks for the password and the phrase before it sends anything", async () => {
    const { calls } = server(false);
    const user = await openDialog();
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent(/erases all your feeds, folders, reading history, settings and your account/);
    expect(dialog).toHaveTextContent("backup/pre-restore-*");
    const go = within(dialog).getByRole("button", { name: "Reset Kipple" });
    expect(go).toBeDisabled();
    await user.type(within(dialog).getByLabelText("Your web password"), "pw-pw-pw");
    expect(go).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Type "reset kipple"/), "reset");
    expect(go).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/Type "reset kipple"/), " kipple");
    expect(go).toBeEnabled();
    expect(within(dialog).queryByText(/KIPPLE_USERNAME/)).toBeNull();
    expect(within(dialog).queryByText(/anyone who can reach your public address/)).toBeNull();
    expect(within(dialog).queryByRole("radio")).toBeNull();
    await user.click(go);
    await waitFor(() => expect(resetting.get()).toEqual({ estimateSeconds: 30 }));
    const post = calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/reset");
    expect(post).toHaveLength(1);
    expect(bodyOf(post[0]!)).toEqual({ password: "pw-pw-pw", phrase: "reset kipple" });
    // Then the waiting page, in place of the app.
    expect(await screen.findByRole("heading", { name: "Resetting Kipple" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Resetting Kipple. It is working, you can leave this page open.");
  });

  it("shows a wrong password and sends nothing further", async () => {
    server(false, { "POST /api/reset": () => json({ error: "bad_password" }, 403) });
    const user = await openDialog();
    const dialog = screen.getByRole("dialog");
    await user.type(within(dialog).getByLabelText("Your web password"), "nope");
    await user.type(within(dialog).getByLabelText(/Type "reset kipple"/), "Reset Kipple");
    await user.click(within(dialog).getByRole("button", { name: "Reset Kipple" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("The current password isn't right.");
    expect(resetting.get()).toBeNull();
  });

  it("says the environment account is ignored, with no choice and no second step", async () => {
    const { calls } = server(true);
    const user = await openDialog();
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Kipple will ignore KIPPLE_USERNAME and KIPPLE_PASSWORD until you create a new account. You can delete them from your compose file whenever convenient.");
    expect(within(dialog).queryByRole("radio")).toBeNull();
    await user.type(within(dialog).getByLabelText("Your web password"), "pw-pw-pw");
    await user.type(within(dialog).getByLabelText(/Type "reset kipple"/), "reset kipple");
    await user.click(within(dialog).getByRole("button", { name: "Reset Kipple" }));
    await waitFor(() => expect(resetting.get()).not.toBeNull());
    const post = calls.find((c) => c.method === "POST" && c.url.pathname === "/api/reset")!;
    expect(bodyOf(post)).toEqual({ password: "pw-pw-pw", phrase: "reset kipple" });
  });

  it("warns that a public address leaves setup open to anyone who can reach it", async () => {
    server(false, {}, true);
    await openDialog();
    expect(screen.getByRole("dialog")).toHaveTextContent("Until you create the new account, anyone who can reach your public address can create it. Do the setup right away.");
  });
});

describe("the page that waits for a reset", () => {
  it("polls until Kipple answers that it is in setup mode, then offers setup", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let setup = false;
    let down = true;
    mockFetch({
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
      "GET /api/instance": () => {
        if (down) throw new TypeError("Failed to fetch");
        return setup ? json({ setup: true, auth: null, access: { enabled: false, verified: false }, open: { reason: null }, restore: "none" }) : json({ setup: false, auth: "password" });
      },
    });
    authStore.set("in");
    resetting.set({ estimateSeconds: 30 });
    window.history.replaceState({ idx: 0 }, "", "/");
    render(<App client={makeQueryClient({ retry: false })} />);
    expect(await screen.findByRole("heading", { name: "Resetting Kipple" })).toBeInTheDocument();
    // Away, then back but still the old instance (the answer of a process that has not stopped yet): keep waiting.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    down = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    expect(screen.getByRole("heading", { name: "Resetting Kipple" })).toBeInTheDocument();
    setup = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    expect(await screen.findByRole("heading", { name: "Kipple is ready for setup" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Set up Kipple" })).toBeInTheDocument();
  });

  it("says Kipple stopped after a long silence", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockFetch({
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
      "GET /api/instance": () => {
        throw new TypeError("Failed to fetch");
      },
    });
    authStore.set("in");
    resetting.set({ estimateSeconds: 30 });
    window.history.replaceState({ idx: 0 }, "", "/");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByRole("heading", { name: "Resetting Kipple" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(310_000);
    });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Kipple stopped. Start it again and the reset will finish."));
  });

  it("tells apart Kipple being away from Kipple answering with something else", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mockFetch({
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
      "GET /api/instance": () => new Response("This address is not allowed", { status: 421 }),
    });
    authStore.set("in");
    resetting.set({ estimateSeconds: 30 });
    window.history.replaceState({ idx: 0 }, "", "/");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByRole("heading", { name: "Resetting Kipple" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(310_000);
    });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Kipple answered, but not as expected. Open it by its local address."));
    expect(screen.queryByText(/Kipple stopped/)).toBeNull();
  });
});
