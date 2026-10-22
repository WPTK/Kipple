// Captures every screen of the app for the design review: each surface (a screen, a state or an open menu) on a phone,
// a tablet and a desktop, in the chosen themes, plus the smallest and largest text sizes on the screens that carry the
// most text. Writes PNGs, a manifest and contact sheets (index.html, and one PNG per area) to --out. It judges nothing:
// it is what a person or a reviewer looks at. docs/maintainers/design-review.md says how the review uses it.
//
//   KIPPLE_SEED_SET=design npm run seed            (in one terminal; it waits for its feeds itself)
//   node ../scripts/maintainers/design-shots.mjs --out ../.design/shots [--themes paper,midnight|all] [--only <regexp>]
//        [--devices phone,tablet,desktop] [--no-sizes] [--url http://127.0.0.1:1919]
// Run from web/ (Playwright is the one web/ installs); the output is relative to it. .design/ is gitignored.
//
// Each device and theme is one signed-in browser context, so one device on the server, whose profile gets the theme, text
// size and layout through PATCH /api/device before its surfaces load. Motion is reduced and the first-run hints are
// marked seen, so two runs of the same build give the same pictures. A screen taller than the window is captured a
// window at a time (up to four), scrolling whatever scrolls. A surface that cannot be reached is listed in the manifest
// with the reason and the run carries on; the exit code is 1 when any was missed. Only a loopback address is accepted,
// with the seed's throwaway credentials, and the devices it registered are removed at the end.
/* global document, window, getComputedStyle -- used inside page.evaluate, which runs in the browser */
import { mkdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";

// Playwright lives in web/node_modules, which a script outside web/ does not see by itself.
const webRequire = createRequire(new URL("../../web/package.json", import.meta.url));
const { chromium } = webRequire("@playwright/test");
const schemes = webRequire("./src/theme/schemes.json");

const { values: opt } = parseArgs({
  options: {
    url: { type: "string", default: "http://127.0.0.1:1919" },
    out: { type: "string" },
    themes: { type: "string", default: "paper,midnight" },
    devices: { type: "string", default: "phone,tablet,desktop" },
    only: { type: "string" },
    "no-sizes": { type: "boolean", default: false },
    user: { type: "string", default: "dev" },
    // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
    password: { type: "string", default: "dev-password-only-for-local-testing" },
  },
});
if (!opt.out) {
  console.error("usage: node ../scripts/maintainers/design-shots.mjs --out <dir> [--themes a,b|all] [--devices phone,tablet,desktop] [--only <regexp>] [--no-sizes]");
  process.exit(2);
}
const origin = new URL(opt.url).origin;
if (!["127.0.0.1", "[::1]"].includes(new URL(origin).hostname)) {
  console.error(`refusing ${origin}: this signs in with the seed's credentials, so only a loopback instance is allowed`);
  process.exit(2);
}

const DEVICES = {
  phone: { viewport: { width: 390, height: 844 }, mobile: true },
  tablet: { viewport: { width: 820, height: 1180 }, mobile: true },
  desktop: { viewport: { width: 1440, height: 900 }, mobile: false },
};
const schemeIds = schemes.map((s) => s.id);
const themes = opt.themes === "all" ? schemeIds : opt.themes.split(",").map((s) => s.trim());
for (const t of themes) if (!schemeIds.includes(t)) throw new Error(`unknown theme ${t} (have ${schemeIds.join(", ")})`);
const devices = opt.devices.split(",").map((s) => s.trim());
for (const d of devices) if (!DEVICES[d]) throw new Error(`unknown device ${d} (have ${Object.keys(DEVICES).join(", ")})`);
/** The text sizes beside the default (web/src/lib/prefs.ts TEXT_SIZES), captured on surfaces marked `sizes`, first theme only. */
const EXTRA_SIZES = opt["no-sizes"] ? [] : [0.875, 1.5];

// ---- Surfaces ---------------------------------------------------------------------------------------------------------
// `area` is the review area (docs/maintainers/design-review.md). `go(page, lib)` brings the surface up; `layout` is set
// on the device first; `sizes` adds the text-size extremes; `signedOut` uses a context with no session.
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
const SURFACES = [
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
const surfaces = opt.only ? SURFACES.filter((s) => new RegExp(opt.only).test(s.id)) : SURFACES;
if (surfaces.length === 0) throw new Error(`--only ${opt.only} matches no surface (have ${SURFACES.map((s) => s.id).join(", ")})`);

// ---- Capture ----------------------------------------------------------------------------------------------------------
const out = resolve(opt.out);
mkdirSync(out, { recursive: true });
const WRITE = { "X-Kipple-Client": "web", Origin: origin };
const shots = [];
const missed = [];
const browser = await chromium.launch();

/** A browser context for `device`, signed in unless `signedOutScheme` ("light" or "dark") is given. */
async function context(device, signedOutScheme) {
  const d = DEVICES[device];
  const ctx = await browser.newContext({
    baseURL: origin, viewport: d.viewport, deviceScaleFactor: 1, isMobile: d.mobile, hasTouch: d.mobile, reducedMotion: "reduce", serviceWorkers: "block",
    colorScheme: signedOutScheme ?? "light",
  });
  if (signedOutScheme) return ctx;
  const page = await ctx.newPage();
  await page.goto("/");
  await page.getByLabel("Username").fill(opt.user);
  await page.getByLabel("Password").fill(opt.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
  await page.close();
  return ctx;
}
const setProfile = async (ctx, patch) => {
  const r = await ctx.request.patch("/api/device", { headers: WRITE, data: patch });
  if (!r.ok()) throw new Error(`PATCH /api/device: ${r.status()} ${await r.text()}`);
};

/** Waits for fonts and for the pictures in the window to finish (or 5 s), and drops the caret and any focus ring left by a click that only navigated. */
async function settle(page, keepFocus) {
  await page.evaluate(() => document.fonts.ready);
  await page
    .waitForFunction(() => [...document.images].filter((i) => i.getBoundingClientRect().top < window.innerHeight).every((i) => i.complete), null, { timeout: 5000 })
    .catch(() => {});
  if (!keepFocus) await page.evaluate(() => document.activeElement?.blur?.());
  await page.addStyleTag({ content: "*{caret-color:transparent!important}" });
  await page.waitForTimeout(300);
}

/** Scrolls whatever scrolls most on the page by one window; false when it is already at the end. */
const scrollOnce = (page) =>
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

async function capture(ctx, lib, s, device, theme, size) {
  const name = `${s.id}__${device}__${theme}${size === 1 ? "" : `__text${size}`}`;
  const page = await ctx.newPage();
  // Requests still waiting for an answer, named in a miss: they tell a slow server from a page that never asked.
  const pending = new Map();
  const errors = [];
  page.on("request", (r) => pending.set(r, Date.now()));
  page.on("requestfinished", (r) => pending.delete(r));
  page.on("requestfailed", (r) => pending.delete(r));
  page.on("pageerror", (e) => errors.push(String(e).split("\n")[0]));
  try {
    if (!s.signedOut) await setProfile(ctx, { "client.layout": s.layout ?? "magazine" });
    await s.go(page, lib);
    await settle(page, s.area === "menus");
    const files = [`${name}.png`];
    await page.screenshot({ path: join(out, files[0]) });
    for (let i = 2; s.tall && i <= 4 && (await scrollOnce(page)); i++) {
      await settle(page, false);
      files.push(`${name}__${i}.png`);
      await page.screenshot({ path: join(out, files.at(-1)) });
    }
    shots.push({ surface: s.id, area: s.area, title: s.title, device, theme, size, files });
    console.log(`ok   ${name}${files.length > 1 ? ` (${files.length} windows)` : ""}`);
  } catch (e) {
    const why = String(e.message).split("\n")[0];
    const waiting = [...pending].filter(([r]) => !r.url().endsWith("/api/events")).map(([r, t]) => `${r.method()} ${new URL(r.url()).pathname} (${Math.round((Date.now() - t) / 1000)} s)`);
    missed.push({ surface: s.id, device, theme, size, why, url: page.url(), waiting, errors });
    console.error(`MISS ${name}: ${why}`);
    await page.screenshot({ path: join(out, `${name}__missed.png`) }).catch(() => {});
  } finally {
    await page.close();
  }
}

try {
  let lib;
  for (const device of devices) {
    const runs = themes.map((theme) => ({ theme, size: 1 })).concat(EXTRA_SIZES.map((size) => ({ theme: themes[0], size })));
    for (const { theme, size } of runs) {
      const todo = surfaces.filter((s) => !s.signedOut && (size === 1 || s.sizes));
      if (todo.length) {
        const ctx = await context(device);
        try {
          await setProfile(ctx, { "ui.theme": theme, "client.text_size": size, "client.peek_seen": true });
          if (!lib) lib = await library(ctx);
          for (const s of todo) await capture(ctx, lib, s, device, theme, size);
        } finally {
          await ctx.close();
        }
      }
      // A signed-out page follows the device's light or dark setting, so it is taken in the matching colour scheme.
      for (const s of surfaces.filter((x) => x.signedOut && size === 1)) {
        const ctx = await context(device, schemes.find((x) => x.id === theme).kind);
        await capture(ctx, lib, s, device, theme, size);
        await ctx.close();
      }
    }
  }
  await removeDevices();
  writeFileSync(join(out, "manifest.json"), JSON.stringify({ origin, taken: new Date().toISOString(), themes, devices, shots, missed }, null, 2));
  writeFileSync(join(out, "index.html"), sheet(shots));
  await areaSheets();
} finally {
  await browser.close();
}
console.log(`\n${shots.length} surface captures in ${out}${missed.length ? `, ${missed.length} missed (see manifest.json)` : ""}`);
process.exit(missed.length ? 1 : 0);

/** Feed and folder ids by (part of) their name, from the bootstrap. */
async function library(ctx) {
  const boot = await (await ctx.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
  const find = (list, key, part) => {
    const hit = list.find((x) => x[key].includes(part));
    if (!hit) throw new Error(`nothing named like "${part}" (seed with KIPPLE_SEED_SET=design)`);
    return hit.id;
  };
  return { feed: (p) => find(boot.feeds, "title", p), folder: (p) => find(boot.folders, "name", p) };
}

/** Removes every device but the one asking, so repeated runs stay under the server's 50-device limit. */
async function removeDevices() {
  const ctx = await context(devices[0]);
  try {
    const list = await (await ctx.request.get("/api/devices", { headers: { Accept: "application/json" } })).json();
    for (const d of (list.devices ?? []).filter((x) => !x.current)) await ctx.request.delete(`/api/devices/${d.id}`, { headers: WRITE });
  } finally {
    await ctx.close();
  }
}

/** The contact sheet: one section per area, one row per surface, every capture of it side by side. `?area=` shows one area. */
function sheet(list) {
  const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
  const areas = [...new Set(list.map((s) => s.area))];
  const body = areas
    .map((area) => {
      const rows = [...new Set(list.filter((s) => s.area === area).map((s) => s.surface))].map((id) => {
        const caps = list.filter((s) => s.surface === id);
        const cells = caps
          .flatMap((c) => c.files.map((f, i) => `<figure class="${c.device}"><a href="${esc(f)}"><img src="${esc(f)}" loading="lazy"></a><figcaption>${esc(`${c.device} · ${c.theme}${c.size === 1 ? "" : ` · text ${c.size}`}${i ? ` · ${i + 1}` : ""}`)}</figcaption></figure>`))
          .join("");
        return `<h3 id="${esc(id)}">${esc(caps[0].title)} <code>${esc(id)}</code></h3><div class="row">${cells}</div>`;
      });
      return `<section data-area="${esc(area)}"><h2>${esc(area)}</h2>${rows.join("")}</section>`;
    })
    .join("");
  return `<!doctype html><meta charset="utf-8"><title>Kipple design review captures</title><style>
body{font:14px system-ui,sans-serif;margin:16px;background:#e9e9ec;color:#111}h2{margin:32px 0 4px;text-transform:capitalize}
h3{margin:18px 0 6px;font-size:14px}code{color:#666;font-weight:400}.row{display:flex;gap:10px;align-items:flex-start;overflow-x:auto}
figure{margin:0;flex:none}img{display:block;border:1px solid #bbb;background:#fff}figcaption{font-size:11px;color:#444;margin-top:2px}
.phone img{width:180px}.tablet img{width:260px}.desktop img{width:420px}</style>
<script>addEventListener("DOMContentLoaded",()=>{const a=new URLSearchParams(location.search).get("area");if(a)for(const s of document.querySelectorAll("section"))if(s.dataset.area!==a)s.remove()})</script>
<h1>Kipple design review captures</h1><p>${esc(new Date().toISOString().slice(0, 16).replace("T", " "))} UTC · ${list.length} captures · themes ${esc(themes.join(", "))}</p>${body}`;
}

/** sheet-<area>.png: the contact sheet of one area as a picture, for sending where a folder of files cannot go. */
async function areaSheets() {
  const page = await browser.newPage({ viewport: { width: 1700, height: 1000 } });
  try {
    for (const area of [...new Set(shots.map((s) => s.area))]) {
      await page.goto(`${pathToFileURL(join(out, "index.html")).href}?area=${area}`);
      await page.evaluate(() => Promise.all([...document.images].map((i) => (i.loading = "eager", i.decode().catch(() => {})))));
      await page.screenshot({ path: join(out, `sheet-${area}.png`), fullPage: true });
    }
  } finally {
    await page.close();
  }
}
