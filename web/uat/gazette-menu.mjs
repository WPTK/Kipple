// UAT: the row menu on the Gazette's stories (#314).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:gazette-menu           in another
//   npm run uat:gazette-menu -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The flow serves its own feed on a loopback port (nine stories, two with a picture) and opens its list with the Gazette
// as the layout. A story has the row menu, with the actions of a row except Mark above and below; the Gazette has no swipe.
// Chromium only (the long press is a real touch sequence sent through the DevTools protocol). For each width (desktop
// 1280x800 with a mouse, phone 375x812 with touch) it checks:
//   M1  desktop: the More button is hidden until the story is hovered, and then shown;
//   M2  desktop: the More button opens the menu with Star, Mark as read, Mute similar, Manage this feed, Open original and // uat-labels: ignore (a menu item's text, not an aria-label)
//       Copy link, and no Mark above or Mark below; Star stars the story (the server says so);
//   M3  desktop: the context menu (right click) opens the same menu; Mark as read marks the story read; // uat-labels: ignore (a menu item's text, not an aria-label)
//   M4  phone: the More button is not seen, but is in the accessibility tree; a long press on a story opens the menu
//       (and not the article), and Star stars it;
//   M5  phone: a tap still opens the story;
//   and after every action of the menu, every story is exactly where it was (the page does not move).
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { Buffer } from "node:buffer";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

const FEED_TITLE = "UAT feed gazette menu";
const COUNT = 9;
const WITH_PICTURE = [4, 7];
// A 1x1 GIF, so a picture's address answers.
const GIF = Buffer.from("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", "base64");

let base = "";
const server = createServer((req, res) => {
  const path = new URL(req.url ?? "/", "http://x").pathname;
  if (path.startsWith("/img/")) return void res.writeHead(200, { "Content-Type": "image/gif" }).end(GIF);
  if (path !== "/feed.xml") return void res.writeHead(404).end();
  const item = (n) => {
    const body = WITH_PICTURE.includes(n) ? `&lt;p&gt;Story ${n}.&lt;/p&gt;&lt;img src="${base}/img/${n}.gif" width="600" height="400"/&gt;` : `&lt;p&gt;Story ${n}.&lt;/p&gt;`;
    return `<item><title>UAT gazette menu story ${n}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-gazette-menu-${n}</guid><pubDate>${new Date(Date.now() - n * 3600_000).toUTCString()}</pubDate><description>${body}</description></item>`;
  };
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT gazette menu</title><link>${base}/</link><description>t</description>${Array.from({ length: COUNT }, (_, i) => item(i + 1)).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

const MENU_WANTED = ["Star", "Mark as read", "Mute similar", "Manage this feed", "Open original", "Copy link"]; // uat-labels: ignore (a menu item's text, not an aria-label)

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    const patch = (b) =>
      page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
    /** Where every story is, in page coordinates. */
    const places = () =>
      page.locator("[data-gazette] article[data-item-id]").evaluateAll((els) => {
        const scroller = document.querySelector('[data-testid="list-scroll"]');
        return els.map((e) => {
          const r = e.getBoundingClientRect();
          return [e.dataset.itemId, Math.round(r.left), Math.round(r.top + (scroller?.scrollTop ?? 0)), Math.round(r.width), Math.round(r.height)].join(":");
        });
      });
    const storyOf = (id) => page.locator(`[data-gazette] article[data-item-id="${id}"]`);
    const menuNames = async () => (await page.getByRole("menu").getByRole("menuitem").allInnerTexts()).map((s) => s.trim());
    const checkMenu = async (where) => {
      await page.getByRole("menu").waitFor({ timeout: 5000 });
      const names = await menuNames();
      const missing = MENU_WANTED.filter((w) => !names.some((n) => n.startsWith(w)));
      check(missing.length === 0 && !names.some((n) => /above|below/i.test(n)), where, "the menu has the row actions and no mark above or below", `the menu offers: ${names.join(", ")} (missing ${missing.join(", ") || "none"})`);
    };
    const itemIs = (id, field) =>
      page.waitForFunction(async ([i, f]) => (await (await fetch(`/api/items/${i}`)).json())[f] === true, [id, field], { timeout: 8000 }).then(
        () => true,
        () => false,
      );
    const stayed = async (home, where) => {
      const now = await places();
      check(JSON.stringify(now) === JSON.stringify(home), where, "every story stays where it was", `the page moved: ${home.join(" ")} -> ${now.join(" ")}`);
    };

    await step(`${tag} M`, async () => {
      for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED_TITLE)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
      const added = await t.addLocalFeed(page, `${base}/feed.xml`, FEED_TITLE, (items) => items.length >= COUNT);
      if (added.error) return t.fail(`${tag} M setup`, added.error);
      const set = await patch({ "client.layout": "gazette" });
      if (set !== 200) return t.fail(`${tag} M setup`, `the Gazette was not set as the layout (${set})`);
      try {
        await page.goto(`/l/all?feed=${added.feedId}`, { waitUntil: "load" });
        await page.locator("[data-gazette] article[data-item-id]").first().waitFor({ timeout: 20000 });
        await page.waitForTimeout(500);
        const ids = await page.locator("[data-gazette] article[data-item-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId));
        if (ids.length !== COUNT) return t.fail(`${tag} M setup`, `the page prints ${ids.length} stories, not ${COUNT}`);
        const home = await places();
        const [first, second, third] = [ids[2], ids[4], ids[5]];

        if (!vp.mobile) {
          const more = storyOf(first).getByRole("button", { name: /^More actions/ });
          const rest = await more.evaluate((e) => getComputedStyle(e).opacity);
          await storyOf(first).hover();
          const hovered = await more.evaluate((e) => getComputedStyle(e).opacity);
          check(rest === "0" && hovered === "1", `${tag} M1`, "the More button shows on hover only", `opacity ${rest} at rest, ${hovered} on hover`);
          await more.click();
          await checkMenu(`${tag} M2 menu`);
          await t.shot(page, `${tag}-gazette-menu`);
          await page.getByRole("menuitem", { name: "Star", exact: true }).click();
          check(await itemIs(first, "starred"), `${tag} M2 star`, "Star stars the story", "the story was not starred");
          await stayed(home, `${tag} M2 place`);
          await storyOf(second).getByRole("link").click({ button: "right" });
          await checkMenu(`${tag} M3 menu`);
          await page.getByRole("menuitem", { name: "Mark as read", exact: true }).click(); // uat-labels: ignore (a menu item's text, not an aria-label)
          check(await itemIs(second, "read"), `${tag} M3 read`, "Mark as read marks the story", "the story was not marked read"); // uat-labels: ignore (a menu item's text, not an aria-label)
          check(page.url().includes("/l/all"), `${tag} M3 stays`, "the list is still open", `now at ${page.url()}`);
          await stayed(home, `${tag} M3 place`);
        } else {
          const more = storyOf(first).getByRole("button", { name: /^More actions/ });
          const seen = await more.evaluate((e) => ({ opacity: getComputedStyle(e).opacity, events: getComputedStyle(e).pointerEvents }));
          check(seen.opacity === "0" && seen.events === "none", `${tag} M4 button`, "the More button is not seen or tapped on touch, and is in the accessibility tree", `opacity ${seen.opacity}, pointer-events ${seen.events}`);
          // A touch lands on whatever is at the point, so the story must be on screen.
          await storyOf(first).getByRole("link").scrollIntoViewIfNeeded();
          await page.waitForTimeout(300);
          const box = await storyOf(first).getByRole("link").boundingBox();
          const cdp = await page.context().newCDPSession(page);
          const touch = (type, points) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points });
          await touch("touchStart", [{ x: Math.round(box.x + 10), y: Math.round(box.y + box.height / 2) }]);
          await page.waitForTimeout(800);
          await touch("touchEnd", []);
          await checkMenu(`${tag} M4 menu`);
          check(page.url().includes("/l/all"), `${tag} M4 not opened`, "the long press did not open the article", `now at ${page.url()}`);
          await t.shot(page, `${tag}-gazette-menu`);
          await page.getByRole("menuitem", { name: "Star", exact: true }).click();
          check(await itemIs(first, "starred"), `${tag} M4 star`, "Star stars the story", "the story was not starred");
          await stayed(home, `${tag} M4 place`);
          await storyOf(third).getByRole("link").tap();
          await page.waitForURL(`**/i/${third}?*`, { timeout: 8000 }).then(
            () => t.pass(`${tag} M5 tap`, "a tap still opens the story"),
            () => t.fail(`${tag} M5 tap`, `a tap did not open the story (at ${page.url()})`),
          );
        }
      } finally {
        await patch({ "client.layout": null });
      }
    });
  });
} finally {
  server.close();
}

await t.finish("Gazette story menu");
