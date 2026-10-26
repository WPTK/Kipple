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
import { setTimeout as delay } from "node:timers/promises";
import { start, waitForStillFor } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

// A 1x1 GIF: a raster image the proxy passes.
const GIF = Buffer.from("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", "base64");
const SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><rect width="200" height="100" fill="#c33"/></svg>';

/**
 * What became of one article image once the browser had finished with it: "loaded" (it drew), "failed" (its fetch
 * ended and it has no picture), "unfetched" (no response for its address within 10 s: the fetch never started),
 * "timeout" (a response came but the image did not finish within 10 s) or "gone" (the image is not in the article).
 * It scrolls the image into view first, because lazy loading starts on nearness to the viewport. A load only counts
 * once the page has received a response for the image's address (`seen`, filled by the flow): `complete` with a
 * `currentSrc` alone is not proof, since a lazy image whose fetch has not begun can look finished in some browsers
 * (see `watchImages`). Then the state is polled with one short read at a time, so there is no handle to dispose, and
 * every read is bounded by the one 10 s deadline. A closed page or any other error is thrown, not reported as a state.
 *
 * The first finished state is the settled one here: the app replaces an image after its error event only when the
 * image proxy answers 415 (its one extra request goes to the same address after the error, and a 200 or 500 there
 * leaves the image as it is), and this flow's server answers the failing image with a fixed 500 and the GIF with a
 * fixed 200.
 */
const finished = async (page, img, seen) => {
  const deadline = Date.now() + 10000;
  const left = () => Math.max(1, deadline - Date.now());
  try {
    if ((await img.count()) === 0) return "gone";
    await img.scrollIntoViewIfNeeded({ timeout: left() });
    const src = await img.evaluate((el) => el.src, undefined, { timeout: left() });
    while (!seen.has(src)) {
      if (Date.now() > deadline) return "unfetched";
      await delay(100); // a poll interval
    }
    for (;;) {
      const state = await img.evaluate((el) => (!el.isConnected ? "gone" : el.complete && el.currentSrc ? (el.naturalWidth > 0 ? "loaded" : "failed") : ""), undefined, { timeout: left() });
      if (state) return state;
      if (Date.now() > deadline) return "timeout";
      await delay(50); // a poll interval
    }
  } catch (e) {
    if ((await img.count().catch(() => 1)) === 0) return "gone";
    if (e.name === "TimeoutError") return "timeout";
    throw e;
  }
};
const WHY = {
  unfetched: "the page got no response for it within 10 s (its fetch never started)",
  timeout: "a response came but its load did not finish within 10 s",
  failed: "its fetch failed",
  loaded: "it loaded",
  gone: "the image is not in the article",
};

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
    // Every response the page gets, from before the article opens: a lazy image may be fetched before the flow looks.
    const seen = new Set();
    const onResponse = (r) => seen.add(r.url());
    page.on("response", onResponse);
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
    // The pictures are lazy: the browser starts a fetch only when one nears the screen, and at the phone preset the
    // article is long enough that it has not begun when the last cue appears. So bring the GIF and then the failing
    // image into view, and wait for each one's response and for the browser to finish with it.
    const shown = await finished(page, photo, seen);
    check(shown === "loaded", `${tag} I3 gif`, "the GIF is shown", `the GIF did not load: ${WHY[shown]}`);
    check((await body.locator('img[width="1"]').count()) === 1, `${tag} I3 pixel`, "the tracking pixel gets no cue", "the tracking pixel was replaced");
    const flaky = await finished(page, body.locator('img[alt="Flaky"]'), seen);
    check(flaky === "failed", `${tag} I3 transient`, "an image whose source is down stays an image (it may load next time), with no cue", `the failing image is not a failed image: ${WHY[flaky]}`);
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
    // The row's lead picture is the article's first image: the opening article shows it (as the list card) while its text
    // loads, so that one address is asked for at once. The images below the fold are the ones counted.
    const lead = Buffer.from(`${base}/photo.gif?n=1`).toString("base64url");
    const onRequest = (r) => {
      const { pathname } = new URL(r.url());
      if (pathname.startsWith("/img/") && !pathname.endsWith(`/${lead}`)) asked.push(r.resourceType());
    };
    if (vp.mobile) await page.goBack();
    const lazyRow = page.getByText("UAT thirty pictures").first();
    page.on("request", onRequest);
    await lazyRow.click();
    await body.getByText("Picture 30 ends the article.").waitFor({ timeout: 15000 });
    // Negative check: no request for 1000 ms after the article rendered, longer than the lazy-load observer and a fetch need.
    await waitForStillFor(page, 1000, () => asked.length, { label: "the below-the-fold image requests to stop" });
    check(asked.length === 0, `${tag} I5 lazy`, "no image below the fold is requested or checked before scrolling", `${asked.length} requests: ${JSON.stringify(asked)}`);
    await body.getByText("Picture 30 ends the article.").scrollIntoViewIfNeeded();
    const last = body.locator('img[alt="Picture 30"]');
    const loaded = await finished(page, last, seen);
    check(loaded === "loaded" && !asked.includes("fetch"), `${tag} I5 scrolled`, "scrolled into view, the images load with no extra check", `${WHY[loaded]}, requests ${JSON.stringify(asked)}`);
    page.off("request", onRequest);
    page.off("response", onResponse);
  });
});

server.close();
await t.finish("image");
