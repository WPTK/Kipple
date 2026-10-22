// UAT: reading typography of an article (design review, Read package).
//
//   KIPPLE_SEED_SET=design npm run seed     in one terminal
//   npm run uat:reading                     in another, once the feeds have fetched
//   npm run uat:reading -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// On a phone (375x812) and a desktop (1280x800) it opens the design library's long essay (every block an article can
// have), a tall picture and the Arabic article, and checks what the browser computes:
//   R1  bullet lists show discs and numbered lists decimals (the base style reset clears them);
//   R2  the title is 28 px, the body headings step down from it (h2 1.2 em) and all are weight 650;
//   R3  a tall picture is no taller than min(70% of the window, 40 rem), keeps its shape, and is centred;
//   R4  code and table wider than the column are focusable, named "Code" and "Table", and axe's
//       scrollable-region-focusable finds nothing in the article;
//   R5  the header and the body share one width, between 45 and 90 characters of the reading font at its size;
//   R6  an Arabic title and body lay out right to left;
//   R7  table headers start-align and a number cell sits on the end edge.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { start } from "./common.mjs";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const t = await start(import.meta.url, 16);
const { check, step } = t;

async function openIn(page, feedTitle, pick) {
  const boot = await (await page.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
  const list = boot.feeds ?? [];
  const feed = list.find((f) => f.title === feedTitle);
  if (!feed) throw new Error(`no feed called ${feedTitle} (seed it with KIPPLE_SEED_SET=design)`);
  await page.goto(`/l/all?feed=${feed.id}`, { waitUntil: "load" });
  const rows = page.locator("[data-testid=list-scroll] [data-item-id]");
  await rows.first().waitFor({ timeout: 15000 });
  const row = pick ? rows.filter({ hasText: pick }).first() : rows.first();
  await row.locator("a").first().click();
  await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
  await page.getByTestId("article-body").waitFor({ timeout: 10000 });
}

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} essay`, async () => {
    await openIn(page, "Long Essays");
    const body = page.getByTestId("article-body");
    await body.locator("ul > li").first().waitFor();
    const markers = await body.evaluate((el) => ({
      ul: getComputedStyle(el.querySelector("ul > li")).listStyleType,
      ol: getComputedStyle(el.querySelector("ol > li")).listStyleType,
      h2: getComputedStyle(el.querySelector("h2")).fontSize,
      h2w: getComputedStyle(el.querySelector("h2")).fontWeight,
      body: getComputedStyle(el).fontSize,
    }));
    check(markers.ul === "disc" && markers.ol === "decimal", `${tag} R1 markers`, "bullets are discs and numbers decimal", `ul ${markers.ul}, ol ${markers.ol}`);
    const title = await page.locator("#article-title").evaluate((el) => ({ size: parseFloat(getComputedStyle(el).fontSize), weight: getComputedStyle(el).fontWeight, root: parseFloat(getComputedStyle(document.documentElement).fontSize) }));
    check(Math.abs(title.size - 1.75 * title.root) < 0.6 && title.weight === "650", `${tag} R2 title`, "the title is 1.75 rem at weight 650", `${title.size}px weight ${title.weight}`);
    const bodyPx = parseFloat(markers.body);
    check(Math.abs(parseFloat(markers.h2) - 1.2 * bodyPx) < 0.6 && markers.h2w === "650", `${tag} R2 h2`, "an h2 is 1.2 em at weight 650", `${markers.h2} weight ${markers.h2w}`);

    // R4: the code block and the table scroll sideways on a phone; once the observers have run they can take focus.
    const pre = body.locator("pre").first();
    await pre.scrollIntoViewIfNeeded();
    await page.waitForTimeout(400);
    const preState = await pre.evaluate((el) => ({ over: el.scrollWidth > el.clientWidth, tab: el.getAttribute("tabindex"), role: el.getAttribute("role"), name: el.getAttribute("aria-label") }));
    if (preState.over) check(preState.tab === "0" && preState.role === "region" && preState.name === "Code", `${tag} R4 code`, "the overflowing code block is a focusable region named Code", JSON.stringify(preState));
    else check(preState.tab === null && preState.name === null, `${tag} R4 code`, "a code block that fits is not a tab stop", JSON.stringify(preState));
    const table = body.locator("table").first();
    await table.scrollIntoViewIfNeeded();
    await page.waitForTimeout(400);
    const tableState = await table.evaluate((el) => ({ over: el.scrollWidth > el.clientWidth, tab: el.getAttribute("tabindex"), name: el.getAttribute("aria-label") }));
    if (tableState.over) check(tableState.tab === "0" && tableState.name === "Table", `${tag} R4 table`, "the overflowing table is focusable and named Table", JSON.stringify(tableState));
    else check(tableState.tab === null && tableState.name === null, `${tag} R4 table`, "a table that fits is not a tab stop", JSON.stringify(tableState));
    if (preState.over) {
      await page.keyboard.press("Shift"); // the last input was the keyboard, so :focus-visible applies
      await pre.focus();
      const ring = await pre.evaluate((el) => getComputedStyle(el).outlineStyle);
      check(ring !== "none", `${tag} R4 ring`, "a focused code block shows a focus ring", `outline-style ${ring}`);
    }
    await page.evaluate(AXE); // evaluated, not injected as a script tag: the page's CSP forbids inline scripts
    const axeFindings = await page.evaluate(async () => {
      const r = await window.axe.run(document.querySelector("article"), { runOnly: ["scrollable-region-focusable"] });
      return r.violations.map((v) => `${v.id}: ${v.nodes.length}`);
    });
    check(axeFindings.length === 0, `${tag} R4 axe`, "axe finds no scrollable region without focus", axeFindings.join("; "));

    // R5: the column is a count of characters of the reading font, and the header lines up with the body.
    const col = await page.evaluate(() => {
      const b = document.querySelector("[data-testid=article-body]");
      const h = document.querySelector("article > header");
      const probe = document.createElement("span");
      probe.textContent = "0".repeat(100);
      probe.style.cssText = "position:absolute;visibility:hidden;white-space:nowrap";
      b.appendChild(probe);
      const ch = probe.getBoundingClientRect().width / 100;
      probe.remove();
      return { body: b.getBoundingClientRect().width, header: h.getBoundingClientRect().width, chars: b.getBoundingClientRect().width / ch };
    });
    const narrow = col.body < 480; // on a phone the window, not the setting, sets the width
    check(Math.abs(col.body - col.header) < 1, `${tag} R5 aligned`, "the header and the body are one width", `body ${col.body}, header ${col.header}`);
    check(narrow || (col.chars >= 45 && col.chars <= 90), `${tag} R5 chars`, "the column holds 45 to 90 characters", `${col.chars.toFixed(1)} characters in ${col.body}px`);

    // R7: table cells.
    const cells = await table.evaluate((el) => ({ th: getComputedStyle(el.querySelector("th")).textAlign, num: [...el.querySelectorAll("td")].map((c) => [c.textContent.trim(), c.dataset.kp ?? "", getComputedStyle(c).textAlign]) }));
    const numCell = cells.num.find(([, kp]) => kp === "num");
    check(cells.th === "start" || cells.th === "left", `${tag} R7 th`, "table headers start-align", cells.th);
    check(!numCell || numCell[2] === "right" || numCell[2] === "end", `${tag} R7 numbers`, "a number cell sits on the end edge", JSON.stringify(cells.num));
    await t.shot(page, `${tag}-essay`);
  });

  await step(`${tag} tall picture`, async () => {
    await openIn(page, "Portrait Gallery");
    const img = page.getByTestId("article-body").locator("img").first();
    await img.waitFor();
    await page.waitForFunction(() => document.querySelector("[data-testid=article-body] img")?.complete);
    const m = await img.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const p = el.parentElement.getBoundingClientRect();
      return { w: r.width, h: r.height, vh: window.innerHeight, rem: parseFloat(getComputedStyle(document.documentElement).fontSize), nw: el.naturalWidth, nh: el.naturalHeight, centred: Math.abs(r.left - p.left - (p.right - r.right)) < 2 };
    });
    const cap = Math.min(0.7 * m.vh, 40 * m.rem);
    check(m.h <= cap + 1, `${tag} R3 cap`, "a tall picture is capped at min(70% of the window, 40 rem)", `height ${m.h}, cap ${cap}`);
    check(Math.abs(m.w / m.h - m.nw / m.nh) < 0.02, `${tag} R3 shape`, "the picture keeps its shape", `${m.w}x${m.h} from ${m.nw}x${m.nh}`);
    check(m.centred, `${tag} R3 centred`, "the capped picture is centred", JSON.stringify(m));
    await t.shot(page, `${tag}-tall-picture`);
  });

  await step(`${tag} arabic`, async () => {
    await openIn(page, "Ünïcödé Nöte – 日本語のニュース", "عنوان");
    const dirs = await page.evaluate(() => ({
      title: getComputedStyle(document.getElementById("article-title")).direction,
      body: getComputedStyle(document.querySelector("[data-testid=article-body] p")).direction,
    }));
    check(dirs.title === "rtl" && dirs.body === "rtl", `${tag} R6 rtl`, "an Arabic article lays out right to left", JSON.stringify(dirs));
    await t.shot(page, `${tag}-arabic`);
  });
});

await t.finish("Reading typography");
