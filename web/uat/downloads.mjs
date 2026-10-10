// UAT: the three downloads in a real browser, each saved as the real file and not as an error body.
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:1919)
//   npm run uat:downloads              in another, once the feeds have fetched
//   npm run uat:downloads -- --url http://127.0.0.1:7091 --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and clicks:
//   D1  Feeds, Feed actions, Export OPML: the saved file is an OPML document (not JSON) and not named *.json;
//   D2  Settings, Statistics, Export…, Download: the saved file is CSV with a header row (not a JSON error);
//   D3  Settings, Account & Devices, Export backup, the download link: the saved file is a zip (starts "PK").
// D3 starts a backup export (POST /api/backup), which replaces any unclaimed one, and downloading it spends it and
// deletes the file, so nothing is left behind. It therefore only runs against a loopback address with the seed's
// credentials unless --allow-remote is given.
// Chromium sends Sec-Fetch-Site: same-origin for these clicks, so this checks that each saved file is the real one;
// the requests without fetch metadata or from another site are covered by the Go tests (TestDownloadOriginMatrix).
// Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium`.
import { readFileSync } from "node:fs";
import { parseArgs } from "node:util";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const setupError = (m) => {
  console.error(m);
  process.exit(2);
};
const { values: opt } = (() => {
  try {
    return parseArgs({
      options: {
        url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919" },
        user: { type: "string", default: process.env.KIPPLE_UAT_USER || "dev" },
        // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
        password: { type: "string", default: process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing" },
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
  console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/).slice(0, 17).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}
const origin = new URL(opt.url).origin;
const loopback = ["127.0.0.1", "[::1]"].includes(new URL(origin).hostname);
if (!opt["allow-remote"] && (!loopback || opt.user !== "dev" || opt.password !== "dev-password-only-for-local-testing")) setupError("only a loopback --url with the seed's account, unless --allow-remote (point it at a throwaway instance)");

const findings = [];
const fail = (where, what) => {
  findings.push(`${where}: ${what}`);
  console.error(`FAIL ${where}: ${what}`);
};
const pass = (where, what) => console.log(`ok   ${where}: ${what}`);
const check = (cond, where, ok, bad) => (cond ? pass(where, ok) : fail(where, bad));

const VIEWPORTS = [
  { id: "desktop", width: 1280, height: 800, mobile: false },
  { id: "phone", width: 375, height: 812, mobile: true },
];

/** Runs `click` and returns what the browser saved: the suggested name and the bytes. */
async function saved(page, click) {
  const [dl] = await Promise.all([page.waitForEvent("download", { timeout: 30000 }), click()]);
  const stream = await dl.createReadStream();
  const chunks = [];
  for await (const c of stream) chunks.push(c);
  return { name: dl.suggestedFilename(), bytes: Buffer.concat(chunks) };
}
const head = (b) => b.subarray(0, 120).toString("utf8").replace(/\s+/g, " ");

async function run(browser, vp) {
  const tag = vp.id;
  const context = await browser.newContext({ baseURL: origin, acceptDownloads: true, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });
  try {
    const page = await context.newPage();
    await page.goto("/", { waitUntil: "load" });
    await page.getByLabel("Username").fill(opt.user);
    await page.getByLabel("Password").fill(opt.password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });

    // D1
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("heading", { level: 1, name: "Feeds" }).waitFor({ timeout: 15000 });
    await page.getByRole("button", { name: "Feed actions" }).click();
    const opml = await saved(page, () => page.getByRole("menuitem", { name: "Export OPML" }).click());
    const text = opml.bytes.toString("utf8");
    check(/<opml[\s>]/.test(text) && !text.trimStart().startsWith("{"), `${tag} D1`, `OPML saved as ${opml.name}`, `${opml.name} reads "${head(opml.bytes)}"`);
    check(!/\.json$/i.test(opml.name), `${tag} D1 name`, "not named .json", `named ${opml.name}`);

    // D2
    await page.goto("/settings/statistics", { waitUntil: "load" });
    await page.getByRole("button", { name: "Export…" }).click();
    const dlg = page.getByRole("dialog", { name: "Export statistics" });
    const stats = await saved(page, () => dlg.getByRole("button", { name: "Download" }).click());
    const csv = stats.bytes.toString("utf8");
    check(/\.csv$/i.test(stats.name) && !csv.trimStart().startsWith("{") && csv.split(/\r?\n/)[0].includes(","), `${tag} D2`, `statistics saved as ${stats.name}`, `${stats.name} reads "${head(stats.bytes)}"`);

    // D3
    await page.goto("/settings/account", { waitUntil: "load" });
    await page.getByRole("button", { name: "Export backup" }).click();
    const bk = page.getByRole("dialog", { name: "Download backup" });
    const link = bk.getByRole("link", { name: /kipple-backup-.*\.zip/ });
    await link.waitFor({ timeout: 60000 });
    const zip = await saved(page, () => link.click());
    check(zip.bytes.length > 1000 && zip.bytes.subarray(0, 2).toString("latin1") === "PK", `${tag} D3`, `backup saved as ${zip.name} (${zip.bytes.length} bytes)`, `${zip.name} is ${zip.bytes.length} bytes reading "${head(zip.bytes)}"`);
  } catch (e) {
    fail(tag, `the run stopped: ${e.message.split("\n").slice(0, 3).join(" ")}`);
  } finally {
    await context.close();
  }
}

const browser = await chromium.launch({ headless: !opt.headed }).catch((e) => setupError(`no browser: ${e.message} (npx playwright install chromium)`));
for (const vp of VIEWPORTS) await run(browser, vp);
await browser.close();
console.log(findings.length ? `\n${findings.length} finding(s)` : "\nall download checks passed");
process.exit(findings.length ? 1 : 0);
