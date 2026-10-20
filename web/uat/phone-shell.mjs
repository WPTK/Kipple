// UAT: the phone shell holds together at iPhone widths.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:phone-shell            in another
//   npm run uat:phone-shell -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the phone width of the run (the desktop pass has nothing to check) it resizes the page and checks:
//   S1  Stats does not scroll the document: its scroll height equals the window height at 393, 402 and 440 wide;
//   F1  the Feeds title is not truncated at 402 and 440 wide;
//   H1  the list header is as tall for Unread as for a feed with a very long title, at 393, 402 and 440 wide;
//   R1  in landscape the article's title and body start at the same left edge;
//   T1  the tab bar marks Feeds on a feed's list and on Feed health, and Unread on the Unread list;
//   M1  at 874x402 the opened row More menu (a bottom sheet on touch, #423) lies inside the screen (#431).
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 15);
const { check, step } = t;

const LONG = "UAT feed with a very long title that cannot possibly fit on one phone line";
const WIDTHS = [393, 402, 440];

await t.eachViewport(async (page, vp) => {
  if (!vp.mobile) return;
  const tag = vp.id;
  const size = (width, height = 850) => page.setViewportSize({ width, height });
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1").first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(600);
  };
  const current = () => page.locator('nav[aria-label="Primary"] a[aria-current="page"]').first().innerText().then((s) => s.replace(/[\d+]/g, "").trim());

  await step(`${tag} S1`, async () => {
    for (const w of WIDTHS) {
      await size(w);
      await open("/stats");
      await page.waitForTimeout(1000);
      const m = await page.evaluate(() => ({ doc: document.documentElement.scrollHeight, win: window.innerHeight }));
      check(m.doc === m.win, `${tag} S1 ${w}`, `Stats leaves the document at ${m.win} px`, `the document is ${m.doc} px tall in a ${m.win} px window`);
      if (w === 402) await t.shot(page, `${tag}-stats`);
    }
  });

  await step(`${tag} F1`, async () => {
    for (const w of [402, 440]) {
      await size(w);
      await open("/feeds");
      const m = await page.locator("main h1").first().evaluate((h) => ({ sw: h.scrollWidth, cw: h.clientWidth, text: h.textContent }));
      check(m.sw <= m.cw, `${tag} F1 ${w}`, `the "${m.text}" title is whole`, `the "${m.text}" title is cut off (${m.sw} px of text in ${m.cw})`);
    }
  });

  await step(`${tag} H1`, async () => {
    await t.importOpml(page, `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body><outline type="rss" text="${LONG}" title="${LONG}" xmlUrl="http://uat-feed-long.invalid/feed.xml"/></body></opml>`);
    const { b, ok } = await t.settle(page, (x) => x.feeds.some((f) => f.title === LONG));
    const feed = b.feeds.find((f) => f.title === LONG);
    if (!ok || !feed) return t.fail(`${tag} H1 setup`, "the long-titled feed was not added");
    for (const w of WIDTHS) {
      await size(w);
      const heights = [];
      for (const path of ["/l/unread", `/l/unread?feed=${feed.id}`]) {
        await open(path);
        heights.push(await page.locator("main h1").first().evaluate((h) => Math.round(h.closest("header").getBoundingClientRect().height)));
      }
      check(heights[0] === heights[1], `${tag} H1 ${w}`, `the header is ${heights[0]} px for Unread and for the long title`, `the header is ${heights[0]} px for Unread but ${heights[1]} px for the long title`);
      if (w === 402) await t.shot(page, `${tag}-long-title`);
    }
  });

  await step(`${tag} T1`, async () => {
    await size(402);
    const checks = [["/l/unread", "Unread"], ["/health", "Feeds"]];
    const feed = (await t.boot(page)).feeds.find((f) => f.title === LONG);
    if (feed) checks.push([`/l/unread?feed=${feed.id}`, "Feeds"]);
    for (const [path, want] of checks) {
      await open(path);
      const got = await current();
      check(got.includes(want), `${tag} T1 ${path}`, `${want} is the current tab`, `${got || "no tab"} is the current tab`);
    }
  });

  await step(`${tag} R1`, async () => {
    await size(852, 393);
    await open("/l/all");
    const link = page.locator("main a[href^='/i/']").first();
    await link.waitFor({ timeout: 15000 });
    await link.click();
    await page.locator("#article-title").waitFor({ timeout: 15000 });
    await page.waitForTimeout(800);
    const m = await page.evaluate(() => ({ title: document.querySelector("#article-title").getBoundingClientRect().left, body: document.querySelector(".article-body")?.getBoundingClientRect().left ?? null }));
    check(m.body !== null && Math.abs(m.title - m.body) <= 1, `${tag} R1`, `title and body start at ${Math.round(m.title)} px`, `the title starts at ${Math.round(m.title)} px and the body at ${m.body === null ? "(no body)" : Math.round(m.body)} px`);
    await t.shot(page, `${tag}-landscape-reader`);
  });

  await step(`${tag} M1`, async () => {
    await size(874, 402);
    await open("/l/all");
    const row = page.locator("main article[data-item-id]").first();
    await row.waitFor({ timeout: 15000 });
    await row.getByRole("button", { name: "More actions" }).click();
    const menu = page.getByRole("menu");
    await menu.waitFor({ timeout: 5000 });
    await page.waitForTimeout(500);
    const m = await page.evaluate(() => {
      const r = document.querySelector('[role="menu"]').getBoundingClientRect();
      return { top: r.top, bottom: r.bottom, limit: window.innerHeight };
    });
    check(m.top >= 0 && m.bottom <= m.limit + 0.5, `${tag} M1`, `the row menu spans ${Math.round(m.top)}-${Math.round(m.bottom)} px, inside 0-${Math.round(m.limit)}`, `the row menu spans ${Math.round(m.top)}-${Math.round(m.bottom)} px, outside 0-${Math.round(m.limit)} (clipped)`);
    await t.shot(page, `${tag}-landscape-row-menu`);
    await page.keyboard.press("Escape");
  });
});

await t.finish("phone-shell");
