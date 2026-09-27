// UAT Suite 1 (docs/uat-plan.md): a scripted pass over every screen of a running Kipple.
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:7080)
//   npm run uat                        in another; exits 1 when any check fails
//   npm run uat -- --url http://127.0.0.1:7091 --screenshots --headed
//
// Checks, per screen, theme (Paper and Midnight, through the browser's light and dark preference, since the default
// theme follows the device) and width (desktop 1280, small tablet 768, phone 390):
//   S1  no uncaught errors or console errors
//   S2  no 4xx/5xx or failed requests to /api/*
//   S3  axe-core, WCAG 2.0/2.1/2.2 A and AA rules, with waivers from uat/waivers.json
//   S4  no horizontal page scroll and nothing cut off at the right edge (tablet and phone widths)
//   S5  no literal "undefined", "NaN" or "[object Object]" in visible text or accessible names
//   S6  scripts/contrast.mjs over all 20 schemes
//
// It signs in, switches the list layout, opens articles (which marks them read and records reading stats) and
// restores the layout it found at the end. That changes the instance, so it only runs against a loopback address
// unless --allow-remote is given: point it at a throwaway instance, never at the one you read on.
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

const require = createRequire(import.meta.url);
const webDir = fileURLToPath(new URL("../", import.meta.url));
const AXE_SOURCE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");

const { values: opt } = parseArgs({
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
});
if (opt.help) {
  // The comment block at the top of this file is the usage text.
  const lines = readFileSync(fileURLToPath(import.meta.url), "utf8").split("\n");
  console.log(lines.slice(0, lines.findIndex((l) => !l.startsWith("//"))).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}

const base = new URL(opt.url);
const LOOPBACK = new Set(["127.0.0.1", "[::1]", "localhost"]);
if (!LOOPBACK.has(base.hostname) && !opt["allow-remote"]) {
  console.error(`refusing to run against ${base.origin}: the run marks articles read and changes the layout.`);
  console.error("Use a throwaway local instance (npm run seed), or pass --allow-remote if you really mean it.");
  process.exit(2);
}
const origin = base.origin;
const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
const outDir = opt.out ?? join(webDir, "uat", "results", stamp);
mkdirSync(outDir, { recursive: true });

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
// Every screen. `layout` switches the list layout first; `path` may be a function of the run's context.
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
const only = opt.only ? new Set(opt.only.split(",").map((s) => s.trim())) : null;
const screens = only ? SCREENS.filter((s) => only.has(s.id)) : SCREENS;
if (only) for (const id of only) if (!SCREENS.some((s) => s.id === id)) console.warn(`unknown screen id: ${id}`);

// Waivers: [{ check: "S3", rule: "color-contrast", screen?: "stats", theme?: "dark", viewport?: "phone", reason }].
// Every waiver needs a reason; an unused waiver is reported so the list does not rot.
const waivers = JSON.parse(readFileSync(new URL("./waivers.json", import.meta.url), "utf8"));
for (const w of waivers) if (!w.reason) throw new Error(`waiver without a reason: ${JSON.stringify(w)}`);
const used = new Set();
function waived(check, rule, where) {
  const i = waivers.findIndex(
    (w) =>
      w.check === check &&
      (w.rule === undefined || w.rule === rule) &&
      (w.screen === undefined || w.screen === where.screen) &&
      (w.theme === undefined || w.theme === where.theme) &&
      (w.viewport === undefined || w.viewport === where.viewport),
  );
  if (i >= 0) used.add(i);
  return i >= 0 ? waivers[i] : null;
}

const findings = []; // { check, severity: "fail" | "waived" | "note", screen, theme, viewport, rule?, message, detail? }
function report(check, where, rule, message, detail) {
  const w = waived(check, rule, where);
  findings.push({ check, severity: w ? "waived" : "fail", ...where, rule, message, detail, waiver: w?.reason });
}
function note(check, where, message, detail) {
  findings.push({ check, severity: "note", ...where, message, detail });
}

const browser = await chromium.launch({ headless: !opt.headed });

// ---- sign in once; every context reuses the session cookie ----
async function signIn() {
  const ctx = await browser.newContext();
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
  const layout = await currentLayout(page);
  const state = await ctx.storageState();
  await ctx.close();
  // Only the session cookie: each context starts as a fresh device, as a new browser would.
  return { cookies: state.cookies, origins: [], layout };
}

async function currentLayout(page) {
  await page.goto(origin + "/l/all", { waitUntil: "load" });
  const btn = page.locator('button[aria-label^="Layout: "]').first();
  await btn.waitFor({ timeout: 15000 });
  const label = (await btn.getAttribute("aria-label")).slice("Layout: ".length);
  return LAYOUTS.find((l) => l.label === label) ?? LAYOUTS[0];
}

async function setLayout(page, layout) {
  const btn = page.locator('button[aria-label^="Layout: "]').first();
  await btn.waitFor({ timeout: 15000 });
  if ((await btn.getAttribute("aria-label")) === `Layout: ${layout.label}`) return;
  await btn.click();
  // The radio's name is the label plus its hint; anchor it so "Compact" does not match "Email - Compact".
  const esc = layout.label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  await page.getByRole("menuitemradio", { name: new RegExp(`^${esc}\\b`) }).click();
  await page.locator(`button[aria-label="Layout: ${layout.label}"]`).first().waitFor({ timeout: 10000 });
  await page.keyboard.press("Escape");
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
    .catch(() => {});
  await page.waitForTimeout(700);
}

// ---- in-page checks ----

// S4: page-level horizontal scroll, and visible elements whose box runs past the right edge of the viewport and
// is not inside something that scrolls sideways on purpose.
function overflowProbe() {
  const vw = document.documentElement.clientWidth;
  const out = { scrollWidth: document.documentElement.scrollWidth, bodyScrollWidth: document.body.scrollWidth, vw, offenders: [] };
  const describe = (el) => {
    const id = el.id ? `#${el.id}` : "";
    const cls = typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
    const name = el.getAttribute("aria-label") || (el.textContent || "").trim().slice(0, 60);
    return `${el.tagName.toLowerCase()}${id}${cls}${name ? ` "${name}"` : ""}`;
  };
  const scrollsX = (el) => {
    for (let a = el.parentElement; a; a = a.parentElement) {
      const ox = getComputedStyle(a).overflowX;
      if (ox === "auto" || ox === "scroll") return true;
    }
    return false;
  };
  const hiddenByClip = (el) => {
    for (let a = el; a; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.display === "none" || cs.visibility === "hidden" || cs.opacity === "0") return true;
      if (cs.clip === "rect(0px, 0px, 0px, 0px)" || cs.clipPath === "inset(50%)") return true;
      if (a !== el && (cs.overflowX === "hidden" || cs.overflowX === "clip")) {
        // Clipped by an ancestor that itself fits: not visible past the edge.
        const r = a.getBoundingClientRect();
        if (r.right <= vw + 1) return true;
      }
    }
    return false;
  };
  for (const el of document.body.querySelectorAll("*")) {
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0 || r.right <= vw + 1) continue;
    if (r.left >= vw) continue; // entirely off screen: a closed drawer or an off-canvas panel
    if (scrollsX(el) || hiddenByClip(el)) continue;
    // Report the outermost offender only.
    if (out.offenders.some((o) => o.el.contains(el))) continue;
    out.offenders.push({ el, desc: describe(el), right: Math.round(r.right) });
    if (out.offenders.length >= 10) break;
  }
  out.offenders = out.offenders.map(({ desc, right }) => ({ desc, right }));
  return out;
}

// S5: text nodes and accessible-name attributes outside the article body (which is the feed's own HTML).
function literalProbe() {
  const bad = /\bundefined\b|\bNaN\b|\[object Object\]/;
  const hits = [];
  const skip = (el) => !!el?.closest(".article-body, script, style, noscript, template");
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const t = n.nodeValue;
    if (!t || !bad.test(t) || skip(n.parentElement)) continue;
    const el = n.parentElement;
    const cs = el && getComputedStyle(el);
    if (cs && (cs.display === "none" || cs.visibility === "hidden")) continue;
    hits.push({ where: el ? el.tagName.toLowerCase() : "?", text: t.trim().slice(0, 160) });
  }
  for (const el of document.body.querySelectorAll("[aria-label],[title],[alt],[placeholder],[aria-valuetext]")) {
    if (skip(el)) continue;
    for (const a of ["aria-label", "title", "alt", "placeholder", "aria-valuetext"]) {
      const v = el.getAttribute(a);
      if (v && bad.test(v)) hits.push({ where: `${el.tagName.toLowerCase()}[${a}]`, text: v.slice(0, 160) });
    }
  }
  if (bad.test(document.title)) hits.push({ where: "title", text: document.title });
  return hits.slice(0, 20);
}

async function runAxe(page) {
  // Evaluated over the DevTools protocol rather than added as a <script>: Kipple's CSP (script-src 'self') would
  // block an inline script, and turning the CSP off would hide the app's own CSP violations from S1.
  if (!(await page.evaluate(() => typeof window.axe !== "undefined"))) await page.evaluate(AXE_SOURCE);
  return page.evaluate(async () => {
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
        feedContent: !!document.querySelector(n.target.join(" "))?.closest(".article-body"),
      })),
    }));
  });
}

// ---- self-test: each probe must flag a page built to fail it, so a quiet run means clean, not broken ----
{
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.setContent(
    `<main><p>Count: NaN</p><button aria-label="[object Object]">x</button><div id="wide" style="width:600px">wide</div>` +
      `<div class="article-body"><p>undefined behaviour is fine in an article</p></div></main>` +
      `<div style="overflow-x:auto"><div style="width:900px">scroller</div></div>` +
      `<span style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)">sr only</span>`,
  );
  const o = await page.evaluate(overflowProbe);
  const lit = await page.evaluate(literalProbe);
  const axeIds = (await runAxe(page)).map((v) => v.id);
  await page.close();
  const problems = [];
  if (!(o.scrollWidth > o.vw)) problems.push("S4 did not see the page scroll sideways");
  if (o.offenders.length !== 1 || !o.offenders[0].desc.startsWith("div#wide")) problems.push(`S4 offenders ${JSON.stringify(o.offenders)}`);
  if (lit.length !== 2 || lit.some((h) => h.text.includes("behaviour"))) problems.push(`S5 hits ${JSON.stringify(lit)}`);
  // The fixture has no <title> and no lang: two violations axe always reports.
  if (!axeIds.includes("document-title") || !axeIds.includes("html-has-lang")) problems.push(`S3 missed a known violation (${axeIds.join(", ")})`);
  if (problems.length) {
    console.error(`self-test failed:\n  ${problems.join("\n  ")}`);
    await browser.close();
    process.exit(2);
  }
}

// ---- the run ----
const ctxInfo = {};
let session;
try {
  session = await signIn();
} catch (e) {
  console.error(`setup failed: ${e.message}`);
  await browser.close();
  process.exit(2);
}
console.log(`Kipple at ${origin}, layout on entry: ${session.layout.label}. Report: ${relative(process.cwd(), outDir) || "."}`);

const results = []; // one row per screen x theme x viewport
for (const theme of THEMES) {
  for (const vp of VIEWPORTS) {
    const context = await browser.newContext({
      baseURL: origin,
      colorScheme: theme.colorScheme,
      viewport: { width: vp.width, height: vp.height },
      isMobile: vp.mobile,
      hasTouch: vp.mobile,
      storageState: { cookies: session.cookies, origins: [] },
      // Keep the run self-contained: no service worker answering from its cache between screens.
      serviceWorkers: "block",
    });
    const page = await context.newPage();
    let current = null; // the screen being checked, for the event handlers below
    const bucket = { console: [], api: [], other: [] };
    page.on("pageerror", (err) => current && bucket.console.push({ kind: "pageerror", text: String(err.stack || err).slice(0, 600) }));
    page.on("console", (msg) => {
      if (!current || msg.type() !== "error") return;
      const url = msg.location()?.url ?? "";
      // "Failed to load resource" is the browser echoing a failed request; S2 reports /api/ ones from the response.
      if (/^Failed to load resource/.test(msg.text())) {
        if (!new URL(url || origin, origin).pathname.startsWith("/api/")) bucket.other.push({ url, text: msg.text() });
        return;
      }
      bucket.console.push({ kind: "console", text: msg.text().slice(0, 600), url });
    });
    page.on("response", (res) => {
      if (!current) return;
      const u = new URL(res.url());
      if (u.origin === origin && u.pathname.startsWith("/api/") && res.status() >= 400)
        bucket.api.push({ status: res.status(), method: res.request().method(), url: u.pathname + u.search });
      else if (u.origin === origin && res.status() >= 400) bucket.other.push({ url: u.pathname + u.search, text: `HTTP ${res.status()}` });
    });
    page.on("requestfailed", (req) => {
      if (!current) return;
      const u = new URL(req.url());
      const why = req.failure()?.errorText ?? "failed";
      // A navigation aborts whatever was in flight (the event stream, prefetches): that is not a failure.
      if (why.includes("ERR_ABORTED")) return;
      if (u.origin === origin && u.pathname.startsWith("/api/")) bucket.api.push({ status: 0, method: req.method(), url: u.pathname + u.search, error: why });
    });

    // An article to open: the first one in All.
    if (!ctxInfo.articlePath) {
      await page.goto("/l/all");
      await settle(page);
      const href = await page.locator('main article a[href^="/i/"]').first().getAttribute("href").catch(() => null);
      ctxInfo.articlePath = href;
      if (!href) console.warn("no articles in All yet: the article screen is skipped (let the feeds fetch first)");
    }

    for (const screen of screens) {
      const where = { screen: screen.id, theme: theme.id, viewport: vp.id };
      const path = typeof screen.path === "function" ? screen.path(ctxInfo) : screen.path;
      if (!path) continue;
      const row = { ...where, title: screen.title, path, checks: {} };
      results.push(row);
      process.stdout.write(`${theme.id.padEnd(5)} ${vp.id.padEnd(7)} ${screen.id.padEnd(15)} `);
      try {
        if (screen.layout) {
          await page.goto("/l/all");
          await settle(page);
          await setLayout(page, screen.layout);
        }
        bucket.console.length = bucket.api.length = bucket.other.length = 0;
        current = screen.id;
        await page.goto(path);
        await settle(page);

        const scheme = await page.evaluate(() => document.documentElement.dataset.theme);
        if (scheme !== theme.scheme) note("setup", where, `expected scheme ${theme.scheme}, page shows ${scheme}`);

        // S4 before axe, so axe's injected script is not part of the layout.
        let s4 = [];
        if (vp.id !== "desktop") {
          const o = await page.evaluate(overflowProbe);
          if (o.scrollWidth > o.vw + 1)
            s4.push({ rule: "page-scroll-x", message: `page scrolls sideways: scrollWidth ${o.scrollWidth} > ${o.vw}` });
          for (const off of o.offenders) s4.push({ rule: "past-right-edge", message: `${off.desc} ends at ${off.right}px (viewport ${o.vw}px)` });
        }
        const s5 = await page.evaluate(literalProbe);
        const axe = await runAxe(page);
        await page.waitForTimeout(200); // late console errors from the last render
        current = null;

        for (const c of bucket.console) report("S1", where, c.kind, c.text, c.url);
        for (const a of bucket.api) report("S2", where, String(a.status), `${a.method} ${a.url} -> ${a.status || a.error}`);
        for (const o of bucket.other) note("S2", where, `non-API request failed: ${o.url} ${o.text}`);
        for (const v of axe) {
          const own = v.nodes.filter((n) => !n.feedContent);
          const feed = v.nodes.filter((n) => n.feedContent);
          if (own.length) report("S3", where, v.id, `${v.id} (${v.impact}): ${v.help}`, own);
          if (feed.length) note("S3", where, `${v.id} in the feed's article HTML: ${v.help}`, feed);
        }
        for (const s of s4) report("S4", where, s.rule, s.message);
        for (const h of s5) report("S5", where, "literal", `${h.where}: "${h.text}"`);

        const mine = findings.filter((f) => f.screen === screen.id && f.theme === theme.id && f.viewport === vp.id && f.severity === "fail");
        row.checks = Object.fromEntries(["S1", "S2", "S3", "S4", "S5"].map((c) => [c, mine.filter((f) => f.check === c).length]));
        row.ok = mine.length === 0;
        if (!row.ok || opt.screenshots) {
          const file = `${screen.id}-${theme.id}-${vp.id}.png`;
          await page.screenshot({ path: join(outDir, file), fullPage: true });
          row.screenshot = file;
        }
        console.log(row.ok ? "ok" : Object.entries(row.checks).filter(([, n]) => n).map(([c, n]) => `${c}x${n}`).join(" "));
      } catch (e) {
        current = null;
        row.ok = false;
        row.error = e.message.split("\n")[0];
        findings.push({ check: "run", severity: "fail", ...where, message: `could not check the screen: ${row.error}` });
        console.log(`ERROR ${row.error}`);
        await page.screenshot({ path: join(outDir, `${screen.id}-${theme.id}-${vp.id}-error.png`), fullPage: true }).catch(() => {});
      }
    }
    await context.close();
  }
}

// Put the layout back the way the run found it.
try {
  const context = await browser.newContext({ baseURL: origin, storageState: { cookies: session.cookies, origins: [] }, serviceWorkers: "block" });
  const page = await context.newPage();
  await page.goto("/l/all");
  await settle(page);
  await setLayout(page, session.layout);
  await page.waitForTimeout(1500); // device-profile sync is debounced
  await context.close();
} catch (e) {
  console.warn(`could not restore the ${session.layout.label} layout: ${e.message}`);
}
await browser.close();

// ---- S6: the theme contrast check over every scheme ----
const contrast = spawnSync(process.execPath, [join(webDir, "scripts", "contrast.mjs")], { cwd: webDir, encoding: "utf8" });
const s6 = { ok: contrast.status === 0, output: (contrast.stdout + contrast.stderr).trim() };
if (!s6.ok) findings.push({ check: "S6", severity: "fail", message: "theme contrast check failed", detail: s6.output });
console.log(`\nS6 theme contrast (all schemes): ${s6.ok ? "ok" : "FAILED"}\n${s6.output}`);

for (const [i, w] of waivers.entries()) if (!used.has(i)) findings.push({ check: "waivers", severity: "note", message: `unused waiver: ${JSON.stringify(w)}` });

// ---- report ----
const fails = findings.filter((f) => f.severity === "fail");
const summary = Object.fromEntries(
  ["S1", "S2", "S3", "S4", "S5", "S6", "run"].map((c) => [c, { fail: fails.filter((f) => f.check === c).length, waived: findings.filter((f) => f.check === c && f.severity === "waived").length }]),
);
writeFileSync(join(outDir, "report.json"), JSON.stringify({ origin, stamp, summary, results, findings, s6 }, null, 2));

// Findings grouped by check and message, so one issue seen on 30 screens reads as one entry listing where it was
// seen and each distinct element (axe nodes) once, with the screens it was on.
const groups = new Map();
for (const f of findings) {
  const key = `${f.severity}|${f.check}|${f.rule ?? ""}|${f.message}`;
  if (!groups.has(key)) groups.set(key, { ...f, seen: [], nodes: new Map() });
  const g = groups.get(key);
  const at = f.screen ? `${f.screen}/${f.theme}/${f.viewport}` : null;
  if (at) g.seen.push(at);
  if (Array.isArray(f.detail)) {
    for (const n of f.detail) {
      if (!g.nodes.has(n.target)) g.nodes.set(n.target, { ...n, on: new Set() });
      if (f.screen) g.nodes.get(n.target).on.add(f.screen);
    }
  }
}
const md = [
  `# UAT Suite 1 report`,
  ``,
  `${origin}, ${stamp}. ${results.length} screen checks (${screens.length} screens x ${THEMES.length} themes x ${VIEWPORTS.length} widths).`,
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
process.exit(fails.length ? 1 : 0);
