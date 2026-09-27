import { vi } from "vitest";
import type { Bootstrap, Card, ItemDetail, ItemsPage } from "@/api/types";

export function card(n: number, over: Partial<Card> = {}): Card {
  return {
    id: String(1000 + n),
    feed_id: "1",
    title: `Article number ${n}`,
    url: `https://example.com/a/${n}`,
    author: "Ada",
    excerpt: `Excerpt for article ${n}`,
    image: null,
    published_at: Math.floor(Date.now() / 1000) - n * 3600,
    sort_at: Math.floor(Date.now() / 1000) - n * 3600,
    read: false,
    starred: false,
    word_count: 500,
    reading_minutes: 3,
    origin_title: null,
    source: "Example Feed",
    muted_by: null,
    muted_by_name: null,
    ...over,
  };
}

export function detail(n: number, over: Partial<ItemDetail> = {}): ItemDetail {
  return {
    ...card(n),
    content_html: `<p>Body of article ${n}</p><p><a href="https://example.com">a link</a></p>`,
    fulltext: { mode: null, effective: 0, available: false, error: null },
    enclosures: [],
    feed: { id: "1", title: "Example Feed", site_url: "https://example.com" },
    trimmed: false,
    ...over,
  };
}

export const bootstrap: Bootstrap = {
  user: { username: "dev", api_enabled: false, password_set: true, access_enabled: false },
  settings: {},
  folders: [{ id: "1", name: "News", position: 0, is_default: true, unread: 3 }],
  feeds: [
    {
      id: "1",
      folder_id: "1",
      title: "Example Feed",
      site_url: "https://example.com",
      icon: null,
      unread: 3,
      status: "ok",
      fulltext: false,
      retention: null,
      interval_minutes: null,
      is_archive: false,
      starred_count: 0,
    },
  ],
  counts: { unread: 3, starred: 0 },
  runs: [],
  warnings: [],
  server_time: Math.floor(Date.now() / 1000),
  version: "test",
};

type Handler = (url: URL, init: RequestInit | undefined) => Response | Promise<Response>;

export const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

/** Route table keyed "METHOD /path". Unmatched requests fail the test loudly. */
export function mockFetch(routes: Record<string, Handler>) {
  const calls: { method: string; url: URL; init?: RequestInit }[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://127.0.0.1");
    const method = (init?.method ?? "GET").toUpperCase();
    calls.push({ method, url, init });
    const h = routes[`${method} ${url.pathname}`];
    if (!h) throw new Error(`unmocked request: ${method} ${url.pathname}`);
    return h(url, init);
  });
  vi.stubGlobal("fetch", fn);
  return { fn, calls };
}

export function pageOf(items: Card[], next: string | null = null, as_of?: string): ItemsPage {
  return { items, next_cursor: next, ...(as_of ? { as_of } : {}) };
}
