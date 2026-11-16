import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { clearToasts } from "@/shell/toasts";
import { clearListMemory } from "./ListPane";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

class NoES {
  addEventListener() {}
  close() {}
}

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("article while its detail loads", () => {
  it("shows the list card's title, source and picture instead of a bare skeleton", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1, { image: "/img/lead.jpg" }), card(2)])),
      "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
      "GET /api/items/1001": () => new Promise<Response>(() => undefined), // never answers
    });
    window.history.replaceState({ idx: 0 }, "", "/i/1001?from=unread");
    render(<App client={makeQueryClient({ retry: false })} />);
    const title = await screen.findByTestId("loading-card-title");
    expect(title.textContent).toBe("Article number 1");
    expect(screen.getByText("Loading article")).toBeTruthy();
    expect(document.querySelector('img[src="/img/lead.jpg"]')).not.toBeNull();
  });
});

describe("article skeleton takes the open article's place", () => {
  const routes = (detailAnswer: () => Promise<Response> | Response) =>
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([card(1, { image: "/img/lead.jpg" }), card(2)])),
      "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
      "GET /api/items/1001": detailAnswer,
      "GET /api/items/1002": () => new Promise<Response>(() => undefined),
    });

  it("is drawn in the same frame and header classes as the open article, so nothing moves when the text arrives", async () => {
    let answer!: (r: Response) => void;
    routes(() => new Promise<Response>((r) => (answer = r)));
    updateDevicePrefs({ articleWidth: "wide" });
    window.history.replaceState({ idx: 0 }, "", "/i/1001?from=unread");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByTestId("loading-card-title");
    const frame = (el: Element) => {
      const article = el.closest("article")!;
      return { article: article.className, col: article.style.getPropertyValue("--kp-col"), header: el.closest("header")!.className, title: el.className };
    };
    const loading = frame(screen.getByTestId("loading-card-title"));
    expect(loading.article).toContain("kp-article");
    // The byline is the open article's own line (author, date, reading time), so it wraps as the real one does.
    const bylineText = screen.getByTestId("loading-card-title").closest("header")!.lastElementChild!.textContent!;
    expect(bylineText).toMatch(/^Ada · .+ · 3 min read$/);
    await waitFor(() => expect(answer).toBeTypeOf("function"));
    answer(json(detail(1)));
    const real = frame(await screen.findByRole("heading", { level: 1, name: "Article number 1" }));
    expect(loading.article).toBe(real.article);
    // The same reading column (the Article width setting), so a title wraps at the same width.
    expect(loading.col).toBe("62rem");
    expect(real.col).toBe(loading.col);
    expect(loading.header).toBe(real.header);
    // The title is set in the open article's own type, so it wraps where the real one will.
    for (const c of loading.title.split(" ").filter((c) => c.startsWith("text-title") || c.startsWith("leading-"))) expect(real.title).toContain(c);
  });

  it("with no list behind it holds the title and byline as bars, a whole line high each", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json(bootstrap),
      "GET /api/items": () => json(pageOf([])),
      "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 0 }),
      "GET /api/items/1001": () => new Promise<Response>(() => undefined),
    });
    window.history.replaceState({ idx: 0 }, "", "/i/1001");
    render(<App client={makeQueryClient({ retry: false })} />);
    await screen.findByText("Loading article");
    const sk = document.querySelector('[role="status"][aria-busy="true"] article.kp-article')!;
    expect(sk).not.toBeNull();
    expect(screen.queryByTestId("loading-card-title")).toBeNull();
    // Two title bars, one source bar and one byline bar, each a whole line (1lh) high.
    expect(sk.querySelectorAll('header [class*="h-[1lh]"]').length).toBe(2 + 2);
    expect(sk.querySelector(".article-body")).not.toBeNull();
  });
});
