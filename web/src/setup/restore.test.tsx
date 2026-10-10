import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { authStore, openRefusedStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { bootstrap, card, json, mockFetch, pageOf } from "@/test/mockApi";
import { themeStore } from "@/theme/theme";
import { DEFAULT_THEME_SETTINGS } from "@/theme/settings";
import { resetDeviceSync } from "@/lib/deviceSync";
import { updatePrefs } from "@/lib/prefs";
import { forgetWizardMemory, restoredFeeds } from "./session";
import { resetOpenSignInGuard } from "./SetupFlow";
import { RestoreWaiting, silenceLimitSeconds } from "./RestoreWaiting";
import { forgetRestoreKeyForTests } from "./restoreKey";

class NoES {
  addEventListener() {}
  close() {}
}

/** What the next upload answers. */
let reply: { status: number; body: unknown; network?: boolean; hold?: boolean } = { status: 200, body: {} };
let sent: File | null = null;
/** The headers of the last upload. */
let sentHeaders: Record<string, string> = {};
/** An upload has been sent in this test: before that, a server with no restore under way answers "none". */
let uploaded = false;

/** XMLHttpRequest as far as the upload needs it: one progress event, then the canned answer. */
class FakeXHR {
  upload: { onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = { onprogress: null };
  status = 0;
  responseText = "";
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  withCredentials = false;
  open() {}
  setRequestHeader(k: string, v: string) {
    sentHeaders[k] = v;
  }
  abort() {
    this.onabort?.();
  }
  send(f: File) {
    sent = f;
    uploaded = true;
    if (reply.hold) return; // still sending until aborted
    setTimeout(() => {
      if (reply.network) return this.onerror?.();
      this.upload.onprogress?.({ lengthComputable: true, loaded: f.size, total: f.size });
      this.status = reply.status;
      this.responseText = JSON.stringify(reply.body);
      this.onload?.();
    }, 0);
  }
}

const BACKUP = {
  kind: "backup",
  kipple_version: "0.8.0",
  created_at: "2026-09-30T10:00:00Z",
  feeds: 42,
  items: 1200,
  starred: 7,
  username: "reader",
  password_state: "password",
  needs_new_password: false,
  new_password_reason: "",
  estimate_seconds: 130,
};

interface World {
  restore: "none" | "uploading" | "checking" | "ready" | "failed" | "confirmed";
  /** What GET /api/setup/restore answers (default: the state alone). */
  status?: unknown;
  setup: boolean;
  signedIn: boolean;
}

const ready = (over: Record<string, unknown> = {}) => ({ state: "ready", summary: { ...BACKUP, ...over }, estimate_seconds: 130 });
const bodyOf = (c: { init?: RequestInit }) => JSON.parse(String(c.init?.body)) as Record<string, unknown>;

function server(w: World, extra: Parameters<typeof mockFetch>[0] = {}) {
  return mockFetch({
    "GET /api/instance": () => json(w.setup ? { setup: true, auth: null, access: { enabled: false, verified: false }, open: { reason: null }, restore: w.restore } : { setup: false, auth: "password" }),
    "POST /api/setup/restore/confirm": () => {
      w.restore = "confirmed";
      return json({ restarting: true, estimate_seconds: 130 }, 202);
    },
    "GET /api/setup/restore": () => json(w.restore === "none" && !uploaded ? { state: "none" } : (w.status ?? { state: w.restore })),
    "DELETE /api/setup/restore": () => {
      w.restore = "none";
      w.status = undefined;
      return new Response(null, { status: 204 });
    },
    "GET /api/setup/restore/feeds": () => new Response('<opml version="2.0"><body/></opml>', { status: 200, headers: { "Content-Type": "text/x-opml" } }),
    "GET /api/bootstrap": () => (w.signedIn ? json({ ...bootstrap, user: { ...bootstrap.user, username: "reader", setup_pending: true } }) : json({ error: "auth" }, 401)),
    "GET /api/auth/me": () => json({ username: "reader", api_enabled: false, password_set: true, access_enabled: false, access_email: null, auth_mode: "password", setup_pending: true }),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/starter-feeds": () => json({ available: false, categories: [] }),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/filters": () => json({ filters: [] }),
    "GET /api/devices": () => json({ devices: [] }),
    ...extra,
  });
}

function go(path = "/") {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

async function chooseFile(user: ReturnType<typeof userEvent.setup>, name = "kipple-backup.zip") {
  await user.click(await screen.findByRole("button", { name: "Restore from a backup" }));
  const input = await screen.findByLabelText("Backup or OPML file");
  await user.upload(input, new File(["x"], name));
}

beforeEach(() => {
  authStore.set("unknown");
  openRefusedStore.set(null);
  forgetWizardMemory();
  sessionStorage.clear();
  localStorage.removeItem("kipple.restoreKey");
  forgetRestoreKeyForTests();
  resetOpenSignInGuard();
  resetDeviceSync();
  liveStore.set(initialLive);
  themeStore.set({ ...DEFAULT_THEME_SETTINGS });
  updatePrefs({ font: "default" });
  vi.stubGlobal("EventSource", NoES);
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
  reply = { status: 202, body: { state: "checking" } };
  sent = null;
  sentHeaders = {};
  uploaded = false;
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("restore in the setup wizard", () => {
  it("offers the restore on the first screen and keeps the account form", async () => {
    server({ restore: "none", setup: true, signedIn: false, status: ready() });
    const { container } = go();
    expect(await screen.findByRole("heading", { level: 1, name: "Create your account" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore from a backup" })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("uploads a backup, shows what is in it and restores everything", async () => {
    const w: World = { restore: "none", setup: true, signedIn: false, status: ready() };
    const { calls } = server(w);
    const { container } = go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByTestId("backup-summary")).toHaveTextContent("You will sign in as reader");
    expect(screen.getByTestId("backup-summary")).toHaveTextContent("42 feeds, 1200 items, 7 starred");
    expect(screen.getByTestId("backup-summary")).toHaveTextContent("Made by Kipple 0.8.0");
    expect(screen.getByText(/Feeds only brings your subscriptions and folders/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Set a new password \(optional\)/)).not.toBeRequired();
    expect(sent?.name).toBe("kipple-backup.zip");
    expect(await axe(container)).toHaveNoViolations();

    await user.click(screen.getByRole("button", { name: "Restore everything" }));
    expect(await screen.findByText(/This usually takes about 3 minutes for a backup this size/)).toBeInTheDocument();
    const confirm = calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/setup/restore/confirm");
    expect(confirm).toHaveLength(1);
    expect(bodyOf(confirm[0] as { init?: RequestInit })).toEqual({});
  });

  it("needs a new password when the backup cannot sign in here, and sends it", async () => {
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: ready({ password_state: "none_access", needs_new_password: true, new_password_reason: "access_unavailable" }) });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByText(/Cloudflare Access/)).toBeInTheDocument();
    expect(screen.getByLabelText("New password")).toBeRequired();
    await user.click(screen.getByRole("button", { name: "Restore everything" }));
    expect(await screen.findByText("Enter a password.")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.pathname === "/api/setup/restore/confirm")).toHaveLength(0);
    await user.type(screen.getByLabelText("New password"), "long enough");
    await user.type(screen.getByLabelText("Password again"), "long enough");
    await user.click(screen.getByRole("button", { name: "Restore everything" }));
    await screen.findByRole("heading", { name: "Restoring your library" });
    const confirm = calls.filter((c) => c.url.pathname === "/api/setup/restore/confirm");
    expect(bodyOf(confirm[0] as { init?: RequestInit })).toEqual({ new_password: "long enough" });
  });

  it("shows the server's message when the file is not a backup", async () => {
    reply = { status: 400, body: { error: "not_a_backup", message: "This is not a Kipple backup zip or an OPML file." } };
    server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user, "notes.txt");
    expect(await screen.findByRole("alert")).toHaveTextContent("This is not a Kipple backup zip or an OPML file.");
    // Back to choosing a file.
    expect(screen.getByLabelText("Backup or OPML file")).toBeInTheDocument();
  });

  it("says another browser holds the restore when the refused upload shows up as a cut connection", async () => {
    reply = { status: 0, body: {}, network: true };
    server({ restore: "none", setup: true, signedIn: false, status: { state: "none", elsewhere: true } });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Another browser is uploading or restoring a backup.");
    expect(alert).not.toHaveTextContent("disk space");
  });

  it("explains a network failure during the upload", async () => {
    reply = { status: 0, body: {}, network: true };
    server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("The upload was refused or interrupted. Check that the server has enough free disk space and that any proxy in front of Kipple allows a file this size.");
  });

  it.each([
    [411, "length_required", "Your browser did not say how big the file is."],
    [400, "upload_incomplete", "The upload stopped before the whole file arrived."],
    [409, "restore_cancelled", "The restore was cancelled."],
  ])("shows the server's message for %i %s", async (status, error, message) => {
    reply = { status, body: { error, message } };
    server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByRole("alert")).toHaveTextContent(message);
  });

  it("takes an OPML file as feeds only, then the account form says the feeds are ready", async () => {
    reply = { status: 200, body: { kind: "opml", feeds: 12 } };
    server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user, "feeds.opml");
    expect(await screen.findByTestId("opml-summary")).toHaveTextContent("12 feeds");
    expect(screen.queryByLabelText("Everything")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Create your account" })).toBeInTheDocument();
    expect(screen.getByText(/Your feeds are ready/)).toBeInTheDocument();
    expect(restoredFeeds.get()?.name).toBe("feeds.opml");
  });

  it("takes feeds only from a backup by fetching its feeds file", async () => {
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    await user.click(await screen.findByLabelText(/^Feeds only/));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Your feeds are ready/);
    expect(calls.filter((c) => c.url.pathname === "/api/setup/restore/feeds")).toHaveLength(1);
    expect(restoredFeeds.get()?.name).toBe("feeds.opml");
  });

  it("offers the saved feeds in the import step", async () => {
    restoredFeeds.set(new File(["<opml/>"], "feeds.opml"));
    const { calls } = server(
      { restore: "none", setup: false, signedIn: true },
      { "POST /api/opml": () => json({ folders_created: 1, feeds_added: 3, feeds_existing: [], folders_merged_case: [], memberships_dropped: [] }) },
    );
    authStore.set("in");
    go("/welcome/import");
    const user = userEvent.setup();
    expect(await screen.findByText(/The feeds from your backup are ready/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Import" }));
    await screen.findByTestId("import-result");
    expect(calls.filter((c) => c.method === "POST" && c.url.pathname === "/api/opml")).toHaveLength(1);
    expect(restoredFeeds.get()).toBeNull();
  });

  it("cancels an upload on the server", async () => {
    const w: World = { restore: "none", setup: true, signedIn: false, status: ready() };
    const { calls } = server(w);
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    await screen.findByTestId("backup-summary");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(await screen.findByLabelText("Backup or OPML file")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "DELETE" && c.url.pathname === "/api/setup/restore")).toHaveLength(1);
  });

  it("after a reload with a backup ready, shows its contents before it can be confirmed", async () => {
    const { calls } = server({ restore: "ready", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    const summary = await screen.findByTestId("backup-summary");
    expect(summary).toHaveTextContent("You will sign in as reader");
    expect(summary).toHaveTextContent("42 feeds, 1200 items, 7 starred");
    await user.click(screen.getByRole("button", { name: "Restore everything" }));
    await screen.findByRole("heading", { name: "Restoring your library" });
    expect(calls.filter((c) => c.url.pathname === "/api/setup/restore/confirm")).toHaveLength(1);
  });

  it("after a reload with no summary yet, offers no way to confirm", async () => {
    server({ restore: "checking", setup: true, signedIn: false, status: { state: "checking" } });
    go();
    expect(await screen.findByText("Checking your backup...")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restore everything" })).toBeNull();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
  });

  it("opens the picker at once when the restore screen is reached again after Back", async () => {
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: { state: "none" } });
    go();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Restore from a backup" }));
    await screen.findByLabelText("Backup or OPML file");
    await user.click(screen.getByRole("button", { name: "Back" }));
    await user.click(await screen.findByRole("button", { name: "Restore from a backup" }));
    expect(screen.getByLabelText("Backup or OPML file")).toBeInTheDocument();
    expect(screen.queryByText("Checking your backup...")).toBeNull();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("polls while the server checks the upload, then shows the summary", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const w: World = { restore: "none", setup: true, signedIn: false, status: { state: "checking" } };
    const { calls } = server(w);
    go();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await chooseFile(user);
    expect(await screen.findByText("Checking your backup...")).toBeInTheDocument();
    w.status = ready();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });
    expect(await screen.findByTestId("backup-summary")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/setup/restore").length).toBeGreaterThanOrEqual(2);
  });

  it("shows the server's message when the check fails, and goes back to the picker", async () => {
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: { state: "failed", error: { code: "newer_kipple", message: "This backup was made by a newer Kipple (0.9). Update Kipple first." } } });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("This backup was made by a newer Kipple (0.9). Update Kipple first.");
    expect(screen.getByLabelText("Backup or OPML file")).toBeInTheDocument();
    // Nothing is cancelled for the person: a later upload replaces the failed attempt on the server.
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
  });

  it("offers to cancel another upload that is in the way, then lets the user try again", async () => {
    reply = { status: 409, body: { error: "restore_busy", message: "Another upload is in progress. Cancel it first." } };
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("Another upload is in progress. Cancel it first.");
    reply = { status: 202, body: { state: "checking" } };
    await user.click(screen.getByRole("button", { name: "Cancel the other upload" }));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(calls.filter((c) => c.method === "DELETE" && c.url.pathname === "/api/setup/restore")).toHaveLength(1);
    // The picker is back, so another file can be sent (the answer to a retry is the checking state).
    server({ restore: "none", setup: true, signedIn: false, status: ready() });
    await user.upload(screen.getByLabelText("Backup or OPML file"), new File(["x"], "again.zip"));
    expect(await screen.findByTestId("backup-summary")).toBeInTheDocument();
  });

  it("says another browser's upload is in the way without offering to cancel it", async () => {
    const msg = "Another browser is uploading or restoring a backup. Kipple stops an upload that stalls or crawls, deletes a backup nobody confirms within an hour, and clears both when it restarts.";
    reply = { status: 409, body: { error: "restore_elsewhere", message: msg } };
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByRole("alert")).toHaveTextContent(msg);
    expect(screen.queryByRole("button", { name: "Cancel the other upload" })).toBeNull();
    expect(calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
  });

  it("sends this browser's owner key with the upload and every restore call, the same key each time", async () => {
    const { calls } = server({ restore: "none", setup: true, signedIn: false, status: ready() });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByTestId("backup-summary")).toBeInTheDocument();
    const key = sentHeaders["X-Kipple-Restore-Key"];
    expect(key).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(localStorage.getItem("kipple.restoreKey")).toBe(key);
    const restoreCalls = calls.filter((c) => c.url.pathname.startsWith("/api/setup/restore"));
    expect(restoreCalls.length).toBeGreaterThan(1);
    for (const c of restoreCalls) expect((c.init?.headers as Record<string, string>)["X-Kipple-Restore-Key"]).toBe(key);
    // Loading the page made no key: the first GET /api/instance went without one.
    const first = calls.find((c) => c.url.pathname === "/api/instance");
    expect((first?.init?.headers as Record<string, string>)["X-Kipple-Restore-Key"]).toBeUndefined();
  });

  it("cancels its own upload on the server while the file is still being sent", async () => {
    reply = { status: 202, body: { state: "checking" }, hold: true };
    const { calls } = server({ restore: "none", setup: true, signedIn: false });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    await user.click(await screen.findByRole("button", { name: "Cancel upload" }));
    // The upload is this browser's from its first byte, so the DELETE is honoured however late the abort arrives.
    await waitFor(() => expect(calls.filter((c) => c.method === "DELETE" && c.url.pathname === "/api/setup/restore")).toHaveLength(1));
    expect(await screen.findByLabelText("Backup or OPML file")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows why the server stopped an upload whose connection it cut", async () => {
    const msg = "The upload was too slow and was stopped. Try again on a faster connection, or restore on the server with kipple restore.";
    reply = { status: 0, body: {}, network: true };
    server({ restore: "none", setup: true, signedIn: false, status: { state: "failed", error: { code: "upload_too_slow", message: msg } } });
    go();
    const user = userEvent.setup();
    await chooseFile(user);
    expect(await screen.findByRole("alert")).toHaveTextContent(msg);
  });

  it("after a reload while a restore is being applied, waits and then asks to sign in", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const w: World = { restore: "confirmed", setup: true, signedIn: false, status: { ...ready(), state: "confirmed" } };
    const { calls } = server(w);
    go();
    expect(await screen.findByRole("heading", { name: "Restoring your library" })).toBeInTheDocument();
    expect(screen.getByTestId("elapsed")).toHaveTextContent("Elapsed 0:0");
    // Kipple is away: the page keeps waiting.
    const before = calls.length;
    w.setup = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    expect(calls.length).toBeGreaterThan(before);
    expect(await screen.findByText(/Restored\. Sign in/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go to sign in" })).toBeInTheDocument();
  });

  it("keeps waiting through network errors and says Kipple stopped after a long silence", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const w: World = { restore: "confirmed", setup: true, signedIn: false };
    let down = false;
    server(w, {
      "GET /api/instance": () => {
        if (down) throw new TypeError("Failed to fetch");
        return json({ setup: true, auth: null, access: { enabled: false, verified: false }, open: { reason: null }, restore: "confirmed" });
      },
    });
    go();
    await screen.findByRole("heading", { name: "Restoring your library" });
    down = true;
    expect(silenceLimitSeconds(300)).toBe(900);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(890_000);
    });
    expect(screen.queryByText(/Kipple stopped/)).toBeNull();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000);
    });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Kipple stopped. Start it again and the restore will finish."));
  });

  it("says a reset was not applied, not that Kipple stopped, when Kipple keeps answering as set up", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    server({ restore: "none", setup: false, signedIn: false }, {
      "GET /api/instance": () => json({ setup: false, auth: null, access: { enabled: false, verified: false }, open: { reason: null }, restore: "none" }),
    });
    render(<RestoreWaiting kind="reset" estimateSeconds={30} username={null} onSignIn={() => {}} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(310_000);
    });
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("the reset was not applied"));
    expect(screen.queryByText(/Kipple stopped/)).toBeNull();
  });
});
