// UAT Suite 1 addition (docs/maintainers/uat-plan.md): the setup wizard, end to end, in a real browser.
//
//   npm run build && node uat/wizard.mjs [--headed] [--screenshots <dir>] [--bin <path to a kipple binary>]
//
// It builds Kipple (go build; needs Go on PATH) unless --bin is given, then starts four THROWAWAY servers on 127.0.0.1
// with fresh data directories under the system temp directory and NO account, so each starts in setup mode. It never
// touches any other instance:
//
//   Run A (desktop 1280x800, browser time zone Asia/Tokyo): opens the address, which shows the account form first
//     (no setup code), and walks both steps with a password. Asserts the feeds step starts with nothing ticked and
//     says Continue, that Import a feeds file opens the import dialog, that adding a feed ends setup, that
//     GET /api/settings then reports the browser's zone, that while unclaimed the other API routes refuse, that a
//     second browser context creating the account afterwards gets 404, and that after completion /api/setup/* answers 404.
//   Run B (the narrowest phone, 320x568): the same wizard in open mode (no password), ending with Continue and nothing
//     added, then a fresh browser context signs in by itself (POST /api/auth/open), and a request that carries a
//     forwarding header is refused with the plain-English screen.
//   Run C (phone, time zone Asia/Tokyo): GET /api/instance unreachable (a Try again screen, never a password form), then
//     Continue on the feeds step, which must save the browser's zone.
//   Run D (a phone and a desktop browser on one fresh server): a feeds file sent to the restore screen is refused in
//     words. The phone then uploads a backup made in Run A and leaves it unconfirmed, as an abandoned tab would. The
//     desktop browser's upload must then be accepted at once (it replaces the phone's), the phone's "Restore this backup"
//     must say another browser has the upload and go back to the picker, and the desktop's confirm must start the restore.
//
// axe-core (WCAG 2.0/2.1/2.2 A and AA) runs on every step at both sizes. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium`.
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium } from "@playwright/test";
import { installPageHelpers, tapProbe } from "./probes.mjs";
import { onPageError } from "./common.mjs";

const require = createRequire(import.meta.url);
const webDir = fileURLToPath(new URL("../", import.meta.url));
const root = fileURLToPath(new URL("../../", import.meta.url));
const { values: opt } = parseArgs({ options: { headed: { type: "boolean", default: false }, screenshots: { type: "string" }, bin: { type: "string" }, help: { type: "boolean", default: false } } });
if (opt.help) {
  console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/).slice(0, 26).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const findings = [];
const fail = (where, what) => {
  findings.push(`${where}: ${what}`);
  console.error(`FAIL ${where}: ${what}`);
};
const check = (where, ok, what) => {
  if (!ok) fail(where, what);
  return ok;
};
const setupError = (m) => {
  console.error(m);
  process.exit(2);
};

if (!existsSync(join(webDir, "dist", "index.html"))) setupError("web/dist is empty: run `npm run build` first (the server embeds it).");
const tmp = mkdtempSync(join(tmpdir(), "kipple-wizard-uat-"));
let bin = opt.bin;
if (!bin) {
  bin = join(tmp, process.platform === "win32" ? "kipple.exe" : "kipple");
  console.log("building Kipple ...");
  // A release version, so the server behaves like a published one (a development build records no "What's new" state).
  const b = spawnSync("go", ["build", "-ldflags", `-X main.version=${process.env.VERSION?.trim() || "v0.0.0-uat"}`, "-o", bin, "./cmd/kipple"], { cwd: root, stdio: "inherit" });
  if (b.status !== 0) setupError("go build failed");
}
if (opt.screenshots) mkdirSync(opt.screenshots, { recursive: true });

const servers = [];
/** A fresh server in setup mode. Resolves with its origin. */
async function startServer(name, port) {
  const dir = join(tmp, name);
  mkdirSync(dir, { recursive: true });
  const env = { ...process.env, KIPPLE_ADDR: `127.0.0.1:${port}`, KIPPLE_DATA: dir, KIPPLE_LOG_LEVEL: "warn" };
  for (const k of ["KIPPLE_USERNAME", "KIPPLE_PASSWORD", "KIPPLE_API_PASSWORD"]) delete env[k];
  const child = spawn(bin, ["serve"], { env, stdio: ["ignore", "ignore", "pipe"] });
  servers.push(child);
  let err = "";
  child.stderr.on("data", (c) => (err += c));
  const origin = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(`${origin}/healthz`)).ok) break;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 200));
    if (i === 99) setupError(`server ${name} did not start:\n${err}`);
  }
  return { origin };
}

async function axeRun(page, where) {
  if (!(await page.evaluate(() => typeof window.axe === "object"))) await page.evaluate(AXE);
  const res = await page.evaluate(() =>
    window.axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] } }).then((r) => r.violations.map((v) => ({ id: v.id, impact: v.impact, nodes: v.nodes.map((n) => n.target.join(" ")).slice(0, 3) }))),
  );
  for (const v of res) fail(`${where} axe`, `${v.id} (${v.impact}) at ${v.nodes.join(" | ")}`);
}

async function overflowRun(page, where) {
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  check(where, over <= 1, `the page scrolls sideways by ${over}px`);
}

/** Checks a step by its heading, runs axe and the sideways-scroll check, and takes the screenshot when asked. */
async function onStep(page, tag, heading, n) {
  const where = `${tag} step ${n}`;
  try {
    await page.getByRole("heading", { level: 1, name: heading }).waitFor({ timeout: 10_000 });
  } catch {
    fail(where, `no heading "${heading}"`);
    throw new Error("lost");
  }
  await page.waitForTimeout(250);
  check(where, await page.getByText(`Step ${n} of 2`).isVisible(), `no "Step ${n} of 2"`);
  await axeRun(page, where);
  await overflowRun(page, where);
  await tapRun(page, where);
  if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, `${tag}-step${n}.png`), fullPage: true });
}

/** S8 of run.mjs on a touch-sized page: every control has a 44 x 44 px hit area (tapProbe in probes.mjs). */
async function tapRun(page, where) {
  if ((page.viewportSize()?.width ?? 9999) >= 600) return;
  await page.evaluate(installPageHelpers);
  const { small } = await page.evaluate(tapProbe, { feed: { body: ".no-feed-html", own: ".no-feed-html" }, min: 44 });
  for (const t of small) fail(`${where} touch target`, `${t.desc} is ${t.w} x ${t.h} px, under 44 x 44`);
}

/** Run A's backup, uploaded in Run D. */
let backupZip = null;

/** Opens the restore screen of the wizard on page and sends the backup through its file picker. */
async function uploadBackup(page) {
  await page.getByRole("button", { name: "Restore from a backup" }).click();
  await page.getByLabel("Backup file").setInputFiles({ name: "kipple-backup.zip", mimeType: "application/zip", buffer: backupZip });
}

const feedsFile = { name: "feeds.opml", mimeType: "text/xml", buffer: Buffer.from('<?xml version="1.0"?><opml version="2.0"><body><outline text="Example" xmlUrl="https://example.org/feed.xml"/></body></opml>') };

/** The cards of "How do you want to sign in?" (RadioCard with children): 44 / 56 px tall, the radio beside the title, and a tap in the padding or on the description picks the card. */
async function radioCardChecks(page, where) {
  const open = page.getByRole("radio", { name: /^No password at all/ });
  const pw = page.getByRole("radio", { name: /^With a password/ });
  const card = open.locator("xpath=ancestor::label/..");
  // Open mode is offered on the loopback address this flow runs on; without it the cards below would go unmeasured.
  if (!check(where, await open.isEnabled(), '"No password at all" is disabled, so the radio cards were not measured')) return;
  for (const large of [false, true]) {
    await page.evaluate((on) => (on ? document.documentElement.setAttribute("data-targets", "large") : document.documentElement.removeAttribute("data-targets")), large);
    const tag = `${where} radio card ${large ? "large" : "normal"}`;
    const m = await open.evaluate((el) => {
      const label = el.closest("label");
      const c = label.parentElement.getBoundingClientRect();
      const l = label.getBoundingClientRect();
      const r = el.getBoundingClientRect();
      const t = label.querySelector("span > span").getBoundingClientRect();
      return { cw: c.width, lh: l.height, rw: r.width, rh: r.height, rMid: r.top + r.height / 2, tMid: t.top + t.height / 2, desc: CSS.escape(el.getAttribute("aria-describedby")) };
    });
    check(tag, m.lh >= (large ? 56 : 44) - 0.5, `the label row is ${m.lh.toFixed(1)} px tall`);
    check(tag, Math.round(m.rw) === 20 && Math.round(m.rh) === 20, `the radio is ${m.rw} x ${m.rh} px`);
    check(tag, Math.abs(m.rMid - m.tMid) <= 3, `the radio centre ${m.rMid.toFixed(1)} is off the title centre ${m.tMid.toFixed(1)}`); // 2 px low on a 24 px line (mt-1), as it always has
    await card.click({ position: { x: m.cw / 2, y: 4 } }); // the card's top padding, above the text
    check(tag, await open.isChecked(), "a tap in the card's padding did not pick it");
    await pw.check();
    await page.locator(`#${m.desc} p`).first().click(); // the description text
    check(tag, await open.isChecked(), "a tap on the card's description did not pick it");
    await pw.check();
  }
  await page.evaluate(() => document.documentElement.removeAttribute("data-targets"));
}

const browser = await chromium.launch({ headless: !opt.headed }).catch((e) => setupError(`could not start Chromium (npx playwright install chromium): ${e.message.split("\n")[0]}`));

try {
  // ---------------------------------------------------------------------------------------------- Run A
  {
    const { origin } = await startServer("a", 7191);
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, timezoneId: "Asia/Tokyo", colorScheme: "light" });
    const page = await ctx.newPage();
    const errors = [];
    onPageError(page, (e) => errors.push(String(e)));
    // A 401 is not waived: a signed-out launch asks /api/instance first and sends nothing that answers 401 (issue #465).
    page.on("console", (m) => m.type() === "error" && !/status of 40[34]|status of 429/.test(m.text()) && errors.push(m.text()));

    // While there is no account, the rest of the API refuses; a second browser looks on.
    const stranger = await browser.newContext();
    const sp = await stranger.newPage();
    await sp.goto(origin);
    const call = (p, path, body) =>
      p.evaluate(async ([u, b]) => (await fetch(u, { method: "POST", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) })).status, [path, body]);
    check("unclaimed", (await sp.evaluate(async () => (await fetch("/api/bootstrap")).status)) === 401, "/api/bootstrap answered without an account");
    check("unclaimed", (await call(sp, "/api/auth/login", { username: "a", password: "b" })) === 409, "sign-in did not say setup_required");

    // Step 1: the first screen is the account form.
    await page.goto(origin);
    await onStep(page, "A", "Create your account", 1);
    await radioCardChecks(page, "A step 1");
    await page.getByLabel("Username").fill("tester");
    await page.getByLabel("Password", { exact: true }).fill("a long enough password");
    await page.getByRole("button", { name: "Create my account" }).click();

    // Step 2: the feeds. Nothing is ticked, so the one button says Continue; the time zone is not asked for.
    await onStep(page, "A", "Choose your feeds", 2);
    // Steps are in the address: a reload lands on the same step.
    await page.reload();
    await onStep(page, "A", "Choose your feeds", 2);
    check("A step 2", (await page.getByRole("button", { name: "Continue", exact: true }).count()) === 1, "no Continue button");
    check("A step 2", (await page.locator('fieldset[data-category] input[type="checkbox"]:checked:not(:disabled)').count()) === 0, "a feed was ticked before anyone chose it");
    check("A step 2", (await page.getByText(/time zone/i).count()) === 0, "the wizard talks about a time zone");
    check("A step 2", (await page.getByRole("button", { name: /skip|back/i }).count()) === 0, "a Skip or Back button is still offered");
    // Import a feeds file: the dialog Settings uses, over this step.
    await page.getByRole("button", { name: "Import a feeds file" }).click();
    await page.getByRole("dialog").waitFor({ timeout: 5000 });
    await axeRun(page, "A step 2 (import dialog)");
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "A-step2-import.png"), fullPage: true });
    await page.getByRole("button", { name: "Cancel" }).click();
    // Ticking one feed changes the button to Add 1 feed; adding it ends setup.
    const first = page.locator('input[type="checkbox"]:not(:disabled)').first();
    if (check("A step 2", (await first.count()) === 1, "no recommended feed to tick (the list is empty)")) {
      await first.check();
      await page.getByRole("button", { name: /^Add 1 feed$/ }).waitFor({ timeout: 5000 }).catch(() => fail("A step 2", 'the button did not read "Add 1 feed"')); // uat-labels: ignore (the wizard Add N feeds button)
      if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "A-step2-ticked.png"), fullPage: true });
      await page.getByRole("button", { name: /^Add 1 feed$/ }).click(); // uat-labels: ignore (the wizard Add N feeds button)
    } else await page.getByRole("button", { name: "Continue", exact: true }).click();
    await page.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("A finish", `the reader did not open (at ${page.url()})`));
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "A-reader.png"), fullPage: true });

    // What the wizard wrote, and that setup routes are gone.
    const settings = await page.evaluate(async () => (await fetch("/api/settings")).json());
    check("A settings", settings.values?.tz === "Asia/Tokyo", `tz is ${settings.values?.tz}, expected Asia/Tokyo`);
    check("A setup routes", (await call(sp, "/api/setup/account", { username: "intruder", password: "intruder-password" })) === 404, "/api/setup/account still answers after setup");
    const me = await page.evaluate(async () => (await fetch("/api/auth/me")).json());
    check("A account", me.setup_pending === false, "setup is still pending after the feeds step");
    // A brand-new install is not an upgrade: the account was created at this version, so the next load (with the feeds
    // the wizard just added) shows no "What's new". The dialog check has teeth when web/dist was built with the same
    // VERSION as the server (a development bundle never shows it); the recorded version is checked either way.
    const about = await page.evaluate(async () => (await fetch("/api/about")).json());
    // A development build ("dev", or a bare commit id with --bin) records nothing.
    const wantSeen = /^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/.test(about.version) ? about.version : "";
    check("A whats-new", (settings.values?.["ui.whats_new_seen"] ?? "") === wantSeen, `ui.whats_new_seen is ${JSON.stringify(settings.values?.["ui.whats_new_seen"])}, expected ${JSON.stringify(wantSeen)}`);
    await page.reload();
    await page.getByRole("main").waitFor({ timeout: 10_000 });
    await page.waitForTimeout(1500);
    check("A whats-new", (await page.getByRole("dialog", { name: "What's new in Kipple" }).count()) === 0, "What's new appeared on the first load after setup");
    check("A account", errors.length === 0, `console or page errors: ${errors.join(" | ")}`);

    // Issue #465: now that the account exists, a signed-out launch of the sign-in page logs nothing and asks for nothing
    // that answers 4xx. Then a sign-in sets the session marker, and a signed-in launch asks for the bootstrap once,
    // with no GET /api/instance first.
    {
      const out = await browser.newContext({ viewport: { width: 390, height: 844 } });
      const op = await out.newPage();
      const strict = [];
      onPageError(op, (e) => strict.push(String(e)));
      op.on("console", (m) => m.type() === "error" && strict.push(m.text()));
      const api = [];
      op.on("response", (r) => {
        const u = new URL(r.url());
        if (u.pathname.startsWith("/api/")) api.push(`${r.status()} ${u.pathname}`);
      });
      await op.goto(origin);
      await op.getByRole("heading", { name: "Sign in to Kipple" }).waitFor({ timeout: 10_000 });
      await op.waitForTimeout(1000);
      check("A signed out", strict.length === 0, `console or page errors: ${strict.join(" | ")}`);
      check("A signed out", api.every((l) => !/^4/.test(l)), `a 4xx answer: ${api.join(", ")}`);
      check("A signed out", !api.some((l) => l.endsWith(" /api/bootstrap")), `the bootstrap was asked for: ${api.join(", ")}`);
      await op.getByLabel("Username").fill("tester");
      await op.getByLabel("Password", { exact: true }).fill("a long enough password");
      await op.getByRole("button", { name: "Sign in" }).click();
      await op.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("A sign-in", `the reader did not open (at ${op.url()})`));
      check("A sign-in", (await op.evaluate(() => document.cookie)).includes("kipple_signed_in=1"), "no session marker after sign-in");
      api.length = 0;
      await op.reload();
      await op.getByRole("main").waitFor({ timeout: 10_000 });
      await op.waitForTimeout(1000);
      check("A signed in", api.filter((l) => l.endsWith(" /api/bootstrap")).length === 1, `not one bootstrap request: ${api.join(", ")}`);
      check("A signed in", !api.some((l) => l.endsWith(" /api/instance")), `a signed-in launch asked /api/instance: ${api.join(", ")}`);
      check("A signed in", strict.length === 0, `console or page errors: ${strict.join(" | ")}`);
      await out.close();
    }

    // A backup of this library, for Run D, made the way the settings page makes one: POST /api/backup answers 200 with
    // the download, or 202 with a job to follow until it is ready. The export asks for the web password first.
    const made = await page.evaluate(async (current) => {
      const opts = { headers: { "X-Kipple-Client": "web" } };
      const post = { method: "POST", headers: { ...opts.headers, "Content-Type": "application/json" }, body: JSON.stringify({ current }) };
      let ans = await (await fetch("/api/backup", post)).json();
      for (let i = 0; ans.status === "building" && ans.job_id && i < 300; i++) {
        await new Promise((r) => setTimeout(r, 1000));
        ans = await (await fetch(`/api/backup/jobs/${ans.job_id}`, opts)).json();
      }
      if (!ans.url) return { error: JSON.stringify(ans) };
      const res = await fetch(ans.url);
      if (!res.ok) return { error: `the download answered ${res.status}` };
      const bytes = new Uint8Array(await res.arrayBuffer());
      let bin = "";
      for (const b of bytes) bin += String.fromCharCode(b);
      return { zip: btoa(bin) };
    }, "a long enough password");
    if (made.zip) backupZip = Buffer.from(made.zip, "base64");
    else fail("A backup", `making the backup for Run D failed: ${made.error}`);
    await ctx.close();
    await stranger.close();
  }

  // ---------------------------------------------------------------------------------------------- Run B
  {
    const { origin } = await startServer("b", 7192);
    const ctx = await browser.newContext({ viewport: { width: 320, height: 568 }, isMobile: true, hasTouch: true, colorScheme: "dark" });
    const page = await ctx.newPage();
    const errors = [];
    onPageError(page, (e) => errors.push(String(e)));
    await page.goto(origin);
    await onStep(page, "B", "Create your account", 1);
    await page.getByLabel("Username").fill("opener");
    await page.getByRole("radio", { name: /No password at all/ }).check();
    await page.getByText("Anyone who can reach this address can read and change everything.").waitFor();
    await axeRun(page, "B step 0 (open mode notice)");
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "B-step2-open.png"), fullPage: true });
    await page.getByRole("button", { name: "Create my account" }).click();
    check("B step 1", await page.getByText("Tick the box to confirm you understand.").isVisible(), "no acknowledgement error");
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByRole("button", { name: "Create my account" }).click();
    await onStep(page, "B", "Choose your feeds", 2);
    await page.getByRole("button", { name: "Continue", exact: true }).click();
    await page.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("B finish", `the reader did not open (at ${page.url()})`));
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "B-reader.png"), fullPage: true });
    const me = await page.evaluate(async () => (await fetch("/api/auth/me")).json());
    check("B account", me.auth_mode === "open", `auth_mode is ${me.auth_mode}`);
    check("B account", errors.length === 0, `page errors: ${errors.join(" | ")}`);

    // A fresh browser signs in by itself in open mode.
    const again = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true });
    const ap = await again.newPage();
    await ap.goto(origin);
    await ap.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("B open sign-in", `did not sign in by itself (at ${ap.url()})`));
    await again.close();

    // A request through a proxy or tunnel is refused, in plain words.
    const proxied = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true, extraHTTPHeaders: { "X-Forwarded-For": "203.0.113.9" } });
    const pp = await proxied.newPage();
    await pp.goto(origin);
    try {
      await pp.getByRole("heading", { level: 1, name: "Kipple can't let you in from here" }).waitFor({ timeout: 10_000 });
      check("B refused", await pp.getByText(/proxy or tunnel/).first().isVisible(), "the reason was not shown");
      await axeRun(pp, "B open refused");
      await overflowRun(pp, "B open refused");
      if (opt.screenshots) await pp.screenshot({ path: join(opt.screenshots, "B-open-refused.png"), fullPage: true });
    } catch {
      fail("B refused", "the refusal screen did not appear for a forwarded request");
    }
    await proxied.close();
    await ctx.close();
  }
  // ---------------------------------------------------------------------------------------------- Run C
  {
    // Server trouble on the first screen, and Continue on the feeds step.
    const { origin } = await startServer("c", 7193);
    const ctx = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true, hasTouch: true, timezoneId: "Asia/Tokyo" });
    const page = await ctx.newPage();
    const errors = [];
    onPageError(page, (e) => errors.push(String(e)));
    // GET /api/instance cannot be reached: the screen says so and offers Try again, and never a password form.
    await page.route("**/api/instance", (r) => r.abort());
    await page.goto(origin);
    try {
      await page.getByRole("heading", { name: /Kipple can't be reached|You're offline/ }).waitFor({ timeout: 10_000 });
      check("C offline", await page.getByRole("button", { name: "Try again" }).isVisible(), "no Try again button");
      check("C offline", (await page.locator('input[type="password"]').count()) + (await page.getByRole("textbox", { name: /password/i }).count()) === 0, "a password form was shown when the server could not be reached");
      await overflowRun(page, "C offline");
      if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "C-offline.png"), fullPage: true });
    } catch {
      fail("C offline", `no "can't be reached" screen when /api/instance was unreachable`);
    }
    await page.unroute("**/api/instance");
    await page.getByRole("button", { name: "Try again" }).click();
    await onStep(page, "C", "Create your account", 1);
    await page.getByLabel("Username").fill("skipper");
    await page.getByLabel("Password", { exact: true }).fill("a long enough password");
    await page.getByRole("button", { name: "Create my account" }).click();
    await onStep(page, "C", "Choose your feeds", 2);
    await page.getByRole("button", { name: "Continue", exact: true }).click();
    await page.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("C continue", `the reader did not open (at ${page.url()})`));
    const settings = await page.evaluate(async () => (await fetch("/api/settings")).json());
    check("C continue", settings.values?.tz === "Asia/Tokyo", `tz is ${settings.values?.tz} after Continue on the feeds step, expected Asia/Tokyo`);
    const me = await page.evaluate(async () => (await fetch("/api/auth/me")).json());
    check("C continue", me.setup_pending === false, "setup is still pending after Continue");
    check("C account", errors.length === 0, `page errors: ${errors.join(" | ")}`);
    await ctx.close();
  }
  // ---------------------------------------------------------------------------------------------- Run D
  if (!backupZip) throw new Error("lost"); // Run A said why
  {
    const { origin } = await startServer("d", 7194);
    const phone = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true, hasTouch: true });
    const desk = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const pp = await phone.newPage();
    const dp = await desk.newPage();
    const errors = [];
    for (const p of [pp, dp]) onPageError(p, (e) => errors.push(String(e)));

    // A feeds file is not a backup: the restore screen says where it goes and stays on the picker.
    await pp.goto(origin);
    await pp.getByRole("button", { name: "Restore from a backup" }).click();
    await onStep(pp, "D", "Restore from a backup", 1);
    await pp.getByLabel("Backup file").setInputFiles(feedsFile);
    try {
      await pp.getByRole("alert").waitFor({ timeout: 10_000 });
      check("D phone", /feeds file, not a Kipple backup/.test((await pp.getByRole("alert").textContent()) ?? ""), "a feeds file was not refused in words");
      if (opt.screenshots) await pp.screenshot({ path: join(opt.screenshots, "D-phone-feeds-file.png"), fullPage: true });
    } catch {
      fail("D phone", "no message after a feeds file was sent to the restore screen");
    }

    // The phone uploads a backup and leaves it unconfirmed.
    await pp.getByLabel("Backup file").setInputFiles({ name: "kipple-backup.zip", mimeType: "application/zip", buffer: backupZip });
    await pp.getByTestId("backup-summary").waitFor({ timeout: 30_000 });
    if (opt.screenshots) await pp.screenshot({ path: join(opt.screenshots, "D-phone-ready.png"), fullPage: true });

    // The desktop browser is not kept out: its upload replaces the phone's.
    await dp.goto(origin);
    await uploadBackup(dp);
    try {
      await dp.getByTestId("backup-summary").waitFor({ timeout: 30_000 });
    } catch {
      fail("D desktop", `the second browser's upload was not accepted: ${(await dp.getByRole("alert").allTextContents()).join(" | ")}`);
      throw new Error("lost");
    }
    await axeRun(dp, "D desktop review");
    await overflowRun(dp, "D desktop review");

    // The phone's upload is gone: its confirm says so and the page goes back to the picker.
    await pp.getByRole("button", { name: "Restore this backup" }).click();
    try {
      await pp.getByRole("alert").waitFor({ timeout: 10_000 });
      check("D phone", /Another browser uploaded a backup, which replaced the one uploaded here/.test((await pp.getByRole("alert").textContent()) ?? ""), "the phone's confirm did not say another browser's upload replaced its own");
      check("D phone", await pp.getByLabel("Backup file").isVisible(), "the phone did not go back to the file picker");
      check("D phone", (await pp.getByTestId("backup-summary").count()) === 0, "the phone still shows the replaced backup");
      await axeRun(pp, "D phone replaced");
      await overflowRun(pp, "D phone replaced");
      await tapRun(pp, "D phone replaced");
      if (opt.screenshots) await pp.screenshot({ path: join(opt.screenshots, "D-phone-replaced.png"), fullPage: true });
    } catch {
      fail("D phone", "no message after the phone confirmed a replaced upload");
    }

    // The desktop browser restores.
    await dp.getByRole("button", { name: "Restore this backup" }).click();
    await dp
      .getByRole("heading", { name: "Restoring your library" })
      .waitFor({ timeout: 10_000 })
      .catch(() => fail("D desktop", "the second browser's confirm did not start the restore"));
    if (opt.screenshots) await dp.screenshot({ path: join(opt.screenshots, "D-desktop-restoring.png"), fullPage: true });
    check("D", errors.length === 0, `page errors: ${errors.join(" | ")}`);
    await phone.close();
    await desk.close();
  }
} catch (e) {
  if (e.message !== "lost") fail("run", e.stack ?? String(e));
} finally {
  await browser.close();
  for (const s of servers) s.kill();
  await new Promise((r) => setTimeout(r, 500));
  try {
    rmSync(tmp, { recursive: true, force: true });
  } catch {
    /* a server on Windows may still hold its files for a moment; it is a temp directory */
  }
}

if (findings.length) {
  console.error(`\n${findings.length} finding${findings.length === 1 ? "" : "s"}`);
  process.exit(1);
}
console.log("wizard UAT: clean");
