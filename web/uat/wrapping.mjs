// UAT: long words and wide tables fit a phone: a table scrolls sideways instead of breaking words, and a title with
// a long unbroken word wraps instead of running past the edge.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:wrapping               in another
//   npm run uat:wrapping -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: one article with a six-column table of long words and a
// one-column layout table (a newsletter's wrapper) holding a long address, whose title carries a 130-character word
// with no spaces or hyphens. For each width (desktop 1280x800, phone 375x812, and 320x568 on the phone pass) it checks:
//   W1  every data-table cell keeps each word on one line (no letter-by-letter breaks), and on a phone the data table
//       scrolls sideways inside the article; the layout table does not scroll and the page itself never does;
//   W2  in the article list, in every layout, the title stays inside its row: it wraps where the layout shows more
//       than one line, and the clamped layouts (Headlines one line, Inbox two) keep it within those lines;
//   W3  in the reader the heading's long word wraps inside the column.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 18);
const { check, step } = t;

const WORDS = ["Measurement", "Uncertainty", "Calibration", "Temperature", "Observations", "Description"];
// No hyphen or slash: browsers already break a URL there, so only overflow-wrap can break this one.
const LONG = `#UATwrapping${"Unbrokenword".repeat(10)}`;
const TITLE = `UAT wrapping ${LONG}`;
const LAYOUTS = ["magazine", "cards", "compact", "gazette", "headlines", "inbox"];
// Layouts that cut a title to a number of lines (an ellipsis ends it), and how many.
const CLAMPED = { headlines: 1, inbox: 2 };
const table =
  `<table><thead><tr>${WORDS.map((w) => `<th>${w}</th>`).join("")}</tr></thead><tbody>` +
  Array.from({ length: 4 }, (_, r) => `<tr>${WORDS.map((w) => `<td>${w}${r}</td>`).join("")}</tr>`).join("") +
  `</tbody></table>`;
const layoutTable = `<table><tr><td>Read it online at ${"Trackinglinkpart".repeat(8)}</td></tr></table>`;
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
let base = "";
const server = createServer((req, res) => {
  if ((req.url ?? "").split("?")[0] !== "/feed.xml") return res.writeHead(404).end("not found");
  const body = `<p>A table of results follows.</p>${table}${layoutTable}<p>The end.</p>`;
  const item = `<item><title>${esc(TITLE)}</title><link>${base}/page/1</link><guid isPermaLink="false">uat-wrapping-1</guid><pubDate>${new Date().toUTCString()}</pubDate><description>${esc(body)}</description></item>`;
  res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT wrapping</title><link>${base}/</link><description>t</description>${item}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

/** Each data-table cell's words, measured: a word drawn on more than one line box was broken inside itself. */
const brokenCells = (body) =>
  body.locator("table").first().evaluate((tbl) => {
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

const setLayout = (page, layout) =>
  page.evaluate(
    (l) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify({ "client.layout": l }) }).then((r) => r.status),
    layout,
  );

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  let ready = false;
  await step(`${tag} W setup`, async () => {
    const added = await t.addLocalFeed(page, `${base}/feed.xml`, "UAT feed wrapping", (items) => items.some((i) => i.title === TITLE));
    if (added.error) return t.fail(`${tag} W setup`, added.error);
    ready = true;
  });
  if (!ready) return;

  const widths = vp.mobile ? [[375, 812], [320, 568]] : [[vp.width, vp.height]];
  for (const [w, h] of widths) {
    const at = `${tag} ${w}`;
    await step(`${at} W2`, async () => {
      await page.setViewportSize({ width: w, height: h });
      for (const layout of LAYOUTS) {
        const status = await setLayout(page, layout);
        if (status !== 200) return t.fail(`${at} W2 ${layout}`, `the layout could not be set (${status})`);
        await page.goto("/l/all", { waitUntil: "load" });
        const link = page.locator("a.row-link").filter({ hasText: "UAT wrapping" }).first();
        await link.waitFor({ timeout: 15000 });
        await link.scrollIntoViewIfNeeded();
        const row = await link.evaluate((a) => {
          const heading = a.closest("h1, h2, h3, h4, h5, h6");
          if (!heading) return null;
          const list = document.querySelector('[data-testid="list-scroll"]') ?? document.body;
          const r = heading.getBoundingClientRect();
          return {
            right: Math.round(r.right),
            sw: heading.scrollWidth,
            cw: heading.clientWidth,
            lines: Math.round(r.height / parseFloat(getComputedStyle(heading).lineHeight)),
            list: Math.round(list.getBoundingClientRect().right),
            win: window.innerWidth,
          };
        });
        if (!row) {
          t.fail(`${at} W2 ${layout}`, "the title link has no heading around it");
          continue;
        }
        const inside = row.right <= Math.min(row.list, row.win);
        if (layout in CLAMPED) {
          check(inside && row.sw <= row.cw && row.lines <= CLAMPED[layout], `${at} W2 ${layout}`, `the title stays inside its row, in at most ${CLAMPED[layout]} line(s)`, `the title runs past its row or its lines: ${JSON.stringify(row)}`);
        } else {
          check(inside && row.sw <= row.cw && row.lines > 1, `${at} W2 ${layout}`, "the title's long word wraps inside the row", `the title runs past its row: ${JSON.stringify(row)}`);
        }
      }
      await setLayout(page, "magazine");
    });

    await step(`${at} W1 W3`, async () => {
      await page.setViewportSize({ width: w, height: h });
      await page.goto("/l/all", { waitUntil: "load" });
      const link = page.locator("a.row-link").filter({ hasText: "UAT wrapping" }).first();
      await link.waitFor({ timeout: 15000 });
      await link.click();
      const body = page.getByTestId("article-body");
      await body.locator("table").nth(1).waitFor({ timeout: 15000 });
      const h1 = await page.locator("#article-title").evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth, right: Math.round(el.getBoundingClientRect().right), win: window.innerWidth }));
      check(h1.sw <= h1.cw && h1.right <= h1.win, `${at} W3`, "the reader heading's long word wraps inside the column", `the heading runs past the column: ${JSON.stringify(h1)}`);

      const broken = await brokenCells(body);
      check(broken.length === 0, `${at} W1 words`, "every data-table cell keeps its words whole", `cells broken inside a word: ${broken.join(", ")}`);
      const size = (n) => body.locator("table").nth(n).evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth, right: Math.round(el.getBoundingClientRect().right) }));
      const data = await size(0);
      const lay = await size(1);
      const page_ = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, win: window.innerWidth }));
      if (vp.mobile) {
        check(data.sw > data.cw, `${at} W1 scroll`, "the data table scrolls sideways inside the article", `the data table does not scroll (${data.sw} px of table in ${data.cw})`);
      }
      check(lay.sw <= lay.cw, `${at} W1 layout`, "the one-column layout table wraps its long address and does not scroll", `the layout table scrolls (${lay.sw} px in ${lay.cw})`);
      check(
        page_.doc <= page_.win && data.right <= page_.win && lay.right <= page_.win,
        `${at} W1 page`,
        "the page itself does not scroll sideways",
        `the page is ${page_.doc} px wide in a ${page_.win} px window, table right edges ${data.right} and ${lay.right}`,
      );
      await body.locator("table").first().scrollIntoViewIfNeeded();
      await t.shot(page, `${tag}-${w}-table`);
    });
  }
});

server.close();
await t.finish("wrapping");
