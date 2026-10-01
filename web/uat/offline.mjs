// UAT Suite 1 addition: offline reading with the service worker, in a real browser (issue #108).
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:7080)
//   npm run uat:offline                in another, once the feeds have fetched
//   npm run uat:offline -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser, lets the service worker install
// and keep the first page of Unread (the app's own prefetch), then takes the browser offline with the app open
// (context.setOffline: the page sees the `offline` event, which is what paused every query before #108) and:
//   O1  goes to a list the worker never kept (Starred) and to Stats: each ends in its error screen, not a loading
//       state that never finishes;
//   O2  opens the first Unread article and stars it: both are queued on the device (the notice counts them);
//   O3  reloads offline: the app starts from the worker's copy of the bootstrap and the Unread list, with the notice;
//   O4  opens that article again: it shows its heading, read from the worker's copy;
//   O5  O1 again, after the offline reload;
//   O6  back online: the queued changes are sent (the notice clears) and Stats loads without a click.
//
// It opens and stars one article (marking it read and recording reading stats once the queue is sent) on the seed's
// account, so it only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error. First time on a machine: `npx playwright install chromium`.
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium } from "@playwright/test";
import { pushRoute } from "./probes.mjs";

const setupError = (m) => {
  console.error(m);
  process.exit(2);
};
const { values: opt } = (() => {
  try {
    return parseArgs({
      options: {
        url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:7080" },
        user: { type: "string", default: process.env.KIPPLE_UAT_USER || "dev" },
        // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
        password: { type: "string", default: process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing" },
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
  console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/).slice(0, 20).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}
const origin = new URL(opt.url).origin;
const loopback = ["127.0.0.1", "[::1]"].includes(new URL(origin).hostname);
if (!opt["allow-remote"] && (!loopback || opt.user !== "dev" || opt.password !== "dev-password-only-for-local-testing")) setupError("only a loopback --url with the seed's account, unless --allow-remote (point it at a throwaway instance)");
if (opt.screenshots) mkdirSync(opt.screenshots, { recursive: true });

const findings = [];
const fail = (where, what) => {
  findings.push(`${where}: ${what}`);
  console.error(`FAIL ${where}: ${what}`);
};
const pass = (where, what) => console.log(`ok   ${where}: ${what}`);

const VIEWPORTS = [
  { id: "desktop", width: 1280, height: 800, mobile: false },
  { id: "phone", width: 375, height: 812, mobile: true },
];

/** Waits until `fn` (run in the page) is truthy; false on timeout. */
async function until(page, fn, arg, ms) {
  try {
    await page.waitForFunction(fn, arg, { timeout: ms, polling: 250 });
    return true;
  } catch {
    return false;
  }
}

async function shot(page, name) {
  if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, `${name}.png`), fullPage: false }).catch(() => {});
}

/** The visible text of every <main> (the shell's and the screen's), on one line. */
async function mainText(page) {
  const t = await page.evaluate(() => [...document.querySelectorAll("main")].map((m) => m.innerText).join(" | ")).catch(() => "");
  return t.replace(/\s+/g, " ").slice(0, 200);
}

async function noticeText(page) {
  return ((await page.getByTestId("offline-notice").textContent().catch(() => "")) ?? "").trim();
}

/** How many offline changes the notice says are waiting (0 when it names none). */
async function pendingCount(page) {
  const m = /(\d+) changes? will be sent/.exec(await noticeText(page));
  return m ? Number(m[1]) : 0;
}

/** Whether the article with this title shows its heading within 15 s. */
function opened(page, title) {
  return page
    .getByRole("heading", { level: 1, name: title })
    .first()
    .waitFor({ timeout: 15000 })
    .then(() => true)
    .catch(() => false);
}

/** O1 and O5: screens the worker never kept end in their error screen, not a loading state that never finishes. */
async function neverKept(page, where) {
  // Each screen's own text next to its error ("Range" is only on Stats; the lists say the same "couldn't reach").
  for (const [path, expect] of [
    ["/l/starred", ["Starred", "Couldn't load articles"]],
    ["/stats", ["Range", "Kipple couldn't reach the server."]],
  ]) {
    await page.evaluate(pushRoute, path);
    // Visible text only: a screen React keeps hidden behind a Suspense fallback still has its innerText.
    const ok = await until(
      page,
      ([p, ts]) => location.pathname === p && [...document.querySelectorAll("main")].some((m) => m.checkVisibility() && ts.every((t) => m.innerText.includes(t))),
      [path, expect],
      15000,
    );
    await shot(page, `${where.replace(/\W+/g, "-")}-${path.replace(/\W+/g, "-")}`);
    if (!ok) fail(`${where} ${path}`, `no "${expect[1]}" after 15 s offline; it shows: ${await mainText(page)}`);
    else pass(`${where} ${path}`, `"${expect[1]}", not a skeleton`);
  }
}

async function run(browser, vp) {
  const tag = vp.id;
  const context = await browser.newContext({
    baseURL: origin,
    viewport: { width: vp.width, height: vp.height },
    isMobile: vp.mobile,
    hasTouch: vp.mobile,
    serviceWorkers: "allow",
  });
  try {
    const page = await context.newPage();
    await page.goto("/", { waitUntil: "load" });
    await page.getByLabel("Username").fill(opt.user);
    await page.getByLabel("Password").fill(opt.password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });

    // The worker registers once the app is up; a page is only controlled from its next load.
    if (!(await until(page, () => navigator.serviceWorker.ready.then(() => true), undefined, 20000))) return setupError(`${tag}: the service worker never activated`);
    await page.reload({ waitUntil: "load" });
    if (!(await until(page, () => !!navigator.serviceWorker.controller, undefined, 10000))) return setupError(`${tag}: the page is not controlled by the worker`);
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
    await page.goto("/l/unread", { waitUntil: "load" });
    const link = page.locator('main article a[href^="/i/"]').first();
    await link.waitFor({ timeout: 15000 }).catch(() => setupError(`${tag}: Unread has no articles (have the seed's feeds fetched?)`));
    const href = await link.getAttribute("href");
    const id = href.split("/").pop();
    // The prefetch of Unread with full content files each article separately: wait for this one.
    const kept = await until(
      page,
      (path) => caches.open("kipple-data").then((c) => c.match(path)).then((r) => !!r),
      `/api/items/${id}`,
      30000,
    );
    if (!kept) return setupError(`${tag}: the worker never kept /api/items/${id} (the Unread prefetch)`);
    const title = (await page.request.get(`/api/items/${id}`, { headers: { Accept: "application/json" } }).then((r) => r.json())).title;

    // The browser goes offline with the app open: the page sees the `offline` event.
    await context.setOffline(true);
    await until(page, () => /offline/i.test(document.querySelector('[data-testid="offline-notice"]')?.textContent ?? ""), undefined, 10000);
    await neverKept(page, `${tag} O1`);

    // O2: opening an article and starring it offline are kept on the device for later (the notice counts them).
    await page.evaluate(pushRoute, "/l/unread");
    await page.locator(`main article a[href="${href}"]`).first().click();
    if (await opened(page, title)) {
      // The read is stored a moment after the article shows.
      await until(page, () => /\d+ changes? will be sent/.test(document.querySelector('[data-testid="offline-notice"]')?.textContent ?? ""), undefined, 10000);
      const before = await pendingCount(page);
      if (before < 1) fail(`${tag} O2`, `opening the article offline queued no read (notice: "${await noticeText(page)}")`);
      await page.keyboard.press("s");
      const after = await until(
        page,
        (n) => {
          const m = /(\d+) changes? will be sent/.exec(document.querySelector('[data-testid="offline-notice"]')?.textContent ?? "");
          return !!m && Number(m[1]) > n;
        },
        before,
        10000,
      );
      if (!after) fail(`${tag} O2`, `the star made offline was not queued (notice: "${await noticeText(page)}")`);
      else if (before >= 1) pass(`${tag} O2`, `read and star queued: "${await noticeText(page)}"`);
    } else fail(`${tag} O2`, `article ${id} did not open after going offline (it shows: ${await mainText(page)})`);

    // O3: a reload offline starts from the worker's copies.
    await page.goto("/l/unread", { waitUntil: "load" });
    const listed = await until(page, (h) => !!document.querySelector(`main article a[href="${h}"]`), href, 15000);
    await shot(page, `${tag}-o3-list`);
    if (!listed) fail(`${tag} O3`, `the Unread list did not show offline after a reload (it shows: ${await mainText(page)})`);
    else pass(`${tag} O3`, "Unread list from the worker's copy");
    const notice = await noticeText(page);
    if (!/offline/i.test(notice)) fail(`${tag} O3`, `no offline notice (it says "${notice}")`);
    else pass(`${tag} O3`, `offline notice: "${notice}"`);

    // O4: the first article opens from the worker's copy.
    if (listed) {
      await page.locator(`main article a[href="${href}"]`).first().click();
      const shown = await opened(page, title);
      await shot(page, `${tag}-o4-article`);
      if (!shown) fail(`${tag} O4`, `article ${id} ("${title}") did not open offline (it shows: ${await mainText(page)})`);
      else pass(`${tag} O4`, `article "${title}" opened offline`);
    }

    await neverKept(page, `${tag} O5`);

    // O6: back online, the queued changes are sent and the failed screen (Stats, the last one) loads by itself.
    await context.setOffline(false);
    const back = await until(
      page,
      () =>
        (document.querySelector('[data-testid="offline-notice"]')?.textContent ?? "").trim() === "" &&
        [...document.querySelectorAll("main")].some((m) => m.checkVisibility() && m.innerText.includes("Range") && !m.innerText.includes("couldn't reach")),
      undefined,
      20000,
    );
    await shot(page, `${tag}-o6-online`);
    if (!back) fail(`${tag} O6`, `back online, still: notice "${await noticeText(page)}", screen: ${await mainText(page)}`);
    else pass(`${tag} O6`, "back online: changes sent, Stats loaded without Try again");
  } finally {
    await context.close().catch(() => {});
  }
}

const browser = await chromium.launch({ headless: !opt.headed }).catch((e) => setupError(`could not start Chromium (npx playwright install chromium): ${e.message.split("\n")[0]}`));
try {
  for (const vp of VIEWPORTS) await run(browser, vp);
} catch (e) {
  setupError(`run failed: ${String(e?.message ?? e).split("\n")[0]}`);
} finally {
  await browser.close();
}
console.log(findings.length ? `\n${findings.length} finding(s)` : "\nclean");
process.exit(findings.length ? 1 : 0);
