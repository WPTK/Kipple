// What the design review tools share: the devices, the surfaces of the app (each screen, state and open menu, and how to
// bring it up), the command-line options they have in common, and a signed-in browser session against a seeded
// instance. design-shots.mjs captures the surfaces; design-probes.mjs measures them. docs/maintainers/design-review.md
// says how the review uses both.
/* global document, window, getComputedStyle -- used inside page.evaluate, which runs in the browser */
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

/** The options both tools take, for node:util parseArgs. */
export const COMMON_OPTIONS = {
  url: { type: "string", default: "http://127.0.0.1:1919" },
  out: { type: "string" },
  themes: { type: "string", default: "paper,midnight" },
  devices: { type: "string", default: "phone,tablet,desktop" },
  only: { type: "string" },
  user: { type: "string", default: "dev" },
  // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
  password: { type: "string", default: "dev-password-only-for-local-testing" },
};

/** Checks the common options; exits 2 with `usage` on a missing --out or a non-loopback --url. */
export function resolveCommon(opt, usage) {
  if (!opt.out) {
    console.error(usage);
    process.exit(2);
  }
  const origin = new URL(opt.url).origin;
  if (!["127.0.0.1", "[::1]"].includes(new URL(origin).hostname)) {
    console.error(`refusing ${origin}: this signs in with the seed's credentials, so only a loopback instance is allowed`);
    process.exit(2);
  }
  const schemeIds = schemes.map((s) => s.id);
  const themes = opt.themes === "all" ? schemeIds : opt.themes.split(",").map((s) => s.trim());
  for (const t of themes) if (!schemeIds.includes(t)) throw new Error(`unknown theme ${t} (have ${schemeIds.join(", ")})`);
  const devices = opt.devices.split(",").map((s) => s.trim());
  for (const d of devices) if (!DEVICES[d]) throw new Error(`unknown device ${d} (have ${Object.keys(DEVICES).join(", ")})`);
  const surfaces = opt.only ? SURFACES.filter((s) => new RegExp(opt.only).test(s.id)) : SURFACES;
  if (surfaces.length === 0) throw new Error(`--only ${opt.only} matches no surface (have ${SURFACES.map((s) => s.id).join(", ")})`);
  return { origin, themes, devices, surfaces };
}

// ---- Surfaces ---------------------------------------------------------------------------------------------------------
// `area` is the review area (docs/maintainers/design-review.md). `go(page, lib)` brings the surface up; `layout` is set
// on the device first; `sizes` adds the text-size extremes; `signedOut` uses a context with no session; `tall` screens
// scroll; a `menus` surface keeps its menu open.
const firstRow = (page) => page.locator('main article[data-item-id] a[href^="/i/"]').first();
const openFirst = async (page) => {
  await firstRow(page).waitFor({ timeout: 20000 });
  await firstRow(page).click();
  await page.getByRole("toolbar", { name: "Article actions" }).or(page.locator('[aria-label="Article actions"]')).first().waitFor({ timeout: 15000 });
};
const list = (path) => async (page) => {
  await page.goto(path);
  await page.locator("main").first().waitFor({ timeout: 15000 });
};
const LAYOUT_SURFACES = ["magazine", "cards", "compact", "inbox", "headlines", "gazette"].map((layout) => ({
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
    id: "menu-list-options", area: "menus", title: "List options (layout and order)",
    go: async (page) => {
      await list("/l/unread")(page);
      await page.getByRole("button", { name: /^List options/ }).click();
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

// ---- Session ----------------------------------------------------------------------------------------------------------
export const writeHeaders = (origin) => ({ "X-Kipple-Client": "web", Origin: origin });

/**
 * A browser session against the seeded instance at `origin`. `context(device, { signedOutScheme, ...contextOptions })`
 * gives a context for the device, signed in unless signedOutScheme ("light" or "dark") is given; extra options (such as
 * a viewport or forcedColors) are passed to Playwright. Each signed-in context is one device on the server.
 */
export function session(browser, { origin, user, password }) {
  const WRITE = writeHeaders(origin);
  const mine = new Set();
  const devicesOf = async (ctx) => (await (await ctx.request.get("/api/devices", { headers: { Accept: "application/json" } })).json()).devices ?? [];
  async function context(device, { signedOutScheme, ...extra } = {}) {
    const d = DEVICES[device];
    const ctx = await browser.newContext({
      baseURL: origin, viewport: d.viewport, deviceScaleFactor: 1, isMobile: d.mobile, hasTouch: d.mobile, reducedMotion: "reduce", serviceWorkers: "block",
      colorScheme: signedOutScheme ?? "light",
      ...extra,
    });
    if (signedOutScheme) return ctx;
    const page = await ctx.newPage();
    await page.goto("/");
    await page.getByLabel("Username").fill(user);
    await page.getByLabel("Password").fill(password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
    await page.close();
    const me = (await devicesOf(ctx)).find((x) => x.current);
    if (me) mine.add(me.id);
    return ctx;
  }
  async function setProfile(ctx, patch) {
    const r = await ctx.request.patch("/api/device", { headers: WRITE, data: patch });
    if (!r.ok()) throw new Error(`PATCH /api/device: ${r.status()} ${await r.text()}`);
  }
  /** Feed and folder ids by (part of) their name, from the bootstrap. */
  async function library(ctx) {
    const boot = await (await ctx.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
    const find = (items, key, part) => {
      const hit = items.find((x) => x[key].includes(part));
      if (!hit) throw new Error(`nothing named like "${part}" (seed with KIPPLE_SEED_SET=design)`);
      return hit.id;
    };
    return { feed: (p) => find(boot.feeds, "title", p), folder: (p) => find(boot.folders, "name", p), boot };
  }
  /**
   * Removes the devices this session signed in (all but the one asking), so runs stay under the server's 50-device limit.
   * Devices of other runs and other people are left alone.
   */
  async function removeDevices(device) {
    const ctx = await context(device);
    try {
      for (const d of (await devicesOf(ctx)).filter((x) => !x.current && mine.has(x.id))) {
        await ctx.request.delete(`/api/devices/${d.id}`, { headers: WRITE });
        mine.delete(d.id);
      }
    } finally {
      await ctx.close();
    }
  }
  return { context, setProfile, library, removeDevices, WRITE };
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
