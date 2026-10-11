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
//   K3  The shortcuts of web/src/lib/keys.ts (the `?` overlay): j k Enter x m s { } Shift+A z g g Shift+G [ ] c o v f u
//       Esc r / ? and the g chords each do what the overlay says; typing in a field does not trigger them. The ones
//       that change state are undone with z or pressed twice, so the seeded instance ends as it began.
// At 390x844 as a touch device:
//   K4  Every swipe or long-press action has an alternative that needs no gesture: a row's Star button and its
//       More actions menu (mark read or unread, star), the article's Back to list button, and the Refresh all feeds
//       button for pull to refresh; and the feed list's drag to reorder works from the keyboard (Move to folder).
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
import { accessControlUrl, unloadRefusal } from "./common.mjs";

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
   * ring drawn by an absolutely placed ::after (a row link's) its containing block's, grown by outline offset and width.
   * Only boxes inside the list's scroller count: the scroller clipping a row scrolled half out of view is not a fault.
   */
  const ringClipped = (e, s) => {
    const scroller = e.closest('[data-testid="list-scroll"]');
    if (!scroller || !e.closest("[data-item-id]")) return false;
    const after = getComputedStyle(e, "::after");
    const own = ringOf(s);
    const st = own ? s : after;
    if (!own && !(ringOf(after) && after.position === "absolute")) return false;
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
      // A rounded clipping box cuts the corners of a ring that is square (or rounder than the box) even when the
      // rectangles nest: the ring's corner arc must stay inside the box's corner arc.
      const corners = [
        ["TopLeft", 1, 1, ring.left, ring.top, c.left, c.top],
        ["TopRight", -1, 1, ring.right, ring.top, c.right, c.top],
        ["BottomLeft", 1, -1, ring.left, ring.bottom, c.left, c.bottom],
        ["BottomRight", -1, -1, ring.right, ring.bottom, c.right, c.bottom],
      ];
      for (const [name, sx, sy, rx, ry, cx, cy] of corners) {
        const R = parseFloat(cs[`border${name}Radius`]) || 0;
        if (R <= 0) continue;
        const rr = parseFloat(st[`border${name}Radius`]) || 0;
        // The ring's outermost corner point: on its arc 45 degrees out, or the square corner itself.
        const k = rr * (1 - Math.SQRT1_2);
        const px = rx + sx * k;
        const py = ry + sy * k;
        const ox = cx + sx * R;
        const oy = cy + sy * R;
        const inCorner = (px - ox) * sx < 0 && (py - oy) * sy < 0;
        if (inCorner && Math.hypot(px - ox, py - oy) > R + 0.5) return true;
      }
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
    await page.getByLabel("Password").fill(opt.password);
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
  await page.waitForTimeout(500);
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
const selectedId = async (page) => (await rowState(page)).find((r) => r[2])?.[0] ?? null;
const until = (page, fn, arg, ms = 4000) => page.waitForFunction(fn, arg, { timeout: ms, polling: 100 }).then(() => true, () => false);
const pathOf = (page) => new URL(page.url()).pathname + new URL(page.url()).search;

/** Cards: the focus ring of a row follows the card's rounded corners (a square ring is cut off by them, which a
 * comparison of rectangles does not see). Leaves the list in the Editorial layout, as the run found it. */
async function cardsRing(page) {
  const where = "K1 Cards focus ring";
  await load(page, "/l/all");
  const pick = async (name) => {
    await page.locator('button[aria-label^="List options, "]').first().click();
    await page.getByRole("menuitemradio", { name: new RegExp(`^${name}\\b`) }).click();
    await page.waitForTimeout(400);
  };
  await pick("Cards");
  try {
    const row = page.locator('[data-testid="list-scroll"] article[data-item-id] a').first();
    await row.waitFor({ timeout: 15000 });
    await row.focus();
    const f = await focusOf(page);
    const corner = await page.evaluate(() => {
      const a = document.activeElement;
      const card = a?.closest("article[data-item-id]");
      return { ring: parseFloat(getComputedStyle(a, "::after").borderTopLeftRadius), card: card ? parseFloat(getComputedStyle(card).borderTopLeftRadius) : 0 };
    });
    check(!!f?.ring && corner.card > 0 && corner.ring > 0 && corner.ring <= corner.card, where, "the ring is drawn, and rounded to sit inside the card's corners", `ring shown=${f?.ring}, ring radius ${corner.ring}px, card radius ${corner.card}px`);
  } finally {
    await pick("Editorial");
  }
}

/** An article open beside the list: the arrow cursor on another row does not take the keys pressed while reading. */
async function splitCursor(page) {
  const where = "K3 open article and cursor";
  await load(page, "/l/all");
  await press(page, "j"); // selects and opens the first row beside the list
  await page.locator('[data-testid="article-body"]').first().waitFor({ timeout: 15000 });
  const open = await page.evaluate(() => document.querySelector('article[aria-current="true"]')?.dataset.itemId ?? null);
  await press(page, "ArrowDown");
  const reached = await until(page, (was) => {
    const id = document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id");
    return !!id && id !== was;
  }, open);
  const cursor = await selectedId(page);
  const marks = await page.evaluate(([o, c]) => {
    const row = (id) => document.querySelector(`article[data-item-id="${id}"]`);
    return { openKept: row(o)?.getAttribute("aria-current") === "true", cursorNotOpen: !row(c)?.hasAttribute("aria-current"), bar: parseFloat(getComputedStyle(row(o), "::before").width) };
  }, [open, cursor]);
  check(!!open && reached && cursor !== open && marks.openKept && marks.cursorNotOpen && marks.bar > 0, where, "the open row keeps its own marker while the cursor is on another row", `open ${open}, cursor ${cursor}, ${JSON.stringify(marks)}`);
  const starred = (id) => page.evaluate((i) => document.querySelector(`article[data-item-id="${i}"] button[aria-pressed]`)?.getAttribute("aria-pressed"), id);
  const [openBefore, cursorBefore] = [await starred(open), await starred(cursor)];
  // Focus leaves the list for the reader: s is about the article being read.
  await page.evaluate(() => document.activeElement?.blur());
  await press(page, "s");
  const onOpen = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") !== was, [open, openBefore]);
  const cursorSame = (await starred(cursor)) === cursorBefore;
  await press(page, "s");
  await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") === was, [open, openBefore]);
  check(onOpen && cursorSame, where + " (s)", "with focus in the reader, s stars the open article, not the row the arrow keys reached", `open article starred=${onOpen}, cursor row untouched=${cursorSame}`);

  // A click on another row's own star moves the cursor (and focus) there: s then acts on that row, and once focus is
  // out of the list again the keys are the open article's. Nothing is left over from the pointer.
  const other = (await rowState(page)).map((r) => r[0]).find((id) => id !== open && id !== cursor);
  const otherBefore = await starred(other);
  await page.locator(`article[data-item-id="${other}"] button[aria-pressed]`).click();
  const clicked = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") !== was, [other, otherBefore]);
  const movedTo = await until(page, (id) => document.querySelector(`article[data-item-id="${id}"]`)?.getAttribute("data-selected") === "true", other);
  await press(page, "s");
  const backAgain = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") === was, [other, otherBefore]);
  check(!!other && clicked && movedTo && backAgain, where + " (click)", "a click on another row's star moves the cursor there, and s then stars that row", `clicked=${clicked} cursor moved=${movedTo} s acted on it=${backAgain}`);
  const openNow = await starred(open);
  await page.evaluate(() => document.activeElement?.blur());
  await press(page, "s");
  const onOpenAgain = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") !== was, [open, openNow]);
  const otherSame = (await starred(other)) === otherBefore;
  await press(page, "s");
  check(onOpenAgain && otherSame, where + " (click, then reader)", "after a click in the list, with focus out of it the keys are the open article's", `open article starred=${onOpenAgain}, clicked row untouched=${otherSame}`);

  // A right or middle click on a row's star starts nothing and leaves nothing behind: with focus out of the list the
  // keys are still the open article's.
  const star2 = page.locator(`article[data-item-id="${other}"] button[aria-pressed]`);
  const otherPressed = await starred(other);
  await star2.click({ button: "right" }).catch(() => {});
  await page.keyboard.press("Escape");
  await star2.click({ button: "middle" }).catch(() => {});
  await page.mouse.click(2, 2, { button: "left" }).catch(() => {}); // the browser's own middle-click scroll mode swallows the next click or key
  await page.evaluate(() => document.activeElement?.blur());
  const openThen = await starred(open);
  await press(page, "s");
  const openFlipped = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") !== was, [open, openThen]);
  const otherUntouched = (await starred(other)) === otherPressed;
  await press(page, "s");
  check(openFlipped && otherUntouched, where + " (right and middle click)", "after a right or middle click, nothing is left over: s stars the open article only", `open article starred=${openFlipped}, clicked row untouched=${otherUntouched}`);
}

// ---- the desktop checks ----
async function desktop(cookies) {
  const ctx = await newContext({ viewport: { width: 1280, height: 800 }, storageState: { cookies, origins: [] } });
  const page = await ctx.newPage();
  const errors = [];
  const refused = [];
  const failedRequests = [];
  const isUnloadRefusal = unloadRefusal(page, origin);
  page.on("pageerror", (e) => (isUnloadRefusal(e) ? refused.push(accessControlUrl(e).pathname) : errors.push(String(e).slice(0, 200))));
  page.on("requestfailed", (req) => {
    const why = req.failure()?.errorText ?? "failed";
    // A navigation aborts what was in flight; each engine words it its own way (as in run.mjs).
    if (/ERR_ABORTED|NS_BINDING_ABORTED|cancel+ed/i.test(why) || new URL(req.url()).origin !== origin) return;
    failedRequests.push(`${req.method()} ${new URL(req.url()).pathname}: ${why}`);
  });
  await load(page, "/l/all");
  // The layout the shortcuts are exercised in: Editorial, whose rows carry the unread dot and a Star button.
  const layoutBtn = page.locator('button[aria-label^="List options, "]').first();
  if ((await layoutBtn.getAttribute("aria-label")) !== "List options, Editorial layout") {
    await layoutBtn.click();
    await page.getByRole("menuitemradio", { name: /^Editorial\b/ }).click();
    await page.waitForTimeout(400);
  }
  await page.waitForSelector("main article a[href^='/i/']", { timeout: 15000 });
  const articlePath = await page.locator("main article a[href^='/i/']").first().getAttribute("href");
  // The sort button is a toggle and the run changes nothing it does not restore: start from newest first.
  if (articlePath && /order(%3A|:)oldest/.test(articlePath)) await page.getByRole("button", { name: /^Newest first$/ }).first().click();
  try {
    await k1(page, articlePath);
    await k2(page);
    await k3(page);
    await cardsRing(page);
    await splitCursor(page);
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
 * move focus only, unlike j and k, which open the article they reach beside the reader pane. */
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
    ["/settings/account", "Generate API password dialog", (p) => p.getByRole("button", { name: "Generate API password" }).first(), true],
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
    await page.waitForTimeout(150);
    let trapped = await inside();
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
  // The shortcut overlay, opened by a key rather than a control: Escape returns focus to what had it.
  await load(page, "/l/all");
  await press(page, "j");
  await page.waitForTimeout(300);
  const before = await focusOf(page);
  await press(page, "?");
  const shown = await until(page, () => !!document.querySelector('[role="dialog"]'));
  await press(page, "Escape");
  const gone = await until(page, () => !document.querySelector('[role="dialog"]'));
  await page.waitForTimeout(300);
  const after2 = await focusOf(page);
  check(shown && gone && !!before && after2?.id === before.id, "K2 shortcut overlay", "? opens it, Escape closes it and focus returns to the row", `shown=${shown} closed=${gone} focus before=${before?.key} after=${after2?.key}`);
}

// K3: the documented shortcuts.
async function k3(page) {
  const where = (k) => `K3 ${k}`;
  await load(page, "/l/all");
  const start = await rowState(page);
  if (start.length < 4) return fail("K3", `only ${start.length} rows loaded, need 4 (let the seeded feeds fetch)`);

  // j and k
  // The selection follows the key a render later: wait for it to be somewhere new.
  const moved = async (key, from) => {
    await press(page, key);
    await until(page, (was) => document.querySelector('article[data-selected="true"]')?.dataset.itemId !== was, from, 3000);
    return selectedId(page);
  };
  const a = await moved("j", null);
  const b = await moved("j", a);
  const c = await moved("k", b);
  check(a === start[0][0] && b === start[1][0] && c === a, where("j / k"), "j selects the first row, then the next; k goes back", `selected ${a}, ${b}, ${c}; expected rows ${start[0][0]}, ${start[1][0]}, ${start[0][0]}`);
  const f1 = await focusOf(page);
  check(!!f1 && f1.key.includes("/i/"), where("j (focus)"), "keyboard focus follows the selected row", `focus is on ${f1?.key}`);

  // x, m, s on the selected row
  const selA = await selectedId(page);
  await press(page, "x");
  const ticked = await until(page, () => window.__kk.said.some((t) => /1 selected/.test(t)));
  const selB = await selectedId(page);
  await press(page, "x");
  const cleared = await until(page, () => window.__kk.said.some((t) => /Selection cleared/.test(t)));
  await page.waitForTimeout(300);
  check(ticked && cleared, where("x"), 'ticks and clears the row ("1 selected", "Selection cleared")', `ticked=${ticked} cleared=${cleared}; selected ${selA} then ${selB}; the live regions said ${JSON.stringify(await page.evaluate(() => window.__kk.said.slice(-4)))}`);
  const s0 = (await rowState(page)).find((r) => r[2]);
  await press(page, "m");
  const flipped = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] .kp-unread-dot`)?.dataset.unread !== was, [s0[0], s0[1]]);
  await press(page, "m");
  const back = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] .kp-unread-dot`)?.dataset.unread === was, [s0[0], s0[1]]);
  check(flipped && back, where("m"), "toggles read and unread", `flipped=${flipped} back=${back}`);
  await press(page, "s");
  const starred = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") !== was, [s0[0], s0[3]]);
  await press(page, "s");
  const unstarred = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") === was, [s0[0], s0[3]]);
  check(starred && unstarred, where("s"), "stars and unstars", `starred=${starred} unstarred=${unstarred}`);

  // The arrow keys move the one cursor: the row they reach is the row s acts on (nothing else is "selected").
  const fromRow = await selectedId(page);
  await press(page, "ArrowDown");
  const reached = await until(page, (was) => {
    const id = document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id");
    return id && id !== was && document.querySelector(`article[data-item-id="${id}"]`)?.getAttribute("data-selected") === "true";
  }, fromRow);
  const arrowRow = await selectedId(page);
  const before = (await rowState(page)).find((r) => r[0] === arrowRow);
  await press(page, "s");
  const arrowStarred = await until(page, ([id, was]) => document.querySelector(`article[data-item-id="${id}"] button[aria-pressed]`)?.getAttribute("aria-pressed") !== was, [arrowRow, before?.[3]]);
  await press(page, "s");
  check(reached && arrowStarred, where("arrow then s"), "the row the arrows reached is selected, and s stars that row", `cursor moved=${reached} from ${fromRow} to ${arrowRow}; starred=${arrowStarred}`);

  // Bulk marking, each undone with z: { } Shift+A.
  const bulk = async (label, keys, expect) => {
    const before = await rowState(page);
    await press(page, ...keys);
    const changed = await until(page, ([ids, want]) => ids.every((id) => document.querySelector(`article[data-item-id="${id}"] .kp-unread-dot`)?.dataset.unread === want), expect(before));
    await press(page, "z");
    const restored = await until(page, (snap) => snap.every(([id, u]) => (document.querySelector(`article[data-item-id="${id}"] .kp-unread-dot`)?.dataset.unread ?? u) === u), before.map((r) => [r[0], r[1]]), 6000);
    const after = restored ? [] : (await rowState(page)).filter((r) => before.some((b) => b[0] === r[0] && b[1] !== r[1]));
    check(changed && restored, where(label), "marks read, and z restores it", `marked=${changed} undone=${restored} rows=${before.length} not restored=${after.length} (${after.slice(0, 3).map((r) => r[0] + ":" + r[1]).join(", ")}) toasts=${JSON.stringify(await page.locator("[role=status]").allInnerTexts())}`);
    // A mark that changed nothing on screen passes at once while its z still waits for the server, and the app
    // ignores a z pressed during that wait: let it finish before the next step presses z.
    await until(page, () => ![...document.querySelectorAll("[role=status]")].some((e) => /Undoing/.test(e.textContent ?? "")), undefined, 12000);
  };
  await load(page, "/l/all");
  await press(page, "j", "j");
  await page.waitForTimeout(300);
  await bulk("}", ["}"], (s) => [s.slice(2).filter((r) => r[1] === "true").map((r) => r[0]), "false"]);
  await bulk("{", ["{"], (s) => [s.slice(0, 1).filter((r) => r[1] === "true").map((r) => r[0]), "false"]);
  await bulk("Shift+A", ["Shift+A"], (s) => [s.filter((r) => r[1] === "true").map((r) => r[0]), "false"]);

  // g g and Shift+G scroll the list
  await load(page, "/l/all");
  await press(page, "Shift+G");
  const lower = await until(page, () => [...document.querySelectorAll("*")].some((e) => e.scrollTop > 400 && /auto|scroll/.test(getComputedStyle(e).overflowY)));
  await press(page, "g", "g");
  const top = await until(page, () => [...document.querySelectorAll("*")].every((e) => e.scrollTop === 0 || !/auto|scroll/.test(getComputedStyle(e).overflowY)));
  check(lower && top, where("Shift+G / g g"), "jump to the bottom and the top", `bottom=${lower} top=${top}`);

  // [ and ] change feed
  await load(page, "/l/unread?feed=2");
  await press(page, "]");
  const next = await until(page, () => !location.search.includes("feed=2") && location.search.includes("feed="));
  const nextPath = pathOf(page);
  await page.waitForTimeout(500); // the next feed's list has taken over the keys
  await press(page, "[");
  const prev = await until(page, () => location.search.includes("feed=2"));
  check(next && prev, where("[ / ]"), `next feed (${nextPath}) and back`, `next=${next} (${nextPath}) back=${prev}`);

  // c toggles Compact
  await load(page, "/l/all");
  await press(page, "c");
  const compactOn = await until(page, () => /List options, Compact layout/.test(document.querySelector('button[aria-label^="List options, "]')?.getAttribute("aria-label") ?? ""));
  await press(page, "c");
  const compactOff = await until(page, () => /List options, Editorial layout/.test(document.querySelector('button[aria-label^="List options, "]')?.getAttribute("aria-label") ?? ""));
  check(compactOn && compactOff, where("c"), "switches to Compact and back", `compact=${compactOn} back=${compactOff}`);

  // o and v open the original in a new tab
  await load(page, "/l/all");
  await press(page, "j");
  for (const key of ["o", "v"]) {
    const popup = page.context().waitForEvent("page", { timeout: 4000 }).then((p) => p, () => null);
    await press(page, key);
    const p2 = await popup;
    check(!!p2, where(key), "opens the original in a new tab", "no new tab opened");
    await p2?.close().catch(() => {});
    await page.bringToFront();
  }

  // Enter opens the article; f toggles full text; u and Escape go back and focus returns to the list
  await load(page, "/l/all");
  await press(page, "j", "Enter");
  const opened = await until(page, () => location.pathname.startsWith("/i/"));
  await page.locator("h1").first().waitFor({ timeout: 8000 }).catch(() => {});
  check(opened, where("Enter"), "opens the selected article", `still at ${pathOf(page)}`);
  const ft = page.getByRole("button", { name: "Full text" }).first();
  await ft.waitFor({ timeout: 8000 }).catch(() => {});
  if (await ft.count()) {
    const was = await ft.getAttribute("aria-pressed");
    await page.locator("article h1, h1").first().focus().catch(() => {});
    await page.evaluate(() => document.activeElement?.blur?.());
    await press(page, "f");
    const toggled = await until(page, (w) => document.querySelector('button[aria-pressed]')?.isConnected && [...document.querySelectorAll('button[aria-pressed]')].some((b) => /Full text/i.test(b.getAttribute("aria-label") ?? b.textContent) && b.getAttribute("aria-pressed") !== w), was, 5000);
    check(toggled, where("f"), "toggles full text", `Full text stayed aria-pressed=${was}`);
    if (toggled) await press(page, "f");
  } else fail(where("f"), "no Full text button on the open article");
  // Beside the list (a wide screen) the article stays open and focus returns to the list's row.
  for (const key of ["u", "Escape"]) {
    if (!pathOf(page).startsWith("/i/")) {
      await load(page, "/l/all");
      await press(page, "j", "Enter");
      await until(page, () => location.pathname.startsWith("/i/"));
    }
    await page.locator("article[aria-labelledby='article-title'] h1, #article-title").first().focus().catch(() => {});
    await page.evaluate(() => document.activeElement?.blur?.());
    await press(page, key);
    const inList = await until(page, () => !!document.activeElement?.closest("article[data-item-id]"), null, 3000);
    const f = await focusOf(page);
    check(inList, where(`${key} (back to the list)`), "focus returns to the list's row", `focus is on ${f ? f.key : "nothing"} at ${pathOf(page)}`);
  }

  // r refreshes: a refresh request leaves
  await load(page, "/l/all");
  const refresh = page.waitForRequest((r) => r.method() === "POST" && /refresh/i.test(r.url()), { timeout: 4000 }).then(() => true, () => false);
  await press(page, "r");
  check(await refresh, where("r"), "asks the server to refresh", "no refresh request after r");

  // The g chords and / and ?
  for (const [keys, want] of [
    [["g", "i"], "/l/unread"],
    [["g", "a"], "/l/all"],
    [["g", "s"], "/l/starred"],
    [["g", "f"], "/feeds"],
    [["g", ","], "/settings"],
  ]) {
    await load(page, want === "/l/all" ? "/feeds" : "/l/all");
    await press(page, ...keys);
    const got = await until(page, (w) => location.pathname === w || location.pathname.startsWith(w + "/"), want);
    check(got, where(keys.join(" ")), `goes to ${want}`, `at ${pathOf(page)}`);
  }
  await load(page, "/l/all");
  await press(page, "/");
  const search = await until(page, () => location.pathname === "/search" && document.activeElement?.matches('input,[role="searchbox"],[role="combobox"]'));
  check(search, where("/"), "opens Search with the caret in the box", `at ${pathOf(page)}, focus ${(await focusOf(page))?.key}`);
  // Typing is not a shortcut
  await page.keyboard.type("jkms");
  const typed = await page.evaluate(() => document.activeElement?.value ?? "");
  check(typed.includes("jkms"), where("typing"), "letters typed in the search box stay there", `the box holds "${typed}"`);
  await load(page, "/l/all");
  await press(page, "?");
  check(await until(page, () => !!document.querySelector('[role="dialog"]')), where("?"), "opens the shortcut overlay", "no dialog after ?");
  await press(page, "Escape");

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
  const star = row.getByRole("button", { name: "Star", exact: true });
  check((await star.count()) >= 1 && (await star.first().evaluate((e) => e.checkVisibility() && e.getBoundingClientRect().width >= 24)), where("swipe left: star"), "a Star button on the row", "no visible Star button (24 px or more) on a row");
  const more = row.getByRole("button", { name: "More actions" });
  if (await more.count()) {
    await more.first().click();
    const items = await page.evaluate(() => [...document.querySelectorAll('[role="menu"] [role^="menuitem"]')].map((e) => e.textContent.trim()));
    check(items.some((t) => /^Mark as (read|unread)/.test(t)) && items.some((t) => /^(Star|Unstar)/.test(t)), where("swipe right / long press: read, star, more"), `the row's More actions menu has ${items.slice(0, 3).join(", ")}...`, `the menu offers: ${items.join(", ") || "nothing"}`);
    await page.keyboard.press("Escape");
  } else fail(where("long press: row menu"), "no More actions button on a row");
  check(await page.getByRole("button", { name: "Refresh all feeds" }).first().isVisible().catch(() => false), where("pull to refresh"), "a Refresh all feeds button", "no visible Refresh all feeds button");
  await row.locator("a[href^='/i/']").first().click();
  await page.waitForURL((u) => u.pathname.startsWith("/i/"), { timeout: 10000 });
  await page.getByRole("button", { name: "Back to list" }).first().waitFor({ timeout: 10000 }).catch(() => {});
  await page.getByRole("button", { name: "Next article" }).first().waitFor({ timeout: 10000 }).catch(() => {});
  const backBtn = page.getByRole("button", { name: "Back to list" });
  check(await backBtn.first().isVisible().catch(() => false), where("swipe back"), "a Back to list button", "no visible Back to list button on the article");
  const articleButtons = await page.evaluate(() => [...document.querySelectorAll("button")].filter((b) => b.checkVisibility()).map((b) => b.getAttribute("aria-label") || b.textContent.trim()));
  check(["Star", "Previous article", "Next article"].every((n) => articleButtons.some((t) => t.startsWith(n))), where("article actions"), "Star, Previous and Next article buttons", `the article has: ${articleButtons.join(", ")}`);
  // Reordering feeds by dragging: Move to folder reaches the same result with no drag (Select mode).
  await load(page, "/feeds");
  await page.getByRole("button", { name: "Select" }).first().click();
  const moveTo = await page.getByText("Move to folder").count().catch(() => 0);
  check(moveTo > 0, where("drag to reorder"), "Select mode offers Move to folder (the grips also take arrow keys)", "no Move to folder in Select mode");
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
