// UAT: reading typography of an article (design review, Read package).
//
//   npm run seed                       in one terminal
//   npm run uat:reading                in another
//   npm run uat:reading -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The flow serves its own feed on a loopback port (three articles: one with every block an article can have, one in
// Arabic, one with a tall picture), so it needs no particular seed. On a phone (375x812) and a desktop (1280x800) it
// opens them and checks what the browser computes:
//   R1  bullet lists show discs (circles when nested) and numbered lists decimals (the base style reset clears them);
//   R2  the title is 1.75 rem, the body headings step down from it (h2 1.2 em) and all are weight 650;
//   R3  a tall picture on its own line, bare or in a div, a figure or a link, is capped at min(70% of the window,
//       40 rem), is drawn contained and centred; an icon in a line of text and a row of icons keep their own size and
//       are not centred; a picture keeps the width attribute it was given (12 px on a 24 px file), and a capped one is as narrow
//       as its drawn shape;
//   R4  code and tables wider than the column are focusable and named ("Code"; a table keeps its caption as its name,
//       otherwise "Table"), axe's scrollable-region-focusable finds nothing, and a table in a figure is not centred text;
//   R5  on a wide desktop, where the column is narrower than the pane, in a reading font far wider than the app font,
//       the header and the body are one width and the column holds about 66 characters of that font (the Standard density);
//   R6  an Arabic article lays out right to left (its title, byline, and the body, whose paragraphs have no dir of
//       their own) while the "Read" line stays left to right;
//   R7  table headers start-align and a number cell sits on the end edge.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { deflateSync } from "node:zlib";
import { start } from "./common.mjs";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const t = await start(import.meta.url, 24);
const { check, step } = t;

const FEED = "UAT feed reading";
const ESSAY = "UAT reading essay with every block";
const ARABIC = "عنوان باللغة العربية يمتد عبر السطر";
const PICTURES = "UAT reading a tall picture";

/** A solid-colour PNG, so the flow needs no image files. */
function png(w, h, [r, g, b]) {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf) => {
    let c = 0xffffffff;
    for (const x of buf) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type), data]);
    const sum = Buffer.alloc(4);
    sum.writeUInt32BE(crc(body));
    return Buffer.concat([len, body, sum]);
  };
  const head = Buffer.alloc(13);
  head.writeUInt32BE(w, 0);
  head.writeUInt32BE(h, 4);
  head[8] = 8;
  head[9] = 2;
  const row = Buffer.concat([Buffer.from([0]), Buffer.from(Array.from({ length: w }, () => [r, g, b]).flat())]);
  const raw = Buffer.concat(Array.from({ length: h }, () => row));
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", head), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}
const IMAGES = { "/img/icon.png": png(24, 24, [200, 60, 60]), "/img/tall.png": png(400, 1000, [60, 120, 200]) };

let base = "";
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const wide = (n) => `<tr>${Array.from({ length: 8 }, (_, i) => `<${n}>${n === "th" ? `Column ${i + 1}` : i === 1 ? "6,650" : `Cell text number ${i + 1} in the row`}</${n}>`).join("")}</tr>`;
const tall = () => `<img src="${base}/img/tall.png" width="400" height="1000" alt="">`;
const icon = () => `<img src="${base}/img/icon.png" width="24" height="24" alt="">`;
// The 24 px picture, asked to be 12 px wide: the width attribute decides, not the picture's own size.
const small = () => `<img src="${base}/img/icon.png" width="12" height="12" alt="">`;
const essayHtml = () =>
  [
    "<h2>A second-level heading</h2>",
    `<p>A line of text with an icon ${icon()} inside it, and a small one ${small()} too.</p>`,
    `<p>${icon()}${icon()}${icon()}</p>`,
    "<ul><li>An item<ul><li>A nested item</li></ul></li><li>Another</li></ul>",
    "<ol><li>First step</li><li>Second step</li></ol>",
    `<pre><code>func main() {\n\tfmt.Println("a line of code wide enough to need sideways scrolling on a phone screen, and on a desktop pane too, which it should do")\n}</code></pre>`,
    `<table><caption>Rivers by length</caption><thead>${wide("th")}</thead><tbody>${wide("td")}</tbody></table>`,
    `<table><thead>${wide("th")}</thead><tbody>${wide("td")}</tbody></table>`,
    `<figure><table><thead>${wide("th")}</thead><tbody>${wide("td")}</tbody></table><figcaption>A table in a figure</figcaption></figure>`,
    tall(),
    `<div>${tall()}</div>`,
    `<figure>${tall()}<figcaption>A tall picture</figcaption></figure>`,
    `<p><a href="https://example.com/">${tall()}</a></p>`,
    "<p>The last paragraph.</p>",
  ].join("");
const server = createServer((req, res) => {
  const path = new URL(req.url ?? "/", "http://x").pathname;
  if (IMAGES[path]) return void res.writeHead(200, { "Content-Type": "image/png" }).end(IMAGES[path]);
  if (path !== "/feed.xml") return void res.writeHead(404).end();
  const item = (title, n, html, author = "") =>
    `<item><title>${esc(title)}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-reading-${n}</guid><pubDate>${new Date(Date.now() - n * 60000).toUTCString()}</pubDate>${author ? `<dc:creator>${esc(author)}</dc:creator>` : ""}<description>${esc(html)}</description></item>`;
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(
      `<?xml version="1.0"?><rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel><title>${FEED}</title><link>${base}/</link><description>t</description>` +
        item(ESSAY, 1, essayHtml()) +
        item(ARABIC, 2, "<p>نص عربي في المقالة، فقرة كاملة من اليمين إلى اليسار، من غير سمة اتجاه.</p>", "كاتب عربي") +
        item(PICTURES, 3, `<p>${tall()}</p>`) +
        "</channel></rss>",
    );
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

async function addFeed(page) {
  for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
  const added = await t.addLocalFeed(page, `${base}/feed.xml`, FEED, (items) => items.length >= 3);
  if (added.error) throw new Error(added.error);
  return added.feedId;
}

async function open(page, feedId, title) {
  await page.goto(`/l/all?feed=${feedId}`, { waitUntil: "load" });
  const rows = page.locator("[data-testid=list-scroll] [data-item-id]");
  await rows.first().waitFor({ timeout: 15000 });
  await rows.filter({ hasText: title }).first().locator("a").first().click();
  await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
  await page.getByTestId("article-body").waitFor({ timeout: 10000 });
  await page.waitForFunction(() => [...document.querySelectorAll("[data-testid=article-body] img")].every((i) => i.complete));
  await page.waitForTimeout(500); // the observers and enhancers run after the first paint
}

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    let feedId;
    await step(`${tag} setup`, async () => {
      feedId = await addFeed(page);
    });
    if (feedId === undefined) return;

    await step(`${tag} essay`, async () => {
      await open(page, feedId, ESSAY);
      const body = page.getByTestId("article-body");
      const m = await body.evaluate((el) => {
        const cs = (e) => getComputedStyle(e);
        return {
          ul: cs(el.querySelector("ul > li")).listStyleType,
          nested: cs(el.querySelector("ul ul > li")).listStyleType,
          ol: cs(el.querySelector("ol > li")).listStyleType,
          h2: cs(el.querySelector("h2")).fontSize,
          h2w: cs(el.querySelector("h2")).fontWeight,
          body: cs(el).fontSize,
        };
      });
      check(m.ul === "disc" && m.ol === "decimal" && m.nested === "circle", `${tag} R1 markers`, "bullets are discs (circles nested) and numbers decimal", JSON.stringify(m));
      const title = await page.locator("#article-title").evaluate((el) => ({ size: parseFloat(getComputedStyle(el).fontSize), weight: getComputedStyle(el).fontWeight, root: parseFloat(getComputedStyle(document.documentElement).fontSize) }));
      check(Math.abs(title.size - 1.75 * title.root) < 0.6 && title.weight === "650", `${tag} R2 title`, "the title is 1.75 rem at weight 650", `${title.size}px weight ${title.weight}`);
      check(Math.abs(parseFloat(m.h2) - 1.2 * parseFloat(m.body)) < 0.6 && m.h2w === "650", `${tag} R2 h2`, "an h2 is 1.2 em at weight 650", `${m.h2} weight ${m.h2w}`);

      // R3: pictures.
      const pics = await body.evaluate((el) => {
        const b = el.getBoundingClientRect();
        const box = (i) => {
          const r = i.getBoundingClientRect();
          return { w: Math.round(r.width), h: Math.round(r.height), display: getComputedStyle(i).display, mid: Math.round((r.left + r.right) / 2 - (b.left + b.right) / 2) };
        };
        const icons = [...el.querySelectorAll("img[width=\"24\"]")].map(box);
        const talls = [...el.querySelectorAll("img[width=\"400\"]")].map((i) => ({ ...box(i), nw: i.naturalWidth, nh: i.naturalHeight, fit: getComputedStyle(i).objectFit, where: i.parentElement.tagName.toLowerCase() }));
        return { icons, talls, vh: window.innerHeight, rem: parseFloat(getComputedStyle(document.documentElement).fontSize), small: [...el.querySelectorAll("img[width=\"12\"]")].map(box) };
      });
      const cap = Math.min(0.7 * pics.vh, 40 * pics.rem);
      check(pics.icons.length === 4 && pics.icons.every((i) => i.w === 24 && i.h === 24 && Math.abs(i.mid) > 10), `${tag} R3 icons`, "an icon in text and a row of icons keep the 24 px they were given and are not centred", JSON.stringify(pics.icons));
      check(pics.talls.length === 4, `${tag} R3 found`, "all four tall pictures are in the article", `${pics.talls.length} found`);
      check(pics.small.length === 1 && pics.small[0].w === 12 && pics.small[0].h === 12 && pics.small[0].display === "inline", `${tag} R3 width attribute`, "a picture keeps the width it was given (12 px on a 24 px file)", JSON.stringify(pics.small));
      for (const [n, p] of pics.talls.entries()) {
        check(p.h <= cap + 1 && p.w <= p.nw && p.fit === "contain", `${tag} R3 cap ${n + 1} ${p.where}`, `a tall picture in a ${p.where} is capped (${p.h} <= ${cap}) and drawn contained (its shape kept)`, JSON.stringify(p));
        check(Math.abs(p.w - cap * (400 / 1000)) <= 2, `${tag} R3 box ${n + 1} ${p.where}`, `a capped picture in a ${p.where} is as wide as its drawn shape (${Math.round(cap * 0.4)}), not the 400 it was given`, JSON.stringify(p));
        check(p.display === "block" && Math.abs(p.mid) <= 2, `${tag} R3 centred ${n + 1} ${p.where}`, `a tall picture in a ${p.where} is centred`, JSON.stringify(p));
      }

      // R4: code and tables.
      const regions = await body.evaluate((el) => {
        const info = (e) => ({ over: e.scrollWidth > e.clientWidth, tab: e.getAttribute("tabindex"), role: e.getAttribute("role"), name: e.getAttribute("aria-label") });
        const tables = [...el.querySelectorAll("table")];
        return { pre: info(el.querySelector("pre")), captioned: info(tables[0]), plain: info(tables[1]), inFigure: info(tables[2]), figureAlign: getComputedStyle(tables[2].querySelector("td")).textAlign };
      });
      if (tag === "phone") check(regions.pre.over && regions.plain.over, `${tag} R4 overflow`, "on a phone the code and the wide table overflow (so the checks below are real)", JSON.stringify(regions));
      check(regions.pre.over ? regions.pre.tab === "0" && regions.pre.role === "region" && regions.pre.name === "Code" : regions.pre.tab === null, `${tag} R4 code`, "the code block is a focusable region named Code while it overflows", JSON.stringify(regions.pre));
      check(regions.plain.over ? regions.plain.tab === "0" && regions.plain.name === "Table" : regions.plain.tab === null, `${tag} R4 table`, "a wide table is focusable and named Table", JSON.stringify(regions.plain));
      check(regions.captioned.over ? regions.captioned.tab === "0" && regions.captioned.name === null : true, `${tag} R4 caption`, "a captioned table keeps its caption as its name (no aria-label)", JSON.stringify(regions.captioned));
      check(regions.figureAlign !== "center", `${tag} R4 figure`, "a table in a figure is not centred text", regions.figureAlign);
      if (regions.pre.over) {
        await page.keyboard.press("Shift"); // the last input was the keyboard, so :focus-visible applies
        await body.locator("pre").focus();
        const ring = await body.locator("pre").evaluate((el) => getComputedStyle(el).outlineStyle);
        check(ring !== "none", `${tag} R4 ring`, "a focused code block shows a focus ring", `outline-style ${ring}`);
      }
      await page.evaluate(AXE); // evaluated, not injected as a script tag: the page's CSP forbids inline scripts
      const axeFindings = await page.evaluate(async () => {
        const r = await window.axe.run(document.querySelector("article[aria-labelledby=article-title]"), { runOnly: ["scrollable-region-focusable"] });
        return r.violations.map((v) => `${v.id}: ${v.nodes.length}`);
      });
      check(axeFindings.length === 0, `${tag} R4 axe`, "axe finds no scrollable region without focus", axeFindings.join("; "));

      // R7: table cells.
      const cells = await body.locator("table").first().evaluate((el) => ({ th: getComputedStyle(el.querySelector("th")).textAlign, num: [...el.querySelectorAll("td")].map((c) => [c.textContent.trim(), c.dataset.kp ?? "", getComputedStyle(c).textAlign]) }));
      const numCell = cells.num.find(([text]) => text === "6,650");
      check(cells.th === "start" || cells.th === "left", `${tag} R7 th`, "table headers start-align", cells.th);
      check(numCell !== undefined && numCell[1] === "num" && (numCell[2] === "right" || numCell[2] === "end"), `${tag} R7 numbers`, "a number cell is marked and sits on the end edge", JSON.stringify(cells.num));
      await t.shot(page, `${tag}-essay`);
    });

    await step(`${tag} pictures`, async () => {
      await open(page, feedId, PICTURES);
      await t.shot(page, `${tag}-tall-picture`);
    });

    await step(`${tag} arabic`, async () => {
      await open(page, feedId, ARABIC);
      const d = await page.evaluate(() => {
        const body = document.querySelector("[data-testid=article-body]");
        const title = document.getElementById("article-title");
        const dir = (e) => getComputedStyle(e).direction;
        return { title: dir(title), byline: dir(title.nextElementSibling), body: dir(body), p: dir(body.querySelector("p")), pAttr: body.querySelector("p").getAttribute("dir"), read: dir(document.querySelector("[data-testid=read-state]").parentElement) };
      });
      check(d.pAttr === null && d.title === "rtl" && d.byline === "rtl" && d.body === "rtl" && d.p === "rtl", `${tag} R6 rtl`, "an Arabic article's title, byline, body and paragraphs run right to left", JSON.stringify(d));
      check(d.read === "ltr", `${tag} R6 read line`, "the Read line stays left to right", d.read);
      await t.shot(page, `${tag}-arabic`);
    });

    if (tag === "desktop") {
      await step(`${tag} column`, async () => {
        // Wide enough that the pane is wider than the column, and in a face far wider than the app's own.
        await page.setViewportSize({ width: 2200, height: 900 });
        await open(page, feedId, ESSAY);
        await page.evaluate(() => document.documentElement.style.setProperty("--kp-reading-font", '"JetBrains Mono Variable", ui-monospace, monospace'));
        await page.waitForTimeout(400);
        await t.shot(page, `${tag}-wide-mono-column`);
        const col = await page.evaluate(() => {
          const b = document.querySelector("[data-testid=article-body]");
          const h = document.querySelector("article[aria-labelledby=article-title] > header");
          const pane = document.querySelector("article[aria-labelledby=article-title]").getBoundingClientRect().width;
          const probe = document.createElement("span");
          probe.textContent = "0".repeat(100);
          probe.style.cssText = "position:absolute;visibility:hidden;white-space:nowrap";
          b.appendChild(probe);
          const ch = probe.getBoundingClientRect().width / 100;
          probe.remove();
          const w = b.getBoundingClientRect().width;
          return { body: Math.round(w), header: Math.round(h.getBoundingClientRect().width), pane: Math.round(pane), chars: w / ch, bodyFont: getComputedStyle(b).fontFamily, headerFont: getComputedStyle(h).fontFamily };
        });
        check(col.pane > col.body + 100, `${tag} R5 room`, "the pane is wider than the column", JSON.stringify(col));
        check(col.bodyFont !== col.headerFont, `${tag} R5 fonts`, "the header and the body are set in different fonts", `${col.headerFont} / ${col.bodyFont}`);
        check(Math.abs(col.body - col.header) <= 1, `${tag} R5 aligned`, "the header and the body are one width", JSON.stringify(col));
        check(Math.abs(col.chars - 66) <= 3, `${tag} R5 chars`, "the column holds about 66 characters (63 to 69) of the reading font at the Standard density", JSON.stringify(col));
        await page.evaluate(() => document.documentElement.style.removeProperty("--kp-reading-font"));
        await page.setViewportSize({ width: vp.width, height: vp.height });
      });
    }
  });
} finally {
  server.close();
}

await t.finish("Reading typography");
