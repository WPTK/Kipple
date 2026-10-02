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
import { flush, resetDeviceSync, syncStore } from "@/lib/deviceSync";
import { FONTS } from "@/lib/fonts";
import { prefsStore, updatePrefs } from "@/lib/prefs";
import { forgetWizardMemory, setupSecret } from "./session";
import { resetOpenSignInGuard } from "./SetupFlow";

// jsdom has no EventSource; the shell subscribes to one.
class NoES {
  addEventListener() {}
  close() {}
}

type Handler = Parameters<typeof mockFetch>[0][string];

interface World {
  /** What GET /api/instance adds while Kipple has no account. */
  state: { access: { enabled: boolean; verified: boolean }; open: { reason: string | null } };
  instance: { setup: boolean; auth: string | null };
  signedIn: boolean;
  pending: boolean;
  authMode: string;
  passwordSet: boolean;
  tz: string;
  starter: unknown;
}

function makeWorld(over: Partial<World> = {}): World {
  return {
    state: { access: { enabled: false, verified: false }, open: { reason: null } },
    instance: { setup: true, auth: null },
    signedIn: false,
    pending: true,
    authMode: "password",
    passwordSet: true,
    tz: "UTC",
    starter: { available: true, categories: [] },
    ...over,
  };
}

const tzMeta = (w: World) => ({ key: "tz", value: w.tz, default: "UTC", label: "Time zone", description: "Used for statistics.", group: "account", kind: "text", surface: "settings" });

/** A tiny fake Kipple: every route the wizard touches, with the state it changes. Extra routes win over these. */
function server(w: World, extra: Record<string, Handler> = {}) {
  return mockFetch({
    "GET /api/instance": () => json(w.instance.setup ? { ...w.instance, ...w.state } : w.instance),
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
    "GET /api/settings": () => json({ settings: [tzMeta(w)], values: { tz: w.tz } }),
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
  forgetWizardMemory();
  sessionStorage.clear();
  resetOpenSignInGuard();
  resetDeviceSync();
  liveStore.set(initialLive);
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  updatePrefs({ font: "default" });
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Step 1: the first screen is the account", () => {
  it("opens on the account form, with no setup code, and passes axe", async () => {
    const { calls } = server(makeWorld());
    const { container } = go("/");
    await headingIs("Create your account");
    expect(screen.getByText("Step 1 of 6")).toBeInTheDocument();
    expect(screen.queryByLabelText("Setup code")).toBeNull();
    expect(screen.queryByText(/setup code/i)).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
    // Nothing is asked of the server but who it is.
    expect(calls.filter((c) => c.url.pathname.startsWith("/api/setup"))).toHaveLength(0);
  });

  it("ignores a #setup= link from an older Kipple and leaves the address alone", async () => {
    server(makeWorld());
    go("/#setup=ABCD-EFGH-JKMN");
    await headingIs("Create your account");
    expect(window.location.hash).toBe("#setup=ABCD-EFGH-JKMN");
  });

  it("offers a reload when setup finished while the page was open", async () => {
    server(makeWorld(), { "POST /api/setup/account": () => json({ error: "not_found" }, 404) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await findAlert()).toHaveTextContent("Reload the page to sign in");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});

describe("Step 1: account", () => {
  const claimed = () => makeWorld();

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
    // Kept in memory for step 6.
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

  it("explains a network failure without losing the form", async () => {
    let n = 0;
    server(claimed(), { "POST /api/setup/account": () => (++n === 1 ? (Promise.reject(new TypeError("x")) as never) : json({ error: "bad_new_password", message: "no" }, 400)) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("Kipple couldn't reach the server.")).toBeInTheDocument();
    expect(screen.getByLabelText("User name")).toHaveValue("reader");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("No.")).toBeInTheDocument();
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
      expect(screen.getByText(/reachable only from this computer, your local network or your Tailscale network/i, { selector: "p" })).toBeInTheDocument();
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

    it("says what open mode means: this computer, the local network and Tailscale, and the Docker bind rule", async () => {
      const w = claimed();
      w.authMode = "open";
      server(w);
      go("/");
      await choose();
      const warn = screen.getByText(/Anyone who can reach this address/).parentElement as HTMLElement;
      expect(warn).toHaveTextContent(/this computer, your local network or your Tailscale network/);
      expect(warn).toHaveTextContent(/In Docker.*publish its port only on your local network or Tailscale address/);
      expect(screen.queryByRole("checkbox", { name: /Also allow devices/ })).toBeNull();
      expect(screen.getByRole("checkbox", { name: /my local network or Tailscale/ })).toBeInTheDocument();
    });

    it.each([
      ["forwarded", /proxy or tunnel/],
      ["host", /KIPPLE_ALLOWED_HOSTS/],
      ["peer", /Tailscale/],
    ])("cannot be chosen when the gate refuses it for good (%s), and says why", async (reason, text) => {
      const w = claimed();
      w.state.open = { reason };
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
    expect(screen.getByText("Step 4 of 6")).toBeInTheDocument();
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

describe("Step 2: time zone", () => {
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
    await headingIs("Look and feel");
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
    await headingIs("Look and feel");
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

  it("Skip moves on even when the zone cannot be saved", async () => {
    browserZoneIs("Europe/Berlin");
    server(signedIn(), { "PATCH /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/timezone");
    await screen.findByTestId("selected-zone");
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Look and feel");
  });

  it("keeps a zone set to UTC on purpose when setup is run again", async () => {
    browserZoneIs("Asia/Tokyo");
    sessionStorage.setItem("kipple.setup.rerun", "1");
    const { calls } = server(signedIn({ tz: "UTC" }));
    go("/welcome/timezone");
    const sel = await screen.findByTestId("selected-zone");
    expect(sel).toHaveTextContent("UTC");
    expect(sel).toHaveTextContent("Kipple is already set to this zone");
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Look and feel");
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
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

  it("lets you move on when the settings cannot be loaded", async () => {
    server(signedIn(), { "GET /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/timezone");
    expect(await findAlert()).toHaveTextContent("couldn't load its settings");
    await userEvent.setup().click(screen.getByRole("button", { name: "Skip this step" }));
    await headingIs("Look and feel");
  });
});

describe("Step 3: theme", () => {
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
    expect(patches).toContainEqual({ "ui.theme": "system", "ui.theme_day": "linen", "ui.theme_night": "graphite", "ui.font_body": "default" });
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

  it("has a Reading font control with every font, grouped, and previews the pick at once (regression: font choice went missing)", async () => {
    server(signedIn());
    go("/welcome/theme");
    const user = userEvent.setup();
    await headingIs("Look and feel");
    const select = screen.getByRole("combobox", { name: "Reading font" });
    const values = within(select).getAllByRole("option").map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(FONTS.map((f) => f.id));
    for (const g of ["Serif", "Sans-serif", "Monospace", "On this device"]) expect(select.querySelector(`optgroup[label="${g}"]`)).not.toBeNull();
    await user.selectOptions(select, "vollkorn");
    expect(prefsStore.get().font).toBe("vollkorn");
    expect(screen.getByTestId("font-preview").style.fontFamily).toContain("Vollkorn");
  });

  it("Continue saves the reading font with the theme pair; Back shows it again (resumable)", async () => {
    const { calls } = server(signedIn());
    go("/welcome/theme");
    const user = userEvent.setup();
    await headingIs("Look and feel");
    await user.selectOptions(screen.getByLabelText("Reading font"), "inter");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Bring your feeds along");
    expect(callTo(calls, "PATCH", "/api/settings").map((c) => bodyOf(c as never))).toContainEqual({ "ui.theme": "system", "ui.theme_day": "paper", "ui.theme_night": "midnight", "ui.font_body": "inter" });
    await user.click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Look and feel");
    expect(screen.getByLabelText("Reading font")).toHaveValue("inter");
    // Skip now keeps what was saved rather than going back to the font before the wizard.
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    expect(prefsStore.get().font).toBe("inter");
  });

  it("Skip puts this device's reading font back, even after Back and forth", async () => {
    updatePrefs({ font: "gentium" });
    const { calls } = server(signedIn());
    go("/welcome/theme");
    const user = userEvent.setup();
    await headingIs("Look and feel");
    expect(screen.getByLabelText("Reading font")).toHaveValue("gentium");
    await user.selectOptions(screen.getByLabelText("Reading font"), "jetbrains-mono");
    await user.click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Choose your time zone");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Look and feel");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    expect(prefsStore.get().font).toBe("gentium");
    expect(callTo(calls, "PATCH", "/api/settings").filter((c) => "ui.font_body" in bodyOf(c as never))).toHaveLength(0);
  });

  it("still puts the original theme back on Skip after going Back and forth", async () => {
    server(signedIn());
    go("/welcome/theme");
    const user = userEvent.setup();
    await heading();
    await user.selectOptions(screen.getByLabelText("Night theme"), "carbon");
    await user.click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Choose your time zone");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Look and feel");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    expect(themeStore.get()).toEqual(DEFAULT_THEME_SETTINGS);
  });

  it("puts an unsaved pick back when setup is ended from another step", async () => {
    server(signedIn());
    go("/welcome/theme");
    const user = userEvent.setup();
    await heading();
    await user.selectOptions(screen.getByLabelText("Day theme"), "linen");
    await user.click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Choose your time zone");
    await user.click(screen.getByRole("button", { name: "Skip the rest of setup" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(themeStore.get()).toEqual(DEFAULT_THEME_SETTINGS);
  });

  it("reports a failed save and stays", async () => {
    server(signedIn(), { "PATCH /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/theme");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    expect(await findAlert()).toHaveTextContent("The server returned an error");
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Look and feel");
  });

  it("goes back to the time zone", async () => {
    server(signedIn());
    go("/welcome/theme");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Choose your time zone");
  });
});

describe("Step 4: OPML import", () => {
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

describe("Step 5: recommended feeds", () => {
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
    expect(screen.queryByText(/^FR$/i)).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "News" })).toBeInTheDocument();
    expect(screen.getByText("2 feeds selected.")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("has one select all or none for the whole list and sends the chosen ids with the folders choice", async () => {
    const { calls } = server(signedIn({ starter }), { "POST /api/starter-feeds": () => json({ added: 3, existing: 0, run_id: "r" }) });
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    expect(screen.getAllByRole("button", { name: /^Select (all|none)$/ })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Select all" }));
    expect(screen.getByText("4 feeds selected.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Select none" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Select none" }));
    expect(screen.getByText("No feeds selected.")).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    await user.click(screen.getByRole("checkbox", { name: /Feed b/ }));
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
    await user.click(screen.getByRole("button", { name: "Select all" }));
    await user.click(screen.getByRole("button", { name: "Select none" }));
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

describe("Step 6: finish", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("generates the API password with the web password from step 1, shows it once with a copy button", async () => {
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
  it("walks all six steps with a password", async () => {
    browserZoneIs("Asia/Tokyo");
    const w = makeWorld();
    const { calls } = server(w, {
      "GET /api/starter-feeds": () => json({ available: true, categories: [{ id: "x", title: "Sample", feeds: [{ id: "f1", title: "Sample feed", url: "https://f1.example.com/feed", checked: true, subscribed: false }] }] }),
      "POST /api/starter-feeds": () => json({ added: 1, existing: 0, run_id: null }),
    });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.type(screen.getByLabelText("Password again"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your time zone");
    await screen.findByTestId("selected-zone");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Look and feel");
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
    expect(callTo(calls, "POST", "/api/setup/account")).toHaveLength(1);
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
    await waitFor(() => expect(screen.getAllByText("The server returned an error. Try again.").length).toBeGreaterThan(0));
    expect(screen.getByRole("button", { name: "Run setup again" })).toBeEnabled();
  });

  it("offers no Sign out without a password, where Kipple would sign the browser straight back in", async () => {
    server({ ...done(), authMode: "open", passwordSet: false } as World);
    go("/settings/account");
    await screen.findByRole("button", { name: "Run setup again" });
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });

  it("drops what the wizard remembered when the app signs out", async () => {
    setupSecret.set("correct horse");
    server(done());
    go("/settings/account");
    await screen.findByRole("button", { name: "Sign out" });
    act(() => authStore.set("out"));
    await waitFor(() => expect(setupSecret.get()).toBeNull());
  });

  it("starts at the recommended feeds from the empty state", async () => {
    server(done(), { "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [], folders: [], counts: { unread: 0, starred: 0 }, user: { ...bootstrap.user, setup_pending: false } }), "GET /api/items": () => json(pageOf([])), "GET /api/starter-feeds": () => json({ available: false, categories: [] }) });
    go("/l/unread");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Recommended feeds" }));
    await headingIs("Recommended feeds");
    expect(window.location.pathname).toBe("/welcome/feeds");
  });
});

// ---- review fixes: the signed-out screen, skip-all, the theme preview and the small things around them ----

describe("Skip the rest of setup from step 2 keeps the time zone", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("saves the preselected zone before it ends setup", async () => {
    browserZoneIs("America/New_York");
    const w = signedIn();
    const { calls } = server(w);
    go("/welcome/timezone");
    await headingIs("Choose your time zone");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Skip the rest of setup" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(w.tz).toBe("America/New_York");
    const order = calls.filter((c) => (c.method === "PATCH" && c.url.pathname === "/api/settings") || c.url.pathname === "/api/onboarding/complete").map((c) => c.url.pathname);
    expect(order).toEqual(["/api/settings", "/api/onboarding/complete"]);
  });

  it("still ends setup when the zone cannot be saved", async () => {
    browserZoneIs("America/New_York");
    const { calls } = server(signedIn(), { "PATCH /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/timezone");
    await headingIs("Choose your time zone");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Skip the rest of setup" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
  });
});

describe("a stale /welcome/<step> address before the account exists", () => {
  it("does not carry the new account past step 2", async () => {
    server(makeWorld());
    go("/welcome/theme");
    const user = userEvent.setup();
    await headingIs("Create your account");
    expect(window.location.pathname).toBe("/");
    await user.type(screen.getByLabelText("User name"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.type(screen.getByLabelText("Password again"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your time zone");
    expect(window.location.pathname).toBe("/welcome/timezone");
  });
});

describe("the signed-out screen when GET /api/instance does not answer", () => {
  it.each([
    ["a server error", () => json({ error: "internal" }, 500), /answered with something unexpected/],
    ["a refused address", () => json({ error: "misdirected" }, 421), /KIPPLE_ALLOWED_HOSTS/],
    ["no network", () => Promise.reject(new TypeError("offline")) as never, /couldn't reach the server/],
  ])("says so, with Try again, instead of a password form (%s)", async (_n, answer, text) => {
    const w = makeWorld({ instance: { setup: false, auth: "open" }, authMode: "open", pending: false, passwordSet: false });
    let broken = true;
    server(w, { "GET /api/instance": (() => (broken ? answer() : json(w.instance))) as Handler });
    go("/");
    expect(await findAlert()).toHaveTextContent(text);
    expect(screen.queryByLabelText("Password")).toBeNull();
    broken = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    // The instance is in open mode: it signs in by itself.
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
  });

  it("still shows the sign-in form when an older server answers with a page that is not JSON", async () => {
    server(makeWorld(), { "GET /api/instance": () => new Response("<html></html>", { status: 200, headers: { "Content-Type": "text/html" } }) });
    go("/");
    expect(await screen.findByRole("heading", { name: "Sign in to Kipple" })).toBeInTheDocument();
  });

  it("still shows the sign-in form for a 401", async () => {
    server(makeWorld(), { "GET /api/instance": () => json({ error: "auth" }, 401) });
    go("/");
    expect(await screen.findByRole("heading", { name: "Sign in to Kipple" })).toBeInTheDocument();
  });
});

describe("the sign-in form meets an open-mode Kipple", () => {
  it("moves to the silent sign-in on a 409 open_mode", async () => {
    const w = makeWorld({ instance: { setup: false, auth: "password" }, authMode: "open", pending: false, passwordSet: false });
    const { calls } = server(w, {
      "POST /api/auth/login": () => {
        // The mode changed since the form was drawn.
        w.instance = { setup: false, auth: "open" };
        return json({ error: "open_mode", message: "this Kipple has no password; it signs in without one" }, 409);
      },
    });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/auth/open")).toHaveLength(1);
  });
});

describe("the sign-in form meets a Kipple with no account", () => {
  it("moves to the account form on a 409 setup_required", async () => {
    const w = makeWorld({ instance: { setup: false, auth: "password" } });
    server(w, {
      "POST /api/auth/login": () => {
        // The instance was reset since the form was drawn.
        w.instance = { setup: true, auth: null };
        return json({ error: "setup_required", message: "Kipple has no account yet; create it first" }, 409);
      },
    });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await headingIs("Create your account");
  });
});

describe("open-mode sign-in edge cases", () => {
  const openWorld = () => makeWorld({ instance: { setup: false, auth: "open" }, authMode: "open", pending: false, passwordSet: false });

  it("stops after one try when the browser does not keep the session cookie, and says why", async () => {
    // The sign-in "succeeds" but the next request is still a 401: nothing was stored.
    const { calls } = server(openWorld(), { "POST /api/auth/open": () => new Response(null, { status: 204 }) });
    go("/");
    await headingIs("Kipple needs cookies to keep you signed in");
    // Give a runaway loop time to show itself.
    await new Promise((r) => setTimeout(r, 300));
    expect(callTo(calls, "POST", "/api/auth/open")).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Kipple needs cookies");
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(callTo(calls, "POST", "/api/auth/open")).toHaveLength(2));
  });

  it("goes to the sign-in form when the account has left open mode, instead of failing the same way every time", async () => {
    const w = openWorld();
    server(w, {
      "POST /api/auth/open": () => {
        w.instance = { setup: false, auth: "password" };
        return json({ error: "not_found" }, 404);
      },
    });
    go("/");
    expect(await screen.findByRole("heading", { name: "Sign in to Kipple" })).toBeInTheDocument();
  });

  it("asks again what the mode is when Try again is pressed after a failure", async () => {
    const w = openWorld();
    let fail = true;
    const { calls } = server(w, {
      "POST /api/auth/open": () => {
        if (fail) return json({ error: "internal" }, 500);
        w.signedIn = true;
        return new Response(null, { status: 204 });
      },
    });
    go("/");
    await screen.findByText("Kipple couldn't sign you in");
    const before = callTo(calls, "GET", "/api/instance").length;
    fail = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "GET", "/api/instance").length).toBeGreaterThan(before);
  });
});

describe("a step that is saving cannot be left", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });
  const never = () => new Promise<Response>(() => undefined);

  it("theme: Back, Skip and Skip the rest are disabled while Continue saves", async () => {
    server(signedIn(), { "PATCH /api/settings": never as Handler });
    go("/welcome/theme");
    await heading();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Saving" })).toBeDisabled());
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Skip" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Skip the rest of setup" })).toBeDisabled();
  });

  it("feeds: Back, Skip and Skip the rest are disabled while Add saves", async () => {
    const starter = { available: true, categories: [{ id: "c", title: "News", feeds: [{ id: "f1", title: "One", url: "https://one.example/feed", checked: true, subscribed: false }] }] };
    server(signedIn({ starter }), { "POST /api/starter-feeds": never as Handler });
    go("/welcome/feeds");
    await screen.findByText("One");
    await userEvent.setup().click(screen.getByRole("button", { name: "Add 1 feed" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Adding" })).toBeDisabled());
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Skip" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Skip the rest of setup" })).toBeDisabled();
  });

  it("import: Back, Skip and Skip the rest are disabled while the file uploads", async () => {
    server(signedIn(), { "POST /api/opml": never as Handler });
    go("/welcome/import");
    await heading();
    const user = userEvent.setup();
    await user.upload(screen.getByLabelText("OPML file"), new File(['<?xml version="1.0"?><opml/>'], "feeds.opml"));
    await user.click(
      await waitFor(() => {
        const b = screen.getByRole("button", { name: "Import" });
        expect(b).toBeEnabled();
        return b;
      }),
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Importing" })).toBeDisabled());
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Skip" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Skip the rest of setup" })).toBeDisabled();
  });

  it("import: refuses a file over the server's 8 MB limit before uploading it", async () => {
    const { calls } = server(signedIn());
    go("/welcome/import");
    await heading();
    const big = new File(["<opml/>"], "huge.opml");
    Object.defineProperty(big, "size", { value: 9 * 1024 * 1024 });
    await userEvent.setup().upload(screen.getByLabelText("OPML file"), big);
    expect(await findAlert()).toHaveTextContent(/too large.*8 MB/);
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    expect(callTo(calls, "POST", "/api/opml")).toHaveLength(0);
  });
});

describe("what the wizard keeps in memory", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("drops the step 1 password when setup turns out to be over", async () => {
    setupSecret.set("correct horse");
    server(signedIn({ pending: false }));
    go("/welcome/finish");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(setupSecret.get()).toBeNull();
  });

  it("warns before a second API password replaces the one shown once", async () => {
    setupSecret.set("correct horse");
    server(signedIn(), { "POST /api/account/api-password": () => json({ api_password: "abcd-efgh-ijkl" }) });
    go("/welcome/finish");
    await heading();
    const user = userEvent.setup();
    expect(screen.queryByText(/already made an API password/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Generate API password" }));
    await screen.findByTestId("api-password");
    await user.click(screen.getByRole("button", { name: "Back" }));
    await headingIs("Recommended feeds");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("You're all set");
    expect(screen.getByText(/already made an API password.*can't show it again/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace API password" })).toBeInTheDocument();
  });

  it("shows no web password field for an account whose password is not known yet", async () => {
    mockFetch({ "GET /api/bootstrap": () => new Promise<Response>(() => undefined) });
    const { FinishStep } = await import("./FinishStep");
    const { QueryClientProvider } = await import("@tanstack/react-query");
    render(
      <QueryClientProvider client={makeQueryClient({ retry: false })}>
        <FinishStep onBack={() => undefined} onFinish={() => undefined} />
      </QueryClientProvider>,
    );
    await headingIs("You're all set");
    expect(screen.queryByLabelText("Your web password")).toBeNull();
    expect(screen.getByRole("button", { name: "Generate API password" })).toBeDisabled();
  });
});

describe("step 1: accessibility and small things", () => {
  const claimed = () => makeWorld();

  it("checks the password while typing without announcing it on every keystroke", async () => {
    server(claimed());
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Password"), "ab");
    expect(screen.queryByText("Use at least 5 characters.")).toBeNull();
    await user.type(screen.getByLabelText("Password again"), "x");
    expect(screen.queryByText("The two passwords don't match.")).toBeNull();
    await user.tab();
    expect(await screen.findByText("The two passwords don't match.")).toBeInTheDocument();
    expect(screen.getByText("Use at least 5 characters.")).toBeInTheDocument();
  });
});

describe("previewing a theme does not write this device's profile", () => {
  const signedIn = () => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true });
  const d = DEFAULT_THEME_SETTINGS;
  const merged = { "ui.theme": "system", "ui.theme_day": d.day, "ui.theme_night": d.night, "ui.theme_schedule": d.schedule, "ui.theme_night_start": d.nightStart, "ui.theme_day_start": d.dayStart };
  const deviceRoutes = (w: World) => ({
    "GET /api/bootstrap": () =>
      json({ ...bootstrap, device: { id: "dev1", name: "This device", profile: {}, merged }, user: { ...bootstrap.user, username: "reader", password_set: true, auth_mode: "password", setup_pending: w.pending } }),
    "PATCH /api/device": () => json({ id: "dev1", name: "This device", profile: {}, merged }),
  });
  const themeKeys = (calls: { method: string; url: URL; init?: RequestInit }[]) => callTo(calls, "PATCH", "/api/device").flatMap((c) => Object.keys(bodyOf(c as never)).filter((k) => k.startsWith("ui.theme")));
  const hydrated = () => waitFor(() => expect(syncStore.get().status).toBe("idle"));

  it("Skip after a preview leaves the profile without theme overrides", async () => {
    const w = signedIn();
    const { calls } = server(w, deviceRoutes(w));
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Day theme"), "linen");
    expect(themeStore.get().day).toBe("linen");
    await flush();
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    await flush();
    expect(themeStore.get()).toEqual(DEFAULT_THEME_SETTINGS);
    expect(themeKeys(calls)).toEqual([]);
  });

  it("Continue saves the default and does not pin the device to it either", async () => {
    const w = signedIn();
    const { calls } = server(w, deviceRoutes(w));
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Day theme"), "linen");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Bring your feeds along");
    await flush();
    expect(callTo(calls, "PATCH", "/api/settings").map((c) => bodyOf(c as never))).toContainEqual({ "ui.theme": "system", "ui.theme_day": "linen", "ui.theme_night": "midnight", "ui.font_body": "default" });
    expect(themeKeys(calls)).toEqual([]);
    expect(themeStore.get().day).toBe("linen");
  });

  it("Continue still writes a pick to a device that already overrides that theme (a changed default would not move it)", async () => {
    const w = signedIn();
    const own = { ...merged, "ui.theme_day": "graphite" };
    const { calls } = server(w, {
      "GET /api/bootstrap": () => json({ ...bootstrap, device: { id: "dev1", name: "This device", profile: { "ui.theme_day": "graphite" }, merged: own }, user: { ...bootstrap.user, username: "reader", password_set: true, auth_mode: "password", setup_pending: w.pending } }),
      "PATCH /api/device": () => json({ id: "dev1", name: "This device", profile: { "ui.theme_day": "linen" }, merged: { ...own, "ui.theme_day": "linen" } }),
    });
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Day theme"), "linen");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Bring your feeds along");
    await flush();
    expect(themeKeys(calls)).toEqual(["ui.theme_day"]);
  });

  const fontKeys = (calls: { method: string; url: URL; init?: RequestInit }[]) => callTo(calls, "PATCH", "/api/device").flatMap((c) => Object.keys(bodyOf(c as never)).filter((k) => k === "ui.font_body"));

  it("a reading-font preview and Skip leave no font override in the profile", async () => {
    const w = signedIn();
    const { calls } = server(w, deviceRoutes(w));
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Reading font"), "manrope");
    expect(prefsStore.get().font).toBe("manrope");
    await flush();
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    await flush();
    expect(prefsStore.get().font).toBe("default");
    expect(fontKeys(calls)).toEqual([]);
  });

  it("Continue saves the font as the default and does not pin the device to it", async () => {
    const w = signedIn();
    const { calls } = server(w, deviceRoutes(w));
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Reading font"), "source-serif");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await headingIs("Bring your feeds along");
    await flush();
    expect(callTo(calls, "PATCH", "/api/settings").map((c) => bodyOf(c as never)["ui.font_body"])).toEqual(["source-serif"]);
    expect(fontKeys(calls)).toEqual([]);
    expect(prefsStore.get().font).toBe("source-serif");
  });

  it("a font change outside the wizard still syncs normally once the preview is over", async () => {
    const w = signedIn();
    const { calls } = server(w, deviceRoutes(w));
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Reading font"), "arvo");
    await user.click(screen.getByRole("button", { name: "Skip" }));
    await headingIs("Bring your feeds along");
    act(() => updatePrefs({ font: "inter" }));
    await flush();
    expect(fontKeys(calls)).toEqual(["ui.font_body"]);
  });

  it("ending setup elsewhere with an unsaved preview leaves the profile alone too", async () => {
    const w = signedIn();
    const { calls } = server(w, deviceRoutes(w));
    go("/welcome/theme");
    await heading();
    await hydrated();
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Night theme"), "carbon");
    await user.selectOptions(screen.getByLabelText("Reading font"), "vollkorn");
    await flush();
    await user.click(screen.getByRole("button", { name: "Skip the rest of setup" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    await flush();
    expect(themeStore.get()).toEqual(DEFAULT_THEME_SETTINGS);
    expect(prefsStore.get().font).toBe("default");
    expect(themeKeys(calls)).toEqual([]);
    expect(fontKeys(calls)).toEqual([]);
  });
});
