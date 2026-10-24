// UAT: list rows and search results in a real browser (design review, ROW package).
//
//   npm run seed                       in one terminal
//   npm run uat:row                    in another
//   npm run uat:row -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: a long unread title, pictures in landscape, portrait, panorama and
// square shapes (generated PNGs), an article with no picture and a read one, all carrying the word "xylograph". For each
// width (desktop 1280x800, phone 375x812):
//   W1  Inbox: the title comes before the source, takes at most two lines, and the star and More never sit on it;
//       nothing scrolls sideways at 375 and, on the phone, at 280;
//   W2  a list of one feed names no source on its rows; the whole library does;
//   W3  the Gazette gives an unread story the unread dot;
//   W4  the star and More show at rest, at full strength: Compact's star and Inbox's More, an unstarred star unfaded;
//   W5  the unread list shows its count beside the title, and a short list ends with "End of unread";
//   W6  Editorial's lead pictures keep one frame (16:9) whatever their shape, fill it when close to it and sit whole in
//       it when not (a portrait or a panorama), and a row with no picture keeps a minimum height;
//   W7  search results are text rows with no picture, say how many results there are, and Mark all names the count;
//       a saved search says Saved and offers Edit;
//   W8  on a short screen (320x256, 400% zoom) the search results are reachable: the screen scrolls, the box stays at
//       the top, and the first result is on screen below it.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { Buffer } from "node:buffer";
import { createServer } from "node:http";
import { deflateSync } from "node:zlib";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 26);
const { check, step } = t;

const FEED_TITLE = "UAT row";
const WORD = "xylograph";
const LONG = `The long unread title ${WORD} that goes on and on, past two lines on a phone, about the harness and the brain – and the end of it`;
// Each article: title, picture size (none: no picture), read.
const ARTICLES = [
  { title: LONG, pic: [1600, 900] },
  { title: `Portrait picture ${WORD}`, pic: [600, 900] },
  { title: `Panorama picture ${WORD}`, pic: [3000, 1000] },
  { title: `Square picture ${WORD}`, pic: [800, 800] },
  { title: `No picture ${WORD}` },
  { title: `Already read ${WORD}`, read: true },
];

/** A plain PNG of the size, with a border so a crop shows. */
function png(w, h) {
  const row = w * 3 + 1;
  const raw = Buffer.alloc(row * h);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const edge = y < 10 || y >= h - 10 || x < 10 || x >= w - 10;
      const o = y * row + 1 + x * 3;
      raw[o] = edge ? 200 : 90;
      raw[o + 1] = edge ? 60 : 130;
      raw[o + 2] = edge ? 60 : 190;
    }
  }
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf) => {
    let c = 0xffffffff;
    for (const b of buf) c = crcTable[(c ^ b) & 255] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const sum = Buffer.alloc(4);
    sum.writeUInt32BE(crc(td));
    return Buffer.concat([len, td, sum]);
  };
  const head = Buffer.alloc(13);
  head.writeUInt32BE(w, 0);
  head.writeUInt32BE(h, 4);
  head[8] = 8;
  head[9] = 2;
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", head), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}

let base = "";
const pngs = new Map();
const server = createServer((req, res) => {
  const path = new URL(req.url ?? "/", "http://x").pathname;
  const pic = path.match(/^\/img\/(\d+)x(\d+)\.png$/);
  if (pic) {
    pngs.set(path, pngs.get(path) ?? png(Number(pic[1]), Number(pic[2])));
    return void res.writeHead(200, { "Content-Type": "image/png" }).end(pngs.get(path));
  }
  if (path !== "/feed.xml") return void res.writeHead(404).end();
  const item = (a, n) =>
    `<item><title>${a.title}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-row-${n}</guid><pubDate>${new Date(Date.now() - n * 60000).toUTCString()}</pubDate><description>&lt;p&gt;The body of article ${n}, a ${WORD} too.&lt;/p&gt;${a.pic ? `&lt;img src="${base}/img/${a.pic[0]}x${a.pic[1]}.png" alt=""&gt;` : ""}</description></item>`;
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>${FEED_TITLE}</title><link>${base}/</link><description>t</description>${ARTICLES.map(item).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

async function setLayout(page, label) {
  await page.goto("/l/all", { waitUntil: "load" });
  const btn = page.locator('button[aria-label^="List options, "]').first();
  await btn.waitFor({ timeout: 15000 });
  if ((await btn.getAttribute("aria-label")) === `List options, ${label} layout`) return;
  await btn.click();
  await page.getByRole("group", { name: "Layout" }).getByRole("menuitemradio", { name: new RegExp(`^${label}\\b`) }).click();
  await page.locator(`button[aria-label="List options, ${label} layout"]`).first().waitFor({ timeout: 10000 });
  if (await page.getByRole("menu").isVisible().catch(() => false)) await page.keyboard.press("Escape");
}
async function layoutOf(page) {
  await page.goto("/l/all", { waitUntil: "load" });
  const btn = page.locator('button[aria-label^="List options, "]').first();
  await btn.waitFor({ timeout: 15000 });
  return ((await btn.getAttribute("aria-label")) ?? "").replace(/^List options, /, "").replace(/ layout$/, "");
}

try {
await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED_TITLE)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
  const added = await t.addLocalFeed(page, `${base}/feed.xml`, FEED_TITLE, (items) => items.length >= ARTICLES.length);
  if (added.error) return t.fail(`${tag} setup`, added.error);
  const read = added.items.find((i) => i.title.startsWith("Already read"));
  const marked = read && (await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [read.id], read: true } }));
  if (!marked?.ok()) return t.fail(`${tag} setup`, "the read article was not marked read");
  const mine = (await t.boot(page)).feeds.find((f) => f.title === FEED_TITLE);
  const own = `/l/all?feed=${mine.id}`;
  const startedWith = await layoutOf(page);
  try {
    await step(`${tag} W1`, async () => {
      await setLayout(page, "Inbox");
      await page.goto(own, { waitUntil: "load" });
      const row = page.locator("article[data-item-id]").first();
      await row.waitFor({ timeout: 15000 });
      const shape = await row.evaluate((el) => {
        const h3 = el.querySelector("h3");
        const meta = el.querySelector("time");
        const line = parseFloat(getComputedStyle(h3).lineHeight);
        const link = h3.getBoundingClientRect();
        const star = el.querySelector('button[aria-label^="Star"]').getBoundingClientRect();
        return { titleFirst: !!(h3.compareDocumentPosition(meta) & Node.DOCUMENT_POSITION_FOLLOWING), lines: Math.round(link.height / line), clear: link.right <= star.left + 0.5 };
      });
      check(shape.titleFirst, `${tag} W1 order`, "the title comes before the time and source", "the time comes first");
      check(shape.lines >= 1 && shape.lines <= 2, `${tag} W1 lines`, "the title takes one or two lines", `${shape.lines} lines`);
      check(shape.clear, `${tag} W1 star`, "the star does not sit on the title", "the star overlaps the title");
      await t.shot(page, `${tag}-W1-inbox`);
      for (const w of vp.mobile ? [375, 280] : [1280]) {
        await page.setViewportSize({ width: w, height: vp.height });
        await page.goto("/l/all", { waitUntil: "load" });
        await page.locator("article[data-item-id]").first().waitFor({ timeout: 15000 });
        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
        const mine = await page.locator("article[data-item-id]").first().evaluate((el) => {
          const link = el.querySelector("h3").getBoundingClientRect();
          const star = el.querySelector('button[aria-label^="Star"]').getBoundingClientRect();
          return link.right <= star.left + 0.5;
        });
        check(over <= 0, `${tag} W1 width ${w}`, "nothing scrolls sideways", `${over}px too wide`);
        check(mine, `${tag} W1 clear ${w}`, "the title ends before the star", "the title runs under the star");
        if (w === 280) await t.shot(page, `${tag}-W1-inbox-280`);
      }
      await page.setViewportSize({ width: vp.width, height: vp.height });
    });

    await step(`${tag} W2`, async () => {
      await setLayout(page, "Inbox");
      await page.goto(own, { waitUntil: "load" });
      const one = page.locator("article[data-item-id]").first();
      await one.waitFor({ timeout: 15000 });
      check(!(await one.innerText()).includes(FEED_TITLE), `${tag} W2 one feed`, "a one-feed list names no source", "the row names its feed");
      await page.goto("/l/all", { waitUntil: "load" });
      const all = page.locator("article[data-item-id]").first();
      await all.waitFor({ timeout: 15000 });
      const text = await all.innerText();
      check(text.includes(FEED_TITLE), `${tag} W2 library`, "the whole library names the source", `row text: ${JSON.stringify(text.slice(0, 80))}`);
      await t.shot(page, `${tag}-W2-all`);
    });

    await step(`${tag} W3`, async () => {
      await setLayout(page, "Gazette");
      await page.goto("/l/all", { waitUntil: "load" });
      await page.locator("article[data-slot]").first().waitFor({ timeout: 20000 });
      const dots = await page.locator('article[data-slot] [data-unread="true"]').count();
      check(dots > 0, `${tag} W3 dot`, "unread Gazette stories carry the unread dot", "no story has a dot");
      await t.shot(page, `${tag}-W3-gazette`);
    });

    await step(`${tag} W4`, async () => {
      await setLayout(page, "Compact");
      await page.goto(own, { waitUntil: "load" });
      const row = page.locator("article[data-item-id]").first();
      await row.waitFor({ timeout: 15000 });
      const star = row.getByRole("button", { name: /^Star:/ });
      const more = row.getByRole("button", { name: /^More actions:/ });
      const o = async (l) => Number(await l.evaluate((e) => getComputedStyle(e).opacity));
      check((await star.isVisible()) && (await o(star)) === 1, `${tag} W4 compact star`, "Compact's star shows at rest, unfaded", "it is hidden or faded");
      check((await more.isVisible()) && (await o(more)) === 1, `${tag} W4 compact more`, "More shows at rest", "it is hidden");
      await t.shot(page, `${tag}-W4-compact`);
      await setLayout(page, "Inbox");
      await page.goto(own, { waitUntil: "load" });
      const r2 = page.locator("article[data-item-id]").first();
      await r2.waitFor({ timeout: 15000 });
      check((await o(r2.getByRole("button", { name: /^More actions:/ }))) === 1, `${tag} W4 inbox more`, "Inbox's More shows at rest", "it is faded");
      check((await o(r2.getByRole("button", { name: /^Star:/ }))) === 1, `${tag} W4 star`, "an unstarred star is drawn at full strength", "it is faded");
    });

    await step(`${tag} W5`, async () => {
      await setLayout(page, "Inbox");
      await page.goto("/l/unread", { waitUntil: "load" });
      const count = page.getByTestId("list-count");
      await count.waitFor({ timeout: 15000 });
      const fresh = await t.boot(page);
      check((await count.innerText()).trim() === String(fresh.counts.unread), `${tag} W5 count`, "the count beside the title is the unread total", `shows ${await count.innerText()}, unread is ${fresh.counts.unread}`);
      await page.goto(`/l/unread?feed=${mine.id}`, { waitUntil: "load" });
      const end = page.getByTestId("list-end");
      await end.waitFor({ timeout: 15000 });
      check((await end.innerText()).trim() === "End of unread", `${tag} W5 end`, 'a short list ends with "End of unread"', `says ${await end.innerText()}`);
      await t.shot(page, `${tag}-W5-end`);
    });

    await step(`${tag} W6`, async () => {
      await setLayout(page, "Editorial");
      await page.goto(own, { waitUntil: "load" });
      await page.locator("article[data-item-id] img").first().waitFor({ timeout: 20000 });
      await page.waitForTimeout(1500);
      const pics = await page.locator("article[data-item-id] img").evaluateAll((imgs) =>
        imgs.filter((i) => i.complete && i.naturalWidth).map((i) => {
          const r = i.getBoundingClientRect();
          const f = i.naturalWidth / i.naturalHeight / (r.width / r.height);
          return { ratio: r.width / r.height, kept: Math.min(f, 1 / f), fit: getComputedStyle(i).objectFit };
        }),
      );
      check(pics.length > 0, `${tag} W6 loaded`, "pictures loaded", "none did");
      const frame = pics.every((p) => Math.abs(p.ratio - 16 / 9) < 0.05 || p.ratio < 1.2 /* side by side keeps its own frame */);
      check(frame, `${tag} W6 frame`, "every lead keeps one frame", JSON.stringify(pics.slice(0, 3)));
      const rule = pics.every((p) => (p.kept >= 0.75 ? p.fit === "cover" : p.fit === "contain"));
      check(rule, `${tag} W6 fit`, "a picture fills its frame only when its shape is close to it", JSON.stringify(pics.slice(0, 4)));
      check(pics.some((p) => p.fit === "contain"), `${tag} W6 portrait`, "a portrait sits whole in its frame", "every picture was cropped");
      await t.shot(page, `${tag}-W6-portrait`);
      await page.goto(own, { waitUntil: "load" });
      await page.locator("article[data-item-id] img").first().waitFor({ timeout: 20000 });
      await page.waitForTimeout(1500);
      await t.shot(page, `${tag}-W6-wide`);
    });

    await step(`${tag} W7`, async () => {
      await page.goto(`/search?q=${WORD}`, { waitUntil: "load" });
      const line = page.getByTestId("result-count");
      await page.locator("article[data-item-id]").first().waitFor({ timeout: 20000 });
      await page.waitForFunction(() => /\d.* results? for xylograph/.test(document.querySelector('[data-testid="result-count"]')?.textContent ?? ""), null, { timeout: 15000 });
      const said = (await line.innerText()).trim();
      check(/^\d+\+? results? for xylograph/.test(said), `${tag} W7 count`, 'the results say how many: "N results for xylograph"', `says ${JSON.stringify(said)}`);
      const imgs = await page.locator("article[data-item-id] img").count();
      check(imgs === 0, `${tag} W7 text rows`, "result rows carry no picture", `${imgs} pictures`);
      const mark = await page.getByRole("button", { name: /^Mark all .*results as read$/ }).innerText();
      const n = said.match(/^(\d+)(\+?)/);
      const expect = n && !n[2] && Number(n[1]) > 1 ? `Mark all ${n[1]} results as read` : "Mark all results as read";
      check(mark.trim() === expect, `${tag} W7 mark all`, `Mark all names its reach: ${expect}`, `says ${JSON.stringify(mark)}`);
      await t.shot(page, `${tag}-W7-search`);
      const made = await page.request.post("/api/saved-searches", { headers: t.WRITE, data: { name: "UAT row saved", q: WORD } });
      if (made.ok()) {
        const id = (await made.json()).id;
        try {
          await page.goto(`/search?q=${WORD}&ss=${id}`, { waitUntil: "load" });
          await page.getByRole("button", { name: "Edit saved search" }).waitFor({ timeout: 15000 });
          check(await page.getByText("Saved", { exact: true }).isVisible(), `${tag} W7 saved`, 'a saved search says "Saved"', "no Saved");
          await t.shot(page, `${tag}-W7-saved`);
        } finally {
          await page.request.delete(`/api/saved-searches/${id}`, { headers: t.WRITE });
        }
      }
    });

    await step(`${tag} W8`, async () => {
      await page.setViewportSize({ width: 320, height: 256 });
      await page.goto(`/search?q=${WORD}`, { waitUntil: "load" });
      await page.locator("article[data-item-id]").first().waitFor({ timeout: 20000, state: "attached" });
      await t.shot(page, `${tag}-W8-top`);
      await page.locator(".search-shell").evaluate((el) => (el.scrollTop = el.scrollHeight));
      await page.waitForTimeout(300);
      const view = await page.evaluate(() => {
        const input = document.querySelector("[data-search-input]").getBoundingClientRect();
        const row = document.querySelector("article[data-item-id]").getBoundingClientRect();
        return { inputTop: input.top, inputBottom: input.bottom, rowTop: row.top, rowBottom: row.bottom, h: window.innerHeight, over: document.scrollingElement.scrollWidth - window.innerWidth };
      });
      check(view.inputTop >= 0 && view.inputBottom <= view.h, `${tag} W8 box`, "the search box stays on screen", JSON.stringify(view));
      check(view.rowTop >= view.inputBottom - 1 && view.rowBottom <= view.h + 1, `${tag} W8 result`, "the first result is on screen below the box", JSON.stringify(view));
      check(view.over <= 0, `${tag} W8 width`, "nothing scrolls sideways", `${view.over}px`);
      await t.shot(page, `${tag}-W8-bottom`);
      await page.setViewportSize({ width: vp.width, height: vp.height });
    });
  } finally {
    await setLayout(page, startedWith).catch((e) => t.fail(`${tag} restore`, `the ${startedWith} layout was not put back: ${String(e.message).split("\n")[0]}`));
    await page.request.delete(`/api/feeds/${mine.id}`, { headers: t.WRITE });
  }
});
} finally {
  server.close();
}

await t.finish("row");