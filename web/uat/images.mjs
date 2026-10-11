// UAT: an article image Kipple does not show (an SVG) gets an "Image not shown" cue instead of a blank gap.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:images                 in another
//   npm run uat:images -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: one article with an SVG whose address says so (never sent to the
// browser), an SVG whose address does not (refused by the image proxy once its bytes are seen), a 1x1 SVG tracking
// pixel and a GIF. For each width (desktop 1280x800, phone 375x812) it opens the article and checks:
//   I1  each SVG is an "Image not shown" cue with its alt text, and no image is left in its place;
//   I2  the cue's "Open original" link goes to the article's own address and opens in a new tab;
//   I3  the GIF is shown, and the tracking pixel gets no cue.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { Buffer } from "node:buffer";
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

// A 1x1 GIF: a raster image the proxy passes.
const GIF = Buffer.from("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", "base64");
const SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><rect width="200" height="100" fill="#c33"/></svg>';

let base = "";
const article = () =>
  `<p>An article with pictures.</p>` +
  `<p><img src="${base}/chart.svg" alt="A chart of rivers" width="600" height="300"></p>` +
  `<p><img src="${base}/badge?style=flat" alt="Build passing"></p>` +
  `<p><img src="${base}/pixel.svg" width="1" height="1" alt=""></p>` +
  `<p><img src="${base}/photo.gif" alt="A photo" width="8" height="8"></p>` +
  `<p>The end.</p>`;
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
const server = createServer((req, res) => {
  const path = (req.url ?? "").split("?")[0];
  if (path === "/feed.xml") {
    const item = `<item><title>UAT images article</title><link>${base}/page/1</link><guid isPermaLink="false">uat-images-1</guid><pubDate>${new Date().toUTCString()}</pubDate><description>${esc(article())}</description></item>`;
    res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT images</title><link>${base}/</link><description>t</description>${item}</channel></rss>`);
  } else if (path === "/chart.svg" || path === "/badge" || path === "/pixel.svg") {
    res.writeHead(200, { "Content-Type": "image/svg+xml" }).end(SVG);
  } else if (path === "/photo.gif") {
    res.writeHead(200, { "Content-Type": "image/gif" }).end(GIF);
  } else {
    res.writeHead(404).end("not found");
  }
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} I`, async () => {
    const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url: `${base}/feed.xml`, title: "UAT feed images", allow_private_net: true } });
    if (!add.ok()) return t.fail(`${tag} I setup`, `the feed was not added: ${add.status()} ${await add.text()}`);
    const feedId = (await add.json()).id ?? (await t.boot(page)).feeds.find((f) => f.title === "UAT feed images")?.id;
    await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
    let itemId;
    const end = Date.now() + 30000;
    while (!itemId && Date.now() < end) {
      const r = await page.request.get(`/api/items?view=all&feed=${feedId}`, { headers: { Accept: "application/json" } });
      itemId = r.ok() ? (await r.json()).items?.[0]?.id : undefined;
      if (!itemId) await page.waitForTimeout(500);
    }
    if (!itemId) return t.fail(`${tag} I setup`, "the feed's article never arrived (was it fetched?)");

    await page.goto(`/i/${itemId}?from=all`, { waitUntil: "load" });
    const body = page.getByTestId("article-body");
    await body.waitFor({ timeout: 15000 });
    await body.getByText("The end.").scrollIntoViewIfNeeded();
    const cues = body.locator(".kp-img-off");
    await cues.nth(1).waitFor({ timeout: 15000 }).catch(() => {});
    const texts = await cues.locator(":scope > span").allInnerTexts();
    check(
      texts.length === 2 && texts.includes("Image not shown: A chart of rivers") && texts.includes("Image not shown: Build passing"),
      `${tag} I1 cues`,
      "both SVGs are an Image not shown cue with their alt text",
      `cues: ${JSON.stringify(texts)}`,
    );
    check((await body.locator('img[alt="A chart of rivers"], img[alt="Build passing"]').count()) === 0, `${tag} I1 no gap`, "no image is left where an SVG was", "an SVG's image is still in the article");

    const link = cues.first().getByRole("link", { name: "Open original" });
    const href = await link.getAttribute("href").catch(() => null);
    const target = await link.getAttribute("target").catch(() => null);
    check(href === `${base}/page/1` && target === "_blank", `${tag} I2 link`, "Open original goes to the article and opens a new tab", `href ${href}, target ${target}`);

    const photo = body.locator('img[alt="A photo"]');
    const shown = await photo.evaluate((i) => i.complete && i.naturalWidth > 0).catch(() => false);
    check(shown, `${tag} I3 gif`, "the GIF is shown", "the GIF did not load");
    check((await body.locator('img[width="1"]').count()) === 1, `${tag} I3 pixel`, "the tracking pixel gets no cue", "the tracking pixel was replaced");
    await cues.first().scrollIntoViewIfNeeded();
    await t.shot(page, `${tag}-image-not-shown`);
  });
});

server.close();
await t.finish("image");
