// UAT Suite 1 addition (docs/maintainers/uat-plan.md): the setup wizard, end to end, in a real browser.
//
//   npm run build && node uat/wizard.mjs [--headed] [--screenshots <dir>] [--bin <path to a kipple binary>]
//
// It builds Kipple (go build; needs Go on PATH) unless --bin is given, then starts three THROWAWAY servers on 127.0.0.1
// with fresh data directories under the system temp directory and NO account, so each starts in setup mode. It never
// touches any other instance:
//
//   Run A (desktop 1280x800, browser time zone Asia/Tokyo): opens the address, which shows the account form first
//     (no setup code), and walks all seven steps with a password. Asserts the time zone step preselects Asia/Tokyo and
//     that GET /api/settings then reports it, that while unclaimed the other API routes refuse, that a second browser
//     context creating the account afterwards gets 404, and that after completion /api/setup/* answers 404.
//   Run B (phone 375x812): the same wizard in open mode (no password), a theme and reading-font preview + Skip that
//     must leave no theme or font overrides in the device profile, then a fresh browser context signs in by itself
//     (POST /api/auth/open), and a request that carries a forwarding header is refused with the plain-English screen.
//   Run C (phone, time zone Asia/Tokyo): GET /api/instance unreachable (a Try again screen, never a password form), then
//     "Skip the rest of setup" on step 2, which must still save the preselected zone.
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

const require = createRequire(import.meta.url);
const webDir = fileURLToPath(new URL("../", import.meta.url));
const root = fileURLToPath(new URL("../../", import.meta.url));
const { values: opt } = parseArgs({ options: { headed: { type: "boolean", default: false }, screenshots: { type: "string" }, bin: { type: "string" }, help: { type: "boolean", default: false } } });
if (opt.help) {
  console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/).slice(0, 22).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
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
  const b = spawnSync("go", ["build", "-o", bin, "./cmd/kipple"], { cwd: root, stdio: "inherit" });
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
  check(where, await page.getByText(`Step ${n} of 7`).isVisible(), `no "Step ${n} of 7"`);
  await axeRun(page, where);
  await overflowRun(page, where);
  if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, `${tag}-step${n}.png`), fullPage: true });
}

const browser = await chromium.launch({ headless: !opt.headed }).catch((e) => setupError(`could not start Chromium (npx playwright install chromium): ${e.message.split("\n")[0]}`));

try {
  // ---------------------------------------------------------------------------------------------- Run A
  {
    const { origin } = await startServer("a", 7191);
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, timezoneId: "Asia/Tokyo", colorScheme: "light" });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    page.on("console", (m) => m.type() === "error" && !/status of 40[134]|status of 429/.test(m.text()) && errors.push(m.text()));

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
    await page.getByLabel("User name").fill("tester");
    await page.getByLabel("Password", { exact: true }).fill("a long enough password");
    await page.getByLabel("Password again").fill("a long enough password");
    await page.getByRole("button", { name: "Create my account" }).click();

    // Step 2: the browser's zone (Asia/Tokyo here) is preselected.
    await onStep(page, "A", "Choose your time zone", 2);
    check("A step 2", ((await page.getByTestId("selected-zone").textContent()) ?? "").includes("Asia/Tokyo"), "Asia/Tokyo was not preselected");
    await page.getByLabel("Search time zones").fill("+9");
    check("A step 2", (await page.getByRole("listbox", { name: "Time zones" }).getByRole("option", { name: /Asia\/Tokyo/ }).count()) === 1, "searching +9 did not find Tokyo");
    await page.getByLabel("Search time zones").fill("");
    await page.getByRole("button", { name: "Continue" }).click();

    await onStep(page, "A", "Look and feel", 3);
    await page.getByLabel("Day theme").selectOption("linen");
    check("A step 3", (await page.evaluate(() => document.documentElement.dataset.theme)) === "linen", "the day theme was not applied at once (light browser)");
    // The reading font is part of this step (it once went missing everywhere but the Aa menu): every font, applied at once.
    const fontSelect = page.getByLabel("Reading font");
    check("A step 3", (await fontSelect.count()) === 1 && (await fontSelect.locator("option").count()) >= 12, "no Reading font select with every font");
    await fontSelect.selectOption("vollkorn");
    check("A step 3", /Vollkorn/.test(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--kp-reading-font"))), "the reading font was not applied at once");
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "A-step4-font.png"), fullPage: true });
    await page.getByRole("button", { name: "Continue" }).click();

    await onStep(page, "A", "Bring your feeds along", 4);
    // Steps are in the address: a reload lands on the same step.
    await page.reload();
    await onStep(page, "A", "Bring your feeds along", 4);
    await page.getByRole("button", { name: "Skip", exact: true }).click();

    await onStep(page, "A", "Recommended feeds", 5);
    await page.getByRole("button", { name: /^Add \d+ feeds?$/ }).click(); // uat-labels: ignore (the wizard Add N feeds button)

    // Step 6: the public URL. 127.0.0.1 is never suggested, so the field starts empty; Skip saves nothing.
    await onStep(page, "A", "Your Kipple's address", 6);
    check("A step 6", (await page.getByLabel("Public URL (optional)").inputValue()) === "", "an IP address was suggested as the public URL");
    await page.getByRole("button", { name: "Skip", exact: true }).click();

    await onStep(page, "A", "You're all set", 7);
    // The reload at step 4 made the page forget the password typed in step 1, so step 7 asks for it again.
    check("A step 7", await page.getByLabel("Your web password").isVisible(), "the web password was not asked for after a reload");
    await page.getByLabel("Your web password").fill("a long enough password");
    await page.getByRole("button", { name: "Generate API password" }).click();
    await page.getByTestId("api-password").waitFor({ timeout: 10_000 });
    check("A step 7", ((await page.getByTestId("api-password").textContent()) ?? "").length > 8, "no API password shown");
    await axeRun(page, "A step 7 (password shown)");
    await page.getByRole("button", { name: "Finish" }).click();
    await page.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("A finish", `the reader did not open (at ${page.url()})`));
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "A-reader.png"), fullPage: true });

    // What the wizard wrote, and that setup routes are gone.
    const settings = await page.evaluate(async () => (await fetch("/api/settings")).json());
    check("A settings", settings.values?.tz === "Asia/Tokyo", `tz is ${settings.values?.tz}, expected Asia/Tokyo`);
    check("A settings", settings.values?.["ui.theme_day"] === "linen", `ui.theme_day is ${settings.values?.["ui.theme_day"]}`);
    check("A settings", settings.values?.["ui.font_body"] === "vollkorn", `ui.font_body is ${settings.values?.["ui.font_body"]}, expected vollkorn`);
    check("A setup routes", (await call(sp, "/api/setup/account", { username: "intruder", password: "intruder-password" })) === 404, "/api/setup/account still answers after setup");
    const me = await page.evaluate(async () => (await fetch("/api/auth/me")).json());
    check("A account", me.setup_pending === false, "setup is still pending after Finish");
    check("A account", errors.length === 0, `console or page errors: ${errors.join(" | ")}`);
    await ctx.close();
    await stranger.close();
  }

  // ---------------------------------------------------------------------------------------------- Run B
  {
    const { origin } = await startServer("b", 7192);
    const ctx = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true, hasTouch: true, colorScheme: "dark" });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(origin);
    await onStep(page, "B", "Create your account", 1);
    await page.getByLabel("User name").fill("opener");
    await page.getByRole("radio", { name: /No password at all/ }).check();
    await page.getByText("Anyone who can reach this address can read and change everything.").waitFor();
    await axeRun(page, "B step 0 (open mode notice)");
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "B-step2-open.png"), fullPage: true });
    await page.getByRole("button", { name: "Create my account" }).click();
    check("B step 1", await page.getByText("Tick the box to confirm you understand.").isVisible(), "no acknowledgement error");
    await page.getByRole("checkbox", { name: /I understand/ }).check();
    await page.getByRole("button", { name: "Create my account" }).click();
    await onStep(page, "B", "Choose your time zone", 2);
    await page.getByRole("button", { name: "Continue" }).click();
    await onStep(page, "B", "Look and feel", 3);
    // A preview is not a choice: trying a theme and skipping must leave this device's profile without theme overrides.
    await page.getByLabel("Day theme").selectOption("linen");
    await page.getByLabel("Reading font").selectOption("inter");
    await page.waitForTimeout(900); // longer than the 500 ms the app waits before it would send a change
    await page.getByRole("button", { name: "Skip", exact: true }).click();
    await onStep(page, "B", "Bring your feeds along", 4);
    await page.waitForTimeout(900);
    const dev = await page.evaluate(async () => (await fetch("/api/device")).json());
    const pinned = Object.keys(dev.profile ?? {}).filter((k) => k.startsWith("ui.theme") || k === "ui.font_body");
    check("B step 3", pinned.length === 0, `a preview and Skip left theme or font overrides in the device profile: ${pinned.join(", ")}`);
    check("B step 3", !/Inter/.test(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--kp-reading-font"))), "Skip did not put the reading font back");
    await page.getByRole("button", { name: "Back" }).click();
    await onStep(page, "B", "Look and feel", 3);
    await page.getByRole("button", { name: "Continue" }).click();
    await onStep(page, "B", "Bring your feeds along", 4);
    await page.getByRole("button", { name: "Skip", exact: true }).click();
    await onStep(page, "B", "Recommended feeds", 5);
    await page.getByRole("button", { name: "Skip", exact: true }).click();
    await onStep(page, "B", "Your Kipple's address", 6);
    await page.getByRole("button", { name: "Skip", exact: true }).click();
    await onStep(page, "B", "You're all set", 7);
    await page.getByRole("button", { name: "Generate API password" }).click();
    await page.getByTestId("api-password").waitFor({ timeout: 10_000 });
    await page.getByRole("button", { name: "Finish" }).click();
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
    // Server trouble on the first screen, and "Skip the rest of setup" from step 2.
    const { origin } = await startServer("c", 7193);
    const ctx = await browser.newContext({ viewport: { width: 375, height: 812 }, isMobile: true, hasTouch: true, timezoneId: "Asia/Tokyo" });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    // GET /api/instance cannot be reached: the screen says so and offers Try again, and never a password form.
    await page.route("**/api/instance", (r) => r.abort());
    await page.goto(origin);
    try {
      await page.getByRole("heading", { level: 1, name: "Kipple couldn't load" }).waitFor({ timeout: 10_000 });
      check("C offline", await page.getByRole("button", { name: "Try again" }).isVisible(), "no Try again button");
      check("C offline", (await page.getByLabel("Password").count()) === 0, "a password form was shown when the server could not be reached");
      await overflowRun(page, "C offline");
      if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, "C-offline.png"), fullPage: true });
    } catch {
      fail("C offline", `no "couldn't load" screen when /api/instance was unreachable`);
    }
    await page.unroute("**/api/instance");
    await page.getByRole("button", { name: "Try again" }).click();
    await onStep(page, "C", "Create your account", 1);
    await page.getByLabel("User name").fill("skipper");
    await page.getByLabel("Password", { exact: true }).fill("a long enough password");
    await page.getByLabel("Password again").fill("a long enough password");
    await page.getByRole("button", { name: "Create my account" }).click();
    await onStep(page, "C", "Choose your time zone", 2);
    await page.getByRole("button", { name: "Skip the rest of setup" }).click();
    await page.waitForURL(/\/l\/unread/, { timeout: 10_000 }).catch(() => fail("C skip-all", `the reader did not open (at ${page.url()})`));
    const settings = await page.evaluate(async () => (await fetch("/api/settings")).json());
    check("C skip-all", settings.values?.tz === "Asia/Tokyo", `tz is ${settings.values?.tz} after "Skip the rest of setup" on step 2, expected Asia/Tokyo`);
    const me = await page.evaluate(async () => (await fetch("/api/auth/me")).json());
    check("C skip-all", me.setup_pending === false, "setup is still pending after Skip the rest of setup");
    check("C account", errors.length === 0, `page errors: ${errors.join(" | ")}`);
    await ctx.close();
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
