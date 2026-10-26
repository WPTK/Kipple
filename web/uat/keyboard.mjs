// UAT Suite 1 addition: keyboard-only use and gesture alternatives, in a real browser (issue #214).
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:1919)
//   npm run uat:keyboard               in another, once the feeds have fetched
//   npm run uat:keyboard -- --browser firefox        (or webkit; chromium is the default)
//   npm run uat:keyboard -- --url http://127.0.0.1:7091 --headed
//
// At 1280x800, with the keyboard only (no mouse, no click, except to sign in):
//   K1  Tab order: on every main screen each visible control is reached by Tab, the one that has focus shows a focus
//       indicator, and Tab walks on to the end of the page with no trap, the way back with Shift+Tab too. A list is
//       one Tab stop: from a row reached with the arrow keys deep in the list, a few Tab presses leave it.
//   K2  Escape: every menu and dialog tried opens from the keyboard, keeps focus inside while it is a modal dialog,
//       closes on Escape, and gives focus back to the control that opened it.
//   K3  There are no single-key shortcuts: letter and symbol keys do nothing on a list and are typed in a field.
//       The arrow keys move focus between rows from any control in a row, and a separator still takes them.
// At 390x844 as a touch device:
//   K4  Every swipe or long-press action has an alternative that needs no gesture: a row's Star button and its
//       More actions menu (mark read or unread, star), the article's Back button, and the Refresh all feeds
//       button for pull to refresh; and the feed list's drag to reorder works from the keyboard (Move).
//
// It opens, stars and marks articles on the seed's account (and undoes what it can), so it only runs against a
// loopback address with the seed's credentials unless --allow-remote is given. Like Suite 1 it uses the run's own
// device (uat/results/.device-<host>-<port>.json), never one of the owner's.
// Exit code: 0 clean, 1 findings, 2 setup error. First time on a machine: `npx playwright install chromium`
// (and firefox webkit for --browser).
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium, firefox, webkit } from "@playwright/test";
import { accessControlUrl, onPageError, startSelect, unloadRefusal, waitForPageStill, waitForStillFor } from "./common.mjs";

const setupError = (m) => {
  console.error(m);
  process.exit(2);
};
const ENGINES = { chromium, firefox, webkit };
const { values: opt } = (() => {
  try {
    return parseArgs({
      options: {
        url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919" },
        user: { type: "string", default: process.env.KIPPLE_UAT_USER || "dev" },
        // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
        password: { type: "string", default: process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing" },
        browser: { type: "string", default: process.env.KIPPLE_UAT_BROWSER || "chromium" },
        screenshots: { type: "string" },
        headed: { type: "boolean", default: false },
        "allow-remote": { type: "boolean", default: false },
        help: { type: "boolean", default: false },
      },
    });
  } catch (e) {
    return setupError(`bad arguments: ${e.message}`);
  }
})();
if (opt.help) {
  const lines = readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/);
  console.log(lines.slice(0, lines.findIndex((l) => !l.startsWith("//"))).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}
if (!(opt.browser in ENGINES)) setupError(`unknown --browser ${opt.browser}. Known: ${Object.keys(ENGINES).join(", ")}`);
const base = (() => {
  try {
    return new URL(opt.url);
  } catch {
    return setupError(`bad --url ${opt.url}`);
  }
})();
const origin = base.origin;
const seedCredentials = opt.user === "dev" && opt.password === "dev-password-only-for-local-testing";
if (!opt["allow-remote"] && (!["127.0.0.1", "[::1]"].includes(base.hostname) || !seedCredentials)) {
  setupError("only a loopback --url with the seed's account, unless --allow-remote (point it at a throwaway instance)");
}
if (opt.screenshots) mkdirSync(opt.screenshots, { recursive: true });
const webDir = fileURLToPath(new URL("../", import.meta.url));
const deviceFile = join(webDir, "uat", "results", `.device-${base.hostname.replace(/[^\w.-]/g, "_")}-${base.port || "default"}.json`);

const findings = [];
const fail = (where, what) => {
  findings.push(`${where}: ${what}`);
  console.error(`FAIL ${where}: ${what}`);
};
/** Said as it happens and again in the closing summary, so it is not lost when a later check throws. */
const notes = [];
const note = (where, what) => {
  notes.push(`${where}: ${what}`);
  console.log(`note ${where}: ${what}`);
};
const pass = (where, what) => console.log(`ok   ${where}: ${what}`);
const check = (cond, where, okText, failText) => (cond ? pass(where, okText) : fail(where, failText));

// Safari and WebKit leave links out of the Tab order unless "Press Tab to highlight each item" (Option+Tab) is on, so a
// WebKit run expects the buttons and fields only. A platform setting, not something the page can change.
const WEBKIT_NO_LINK_TAB = true;

// ---- in-page helpers, installed after every full load as window.__kk ----
function installHelpers() {
  const roving = '[role="radio"],[role="tab"],[role="menuitemradio"],input[type="radio"]';
  const keyOf = (e) =>
    [e.tagName, e.getAttribute("role") ?? "", e.getAttribute("aria-label") ?? "", e.getAttribute("href") ?? "", e.id, e.getAttribute("name") ?? "", (e.textContent ?? "").trim().slice(0, 30)].join("|");
  const ringOf = (st) => st.outlineStyle !== "none" && parseFloat(st.outlineWidth) > 0 && st.outlineColor !== "rgba(0, 0, 0, 0)";
  /**
   * Whether a list row control's focus ring is cut off by a box that clips its overflow (the row's neighbours, the
   * swipe container, a line-clamped title), checked from the boxes: the ring's rectangle is the control's box, or for a
   * ring drawn by an absolutely placed pseudo-element (a row link's ::before) its containing block's, grown by outline offset and width.
   * Only boxes inside the list's scroller count: the scroller clipping a row scrolled half out of view is not a fault.
   */
  const ringClipped = (e, s) => {
    const scroller = e.closest('[data-testid="list-scroll"]');
    if (!scroller || !e.closest("[data-item-id]")) return false;
    const pseudo = [getComputedStyle(e, "::before"), getComputedStyle(e, "::after")].find((p) => ringOf(p) && p.position === "absolute");
    const own = ringOf(s);
    const st = own ? s : pseudo;
    if (!own && !pseudo) return false;
    const box = own ? e : e.offsetParent;
    if (!box) return false;
    const grow = parseFloat(st.outlineOffset) + parseFloat(st.outlineWidth);
    const r = box.getBoundingClientRect();
    const ring = { top: r.top - grow, left: r.left - grow, bottom: r.bottom + grow, right: r.right + grow };
    for (let a = own ? e.parentElement : box; a && a !== scroller; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowX === "visible" && cs.overflowY === "visible") continue;
      const c = a.getBoundingClientRect();
      if (ring.top < c.top - 0.5 || ring.left < c.left - 0.5 || ring.bottom > c.bottom + 0.5 || ring.right > c.right + 0.5) return true;
    }
    return false;
  };
  // Everything the live regions said (an announcement is replaced within moments, too fast to poll for).
  const said = [];
  new MutationObserver(() => {
    for (const r of document.querySelectorAll('[role="status"],[aria-live]')) {
      const t = (r.textContent ?? "").trim();
      if (t && said[said.length - 1] !== t) said.push(t);
    }
  }).observe(document.documentElement, { subtree: true, childList: true, characterData: true });
  window.__kk = {
    said,
    n: 0,
    keyOf,
    /** An element Tab should reach: enabled, tabbable, drawn, not inert or hidden from assistive technology. */
    expected(skipLinks) {
      const sel = 'a[href],button,input,select,textarea,summary,[tabindex],[role="button"],[role="switch"],[role="checkbox"],[role="slider"]';
      const out = [];
      let last = null;
      // A list is one Tab stop (web/src/layouts/rowTabStop.ts): the other rows' controls are tabindex -1, left out below.
      for (const e of document.querySelectorAll(sel)) {
        if (skipLinks && e.matches("a[href]")) continue;
        // Of a radio group Tab reaches only the checked one; it can be the last stop of a page.
        const checkedRadio = e instanceof HTMLInputElement && e.type === "radio" && e.checked && !e.disabled && e.isConnected;
        if (checkedRadio && e.checkVisibility({ visibilityProperty: true })) [last, this.lastEl] = [keyOf(e), e];
        if (e.disabled || e.tabIndex < 0 || e.matches(roving) || (e instanceof HTMLInputElement && e.type === "hidden")) continue;
        if (!e.checkVisibility({ visibilityProperty: true })) continue;
        if (e.closest('[inert],[aria-hidden="true"]')) continue;
        const r = e.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) continue;
        out.push(keyOf(e));
        [last, this.lastEl] = [keyOf(e), e];
      }
      // Unique keys, and the last control in the page's order (a key can occur twice: a feed in the sidebar and in the list).
      return { keys: [...new Set(out)], last };
    },
    /** What has focus: null when nothing in the page does (focus has left it). */
    focus() {
      const e = document.activeElement;
      if (!e || e === document.body || e === document.documentElement) return null;
      e.__kid ||= ++window.__kk.n;
      const s = getComputedStyle(e);
      const wrap = e.closest("label,[class*='focus-within']");
      const ring =
        ringOf(s) ||
        (s.boxShadow && s.boxShadow !== "none") ||
        ringOf(getComputedStyle(e, "::after")) ||
        ringOf(getComputedStyle(e, "::before")) ||
        (wrap && wrap !== e && (ringOf(getComputedStyle(wrap)) || getComputedStyle(wrap).boxShadow !== "none"));
      return { id: e.__kid, key: keyOf(e), ring: !!ring && !ringClipped(e, s), inRow: !!e.closest("article[data-item-id]") };
    },
  };
}

// ---- setup ----
const browser = await ENGINES[opt.browser]
  .launch({ headless: !opt.headed })
  .catch((e) => setupError(`could not start ${opt.browser} (npx playwright install ${opt.browser}): ${String(e.message).split("\n")[0]}`));

async function signIn() {
  const ctx = await newContext({});
  try {
    let saved = null;
    try {
      const c = JSON.parse(readFileSync(deviceFile, "utf8"));
      if (c && c.name === "kipple_device" && typeof c.value === "string") saved = c;
    } catch {
      /* no remembered device yet */
    }
    if (saved) await ctx.addCookies([saved]).catch(() => {});
    const page = await ctx.newPage();
    await page.goto(origin + "/");
    await page.getByLabel("Username").waitFor({ timeout: 15000 }).catch(() => setupError(`no sign-in form at ${origin} (is the UI built before the binary?)`));
    await page.getByLabel("Username").fill(opt.user);
    await page.getByLabel("Password", { exact: true }).fill(opt.password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 }).catch(() => setupError("sign-in failed"));
    const cookies = (await ctx.storageState()).cookies;
    const device = cookies.find((c) => c.name === "kipple_device");
    if (!device) setupError("signed in, but the server issued no device cookie");
    if (!saved) {
      mkdirSync(dirname(deviceFile), { recursive: true });
      writeFileSync(deviceFile, JSON.stringify(device));
    }
    return cookies;
  } finally {
    await ctx.close();
  }
}

/** Firefox keeps no emulated color scheme across Kipple's Cross-Origin-Opener-Policy; nothing here depends on a theme. */
function newContext(extra) {
  return browser.newContext({ baseURL: origin, serviceWorkers: "block", ...extra });
}

// ---- common steps ----
async function load(page, path) {
  await page.goto(path, { waitUntil: "domcontentloaded" });
  await page.locator("h1").first().waitFor({ state: "visible", timeout: 15000 });
  await page.waitForFunction(() => !document.querySelector('[aria-busy="true"]'), null, { timeout: 15000 });
  await page.evaluate(installHelpers);
  await waitForPageStill(page, 250);
}
const focusOf = (page) => page.evaluate(() => window.__kk.focus());
const shot = (page, name) => (opt.screenshots ? page.screenshot({ path: join(opt.screenshots, `${opt.browser}-${name}.png`) }).catch(() => {}) : undefined);
const press = async (page, ...keys) => {
  for (const k of keys) await page.keyboard.press(k);
};
const rowState = (page) =>
  page.evaluate(() =>
    [...document.querySelectorAll("article[data-item-id]")].map((a) => [a.dataset.itemId, a.querySelector(".kp-unread-dot")?.dataset.unread ?? "?", a.dataset.selected === "true", a.querySelector('button[aria-pressed]')?.getAttribute("aria-pressed") ?? "?"]),
  );
const until = (page, fn, arg, ms = 4000) => page.waitForFunction(fn, arg, { timeout: ms, polling: 100 }).then(() => true, () => false);
// Waits until focus is on another element than this id (an arrow key moved it); if it never moves, the check that follows says so.
const focusLeft = (page, id) => until(page, (was) => { const now = window.__kk.focus()?.id; return now != null && now !== was; }, id, 3000);
const pathOf = (page) => new URL(page.url()).pathname + new URL(page.url()).search;

// ---- the desktop checks ----
async function desktop(cookies) {
  const ctx = await newContext({ viewport: { width: 1280, height: 800 }, storageState: { cookies, origins: [] } });
  const page = await ctx.newPage();
  const errors = [];
  const refused = [];
  const failedRequests = [];
  const isUnloadRefusal = unloadRefusal(page, origin);
  onPageError(page, (e) => (isUnloadRefusal(e) ? refused.push(accessControlUrl(e).pathname) : errors.push(String(e).slice(0, 200))));
  page.on("requestfailed", (req) => {
    const why = req.failure()?.errorText ?? "failed";
    // A navigation aborts what was in flight; each engine words it its own way (as in run.mjs).
    if (/ERR_ABORTED|NS_BINDING_ABORTED|cancel+ed/i.test(why) || new URL(req.url()).origin !== origin) return;
    failedRequests.push(`${req.method()} ${new URL(req.url()).pathname}: ${why}`);
  });
  await load(page, "/l/all");
  // The layout the keys are exercised in: Editorial, whose rows carry the unread dot and a Star button.
  const layoutBtn = page.locator('button[aria-label^="List options, "]').first();
  if ((await layoutBtn.getAttribute("aria-label")) !== "List options, Editorial layout") {
    await layoutBtn.click();
    await page.getByRole("menuitemradio", { name: /^Editorial\b/ }).click();
    await page.waitForFunction(() => document.querySelector('button[aria-label^="List options, "]')?.getAttribute("aria-label") === "List options, Editorial layout", null, { timeout: 5000 });
  }
  await page.waitForSelector("main article a[href^='/i/']", { timeout: 15000 });
  const articlePath = await page.locator("main article a[href^='/i/']").first().getAttribute("href");
  // The sort button is a toggle and the run changes nothing it does not restore: start from newest first.
  if (articlePath && /order(%3A|:)oldest/.test(articlePath)) await page.getByRole("button", { name: /^Newest first$/ }).first().click();
  try {
    await k1(page, articlePath);
    await k2(page);
    await k3(page);
  } finally {
    if (refused.length) note("desktop", `${refused.length} fetch(es) WebKit refused while the page was being left (not counted): ${[...new Set(refused)].join(", ")}`);
  }
  if (errors.length) fail("desktop", `uncaught page errors: ${errors.join(" | ")}`);
  if (failedRequests.length) fail("desktop", `requests failed: ${failedRequests.slice(0, 4).join(" | ")}`);
  await ctx.close();
}

// K1: Tab order, focus visibility, no trap.
async function k1(page, articlePath) {
  const screens = [
    ["/l/all", "list", true],
    ["/l/unread", "list", true],
    ["/l/starred", "empty list", false],
    ["/search?q=the", "search", true],
    ["/search", "search, no query", false],
    ["/feeds", "feeds", false],
    ["/health", "feed health", false],
    ["/settings/appearance", "settings: appearance", false],
    ["/settings/sync", "settings: sync", false],
    ["/settings/statistics", "settings: statistics", false],
    ["/settings/filters", "settings: filters", false],
    ["/settings/account", "settings: account", false],
    ["/settings/advanced", "settings: advanced", false],
    ["/stats", "stats", false],
    ["/stats/wrapped", "wrapped", false],
    [articlePath, "article", true],
  ];
  const skipLinks = WEBKIT_NO_LINK_TAB && opt.browser === "webkit";
  for (const [path, name, list] of screens) {
    const where = `K1 ${name}`;
    await load(page, path);
    const { keys: expected, last: lastKey } = await page.evaluate((links) => window.__kk.expected(links), skipLinks);
    // A screen may put the caret in a field on load (Search): walk from the start of the page, not from there.
    const rewound = await rewind(page);
    const walked = await walk(page, where, lastKey, rewound);
    report(where, expected, walked);
    if (walked.ended && walked.ended !== "TRAP") await backwards(page, where);
    if (list) await onlyFocusMoves(page, `${where} (deep in the list)`, () => leaveList(page, `${where} (deep in the list)`, 30));
  }
}

/** Run `fn` (arrow keys through the list) and check that it opened nothing and marked nothing read: the arrows
 * move focus only: they open nothing and mark nothing read. */
async function onlyFocusMoves(page, where, fn) {
  const sent = [];
  const onRequest = (req) => {
    if (req.method() === "POST" && /\/api\/items\/(mark-read|[^/]+\/open)$/.test(new URL(req.url()).pathname)) sent.push(new URL(req.url()).pathname);
  };
  const path = new URL(page.url()).pathname;
  page.on("request", onRequest);
  try {
    await fn();
    await page.waitForLoadState("networkidle").catch(() => {});
  } finally {
    page.off("request", onRequest);
  }
  const moved = new URL(page.url()).pathname;
  check(!sent.length && moved === path, `${where} (nothing opened)`, "the arrow keys open nothing and mark nothing read", `the arrow keys sent ${sent.length} open or mark-read request(s) (${sent.slice(0, 3).join(", ")}) and the page went from ${path} to ${moved}`);
}

/** The list is one Tab stop: after moving `moves` rows down with the arrow keys (which loads more rows as it goes), a
 * few Tab presses leave the list for the control after it. A list whose rows were all Tab stops would never let go. */
async function leaveList(page, where, moves) {
  const row = page.locator('[data-testid="list-scroll"] article[data-item-id] a').first();
  if (!(await row.count())) return fail(where, "the list has no rows");
  await row.focus();
  const start = await page.evaluate(() => document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id"));
  const rows = new Set([start]);
  for (let i = 0; i < moves; i++) {
    await page.keyboard.press("ArrowDown");
    // The list scrolls the row into view and then moves focus to it, a frame or two later.
    const moved = await page
      .waitForFunction((seen) => {
        const id = document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id");
        return id && !seen.includes(id) ? id : null;
      }, [...rows], { timeout: 2000 })
      .then((h) => h.jsonValue())
      .catch(() => null);
    if (!moved) break; // the end of a short list, or the arrow did nothing
    rows.add(moved);
  }
  rows.delete(start);
  if (rows.size < Math.min(moves, 5)) return fail(where, `ArrowDown moved focus through ${rows.size} rows in ${moves} presses`);
  moves = rows.size;
  for (let i = 1; i <= 6; i++) {
    await page.keyboard.press("Tab");
    const out = await page.evaluate((links) => {
      const e = document.activeElement;
      if (!e || e === document.body) return "the end of the page";
      // Firefox keeps focus on the page's last control when Tab has nowhere further to go (see walk).
      window.__kk.expected(links);
      if (e === window.__kk.lastEl) return "the end of the page (its last control)";
      return e.closest('[data-testid="list-scroll"]') ? null : (e.getAttribute("aria-label") ?? e.textContent ?? "").trim().slice(0, 40) || e.tagName;
    }, WEBKIT_NO_LINK_TAB && opt.browser === "webkit");
    if (out) return pass(where, `after ${moves} rows down, Tab ${i}x leaves the list for ${out}`);
  }
  fail(where, `after ${moves} rows down, focus was still in the list after 6 Tab presses (keyboard trap)`);
}

/** Shift+Tab to the start of the page; the controls passed on the way. */
async function rewind(page) {
  const rewound = new Set();
  for (let i = 0; i < 80; i++) {
    const f = await focusOf(page);
    if (!f) break;
    rewound.add(f.key);
    await page.keyboard.press("Shift+Tab");
  }
  return rewound;
}

/** Tab on from where focus is, at most 400 times: what it reached, which stops showed no ring, and how it ended. */
async function walk(page, where, lastKey, seenBefore) {
  const cap = 400;
  const seen = new Set(seenBefore); // reached by Tab (backwards) on the way to the start
  const ids = new Set();
  const noRing = new Set();
  let ended = null;
  let same = 0;
  let prev = null;
  let steps = 0;
  for (; steps < cap; steps++) {
    await page.keyboard.press("Tab");
    const f = await focusOf(page);
    if (!f) {
      ended = "left the page";
      break;
    }
    if (ids.has(f.id) && f.id !== prev) {
      ended = "wrapped to the start";
      break;
    }
    same = f.id === prev ? same + 1 : 0;
    if (same >= 2 && f.key === lastKey) {
      // Firefox keeps focus on the page's last control when Tab has nowhere further to go (no browser toolbar to
      // move to in a headless run); that is the end of the page, not a trap.
      ended = "last control";
      break;
    }
    if (same >= 2) {
      ended = "TRAP";
      fail(where, `focus stuck on ${f.key} after ${steps} Tab presses`);
      break;
    }
    prev = f.id;
    ids.add(f.id);
    seen.add(f.key);
    if (!f.ring) noRing.add(f.key);
  }
  if (ended === null) fail(where, `Tab did not reach the end of the page in ${cap} presses (keyboard trap or an endless page)`);
  return { seen, noRing, ended };
}

function report(where, expected, { seen, noRing, ended }) {
  const missed = expected.filter((k) => !seen.has(k)).map((k) => k.replace(/\|+/g, "|"));
  if (missed.length) fail(where, `${missed.length} control(s) Tab never reached: ${missed.slice(0, 4).join("; ")}`);
  if (noRing.size) fail(where, `${noRing.size} focused control(s) show no focus indicator: ${[...noRing].slice(0, 4).map((k) => k.replace(/\|+/g, "|")).join("; ")}`);
  if (!missed.length && !noRing.size && ended) pass(where, `${seen.size} stops, ${ended}, focus always visible`);
}

/** The way back: Shift+Tab from the first stop moves on too (no backward trap). */
async function backwards(page, where) {
  const back = new Set();
  for (let i = 0; i < 4; i++) {
    await page.keyboard.press("Shift+Tab");
    const f = await focusOf(page);
    back.add(f ? f.id : 0);
  }
  check(back.size >= 3, `${where} (Shift+Tab)`, "moves backwards through the page", `Shift+Tab visited only ${back.size} distinct place(s) in 4 presses`);
}

// K2: menus and dialogs open from the keyboard and Escape closes them and returns focus.
async function k2(page) {
  const cases = [
    ["/l/all", "Layout menu", (p) => p.locator('button[aria-label^="List options, "]').first(), false],
    ["/l/all", "Reading appearance", (p) => p.getByRole("button", { name: "Reading appearance" }).first(), true],
    ["/l/all", "List actions menu", (p) => p.getByRole("button", { name: "List actions" }).first(), false],
    ["/l/all", "Row More actions menu", (p) => p.getByRole("button", { name: "More actions" }).first(), false],
    ["/feeds", "Add feed dialog", (p) => p.getByRole("button", { name: "Add feed" }).first(), true], // uat-labels: ignore (the Add feed button, not a filter chip)
    ["/feeds", "Feed actions menu", (p) => p.getByRole("button", { name: "Feed actions" }).first(), false],
    ["/settings/account", "Generate new app password dialog", (p) => p.getByRole("button", { name: "Generate new app password" }).first(), true],
    ["/settings/account", "Export backup dialog", (p) => p.getByRole("button", { name: "Export backup" }).first(), true],
  ];
  let at = null;
  for (const [path, name, find, modal] of cases) {
    const where = `K2 ${name}`;
    if (at !== path) {
      await load(page, path);
      at = path;
    }
    const trigger = find(page);
    if (!(await trigger.count())) {
      fail(where, "trigger not found on the screen");
      continue;
    }
    await trigger.focus();
    await page.evaluate(() => (window.__trigger = document.activeElement));
    await page.keyboard.press("Enter");
    const open = await until(page, () => !!document.querySelector('[role="dialog"],[role="menu"]'));
    if (!open) {
      fail(where, "Enter on the trigger did not open it");
      continue;
    }
    const inside = () => page.evaluate(() => !!document.activeElement?.closest('[role="dialog"],[role="menu"]'));
    // Focus moves in a moment after the content mounts; a menu or dialog that never takes it is the finding.
    let trapped = await until(page, () => !!document.activeElement?.closest('[role="dialog"],[role="menu"]'), null, 1500);
    if (modal) {
      // A modal dialog holds focus: Tab goes round inside it and never to the page behind.
      for (let i = 0; i < 12 && trapped; i++) {
        await page.keyboard.press("Tab");
        trapped = await inside();
      }
    }
    await page.keyboard.press("Escape");
    const closed = await until(page, () => !document.querySelector('[role="dialog"],[role="menu"]'));
    // Radix hands focus back a tick after the content unmounts.
    const back = await until(page, () => document.activeElement === window.__trigger, null, 1500);
    const problems = [];
    if (!trapped) problems.push(modal ? "focus left the dialog while it was open" : "focus was not moved into the menu");
    if (!closed) problems.push("Escape did not close it");
    if (!back) problems.push(`focus did not return to the trigger (it is on ${(await focusOf(page))?.key ?? "the page"})`);
    if (problems.length) {
      fail(where, problems.join("; "));
      await shot(page, `k2-${name.replace(/\W+/g, "-")}`);
      if (!closed) await page.reload();
    } else pass(where, "opens, keeps focus, Escape closes and focus returns");
  }
}

// K3: there are no single-key shortcuts. Letters and symbols do nothing on a list (nothing is selected, marked,
// starred, opened or navigated, and no dialog appears), and typing in the search box types. Arrow keys on a row link
// still move focus between rows, and a separator still takes the arrow keys.
async function k3(page) {
  const where = (k) => `K3 ${k}`;
  await load(page, "/l/all");
  const start = await rowState(page);
  if (start.length < 4) return fail("K3", `only ${start.length} rows loaded, need 4 (let the seeded feeds fetch)`);
  const path0 = pathOf(page);
  await page.evaluate(() => document.activeElement?.blur?.());
  await press(page, ..."jkmsxofvucrz/?[]{}".split(""), "Shift+A", "Shift+G", "g", "g", "g", "a");
  // A negative (nothing happens): the rows, the address and any dialog must hold still for longer than the 700 ms mark delay plus a round trip.
  const dialogUp = () => page.evaluate(() => !!document.querySelector('[role="dialog"]'));
  const [after, , dialog] = await waitForStillFor(page, 1000, async () => [await rowState(page), pathOf(page), await dialogUp()], { label: "the rows, the address and dialogs to stop changing after the letter keys" });
  check(JSON.stringify(after) === JSON.stringify(start) && pathOf(page) === path0 && !dialog, where("letters"), "letter and symbol keys change nothing on a list", `rows changed=${JSON.stringify(after) !== JSON.stringify(start)} path ${path0} -> ${pathOf(page)} dialog=${dialog}`);

  // Arrow keys on a row link still move focus (and only focus) from row to row.
  const first = page.locator("main article a[href^='/i/']").first();
  await first.focus();
  const f0 = await focusOf(page);
  await press(page, "ArrowDown");
  await focusLeft(page, f0?.id);
  // The row must not open: the address holds still for longer than the 700 ms mark delay plus a round trip.
  const [f1] = await waitForStillFor(page, 1000, async () => [await focusOf(page), pathOf(page)], { label: "focus and the address to stop changing after ArrowDown" });
  check(!!f0 && !!f1 && f1.id !== f0.id && pathOf(page) === path0, where("arrows"), "ArrowDown moves focus to the next row without opening it", `focus ${f0?.key} -> ${f1?.key}, path ${pathOf(page)}`);

  // From a row's other controls too (Safari's Tab skips links, so the star may be all a keyboard user reaches).
  await load(page, "/l/all");
  const star = page.locator("main article").first().locator("button").first();
  await star.focus();
  const s0 = await focusOf(page);
  await press(page, "ArrowDown");
  await focusLeft(page, s0?.id);
  const [s1] = await waitForStillFor(page, 1000, async () => [await focusOf(page), await page.evaluate(() => !!document.querySelector('[role="menu"]'))], { label: "focus and any menu to stop changing after ArrowDown" });
  check(!!s0 && !!s1 && s1.id !== s0.id && !(await page.evaluate(() => !!document.querySelector('[role="menu"]'))), where("arrows from star"), "ArrowDown from a row's star button reaches the next row", `focus ${s0?.key} -> ${s1?.key}`);

  // Typing is not a shortcut: the search box takes the letters.
  await load(page, "/search");
  await page.getByRole("searchbox").first().focus();
  await page.keyboard.type("jkms/?");
  const typed = await page.evaluate(() => document.activeElement?.value ?? "");
  check(typed === "jkms/?", where("typing"), "letters typed in the search box stay there", `the box holds "${typed}"`);
  await load(page, "/l/all");

  // The sidebar's resize handle is a focusable separator: arrow keys must move it.
  const sep = page.getByRole("separator", { name: "Resize sidebar" });
  if (await sep.count()) {
    await sep.focus();
    const w0 = await page.evaluate(() => document.querySelector("nav,aside")?.getBoundingClientRect().width ?? 0);
    await press(page, "ArrowRight", "ArrowRight");
    const w1 = await page.evaluate(() => document.querySelector("nav,aside")?.getBoundingClientRect().width ?? 0);
    await press(page, "ArrowLeft", "ArrowLeft");
    check(w1 > w0, "K3 sidebar resize", "ArrowRight widens the sidebar", `width stayed ${w0}`);
  }
}

// K4: swipe and long-press actions have a button or menu on a touch device.
async function phone(cookies) {
  const ctx = await newContext({
    viewport: { width: 390, height: 844 },
    // Firefox has no mobile emulation; it still gets touch and the width.
    isMobile: opt.browser !== "firefox",
    hasTouch: true,
    storageState: { cookies, origins: [] },
  });
  const page = await ctx.newPage();
  await load(page, "/l/all");
  const where = (k) => `K4 ${k}`;
  const row = page.locator("main article[data-item-id]").first();
  const star = row.getByRole("button", { name: /^Star: / });
  check((await star.count()) >= 1 && (await star.first().evaluate((e) => e.checkVisibility() && e.getBoundingClientRect().width >= 24)), where("swipe left: star"), "a Star button on the row", "no visible Star button (24 px or more) on a row");
  const more = row.getByRole("button", { name: "More actions" });
  if (await more.count()) {
    await more.first().click();
    const items = await page.evaluate(() => [...document.querySelectorAll('[role="menu"] [role^="menuitem"]')].map((e) => e.textContent.trim()));
    check(items.some((t) => /^Mark as (read|unread)/.test(t)) && items.some((t) => /^(Star|Unstar)/.test(t)), where("swipe right / long press: read, star, more"), `the row's More actions menu has ${items.slice(0, 3).join(", ")}...`, `the menu offers: ${items.join(", ") || "nothing"}`);
    await page.keyboard.press("Escape");
  } else fail(where("long press: row menu"), "no More actions button on a row");
  // The header has no Refresh button on a phone: the pull on the list and the actions menu do it.
  await page.getByRole("button", { name: "List actions" }).first().click();
  check(await page.getByRole("menuitem", { name: "Refresh all feeds" }).first().isVisible().catch(() => false), where("pull to refresh"), "a Refresh all feeds item in the actions menu", "no Refresh all feeds item in the actions menu");
  await page.keyboard.press("Escape");
  await row.locator("a[href^='/i/']").first().click();
  await page.waitForURL((u) => u.pathname.startsWith("/i/"), { timeout: 10000 });
  await page.getByRole("button", { name: "Back", exact: true }).first().waitFor({ timeout: 10000 }).catch(() => {});
  await page.getByRole("button", { name: "Next article" }).first().waitFor({ timeout: 10000 }).catch(() => {});
  const backBtn = page.getByRole("button", { name: "Back", exact: true });
  check(await backBtn.first().isVisible().catch(() => false), where("swipe back"), "a Back button", "no visible Back button on the article");
  const articleButtons = await page.evaluate(() => [...document.querySelectorAll("button")].filter((b) => b.checkVisibility()).map((b) => b.getAttribute("aria-label") || b.textContent.trim()));
  check(["Star", "Previous article", "Next article"].every((n) => articleButtons.some((t) => t.startsWith(n))), where("article actions"), "Star, Previous and Next article buttons", `the article has: ${articleButtons.join(", ")}`);
  // Reordering feeds by dragging: Move (to a folder) reaches the same result with no drag (Select mode).
  await load(page, "/feeds");
  await startSelect(page);
  const moveTo = await page.getByRole("region", { name: "Selected feeds" }).getByRole("button", { name: "Move", exact: true }).count().catch(() => 0);
  check(moveTo > 0, where("drag to reorder"), "Select mode offers Move (the grips also take arrow keys)", "no Move button in Select mode");
  await ctx.close();
}

async function main() {
  let exitCode = 0;
  try {
    const cookies = await signIn();
    console.log(`Kipple at ${origin}, ${opt.browser} ${browser.version()}`);
    await desktop(cookies);
    await phone(cookies);
    if (findings.length) {
      console.error(`\n${findings.length} finding(s):\n${findings.map((f) => `  ${f}`).join("\n")}`);
      exitCode = 1;
    } else console.log("\nclean");
  } catch (e) {
    console.error(`run aborted: ${String(e?.stack ?? e).split("\n").slice(0, 3).join("\n")}`);
    exitCode = 2;
  } finally {
    if (notes.length) console.log(`${notes.length} note(s), not counted as findings`);
    await browser.close().catch(() => {});
  }
  process.exitCode = exitCode;
}
await main();
