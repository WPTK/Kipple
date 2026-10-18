// UAT: an article image Kipple does not show (an SVG) gets an "Image not shown" cue instead of a blank gap.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:images                 in another
//   npm run uat:images -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: one article with an SVG whose address says so (never sent to the
// browser), an SVG whose address does not (refused by the image proxy once its bytes are seen), a 1x1 SVG tracking
// pixel, a GIF and an image whose source answers 500, plus the two other articles named below. It needs nothing from the
// seed's feeds, so it runs against the public set as well as the fixture feeds CI uses. For each width (desktop
// 1280x800, phone 375x812) it opens the article and checks:
//   I1  each SVG is an "Image not shown" cue with its alt text, no image is left in its place, and the cue keeps the
//       box the image declared;
//   I2  the cue's "Open original" link goes to the article's own address and opens in a new tab;
//   I3  the GIF is shown, the tracking pixel gets no cue, and the image whose source is down stays an image (no cue);
//   I4  back to the list and the same article again (from the app's cache): the cues are there again;
//   I5  a second article of 30 lazy images below the fold sends no image request or check until scrolled, then loads.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { Buffer } from "node:buffer";
import { createServer } from "node:http";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
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
  `<p><img src="${base}/flaky.gif" alt="Flaky"></p>` +
  `<p>The end.</p>`;
// Far more text than any browser's lazy-load margin, then 30 pictures: none is near the first screen.
const filler = "A long paragraph of text that keeps the pictures well below the first screen of the article. ".repeat(6);
const lazyArticle = () =>
  `<p>Thirty pictures follow, far below.</p>` +
  `<p>${filler}</p>`.repeat(80) +
  Array.from({ length: 30 }, (_, i) => `<p><img src="${base}/photo.gif?n=${i + 1}" alt="Picture ${i + 1}" width="600" height="400"></p>`).join("") +
  `<p>Picture 30 ends the article.</p>`;
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
const server = createServer((req, res) => {
  const path = (req.url ?? "").split("?")[0];
  if (path === "/feed.xml") {
    const item =
      `<item><title>UAT images article</title><link>${base}/page/1</link><guid isPermaLink="false">uat-images-1</guid><pubDate>${new Date().toUTCString()}</pubDate><description>${esc(article())}</description></item>` +
      `<item><title>UAT thirty pictures</title><link>${base}/page/2</link><guid isPermaLink="false">uat-images-2</guid><pubDate>${new Date(Date.now() - 60000).toUTCString()}</pubDate><description>${esc(lazyArticle())}</description></item>` +
      // The other article I4 opens on the desktop: the flow's own, so it does not depend on which feeds the seed added.
      `<item><title>UAT plain article</title><link>${base}/page/3</link><guid isPermaLink="false">uat-images-3</guid><pubDate>${new Date(Date.now() - 120000).toUTCString()}</pubDate><description>${esc("<p>No pictures here.</p>")}</description></item>`;
    res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT images</title><link>${base}/</link><description>t</description>${item}</channel></rss>`);
  } else if (path === "/chart.svg" || path === "/badge" || path === "/pixel.svg") {
    res.writeHead(200, { "Content-Type": "image/svg+xml" }).end(SVG);
  } else if (path === "/photo.gif") {
    res.writeHead(200, { "Content-Type": "image/gif" }).end(GIF);
  } else if (path === "/flaky.gif") {
    res.writeHead(500).end("down");
  } else {
    res.writeHead(404).end("not found");
  }
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} I`, async () => {
    const added = await t.addLocalFeed(page, `${base}/feed.xml`, "UAT feed images", (items) => items.some((i) => i.title === "UAT images article"));
    if (added.error) return t.fail(`${tag} I setup`, added.error);

    const body = page.getByTestId("article-body");
    const cues = body.locator(".kp-img-off");
    const verify = async (id) => {
      await body.waitFor({ timeout: 15000 });
      await body.getByText("The end.").scrollIntoViewIfNeeded();
      await cues.nth(1).waitFor({ timeout: 15000 }).catch(() => {});
      const texts = await cues.locator(":scope > span").allInnerTexts();
      check(
        texts.length === 2 && texts.includes("Image not shown: A chart of rivers") && texts.includes("Image not shown: Build passing"),
        `${tag} ${id} cues`,
        "both SVGs are an Image not shown cue with their alt text",
        `cues: ${JSON.stringify(texts)}`,
      );
      check((await body.locator('img[alt="A chart of rivers"], img[alt="Build passing"]').count()) === 0, `${tag} ${id} no gap`, "no image is left where an SVG was", "an SVG's image is still in the article");
    };

    // Opened from its feed's list, as a reader would, so I4 can come back to the list and open it again in the app.
    const row = page.getByText("UAT images article").first();
    await page.goto("/l/all", { waitUntil: "load" });
    await row.click();
    await verify("I1");
    const chart = await cues.filter({ hasText: "A chart of rivers" }).boundingBox();
    check(chart && Math.abs(chart.height - chart.width / 2) <= 2, `${tag} I1 box`, "the chart's cue keeps its declared 2:1 box", `box ${JSON.stringify(chart)}`);

    const link = cues.first().getByRole("link", { name: "Open original" });
    const href = await link.getAttribute("href").catch(() => null);
    const target = await link.getAttribute("target").catch(() => null);
    check(href === `${base}/page/1` && target === "_blank", `${tag} I2 link`, "Open original goes to the article and opens a new tab", `href ${href}, target ${target}`);

    const photo = body.locator('img[alt="A photo"]');
    const shown = await photo.evaluate((i) => i.complete && i.naturalWidth > 0).catch(() => false);
    check(shown, `${tag} I3 gif`, "the GIF is shown", "the GIF did not load");
    check((await body.locator('img[width="1"]').count()) === 1, `${tag} I3 pixel`, "the tracking pixel gets no cue", "the tracking pixel was replaced");
    const flaky = await body.locator('img[alt="Flaky"]').evaluate((i) => i.complete && i.naturalWidth === 0).catch(() => null);
    check(flaky === true, `${tag} I3 transient`, "an image whose source is down stays an image (it may load next time), with no cue", `the failing image is ${flaky === null ? "gone" : "not failed"}`);
    await cues.first().scrollIntoViewIfNeeded();
    await t.shot(page, `${tag}-image-not-shown`);

    // I4: back to the list and the same article again, from the app's cache, into a new body.
    // On the desktop the list stays beside the article; another article first, so the reopen is a new render.
    if (vp.mobile) await page.goBack();
    else await page.getByText("UAT plain article").first().click();
    await row.click();
    await verify("I4 reopened");

    // I5: an article of 30 lazy images below the fold sends no image request, and no check, until it is scrolled.
    const asked = [];
    const onRequest = (r) => {
      if (new URL(r.url()).pathname.startsWith("/img/")) asked.push(r.resourceType());
    };
    if (vp.mobile) await page.goBack();
    const lazyRow = page.getByText("UAT thirty pictures").first();
    page.on("request", onRequest);
    await lazyRow.click();
    await body.getByText("Picture 30 ends the article.").waitFor({ timeout: 15000 });
    await page.waitForTimeout(1500);
    check(asked.length === 0, `${tag} I5 lazy`, "no image below the fold is requested or checked before scrolling", `${asked.length} requests: ${JSON.stringify(asked)}`);
    await body.getByText("Picture 30 ends the article.").scrollIntoViewIfNeeded();
    const last = body.locator('img[alt="Picture 30"]');
    const loaded = await last
      .evaluate((i) => (i.complete && i.naturalWidth > 0) || new Promise((ok) => i.addEventListener("load", () => ok(true), { once: true })))
      .catch(() => false);
    check(loaded === true && !asked.includes("fetch"), `${tag} I5 scrolled`, "scrolled into view, the images load with no extra check", `loaded ${loaded}, requests ${JSON.stringify(asked)}`);
    page.off("request", onRequest);
  });
});

server.close();
await t.finish("image");
