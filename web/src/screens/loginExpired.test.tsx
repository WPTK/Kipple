import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import { api, ApiError, authStore, busyMessage, sessionLostStore, setSignedIn, signedOutStore } from "@/api/client";
import { offlineStore } from "@/lib/offlineState";
import { OfflineNotice } from "@/shell/OfflineNotice";
import { json, mockFetch } from "@/test/mockApi";
import { LoginScreen } from "./LoginScreen";

const reload = vi.hoisted(() => vi.fn());
vi.mock("@/lib/reload", () => ({ reloadToSignIn: reload, SIGN_IN_RELOAD: "kipple-signin" }));

/** What fetch gives for `redirect: "manual"` when the access proxy sends the request to its login page. */
const opaqueRedirect = () => ({ type: "opaqueredirect", status: 0, ok: false, headers: new Headers() }) as unknown as Response;

beforeEach(() => {
  sessionLostStore.set(false);
  signedOutStore.set(false);
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
  it("says you were signed out when the server ended a signed-in session", async () => {
    mockFetch({ "GET /api/x": () => json({ error: "auth" }, 401) });
    authStore.set("in");
    await expect(api("/api/x")).rejects.toBeInstanceOf(ApiError);
    expect(sessionLostStore.get()).toBe(true);
    render(
      <QueryClientProvider client={new QueryClient()}>
        <LoginScreen />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Your session ended");
  });

  it("a first visit shows no signed-out note", () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <LoginScreen />
      </QueryClientProvider>,
    );
    expect(screen.queryByText(/signed out|session ended/)).toBeNull();
  });

  it("focuses the username after the session ended", () => {
    sessionLostStore.set(true);
    render(
      <QueryClientProvider client={new QueryClient()}>
        <LoginScreen />
      </QueryClientProvider>,
    );
    expect(screen.getByLabelText("Username")).toHaveFocus();
  });

  it("says You signed out. after a sign-out you chose, and not after a lost session", () => {
    setSignedIn();
    authStore.set("out");
    expect(signedOutStore.get()).toBe(true);
    render(
      <QueryClientProvider client={new QueryClient()}>
        <LoginScreen />
      </QueryClientProvider>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("You signed out.");
    expect(screen.getByLabelText("Username")).toHaveFocus();
  });

  it("puts a wrong password under the password field, and marks both fields", async () => {
    mockFetch({ "POST /api/auth/login": () => json({ error: "auth" }, 401) });
    const user = await signIn();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("That username or password didn't match.");
    expect(screen.getByLabelText("Password", { selector: "input" })).toHaveAccessibleDescription("That username or password didn't match.");
    expect(screen.getByLabelText("Username")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Password", { selector: "input" })).toHaveFocus();
    await user.type(screen.getByLabelText("Password", { selector: "input" }), "x");
  });

  it("tells a forgotten password where to reset it", () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <LoginScreen />
      </QueryClientProvider>,
    );
    expect(screen.getByText(/Forgot your password\?/)).toHaveTextContent("kipple password");
  });

  it("names the wait the server asked for when sign-in is paused", () => {
    expect(busyMessage(new ApiError(503, "busy", null, 45_000))).toContain("about 45 seconds");
    expect(busyMessage(new ApiError(503, "busy", null, 60_000))).toContain("about 60 seconds");
    expect(busyMessage(new ApiError(503, "busy", null, 120_000))).toContain("about 2 minutes");
    expect(busyMessage(new ApiError(503, "busy", null, 0))).toContain("about 5 seconds");
  });

  it("an expired access-proxy sign-in is not called a wrong password, and offers a reload through the proxy", async () => {
    mockFetch({ "POST /api/auth/login": opaqueRedirect });
    const user = await signIn();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("has expired");
    expect(alert).not.toHaveTextContent("didn't match");
    await user.click(screen.getByRole("button", { name: "Reload" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("a sign-in that could not reach Kipple names the cause, and trying again signs in", async () => {
    let tries = 0;
    mockFetch({
      "POST /api/auth/login": () => {
        tries++;
        if (tries === 1) throw new TypeError("Failed to fetch");
        return json({ ok: true });
      },
      "GET /api/bootstrap": () => new Promise<Response>(() => {}),
    });
    const user = await signIn();
    expect(await screen.findByRole("alert")).toHaveTextContent("Kipple can't be reached");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(authStore.get()).toBe("in"));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(tries).toBe(2);
  });
  it("a wrong password is still a wrong password, with no reload offered", async () => {
    mockFetch({ "POST /api/auth/login": () => json({ error: "auth" }, 401) });
    await signIn();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("That username or password didn't match.");
    expect(screen.getByLabelText("Password")).toHaveAccessibleDescription(/didn't match/);
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
