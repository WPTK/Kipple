import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { HealthFeed } from "@/api/admin";
import type { Feed } from "@/api/types";
import { failureReason, retryLabel } from "@/lib/feedStatus";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { looksPrivate } from "./feeds/AddFeedDialog";

// Repairing a feed that can't be fetched: the reason in words, when it last worked and tries again, and the two ways out.

class NoES {
  addEventListener() {}
  close() {}
}

const failing: Feed = { ...bootstrap.feeds[0]!, unread: 0, status: "erroring", last_error: "lookup x.test: no such host", last_error_class: "dns" };
const nowS = Math.floor(Date.now() / 1000);
const health = (over: Partial<HealthFeed> = {}): HealthFeed => ({
  id: failing.id, title: failing.title, url: "https://x.test/feed", url_original: null, status: "erroring", redirect_pending: false, notices: [], enabled: true, disabled_reason: null,
  last_success_at: nowS - 3 * 3600, last_fetch_at: nowS - 60, last_error_at: nowS - 60, last_error_class: "dns", last_error: "lookup x.test: no such host", last_status: null, consecutive_failures: 2,
  current_delay_s: 3600, next_fetch_at: nowS + 3600, redirect_to: null, redirect_kind: null, redirect_count: 0, redirect_owner: null, last_new_items_at: null, trimmed_unread_count: 0,
  trimmed_unread_since: null, host_throttled_until: null, ...over,
});
const healthBody = (feeds: HealthFeed[]) => ({ feeds, reader_last_seen_at: null, snapshot: { last_at: null, last_error: null }, clock: { ahead_s: 0 }, db: { db_bytes: 1, wal_bytes: 0, backup_bytes: 0, imgcache_bytes: 0 }, unread_total: 0 });
const detail = { ...failing, url: "https://x.test/feed", url_original: null, custom_title: null, position: 0, enabled: true, disabled_reason: null, dedup_mode: "auto", rekey_pending: false, user_agent: null, has_http_auth: false, ignore_http_cache: false, disable_http2: false, allow_insecure_tls: false, allow_private_net: false, next_fetch_at: nowS + 3600 };

function go(path: string, feeds: HealthFeed[] = [health()], extra: Record<string, () => Response> = {}) {
  const m = mockFetch({
    "GET /api/bootstrap": () => json({ ...bootstrap, feeds: [failing], counts: { unread: 0, starred: 0 } }),
    "GET /api/items": () => json(pageOf([])),
    "GET /api/health/feeds": () => json(healthBody(feeds)),
    "GET /api/feeds/1": () => json(detail),
    ...extra,
  });
  window.history.replaceState({ idx: 0 }, "", path);
  render(<App client={makeQueryClient({ retry: false })} />);
  return m;
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("failure words", () => {
  it("maps each cause to reader words and never shows the server's text", () => {
    expect(failureReason({ last_error_class: "dns", last_error: "lookup x: no such host" })).toBe("This address does not exist");
    expect(failureReason({ last_error_class: "connect", last_error: "dial tcp: refused" })).toBe("Kipple can't connect to this address");
    expect(failureReason({ last_error_class: "parse", last_error: "not a feed: x" })).toBe("The reply isn't a feed");
    expect(failureReason({ last_error_class: "http", last_error: "HTTP 404" })).toBe("The site says this feed isn't there");
    expect(failureReason({ last_error_class: "http", last_error: "HTTP 503" })).toBe("The site had a problem answering");
    expect(failureReason({ last_error: "something" })).toBe("Kipple couldn't read this feed");
  });

  it("says when Kipple tries again", () => {
    const now = 1_000_000_000_000;
    expect(retryLabel(now / 1000 + 3600, now)).toBe("Trying again in 1 hour");
    expect(retryLabel(now / 1000 - 10, now)).toBe("Trying again shortly");
    expect(retryLabel(null, now)).toBe("");
  });
});

describe("an address on the reader's own network", () => {
  it("is recognised as it is typed", () => {
    for (const a of ["127.0.0.1:8080/feed", "http://10.1.2.3/f", "192.168.1.5", "172.20.0.1", "nas.local/rss", "localhost:3000"]) expect(looksPrivate(a), a).toBe(true);
    for (const a of ["example.com/feed.xml", "https://172.32.0.1/f", "8.8.8.8", ""]) expect(looksPrivate(a), a).toBe(false);
  });
});

describe("the repair surface", () => {
  it("sits in the feed's empty list with the facts and both buttons, and opens the editor on the address", async () => {
    go("/l?feed=1");
    const user = userEvent.setup();
    const box = await screen.findByTestId("feed-repair");
    expect(await within(box).findByText(/Last worked 3 hours ago\. Trying again in 1 hour\./)).toBeInTheDocument();
    await user.click(within(box).getByRole("button", { name: /^Edit address of/ }));
    const dlg = await screen.findByRole("dialog", { name: "Edit feed" });
    expect(await within(dlg).findByTestId("feed-reason")).toHaveTextContent("This address does not exist.");
    const address = await within(dlg).findByLabelText("Feed address");
    await waitFor(() => expect(address).toHaveFocus());
    expect(within(dlg).getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("Try now fetches the feed at once and says how it went", async () => {
    const m = go("/l?feed=1", [health()], { "POST /api/feeds/1/refresh": () => json({ outcome: "error", error: "lookup x.test: no such host", error_class: "dns" }) });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: /^Try .* now$/ }));
    await waitFor(() => expect(m.calls.some((c) => c.method === "POST" && c.url.pathname === "/api/feeds/1/refresh")).toBe(true));
    expect((await screen.findAllByText(/still can't be fetched: this address does not exist\./)).length).toBeGreaterThan(0);
  });

  it("is on the Feed health row with the server's text behind Details", async () => {
    go("/health");
    const user = userEvent.setup();
    expect((await screen.findAllByText("This address does not exist.")).length).toBeGreaterThan(0);
    expect(screen.getByTestId("health-summary")).toHaveTextContent("1 feed needs attention");
    expect(screen.getAllByText("lookup x.test: no such host")[0]).not.toBeVisible();
    await user.click(screen.getAllByText("Details")[0]!);
    expect(screen.getAllByText("lookup x.test: no such host")[0]).toBeVisible();
    await user.click(screen.getAllByRole("button", { name: /^Edit address of/ })[0]!);
    const dlg = await screen.findByRole("dialog", { name: "Edit feed" });
    await waitFor(() => expect(within(dlg).getByLabelText("Feed address")).toHaveFocus());
  });

  it("the summary line is the filter", async () => {
    go("/health", [health(), health({ id: "9", title: "Fine", status: "ok", last_error: null, last_error_class: null, consecutive_failures: 0 })]);
    const user = userEvent.setup();
    const summary = await screen.findByTestId("health-summary");
    expect(summary).toHaveTextContent("1 feed needs attention. 1 working.");
    expect(screen.getByText("Fine")).toBeInTheDocument();
    await user.click(within(summary).getByRole("button", { name: "1 feed needs attention" }));
    expect(screen.queryByText("Fine")).toBeNull();
    await user.click(within(summary).getByRole("button", { name: "Show all" }));
    expect(screen.getByText("Fine")).toBeInTheDocument();
  });
});
