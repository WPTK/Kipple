// The design review's surfaces and the browser plumbing that reaches them, shared by design-shots.mjs (pictures) and
// design-probes.mjs (measurements), so both look at the same screens in the same state. docs/maintainers/design-review.md
// says how the review uses them.
//
// A surface is one screen, state or open menu: `area` is the review area, `go(page, lib)` brings it up, `layout` is set
// on the device first, `sizes` marks the screens that also get the text-size extremes, `tall` the ones taller than a
// window, `signedOut` the ones that need a context with no session.
/* global document, window, getComputedStyle -- used inside page.evaluate, which runs in the browser */
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

// Playwright lives in web/node_modules, which a script outside web/ does not see by itself.
export const webRequire = createRequire(new URL("../../../web/package.json", import.meta.url));
export const { chromium } = webRequire("@playwright/test");
export const schemes = webRequire("./src/theme/schemes.json");

export const DEVICES = {
  phone: { viewport: { width: 390, height: 844 }, mobile: true },
  tablet: { viewport: { width: 820, height: 1180 }, mobile: true },
  desktop: { viewport: { width: 1440, height: 900 }, mobile: false },
};
/** The text sizes beside the default (web/src/lib/prefs.ts TEXT_SIZES). */
export const EXTRA_SIZES = [0.875, 1.5];
// The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
export const SEED_USER = "dev";
export const SEED_PASSWORD = "dev-password-only-for-local-testing";

const firstRow = (page) => page.locator('main article[data-item-id] a[href^="/i/"]').first();
const openFirst = async (page) => {
  await firstRow(page).waitFor({ timeout: 20000 });
  await firstRow(page).click();
  await page.getByRole("toolbar", { name: "Article actions" }).or(page.locator('[aria-label="Article actions"]')).first().waitFor({ timeout: 15000 });
  // The toolbar shows before the text; wait for the text, or a capture can show the loading skeleton.
  await page.locator("main .article-body").first().waitFor({ timeout: 15000 });
};
const list = (path) => async (page) => {
  await page.goto(path);
  await page.locator("main").first().waitFor({ timeout: 15000 });
};
// The layout ids come from the app (`LAYOUT_IDS` in devicePrefs.ts), so a new layout is reviewed without touching this file.
export const LAYOUT_IDS = [...readFileSync(new URL("../../../web/src/lib/devicePrefs.ts", import.meta.url), "utf8").match(/LAYOUT_IDS = \[([^\]]+)\]/)[1].matchAll(/"(\w+)"/g)].map((m) => m[1]);
const LAYOUT_SURFACES = LAYOUT_IDS.map((layout) => ({
  id: `list-${layout}`, area: "lists", title: `All unread, ${layout} layout`, layout, sizes: layout === "magazine" || layout === "headlines",
  go: async (page) => {
    await list("/l/unread")(page);
    await page.locator("main article[data-item-id]").first().waitFor({ timeout: 20000 });
  },
}));
export const SURFACES = [
  ...LAYOUT_SURFACES,
  { id: "list-folder", area: "lists", title: "A folder (News)", go: async (page, lib) => list(`/l/unread?folder=${lib.folder("News")}`)(page) },
  { id: "list-long-feed-name", area: "lists", title: "A feed with a long name", go: async (page, lib) => list(`/l/unread?feed=${lib.feed("Example Site")}`)(page) },
  { id: "list-pictures", area: "lists", title: "Portrait, panorama and tiny pictures (Cards)", layout: "cards", go: async (page, lib) => list(`/l/all?folder=${lib.folder("Pictures")}`)(page) },
  { id: "list-unicode", area: "lists", title: "Unicode and right-to-left titles", go: async (page, lib) => list(`/l/all?feed=${lib.feed("Ünïcödé")}`)(page) },
  { id: "state-starred", area: "states", title: "Starred", go: list("/l/starred") },
  { id: "state-muted", area: "states", title: "Muted", go: list("/l/muted") },
  { id: "state-empty-feed", area: "states", title: "A feed with no articles", go: async (page, lib) => list(`/l/all?feed=${lib.feed("Empty Feed")}`)(page) },
  { id: "state-caught-up", area: "states", title: "Nothing unread (Quiet folder after reading)", go: async (page, lib) => list(`/l/unread?feed=${lib.feed("Empty Feed")}`)(page) },
  { id: "state-feed-error", area: "states", title: "A feed that fails", go: async (page, lib) => list(`/l/all?feed=${lib.feed("Broken Feed")}`)(page) },
  { id: "state-not-found", area: "states", title: "An address that is not a page", go: list("/no-such-page") },
  { id: "article-picture", area: "article", title: "An article with a lead picture", sizes: true, go: async (page, lib) => (await list(`/l/all?feed=${lib.feed("The Morning Ledger")}`)(page), openFirst(page)) },
  { id: "article-long", area: "article", title: "A long article with every block type", tall: true, go: async (page, lib) => (await list(`/l/all?feed=${lib.feed("Long Essays")}`)(page), openFirst(page)) },
  { id: "article-unicode", area: "article", title: "An article in Japanese", go: async (page, lib) => (await list(`/l/all?feed=${lib.feed("Ünïcödé")}`)(page), openFirst(page)) },
  {
    id: "menu-article-more", area: "menus", title: "The article's More actions menu",
    go: async (page, lib) => {
      await list(`/l/all?feed=${lib.feed("The Morning Ledger")}`)(page);
      await openFirst(page);
      await page.locator('[aria-label="Article actions"]').getByRole("button", { name: "More actions" }).click();
      await page.getByRole("menu").waitFor({ timeout: 5000 });
    },
  },
  {
    id: "menu-row", area: "menus", title: "A row's More actions menu",
    go: async (page) => {
      await list("/l/unread")(page);
      const more = page.locator("main article[data-item-id]").first().getByRole("button", { name: /^More actions/ });
      await more.click({ force: true });
      await page.getByRole("menu").waitFor({ timeout: 5000 });
    },
  },
  {
    id: "menu-list-options", area: "menus", title: "Show and sort (layout and order)",
    go: async (page) => {
      await list("/l/unread")(page);
      await page.getByRole("button", { name: /^Show and sort/ }).click();
      await page.getByRole("menu").waitFor({ timeout: 5000 });
    },
  },
  { id: "nav-feeds", area: "navigation", title: "Feeds screen", tall: true, go: list("/feeds") },
  { id: "nav-search-empty", area: "search", title: "Search, nothing typed", go: list("/search") },
  {
    id: "nav-search-results", area: "search", title: "Search results for bridges",
    go: async (page) => {
      await list("/search")(page);
      const box = page.locator('input[type="search"]').first();
      await box.fill("bridges");
      await box.press("Enter");
      await page.locator("main article[data-item-id]").first().waitFor({ timeout: 15000 });
    },
  },
  { id: "stats", area: "stats", title: "Statistics", tall: true, go: list("/stats") },
  { id: "stats-wrapped", area: "stats", title: "Wrapped", tall: true, go: list("/stats/wrapped") },
  { id: "health", area: "settings", title: "Feed health", tall: true, go: list("/health") },
  { id: "settings", area: "settings", title: "Settings, first screen", go: list("/settings") },
  ...["appearance", "sync", "statistics", "filters", "account", "advanced", "about"].map((g) => ({
    id: `settings-${g}`, area: "settings", title: `Settings: ${g}`, tall: true, sizes: g === "appearance", go: list(`/settings/${g}`),
  })),
  { id: "signed-out", area: "setup", title: "Sign-in screen", signedOut: true, go: list("/") },
];

/** The surfaces whose id matches `only` (a regexp), or all of them. */
export function pickSurfaces(only) {
  const picked = only ? SURFACES.filter((s) => new RegExp(only).test(s.id)) : SURFACES;
  if (picked.length === 0) throw new Error(`--only ${only} matches no surface (have ${SURFACES.map((s) => s.id).join(", ")})`);
  return picked;
}

/** The scheme ids named by a --themes value ("all" or a comma list), checked against schemes.json. */
export function pickThemes(value) {
  const ids = schemes.map((s) => s.id);
  const picked = value === "all" ? ids : value.split(",").map((s) => s.trim());
  for (const t of picked) if (!ids.includes(t)) throw new Error(`unknown theme ${t} (have ${ids.join(", ")})`);
  return picked;
}

/** The device names of a --devices value, checked against DEVICES. */
export function pickDevices(value) {
  const picked = value.split(",").map((s) => s.trim());
  for (const d of picked) if (!DEVICES[d]) throw new Error(`unknown device ${d} (have ${Object.keys(DEVICES).join(", ")})`);
  return picked;
}

/** The origin of `url`, or exits: the scripts sign in with the seed's credentials, so only a loopback instance is allowed. */
export function loopbackOrigin(url) {
  const origin = new URL(url).origin;
  if (!["127.0.0.1", "[::1]"].includes(new URL(origin).hostname)) {
    console.error(`refusing ${origin}: this signs in with the seed's credentials, so only a loopback instance is allowed`);
    process.exit(2);
  }
  return origin;
}

/**
 * A signed-in session on the seeded instance at `origin`: `context(device, signedOutScheme, extra)` opens a browser
 * context (signed out when a "light" or "dark" scheme is given; `extra` adds context options), `setProfile` patches the
 * device's profile, `library` finds feeds and folders by name, `removeDevices` removes every device the run registered.
 */
export function designSession(browser, origin, user = SEED_USER, password = SEED_PASSWORD) {
  const WRITE = { "X-Kipple-Client": "web", Origin: origin };
  const deviceList = [];
  const mine = new Set();
  const devicesOf = async (ctx) => (await (await ctx.request.get("/api/devices", { headers: { Accept: "application/json" } })).json()).devices ?? [];
  async function context(device, signedOutScheme, extra = {}) {
    const d = DEVICES[device] ?? device;
    const ctx = await browser.newContext({
      baseURL: origin, viewport: d.viewport, deviceScaleFactor: 1, isMobile: d.mobile, hasTouch: d.mobile, reducedMotion: "reduce", serviceWorkers: "block",
      colorScheme: signedOutScheme ?? "light", ...extra,
    });
    if (signedOutScheme) return ctx;
    const page = await ctx.newPage();
    await page.goto("/");
    await page.getByLabel("Username").fill(user);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
    await page.close();
    deviceList.push(device);
    const me = (await devicesOf(ctx)).find((x) => x.current);
    if (me) mine.add(me.id);
    return ctx;
  }
  const setProfile = async (ctx, patch) => {
    const r = await ctx.request.patch("/api/device", { headers: WRITE, data: patch });
    if (!r.ok()) throw new Error(`PATCH /api/device: ${r.status()} ${await r.text()}`);
  };
  /** Feed and folder ids by (part of) their name, from the bootstrap. */
  async function library(ctx) {
    const boot = await (await ctx.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
    const find = (items, key, part) => {
      const hit = items.find((x) => x[key].includes(part));
      if (!hit) throw new Error(`nothing named like "${part}" (seed with KIPPLE_SEED_SET=design)`);
      return hit.id;
    };
    return { feed: (p) => find(boot.feeds, "title", p), folder: (p) => find(boot.folders, "name", p) };
  }
  /**
   * Removes the devices this run signed in, so repeated runs stay under the server's 50-device limit. Devices of other
   * runs (probes can run in parallel) and other sessions are left alone. The device asking cannot remove itself, so one
   * stays per run; above the limit the server evicts the least recently seen.
   */
  async function removeDevices() {
    const ctx = await context(deviceList[0] ?? "desktop");
    try {
      for (const d of (await devicesOf(ctx)).filter((x) => !x.current && mine.has(x.id))) {
        await ctx.request.delete(`/api/devices/${d.id}`, { headers: WRITE });
        mine.delete(d.id);
      }
    } finally {
      await ctx.close();
    }
  }
  return { context, setProfile, library, removeDevices };
}

/** Waits for fonts and for the pictures in the window to finish (or 5 s), and drops the caret and any focus ring left by a click that only navigated. */
export async function settle(page, keepFocus) {
  await page.evaluate(() => document.fonts.ready);
  await page
    .waitForFunction(() => [...document.images].filter((i) => i.getBoundingClientRect().top < window.innerHeight).every((i) => i.complete), null, { timeout: 5000 })
    .catch(() => {});
  if (!keepFocus) await page.evaluate(() => document.activeElement?.blur?.());
  await page.addStyleTag({ content: "*{caret-color:transparent!important}" });
  await page.waitForTimeout(300);
}

/** Scrolls whatever scrolls most on the page by one window; false when it is already at the end. */
export const scrollOnce = (page) =>
  page.evaluate(() => {
    const els = [document.scrollingElement, ...document.querySelectorAll("*")].filter((e) => {
      if (!e) return false;
      const oy = e === document.scrollingElement ? "auto" : getComputedStyle(e).overflowY;
      return /(auto|scroll)/.test(oy) && e.scrollHeight - e.clientHeight > 8;
    });
    const el = els.sort((a, b) => b.scrollHeight - b.clientHeight - (a.scrollHeight - a.clientHeight))[0];
    if (!el || el.scrollTop + el.clientHeight >= el.scrollHeight - 4) return false;
    el.scrollTop += el.clientHeight - 48;
    return true;
  });
