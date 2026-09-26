import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { offlineStore } from "@/lib/offlineState";
import { OfflineNotice } from "@/shell/OfflineNotice";
import { json, mockFetch } from "@/test/mockApi";
import { LoginScreen } from "./LoginScreen";

const reload = vi.hoisted(() => vi.fn());
vi.mock("@/lib/reload", () => ({ reloadToSignIn: reload, SIGN_IN_RELOAD: "kipple-signin" }));

/** What fetch gives for `redirect: "manual"` when the access proxy sends the request to its login page. */
const opaqueRedirect = () => ({ type: "opaqueredirect", status: 0, ok: false, headers: new Headers() }) as unknown as Response;

beforeEach(() => {
  reload.mockClear();
  authStore.set("out");
  offlineStore.set({ online: true, pending: 0, updateReady: false, sessionExpired: false });
});

async function signIn() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <LoginScreen />
    </QueryClientProvider>,
  );
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Username"), "reader");
  await user.type(screen.getByLabelText("Password"), "pw");
  await user.click(screen.getByRole("button", { name: "Sign in" }));
  return user;
}

describe("the sign-in screen", () => {
  it("an expired access-proxy sign-in is not called a wrong password, and offers a reload through the proxy", async () => {
    mockFetch({ "POST /api/auth/login": opaqueRedirect });
    const user = await signIn();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("has expired");
    expect(alert).not.toHaveTextContent("didn't match");
    await user.click(screen.getByRole("button", { name: "Reload" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("a wrong password is still a wrong password, with no reload offered", async () => {
    mockFetch({ "POST /api/auth/login": () => json({ error: "auth" }, 401) });
    await signIn();
    expect(await screen.findByRole("alert")).toHaveTextContent("That username or password didn't match.");
    expect(screen.queryByRole("button", { name: "Reload" })).toBeNull();
  });
});

describe("every Reload for an expired sign-in goes through the network, not the stored shell", () => {
  it("the notice's Reload", async () => {
    offlineStore.set((s) => ({ ...s, sessionExpired: true }));
    render(<OfflineNotice />);
    await userEvent.setup().click(screen.getByRole("button", { name: "Reload" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("the launch screen's Reload", async () => {
    authStore.set("unknown");
    mockFetch({ "GET /api/bootstrap": opaqueRedirect });
    render(<App client={makeQueryClient({ retry: false })} />);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Reload" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });
});
