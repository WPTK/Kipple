// UAT Suite 1 (docs/uat-plan.md): a scripted pass over every screen of a running Kipple.
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:1919)
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
//   S7  the reading-font choice is reachable: the Aa menu above every list and article, and Settings > Appearance &
//       Reading, each offer a "Reading font" select with every font (it once went missing from Settings unnoticed)
// Known, accepted issues are waived in uat/waivers.json:
//   [{ "check": "S3", "rule"?: "<axe id>", "match"?: "<text in the finding>", "screen"?, "theme"?, "viewport"?, "browser"?,
//      "reason": "why this is accepted" }]
//
// Exit code: 0 clean, 1 findings, 2 a screen or the run itself could not be checked.
//
// Every theme and width is a fresh browser sharing one session and one device of the run's own (its cookie is kept
// in uat/results/.device-<host>-<port>.json, so repeated runs reuse it rather than filling the server's device
// table), so the owner's own devices are never touched. The run still changes the instance: it switches that
// device's list layout and opens an article (which marks it read and records reading stats). So it only runs against
// a loopback address with the seed's credentials unless --allow-remote is given: point it at a throwaway or copied
// instance, never at the one you read on.
//
// Options (environment variable in brackets):
//   --url <base>        Kipple to test [KIPPLE_UAT_URL], default http://127.0.0.1:1919
//   --user <name>       [KIPPLE_UAT_USER], default the seed's user
//   --password <pw>     [KIPPLE_UAT_PASSWORD], default the seed's password
//   --out <dir>         report directory, default uat/results/<timestamp> (per engine, with --browsers)
//   --only <ids>        comma-separated screen ids to run (see SCREENS below)
//   --screenshots       save a full-page screenshot of every screen (failing screens are always saved)
//   --browser <engine>  chromium (default), firefox or webkit [KIPPLE_UAT_BROWSER]
//   --browsers <list>   several engines in turn, comma-separated, or "all": one run each, reports in <out>/<engine>;
//                       the exit code is the worst of them. The default run stays Chromium only
//   --headed            show the browser
//   --allow-remote      allow a non-loopback --url or credentials other than the seed's
//   --help              this text
//
// First time on a machine: `npx playwright install chromium` (and firefox webkit for --browsers). See docs/uat-plan.md, Suite 1.
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium, firefox, webkit } from "@playwright/test";
import { activeScheme, axeProbe, hasAxe, installPageHelpers, literalProbe, overflowProbe, pushRoute, screenReady } from "./probes.mjs";

/** The first line of an error's message. */
const firstLine = (e) => String(e?.message ?? e).split("\n")[0];
/** `s` as a literal inside a RegExp. */
const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

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
    return setupError(`${what}: ${firstLine(e)}`);
  }
}

const require = createRequire(import.meta.url);
const webDir = fileURLToPath(new URL("../", import.meta.url));
const AXE_SOURCE = orSetupError("axe-core not found (npm ci)", () => readFileSync(require.resolve("axe-core/axe.min.js"), "utf8"));

const { values: opt } = orSetupError("bad arguments (see --help)", () => parseArgs({
  options: {
    url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919" },
    user: { type: "string", default: process.env.KIPPLE_UAT_USER || "dev" },
    // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
    password: { type: "string", default: process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing" },
    out: { type: "string" },
    only: { type: "string" },
    screenshots: { type: "boolean", default: false },
    headed: { type: "boolean", default: false },
    browser: { type: "string", default: process.env.KIPPLE_UAT_BROWSER || "chromium" },
    browsers: { type: "string" },
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

const ENGINES = { chromium, firefox, webkit };
const engineName = opt.browser;
if (!(engineName in ENGINES)) setupError(`unknown --browser ${engineName}. Known: ${Object.keys(ENGINES).join(", ")}`);
if (opt.browsers !== undefined) {
  // One child run per engine, in turn (never in parallel: they share the instance and its device), each with its own
  // report directory. The exit code is the worst of them (2 over 1 over 0), so a missing engine is never a pass.
  const list = opt.browsers === "all" ? Object.keys(ENGINES) : opt.browsers.split(",").map((b) => b.trim()).filter(Boolean);
  const bad = list.filter((b) => !(b in ENGINES));
  if (!list.length || bad.length) setupError(`bad --browsers: ${bad.join(", ") || "(none given)"}. Use all or a list of ${Object.keys(ENGINES).join(", ")}`);
  const parent = opt.out ?? join(webDir, "uat", "results", `browsers-${new Date().toISOString().replace(/[:.]/g, "-")}`);
  const own = ["--browsers", "--browser", "--out"];
  const passthrough = process.argv.slice(2).filter((a, i, all) => !own.includes(a.replace(/=.*/, "")) && !(i > 0 && own.includes(all[i - 1])));
  let worst = 0;
  for (const b of new Set(list)) {
    console.log(`\n==== ${b} ====`);
    const r = spawnSync(process.execPath, [fileURLToPath(import.meta.url), ...passthrough, "--browser", b, "--out", join(parent, b)], { stdio: "inherit" });
    const code = r.status ?? 2;
    console.log(`==== ${b}: exit ${code} ====`);
    worst = Math.max(worst, code);
  }
  process.exit(worst);
}

const base = orSetupError(`bad --url ${opt.url}`, () => new URL(opt.url));
const LOOPBACK = new Set(["127.0.0.1", "[::1]", "localhost"]);
// A loopback address alone does not prove a throwaway instance (a port forward can put the real one there), so other
// credentials than the seed's need the flag too: the seed's only work on a seeded instance.
const seedCredentials = opt.user === "dev" && opt.password === "dev-password-only-for-local-testing";
if ((!LOOPBACK.has(base.hostname) || !seedCredentials) && !opt["allow-remote"]) {
  setupError(
    `refusing to run against ${base.origin}${seedCredentials ? "" : ` as ${opt.user}`}: the run marks articles read and changes a device's layout.\n` +
      "Use the seeded throwaway instance (npm run seed) with its default account, or pass --allow-remote for a copied instance.",
  );
}
const origin = base.origin;
// Local time, for the results directory and the report (the offset is written out in the report).
const started = new Date();
const pad = (n) => String(n).padStart(2, "0");
const stamp =
  `${started.getFullYear()}-${pad(started.getMonth() + 1)}-${pad(started.getDate())}T` +
  `${pad(started.getHours())}-${pad(started.getMinutes())}-${pad(started.getSeconds())}`;
const startedText = started.toLocaleString("en-US", { dateStyle: "medium", timeStyle: "long" });
// A second run in the same second gets its own directory rather than mixing its report into the first one's.
let outDir = opt.out ?? join(webDir, "uat", "results", stamp);
if (!opt.out) for (let n = 2; existsSync(outDir); n++) outDir = join(webDir, "uat", "results", `${stamp}-${n}`);
// The run's own device, remembered per instance so repeated runs reuse it: the server caps new devices per session
// and in total, and each new one would stay registered for a month.
const deviceFile = join(webDir, "uat", "results", `.device-${base.hostname.replace(/[^\w.-]/g, "_")}-${base.port || "default"}.json`);

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
// The query the Search screen runs (a word every seeded feed has).
const SEARCH_Q = "the";
// Every screen. `layout` switches the list layout on the screen before it is checked; `path` may be a function of
// the run's context. The Unread and later screens show whatever layout the last list screen left.
// `heading` must be the text of a visible h1 once the screen has settled, which proves the check looks at the right
// screen (and that in-app navigation reached it).
const SCREENS = [
  ...LAYOUTS.map((l) => ({ id: `list-${l.id}`, title: `List: ${l.label}`, path: "/l/all", layout: l, heading: "All articles", font: "menu" })),
  { id: "unread", title: "Unread list", path: "/l/unread", heading: "Unread", font: "menu" },
  { id: "starred", title: "Starred (empty)", path: "/l/starred", heading: "Starred", font: "menu" },
  { id: "article", title: "Article", path: (ctx) => ctx.articlePath, heading: (ctx) => ctx.articleTitle, font: "menu" },
  { id: "search", title: "Search results", path: `/search?q=${SEARCH_Q}`, heading: "Search", font: "menu" },
  { id: "search-empty", title: "Search, no query", path: "/search", heading: "Search" },
  { id: "feeds", title: "Manage feeds", path: "/feeds", heading: "Feeds" },
  { id: "health", title: "Feed health", path: "/health", heading: "Feed health" },
  // Settings is master-detail: /settings is the group list on a narrow screen (and the first group beside the rail on
  // a wide one), and each group is its own page, whose h1 is the group's name below 900 px and "Settings" beside the rail.
  { id: "settings", title: "Settings", path: "/settings", heading: "Settings" },
  ...[
    ["appearance", "Appearance & Reading"],
    ["sync", "Sync & Feeds"],
    ["statistics", "Statistics"],
    ["filters", "Filters & Saved Searches"],
    ["account", "Account & Devices"],
    ["advanced", "Advanced"],
  ].map(([g, label]) => ({ id: `settings-${g}`, title: `Settings: ${label}`, path: `/settings/${g}`, heading: (_ctx, vp) => (vp.width >= 900 ? "Settings" : label), font: g === "appearance" ? "settings" : undefined })),
  { id: "stats", title: "Stats", path: "/stats", heading: "Stats" },
  { id: "wrapped", title: "Wrapped", path: "/stats/wrapped", heading: "Your year" },
];
const only = opt.only !== undefined ? new Set(opt.only.split(",").map((s) => s.trim()).filter(Boolean)) : null;
const unknown = only ? [...only].filter((id) => !SCREENS.some((s) => s.id === id)) : [];
if (unknown.length || (only && !only.size)) {
  setupError(`unknown screen id(s): ${unknown.join(", ") || "(none given)"}. Known: ${SCREENS.map((s) => s.id).join(", ")}`);
}
const screens = only ? SCREENS.filter((s) => only.has(s.id)) : SCREENS;

// Waivers (format in the header comment). Every waiver needs a reason, and every key and value is checked, so a typo
// cannot widen one to everything. S1, S2, S4, S5 and S6 have coarse rules (S5's is always "literal"), so a waiver
// for them must also name the text of the one finding it accepts in `match` (S3 findings are one element each, so
// a rule id alone is already narrow, and `match` can name the element). One that matched nothing is reported so the
// list does not rot.
const waivers = orSetupError("uat/waivers.json", () => {
  const list = JSON.parse(readFileSync(new URL("./waivers.json", import.meta.url), "utf8"));
  if (!Array.isArray(list)) throw new Error("must be a JSON array");
  const allowed = {
    check: ["S1", "S2", "S3", "S4", "S5", "S6"],
    browser: Object.keys(ENGINES),
    screen: [...SCREENS.map((s) => s.id), "boot"],
    theme: THEMES.map((t) => t.id),
    viewport: VIEWPORTS.map((v) => v.id),
  };
  for (const w of list) {
    const bad = (why) => new Error(`${why}: ${JSON.stringify(w)}`);
    if (typeof w !== "object" || w === null) throw bad("a waiver is an object");
    for (const k of Object.keys(w)) if (!["check", "rule", "match", "screen", "theme", "viewport", "browser", "reason"].includes(k)) throw bad(`unknown key "${k}"`);
    for (const k of Object.keys(w)) if (typeof w[k] !== "string" || !w[k]) throw bad(`"${k}" must be a non-empty string`);
    if (!w.check || !w.reason) throw bad("every waiver needs a check and a reason");
    for (const [k, ok] of Object.entries(allowed)) if (w[k] !== undefined && !ok.includes(w[k])) throw bad(`"${k}" must be one of ${ok.join(", ")}`);
    if (["S1", "S2", "S4", "S5", "S6"].includes(w.check) && !w.match) throw bad(`an ${w.check} waiver needs "match"`);
  }
  return list;
});
const used = new Set();
function waived(check, rule, where, message, detail) {
  const text = `${message}\n${detail === undefined ? "" : JSON.stringify(detail)}`;
  const hits = waivers
    .map((w, i) => [w, i])
    .filter(
      ([w]) =>
        w.check === check &&
        (w.rule === undefined || w.rule === rule) &&
        (w.match === undefined || text.includes(w.match)) &&
        (w.screen === undefined || w.screen === where.screen) &&
        (w.theme === undefined || w.theme === where.theme) &&
        (w.viewport === undefined || w.viewport === where.viewport) &&
        (w.browser === undefined || w.browser === engineName),
    );
  for (const [, i] of hits) used.add(i);
  return hits[0]?.[0] ?? null;
}

// { check, severity: "fail" | "waived" | "note", screen?, theme?, viewport?, rule?, message, detail?, waiver? }
const findings = [];
function report(check, where, rule, message, detail) {
  const w = waived(check, rule, where, message, detail);
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

// What the feeds supplied, so findings in it are told apart from Kipple's own.
const FEED = {
  /** The article's own HTML: the reader pane's .article-body only (Settings > Appearance draws its preview with the
   * same class, and that is Kipple's). */
  body: 'article[aria-labelledby="article-title"] .article-body',
  /** What Kipple itself adds inside the article body (embed frames and play buttons, highlight marks: articleDom.ts,
   * highlight.ts), which stays Kipple's for S3 and S4. Highlight marks wrap the feed's own words, so S5 leaves them
   * out of this (`ownText`). */
  own: '[class^="kp-"], [class*=" kp-"]',
  ownText: '[class^="kp-"]:not(.kp-hl), [class*=" kp-"]:not(.kp-hl)',
  /** The S5 literals (see literalProbe). No word boundary after undefined and NaN: "undefinedm ago", "NaNkB". */
  pattern: String.raw`\bundefined|\bNaN|\[object Object\]|\bInvalid Date\b`,
  /** Feed-supplied strings that themselves contain an S5 literal: feed, folder and saved-search names and item titles,
   * excerpts, authors, sources and search snippets, as the API returns them (harvestFeedText). */
  names: [],
};
// Installed in every page next to axe (an init script, or page.evaluate for the self-test page): see probes.mjs.
const PAGE_SCRIPT = `${AXE_SOURCE}\n(${installPageHelpers.toString()})();`;
const FEED_KEYS = new Set(["title", "name", "excerpt", "author", "source", "origin_title", "feed_title", "folder_name", "snippet"]);
const feedStrings = new Set();
const decodeSnippet = (h) => {
  // Strip tags to a fixed point rather than one pass, so a malformed nested tag like "<scr<script>ipt>"
  // can't survive a single regex sweep and reassemble into something tag-shaped. This output is only ever
  // compared as plain text (feedStrings/FEED.names), never rendered as HTML, but a thorough strip costs
  // nothing here and keeps the pattern honest.
  let prev;
  do {
    prev = h;
    h = h.replace(/<[^>]*>/g, "");
  } while (h !== prev);
  return h.replace(/&(amp|lt|gt|quot|#39|#34);/g, (_, e) => ({ amp: "&", lt: "<", gt: ">", quot: '"', "#39": "'", "#34": '"' })[e]);
};

async function runAxe(page) {
  // The screen checks get axe from an init script (checkScreens); the self-test page, filled by setContent, gets it
  // here. Both go over the DevTools protocol rather than a <script>: Kipple's CSP (script-src 'self') would block an
  // inline script, and turning the CSP off would hide the app's own CSP violations from S1.
  if (!(await page.evaluate(hasAxe))) await page.evaluate(PAGE_SCRIPT);
  return page.evaluate(axeProbe, FEED);
}

// ---- navigation helpers ----

async function setLayout(page, layout) {
  const btn = page.locator('button[aria-label^="Layout: "]').first();
  await btn.waitFor({ timeout: 15000 });
  if ((await btn.getAttribute("aria-label")) === `Layout: ${layout.label}`) return;
  await btn.click();
  // The radio's name is the label plus its hint; anchor it so "Compact" does not match "Email - Compact".
  const esc = escapeRe(layout.label);
  await page.getByRole("menuitemradio", { name: new RegExp(`^${esc}\\b`) }).click();
  await page.locator(`button[aria-label="Layout: ${layout.label}"]`).first().waitFor({ timeout: 10000 });
  if (await page.getByRole("menu").isVisible().catch(() => false)) await page.keyboard.press("Escape");
}

// Wait for the lazy screen and its queries: no busy skeleton, no boot splash, then a short beat for late renders.
async function settle(page) {
  await page.waitForFunction(screenReady, null, { timeout: 15000, polling: 100 }).catch((e) => {
    // Checking a skeleton would pass a screen whose content was never seen. Anything else (a crashed or closed
    // page) keeps its own message.
    throw e?.name === "TimeoutError" ? new Error("still loading after 15 s") : e;
  });
  // Then until no same-origin request has been in flight for 300 ms (the event stream never ends, so it is not
  // counted), for at most 10 s: lazy chunks and the queries a screen fires once it mounts.
  const inflight = pending.get(page);
  if (inflight) {
    let quietSince = Date.now();
    for (const until = Date.now() + 10000; Date.now() < until; ) {
      if (inflight.size) quietSince = Date.now();
      else if (Date.now() - quietSince >= 300) break;
      await page.waitForTimeout(50);
    }
  }
  await page.waitForTimeout(150);
}

/** Same-origin requests in flight per page, except the event stream (see settle). */
const pending = new WeakMap();
function trackRequests(page) {
  const set = new Set();
  pending.set(page, set);
  const done = (req) => set.delete(req);
  page.on("request", (req) => {
    const u = new URL(req.url());
    if (u.origin === origin && u.pathname !== "/api/events") set.add(req);
  });
  page.on("requestfinished", done);
  page.on("requestfailed", done);
}

/**
 * Opens a screen the way a reader moves around. The first one in a browser, or any with `reload`, is a full load.
 * Later ones click the app's own link to that address when one is on screen (a real router push), and otherwise
 * push a new history entry (a fresh key, the next index, as the router writes them) and announce it with popstate,
 * which the router follows. The heading check per screen (SCREENS) confirms the route really rendered. Requests
 * still counted from the screen before (a beacon that never reports back) are forgotten.
 */
async function go(page, path, { reload = false } = {}) {
  pending.get(page)?.clear();
  if (reload || new URL(page.url()).origin !== origin) {
    await page.goto(path);
    return;
  }
  // Escape backslashes before quotes, so a backslash in path can't unescape the quote that follows it.
  const cssEscaped = path.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
  const link = page.locator(`a[href="${cssEscaped}"]:visible`).first();
  if (await link.count().catch(() => 0)) {
    const clicked = await link
      .click({ timeout: 3000 })
      .then(() => true)
      .catch(() => false);
    if (clicked) return;
  }
  await page.evaluate(pushRoute, path);
}

// ---- the run ----
const browser = await ENGINES[engineName]
  .launch({ headless: !opt.headed })
  .catch((e) => setupError(`could not start ${engineName} (npx playwright install ${engineName}): ${firstLine(e)}`));
// Firefox drops a context's emulated color scheme when the document is served with Cross-Origin-Opener-Policy
// (Kipple sends same-origin): the navigation swaps the browsing context group and matchMedia goes back to light. The
// real engine follows the OS, so for Firefox the preference is set on the browser itself, one browser per theme.
const themeBrowsers = new Map();
async function browserFor(theme) {
  if (engineName !== "firefox") return browser;
  if (!themeBrowsers.has(theme.id)) {
    themeBrowsers.set(theme.id, await firefox.launch({ headless: !opt.headed, firefoxUserPrefs: { "ui.systemUsesDarkTheme": theme.colorScheme === "dark" ? 1 : 0 } }));
  }
  return themeBrowsers.get(theme.id);
}
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
  console.error(`run aborted: ${firstLine(e)}`);
  exitCode = 2;
} finally {
  for (const b of themeBrowsers.values()) await b.close().catch(() => {});
  await browser.close().catch((e) => console.warn(`closing the browser failed: ${firstLine(e)}`));
}
// Set rather than process.exit(), so piped output is flushed before Node leaves.
process.exitCode = exitCode;

// Each probe must flag a page built to fail it, so a quiet run means clean, not broken.
async function selfTest() {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.setContent(
    `<main><p>Count: NaN</p><p>Updated NaNm ago</p><p>Invalid Date</p><button aria-label="[object Object]">x</button>` +
      `<label>Night starts <input value="undefined"></label><div id="wide" style="width:600px">wide</div>` +
      `<div id="reader" style="overflow-y:auto;height:120px"><article aria-labelledby="article-title">` +
      `<h1 id="article-title">NaN-boxing explained</h1><p>Tue · NaN min read</p><div class="article-body">` +
      `<p>undefined behaviour is fine in an article</p><p><code>NaN</code></p><div style="width:800px">a wide embed</div>` +
      `<button id="feedbtn"></button><figure><button class="kp-embed-play" id="kpbtn"></button></figure></div>` +
      `</article></div>` +
      `<div class="article-body"><p>Preview: NaN</p></div>` +
      `<div data-item-id="1"><h3><a href="#x" aria-label="Unread, undefined, Some feed">Some title</a></h3>` +
      `<p>An excerpt about NaN-boxing</p><time>NaNm</time></div>` +
      `<p>The <mark>NaN</mark> trick</p>` +
      `<div style="display:none"><span>NaN hidden</span></div>` +
      `<div id="pane" style="overflow-y:auto;height:100px"><div style="width:700px">too wide for its pane</div></div>` +
      `<div style="overflow-y:auto;height:40px"><div id="fixed" style="position:fixed;left:0;bottom:0;width:500px">fixed bar</div></div>` +
      `<div class="overflow-x-auto" style="overflow-x:auto"><div style="width:900px">a scroller on purpose</div></div>` +
      `<span style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)">sr only</span>` +
      `<p>Size NaNkB</p><label>Theme <select><option>Paper</option><option selected>undefined</option></select></label>` +
      `<nav><a href="#y">NaN Tech</a><button aria-label="Edit NaN Tech">e</button></nav>` +
      `<p>Updated undefinedm ago</p><a href="#z" aria-label="Unread, NaN-boxing explained, undefined">NaN-boxing explained</a>` +
      `<svg width="20" height="20" role="img" aria-label="chart"><rect width="20" height="20"><title>Invalid Date: 3 items</title></rect></svg>` +
      `<div style="overflow:hidden;width:200px"><div id="clipped" style="white-space:nowrap;width:300px">a row cut off at its box</div></div>` +
      `<div style="overflow:hidden;width:200px;height:30px;position:relative"><button id="swipe" style="position:absolute;left:220px">Star</button></div>` +
      `<div style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap;width:100px"><span>a long title that truncates</span></div>` +
      `<div style="overflow:hidden;width:300px"><div class="overflow-x-auto" style="overflow-x:auto;width:200px">` +
      `<div style="white-space:nowrap;width:400px">chips in a scroller</div></div></div>` +
      `<p>NaN Tech Weekly: undefined results</p></main>`,
  );
  await page.evaluate(PAGE_SCRIPT);
  const probeFeed = {
    ...FEED,
    names: ["NaN Tech", "NaN-boxing explained", "An excerpt about NaN-boxing", "The NaN trick", "NaN Tech Weekly: undefined results"],
  };
  const o = await page.evaluate(overflowProbe, probeFeed);
  const lit = await page.evaluate(literalProbe, probeFeed);
  const axe = await runAxe(page);
  const axeIds = axe.map((v) => v.id);
  await page.close();
  const problems = [];
  const same = (got, want) => JSON.stringify([...got].sort()) === JSON.stringify([...want].sort());
  const own = lit.filter((h) => !h.feed).map((h) => h.text);
  const feed = lit.filter((h) => h.feed).map((h) => h.text);
  const wantOwn = [
    "Count: NaN", "Updated NaNm ago", "Invalid Date", "[object Object]", "undefined", "Tue · NaN min read", "Preview: NaN",
    "Unread, undefined, Some feed", "NaNm", "Size NaNkB", "undefined", "Updated undefinedm ago",
    "Unread, NaN-boxing explained, undefined", "Invalid Date: 3 items",
  ];
  const wantFeed = [
    "NaN-boxing explained", "undefined behaviour is fine in an article", "NaN", "An excerpt about NaN-boxing", "NaN",
    "NaN Tech", "Edit NaN Tech", "NaN-boxing explained", "NaN Tech Weekly: undefined results",
  ];
  const buttons = axe.find((v) => v.id === "button-name")?.nodes ?? [];
  const nodeSplit = buttons.map((n) => `${n.target}:${n.feedContent}`);
  if (!same(nodeSplit, ["#feedbtn:true", "#kpbtn:false"])) problems.push(`S3 feed/Kipple split ${JSON.stringify(nodeSplit)}`);
  // Harvesting keeps real feed text and drops a bare literal, which would otherwise excuse every hit of it.
  harvestFeedText({ summary: { title: "Your top NaN feeds" }, items: [{ id: "1", author: "undefined", title: "Why NaN != NaN", url: "https://x/NaN" }] });
  if (!same(FEED.names, ["Why NaN != NaN"])) problems.push(`harvest ${JSON.stringify(FEED.names)}`);
  feedStrings.clear();
  FEED.names = [];
  if (!(o.scrollWidth > o.vw)) problems.push("S4 did not see the page scroll sideways");
  if (!same(o.offenders.map((x) => x.desc.split(" ")[0]), ["div#wide", "div#fixed"])) problems.push(`S4 offenders ${JSON.stringify(o.offenders)}`);
  if (!same(o.scrollers.map((x) => `${x.desc.split(" ")[0]}:${x.feed}`), ["div#pane:false", "div#reader:true"])) problems.push(`S4 scrollers ${JSON.stringify(o.scrollers)}`);
  if (!same(o.clipped.map((x) => x.desc.split(" ")[0]), ["div#clipped"])) problems.push(`S4 clipped ${JSON.stringify(o.clipped)}`);
  if (!same(own, wantOwn)) problems.push(`S5 hits ${JSON.stringify(own)}`);
  if (!same(feed, wantFeed)) problems.push(`S5 feed notes ${JSON.stringify(feed)}`);
  // The fixture has no <title> and no lang: two violations axe always reports.
  if (!axeIds.includes("document-title") || !axeIds.includes("html-has-lang")) problems.push(`S3 missed a known violation (${axeIds.join(", ")})`);
  if (problems.length) throw new Error(`self-test failed: ${problems.join("; ")}`);
}

// Feed-supplied strings that contain an S5 literal (FEED.names). Two sources: every /api/ JSON answer a screen
// loads (harvestFeedText, so items that arrive mid-run and titles only Stats still knows are covered), and, before
// the first screen, a head start (collectFeedText). Only the fields feeds and the
// user fill (titles, names, excerpts, authors, sources, search snippets), and only on a record of an item, feed,
// folder or saved search (an object with an id, item_id or feed_id): a summary or label the server composes itself
// has none, so its text stays Kipple's.
function harvestFeedText(json) {
  const bad = new RegExp(FEED.pattern);
  const badAll = new RegExp(FEED.pattern, "g");
  const walk = (v) => {
    if (Array.isArray(v)) return v.forEach(walk);
    if (!v || typeof v !== "object") return;
    const record = "id" in v || "item_id" in v || "feed_id" in v;
    for (const [k, x] of Object.entries(v)) {
      if (typeof x !== "string") {
        walk(x);
        continue;
      }
      if (!record || !FEED_KEYS.has(k)) continue;
      const s = (k === "snippet" ? decodeSnippet(x) : x).replace(/\s+/g, " ").trim();
      // A string that is little more than the literal ("undefined" as an author) would excuse every Kipple hit of
      // that literal on every screen, so it is not taken; it shows up as an S5 finding instead.
      if (bad.test(s) && s.replace(badAll, "").replace(/[\W_]/g, "").length >= 3) feedStrings.add(s);
    }
  };
  walk(json);
  FEED.names = [...feedStrings];
}

// The head start: the bootstrap and the first page of All and of the run's search (what the first screens show). The
// screens' own API answers bring the rest. A search the server finds too broad (422 on a big instance) is skipped.
async function collectFeedText(request) {
  const headers = { Accept: "application/json", "X-Kipple-Client": "web" };
  const get = async (path, required) => {
    const r = await request.get(origin + path, { headers });
    if (r.ok()) return harvestFeedText(await r.json());
    if (required) throw new Error(`GET ${path.split("?")[0]} answered ${r.status()}`);
  };
  await get("/api/bootstrap", true);
  await get(`/api/items?${new URLSearchParams({ view: "all", limit: "100" })}`, true);
  await get(`/api/items?${new URLSearchParams({ view: "all", q: SEARCH_Q, limit: "100" })}`, false);
}

function loadDevice() {
  try {
    const c = JSON.parse(readFileSync(deviceFile, "utf8"));
    return c && c.name === "kipple_device" && typeof c.value === "string" ? c : null;
  } catch {
    return null;
  }
}

// Signs in through the form once, with the remembered device cookie when there is one. Every context then reuses
// the session and device cookies: one device of the run's own, never one of the owner's.
async function signIn() {
  const ctx = await browser.newContext();
  try {
    const saved = loadDevice();
    if (saved) await ctx.addCookies([saved]).catch(() => {});
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
    await collectFeedText(page.request);

    const cookies = (await ctx.storageState()).cookies;
    if (!cookies.some((c) => c.name === "kipple_session")) throw new Error("signed in, but no kipple_session cookie");
    const device = cookies.find((c) => c.name === "kipple_device");
    if (!device) throw new Error("signed in, but the server issued no device cookie");
    mkdirSync(dirname(deviceFile), { recursive: true });
    writeFileSync(deviceFile, JSON.stringify(device));
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
        context = await (await browserFor(theme)).newContext({
          baseURL: origin,
          colorScheme: theme.colorScheme,
          viewport: { width: vp.width, height: vp.height },
          // Firefox has no mobile emulation (Playwright rejects isMobile there); it still gets the width and touch.
          isMobile: vp.mobile && engineName !== "firefox",
          hasTouch: vp.mobile,
          storageState: { cookies, origins: [] },
          // Keep the run self-contained: no service worker answering from its cache between screens.
          serviceWorkers: "block",
        });
        // axe-core in every document, over the DevTools protocol like page.evaluate (so Kipple's CSP does not block
        // it, and stays on for S1), once per browser instead of once per screen.
        await context.addInitScript({ content: PAGE_SCRIPT });
        const page = await context.newPage();
        trackRequests(page);
        await checkCombo(page, theme, vp, ctxInfo, results);
      } catch (e) {
        broken(combo, `could not open a browser at ${vp.id}/${theme.id}: ${firstLine(e)}`);
        console.log(`${theme.id} ${vp.id}: ERROR ${firstLine(e)}`);
      } finally {
        await context?.close().catch(() => {});
      }
    }
  }
  return results;
}

async function checkCombo(page, theme, vp, ctxInfo, results) {
  // Events are always collected; each screen reports what arrived while it was checked, and what arrives after (a
  // query that fails late, during the screenshot) is reported against it when the next screen starts.
  let prev = null;
  const bucket = { console: [], api: [], other: [] };
  page.on("pageerror", (err) => bucket.console.push({ kind: "pageerror", text: String(err.stack || err).slice(0, 600) }));
  page.on("console", (msg) => {
    if (msg.type() !== "error") return;
    const url = msg.location()?.url ?? "";
    // "Failed to load resource" is the browser echoing a failed request. Same-origin ones are reported from the
    // response below (S2 for /api/, a note otherwise); only other origins are known from this message alone.
    if (/^Failed to load resource/.test(msg.text())) {
      if (new URL(url || origin, origin).origin !== origin) bucket.other.push({ url, text: msg.text() });
      return;
    }
    bucket.console.push({ kind: "console", text: msg.text().slice(0, 600), url });
  });
  const harvesting = new Set();
  page.on("response", (res) => {
    const u = new URL(res.url());
    if (u.origin !== origin) return;
    if (res.ok() && u.pathname.startsWith("/api/") && (res.headers()["content-type"] ?? "").includes("json")) {
      const p = res
        .json()
        .then(harvestFeedText)
        .catch(() => {})
        .finally(() => harvesting.delete(p));
      harvesting.add(p);
    }
    if (res.status() < 400) return;
    if (u.pathname.startsWith("/api/")) bucket.api.push({ status: res.status(), method: res.request().method(), url: u.pathname + u.search });
    else bucket.other.push({ url: u.pathname + u.search, text: `HTTP ${res.status()}` });
  });
  page.on("requestfailed", (req) => {
    const u = new URL(req.url());
    const why = req.failure()?.errorText ?? "failed";
    // A navigation aborts whatever was in flight (the event stream, prefetches): that is not a failure.
    // Each engine words it its own way: ERR_ABORTED (Chromium), NS_BINDING_ABORTED (Firefox), "cancelled" (WebKit).
    if (/ERR_ABORTED|NS_BINDING_ABORTED|cancel+ed/i.test(why)) return;
    if (u.origin !== origin) return; // other origins: the console message above
    if (u.pathname.startsWith("/api/")) bucket.api.push({ status: 0, method: req.method(), url: u.pathname + u.search, error: why });
    else bucket.other.push({ url: u.pathname + u.search, text: why });
  });

  // S1 and S2 events collected for the current screen, reported even when the screen could not be finished (they
  // are often why).
  const flush = (where) => {
    for (const c of bucket.console) report("S1", where, c.kind, c.text, c.url);
    for (const a of bucket.api) report("S2", where, String(a.status), `${a.method} ${a.url} -> ${a.status || a.error}`);
    for (const o of bucket.other) note("S2", where, `non-API request failed: ${o.url} ${o.text}`);
    bucket.console.length = bucket.api.length = bucket.other.length = 0;
  };

  // An article to open: the first one in All. Looked for again in the next browser while none is found (the feeds may
  // still be fetching). When this is the browser's first load, it is where the app boots, so its errors are checked
  // too (as the "boot" screen).
  if (!ctxInfo.articlePath && screens.some((s) => s.id === "article")) {
    try {
      await go(page, "/l/all");
      await settle(page);
      const link = page.locator('main article a[href^="/i/"]').first();
      ctxInfo.articlePath = await link.getAttribute("href", { timeout: 5000 });
      ctxInfo.articleTitle = ((await link.textContent()) ?? "").trim().slice(0, 40);
    } catch {
      ctxInfo.articlePath = null;
    }
    flush({ screen: "boot", theme: theme.id, viewport: vp.id });
  }

  // A full load for the next screen: after a screen that failed (the app may be stuck on an error page that ignores
  // the router), or when the next screen has the same heading (Search after Search, the list layouts), where the
  // heading check could not tell that the navigation happened.
  let reloadNext = false;
  let prevHeading = null;
  for (const screen of screens) {
    const where = { screen: screen.id, theme: theme.id, viewport: vp.id };
    const path = typeof screen.path === "function" ? screen.path(ctxInfo) : screen.path;
    if (prev) flush(prev);
    process.stdout.write(`${theme.id.padEnd(5)} ${vp.id.padEnd(7)} ${screen.id.padEnd(15)} `);
    const row = { ...where, title: screen.title, path, checks: {} };
    results.push(row);
    if (!path) {
      row.ok = false;
      broken(where, "no article in All to open (let the feeds fetch first)");
      console.log("ERROR no article to open");
      continue;
    }
    // The heading first: after an in-app navigation the screen before stays up until the router has rendered the
    // new one, and waiting for "not busy" on the old screen would pass at once.
    const heading = typeof screen.heading === "function" ? screen.heading(ctxInfo, vp) : screen.heading;
    try {
      await go(page, path, { reload: reloadNext || heading === prevHeading });
      reloadNext = false;
      prevHeading = heading;
      const esc = escapeRe(heading);
      // The list headings are whole; the article's is the start of its title.
      const want = new RegExp(screen.id === "article" ? `^\\s*${esc}` : `^\\s*${esc}\\s*$`);
      await page
        .locator("h1")
        .filter({ hasText: want })
        .first()
        .waitFor({ state: "visible", timeout: 15000 })
        .catch(() => {
          throw new Error(`the screen did not render: no visible h1 "${heading}"`);
        });
      await settle(page);
      if (screen.layout) {
        await setLayout(page, screen.layout);
        await settle(page);
      }

      const scheme = await page.evaluate(activeScheme);
      if (scheme !== theme.scheme)
        throw new Error(`expected the ${theme.scheme} scheme, the page shows ${scheme} (does the account default to a fixed theme?)`);

      const s4 = [];
      if (vp.id !== "desktop") {
        const o = await page.evaluate(overflowProbe, FEED);
        if (o.scrollWidth > o.vw + 1) s4.push({ rule: "page-scroll-x", message: `page scrolls sideways: scrollWidth ${o.scrollWidth} > ${o.vw}` });
        for (const s of o.scrollers) {
          const message = `${s.desc} scrolls sideways (${s.scrollWidth} > ${s.clientWidth})`;
          s4.push({ rule: "scroller-x", message: s.feed ? `${message}, pushed wide by the article HTML only` : message, feed: s.feed });
        }
        for (const c of o.clipped) s4.push({ rule: "clipped", message: `${c.desc} is cut off at ${c.edge}px (it ends at ${c.right}px)` });
        for (const off of o.offenders) s4.push({ rule: "past-right-edge", message: `${off.desc} ends at ${off.right}px (viewport ${o.vw}px)` });
      }
      await Promise.all([...harvesting]); // this screen's API answers are in FEED.names
      const s5 = await page.evaluate(literalProbe, FEED);
      const axe = await runAxe(page);
      if (screen.font) {
        const s7 = await fontCheck(page, screen.font);
        if (s7) report("S7", where, "font-choice", s7);
      }
      await page.waitForTimeout(100); // late console errors from the last render

      flush(where);
      prev = where;
      for (const v of axe) {
        const own = v.nodes.filter((n) => !n.feedContent);
        const feed = v.nodes.filter((n) => n.feedContent);
        // One finding per element, so a waiver's `match` accepts that element and not every other one the rule flags.
        for (const n of own) report("S3", where, v.id, `${v.id} (${v.impact}): ${v.help}`, [n]);
        if (feed.length) note("S3", where, `${v.id} in the feed's article HTML: ${v.help}`, feed);
      }
      for (const s of s4) {
        if (s.feed) note("S4", where, s.message);
        else report("S4", where, s.rule, s.message);
      }
      for (const h of s5) {
        if (h.feed) note("S5", where, `feed text ${h.where}: "${h.text}"`);
        else report("S5", where, "literal", `${h.where}: "${h.text}"`);
      }

      const mine = findings.filter((f) => f.screen === screen.id && f.theme === theme.id && f.viewport === vp.id && f.severity === "fail");
      row.checks = Object.fromEntries(["S1", "S2", "S3", "S4", "S5", "S7"].map((c) => [c, mine.filter((f) => f.check === c).length]));
      row.ok = mine.length === 0;
      if (!row.ok || opt.screenshots) {
        // The screen is checked by now: a screenshot that fails (a page too tall to capture, a full disk) is a note.
        const file = `${screen.id}-${theme.id}-${vp.id}.png`;
        await page
          .screenshot({ path: join(outDir, file), fullPage: true })
          .then(() => (row.screenshot = file))
          .catch((e) => note("run", where, `no screenshot: ${firstLine(e)}`));
      }
      console.log(row.ok ? "ok" : Object.entries(row.checks).filter(([, n]) => n).map(([c, n]) => `${c}x${n}`).join(" "));
    } catch (e) {
      row.ok = false;
      row.error = firstLine(e);
      flush(where);
      prev = where;
      reloadNext = true;
      prevHeading = null;
      broken(where, `could not check the screen: ${row.error}`);
      console.log(`ERROR ${row.error}`);
      await page.screenshot({ path: join(outDir, `${screen.id}-${theme.id}-${vp.id}-error.png`), fullPage: true }).catch(() => {});
    }
  }
  // Whatever the last screen still had coming.
  await page.waitForTimeout(300);
  if (prev) flush(prev);
}

/**
 * S7: the reading-font select is on screen (Settings) or in the Aa menu (lists and articles), with every font: the
 * default, the 11 bundled ones and those on the device. Returns what is wrong, or null. The menu is closed again.
 */
async function fontCheck(page, where) {
  const MIN_FONTS = 12;
  let scope = page;
  if (where === "menu") {
    const aa = page.getByRole("button", { name: "Reading appearance" }).first();
    if (!(await aa.isVisible().catch(() => false))) return "no visible Aa (Reading appearance) button";
    await aa.click();
    scope = page.getByRole("dialog", { name: "Reading appearance" });
    if (!(await scope.waitFor({ state: "visible", timeout: 3000 }).then(() => true, () => false))) return "the Aa button did not open the reading menu";
  }
  const select = scope.getByRole("combobox", { name: "Reading font" });
  const n = await select.count();
  let problem = null;
  if (n !== 1) problem = `${n} "Reading font" selects ${where === "menu" ? "in the Aa menu" : "on the page"}, expected 1`;
  else if (!(await select.isVisible())) problem = `the "Reading font" select is not visible`;
  else {
    const options = await select.locator("option").count();
    if (options < MIN_FONTS) problem = `the "Reading font" select offers ${options} fonts, expected at least ${MIN_FONTS}`;
  }
  if (where === "menu") await page.keyboard.press("Escape");
  return problem;
}

// S6: the theme contrast check over every scheme.
function contrastCheck() {
  const r = spawnSync(process.execPath, [join(webDir, "scripts", "contrast.mjs")], { cwd: webDir, encoding: "utf8" });
  const s6 = { ok: r.status === 0, output: `${r.stdout ?? ""}${r.stderr ?? ""}${r.error ? String(r.error) : ""}`.trim() };
  if (!s6.ok) {
    // One finding per failed threshold (the lines after "FAILURES:"), so a waiver accepts one scheme's one miss.
    const lines = (r.stderr ?? "").split(/\r?\n/);
    const at = lines.findIndex((l) => l.trim() === "FAILURES:");
    const misses = at >= 0 ? lines.slice(at + 1).map((l) => l.trim()).filter(Boolean) : [];
    if (misses.length) for (const m of misses) report("S6", {}, "contrast", m);
    else report("S6", {}, "contrast", "theme contrast check failed", s6.output);
  }
  console.log(`\nS6 theme contrast (all schemes): ${s6.ok ? "ok" : "FAILED"}\n${s6.output}`);
  return s6;
}

function writeReport(results, s6) {
  // Unused only means something for a waiver whose screen ran (and, for S1 to S5 waivers with no screen, only on a
  // full run): with --only, the rest simply were not exercised.
  for (const [i, w] of waivers.entries()) {
    if (used.has(i)) continue;
    const exercised = w.screen ? screens.some((s) => s.id === w.screen) : w.check === "S6" || !only;
    if (exercised) note("waivers", {}, `unused waiver: ${JSON.stringify(w)}`);
  }
  const fails = findings.filter((f) => f.severity === "fail");
  const summary = Object.fromEntries(
    ["S1", "S2", "S3", "S4", "S5", "S6", "S7", "run"].map((c) => [
      c,
      { fail: fails.filter((f) => f.check === c).length, waived: findings.filter((f) => f.check === c && f.severity === "waived").length },
    ]),
  );
  writeFileSync(join(outDir, "report.json"), JSON.stringify({ origin, stamp, summary, results, findings, s6 }, null, 2));

  // Findings grouped by check, rule and message with its numbers taken out (S4 names pixel widths that differ per
  // viewport), so one issue seen on 30 screens reads as one entry listing where it was seen and each distinct element
  // (axe nodes) once, with the screens it was on.
  const groups = new Map();
  for (const f of findings) {
    const key = `${f.severity}|${f.check}|${f.rule ?? ""}|${f.message.replace(/\d+/g, "#")}`;
    if (!groups.has(key)) groups.set(key, { ...f, seen: [], nodes: new Map(), variants: new Set() });
    const g = groups.get(key);
    g.variants.add(f.message);
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
    `${engineName} ${browser.version()}, ${origin}, ${startedText}. ${checked} of ${screens.length * THEMES.length * VIEWPORTS.length} screen checks completed ` +
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
      md.push(`- **${g.check}** ${g.message}${g.variants.size > 1 ? ` (and ${g.variants.size - 1} more with other numbers)` : ""}${g.waiver ? ` (waived: ${g.waiver})` : ""}`);
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
