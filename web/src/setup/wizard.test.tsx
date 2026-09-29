import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore, openRefusedStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { themeStore } from "@/theme/theme";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { resetTakenSetupCode } from "./api";
import { setupSecret } from "./secret";

// jsdom has no EventSource; the shell subscribes to one.
class NoES {
  addEventListener() {}
  close() {}
}

type Handler = Parameters<typeof mockFetch>[0][string];

interface World {
  /** Answers GET /api/setup/state. */
  state: { claimed: boolean; access: { enabled: boolean; verified: boolean }; open: { reason: string | null; lan_reason: string | null } };
  instance: { setup: boolean; auth: string | null };
  signedIn: boolean;
  pending: boolean;
  authMode: string;
  passwordSet: boolean;
  tz: string;
  envTz: string | null;
  starter: unknown;
}

function makeWorld(over: Partial<World> = {}): World {
  return {
    state: { claimed: false, access: { enabled: false, verified: false }, open: { reason: null, lan_reason: null } },
    instance: { setup: true, auth: null },
    signedIn: false,
    pending: true,
    authMode: "password",
    passwordSet: true,
    tz: "UTC",
    envTz: null,
    starter: { available: true, categories: [] },
    ...over,
  };
}

const tzMeta = (w: World) => ({ key: "tz", value: w.envTz ?? w.tz, default: "UTC", label: "Time zone", description: "Used for statistics.", group: "account", kind: "text", surface: "settings", env_override: w.envTz });

/** A tiny fake Kipple: every route the wizard touches, with the state it changes. Extra routes win over these. */
function server(w: World, extra: Record<string, Handler> = {}) {
  return mockFetch({
    "GET /api/instance": () => json(w.instance),
    "GET /api/setup/state": () => json({ ...w.state, token_hint: "hint", token_issued_at: 1_790_000_000 }),
    "POST /api/setup/claim": () => new Response(null, { status: 204 }),
    "POST /api/setup/account": () => {
      w.signedIn = true;
      w.instance = { setup: false, auth: "password" };
      return json({ username: "reader", auth_mode: w.authMode }, 201);
    },
    "POST /api/auth/open": () => {
      w.signedIn = true;
      return new Response(null, { status: 204 });
    },
    "GET /api/bootstrap": () => (w.signedIn ? json({ ...bootstrap, user: { ...bootstrap.user, username: "reader", password_set: w.passwordSet, auth_mode: w.authMode, setup_pending: w.pending } }) : json({ error: "auth" }, 401)),
    "GET /api/auth/me": () => json({ username: "reader", api_enabled: false, password_set: w.passwordSet, access_enabled: false, access_email: null, auth_mode: w.authMode, setup_pending: w.pending }),
    "GET /api/settings": () => json({ settings: [tzMeta(w)], values: { tz: w.envTz ?? w.tz } }),
    "PATCH /api/settings": (_u, init) => {
      const patch = JSON.parse(String(init?.body)) as Record<string, unknown>;
      if (typeof patch.tz === "string") w.tz = patch.tz;
      return json({ settings: [tzMeta(w)], values: { tz: w.tz, ...patch } });
    },
    "GET /api/starter-feeds": () => json(w.starter),
    "POST /api/onboarding/complete": () => {
      w.pending = false;
      return new Response(null, { status: 204 });
    },
    "POST /api/onboarding/restart": () => {
      w.pending = true;
      return new Response(null, { status: 204 });
    },
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

const bodyOf = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body)) as Record<string, unknown>;
const callTo = (calls: { method: string; url: URL; init?: RequestInit }[], method: string, path: string) => calls.filter((c) => c.method === method && c.url.pathname === path);
/** The first alert on the page that is not the shell's own (always present, empty) alert region. */
async function findAlert(): Promise<HTMLElement> {
  return waitFor(() => {
    const a = screen.getAllByRole("alert").filter((e) => e.getAttribute("data-testid") !== "alert-region");
    expect(a.length).toBeGreaterThan(0);
    return a[0] as HTMLElement;
  });
}
/** Waits for the step whose heading is `name` (the heading of the step just left is still there for a moment). */
const headingIs = (name: string) => screen.findByRole("heading", { level: 1, name });
const heading = () => screen.findByRole("heading", { level: 1 });

function browserZoneIs(zone: string) {
  const orig = Intl.DateTimeFormat.prototype.resolvedOptions;
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockImplementation(function (this: Intl.DateTimeFormat) {
    return { ...orig.call(this), timeZone: zone };
  });
}

beforeEach(() => {
  authStore.set("unknown");
  openRefusedStore.set(null);
  setupSecret.set(null);
  resetTakenSetupCode();
  liveStore.set(initialLive);
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Step 1: setup code", () => {
  it("asks for the code first, says where to find it, and passes axe", async () => {
    server(makeWorld());
    const { container } = go("/");
    await headingIs("Enter your setup code");
    expect(screen.getByText("Step 1 of 7")).toBeInTheDocument();
    expect(screen.getByLabelText("Setup code")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Where do I find the code?" }));
    expect(screen.getAllByText(/kipple setup-token/).length).toBeGreaterThan(0);
    expect(await axe(container)).toHaveNoViolations();
  });

  it("fills the code in from #setup=<code> and removes it from the address at once", async () => {
    server(makeWorld());
    go("/#setup=ABCD-EFGH-JKMN");
    expect(await screen.findByLabelText("Setup code")).toHaveValue("ABCD-EFGH-JKMN");
    expect(window.location.hash).toBe("");
    expect(screen.getByText(/code from your link is filled in/)).toBeInTheDocument();
  });

  it("sends the code and moves to the account step", async () => {
    const { calls } = server(makeWorld());
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Setup code"), "abcd efgh");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Create your account");
    await waitFor(() => expect(callTo(calls, "POST", "/api/setup/claim")).toHaveLength(1));
    expect(bodyOf(callTo(calls, "POST", "/api/setup/claim")[0] as never)).toEqual({ token: "abcd efgh" });
    // Setup calls are made before any sign-in: the app never mistakes them for one.
    expect(authStore.get()).toBe("out");
  });

  it("goes straight to the account step when the setup session is already valid", async () => {
    server(makeWorld({ state: { ...makeWorld().state, claimed: true } }));
    go("/");
    await headingIs("Create your account");
  });

  it("asks for a code before it sends anything", async () => {
    const { calls } = server(makeWorld());
    go("/");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent("Enter the setup code first.");
    expect(callTo(calls, "POST", "/api/setup/claim")).toHaveLength(0);
  });

  it("explains a wrong code", async () => {
    server(makeWorld(), { "POST /api/setup/claim": () => json({ error: "bad_token", message: "that is not the current setup code (`kipple setup-token` shows it)" }, 403) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Setup code"), "wrong");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent(/isn't the current setup code.*kipple setup-token/);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Enter your setup code");
  });

  it("says how long to wait after too many wrong codes", async () => {
    server(makeWorld(), { "POST /api/setup/claim": () => json({ error: "locked" }, 429, { "Retry-After": "42" }) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Setup code"), "wrong");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent("about 42 seconds");
  });

  it("says so when the server cannot be reached", async () => {
    server(makeWorld(), { "POST /api/setup/claim": () => Promise.reject(new TypeError("offline")) as never });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Setup code"), "abc");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent("couldn't reach the server");
  });

  it("offers a reload when setup finished while the page was open", async () => {
    server(makeWorld(), { "GET /api/setup/state": () => json({ error: "not_found" }, 404) });
    go("/");
    expect(await findAlert()).toHaveTextContent("Reload the page to sign in");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});

describe("Step 2: account", () => {
  const claimed = () => makeWorld({ state: { claimed: true, access: { enabled: false, verified: false }, open: { reason: null, lan_reason: null } } });

  it("creates the account with a password, signs in and continues at the time zone", async () => {
    const w = claimed();
    const { calls } = server(w);
    const { container } = go("/");
    const user = userEvent.setup();
    await headingIs("Create your account");
    expect(await axe(container)).toHaveNoViolations();
    await user.type(screen.getByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.type(screen.getByLabelText("Password again"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your time zone");
    expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", password: "correct horse" });
    expect(window.location.pathname).toBe("/welcome/timezone");
    // Kept in memory for step 7.
    expect(setupSecret.get()).toBe("correct horse");
  });

  it("checks the user name and password before sending", async () => {
    const { calls } = server(claimed());
    go("/");
    const user = userEvent.setup();
    await screen.findByLabelText("User name");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("Enter a user name.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("User name"), "no spaces");
    expect(screen.getByText(/1 to 64 letters, digits, dots, dashes or underscores/, { selector: "[role=alert] span" })).toBeInTheDocument();
    await user.clear(screen.getByLabelText("User name"));
    await user.type(screen.getByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "abcd");
    expect(screen.getByText("Use at least 5 characters.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Password"));
    await user.type(screen.getByLabelText("Password"), "change-me");
    expect(screen.getByText(/example password/)).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Password"));
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "different");
    expect(screen.getByText("The two passwords don't match.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(callTo(calls, "POST", "/api/setup/account")).toHaveLength(0);
  });

  it("shows the server's word on a bad password and keeps what was typed", async () => {
    server(claimed(), { "POST /api/setup/account": () => json({ error: "bad_new_password", message: "the password must not be the example value" }, 400) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("The password must not be the example value.")).toBeInTheDocument();
    expect(screen.getByLabelText("User name")).toHaveValue("reader");
  });

  it("goes back to the code when the setup session ran out", async () => {
    server(claimed(), { "POST /api/setup/account": () => json({ error: "setup_session", message: "enter the setup code first" }, 401) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Enter your setup code");
    expect(screen.getByText(/timed out/)).toBeInTheDocument();
    expect(authStore.get()).toBe("out");
  });

  it("says Kipple was set up a moment ago when someone else got there first", async () => {
    server(claimed(), { "POST /api/setup/account": () => json({ error: "already_set_up", message: "sign in instead" }, 409) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await findAlert()).toHaveTextContent(/set up a moment ago/);
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("explains a network failure and a rate limit without losing the form", async () => {
    let n = 0;
    server(claimed(), { "POST /api/setup/account": () => (++n === 1 ? (Promise.reject(new TypeError("x")) as never) : json({ error: "rate" }, 429)) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("Kipple couldn't reach the server.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText(/Too many attempts/)).toBeInTheDocument();
  });

  describe("no password, open mode", () => {
    const choose = async () => {
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText("User name"), "reader");
      await user.click(screen.getByRole("radio", { name: /No password at all/ }));
      return user;
    };

    it("shows the notice, needs the acknowledgement and sends it", async () => {
      const w = claimed();
      w.authMode = "open";
      w.passwordSet = false;
      const { calls } = server(w);
      const { container } = go("/");
      const user = await choose();
      expect(screen.getByText("Anyone who can reach this address can read and change everything.")).toBeInTheDocument();
      expect(screen.getByText(/reachable only from this computer \(localhost\) or over Tailscale/i, { selector: "p" })).toBeInTheDocument();
      expect(await axe(container)).toHaveNoViolations();
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      expect(await screen.findByText("Tick the box to confirm you understand.")).toBeInTheDocument();
      expect(callTo(calls, "POST", "/api/setup/account")).toHaveLength(0);
      await user.click(screen.getByRole("checkbox", { name: /I understand/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      await headingIs("Choose your time zone");
      expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", passwordless: "open", acknowledge_open: true });
      expect(setupSecret.get()).toBeNull();
    });

    it("asks for the local-network switch when only that lets it work, and sends open_lan", async () => {
      const w = claimed();
      w.state.open = { reason: "peer", lan_reason: null };
      w.authMode = "open";
      const { calls } = server(w);
      go("/");
      const user = await choose();
      expect(screen.getByText(/only accepts this computer and Tailscale devices|isn't this computer or on your Tailscale network/)).toBeInTheDocument();
      await user.click(screen.getByRole("checkbox", { name: /I understand/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      expect(await findAlert()).toHaveTextContent(/Also allow devices on my local network/);
      await user.click(screen.getByRole("checkbox", { name: "Also allow devices on my local network" }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      await headingIs("Choose your time zone");
      expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", passwordless: "open", acknowledge_open: true, open_lan: true });
    });

    it.each([
      ["forwarded", /proxy or tunnel/],
      ["host", /KIPPLE_ALLOWED_HOSTS/],
      ["peer", /Tailscale/],
    ])("cannot be chosen when the gate refuses it for good (%s), and says why", async (reason, text) => {
      const w = claimed();
      w.state.open = { reason, lan_reason: reason };
      server(w);
      go("/");
      await screen.findByLabelText("User name");
      const radio = screen.getByRole("radio", { name: /No password at all/ });
      expect(radio).toBeDisabled();
      expect(screen.getByRole("note")).toHaveTextContent(text);
    });

    it("shows the server's refusal in plain English when the gate says no at the last moment", async () => {
      server(claimed(), { "POST /api/setup/account": () => json({ error: "open_refused", reason: "forwarded", message: "developer wording" }, 403) });
      go("/");
      const user = await choose();
      await user.click(screen.getByRole("checkbox", { name: /I understand/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      const alert = await findAlert();
      expect(alert).toHaveTextContent(/proxy or tunnel/);
      expect(alert).not.toHaveTextContent("developer wording");
    });

    it("maps a missing acknowledgement answer onto the checkbox", async () => {
      server(claimed(), { "POST /api/setup/account": () => json({ error: "ack_required" }, 400) });
      go("/");
      const user = await choose();
      await user.click(screen.getByRole("checkbox", { name: /I understand/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      expect(await screen.findByText("Tick the box to confirm you understand.")).toBeInTheDocument();
    });
  });

  describe("no password, Cloudflare Access", () => {
    it("is not offered without Access", async () => {
      server(claimed());
      go("/");
      await screen.findByLabelText("User name");
      expect(screen.queryByRole("radio", { name: /Cloudflare Access/ })).toBeNull();
    });

    it("is offered but disabled until this request came through Access", async () => {
      const w = claimed();
      w.state.access = { enabled: true, verified: false };
      server(w);
      go("/");
      await screen.findByLabelText("User name");
      expect(screen.getByRole("radio", { name: /Cloudflare Access/ })).toBeDisabled();
      expect(screen.getByText(/open Kipple through its Cloudflare Access address/)).toBeInTheDocument();
    });

    it("creates the account without a password when Access is verified", async () => {
      const w = claimed();
      w.state.access = { enabled: true, verified: true };
      w.authMode = "access";
      w.passwordSet = false;
      const { calls } = server(w);
      go("/");
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText("User name"), "reader");
      await user.click(screen.getByRole("radio", { name: /Cloudflare Access/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      await headingIs("Choose your time zone");
      expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", passwordless: "access" });
    });

    it("explains a refusal for lack of an Access sign-in", async () => {
      const w = claimed();
      w.state.access = { enabled: true, verified: true };
      server(w, { "POST /api/setup/account": () => json({ error: "access_required" }, 403) });
      go("/");
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText("User name"), "reader");
      await user.click(screen.getByRole("radio", { name: /Cloudflare Access/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      expect(await findAlert()).toHaveTextContent(/needs Access to be set up/);
    });
  });
});

describe("open-mode sign-in", () => {
  it("signs in without asking anything and opens the reader", async () => {
    const w = makeWorld({ instance: { setup: false, auth: "open" }, authMode: "open", pending: false, passwordSet: false });
    const { calls } = server(w);
    go("/");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/auth/open")).toHaveLength(1);
  });

  it.each([
    ["forwarded", /proxy or tunnel/],
    ["host", /KIPPLE_ALLOWED_HOSTS/],
    ["peer", /Tailscale network/],
  ])("says why when the gate refuses this address (%s), and lets you try again", async (reason, text) => {
    const w = makeWorld({ instance: { setup: false, auth: "open" }, authMode: "open", pending: false });
    let refuse = true;
    server(w, {
      "POST /api/auth/open": () => {
        if (refuse) return json({ error: "open_refused", reason, message: "developer wording" }, 403);
        w.signedIn = true;
        return new Response(null, { status: 204 });
      },
    });
    const { container } = go("/");
    await headingIs("Kipple can't let you in from here");
    expect(screen.getByRole("alert")).toHaveTextContent(text);
    expect(screen.getByText(/localhost or its IP address/)).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
    refuse = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
  });

  it("shows the same explanation when a signed-in request is refused later", async () => {
    const w = makeWorld({ instance: { setup: false, auth: "open" }, authMode: "open", pending: false, signedIn: true });
    let refuse = false;
    server(w, {
      "GET /api/items": () => (refuse ? json({ error: "open_refused", reason: "peer", message: "x" }, 403) : json(pageOf([card(1)]))),
    });
    go("/");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    refuse = true;
    await act(async () => {
      const { api } = await import("@/api/client");
      await api("/api/items").catch(() => undefined);
    });
    await headingIs("Kipple can't let you in from here");
    expect(screen.getByRole("alert")).toHaveTextContent(/Tailscale network/);
    refuse = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
  });

  it("shows the sign-in form for a password account, as before", async () => {
    server(makeWorld({ instance: { setup: false, auth: "password" } }));
    go("/");
    expect(await screen.findByRole("heading", { name: "Sign in to Kipple" })).toBeInTheDocument();
  });

  it("falls back to the sign-in form on a server without the setup routes", async () => {
    server(makeWorld(), { "GET /api/instance": () => json({ error: "not_found" }, 404) });
    go("/");
    expect(await screen.findByRole("heading", { name: "Sign in to Kipple" })).toBeInTheDocument();
  });
});

describe("routing into and out of /welcome", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("sends an account with setup pending to /welcome from anywhere", async () => {
    server(signedIn());
    go("/l/unread");
    await headingIs("Choose your time zone");
    expect(window.location.pathname).toBe("/welcome/timezone");
  });

  it("does not, once setup is done, and sends /welcome to the reader", async () => {
    server(signedIn({ pending: false }));
    go("/welcome/theme");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/l/unread");
  });

  it("turns an unknown step into the first", async () => {
    server(signedIn());
    go("/welcome/nowhere");
    await headingIs("Choose your time zone");
    expect(window.location.pathname).toBe("/welcome/timezone");
  });

  it("resumes at the step in the address after a reload", async () => {
    server(signedIn());
    go("/welcome/import");
    await headingIs("Bring your feeds along");
    expect(screen.getByText("Step 5 of 7")).toBeInTheDocument();
  });

  it("moves focus to the new step's heading", async () => {
    server(signedIn());
    go("/welcome/theme");
    const h = await heading();
    await waitFor(() => expect(h).toHaveFocus());
  });

  it("ends setup from any step with 'Skip the rest of setup'", async () => {
    const w = signedIn();
    const { calls } = server(w);
    go("/welcome/import");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Skip the rest of setup" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
  });

  it("stays on the step and says so when ending setup fails", async () => {
    server(signedIn(), { "POST /api/onboarding/complete": () => json({ error: "internal" }, 500) });
    go("/welcome/import");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Skip the rest of setup" }));
    expect(await screen.findByText("The server returned an error. Try again.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Bring your feeds along");
  });
});

describe("Step 3: time zone", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("preselects the browser's zone, saves it and goes on", async () => {
    browserZoneIs("Asia/Tokyo");
    const w = signedIn();
    const { calls } = server(w);
    const { container } = go("/welcome/timezone");
    const sel = await screen.findByTestId("selected-zone");
    expect(sel).toHaveTextContent("Asia/Tokyo (UTC+09:00)");
    expect(sel).toHaveTextContent("Suggested from your browser");
    expect(await axe(container)).toHaveNoViolations();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Pick a look");
    expect(bodyOf(callTo(calls, "PATCH", "/api/settings")[0] as never)).toEqual({ tz: "Asia/Tokyo" });
    expect(w.tz).toBe("Asia/Tokyo");
  });

  it("Skip keeps the preselected zone rather than leaving UTC", async () => {
    browserZoneIs("Europe/Berlin");
    const w = signedIn();
    server(w);
    go("/welcome/timezone");
    await screen.findByTestId("selected-zone");
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Pick a look");
    expect(w.tz).toBe("Europe/Berlin");
  });

  it("filters the list by name and by offset", async () => {
    browserZoneIs("UTC");
    server(signedIn());
    go("/welcome/timezone");
    const user = userEvent.setup();
    const search = await screen.findByLabelText("Search time zones");
    const list = screen.getByRole("listbox", { name: "Time zones" });
    const all = within(list).getAllByRole("option").length;
    expect(all).toBeGreaterThan(50);
    await user.type(search, "tokyo");
    expect(within(list).getAllByRole("option").map((o) => o.getAttribute("value"))).toEqual(["Asia/Tokyo"]);
    expect(screen.getByText("1 time zone match.")).toBeInTheDocument();
    await user.clear(search);
    await user.type(search, "+9");
    const nine = within(list).getAllByRole("option").map((o) => o.getAttribute("value"));
    expect(nine).toContain("Asia/Tokyo");
    expect(nine).toContain("Asia/Seoul");
    expect(nine).not.toContain("America/New_York");
    await user.clear(search);
    await user.type(search, "qqqq");
    expect(within(list).queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("No time zone matches.")).toBeInTheDocument();
  });

  it("saves the zone picked from the list", async () => {
    browserZoneIs("UTC");
    const w = signedIn();
    const { calls } = server(w);
    go("/welcome/timezone");
    const user = userEvent.setup();
    await screen.findByLabelText("Search time zones");
    await user.selectOptions(screen.getByRole("listbox", { name: "Time zones" }), "Pacific/Auckland");
    expect(screen.getByTestId("selected-zone")).toHaveTextContent("Pacific/Auckland");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await heading();
    expect(bodyOf(callTo(calls, "PATCH", "/api/settings")[0] as never)).toEqual({ tz: "Pacific/Auckland" });
  });

  it("falls back to UTC, and says so, for a browser zone that is not in the list", async () => {
    browserZoneIs("Mars/Olympus_Mons");
    const w = signedIn();
    const { calls } = server(w);
    go("/welcome/timezone");
    const sel = await screen.findByTestId("selected-zone");
    expect(sel).toHaveTextContent("UTC");
    expect(screen.getByText(/reported the time zone "Mars\/Olympus_Mons"/)).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    await heading();
    // Nothing to save: UTC is already what Kipple has.
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
  });

  it("falls back to UTC when the server refuses the zone", async () => {
    browserZoneIs("Asia/Tokyo");
    const w = signedIn();
    server(w, { "PATCH /api/settings": () => json({ error: "invalid_settings", keys: ["tz"], issues: [{ key: "tz", message: "unknown time zone" }] }, 400) });
    go("/welcome/timezone");
    await screen.findByTestId("selected-zone");
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent(/doesn't know the time zone "Asia\/Tokyo".*UTC is selected instead/);
    expect(screen.getByTestId("selected-zone")).toHaveTextContent("UTC");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Choose your time zone");
  });

  it("reports another failure to save and stays", async () => {
    browserZoneIs("Asia/Tokyo");
    server(signedIn(), { "PATCH /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/timezone");
    await screen.findByTestId("selected-zone");
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent("The server returned an error");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Choose your time zone");
  });

  it("keeps a zone that was chosen before (a repeat run) instead of the browser's", async () => {
    browserZoneIs("Asia/Tokyo");
    const w = signedIn({ tz: "America/New_York" });
    const { calls } = server(w);
    go("/welcome/timezone");
    const sel = await screen.findByTestId("selected-zone");
    expect(sel).toHaveTextContent("America/New York");
    expect(sel).toHaveTextContent("Kipple is already set to this zone");
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    await heading();
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
  });

  it("is read-only and says why when the TZ environment variable is set", async () => {
    browserZoneIs("Asia/Tokyo");
    const { calls } = server(signedIn({ envTz: "Europe/Paris" }));
    const { container } = go("/welcome/timezone");
    expect(await screen.findByText("Set by the TZ environment variable; remove it to choose here.")).toBeInTheDocument();
    expect(screen.getByText("Europe/Paris")).toBeInTheDocument();
    expect(screen.queryByLabelText("Search time zones")).toBeNull();
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Pick a look");
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
  });

  it("lets you move on when the settings cannot be loaded", async () => {
    server(signedIn(), { "GET /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/timezone");
    expect(await findAlert()).toHaveTextContent("couldn't load its settings");
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip this step" }));
    await headingIs("Pick a look");
  });
});

describe("Step 4: theme", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("applies the picks at once, shows both samples and saves the pair as the default", async () => {
    const { calls } = server(signedIn());
    const { container } = go("/welcome/theme");
    const user = userEvent.setup();
    await heading();
    expect(screen.getByTestId("sample-day")).toHaveAttribute("data-scheme", "paper");
    expect(screen.getByTestId("sample-night")).toHaveAttribute("data-scheme", "midnight");
    await user.selectOptions(screen.getByLabelText("Day theme"), "linen");
    await user.selectOptions(screen.getByLabelText("Night theme"), "graphite");
    expect(screen.getByTestId("sample-day")).toHaveAttribute("data-scheme", "linen");
    expect(themeStore.get()).toMatchObject({ mode: "follow", schedule: false, day: "linen", night: "graphite" });
    expect(await axe(container)).toHaveNoViolations();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Bring your feeds along");
    const patches = callTo(calls, "PATCH", "/api/settings").map((c) => bodyOf(c as never));
    expect(patches).toContainEqual({ "ui.theme": "system", "ui.theme_day": "linen", "ui.theme_night": "graphite" });
  });

  it("Skip puts this device's theme back the way it was", async () => {
    const { calls } = server(signedIn());
    go("/welcome/theme");
    const user = userEvent.setup();
    await heading();
    await user.selectOptions(screen.getByLabelText("Day theme"), "linen");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    expect(themeStore.get()).toEqual(DEFAULT_THEME_SETTINGS);
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
  });

  it("reports a failed save and stays", async () => {
    server(signedIn(), { "PATCH /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/theme");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent("The server returned an error");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Pick a look");
  });

  it("goes back to the time zone", async () => {
    server(signedIn());
    go("/welcome/theme");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Choose your time zone");
  });
});

describe("Step 5: OPML import", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });
  const file = (name = "feeds.opml") => new File(['<?xml version="1.0"?><opml/>'], name, { type: "text/x-opml" });
  const result = { folders_created: 2, feeds_added: 5, feeds_existing: [{ url: "https://a.example/f", feed_id: "9" }], folders_merged_case: [], memberships_dropped: [], run_id: "r1" };

  it("imports a file, shows the report and continues", async () => {
    const { calls } = server(signedIn(), { "POST /api/opml": () => json(result) });
    const { container } = go("/welcome/import");
    const user = userEvent.setup();
    await heading();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    await user.upload(screen.getByLabelText("OPML file"), file());
    await user.type(screen.getByLabelText("Mark older articles as read (optional)"), "7");
    await user.click(await waitFor(() => { const b = screen.getByRole("button", { name: "Import" }); expect(b).toBeEnabled(); return b; }));
    const res = await screen.findByTestId("import-result");
    expect(res).toHaveTextContent("5 feeds added");
    expect(res).toHaveTextContent("2 folders created");
    expect(res).toHaveTextContent("1 feed was already in Kipple");
    expect(callTo(calls, "POST", "/api/opml")[0]?.url.searchParams.get("mark_read_older_than_days")).toBe("7");
    expect(await axe(container)).toHaveNoViolations();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Recommended feeds");
  });

  it("refuses a file that is not OPML before uploading it", async () => {
    const { calls } = server(signedIn());
    go("/welcome/import");
    await heading();
    await userEvent.setup({ applyAccept: false }).upload(screen.getByLabelText("OPML file"), new File(["hello"], "notes.txt", { type: "text/plain" }));
    expect(await findAlert()).toHaveTextContent("doesn't look like an OPML file");
    expect(callTo(calls, "POST", "/api/opml")).toHaveLength(0);
  });

  it("says what was wrong with a file the server cannot read", async () => {
    server(signedIn(), { "POST /api/opml": () => json({ error: "bad_opml", message: "not an OPML document" }, 400) });
    go("/welcome/import");
    const user = userEvent.setup();
    await heading();
    await user.upload(screen.getByLabelText("OPML file"), file());
    await user.click(await waitFor(() => { const b = screen.getByRole("button", { name: "Import" }); expect(b).toBeEnabled(); return b; }));
    expect(await findAlert()).toHaveTextContent("couldn't read that file: not an OPML document");
  });

  it("checks the days number", async () => {
    server(signedIn());
    go("/welcome/import");
    const user = userEvent.setup();
    await heading();
    await user.upload(screen.getByLabelText("OPML file"), file());
    await user.type(screen.getByLabelText("Mark older articles as read (optional)"), "9000");
    expect(screen.getByText("Enter a whole number from 1 to 365, or leave empty.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
  });

  it("can be skipped, and goes back", async () => {
    server(signedIn());
    go("/welcome/import");
    const user = userEvent.setup();
    await heading();
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Recommended feeds");
    await user.click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Bring your feeds along");
  });
});

describe("Step 6: recommended feeds", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });
  const feed = (id: string, over: Record<string, unknown> = {}) => ({ id, title: `Feed ${id}`, url: `https://${id}.example.com/feed.xml`, site: `https://www.${id}.example.com/`, description: `About ${id}`, checked: true, subscribed: false, ...over });
  const starter = {
    available: true,
    categories: [
      { id: "tech", title: "Technology", feeds: [feed("a"), feed("b", { checked: false, lang: "fr" }), feed("c", { subscribed: true })] },
      { id: "news", title: "News", feeds: [feed("d"), feed("e", { checked: false })] },
    ],
  };

  it("lists every category with its feeds, ticks the suggested ones and passes axe", async () => {
    server(signedIn({ starter }));
    const { container } = go("/welcome/feeds");
    await screen.findByText("Feed a");
    const tech = screen.getByRole("group", { name: "Technology" });
    expect(within(tech).getByRole("checkbox", { name: /Feed a/ })).toBeChecked();
    expect(within(tech).getByRole("checkbox", { name: /Feed b/ })).not.toBeChecked();
    expect(within(tech).getByRole("checkbox", { name: /Feed c/ })).toBeChecked();
    expect(within(tech).getByRole("checkbox", { name: /Feed c/ })).toBeDisabled();
    expect(within(tech).getByText("Already added")).toBeInTheDocument();
    expect(within(tech).getByText(/a\.example\.com/)).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "News" })).toBeInTheDocument();
    expect(screen.getByText("2 feeds selected.")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("selects all or none in a category and sends the chosen ids with the folders choice", async () => {
    const { calls } = server(signedIn({ starter }), { "POST /api/starter-feeds": () => json({ added: 3, existing: 0, run_id: "r" }) });
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    await user.click(screen.getByRole("button", { name: "Select all in Technology" }));
    expect(screen.getByText("3 feeds selected.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Select none in News" }));
    expect(screen.getByText("2 feeds selected.")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /Feed e/ }));
    await user.click(screen.getByRole("switch", { name: /own folder/ }));
    await user.click(screen.getByRole("button", { name: "Add 3 feeds" }));
    await headingIs("You're all set");
    expect(bodyOf(callTo(calls, "POST", "/api/starter-feeds")[0] as never)).toEqual({ ids: ["a", "b", "e"], folders: false });
  });

  it("adds nothing until asked, and Skip sends nothing", async () => {
    const { calls } = server(signedIn({ starter }));
    go("/welcome/feeds");
    await screen.findByText("Feed a");
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("You're all set");
    expect(callTo(calls, "POST", "/api/starter-feeds")).toHaveLength(0);
  });

  it("can have everything unticked", async () => {
    server(signedIn({ starter }));
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    await user.click(screen.getByRole("button", { name: "Select none in Technology" }));
    await user.click(screen.getByRole("button", { name: "Select none in News" }));
    expect(screen.getByText("No feeds selected.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add feeds" })).toBeDisabled();
  });

  it("shows an empty state when there are no recommendations", async () => {
    server(signedIn({ starter: { available: false, categories: [] } }));
    go("/welcome/feeds");
    expect(await screen.findByText("No recommended feeds are available.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add feeds" })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("You're all set");
  });

  it("offers a retry when the list cannot be loaded", async () => {
    let fail = true;
    server(signedIn({ starter }), { "GET /api/starter-feeds": () => (fail ? json({ error: "internal" }, 500) : json(starter)) });
    go("/welcome/feeds");
    expect(await findAlert()).toHaveTextContent("couldn't load the recommended feeds");
    fail = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Feed a")).toBeInTheDocument();
  });

  it.each([
    [{ error: "unknown_id", message: "not a recommended feed: zz" }, 400, /list of recommended feeds changed/],
    [{ error: "unavailable" }, 503, /no recommended feeds to add right now/],
    [{ error: "internal" }, 500, /server returned an error/],
  ])("explains a failed add (%j) and stays on the step", async (body, status, text) => {
    server(signedIn({ starter }), { "POST /api/starter-feeds": () => json(body, status) });
    go("/welcome/feeds");
    await screen.findByText("Feed a");
    await userEvent.setup().click(screen.getByRole("button", { name: /^Add \d feeds?$/ }));
    expect(await findAlert()).toHaveTextContent(text);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Recommended feeds");
  });
});

describe("Step 7: finish", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("generates the API password with the web password from step 2, shows it once with a copy button", async () => {
    setupSecret.set("correct horse");
    const { calls } = server(signedIn(), { "POST /api/account/api-password": () => json({ api_password: "abcd-efgh-ijkl" }) });
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText");
    const { container } = go("/welcome/finish");
    await heading();
    // No second prompt for the password.
    expect(screen.queryByLabelText("Your web password")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Generate API password" }));
    expect(await screen.findByTestId("api-password")).toHaveTextContent("abcd-efgh-ijkl");
    expect(bodyOf(callTo(calls, "POST", "/api/account/api-password")[0] as never)).toEqual({ current: "correct horse", generate: true });
    expect(screen.getByText(/can't show it again/)).toBeInTheDocument();
    expect(screen.getByText(/\/api\/greader\.php/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith("abcd-efgh-ijkl");
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("asks for the web password again after a reload", async () => {
    const { calls } = server(signedIn(), { "POST /api/account/api-password": () => json({ api_password: "zzzz" }) });
    go("/welcome/finish");
    const user = userEvent.setup();
    await heading();
    expect(screen.getByRole("button", { name: "Generate API password" })).toBeDisabled();
    await user.type(screen.getByLabelText("Your web password"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Generate API password" }));
    await screen.findByTestId("api-password");
    expect(bodyOf(callTo(calls, "POST", "/api/account/api-password")[0] as never)).toEqual({ current: "correct horse", generate: true });
  });

  it("needs no password in open mode", async () => {
    const { calls } = server(signedIn({ authMode: "open", passwordSet: false }), { "POST /api/account/api-password": () => json({ api_password: "open-one" }) });
    go("/welcome/finish");
    const user = userEvent.setup();
    await heading();
    expect(screen.queryByLabelText("Your web password")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Generate API password" }));
    await screen.findByTestId("api-password");
    expect(bodyOf(callTo(calls, "POST", "/api/account/api-password")[0] as never)).toEqual({ generate: true });
  });

  it("shows why a refused password was refused", async () => {
    setupSecret.set("wrong");
    server(signedIn(), { "POST /api/account/api-password": () => json({ error: "bad_password" }, 403) });
    go("/welcome/finish");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Generate API password" }));
    expect(await findAlert()).toHaveTextContent("The current password isn't right.");
  });

  it("finishes without an API password, forgets the remembered one and opens the reader", async () => {
    setupSecret.set("correct horse");
    const { calls } = server(signedIn());
    go("/welcome/finish");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Finish" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
    expect(callTo(calls, "POST", "/api/account/api-password")).toHaveLength(0);
    expect(setupSecret.get()).toBeNull();
  });

  it("stays and says so when finishing fails", async () => {
    server(signedIn(), { "POST /api/onboarding/complete": () => json({ error: "internal" }, 500) });
    go("/welcome/finish");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Finish" }));
    await waitFor(() => expect(screen.getAllByText("The server returned an error. Try again.").length).toBeGreaterThan(0));
    expect(screen.getByRole("button", { name: "Finish" })).toBeEnabled();
  });
});

describe("the whole run", () => {
  it("walks all seven steps with a password", async () => {
    browserZoneIs("Asia/Tokyo");
    const w = makeWorld();
    const { calls } = server(w, {
      "GET /api/starter-feeds": () => json({ available: true, categories: [{ id: "x", title: "Sample", feeds: [{ id: "f1", title: "Sample feed", url: "https://f1.example.com/feed", checked: true, subscribed: false }] }] }),
      "POST /api/starter-feeds": () => json({ added: 1, existing: 0, run_id: null }),
    });
    go("/#setup=ABCD-EFGH");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Continue" }));
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.type(screen.getByLabelText("Password again"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your time zone");
    await screen.findByTestId("selected-zone");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Pick a look");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Bring your feeds along");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Recommended feeds");
    await user.click(await screen.findByRole("button", { name: "Add 1 feed" }));
    await headingIs("You're all set");
    await user.click(screen.getByRole("button", { name: "Finish" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(w.tz).toBe("Asia/Tokyo");
    expect(w.pending).toBe(false);
    expect(callTo(calls, "POST", "/api/setup/claim")).toHaveLength(1);
  });
});

describe("Settings: run setup again", () => {
  const done = () => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, pending: false });

  it("restarts the first-run steps and opens the time zone step", async () => {
    const { calls } = server(done());
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Run setup again" }));
    await headingIs("Choose your time zone");
    expect(callTo(calls, "POST", "/api/onboarding/restart")).toHaveLength(1);
    expect(window.location.pathname).toBe("/welcome/timezone");
  });

  it("says so when it cannot restart", async () => {
    server(done(), { "POST /api/onboarding/restart": () => json({ error: "internal" }, 500) });
    go("/settings/account");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Run setup again" }));
    expect(await screen.findByText("The server returned an error. Try again.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run setup again" })).toBeEnabled();
  });

  it("shows the time zone read-only in Settings when the TZ variable is set", async () => {
    server(done(), {
      "GET /api/settings": () => json({ settings: [tzMeta({ ...done(), envTz: "Europe/Paris" })], values: { tz: "Europe/Paris" } }),
    });
    go("/settings/account");
    const field = await screen.findByLabelText("Time zone");
    expect(field).toHaveAttribute("readonly");
    expect(field).toHaveValue("Europe/Paris");
    expect(screen.getByText("Set by the TZ environment variable; remove it to choose here.")).toBeInTheDocument();
  });

  it("starts at the recommended feeds from the empty state", async () => {
    server(done(), { "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [], folders: [], counts: { unread: 0, starred: 0 }, user: { ...bootstrap.user, setup_pending: false } }), "GET /api/items": () => json(pageOf([])), "GET /api/starter-feeds": () => json({ available: false, categories: [] }) });
    go("/l/unread");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Recommended feeds" }));
    await headingIs("Recommended feeds");
    expect(window.location.pathname).toBe("/welcome/feeds");
  });
});
