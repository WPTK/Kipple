import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import type { Feed, Headline } from "@/api/types";
import { resetDevicePrefs } from "@/lib/devicePrefs";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, json, mockFetch } from "@/test/mockApi";

// A key press on the Feed index moves the Tab stop between two rows. It must not redraw the other feeds' blocks.

const drawn = vi.hoisted(() => ({ rows: new Map<string, number>() }));
vi.mock("@/layouts/digest", async (original) => {
  const real = await original<typeof import("@/layouts/digest")>();
  return {
    ...real,
    DigestRow: (props: Parameters<typeof real.DigestRow>[0]) => {
      drawn.rows.set(props.item.id, (drawn.rows.get(props.item.id) ?? 0) + 1);
      return real.DigestRow(props);
    },
  };
});

class NoES {
  addEventListener() {}
  close() {}
}

const FEEDS = 120;
const feed = (id: number): Feed => ({ ...bootstrap.feeds![0]!, id: String(id), folder_id: "1", title: `Feed ${String(id).padStart(3, "0")}`, unread: 1 });
const headlines = (id: number): Headline[] => [0, 1, 2].map((k) => ({ id: String(id * 10 + k), feed_id: String(id), title: `Feed ${id} story ${k + 1}`, published_at: 1_790_000_000 - k, read: false, starred: false }));

beforeEach(() => {
  clearToasts();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  drawn.rows.clear();
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("the Feed index with many feeds", () => {
  it("redraws only the blocks a key press moves between", async () => {
    const feeds = Array.from({ length: FEEDS }, (_, i) => feed(i + 1));
    mockFetch({
      "GET /api/bootstrap": () => json({ ...bootstrap, feeds, counts: { unread: FEEDS, starred: 0 } }),
      "GET /api/feeds/latest": () => json({ feeds: feeds.map((f) => ({ feed_id: f.id, items: headlines(Number(f.id)) })) }),
      "GET /api/items": () => json({ items: [], next_cursor: null }),
      "GET /api/settings": () => json({ settings: [], values: {} }),
    });
    window.history.replaceState({ idx: 0 }, "", "/feeds/index");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByText("Feed 120 story 3");
    const links = [...document.querySelectorAll<HTMLAnchorElement>("[data-feed-index] a")];
    links[0]!.focus();
    await waitFor(() => expect(links[0]).toHaveFocus());
    const before = new Map(drawn.rows);
    const user = userEvent.setup();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(links[1]).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(links[2]).toHaveFocus());
    // Feed 1's rows were redrawn for the Tab stop moving inside the block; no other feed's row was drawn again.
    const again = [...drawn.rows].filter(([id, n]) => n !== (before.get(id) ?? 0)).map(([id]) => id);
    expect(again.length).toBeGreaterThan(0);
    expect(again.every((id) => Number(id) >= 10 && Number(id) < 20)).toBe(true);
  });
});
