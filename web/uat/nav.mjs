// UAT: one destination is current, the resize handles are reachable, focus survives Back and Forward, and the landscape
// tab badge sits after the name (the navigation package of the design review: #638, #631, #641, #643).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/nav.mjs                   in another, once the feeds have fetched
//   node uat/nav.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the desktop width of the run:
//   A1  the sidebar marks exactly one destination: Unread on Unread, Feeds on Feed health, none on a feed's list;
//   A2  the sidebar has no "Manage feeds", and All articles has an icon of its own;
//   A3  "Resize sidebar" is at least 24 px wide and the sidebar's rows end at least 8 px before its edge;
//   A4  open an article from the list, press Back: focus is on the row left, not the body; Forward: not the body.
// On the phone width of the run:
//   B1  at 812x375 the unread badge follows the Articles name and is not on top of it.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
/* global document, NodeFilter -- used inside evaluate callbacks, which run in the browser */
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const feed = (await t.boot(page)).feeds[0];
  if (!feed) return t.fail(`${tag} setup`, "the seeded instance has no feed");
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(600);
  };
  if (!vp.mobile) {
    const side = page.locator('nav[aria-label="Primary"]').first();
    const current = () => side.locator('a[aria-current="page"]').evaluateAll((as) => as.map((a) => a.innerText.replace(/\s+/g, " ").replace(/\s*\d+\+?$/, "").trim()));
    await step(`${tag} A1`, async () => {
      for (const [path, want] of [["/l/unread", ["Unread"]], ["/l/all", ["All articles"]], ["/health", ["Feeds"]], [`/l/unread?feed=${feed.id}`, []]]) {
        await open(path);
        const got = await current();
        check(JSON.stringify(got) === JSON.stringify(want), `${tag} A1 ${path.split("?")[0]}${path.includes("feed") ? " (feed)" : ""}`, `current: ${want.join(", ") || "none"}`, `current: ${got.join(", ") || "none"}`);
      }
      await t.shot(page, `${tag}-sidebar-feed-list`);
    });
    await step(`${tag} A2`, async () => {
      await open("/l/unread");
      const names = await side.locator("a").allInnerTexts();
      check(!names.some((n) => /Manage feeds/.test(n)), `${tag} A2 name`, "no Manage feeds", "Manage feeds is still there");
      const icons = await side.locator("a svg").evaluateAll((s) => s.map((e) => e.getAttribute("class") ?? "").slice(0, 6));
      check(new Set(icons).size === icons.length, `${tag} A2 icons`, "each destination has its own icon", `icons repeat: ${icons.join(" | ")}`);
    });
    await step(`${tag} A3`, async () => {
      await open("/l/unread");
      const handle = page.getByRole("separator", { name: "Resize sidebar" });
      const box = await handle.boundingBox();
      check(!!box && box.width >= 24, `${tag} A3 width`, `the handle is ${box?.width} px wide`, `the handle is ${box?.width} px wide, under 24`);
      const gap = await side.evaluate((nav) => {
        const edge = nav.getBoundingClientRect().right;
        return Math.min(...[...nav.querySelectorAll("a, button")].map((e) => edge - e.getBoundingClientRect().right));
      });
      check(gap >= 8, `${tag} A3 clear`, `controls end ${Math.round(gap)} px from the edge`, `a control ends ${Math.round(gap)} px from the edge`);
    });
    await step(`${tag} A4`, async () => {
      await open("/l/all");
      const row = page.locator("[data-item-id] a").first();
      await row.waitFor();
      const id = await row.evaluate((a) => a.closest("[data-item-id]").dataset.itemId);
      await row.click();
      await page.waitForURL(/\/i\//, { timeout: 5000 });
      await page.goBack();
      await page.waitForURL(/\/l\/all/, { timeout: 5000 });
      await page.waitForTimeout(600);
      const where = () => page.evaluate(() => ({ body: document.activeElement === document.body, row: document.activeElement?.closest("[data-item-id]")?.dataset.itemId ?? null, tag: document.activeElement?.tagName }));
      let w = await where();
      check(!w.body && w.row === id, `${tag} A4 back`, "Back puts focus on the row left", `focus is on ${w.row ?? w.tag}`);
      await page.goForward();
      await page.waitForURL(/\/i\//, { timeout: 5000 });
      await page.waitForTimeout(600);
      w = await where();
      check(!w.body, `${tag} A4 forward`, "Forward does not drop focus to the body", "Forward left focus on the body");
    });
  } else {
    await step(`${tag} B1`, async () => {
      await page.setViewportSize({ width: 812, height: 375 });
      await open("/l/unread");
      const r = await page.locator('nav[aria-label="Primary"]').last().locator("a").first().evaluate((a) => {
        const badge = a.querySelector('[data-testid="unread-count"], [data-testid="unread-dot"]');
        const walker = document.createTreeWalker(a, NodeFilter.SHOW_TEXT);
        let label = null;
        for (let n = walker.nextNode(); n; n = walker.nextNode()) if (/Articles/.test(n.textContent)) label = n;
        if (!badge || !label) return null;
        const range = document.createRange();
        range.selectNodeContents(label);
        return { badgeLeft: badge.getBoundingClientRect().left, labelRight: range.getBoundingClientRect().right };
      });
      check(r === null || r.badgeLeft >= r.labelRight - 1, `${tag} B1`, "the badge follows the name", `the badge starts at ${r?.badgeLeft} inside the name that ends at ${r?.labelRight}`);
      await t.shot(page, `${tag}-landscape-badge`);
    });
  }
});
await t.finish("nav");
