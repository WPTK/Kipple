// Reproduces the measurement behind unloadRefusal in common.mjs (issue #370): sign in under WebKit, navigate at once,
// and count the runs in which WebKit refuses a same-origin fetch as the page is left, and how many of those also had a
// `requestfailed` for the same URL.
//
//   node uat/measure-unload-refusal.mjs [--runs 20]     against the seeded instance (KIPPLE_UAT_URL, default 127.0.0.1:1919)
//
// The counts vary from machine to machine (the race is timing), so this prints them rather than asserting them. What
// the filter relies on is the second line: refusals that came with a requestfailed for their URL should be 0.
import { webkit } from "@playwright/test";
import { accessControlUrl } from "./common.mjs";

const runs = Number(process.argv[process.argv.indexOf("--runs") + 1]) || 20;
const base = process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919";
const user = process.env.KIPPLE_UAT_USER || "dev";
const password = process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing";
if (!["127.0.0.1", "[::1]"].includes(new URL(base).hostname)) {
  console.error("loopback only");
  process.exit(2);
}

const browser = await webkit.launch();
let refusedRuns = 0;
let withRequestFailed = 0;
for (let i = 0; i < runs; i++) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  const refused = [];
  const failed = new Set();
  page.on("pageerror", (e) => {
    const u = accessControlUrl(e);
    if (u) refused.push(u.href);
  });
  page.on("requestfailed", (r) => failed.add(r.url()));
  await page.goto(base + "/");
  await page.getByLabel("Username").fill(user);
  await page.getByLabel("Password").fill(password);
  await page.keyboard.press("Enter");
  await page.waitForURL((u) => !/login|setup/.test(u.pathname), { timeout: 15000 }).catch(() => {});
  await page.goto(base + "/l/all").catch(() => {});
  await page.waitForTimeout(500);
  if (refused.length) refusedRuns++;
  if (refused.some((u) => failed.has(u))) withRequestFailed++;
  await ctx.close();
}
await browser.close();
console.log(`${refusedRuns} of ${runs} runs had a refused fetch while the page was left`);
console.log(`${withRequestFailed} of ${runs} runs had a requestfailed for the same URL as a refusal`);
