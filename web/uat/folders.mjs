// UAT Suite 1 addition: nested folders in a real browser (issue #209).
//
//   npm run build && npm run seed      in one terminal (the seed serves the embedded build on 127.0.0.1:1919)
//   npm run uat:folders                in another, once the feeds have fetched
//   npm run uat:folders -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and, on the Feeds screen:
//   N1  creates a folder, a subfolder inside it and a subfolder inside that (New folder, then New subfolder);
//   N2  moves a feed into the deepest one (Select, Move to folder, picked by its path);
//   N3  counts: each of the three folders shows the feed's unread number (its subtree), in the bootstrap and on screen;
//       the sidebar (desktop) is a tree with the three levels; axe-core has no finding on Feeds and on the folder list;
//       the phone has no sideways scroll with three levels open;
//   N4  moves the deepest folder to the top level and back (Move to…), checking the parent each time;
//   N5  deletes the top folder: the dialog says its subfolders go and its feed moves, and afterwards the three folders
//       are gone and the feed is in the default folder. The feed is then put back where it was.
//
// It changes the seed's library (and puts the feed back), so it only runs against a loopback address with the seed's
// credentials unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium`.
import { mkdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium } from "@playwright/test";

const require = createRequire(import.meta.url);
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
  console.log(readFileSync(fileURLToPath(import.meta.url), "utf8").split(/\r?\n/).slice(0, 19).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
  process.exit(0);
}
const origin = new URL(opt.url).origin;
const loopback = ["127.0.0.1", "[::1]"].includes(new URL(origin).hostname);
if (!opt["allow-remote"] && (!loopback || opt.user !== "dev" || opt.password !== "dev-password-only-for-local-testing")) setupError("only a loopback --url with the seed's account, unless --allow-remote (point it at a throwaway instance)");
if (opt.screenshots) mkdirSync(opt.screenshots, { recursive: true });
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const SEP = " › ";
// What the server asks of a write that is not from the app's own fetch: the client header and a same-origin Origin.
const WRITE = { "X-Kipple-Client": "web", Origin: origin };

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

async function shot(page, name) {
  if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, `${name}.png`), fullPage: false }).catch(() => {});
}

async function axeRun(page, where) {
  if (!(await page.evaluate(() => typeof window.axe === "object"))) await page.evaluate(AXE);
  const res = await page.evaluate(() =>
    window.axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"] } }).then((r) => r.violations.map((v) => `${v.id} at ${v.nodes.map((n) => n.target.join(" ")).slice(0, 3).join(" | ")}`)),
  );
  check(res.length === 0, `${where} axe`, "no findings", res.join("; "));
}

const boot = (page) => page.request.get("/api/bootstrap", { headers: { Accept: "application/json" } }).then((r) => r.json());
const folderNamed = (b, name, parent) => b.folders.find((f) => f.name === name && (f.parent_id ?? null) === parent);

async function newFolder(page, name, parentLabel) {
  if (parentLabel) {
    await page.getByRole("button", { name: `Folder actions for ${parentLabel}`, exact: true }).click();
    await page.getByRole("menuitem", { name: "New subfolder" }).click();
  } else {
    await page.getByRole("button", { name: "Feed actions" }).click();
    await page.getByRole("menuitem", { name: "New folder" }).click();
  }
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "Name" }).fill(name);
  await dialog.getByRole("button", { name: "Create" }).click();
  await dialog.waitFor({ state: "detached", timeout: 10000 });
}

async function moveFolderTo(page, label, intoLabel) {
  await page.getByRole("button", { name: `Folder actions for ${label}`, exact: true }).click();
  await page.getByRole("menuitem", { name: "Move to…" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "Move into" }).selectOption({ label: intoLabel });
  await dialog.getByRole("button", { name: "Move" }).click();
  await dialog.waitFor({ state: "detached", timeout: 10000 });
}

/** Polls the bootstrap until `fn(b)` is truthy; returns the last bootstrap and whether it held. */
async function settle(page, fn, ms = 10000) {
  const end = Date.now() + ms;
  let b;
  do {
    b = await boot(page);
    if (fn(b)) return { b, ok: true };
    await page.waitForTimeout(250);
  } while (Date.now() < end);
  return { b, ok: false };
}

async function run(browser, vp) {
  const tag = vp.id;
  const P = `UAT Parent ${tag}`;
  const C = "UAT Child";
  const G = "UAT Grandchild";
  const context = await browser.newContext({ baseURL: origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });
  let restore = null;
  try {
    const page = await context.newPage();
    await page.goto("/", { waitUntil: "load" });
    await page.getByLabel("Username").fill(opt.user);
    await page.getByLabel("Password").fill(opt.password);
    await page.getByRole("button", { name: "Sign in" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });

    // A run that stopped half-way leaves its folders behind: remove them first (their feeds go to the default).
    const stale = folderNamed(await boot(page), P, null);
    if (stale) {
      const r = await page.request.delete(`/api/folders/${stale.id}`, { headers: WRITE });
      if (!r.ok()) return setupError(`${tag}: could not remove the folders of an earlier run (${r.status()} ${await r.text()})`);
    }
    const b0 = await boot(page);
    const feed = b0.feeds.find((f) => !f.is_archive && f.unread > 0);
    if (!feed) return setupError(`${tag}: no feed with unread articles (have the seed's feeds fetched?)`);
    restore = { id: feed.id, folder: feed.folder_id };

    // N1
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("heading", { level: 1, name: "Feeds" }).waitFor({ timeout: 15000 });
    await newFolder(page, P, null);
    await newFolder(page, C, P);
    await newFolder(page, G, `${P}${SEP}${C}`);
    const made = await settle(page, (b) => {
      const p = folderNamed(b, P, null);
      const c = p && folderNamed(b, C, p.id);
      return c && folderNamed(b, G, c.id);
    });
    check(made.ok, `${tag} N1`, "three levels created", "the folders did not appear nested in the bootstrap");
    if (!made.ok) return;
    const pid = folderNamed(made.b, P, null).id;
    const cid = folderNamed(made.b, C, pid).id;
    const gid = folderNamed(made.b, G, cid).id;

    // N2
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await page.getByRole("checkbox", { name: `Select ${feed.title}`, exact: true }).first().click();
    await page.getByRole("button", { name: "Move to folder" }).click();
    const mv = page.getByRole("dialog");
    await mv.getByRole("combobox", { name: "Move to folder" }).selectOption({ label: `${P}${SEP}${C}${SEP}${G}` });
    await mv.getByRole("button", { name: "Move" }).click();
    await mv.waitFor({ state: "detached", timeout: 10000 });
    const moved = await settle(page, (b) => b.feeds.find((f) => f.id === feed.id)?.folder_id === gid);
    check(moved.ok, `${tag} N2`, "the feed is in the deepest folder", "the feed did not move");

    // N3
    const unread = moved.b.feeds.find((f) => f.id === feed.id).unread;
    const counts = [pid, cid, gid].map((id) => moved.b.folders.find((f) => f.id === id).unread);
    check(counts.every((n) => n === unread), `${tag} N3 bootstrap`, `each level counts ${unread}`, `counts ${counts.join(", ")}, the feed has ${unread}`);
    await page.goto("/feeds", { waitUntil: "load" });
    const parentRow = page.getByRole("link", { name: new RegExp(`^${P}`) }).first();
    await parentRow.waitFor({ timeout: 10000 });
    const badge = (await parentRow.innerText()).replace(/\s+/g, " ");
    check(badge.includes(String(Math.min(unread, 9999))), `${tag} N3 screen`, `the parent row shows ${unread}`, `the parent row reads "${badge}"`);
    await shot(page, `${tag}-feeds-nested`);
    await axeRun(page, `${tag} N3 Feeds`);
    if (vp.mobile) {
      const wide = await page.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
      check(wide <= 0, `${tag} N3 width`, "no sideways scroll", `${wide}px past the edge`);
    } else {
      const tree = page.getByRole("navigation", { name: "Primary" }).getByRole("tree", { name: "Feeds" });
      const levels = await tree.getByRole("treeitem").evaluateAll((els, names) => els.filter((e) => names.some((n) => e.querySelector("a")?.textContent?.startsWith(n))).map((e) => e.getAttribute("aria-level")), [P, C, G]);
      check(levels.join() === "1,2,3", `${tag} N3 sidebar`, "a tree with three levels", `levels ${levels.join()}`);
    }
    await page.goto(`/l/unread?folder=${gid}`, { waitUntil: "load" });
    const heading = page.getByRole("heading", { level: 1, name: `${P}${SEP}${C}${SEP}${G}` });
    check(await heading.waitFor({ timeout: 10000 }).then(() => true, () => false), `${tag} N3 title`, "the folder list's title names its path", "no heading with the path");
    await shot(page, `${tag}-folder-list`);
    await axeRun(page, `${tag} N3 folder list`);

    // N4
    await page.goto("/feeds", { waitUntil: "load" });
    await moveFolderTo(page, `${P}${SEP}${C}${SEP}${G}`, "Top level");
    let r = await settle(page, (b) => (b.folders.find((f) => f.id === gid)?.parent_id ?? null) === null);
    check(r.ok, `${tag} N4 out`, "moved to the top level", "still nested");
    await moveFolderTo(page, G, `${P}${SEP}${C}`);
    r = await settle(page, (b) => b.folders.find((f) => f.id === gid)?.parent_id === cid);
    check(r.ok, `${tag} N4 back`, "moved back inside its parent", "not back in place");

    // N5
    await page.getByRole("button", { name: `Folder actions for ${P}`, exact: true }).click();
    await page.getByRole("menuitem", { name: "Delete folder" }).click();
    const del = page.getByRole("dialog");
    const text = (await del.innerText()).replace(/\s+/g, " ");
    check(/2 subfolders are deleted too/.test(text) && /not deleted: it moves to/.test(text), `${tag} N5 dialog`, "names the subfolders and the feed", `reads "${text.slice(0, 200)}"`);
    await shot(page, `${tag}-delete-dialog`);
    await del.getByRole("button", { name: "Delete folder" }).click();
    const def = b0.folders.find((f) => f.is_default).id;
    r = await settle(page, (b) => ![pid, cid, gid].some((id) => b.folders.some((f) => f.id === id)) && b.feeds.find((f) => f.id === feed.id)?.folder_id === def);
    check(r.ok, `${tag} N5 result`, "subtree gone, feed in the default folder", "the subtree or the feed is not where it should be");
  } catch (e) {
    const page = context.pages()[0];
    if (page) await shot(page, `${tag}-stopped`);
    fail(`${tag}`, `the run stopped: ${e.message.split("\n").slice(0, 3).join(" ")}`);
  } finally {
    if (restore) {
      const page = context.pages()[0];
      await page?.request.patch(`/api/feeds/${restore.id}`, { data: { folder_id: restore.folder }, headers: WRITE }).catch(() => {});
    }
    await context.close();
  }
}

const browser = await chromium.launch({ headless: !opt.headed }).catch((e) => setupError(`no browser: ${e.message} (npx playwright install chromium)`));
for (const vp of VIEWPORTS) await run(browser, vp);
await browser.close();
console.log(findings.length ? `\n${findings.length} finding(s)` : "\nall nested-folder checks passed");
process.exit(findings.length ? 1 : 0);
