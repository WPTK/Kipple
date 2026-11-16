import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { render, screen, within } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import type { HealthFeed } from "@/api/admin";
import { bootstrap, json, mockFetch, pageOf } from "@/test/mockApi";
import { wide } from "@/test/media";
import { EmptyState } from "@/ui/EmptyState";
import { Modal } from "@/ui/kit";

// Targets, names, reading order and wrapping that were measured on the running app.

class NoES {
  addEventListener() {}
  close() {}
}

const nowS = Math.floor(Date.now() / 1000);
const health = (over: Partial<HealthFeed>): HealthFeed => ({
  id: "1", title: "Broken Feed", url: "https://x.test/feed", url_original: null, status: "ok", redirect_pending: false, notices: [], enabled: true, disabled_reason: null,
  last_success_at: nowS - 3600, last_fetch_at: nowS - 60, last_error_at: null, last_error_class: null, last_error: null, last_status: null, consecutive_failures: 0,
  current_delay_s: 3600, next_fetch_at: nowS + 3600, redirect_to: null, redirect_kind: null, redirect_count: 0, redirect_owner: null, last_new_items_at: null, trimmed_unread_count: 0,
  trimmed_unread_since: null, host_throttled_until: null, ...over,
});
const failing = (over: Partial<HealthFeed> = {}) =>
  health({ status: "erroring", last_error: "lookup x.test: no such host", last_error_class: "dns", last_error_at: nowS - 60, consecutive_failures: 2, ...over });
const body = (feeds: HealthFeed[]) => ({ feeds, reader_last_seen_at: null, snapshot: { last_at: null, last_error: null }, clock: { ahead_s: 0 }, db: { db_bytes: 1, wal_bytes: 0, backup_bytes: 0, imgcache_bytes: 0 }, unread_total: 0 });

function go(path: string, feeds: HealthFeed[] = []) {
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([])),
    "GET /api/health/feeds": () => json(body(feeds)),
  });
  window.history.replaceState({ idx: 0 }, "", path);
  render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("empty-state links", () => {
  it("are boxes at least 24 px high", () => {
    const { container } = render(<EmptyState title="Nothing here" action={<a href="/x">Show all articles</a>} />);
    expect(container.firstElementChild?.className).toContain("[&>a]:min-h-6");
    expect(container.firstElementChild?.className).toContain("[&>a]:inline-flex");
  });
});

describe("dialogs", () => {
  it("say they are modal", () => {
    render(<Modal open onOpenChange={() => {}} title="Sure?" />);
    expect(screen.getByRole("dialog", { name: "Sure?" })).toHaveAttribute("aria-modal", "true");
  });
});

describe("Feed health", () => {
  it("names Try now by what it says", async () => {
    go("/health", [failing()]);
    const btn = (await screen.findAllByRole("button", { name: /^Try now/ }))[0]!;
    expect(btn.getAttribute("aria-label")).toContain(btn.textContent!.trim());
    expect(btn.getAttribute("aria-label")).toContain("Broken Feed");
  });

  it("counts every feed once, so the numbers add up to the total", async () => {
    go("/health", [
      failing({ id: "1", title: "A" }),
      failing({ id: "2", title: "B", status: "failing", consecutive_failures: 20 }),
      health({ id: "3", title: "C", status: "redirecting", redirect_pending: true, redirect_to: "https://new.test/feed", redirect_kind: "permanent" }),
      // A temporary redirect is a note on a feed that works.
      health({ id: "4", title: "D", notices: ["The site redirected this feed temporarily."], redirect_kind: "temporary", redirect_to: "https://tmp.test/feed" }),
      health({ id: "5", title: "E", status: "disabled", enabled: false, disabled_reason: "user" }),
      health({ id: "6", title: "F" }),
      health({ id: "7", title: "G" }),
    ]);
    const summary = await screen.findByTestId("health-summary");
    expect(summary).toHaveTextContent("2 feeds need attention. 1 moved. 1 paused. 3 working.");
    const sum = [...summary.textContent!.matchAll(/(\d+) (?:feeds? need|moved|paused|working)/g)].reduce((n, m) => n + Number(m[1]), 0);
    expect(sum).toBe(7);
  });

  it("lets a long address wrap instead of clipping it", async () => {
    go("/health", [health({ url: "https://example.test/" + "a".repeat(120) })]);
    const url = (await screen.findAllByText(/^https:\/\/example\.test\/a+$/))[0]!;
    expect(url.className).not.toContain("truncate");
    expect(url.className).toContain("[overflow-wrap:anywhere]");
  });

  it("puts the row's Actions menu before its repair buttons and Details in the tab order", async () => {
    wide();
    go("/health", [failing()]);
    const actions = await screen.findByRole("button", { name: "Actions for Broken Feed" });
    for (const name of [/^Edit address of/, /^Try now/, "Details"]) {
      const b = screen.getByRole("button", { name });
      expect(actions.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING, String(name)).toBeTruthy();
    }
  });
});

describe("the sidebar", () => {
  it("has no headings of its own: the page's title is the first heading, and the tree keeps its name", async () => {
    wide();
    go("/l");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(await screen.findByRole("tree", { name: "Feeds" })).toBeInTheDocument();
    expect(within(nav).queryAllByRole("heading")).toHaveLength(0);
  });
});

describe("sources that jsdom cannot lay out", () => {
  const read = (p: string) => readFileSync(p, "utf8");
  it("the article toolbar wraps at a narrow width instead of scrolling sideways", () => {
    const src = read("src/screens/ArticlePane.tsx");
    const at = src.indexOf('aria-label="Article actions"');
    expect(at).toBeGreaterThan(0);
    const toolbar = src.slice(at, at + 300);
    expect(toolbar).toContain("flex-wrap");
    expect(toolbar).not.toContain("overflow-x-auto");
  });

  it("running text in an article breaks a long word at the column's edge", () => {
    const css = read("src/index.css").replace(/\/\*[\s\S]*?\*\//g, "");
    const rule = /\.article-body > :is\(([^)]*)\)\s*\{([^}]*)\}/.exec(css);
    expect(rule?.[1]).toMatch(/\bp\b/);
    expect(rule?.[2]).toMatch(/overflow-wrap:\s*anywhere/);
    // Tables keep break-word: anywhere would shrink their cells letter by letter.
    expect(rule?.[1]).not.toMatch(/\btable\b|\btd\b/);
  });
});
