import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";

class NoES {
  addEventListener() {}
  close() {}
}

// What the server sends for an article with an SVG in it: the image stays, its address empty
// (internal/api TestRefusedSVGKeepsAnEmptyImage).
const ARTICLE = { content_html: '<p>Intro.</p><p><img src="" alt="A diagram of the river"></p><p><img src="/img/ok" alt="A photo"></p>' };

beforeEach(() => {
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true });
  vi.stubGlobal("EventSource", NoES);
  mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf([card(1)])),
    "GET /api/settings": () => json({ settings: [], values: {} }),
    "GET /api/items/1001": () => json(detail(1, ARTICLE)),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true, ...ARTICLE }) }),
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("an article image that cannot be shown", () => {
  it("gets the Image not shown cue in its place, with a link to the original article; a loaded image is left alone", async () => {
    window.history.replaceState({ idx: 0 }, "", "/i/1001?from=unread");
    render(<App client={makeQueryClient({ retry: false })} />);
    const body = await screen.findByTestId("article-body");
    // The client sanitizer keeps the empty address. A browser fires error for it; jsdom does not, so the test does.
    const refused = body.querySelector('img[alt="A diagram of the river"]');
    expect(refused?.getAttribute("src")).toBe("");
    refused?.dispatchEvent(new Event("error"));
    const cue = await waitFor(() => {
      const c = body.querySelector<HTMLElement>(".kp-img-off");
      expect(c).not.toBeNull();
      return c as HTMLElement;
    });
    expect(within(cue).getByText("Image not shown: A diagram of the river")).toBeTruthy();
    expect(within(cue).getByRole("link", { name: "Open original" }).getAttribute("href")).toBe("https://example.com/a/1");
    expect(body.querySelector('img[alt="A photo"]')).not.toBeNull();
    expect(body.querySelectorAll(".kp-img-off")).toHaveLength(1);
  });
});
