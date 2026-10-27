// UAT: rows and the list header stay one readable column on a tablet, and nothing in the header flips at 900 px.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/tablet-widths.mjs         in another
//   node uat/tablet-widths.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the desktop run it resizes the window and, in Compact and in Headlines, checks:
//   W1  at 820 px (a tablet held upright) the list rows and the list header are one column no wider than 42 rem, centered,
//       with the header and the rows sharing a left edge and the Oldest first button in the header;
//   W2  at 1000 and at 1400 px with no article open the same holds (where the list is wider than the column);
//   W3  at 899 and at 900 px the header has the same controls: nothing in it appears or goes away at the change to
//       three panes;
//   W4  at 900 px nothing scrolls sideways and the list pane keeps its floor width;
//   W6  in Cards and the Gazette (which fill the pane) the header is held to the same 42 rem column at 1000 and 1400 px;
//   W7  at 899 and at 900 px in a window 480 px tall the header has the same controls (the order does not depend on height);
//   W5  at 1400 px the Digest page and the Feed index are at most 42 rem wide.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

const patch = (page, b) =>
  page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);

await t.eachViewport(async (page, vp) => {
  if (vp.mobile) return;
  page.setDefaultTimeout(8000);
  const at = async (w, layout) => {
    await patch(page, { "client.layout": layout });
    await page.setViewportSize({ width: w, height: 900 });
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.getByTestId("list-scroll").locator("[data-item-id]").first().waitFor({ timeout: 15000 });
  };
  const measure = () =>
    page.evaluate(() => {
      const box = (el) => {
        const r = el.getBoundingClientRect();
        return { left: Math.round(r.left), right: Math.round(r.right), width: Math.round(r.width) };
      };
      const scroll = document.querySelector('[data-testid="list-scroll"]');
      const row = scroll?.querySelector("[data-item-id]")?.closest(".kp-row");
      const head = document.querySelector("main header [data-title-row]");
      const buttons = [...document.querySelectorAll("main header [data-title-row] button")].filter((b) => b.getClientRects().length).map((b) => b.getAttribute("aria-label") || b.textContent);
      return {
        rem: parseFloat(getComputedStyle(document.documentElement).fontSize),
        row: row ? box(row) : null,
        head: head ? box(head) : null,
        list: scroll ? box(scroll) : null,
        buttons,
        sideways: document.documentElement.scrollWidth > innerWidth,
      };
    });

  for (const layout of ["compact", "headlines"]) {
    for (const w of [820, 1000, 1400]) {
      await step(`${layout} ${w}`, async () => {
        await at(w, layout);
        const m = await measure();
        const cap = 42 * m.rem;
        const col = `${layout} at ${w} px`;
        const id = w === 820 ? "W1" : "W2";
        check(!!m.row && m.row.width <= cap + 1, `${col} ${id} row`, `rows are at most 42 rem (${cap} px) wide`, `a row is ${m.row?.width} px wide, more than ${cap}`);
        check(!!m.head && m.head.width <= cap + 1, `${col} ${id} header`, "the header is at most 42 rem wide", `the header is ${m.head?.width} px wide`);
        // Where the list is narrower than the column the rows fill it and the header keeps its own 1 rem of padding.
        const capped = !!m.list && m.list.width > cap + 32;
        check(!capped || (!!m.row && !!m.head && Math.abs(m.row.left - m.head.left) <= 1), `${col} ${id} aligned`, "the header and the rows share one left edge", `row at ${m.row?.left}, header at ${m.head?.left}`);
        check(!!m.row && !!m.list && Math.abs(m.row.left - m.list.left - (m.list.right - m.row.right)) <= 2, `${col} ${id} centered`, "the column is centered in the list", `row ${m.row?.left}..${m.row?.right} in list ${m.list?.left}..${m.list?.right}`);
        check(m.buttons.includes("Oldest first"), `${col} ${id} order`, "the header has Oldest first", `the header buttons are ${m.buttons.join(", ")}`);
      });
    }
    await step(`${layout} 899 vs 900`, async () => {
      await at(899, layout);
      const below = await measure();
      await at(900, layout);
      const above = await measure();
      check(below.buttons.join("|") === above.buttons.join("|"), `${layout} W3 flip`, `the header has the same controls at 899 and 900 px: ${above.buttons.join(", ")}`, `at 899 px: ${below.buttons.join(", ")}; at 900 px: ${above.buttons.join(", ")}`);
      check(!above.sideways, `${layout} W4 sideways`, "at 900 px nothing scrolls sideways", "the page scrolls sideways at 900 px");
      check((above.list?.width ?? 0) >= 260, `${layout} W4 list`, `the list pane is ${above.list?.width} px wide`, `the list pane is only ${above.list?.width} px wide`);
    });
  }
  // W6: a layout that fills the pane still has a header held to the column.
  for (const layout of ["cards", "gazette"]) {
    for (const w of [1000, 1400]) {
      await step(`${layout} header ${w}`, async () => {
        await patch(page, { "client.layout": layout });
        await page.setViewportSize({ width: w, height: 900 });
        await page.goto("/l/unread", { waitUntil: "load" });
        await page.locator("main header [data-title-row]").waitFor({ timeout: 15000 });
        const m = await measure();
        check(!!m.head && m.head.width <= 42 * m.rem + 1, `${layout} at ${w} px W6 header`, "the header is at most 42 rem wide", `the header is ${m.head?.width} px wide`);
      });
    }
  }
  // W7: height plays no part in what the header shows.
  await step("899 vs 900 short", async () => {
    await patch(page, { "client.layout": "compact" });
    const seen = [];
    for (const w of [899, 900]) {
      await page.setViewportSize({ width: w, height: 480 });
      await page.goto("/l/unread", { waitUntil: "load" });
      await page.getByTestId("list-scroll").locator("[data-item-id]").first().waitFor({ timeout: 15000 });
      seen.push((await measure()).buttons.join("|"));
    }
    check(seen[0] === seen[1] && seen[1].includes("Oldest first"), "W7 short flip", "a 480 px tall window has the same header controls at 899 and 900 px", `at 899 px: ${seen[0]}; at 900 px: ${seen[1]}`);
  });
  // W5: the pages that fill the whole pane (the Digest, the Feed index) hold their sections to the column too.
  await step("pages at 1400", async () => {
    await patch(page, { "client.layout": "digest" });
    await page.setViewportSize({ width: 1400, height: 900 });
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.locator("[data-page-box] [data-digest]").waitFor({ timeout: 15000 });
    const digest = await page.locator("[data-page-box]").evaluate((el) => [el.getBoundingClientRect().width, parseFloat(getComputedStyle(document.documentElement).fontSize)]);
    check(digest[0] <= 42 * digest[1] + 1, "digest W5", "the Digest page is at most 42 rem wide", `the Digest page is ${digest[0]} px wide`);
    await page.goto("/feeds/index", { waitUntil: "load" });
    await page.locator("[data-feed-index]").waitFor({ timeout: 15000 });
    const index = await page.locator("[data-feed-index]").evaluate((el) => [el.getBoundingClientRect().width, parseFloat(getComputedStyle(document.documentElement).fontSize)]);
    check(index[0] <= 42 * index[1] + 1, "feed index W5", "the Feed index is at most 42 rem wide", `the Feed index is ${index[0]} px wide`);
  });
  await patch(page, { "client.layout": null });
});

await t.finish("tablet-widths");
