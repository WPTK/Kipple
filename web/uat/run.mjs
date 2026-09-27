// UAT Suite 1 (docs/uat-plan.md): a scripted pass over every screen of a running Kipple.
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:7080)
//   npm run uat                        in another, once the feeds have fetched
//   npm run uat -- --url http://127.0.0.1:7091 --screenshots --headed
//
// Checks, per screen, theme (Paper and Midnight, through the browser's light and dark preference, since the default
// theme follows the device) and width (desktop 1280, small tablet 768, phone 390):
//   S1  no uncaught errors or console errors
//   S2  no 4xx/5xx or failed requests to /api/*
//   S3  axe-core, WCAG 2.0/2.1/2.2 A and AA rules
//   S4  no sideways scroll (page or an unintended scroller) and nothing past the right edge (tablet and phone widths)
//   S5  no literal "undefined", "NaN", "[object Object]" or "Invalid Date" in visible text, field values or
//       accessible names
//   S6  scripts/contrast.mjs over all 20 schemes
// Known, accepted issues are waived in uat/waivers.json, each with a reason.
//
// Exit code: 0 clean, 1 findings, 2 a screen or the run itself could not be checked.
//
// Each theme and width is a fresh browser that signs in with the same session but registers as a new device, so the
// owner's own device settings are never touched. The run still changes the instance: it switches those devices'
// list layout, opens an article (which marks it read and records reading stats) and leaves the new device rows
// behind. So it only runs against a loopback address unless --allow-remote is given: point it at a throwaway or
// copied instance, never at the one you read on.
//
// Options (environment variable in brackets):
//   --url <base>        Kipple to test [KIPPLE_UAT_URL], default http://127.0.0.1:7080
//   --user <name>       [KIPPLE_UAT_USER], default the seed's user
//   --password <pw>     [KIPPLE_UAT_PASSWORD], default the seed's password
//   --out <dir>         report directory, default uat/results/<timestamp>
//   --only <ids>        comma-separated screen ids to run (see SCREENS below)
//   --screenshots       save a full-page screenshot of every screen (failing screens are always saved)
//   --headed            show the browser
//   --allow-remote      allow a non-loopback --url
//   --help              this text
//
// First time on a machine: `npx playwright install chromium`. See docs/uat-plan.md, Suite 1.
import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium } from "@playwright/test";

// Anything that stops the run before it can check a screen exits 2, never 1 ("findings").
function setupError(message) {
  console.error(message);
  process.exit(2);
}
/** Runs `fn`, turning a throw into a setup error. */
function orSetupError(what, fn) {
  try {
    return fn();
  } catch (e) {
    return setupError(`${what}: ${e.message.split("\n")[0]}`);
  }
}

const require = createRequire(import.meta.url);
const webDir = fileURLToPath(new URL("../", import.meta.url));
const AXE_SOURCE = orSetupError("axe-core not found (npm ci)", () => readFileSync(require.resolve("axe-core/axe.min.js"), "utf8"));

const { values: opt } = orSetupError("bad arguments (see --help)", () => parseArgs({
  options: {
    url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:7080" },
    user: { type: "string", default: process.env.KIPPLE_UAT_USER || "dev" },
    // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
    password: { type: "string", default: process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing" },
    out: { type: "string" },
    only: { type: "string" },
    screenshots: { type: "boolean", default: false },
    headed: { type: "boolean", default: false },
    "allow-remote": { type: "boolean", default: false },
    help: { type: "boolean", default: false },
  },
}));
if (opt.help) {
  // The comment block at the top of this file is the usage text.
  const lines = readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/);
  console.log(lines.slice(0, lines.findIndex((l) => !l.startsWith("//"))).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}

const base = orSetupError(`bad --url ${opt.url}`, () => new URL(opt.url));
const LOOPBACK = new Set(["127.0.0.1", "[::1]", "localhost"]);
if (!LOOPBACK.has(base.hostname) && !opt["allow-remote"]) {
  setupError(
    `refusing to run against ${base.origin}: the run marks articles read and adds devices.\n` +
      "Use a throwaway local instance (npm run seed), or pass --allow-remote if you really mean it.",
  );
}
const origin = base.origin;
const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
const outDir = opt.out ?? join(webDir, "uat", "results", stamp);

const THEMES = [
  { id: "light", colorScheme: "light", scheme: "paper" },
  { id: "dark", colorScheme: "dark", scheme: "midnight" },
];
const VIEWPORTS = [
  { id: "desktop", width: 1280, height: 800, mobile: false },
  { id: "tablet", width: 768, height: 1024, mobile: true },
  { id: "phone", width: 390, height: 844, mobile: true },
];
const LAYOUTS = [
  { id: "magazine", label: "Editorial" },
  { id: "cards", label: "Cards" },
  { id: "compact", label: "Compact" },
  { id: "inbox", label: "Inbox" },
  { id: "headlines", label: "Email - Compact" },
];
// Every screen. `layout` switches the list layout on the screen before it is checked; `path` may be a function of
// the run's context. The Unread and later screens show whatever layout the last list screen left.
const SCREENS = [
  ...LAYOUTS.map((l) => ({ id: `list-${l.id}`, title: `List: ${l.label}`, path: "/l/all", layout: l })),
  { id: "unread", title: "Unread list", path: "/l/unread" },
  { id: "starred", title: "Starred (empty)", path: "/l/starred" },
  { id: "article", title: "Article", path: (ctx) => ctx.articlePath },
  { id: "search", title: "Search results", path: "/search?q=the" },
  { id: "search-empty", title: "Search, no query", path: "/search" },
  { id: "feeds", title: "Manage feeds", path: "/feeds" },
  { id: "health", title: "Feed health", path: "/health" },
  { id: "settings", title: "Settings", path: "/settings" },
  { id: "stats", title: "Stats", path: "/stats" },
  { id: "wrapped", title: "Wrapped", path: "/stats/wrapped" },
];
const only = opt.only ? new Set(opt.only.split(",").map((s) => s.trim()).filter(Boolean)) : null;
const unknown = only ? [...only].filter((id) => !SCREENS.some((s) => s.id === id)) : [];
if (unknown.length || (only && !only.size)) {
  console.error(`unknown screen id(s): ${unknown.join(", ") || "(none given)"}. Known: ${SCREENS.map((s) => s.id).join(", ")}`);
  process.exit(2);
}
const screens = only ? SCREENS.filter((s) => only.has(s.id)) : SCREENS;

// Waivers: [{ check: "S3", rule: "color-contrast", screen?: "stats", theme?: "dark", viewport?: "phone", reason }].
// Every waiver needs a reason; one that matched nothing is reported so the list does not rot.
const waivers = orSetupError("uat/waivers.json", () => {
  const list = JSON.parse(readFileSync(new URL("./waivers.json", import.meta.url), "utf8"));
  if (!Array.isArray(list)) throw new Error("must be a JSON array");
  for (const w of list) if (!w?.check || !w.reason) throw new Error(`every waiver needs a check and a reason: ${JSON.stringify(w)}`);
  return list;
});
const used = new Set();
function waived(check, rule, where) {
  const hits = waivers
    .map((w, i) => [w, i])
    .filter(
      ([w]) =>
        w.check === check &&
        (w.rule === undefined || w.rule === rule) &&
        (w.screen === undefined || w.screen === where.screen) &&
        (w.theme === undefined || w.theme === where.theme) &&
        (w.viewport === undefined || w.viewport === where.viewport),
    );
  for (const [, i] of hits) used.add(i);
  return hits[0]?.[0] ?? null;
}

// { check, severity: "fail" | "waived" | "note", screen?, theme?, viewport?, rule?, message, detail?, waiver? }
const findings = [];
function report(check, where, rule, message, detail) {
  const w = waived(check, rule, where);
  findings.push({ check, severity: w ? "waived" : "fail", ...where, rule, message, detail, waiver: w?.reason });
}
function note(check, where, message, detail) {
  findings.push({ check, severity: "note", ...where, message, detail });
}
// A screen or the whole run could not be checked: never waivable, exit code 2.
function broken(where, message) {
  findings.push({ check: "run", severity: "fail", ...where, message });
}

// ---- in-page probes (run in the page through page.evaluate) ----

// Where the feed's own text shows. `body` is the article's HTML (the reader pane's .article-body only: Settings >
// Appearance draws its preview with the same class, and that is Kipple's). `text` adds the feed-supplied fields of
// list rows (title, excerpt) and the article title; the rows' times, badges and the article's byline are Kipple's.
const FEED = {
  body: 'article[aria-labelledby="article-title"] .article-body',
  text: 'article[aria-labelledby="article-title"] .article-body, [data-item-id] h3, [data-item-id] p, #article-title',
};

// S4. The page must not scroll sideways, and no visible element may run past the right edge of the viewport. Content
// wider than a scroll container that fits on screen is caught through that container: every screen scrolls in an
// `overflow-y-auto` box, whose overflow-x computes to auto as well, so a container that actually scrolls sideways is
// a finding unless it asks for it (a Tailwind overflow-x-auto/-scroll or overflow-auto/-scroll class) or holds the
// feed's own article HTML (wide code blocks and tables scroll there on purpose).
function overflowProbe(feed) {
  const vw = document.documentElement.clientWidth;
  const out = { scrollWidth: document.documentElement.scrollWidth, vw, offenders: [], scrollers: [] };
  const describe = (el) => {
    const id = el.id ? `#${el.id}` : "";
    const cls = typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
    const name = el.getAttribute("aria-label") || (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 60);
    return `${el.tagName.toLowerCase()}${id}${cls}${name ? ` "${name}"` : ""}`;
  };
  const visible = (el) => el.checkVisibility({ opacityProperty: true, visibilityProperty: true });
  const scrolls = (el) => {
    const ox = getComputedStyle(el).overflowX;
    return ox === "auto" || ox === "scroll";
  };
  const deliberate = (el) =>
    /(^|\s)([\w-]+:)*overflow(-x)?-(auto|scroll)(\s|$)/.test(typeof el.className === "string" ? el.className : "") ||
    !!el.closest(feed.body);
  // Clipped by an ancestor that itself ends inside the viewport (overflow hidden, or a scroller: those are checked
  // on their own below), or hidden the screen-reader-only way. Only ancestors from the containing block up clip: a
  // fixed element escapes every one, an absolute one those between it and its offset parent.
  const contained = (el) => {
    for (let a = el; a && a !== document.body; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.clip === "rect(0px, 0px, 0px, 0px)" || cs.clipPath === "inset(50%)") return true;
    }
    const pos = getComputedStyle(el).position;
    if (pos === "fixed") return false;
    let a = pos === "absolute" ? el.offsetParent : el.parentElement;
    for (; a && a !== document.body; a = a.parentElement) {
      if (getComputedStyle(a).overflowX !== "visible" && a.getBoundingClientRect().right <= vw + 1) return true;
    }
    return false;
  };
  for (const el of document.body.querySelectorAll("*")) {
    if (scrolls(el) && el.scrollWidth > el.clientWidth + 1 && !deliberate(el) && visible(el)) {
      if (out.scrollers.length < 10) out.scrollers.push({ desc: describe(el), scrollWidth: el.scrollWidth, clientWidth: el.clientWidth });
    }
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0 || r.right <= vw + 1) continue;
    if (r.left >= vw) continue; // entirely off screen: a closed drawer or an off-canvas panel
    if (!visible(el) || contained(el)) continue;
    // Report the outermost offender only.
    if (out.offenders.some((o) => o.el.contains(el))) continue;
    if (out.offenders.length < 10) out.offenders.push({ el, desc: describe(el), right: Math.round(r.right) });
  }
  out.offenders = out.offenders.map(({ desc, right }) => ({ desc, right }));
  return out;
}

// S5. Visible text nodes, form field values and accessible-name attributes: "undefined", "NaN" (also with a unit
// stuck to it, as in "NaNm"), "[object Object]" and "Invalid Date". Feed text (see FEED) can legitimately say
// "undefined behaviour" or "NaN-boxing", so there only a comma- or line-separated piece that is nothing but the
// literal (a field that rendered as undefined) counts; other hits there are notes.
function literalProbe(feed) {
  const bad = /\bundefined\b|\bNaN(?![A-Za-z]{2})|\[object Object\]|\bInvalid Date\b/;
  const alone = /^\s*(undefined|NaN|\[object Object\]|Invalid Date)\s*$/;
  const hits = [];
  const add = (el, where, text) => {
    const inFeed = !!el.closest(feed.text);
    const own = !inFeed || text.split(/[,·|\n]/).some((part) => alone.test(part));
    hits.push({ where, text: text.trim().replace(/\s+/g, " ").slice(0, 160), feed: !own });
  };
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const el = n.parentElement;
    if (!el || !n.nodeValue || !bad.test(n.nodeValue) || el.closest("script, style, noscript, template")) continue;
    if (!el.checkVisibility({ visibilityProperty: true })) continue;
    add(el, el.tagName.toLowerCase(), n.nodeValue);
  }
  for (const el of document.body.querySelectorAll("[aria-label],[title],[alt],[placeholder],[aria-valuetext]")) {
    if (!el.checkVisibility({ visibilityProperty: true })) continue;
    for (const a of ["aria-label", "title", "alt", "placeholder", "aria-valuetext"]) {
      const v = el.getAttribute(a);
      if (v && bad.test(v)) add(el, `${el.tagName.toLowerCase()}[${a}]`, v);
    }
  }
  for (const el of document.body.querySelectorAll("input, textarea")) {
    if (el.type === "hidden" || el.type === "password" || !el.checkVisibility({ visibilityProperty: true })) continue;
    if (el.value && bad.test(el.value)) add(el, `${el.tagName.toLowerCase()}.value`, el.value);
  }
  if (bad.test(document.title)) hits.push({ where: "title", text: document.title, feed: false });
  return hits.slice(0, 30);
}

async function runAxe(page) {
  // Evaluated over the DevTools protocol rather than added as a <script>: Kipple's CSP (script-src 'self') would
  // block an inline script, and turning the CSP off would hide the app's own CSP violations from S1.
  if (!(await page.evaluate(() => typeof window.axe !== "undefined"))) await page.evaluate(AXE_SOURCE);
  return page.evaluate(async (feed) => {
    const r = await window.axe.run(document, {
      runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22a", "wcag22aa"] },
      resultTypes: ["violations"],
    });
    return r.violations.map((v) => ({
      id: v.id,
      impact: v.impact,
      help: v.help,
      helpUrl: v.helpUrl,
      nodes: v.nodes.map((n) => ({
        target: n.target.join(" "),
        html: n.html.slice(0, 300),
        summary: n.failureSummary?.split("\n").slice(0, 3).join(" ").slice(0, 300),
        // Nodes inside the article body are the feed's own markup, reported apart from Kipple's.
        feedContent: !!document.querySelector(n.target.join(" "))?.closest(feed.body),
      })),
    }));
  }, FEED);
}

// ---- navigation helpers ----

async function setLayout(page, layout) {
  const btn = page.locator('button[aria-label^="Layout: "]').first();
  await btn.waitFor({ timeout: 15000 });
  if ((await btn.getAttribute("aria-label")) === `Layout: ${layout.label}`) return;
  await btn.click();
  // The radio's name is the label plus its hint; anchor it so "Compact" does not match "Email - Compact".
  const esc = layout.label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  await page.getByRole("menuitemradio", { name: new RegExp(`^${esc}\\b`) }).click();
  await page.locator(`button[aria-label="Layout: ${layout.label}"]`).first().waitFor({ timeout: 10000 });
  if (await page.getByRole("menu").isVisible().catch(() => false)) await page.keyboard.press("Escape");
}

// Wait for the lazy screen and its queries: no busy skeleton, no boot splash, then a short beat for late renders.
async function settle(page) {
  await page.waitForLoadState("load");
  await page
    .waitForFunction(
      () => !document.querySelector('[aria-busy="true"]') && !/^\s*Loading Kipple\s*$/.test(document.body.innerText),
      null,
      { timeout: 15000 },
    )
    .catch(() => {
      // Checking a skeleton would pass a screen whose content was never seen.
      throw new Error("still loading after 15 s");
    });
  await page.waitForTimeout(700);
}

// ---- the run ----
const browser = await chromium
  .launch({ headless: !opt.headed })
  .catch((e) => setupError(`could not start Chromium (npx playwright install chromium): ${e.message.split("\n")[0]}`));
let exitCode;
try {
  await selfTest();
  const cookies = await signIn();
  mkdirSync(outDir, { recursive: true });
  console.log(`Kipple at ${origin}. Report: ${relative(process.cwd(), outDir) || "."}`);
  const results = await checkScreens(cookies);
  const s6 = contrastCheck();
  exitCode = writeReport(results, s6);
} catch (e) {
  console.error(`run aborted: ${e.message.split("\n")[0]}`);
  exitCode = 2;
} finally {
  await browser.close();
}
process.exit(exitCode);

// Each probe must flag a page built to fail it, so a quiet run means clean, not broken.
async function selfTest() {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.setContent(
    `<main><p>Count: NaN</p><p>Updated NaNm ago</p><p>Invalid Date</p><button aria-label="[object Object]">x</button>` +
      `<label>Night starts <input value="undefined"></label><div id="wide" style="width:600px">wide</div>` +
      `<article aria-labelledby="article-title"><h1 id="article-title">NaN-boxing explained</h1><p>Tue · NaN min read</p>` +
      `<div class="article-body"><p>undefined behaviour is fine in an article</p></div></article>` +
      `<div class="article-body"><p>Preview: NaN</p></div>` +
      `<div data-item-id="1"><h3><a href="#x" aria-label="Unread, undefined, Some feed">Some title</a></h3>` +
      `<p>An excerpt about NaN-boxing</p><time>NaNm</time></div>` +
      `<div style="display:none"><span>NaN hidden</span></div>` +
      `<div id="pane" style="overflow-y:auto;height:100px"><div style="width:700px">too wide for its pane</div></div>` +
      `<div style="overflow-y:auto;height:40px"><div id="fixed" style="position:fixed;left:0;bottom:0;width:500px">fixed bar</div></div>` +
      `<div class="overflow-x-auto" style="overflow-x:auto"><div style="width:900px">a scroller on purpose</div></div>` +
      `<span style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)">sr only</span></main>`,
  );
  const o = await page.evaluate(overflowProbe, FEED);
  const lit = await page.evaluate(literalProbe, FEED);
  const axeIds = (await runAxe(page)).map((v) => v.id);
  await page.close();
  const problems = [];
  const same = (got, want) => JSON.stringify([...got].sort()) === JSON.stringify([...want].sort());
  const own = lit.filter((h) => !h.feed).map((h) => h.text);
  const feed = lit.filter((h) => h.feed).map((h) => h.text);
  const wantOwn = ["Count: NaN", "Updated NaNm ago", "Invalid Date", "[object Object]", "undefined", "Tue · NaN min read", "Preview: NaN", "Unread, undefined, Some feed", "NaNm"];
  const wantFeed = ["NaN-boxing explained", "undefined behaviour is fine in an article", "An excerpt about NaN-boxing"];
  if (!(o.scrollWidth > o.vw)) problems.push("S4 did not see the page scroll sideways");
  if (!same(o.offenders.map((x) => x.desc.split(" ")[0]), ["div#wide", "div#fixed"])) problems.push(`S4 offenders ${JSON.stringify(o.offenders)}`);
  if (!same(o.scrollers.map((x) => x.desc.split(" ")[0]), ["div#pane"])) problems.push(`S4 scrollers ${JSON.stringify(o.scrollers)}`);
  if (!same(own, wantOwn)) problems.push(`S5 hits ${JSON.stringify(own)}`);
  if (!same(feed, wantFeed)) problems.push(`S5 feed notes ${JSON.stringify(feed)}`);
  // The fixture has no <title> and no lang: two violations axe always reports.
  if (!axeIds.includes("document-title") || !axeIds.includes("html-has-lang")) problems.push(`S3 missed a known violation (${axeIds.join(", ")})`);
  if (problems.length) throw new Error(`self-test failed: ${problems.join("; ")}`);
}

// Signs in through the form once. Every context reuses the session cookie only: without the device cookie each one
// registers as a new device, as a new browser would, and the owner's devices keep their settings.
async function signIn() {
  const ctx = await browser.newContext();
  try {
    const page = await ctx.newPage();
    const res = await page.goto(origin + "/", { waitUntil: "load" });
    if (!res || !res.ok()) throw new Error(`${origin} answered ${res?.status()}`);
    await page.getByLabel("Username").waitFor({ timeout: 15000 }).catch(() => {
      throw new Error(`no sign-in form at ${origin}. Is the UI built (npm run build) before the binary (npm run seed)?`);
    });
    await page.getByLabel("Username").fill(opt.user);
    await page.getByLabel("Password").fill(opt.password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 }).catch(async () => {
      const alert = await page.getByRole("alert").first().textContent().catch(() => null);
      throw new Error(`sign-in failed${alert ? `: ${alert}` : ""}`);
    });
    const cookies = (await ctx.storageState()).cookies.filter((c) => c.name !== "kipple_device");
    if (!cookies.some((c) => c.name === "kipple_session")) throw new Error("signed in, but no kipple_session cookie");
    return cookies;
  } finally {
    await ctx.close();
  }
}

async function checkScreens(cookies) {
  const results = []; // one row per screen x theme x viewport
  const ctxInfo = {};
  for (const theme of THEMES) {
    for (const vp of VIEWPORTS) {
      const combo = { theme: theme.id, viewport: vp.id };
      let context;
      try {
        context = await browser.newContext({
          baseURL: origin,
          colorScheme: theme.colorScheme,
          viewport: { width: vp.width, height: vp.height },
          isMobile: vp.mobile,
          hasTouch: vp.mobile,
          storageState: { cookies, origins: [] },
          // Keep the run self-contained: no service worker answering from its cache between screens.
          serviceWorkers: "block",
        });
        const page = await context.newPage();
        await checkCombo(page, theme, vp, ctxInfo, results);
      } catch (e) {
        broken(combo, `could not open a browser at ${vp.id}/${theme.id}: ${e.message.split("\n")[0]}`);
        console.log(`${theme.id} ${vp.id}: ERROR ${e.message.split("\n")[0]}`);
      } finally {
        await context?.close().catch(() => {});
      }
    }
  }
  return results;
}

async function checkCombo(page, theme, vp, ctxInfo, results) {
  let current = null; // the screen being checked, for the event handlers below
  const bucket = { console: [], api: [], other: [] };
  page.on("pageerror", (err) => current && bucket.console.push({ kind: "pageerror", text: String(err.stack || err).slice(0, 600) }));
  page.on("console", (msg) => {
    if (!current || msg.type() !== "error") return;
    const url = msg.location()?.url ?? "";
    // "Failed to load resource" is the browser echoing a failed request. Same-origin ones are reported from the
    // response below (S2 for /api/, a note otherwise); only other origins are known from this message alone.
    if (/^Failed to load resource/.test(msg.text())) {
      if (new URL(url || origin, origin).origin !== origin) bucket.other.push({ url, text: msg.text() });
      return;
    }
    bucket.console.push({ kind: "console", text: msg.text().slice(0, 600), url });
  });
  page.on("response", (res) => {
    if (!current || res.status() < 400) return;
    const u = new URL(res.url());
    if (u.origin !== origin) return;
    if (u.pathname.startsWith("/api/")) bucket.api.push({ status: res.status(), method: res.request().method(), url: u.pathname + u.search });
    else bucket.other.push({ url: u.pathname + u.search, text: `HTTP ${res.status()}` });
  });
  page.on("requestfailed", (req) => {
    if (!current) return;
    const u = new URL(req.url());
    const why = req.failure()?.errorText ?? "failed";
    // A navigation aborts whatever was in flight (the event stream, prefetches): that is not a failure.
    if (why.includes("ERR_ABORTED")) return;
    if (u.origin === origin && u.pathname.startsWith("/api/")) bucket.api.push({ status: 0, method: req.method(), url: u.pathname + u.search, error: why });
  });

  // An article to open: the first one in All. Looked for again in the next browser while none is found (the feeds may
  // still be fetching).
  if (!ctxInfo.articlePath && screens.some((s) => s.id === "article")) {
    try {
      await page.goto("/l/all");
      await settle(page);
      ctxInfo.articlePath = await page.locator('main article a[href^="/i/"]').first().getAttribute("href", { timeout: 5000 });
    } catch {
      ctxInfo.articlePath = null;
    }
  }

  // S1 and S2 events collected for the current screen, reported even when the screen could not be finished (they
  // are often why).
  const flush = (where) => {
    for (const c of bucket.console) report("S1", where, c.kind, c.text, c.url);
    for (const a of bucket.api) report("S2", where, String(a.status), `${a.method} ${a.url} -> ${a.status || a.error}`);
    for (const o of bucket.other) note("S2", where, `non-API request failed: ${o.url} ${o.text}`);
    bucket.console.length = bucket.api.length = bucket.other.length = 0;
  };

  for (const screen of screens) {
    const where = { screen: screen.id, theme: theme.id, viewport: vp.id };
    const path = typeof screen.path === "function" ? screen.path(ctxInfo) : screen.path;
    process.stdout.write(`${theme.id.padEnd(5)} ${vp.id.padEnd(7)} ${screen.id.padEnd(15)} `);
    const row = { ...where, title: screen.title, path, checks: {} };
    results.push(row);
    if (!path) {
      row.ok = false;
      broken(where, "no article in All to open (let the feeds fetch first)");
      console.log("ERROR no article to open");
      continue;
    }
    try {
      bucket.console.length = bucket.api.length = bucket.other.length = 0;
      current = screen.id;
      await page.goto(path);
      await settle(page);
      if (screen.layout) {
        await setLayout(page, screen.layout);
        await settle(page);
      }

      const scheme = await page.evaluate(() => document.documentElement.dataset.theme);
      if (scheme !== theme.scheme)
        throw new Error(`expected the ${theme.scheme} scheme, the page shows ${scheme} (does the account default to a fixed theme?)`);

      // S4 before axe, so axe's injected script is not part of the layout.
      const s4 = [];
      if (vp.id !== "desktop") {
        const o = await page.evaluate(overflowProbe, FEED);
        if (o.scrollWidth > o.vw + 1) s4.push({ rule: "page-scroll-x", message: `page scrolls sideways: scrollWidth ${o.scrollWidth} > ${o.vw}` });
        for (const s of o.scrollers) s4.push({ rule: "scroller-x", message: `${s.desc} scrolls sideways (${s.scrollWidth} > ${s.clientWidth})` });
        for (const off of o.offenders) s4.push({ rule: "past-right-edge", message: `${off.desc} ends at ${off.right}px (viewport ${o.vw}px)` });
      }
      const s5 = await page.evaluate(literalProbe, FEED);
      const axe = await runAxe(page);
      await page.waitForTimeout(200); // late console errors from the last render
      current = null;

      flush(where);
      for (const v of axe) {
        const own = v.nodes.filter((n) => !n.feedContent);
        const feed = v.nodes.filter((n) => n.feedContent);
        if (own.length) report("S3", where, v.id, `${v.id} (${v.impact}): ${v.help}`, own);
        if (feed.length) note("S3", where, `${v.id} in the feed's article HTML: ${v.help}`, feed);
      }
      for (const s of s4) report("S4", where, s.rule, s.message);
      for (const h of s5) {
        if (h.feed) note("S5", where, `feed text ${h.where}: "${h.text}"`);
        else report("S5", where, "literal", `${h.where}: "${h.text}"`);
      }

      const mine = findings.filter((f) => f.screen === screen.id && f.theme === theme.id && f.viewport === vp.id && f.severity === "fail");
      row.checks = Object.fromEntries(["S1", "S2", "S3", "S4", "S5"].map((c) => [c, mine.filter((f) => f.check === c).length]));
      row.ok = mine.length === 0;
      if (!row.ok || opt.screenshots) {
        row.screenshot = `${screen.id}-${theme.id}-${vp.id}.png`;
        await page.screenshot({ path: join(outDir, row.screenshot), fullPage: true });
      }
      console.log(row.ok ? "ok" : Object.entries(row.checks).filter(([, n]) => n).map(([c, n]) => `${c}x${n}`).join(" "));
    } catch (e) {
      current = null;
      row.ok = false;
      row.error = e.message.split("\n")[0];
      flush(where);
      broken(where, `could not check the screen: ${row.error}`);
      console.log(`ERROR ${row.error}`);
      await page.screenshot({ path: join(outDir, `${screen.id}-${theme.id}-${vp.id}-error.png`), fullPage: true }).catch(() => {});
    }
  }
}

// S6: the theme contrast check over every scheme.
function contrastCheck() {
  const r = spawnSync(process.execPath, [join(webDir, "scripts", "contrast.mjs")], { cwd: webDir, encoding: "utf8" });
  const s6 = { ok: r.status === 0, output: `${r.stdout ?? ""}${r.stderr ?? ""}${r.error ? String(r.error) : ""}`.trim() };
  if (!s6.ok) report("S6", {}, "contrast", "theme contrast check failed", s6.output);
  console.log(`\nS6 theme contrast (all schemes): ${s6.ok ? "ok" : "FAILED"}\n${s6.output}`);
  return s6;
}

function writeReport(results, s6) {
  for (const [i, w] of waivers.entries()) if (!used.has(i)) note("waivers", {}, `unused waiver: ${JSON.stringify(w)}`);
  const fails = findings.filter((f) => f.severity === "fail");
  const summary = Object.fromEntries(
    ["S1", "S2", "S3", "S4", "S5", "S6", "run"].map((c) => [
      c,
      { fail: fails.filter((f) => f.check === c).length, waived: findings.filter((f) => f.check === c && f.severity === "waived").length },
    ]),
  );
  writeFileSync(join(outDir, "report.json"), JSON.stringify({ origin, stamp, summary, results, findings, s6 }, null, 2));

  // Findings grouped by check and message, so one issue seen on 30 screens reads as one entry listing where it was
  // seen and each distinct element (axe nodes) once, with the screens it was on.
  const groups = new Map();
  for (const f of findings) {
    const key = `${f.severity}|${f.check}|${f.rule ?? ""}|${f.message}`;
    if (!groups.has(key)) groups.set(key, { ...f, seen: [], nodes: new Map() });
    const g = groups.get(key);
    if (f.screen) g.seen.push(`${f.screen}/${f.theme}/${f.viewport}`);
    else if (f.theme) g.seen.push(`${f.theme}/${f.viewport}`);
    if (Array.isArray(f.detail)) {
      for (const n of f.detail) {
        if (!g.nodes.has(n.target)) g.nodes.set(n.target, { ...n, on: new Set() });
        if (f.screen) g.nodes.get(n.target).on.add(f.screen);
      }
    }
  }
  const checked = results.filter((r) => !r.error && r.path).length;
  const md = [
    `# UAT Suite 1 report`,
    ``,
    `${origin}, ${stamp}. ${checked} of ${screens.length * THEMES.length * VIEWPORTS.length} screen checks completed ` +
      `(${screens.length} screens x ${THEMES.length} themes x ${VIEWPORTS.length} widths).`,
    ``,
    `| Check | Failures | Waived |`,
    `|---|---|---|`,
    ...Object.entries(summary).map(([c, s]) => `| ${c} | ${s.fail} | ${s.waived} |`),
    ``,
  ];
  for (const sev of ["fail", "waived", "note"]) {
    const list = [...groups.values()].filter((g) => g.severity === sev);
    if (!list.length) continue;
    md.push(`## ${sev === "fail" ? "Failures" : sev === "waived" ? "Waived" : "Notes"}`, "");
    for (const g of list) {
      md.push(`- **${g.check}** ${g.message}${g.waiver ? ` (waived: ${g.waiver})` : ""}`);
      if (g.seen.length) md.push(`  - seen on ${g.seen.length}: ${g.seen.slice(0, 12).join(", ")}${g.seen.length > 12 ? ", ..." : ""}`);
      const nodes = [...g.nodes.values()];
      for (const n of nodes.slice(0, 6)) {
        md.push(`  - \`${n.target}\` on ${[...n.on].join(", ")}: ${n.summary ?? ""}`);
        md.push(`    \`${n.html.replace(/`/g, "'")}\``);
      }
      if (nodes.length > 6) md.push(`  - ${nodes.length - 6} more element(s) in report.json`);
      if (!nodes.length && g.detail && g.check !== "S6") md.push(`  - ${String(g.detail).slice(0, 300)}`);
    }
    md.push("");
  }
  writeFileSync(join(outDir, "report.md"), md.join("\n"));

  console.log(`\n${Object.entries(summary).map(([c, s]) => `${c} ${s.fail}${s.waived ? ` (+${s.waived} waived)` : ""}`).join("  ")}`);
  console.log(`Report: ${join(outDir, "report.md")}`);
  if (summary.run.fail) return 2;
  return fails.length ? 1 : 0;
}
