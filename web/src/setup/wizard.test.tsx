import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore, openRefusedStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf, setSessionMarker } from "@/test/mockApi";
import { themeStore } from "@/theme/theme";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { resetDeviceSync } from "@/lib/deviceSync";
import { updatePrefs } from "@/lib/prefs";
import { endRerun, markRerun, rerunStore } from "./session";
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
    tz: "", // unset until chosen
    starter: { available: true, categories: [] },
    ...over,
  };
}

const tzMeta = (w: World) => ({ key: "tz", value: w.tz, default: "", label: "Time zone", description: "Used for statistics.", group: "account", kind: "text", surface: "settings" });

/** A tiny fake Kipple: every route the wizard touches, with the state it changes. Extra routes win over these. */
function server(w: World, extra: Record<string, Handler> = {}) {
  // The browser holds the session marker exactly when the server has signed it in.
  setSessionMarker(w.signedIn);
  return mockFetch({
    "GET /api/instance": () => json(w.instance.setup ? { ...w.instance, ...w.state, signed_in: false } : { ...w.instance, signed_in: w.signedIn }),
    "POST /api/setup/account": () => {
      w.signedIn = true;
      setSessionMarker(true);
      w.instance = { setup: false, auth: "password" };
      return json({ username: "reader", auth_mode: w.authMode }, 201);
    },
    "POST /api/auth/open": () => {
      w.signedIn = true;
      setSessionMarker(true);
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
  endRerun();
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
    expect(screen.getByText("Step 1 of 2")).toBeInTheDocument();
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
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await findAlert()).toHaveTextContent("Reload the page to sign in");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});

describe("Step 1: account", () => {
  const claimed = () => makeWorld();

  it("creates the account with a password, signs in and continues at the feeds", async () => {
    const w = claimed();
    const { calls } = server(w);
    const { container } = go("/");
    const user = userEvent.setup();
    await headingIs("Create your account");
    expect(document.title).toBe("Step 1 of 2: Create your account - Kipple");
    expect(await axe(container)).toHaveNoViolations();
    await user.type(screen.getByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your feeds");
    expect(document.title).toBe("Step 2 of 2: Choose your feeds - Kipple");
    expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", password: "correct horse" });
    expect(window.location.pathname).toBe("/welcome/feeds");
  });

  it("checks the username and password before sending", async () => {
    const { calls } = server(claimed());
    go("/");
    const user = userEvent.setup();
    await screen.findByLabelText("Username");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("Enter a username.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Username"), "no spaces");
    expect(screen.getByText(/1 to 64 letters, digits, dots, dashes or underscores/, { selector: "[role=alert] span" })).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Username"));
    await user.type(screen.getByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "abcd");
    expect(screen.getByText("Use at least 8 characters.")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Password"));
    await user.type(screen.getByLabelText("Password"), "change-me");
    expect(screen.getByText(/example password/)).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Password"));
    await user.type(screen.getByLabelText("Password"), "long enough");
    // An empty password has no live check: the submit says what is missing, once, and puts the cursor there.
    await user.clear(screen.getByLabelText("Password"));
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(screen.getAllByText("Enter a password.")).toHaveLength(1);
    expect(screen.getByLabelText("Password")).toHaveFocus();
    expect(callTo(calls, "POST", "/api/setup/account")).toHaveLength(0);
    await user.type(screen.getByLabelText("Password"), "l");
    expect(screen.queryByText("Enter a password.")).toBeNull();
  });

  it("shows the server's word on a bad password and keeps what was typed", async () => {
    server(claimed(), { "POST /api/setup/account": () => json({ error: "bad_new_password", message: "the password must not be the example value" }, 400) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("The password must not be the example value.")).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toHaveValue("reader");
  });

  it("says Kipple was set up a moment ago when someone else got there first", async () => {
    server(claimed(), { "POST /api/setup/account": () => json({ error: "already_set_up", message: "sign in instead" }, 409) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await findAlert()).toHaveTextContent(/set up a moment ago/);
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });

  it("explains a network failure without losing the form", async () => {
    let n = 0;
    server(claimed(), { "POST /api/setup/account": () => (++n === 1 ? (Promise.reject(new TypeError("x")) as never) : json({ error: "bad_new_password", message: "no" }, 400)) });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "long enough");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("Kipple can't be reached.")).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toHaveValue("reader");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    expect(await screen.findByText("No.")).toBeInTheDocument();
  });

  describe("no password, open mode", () => {
    const choose = async () => {
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText("Username"), "reader");
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
      await headingIs("Choose your feeds");
      expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", passwordless: "open", acknowledge_open: true });
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
      ["host", /Other names for this server in Settings/],
      ["peer", /Tailscale/],
    ])("cannot be chosen when the gate refuses it for good (%s), and says why", async (reason, text) => {
      const w = claimed();
      w.state.open = { reason };
      server(w);
      go("/");
      await screen.findByLabelText("Username");
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
      await screen.findByLabelText("Username");
      expect(screen.queryByRole("radio", { name: /Cloudflare Access/ })).toBeNull();
    });

    it("is offered but disabled until this request came through Access", async () => {
      const w = claimed();
      w.state.access = { enabled: true, verified: false };
      server(w);
      go("/");
      await screen.findByLabelText("Username");
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
      await user.type(await screen.findByLabelText("Username"), "reader");
      await user.click(screen.getByRole("radio", { name: /Cloudflare Access/ }));
      await user.click(screen.getByRole("button", { name: "Create my account" }));
      await headingIs("Choose your feeds");
      expect(bodyOf(callTo(calls, "POST", "/api/setup/account")[0] as never)).toEqual({ username: "reader", passwordless: "access" });
    });

    it("explains a refusal for lack of an Access sign-in", async () => {
      const w = claimed();
      w.state.access = { enabled: true, verified: true };
      server(w, { "POST /api/setup/account": () => json({ error: "access_required" }, 403) });
      go("/");
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText("Username"), "reader");
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
    ["host", /Other names for this server in Settings/],
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

  it("sends an account with setup pending to the feeds step from anywhere", async () => {
    server(signedIn());
    go("/l/unread");
    await headingIs("Choose your feeds");
    expect(window.location.pathname).toBe("/welcome/feeds");
  });

  it("does not, once setup is done, and sends /welcome to the reader", async () => {
    server(signedIn({ pending: false }));
    go("/welcome/feeds");
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/l/unread");
  });

  it("turns any other step address into the feeds step", async () => {
    server(signedIn());
    go("/welcome/timezone");
    await headingIs("Choose your feeds");
    expect(window.location.pathname).toBe("/welcome/feeds");
  });

  it("shows step 2 of 2", async () => {
    server(signedIn());
    go("/welcome/feeds");
    await headingIs("Choose your feeds");
    expect(screen.getByText("Step 2 of 2")).toBeInTheDocument();
  });

  it("moves focus to the step's heading", async () => {
    server(signedIn());
    go("/welcome/feeds");
    const h = await heading();
    await waitFor(() => expect(h).toHaveFocus());
  });

  it("offers one way out of setup: no Skip, no Back, no 'Skip the rest of setup'", async () => {
    server(signedIn());
    go("/welcome/feeds");
    await heading();
    expect(screen.queryByRole("button", { name: /skip/i })).toBeNull();
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
    expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled();
  });
});

describe("Step 2: the feeds", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });
  const feed = (id: string, over: Record<string, unknown> = {}) => ({ id, title: `Feed ${id}`, url: `https://${id}.example.com/feed.xml`, site: `https://www.${id}.example.com/`, description: `About ${id}`, subscribed: false, ...over });
  const starter = {
    available: true,
    categories: [
      { id: "tech", title: "Technology", feeds: [feed("a"), feed("b", { lang: "fr" }), feed("c", { subscribed: true })] },
      { id: "news", title: "News", feeds: [feed("d"), feed("e")] },
    ],
  };

  it("lists every category with its feeds, ticks none and passes axe", async () => {
    server(signedIn({ starter }));
    const { container } = go("/welcome/feeds");
    await screen.findByText("Feed a");
    const tech = screen.getByRole("group", { name: "Technology" });
    expect(within(tech).getByRole("checkbox", { name: /Feed a/ })).not.toBeChecked();
    expect(within(tech).getByRole("checkbox", { name: /Feed b/ })).not.toBeChecked();
    expect(within(tech).getByRole("checkbox", { name: /Feed c/ })).toBeChecked();
    expect(within(tech).getByRole("checkbox", { name: /Feed c/ })).toBeDisabled();
    expect(within(tech).getByText("Already added")).toBeInTheDocument();
    expect(within(tech).getByText(/a\.example\.com/)).toBeInTheDocument();
    expect(screen.queryByText(/^FR$/i)).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "News" })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("says Continue until something is ticked, then Add N feeds", async () => {
    server(signedIn({ starter }));
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: /^Add/ })).toBeNull();
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    expect(screen.getByRole("button", { name: "Add 1 feed" })).toBeEnabled();
    await user.click(screen.getByRole("checkbox", { name: /Feed d/ }));
    expect(screen.getByRole("button", { name: "Add 2 feeds" })).toBeEnabled();
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    await user.click(screen.getByRole("checkbox", { name: /Feed d/ }));
    expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled();
  });

  it("has one select all or none for the whole list and sends the chosen ids with the folders choice", async () => {
    const { calls } = server(signedIn({ starter }), { "POST /api/starter-feeds": () => json({ added: 3, existing: 0, run_id: "r" }) });
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    expect(screen.getAllByRole("button", { name: /^Select (all|none)$/ })).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Select all" }));
    expect(screen.getByRole("button", { name: "Add 4 feeds" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Select none" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Select none" }));
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    await user.click(screen.getByRole("checkbox", { name: /Feed b/ }));
    await user.click(screen.getByRole("checkbox", { name: /Feed e/ }));
    await user.click(screen.getByRole("checkbox", { name: /own folder/ }));
    await user.click(screen.getByRole("button", { name: "Add 3 feeds" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(bodyOf(callTo(calls, "POST", "/api/starter-feeds")[0] as never)).toEqual({ ids: ["a", "b", "e"], folders: false });
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
  });

  it("ends setup even when the bootstrap refresh that adding feeds starts answers after setup is marked done", async () => {
    const w = signedIn({ starter });
    let bootstraps = 0;
    server(w, {
      "POST /api/starter-feeds": () => json({ added: 1, existing: 0, run_id: "r" }),
      // The refresh started by adding: it is answered from the server's state at the time it was asked (pending), late.
      "GET /api/bootstrap": async () => {
        const pendingThen = w.pending;
        if (++bootstraps > 1) await new Promise((r) => setTimeout(r, 300));
        return json({ ...bootstrap, user: { ...bootstrap.user, username: "reader", password_set: w.passwordSet, auth_mode: w.authMode, setup_pending: bootstraps > 1 ? pendingThen : w.pending } });
      },
    });
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    await user.click(screen.getByRole("button", { name: "Add 1 feed" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
  });

  it("Continue with nothing ticked adds nothing and ends setup in the reader", async () => {
    const { calls } = server(signedIn({ starter }));
    go("/welcome/feeds");
    await screen.findByText("Feed a");
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/starter-feeds")).toHaveLength(0);
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
    // The reader is an arrival, not a page load: focus is on its heading, not left on the page (#499).
    await waitFor(() => expect(document.activeElement?.hasAttribute("data-route-heading")).toBe(true));
    // The empty states are not live regions, so nothing is read twice on arrival.
    expect(document.querySelectorAll('[role="status"] h2')).toHaveLength(0);
  });

  it("shows an empty state when there are no recommendations, and Continue still ends setup", async () => {
    server(signedIn({ starter: { available: false, categories: [] } }));
    go("/welcome/feeds");
    expect(await screen.findByText("No recommended feeds are available.")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
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
    [{ error: "internal" }, 500, /server had a problem/],
  ])("explains a failed add (%j) and stays on the step", async (body, status, text) => {
    server(signedIn({ starter }), { "POST /api/starter-feeds": () => json(body, status) });
    go("/welcome/feeds");
    await screen.findByText("Feed a");
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    await user.click(screen.getByRole("button", { name: "Add 1 feed" }));
    expect(await findAlert()).toHaveTextContent(text);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Choose your feeds");
  });

  it("stays on the step and says so when ending setup fails", async () => {
    server(signedIn(), { "POST /api/onboarding/complete": () => json({ error: "internal" }, 500) });
    go("/welcome/feeds");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Couldn't finish setup. Kipple's server had a problem.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Choose your feeds");
  });

  it("disables the button while adding saves", async () => {
    const never = () => new Promise<Response>(() => undefined);
    server(signedIn({ starter }), { "POST /api/starter-feeds": never as Handler });
    go("/welcome/feeds");
    await screen.findByText("Feed a");
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox", { name: /Feed a/ }));
    await user.click(screen.getByRole("button", { name: "Add 1 feed" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Adding" })).toBeDisabled());
  });

  it("imports a feeds file from the link, in the dialog Settings uses, and stays on the step", async () => {
    let imported = false; // the file holds a recommended feed: once imported, the list says it is already added
    const afterImport = { ...starter, categories: starter.categories.map((c) => ({ ...c, feeds: c.feeds.map((f) => (f.id === "a" ? { ...f, subscribed: true } : f)) })) };
    const { calls } = server(signedIn({ starter }), {
      "POST /api/opml": () => {
        imported = true;
        return json({ folders_created: 0, feeds_added: 2, feeds_existing: [], folders_merged_case: [], memberships_dropped: [] });
      },
      "GET /api/starter-feeds": () => json(imported ? afterImport : starter),
    });
    go("/welcome/feeds");
    const user = userEvent.setup();
    await screen.findByText("Feed a");
    await user.click(screen.getByRole("button", { name: "Import a feeds file" }));
    const dialog = await screen.findByRole("dialog");
    await user.upload(within(dialog).getByLabelText("OPML file"), new File(['<?xml version="1.0"?><opml version="2.0"><body/></opml>'], "feeds.opml"));
    await user.click(
      await waitFor(() => {
        const b = within(dialog).getByRole("button", { name: "Import" });
        expect(b).toBeEnabled();
        return b;
      }),
    );
    expect(await screen.findByRole("heading", { name: "Import finished" })).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/opml")).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Choose your feeds");
    // A recommended feed that came in with the file shows as added, not as a choice still to make.
    await waitFor(() => expect(screen.getByRole("checkbox", { name: /Feed a/ })).toBeDisabled());
    expect(screen.getByRole("checkbox", { name: /Feed a/ })).toBeChecked();
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(0);
  });
});

describe("the time zone", () => {
  const signedIn = (over: Partial<World> = {}) => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, ...over });

  it("is the browser's, saved when setup ends and before it is marked done", async () => {
    browserZoneIs("America/New_York");
    const w = signedIn();
    const { calls } = server(w);
    go("/welcome/feeds");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(w.tz).toBe("America/New_York");
    const order = calls.filter((c) => (c.method === "PATCH" && c.url.pathname === "/api/settings") || c.url.pathname === "/api/onboarding/complete").map((c) => c.url.pathname);
    expect(order).toEqual(["/api/settings", "/api/onboarding/complete"]);
  });

  it("leaves a zone somebody chose alone", async () => {
    browserZoneIs("America/New_York");
    const w = signedIn({ tz: "Asia/Tokyo" });
    const { calls } = server(w);
    go("/welcome/feeds");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
    expect(w.tz).toBe("Asia/Tokyo");
  });

  it("leaves an explicit UTC alone", async () => {
    browserZoneIs("America/New_York");
    const w = signedIn({ tz: "UTC" });
    const { calls } = server(w);
    go("/welcome/feeds");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
    expect(w.tz).toBe("UTC");
  });

  it("is kept as saved when setup is run again", async () => {
    browserZoneIs("America/New_York");
    markRerun();
    const w = signedIn({ pending: false });
    const { calls } = server(w);
    go("/welcome/feeds");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "PATCH", "/api/settings")).toHaveLength(0);
    expect(w.tz).toBe("");
  });

  it("does not hold setup back when it cannot be saved", async () => {
    browserZoneIs("America/New_York");
    const { calls } = server(signedIn(), { "PATCH /api/settings": () => json({ error: "internal" }, 500) });
    go("/welcome/feeds");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
  });
});

describe("the whole run", () => {
  it("is two steps: the account, then the feeds", async () => {
    browserZoneIs("Asia/Tokyo");
    const w = makeWorld();
    const { calls } = server(w, {
      "GET /api/starter-feeds": () => json({ available: true, categories: [{ id: "x", title: "Sample", feeds: [{ id: "f1", title: "Sample feed", url: "https://f1.example.com/feed", subscribed: false }] }] }),
      "POST /api/starter-feeds": () => json({ added: 1, existing: 0, run_id: null }),
    });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your feeds");
    await user.click(await screen.findByRole("checkbox", { name: /Sample feed/ }));
    await user.click(screen.getByRole("button", { name: "Add 1 feed" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(w.tz).toBe("Asia/Tokyo");
    expect(w.pending).toBe(false);
    expect(callTo(calls, "POST", "/api/setup/account")).toHaveLength(1);
  });
});

describe("Settings: run setup again", () => {
  const done = () => makeWorld({ instance: { setup: false, auth: "password" }, signedIn: true, pending: false });

  it("opens the feeds step in this tab and tells the server nothing", async () => {
    const { calls } = server(done());
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Run setup again" }));
    await headingIs("Choose your feeds");
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    expect(window.location.pathname).toBe("/welcome/feeds");
  });

  it("ends the rerun when the person leaves the steps with Back", async () => {
    server(done());
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Run setup again" }));
    await headingIs("Choose your feeds");
    expect(rerunStore.get()).toBe(true);
    act(() => window.history.back());
    await waitFor(() => expect(window.location.pathname).toBe("/settings/account"));
    await waitFor(() => expect(rerunStore.get()).toBe(false));
  });

  it("ends in the reader, and finishing leaves nothing open for the next visit", async () => {
    const { calls } = server(done());
    go("/settings/account");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Run setup again" }));
    await headingIs("Choose your feeds");
    expect(rerunStore.get()).toBe(true);
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "POST", "/api/onboarding/complete")).toHaveLength(1);
    expect(rerunStore.get()).toBe(false);
  });

  it("offers no Sign out without a password, where Kipple would sign the browser straight back in", async () => {
    server({ ...done(), authMode: "open", passwordSet: false } as World);
    go("/settings/account");
    await screen.findByRole("button", { name: "Run setup again" });
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });

  it("drops the rerun when the app signs out", async () => {
    markRerun();
    server(done());
    go("/settings/account");
    await screen.findByRole("button", { name: "Sign out" });
    act(() => authStore.set("out"));
    await waitFor(() => expect(rerunStore.get()).toBe(false));
  });

  it("starts at the recommended feeds from the empty state", async () => {
    server(done(), { "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [], folders: [], counts: { unread: 0, starred: 0 }, user: { ...bootstrap.user, setup_pending: false } }), "GET /api/items": () => json(pageOf([])), "GET /api/starter-feeds": () => json({ available: false, categories: [] }) });
    go("/l/unread");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Recommended feeds" }));
    await headingIs("Choose your feeds");
    expect(window.location.pathname).toBe("/welcome/feeds");
  });
});

describe("a stale /welcome/<step> address before the account exists", () => {
  it("does not carry the new account past step 2", async () => {
    server(makeWorld());
    go("/welcome/feeds");
    const user = userEvent.setup();
    await headingIs("Create your account");
    expect(window.location.pathname).toBe("/");
    await user.type(screen.getByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "correct horse");
    await user.click(screen.getByRole("button", { name: "Create my account" }));
    await headingIs("Choose your feeds");
    expect(window.location.pathname).toBe("/welcome/feeds");
  });
});

describe("the signed-out screen when GET /api/instance does not answer", () => {
  it.each([
    ["a server error", () => json({ error: "internal" }, 500), /server had a problem/],
    ["a refused address", () => json({ error: "misdirected" }, 421), /Other names for this server in Settings/],
    ["no network", () => Promise.reject(new TypeError("offline")) as never, /can't be reached/],
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

  it("goes to the account form when the open sign-in finds no account (409 setup_required)", async () => {
    const w = openWorld();
    server(w, {
      "POST /api/auth/open": () => {
        w.instance = { setup: true, auth: null };
        return json({ error: "setup_required", message: "Kipple has no account yet; create it first" }, 409);
      },
    });
    go("/");
    await headingIs("Create your account");
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
    await screen.findByText("Kipple's server had a problem");
    const before = callTo(calls, "GET", "/api/instance").length;
    fail = false;
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Article number 1")).toBeInTheDocument();
    expect(callTo(calls, "GET", "/api/instance").length).toBeGreaterThan(before);
  });
});

describe("step 1: accessibility and small things", () => {
  const claimed = () => makeWorld();

  it("checks the password while typing without announcing it on every keystroke", async () => {
    server(claimed());
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Password"), "ab");
    expect(screen.queryByText("Use at least 8 characters.")).toBeNull();
    await user.tab();
    expect(await screen.findByText("Use at least 8 characters.")).toBeInTheDocument();
  });
});

