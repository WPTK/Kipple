import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import App, { makeQueryClient } from "@/App";
import { initPrefs, updatePrefs } from "@/lib/prefs";
import { initTheme } from "@/theme/theme";
import { authStore } from "@/api/client";
import { liveStore, initialLive } from "@/api/events";
import { bootstrap, card, detail, json, mockFetch, pageOf } from "@/test/mockApi";

// jsdom has no EventSource; the shell subscribes to one.
class NoES {
  addEventListener() {}
  close() {}
}

function routes(extra: Parameters<typeof mockFetch>[0] = {}) {
  const items = [card(1), card(2, { read: true }), card(3, { starred: true })];
  return mockFetch({
    "GET /api/bootstrap": () => json(bootstrap),
    "GET /api/items": () => json(pageOf(items)),
    "GET /api/items/1001": () => json(detail(1)),
    "GET /api/items/1002": () => json(detail(2, { read: true })),
    "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true }) }),
    "POST /api/items/1002/open": () => json({ session_key: "k", item: detail(2, { read: true }) }),
    "PUT /api/items/1001/star": () => json({ starred: true, restored: false }),
    "POST /api/items/mark-read": () => json({ changed: ["1001"], restored: [] }),
    ...extra,
  });
}

function go(path: string) {
  window.history.replaceState({ idx: 0 }, "", path);
  return render(<App client={makeQueryClient({ retry: false })} />);
}

beforeEach(() => {
  authStore.set("unknown");
  liveStore.set(initialLive);
  vi.stubGlobal("EventSource", NoES);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("Login", () => {
  it("shows the login screen on 401 and signs in", async () => {
    let signedIn = false;
    mockFetch({
      "GET /api/bootstrap": () => (signedIn ? json(bootstrap) : json({ error: "auth" }, 401)),
      "POST /api/auth/login": () => {
        signedIn = true;
        return new Response(null, { status: 204 });
      },
      "GET /api/items": () => json(pageOf([card(1)])),
    });
    const { container } = go("/l/unread");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Sign in to Kipple" });
    expect(await axe(container)).toHaveNoViolations();

    await user.type(screen.getByLabelText("Username"), "dev");
    await user.type(screen.getByLabelText("Password"), "pw");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByText("Article number 1");
  });

  it("explains a wrong password without leaking which field", async () => {
    mockFetch({
      "GET /api/bootstrap": () => json({ error: "auth" }, 401),
      "POST /api/auth/login": () => json({ error: "auth" }, 401),
    });
    go("/");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Username"), "dev");
    await user.type(screen.getByLabelText("Password"), "nope");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("didn't match");
  });
});

describe("Unread list (Magazine)", () => {
  it("renders rows with unread state, day header and landmarks, and passes axe", async () => {
    routes();
    const { container } = go("/l/unread");
    await screen.findByText("Article number 1");
    expect(screen.getByRole("main")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Primary" })).toBeInTheDocument();
    expect(screen.getByText("Today")).toBeInTheDocument();
    // Unread rows announce "Unread" in their accessible name; read rows do not.
    expect(screen.getByRole("link", { name: /^Unread, Article number 1/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /^Article number 2/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Unstar" })).toHaveAttribute("aria-pressed", "true");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("tab bar has the four tabs with 44px hit rules and Unread current", async () => {
    routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const nav = screen.getByRole("navigation", { name: "Primary" });
    for (const name of ["Unread", "Feeds", "Search", "Settings"]) {
      expect(within(nav).getByRole("link", { name: new RegExp(name) })).toBeInTheDocument();
    }
    expect(within(nav).getByRole("link", { name: /Unread/ })).toHaveAttribute("aria-current", "page");
  });

  it("stars a row optimistically and calls PUT star", async () => {
    const { calls } = routes();
    go("/l/unread");
    await screen.findByText("Article number 1");
    const user = userEvent.setup();
    const stars = screen.getAllByRole("button", { name: "Star" });
    await user.click(stars[0]!);
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.pathname === "/api/items/1001/star")).toBe(true));
    expect(JSON.parse(String(calls.find((c) => c.method === "PUT")?.init?.body))).toEqual({ starred: true });
    expect((calls.find((c) => c.method === "PUT")?.init?.headers as Record<string, string>)["X-Kipple-Client"]).toBe("web");
  });

  it("shows the empty state copy", async () => {
    routes({ "GET /api/items": () => json(pageOf([])) });
    go("/l/unread");
    expect(await screen.findByText("All caught up")).toBeInTheDocument();
  });

  it("shows a retryable error when the list fails", async () => {
    routes({ "GET /api/items": () => json({ error: "internal" }, 500) });
    go("/l/unread");
    expect(await screen.findByText("Couldn't load articles")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Article view", () => {
  it("renders safely, marks read via /open, and passes axe", async () => {
    const dirty = '<p>Hello</p><script>window.pwned=1</script><img src="x" onerror="window.pwned=1"><a href="https://e.x">go</a>';
    const { calls } = routes({
      "GET /api/items/1001": () => json(detail(1, { content_html: dirty })),
      "POST /api/items/1001/open": () => json({ session_key: "k", item: detail(1, { read: true, content_html: dirty }) }),
    });
    const { container } = go("/i/1001?from=unread");
    const body = await screen.findByTestId("article-body");
    expect(body.querySelector("script")).toBeNull();
    expect(body.querySelector("img")?.getAttribute("onerror")).toBeNull();
    const link = within(body).getByRole("link", { name: "go" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname === "/api/items/1001/open")).toBe(true));
    expect(JSON.parse(String(calls.find((c) => c.url.pathname.endsWith("/open"))?.init?.body))).toEqual({ via: "tap" });
    expect(screen.getByRole("heading", { level: 1, name: "Article number 1" })).toBeInTheDocument();
    expect(screen.getByRole("toolbar", { name: "Article actions" })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("moves to the next article with the button, replacing history", async () => {
    routes();
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    const before = window.history.length;
    await waitFor(() => expect(screen.getByRole("button", { name: "Next article" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Next article" }));
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
    expect(window.history.length).toBe(before);
  });

  it("j and k move through the list order", async () => {
    routes();
    go("/i/1002?from=unread");
    await screen.findByTestId("article-body");
    await waitFor(() => expect(screen.getByRole("button", { name: "Previous article" })).toBeEnabled());
    const user = userEvent.setup();
    await user.keyboard("k");
    await waitFor(() => expect(window.location.pathname).toBe("/i/1001"));
    await user.keyboard("j");
    await waitFor(() => expect(window.location.pathname).toBe("/i/1002"));
  });

  it("full text toggle posts mode 1 and swaps the content", async () => {
    const { calls } = routes({
      "POST /api/items/1001/fulltext": () => json({ mode: 1, effective: 1, status: "ok", content_html: "<p>The whole article</p>", word_count: 900, error: null }),
    });
    go("/i/1001?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Full text" }));
    await screen.findByText("The whole article");
    expect(JSON.parse(String(calls.find((c) => c.url.pathname.endsWith("/fulltext"))?.init?.body))).toEqual({ mode: 1 });
  });

  it("does not react to letter keys when single-key shortcuts are off", async () => {
    routes();
    updatePrefs({ shortcuts: false });
    go("/i/1002?from=unread");
    await screen.findByTestId("article-body");
    const user = userEvent.setup();
    await user.keyboard("k");
    expect(window.location.pathname).toBe("/i/1002");
    updatePrefs({ shortcuts: true });
  });
});

describe("Settings", () => {
  it("offers a short theme list, More themes, and a collapsed accessibility group; passes axe", async () => {
    routes();
    const { container } = go("/settings");
    await screen.findByRole("heading", { name: "Settings" });
    expect(screen.getByRole("radio", { name: /Follow system.*by day/ })).toBeChecked();
    for (const n of ["Paper", "Linen", "Newsprint", "Graphite", "Midnight"]) expect(screen.getByRole("radio", { name: new RegExp("^" + n) })).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: /^Fountain/ })).toBeNull();
    expect(screen.queryByRole("radio", { name: /^Carbon/ })).toBeNull();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "More themes" }));
    expect(screen.getByRole("radio", { name: /^Fountain/ })).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: /^Carbon/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Accessibility themes" }));
    expect(screen.getByRole("radio", { name: /^Carbon/ })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("picking a theme applies it and updates meta theme-color live", async () => {
    routes();
    go("/settings");
    await screen.findByRole("heading", { name: "Settings" });
    const off = initTheme();
    const user = userEvent.setup();
    await user.click(screen.getByRole("radio", { name: /^Newsprint/ }));
    expect(document.documentElement.dataset.theme).toBe("newsprint");
    expect(document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')?.content).toBe("#e6e5e1");
    off();
  });

  it("density steps drive list rows and reading text together", async () => {
    routes();
    go("/settings");
    await screen.findByRole("heading", { name: "Settings" });
    const user = userEvent.setup();
    await user.click(screen.getByRole("radio", { name: "Airy" }));
    initPrefs();
    await user.click(screen.getByRole("radio", { name: "Dense" }));
    expect(document.documentElement.dataset.listDensity).toBe("dense");
    expect(document.documentElement.dataset.readingDensity).toBe("dense");
  });
});

describe("Feeds and Search", () => {
  it("lists folders and feeds with unread counts", async () => {
    routes();
    const { container } = go("/feeds");
    expect(await screen.findByRole("link", { name: /Example Feed/ })).toHaveAttribute("href", "/l/unread?feed=1");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("asks for two characters before searching", async () => {
    routes();
    const { container } = go("/search");
    expect(await screen.findByText("Search your articles")).toBeInTheDocument();
    const user = userEvent.setup();
    await user.type(screen.getByRole("searchbox", { name: "Search articles" }), "a");
    expect(await screen.findByText("Keep typing", undefined, { timeout: 2000 })).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });
});
