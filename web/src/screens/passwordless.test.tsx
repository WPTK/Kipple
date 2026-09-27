import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";

// jsdom has no EventSource; the shell subscribes to one.
class NoES {
  addEventListener() {}
  close() {}
}

type User = (typeof bootstrap)["user"];

function base(user: Partial<User>, extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, user: { ...bootstrap.user, ...user } }),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const body = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body));

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Passwordless account (Cloudflare Access)", () => {
  it("offers Remove web password only with a verified Access sign-in", async () => {
    base({ password_set: true, access_enabled: true, access_email: null });
    go("/settings");
    await screen.findByRole("button", { name: "Change web password" });
    expect(screen.queryByRole("button", { name: "Remove web password" })).toBeNull();
  });

  it("removes the password after the current one is entered", async () => {
    const { calls } = base({ password_set: true, access_enabled: true, access_email: "owner@example.com" }, { "POST /api/account/password": () => new Response(null, { status: 204 }) });
    go("/settings");
    const user = userEvent.setup();
    expect(await screen.findByText(/Cloudflare Access: owner@example.com/)).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Remove web password" }));
    const dlg = await screen.findByRole("dialog", { name: "Remove web password" });
    expect(within(dlg).getByRole("button", { name: "Remove password" })).toBeDisabled();
    await user.type(within(dlg).getByLabelText("Current password"), "old passphrase");
    await user.click(within(dlg).getByRole("button", { name: "Remove password" }));
    await vi.waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/account/password")).toBe(true));
    expect(body(calls.find((c) => c.url.pathname === "/api/account/password") as never)).toEqual({ current: "old passphrase", remove: true });
  });

  it("explains a removal refused for lack of an Access sign-in", async () => {
    base({ password_set: true, access_enabled: true, access_email: "owner@example.com" }, { "POST /api/account/password": () => json({ error: "access_required" }, 403) });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Remove web password" }));
    const dlg = await screen.findByRole("dialog", { name: "Remove web password" });
    await user.type(within(dlg).getByLabelText("Current password"), "old passphrase");
    await user.click(within(dlg).getByRole("button", { name: "Remove password" }));
    expect(await within(dlg).findByRole("alert")).toHaveTextContent("Cloudflare Access sign-in");
  });

  it("sets a password without asking for a current one when there is none", async () => {
    const { calls } = base({ password_set: false, access_enabled: true, access_email: "owner@example.com" }, { "POST /api/account/password": () => new Response(null, { status: 204 }) });
    go("/settings");
    const user = userEvent.setup();
    expect(await screen.findByText(/No web password/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove web password" })).toBeNull();
    await user.click(await screen.findByRole("button", { name: "Set web password" }));
    const dlg = await screen.findByRole("dialog", { name: "Set web password" });
    expect(within(dlg).queryByLabelText("Current password")).toBeNull();
    await user.type(within(dlg).getByLabelText("New password"), "abcdef");
    await user.type(within(dlg).getByLabelText("New password again"), "abcdef");
    await user.click(within(dlg).getByRole("button", { name: "Set password" }));
    await vi.waitFor(() => expect(calls.some((c) => c.url.pathname === "/api/account/password")).toBe(true));
    expect(body(calls.find((c) => c.url.pathname === "/api/account/password") as never)).toEqual({ current: "", new: "abcdef" });
  });

  it("generates an API password without a web password field", async () => {
    base({ password_set: false, access_enabled: true, access_email: "owner@example.com" }, { "POST /api/account/api-password": () => json({ api_password: "fresh-api-password-xyz" }) });
    go("/settings");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Generate API password" }));
    const dlg = await screen.findByRole("dialog", { name: "Generate API password" });
    expect(within(dlg).queryByLabelText("Your web password")).toBeNull();
    await user.click(within(dlg).getByRole("button", { name: "Generate" }));
    const shown = await screen.findByRole("dialog", { name: "Your new API password" });
    expect(within(shown).getByTestId("api-password")).toHaveTextContent("fresh-api-password-xyz");
  });
});

describe("Login without a password", () => {
  it("submits an empty password and explains a refusal", async () => {
    const { calls } = mockFetch({
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
      "POST /api/auth/login": () => json({ error: "auth" }, 401),
    });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "dev");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("only through Cloudflare Access");
    expect(body(calls.find((c) => c.url.pathname === "/api/auth/login") as never)).toEqual({ username: "dev", password: "" });
  });
});
