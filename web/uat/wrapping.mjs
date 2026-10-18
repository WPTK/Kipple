// UAT: long words and wide tables fit a phone: a table scrolls sideways instead of breaking words, and a title with
// a long unbroken word wraps instead of being cut off.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:wrapping               in another
//   npm run uat:wrapping -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: one article with a six-column table of long words, whose title
// carries a 130-character word with no spaces or hyphens. For each width (desktop 1280x800, phone 375x812, and 320x568
// on the phone pass) it checks:
//   W1  every table cell keeps each word on one line (no letter-by-letter breaks), and on a phone the table scrolls
//       sideways inside the article while the page itself does not;
//   W2  in the article list the title's long word wraps inside the row: nothing past its right edge, the row no wider
//       than the list;
//   W3  in the reader the heading's long word wraps inside the column.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 17);
const { check, step } = t;

const WORDS = ["Measurement", "Uncertainty", "Calibration", "Temperature", "Observations", "Description"];
// No hyphen or slash: browsers already break a URL there, so only overflow-wrap can break this one.
const LONG = `#UATwrapping${"Unbrokenword".repeat(10)}`;
const TITLE = `UAT wrapping ${LONG}`;
const table =
  `<table><thead><tr>${WORDS.map((w) => `<th>${w}</th>`).join("")}</tr></thead><tbody>` +
  Array.from({ length: 4 }, (_, r) => `<tr>${WORDS.map((w) => `<td>${w}${r}</td>`).join("")}</tr>`).join("") +
  `</tbody></table>`;
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
let base = "";
const server = createServer((req, res) => {
  if ((req.url ?? "").split("?")[0] !== "/feed.xml") return res.writeHead(404).end("not found");
  const body = `<p>A table of results follows.</p>${table}<p>The end.</p>`;
  const item = `<item><title>${esc(TITLE)}</title><link>${base}/page/1</link><guid isPermaLink="false">uat-wrapping-1</guid><pubDate>${new Date().toUTCString()}</pubDate><description>${esc(body)}</description></item>`;
  res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT wrapping</title><link>${base}/</link><description>t</description>${item}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

/** Each cell's words, measured: a word drawn on more than one line box was broken inside itself. */
const brokenCells = (body) =>
  body.locator("table").evaluate((tbl) => {
    const out = [];
    for (const cell of tbl.querySelectorAll("th, td")) {
      const node = cell.firstChild;
      if (!node || node.nodeType !== 3) continue;
      const r = document.createRange();
      r.selectNodeContents(node);
      const tops = new Set([...r.getClientRects()].map((x) => Math.round(x.top)));
      if (tops.size > 1) out.push(cell.textContent);
    }
    return out;
  });

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} W`, async () => {
    const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url: `${base}/feed.xml`, title: "UAT feed wrapping", allow_private_net: true } });
    if (!add.ok()) return t.fail(`${tag} W setup`, `the feed was not added: ${add.status()} ${await add.text()}`);
    const feedId = (await add.json()).id ?? (await t.boot(page)).feeds.find((f) => f.title === "UAT feed wrapping")?.id;
    await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
    let found = false;
    const end = Date.now() + 30000;
    while (!found && Date.now() < end) {
      const r = await page.request.get(`/api/items?view=all&feed=${feedId}`, { headers: { Accept: "application/json" } });
      found = r.ok() && (await r.json()).items?.some((i) => i.title === TITLE);
      if (!found) await page.waitForTimeout(500);
    }
    if (!found) return t.fail(`${tag} W setup`, "the feed's article never arrived (was it fetched?)");

    const widths = vp.mobile ? [[375, 812], [320, 568]] : [[vp.width, vp.height]];
    for (const [w, h] of widths) {
      await page.setViewportSize({ width: w, height: h });
      const at = `${tag} ${w}`;
      await page.goto(`/l/all`, { waitUntil: "load" });
      const link = page.locator("a.row-link").filter({ hasText: "UAT wrapping" }).first();
      await link.waitFor({ timeout: 15000 });
      await link.scrollIntoViewIfNeeded();
      const row = await link.evaluate((a) => {
        const heading = a.closest("h3, h4") ?? a;
        const list = a.closest("[data-index]")?.parentElement ?? document.querySelector("main") ?? document.body;
        const r = heading.getBoundingClientRect();
        return { right: Math.round(r.right), sw: heading.scrollWidth, cw: heading.clientWidth, list: Math.round(list.getBoundingClientRect().right), win: window.innerWidth };
      });
      check(
        row.sw <= row.cw && row.right <= Math.min(row.list, row.win),
        `${at} W2`,
        "the list title's long word wraps inside the row",
        `the title runs past its row: ${JSON.stringify(row)}`,
      );

      await link.click();
      const body = page.getByTestId("article-body");
      await body.locator("table").waitFor({ timeout: 15000 });
      const h1 = await page.locator("#article-title").evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth, right: Math.round(el.getBoundingClientRect().right), win: window.innerWidth }));
      check(h1.sw <= h1.cw && h1.right <= h1.win, `${at} W3`, "the reader heading's long word wraps inside the column", `the heading runs past the column: ${JSON.stringify(h1)}`);

      const broken = await brokenCells(body);
      check(broken.length === 0, `${at} W1 words`, "every table cell keeps its words whole", `cells broken inside a word: ${broken.join(", ")}`);
      const m = await body.locator("table").evaluate((tbl) => ({
        sw: tbl.scrollWidth,
        cw: tbl.clientWidth,
        doc: document.documentElement.scrollWidth,
        win: window.innerWidth,
        right: Math.round(tbl.getBoundingClientRect().right),
      }));
      if (vp.mobile) {
        check(m.sw > m.cw, `${at} W1 scroll`, "the table scrolls sideways inside the article", `the table does not scroll (${m.sw} px of table in ${m.cw})`);
      }
      check(m.doc <= m.win && m.right <= m.win, `${at} W1 page`, "the page itself does not scroll sideways", `the page is ${m.doc} px wide in a ${m.win} px window, table right edge ${m.right}`);
      await body.locator("table").scrollIntoViewIfNeeded();
      await t.shot(page, `${tag}-${w}-table`);
    }
  });
});

server.close();
await t.finish("wrapping");
