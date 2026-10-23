import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App, { makeQueryClient } from "@/App";
import { authStore } from "@/api/client";
import { initialLive, liveStore } from "@/api/events";
import { resetDevicePrefs, updateDevicePrefs } from "@/lib/devicePrefs";
import { DEFAULT_PREFS, prefsStore } from "@/lib/prefs";
import { resetUndo } from "@/lib/undo";
import { clearToasts } from "@/shell/toasts";
import { Switch } from "@/ui/kit";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";
import { clearListMemory } from "./ListPane";
import { LoginScreen } from "./LoginScreen";
import { FontSelect } from "./AppearanceControls";

// What a screen reader is told, and when (issues #482, #497, #498, #499, #500).

class NoES {
  addEventListener() {}
  close() {}
}

const BASE = 1_790_000_000;
const items = () => [1, 2, 3].map((n) => card(n, { published_at: BASE - n * 3600, sort_at: BASE - n * 3600 }));

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  const open = (n: number) => () => json({ session_key: "k", item: detail(n, { read: true }) });
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items(), null, "2000")),
    "GET /api/status": () => json({ runs: [], inflight: 0, unread_total: 3 }),
    "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    ...Object.fromEntries([1, 2, 3].map((n) => [`GET /api/items/${1000 + n}`, () => json(detail(n, { read: true }))])),
    ...Object.fromEntries([1, 2, 3].map((n) => [`POST /api/items/${1000 + n}/open`, open(n)])),
    ...extra,
  });
}
function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}
function media(...on: string[]) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: on.some((q) => query.includes(q)),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

beforeEach(() => {
  vi.stubGlobal("EventSource", NoES);
  clearToasts();
  clearListMemory();
  authStore.set("unknown");
  liveStore.set(initialLive);
  resetDevicePrefs();
  resetUndo();
  prefsStore.set({ ...DEFAULT_PREFS });
  updateDevicePrefs({ peekSeen: true, layout: "inbox" });
  media("min-width: 820px");
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("refresh shows it is working from the tap (#482)", () => {
  it("spins and says Refreshing while the request is still on its way", async () => {
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    routes({
      "POST /api/refresh": async () => {
        await held;
        return json({ run_id: "r1", total: 1 }, 202);
      },
    });
    go("/l/unread");
    await screen.findByText("Article number 2");
    await userEvent.setup().click(screen.getByRole("button", { name: "Refresh all feeds" }));
    const btn = await screen.findByRole("button", { name: "Refreshing" });
    expect(btn.querySelector("svg")).toHaveClass("animate-spin");
    release();
    // The answer shows the run at once: no gap before its run.start event arrives (none does here).
    await waitFor(() => expect(liveStore.get().runs.r1).toBeDefined());
    expect(screen.getByRole("button", { name: "Refreshing" })).toBeInTheDocument();
  });
});

describe("switch names (#497)", () => {
  it("are the label alone: the visible On or Off is not part of the name", () => {
    render(<Switch label="Highlight keywords" checked onChange={() => {}} />);
    expect(screen.getByRole("switch", { name: "Highlight keywords" })).toBeChecked();
  });

  it("a switch that is off for a reason is described by that reason", () => {
    mockFetch({ "GET /api/settings": () => json({ settings: [] }) });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <FontSelect />
      </QueryClientProvider>,
    );
    const sw = screen.getByRole("switch", { name: "Use it everywhere" });
    expect(sw).toBeDisabled();
    expect(sw).toHaveAccessibleDescription(/Choose a reading font above/);
  });
});

describe("field errors are the field's (#498)", () => {
  it("sign-in: a wrong password marks both fields invalid, describes them by the error and focuses the password", async () => {
    mockFetch({ "POST /api/auth/login": () => json({ error: "auth" }, 401) });
    render(
      <QueryClientProvider client={new QueryClient()}>
        <LoginScreen />
      </QueryClientProvider>,
    );
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Username"), "reader");
    await user.type(screen.getByLabelText("Password"), "pw");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    const alert = await screen.findByRole("alert");
    for (const label of ["Username", "Password"]) {
      const input = screen.getByLabelText(label);
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(input).toHaveAccessibleDescription(alert.textContent ?? "");
    }
    expect(screen.getByLabelText("Password")).toHaveFocus();
  });

  it("add feed: the error marks the address field invalid, describes it, and focus is back on it", async () => {
    routes({
      "GET /api/settings": () => json({ settings: [] }),
      "POST /api/feeds": () => json({ error: "invalid_url", message: "That is not a web address." }, 422),
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Add feed" });
    const field = within(dlg).getByLabelText("Feed or website address");
    await user.type(field, "not a url !!");
    await user.click(within(dlg).getByRole("button", { name: "Add feed" }));
    await waitFor(() => expect(field).toHaveAttribute("aria-invalid", "true"));
    expect(field).toHaveAccessibleDescription(/.+/);
    expect(field).toHaveFocus();
  });
});

describe("add feed: an error from picking a feed stays with the pick", () => {
  it("Back to the address form starts with no error", async () => {
    routes({
      "GET /api/settings": () => json({ settings: [] }),
      "POST /api/feeds": (_u, init) => {
        const b = JSON.parse(String(init?.body)) as { url: string };
        if (b.url === "https://site.test")
          return json({ status: "choose", candidates: [{ url: "https://site.test/a.xml", title: "Posts", type: "rss" }, { url: "https://site.test/b.xml", title: "Comments", type: "rss" }] });
        return json({ error: "fetch_failed", message: "That feed could not be read." }, 422);
      },
    });
    go("/feeds");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Add feed" }));
    const dlg = await screen.findByRole("dialog", { name: "Add feed" });
    await user.type(within(dlg).getByLabelText("Feed or website address"), "https://site.test");
    await user.click(within(dlg).getByRole("button", { name: "Add feed" }));
    const pick = await within(dlg).findByRole("radio", { name: /Comments/ });
    await user.click(pick);
    await user.click(within(dlg).getByRole("button", { name: "Add selected feed" }));
    await within(dlg).findByRole("alert");
    expect(within(dlg).getByRole("radio", { name: /Comments/ })).toHaveFocus();
    await user.click(within(dlg).getByRole("button", { name: "Back" }));
    const field = await within(dlg).findByLabelText("Feed or website address");
    expect(field).not.toHaveAttribute("aria-invalid");
    expect(within(dlg).queryByRole("alert")).toBeNull();
  });
});

describe("status text that is not stale or repeated (#499)", () => {
  it("Next retires the reader's Marked unread toast, which would name no article any more", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    const user = userEvent.setup();
    await user.click(document.querySelector<HTMLElement>('[data-item-id="1001"] a') as HTMLElement);
    const toolbar = within(await screen.findByRole("toolbar", { name: "Article actions" }));
    await user.click(await toolbar.findByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Mark as unread" }));
    await waitFor(() => expect(screen.getByTestId("undo-region")).toHaveTextContent("Marked unread"));
    await user.click(toolbar.getByRole("button", { name: "Next article" }));
    await screen.findByRole("heading", { level: 1, name: "Article number 2" });
    expect(screen.getByTestId("undo-region")).toHaveTextContent("");
  });
});

describe("names that tell rows apart (#500)", () => {
  it("row buttons carry the article's title; the row link starts with it; the Unread link reads its count once", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 2");
    expect(screen.getByRole("button", { name: "Star: Article number 2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "More actions: Article number 2" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Article number 2" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Primary" });
    // The badge is its own flex item: a browser reads "Unread 60" (web/uat/announce.mjs); jsdom joins it as "Unread3".
    expect(within(nav).getByRole("link", { name: /^Unread\s?3$/ })).toBeInTheDocument();
  });
});
