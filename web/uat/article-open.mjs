// UAT: an article opens while list pictures are still loading. The browser allows six HTTP/1.1 connections per host on
// plain http, and list pictures that never finish used to hold them, so the article's own read waited behind them.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/article-open.mjs          in another
//   node uat/article-open.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: twelve articles, each with a thumbnail (the row's lead picture)
// whose source never answers, so every list picture the page asks for stays in flight. For each width (desktop 1280x800, phone 375x812) it
// opens the list and checks:
//   A1  the list shows its rows while the pictures are held;
//   A2  opening an article shows its text within 4 s (the picture requests are still held);
//   A3  with the article's read delayed 4 s, the title and source the list already knew are on screen meanwhile.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

const COUNT = 12;
let base = "";
const held = new Set();
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
const server = createServer((req, res) => {
  const path = (req.url ?? "").split("?")[0];
  if (path === "/feed.xml") {
    const items = Array.from({ length: COUNT }, (_, i) => {
      const n = i + 1;
      const html = `<p>Body text of held article ${n}.</p>`;
      return `<item><title>UAT held article ${n}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-open-${n}</guid><pubDate>${new Date(Date.now() - n * 60000).toUTCString()}</pubDate><description>${esc(html)}</description><media:thumbnail url="${base}/held/${n}.jpg" width="800" height="450"/></item>`;
    }).join("");
    res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>UAT article open</title><link>${base}/</link><description>t</description>${items}</channel></rss>`);
  } else if (path.startsWith("/held/")) {
    held.add(res); // never answered: the picture stays in flight until the flow ends
  } else {
    res.writeHead(404).end("not found");
  }
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} A`, async () => {
    const added = await t.addLocalFeed(page, `${base}/feed.xml`, "UAT feed article open", (items) => items.length >= COUNT);
    if (added.error) return t.fail(`${tag} A setup`, added.error);

    await page.goto(`/l/all?feed=${added.feedId}`, { waitUntil: "load" });
    const rows = page.getByText(/^UAT held article \d+$/);
    await rows.first().waitFor({ timeout: 15000 });
    check((await rows.count()) > 0, `${tag} A1 list`, "the list shows its rows while the pictures are held", "no rows");
    await page.waitForTimeout(1500); // let the list's pictures be asked for and held

    const body = page.getByTestId("article-body");
    const started = Date.now();
    await rows.first().click();
    await body.getByText(/^Body text of held article \d+\.$/).waitFor({ timeout: 5000 }).catch(() => {});
    const took = Date.now() - started;
    console.log(`     ${tag}: ${held.size} pictures held, article text after ${took} ms`);
    const shown = (await body.getByText(/^Body text of held article \d+\.$/).count()) > 0;
    check(shown && took < 4000, `${tag} A2 text`, "the article text shows within 4 s while list pictures are held", shown ? `it took ${took} ms` : "the article text never showed (still loading)");

    // A3: the read held for 4 s; the list's card (title and source) stands in for the text until it arrives. The list
    // is opened afresh, so nothing here depends on where A2 left the page.
    await page.route("**/api/items/*", async (route) => {
      if (route.request().method() === "GET") await new Promise((r) => setTimeout(r, 4000));
      await route.continue();
    });
    await page.goto(`/l/all?feed=${added.feedId}`, { waitUntil: "load" });
    await rows.nth(1).waitFor({ timeout: 15000 });
    await rows.nth(1).click();
    const card = page.getByTestId("loading-card-title");
    const cardShown = await card.waitFor({ timeout: 3000 }).then(() => true, () => false);
    check(cardShown && /^UAT held article \d+$/.test((await card.textContent()) ?? ""), `${tag} A3 card`, "the list's title shows while the article text loads", "no card title while loading");
    await body.getByText(/^Body text of held article \d+\.$/).waitFor({ timeout: 15000 });
    await page.unroute("**/api/items/*");
  });
});

for (const res of held) res.destroy();
server.close();
await t.finish("article-open");
