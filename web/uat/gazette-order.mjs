// UAT: in the Gazette, an open article's previous and next follow the page's printed order (#315).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:gazette-order          in another
//   npm run uat:gazette-order -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: nine articles, newest first, of which only the fourth and the seventh
// carry a picture. In that feed's list the Gazette leads with the newest picture, so the page prints the fourth story
// first and the newest story second: not the order the server lists them in. For each width (desktop 1280x800, phone
// 375x812) it opens the feed's list with the Gazette as the layout and checks:
//   O1  with the first printed story open, Next steps through the page's stories in the order the list prints them
//       (on a phone no list is mounted while the article is open; on the desktop it sits beside the article);
//   O2  Previous steps back through the same stories, and is switched off on the first.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 15);
const { check, step } = t;

const FEED_TITLE = "UAT feed gazette order";
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
    return `<item><title>UAT gazette order story ${n}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-gazette-order-${n}</guid><pubDate>${new Date(Date.now() - n * 3600_000).toUTCString()}</pubDate><description>${body}</description></item>`;
  };
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT gazette order</title><link>${base}/</link><description>t</description>${Array.from({ length: COUNT }, (_, i) => item(i + 1)).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    const patch = (b) =>
      page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
    const path = () => new URL(page.url()).pathname;

    await step(`${tag} O`, async () => {
      for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED_TITLE)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
      const added = await t.addLocalFeed(page, `${base}/feed.xml`, FEED_TITLE, (items) => items.length >= COUNT);
      if (added.error) return t.fail(`${tag} O setup`, added.error);
      const set = await patch({ "client.layout": "gazette" });
      if (set !== 200) return t.fail(`${tag} O setup`, `the Gazette was not set as the layout (${set})`);
      try {
        await page.goto(`/l/all?feed=${added.feedId}`, { waitUntil: "load" });
        await page.locator("[data-gazette] article[data-item-id]").first().waitFor({ timeout: 20000 });
        const printed = await page.locator("[data-gazette] article[data-item-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId));
        const newest = added.items.map((i) => i.id);
        if (printed.length !== COUNT || printed.join() === newest.join()) {
          return t.fail(`${tag} O setup`, `the page must print all ${COUNT} stories in an order other than the server's: printed ${printed.join(" ")}, server ${newest.join(" ")}`);
        }

        await page.locator(`[data-gazette] article[data-item-id="${printed[0]}"] a`).first().click();
        await page.waitForURL(`**/i/${printed[0]}?*`, { timeout: 10000 });
        await page.getByTestId("article-body").waitFor({ timeout: 15000 });
        if (vp.mobile) {
          const mounted = await page.locator("[data-gazette]").count();
          check(mounted === 0, `${tag} O1 no list`, "no list is mounted while the article is open", `${mounted} Gazette page(s) are mounted`);
        }
        const next = page.getByRole("button", { name: "Next article" }).first();
        const prev = page.getByRole("button", { name: "Previous article" }).first();
        const seen = [path()];
        for (let i = 1; i < COUNT; i++) {
          await next.click();
          await page.waitForURL(`**/i/${printed[i]}?*`, { timeout: 10000 }).catch(() => {});
          seen.push(path());
        }
        const want = printed.map((id) => `/i/${id}`);
        check(seen.join() === want.join(), `${tag} O1`, `Next steps through all ${COUNT} stories in printed order`, `Next visited ${seen.join(" ")}, the page prints ${want.join(" ")}`);
        await t.shot(page, `${tag}-gazette-order-last`);
        const back = [path()];
        for (let i = COUNT - 2; i >= 0; i--) {
          await prev.click();
          await page.waitForURL(`**/i/${printed[i]}?*`, { timeout: 10000 }).catch(() => {});
          back.push(path());
        }
        check(back.join() === want.slice().reverse().join(), `${tag} O2`, "Previous steps back through them", `Previous visited ${back.join(" ")}`);
        await page.waitForFunction(() => document.querySelector('button[aria-label="Previous article"]')?.getAttribute("aria-disabled") === "true", null, { timeout: 5000 }).then(
          () => t.pass(`${tag} O2 first`, "Previous is off on the first story"),
          () => t.fail(`${tag} O2 first`, "Previous is on at the first story"),
        );
      } finally {
        await patch({ "client.layout": null });
      }
    });
  });
} finally {
  server.close();
}

await t.finish("Gazette article order");
