// UAT: the Coffee break layout (#39).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:coffee                 in another
//   npm run uat:coffee -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The flow serves one feed on a loopback port (four short articles, two of about four minutes, one of about nine and one
// with no text at all), puts it in a folder of its own ("UAT Coffee") and opens that folder's Unread with the Coffee
// break as the layout. For each width (desktop 1280x800, phone 375x812) it checks:
//   K1  the four sections are drawn in order, each with its articles: Under 3 minutes, 3 to 7 minutes, 8 minutes and up,
//       Unknown length; and the first shows three rows and "1 more" (the Rows per section setting, an exact count);
//   K2  "1 more" opens the section in place and its button goes;
//   K3  Mark below on an article marks the rest of its section and no other; Mark above likewise, in a section other than the
//       first (the newer article of the first section stays unread); and a mark-read request scoped to the folder with
//       unknown_length marks the article with no text and nothing else;
//   K4  Mark all (confirmed in its dialog) marks every section, and one Undo puts them all back;
//   K5  the arrow keys visit the articles and the "more" button in the order they are drawn, and the page has one Tab stop;
//   K6  the Layout menu offers Images and Rows per section for the device, and a choice changes the page;
//   K7  All, and Unread with a reading-time filter, show a plain list, and the menu says why;
//   K8  `?len=` takes only the filter's own lengths: a section's length in the address changes nothing;
//   and no request goes to an export or download address.
// Chromium only. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { URL } from "node:url";
import { expect } from "@playwright/test";
import { start, waitForStillFor } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

const FOLDER = "UAT Coffee";
const FEED = "UAT feed coffee";
const SECTIONS = ["Under 3 minutes", "3 to 7 minutes", "8 minutes and up", "Unknown length"];
// Words: reading time is whole minutes at 230 words a minute, so 80 words is one minute, 800 four and 2000 nine.
const STORIES = [
  { n: 1, words: 80 },
  { n: 2, words: 80 },
  { n: 3, words: 80 },
  { n: 4, words: 80 },
  { n: 5, words: 800 },
  { n: 6, words: 800 },
  { n: 7, words: 2000 },
  { n: 8, words: 0 },
];

let base = "";
const server = createServer((req, res) => {
  if (new URL(req.url ?? "/", "http://x").pathname !== "/coffee.xml") return void res.writeHead(404).end();
  const item = ({ n, words }) => {
    const body = words ? `<description>&lt;p&gt;${"word ".repeat(words).trim()}&lt;/p&gt;</description>` : "";
    return `<item><title>UAT coffee story ${n}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-coffee-${n}</guid><pubDate>${new Date(Date.now() - n * 3600_000).toUTCString()}</pubDate>${body}</item>`;
  };
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>${FEED}</title><link>${base}/</link><description>t</description>${STORIES.map(item).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    const requests = [];
    page.on("request", (r) => requests.push(new URL(r.url()).pathname));
    const patch = (b) =>
      page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
    const section = (name) => page.locator("section[data-section]").filter({ has: page.getByRole("heading", { level: 2, name, exact: true }) });
    const idsOf = (name) => section(name).locator("article[data-item-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId));
    const headings = () => page.locator("section[data-section] > h2").allTextContents();
    const settled = (fn, ms = 8000) => page.waitForFunction(fn, null, { timeout: ms }).then(() => true, () => false);
    const readState = (ids) => page.evaluate(async (list) => Promise.all(list.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true)), ids);
    const unreadAll = (ids) => page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids, read: false } }).catch(() => {});
    const openMenu = async (id) => {
      const row = page.locator(`article[data-item-id="${id}"]`);
      await row.scrollIntoViewIfNeeded();
      if (vp.mobile) {
        // A long press, as a real touch sequence: the More button is not tapped on touch.
        await waitForStillFor(page, 150, () => row.getByRole("link").boundingBox(), { label: "the row to stop moving after scrolling to it" });
        const box = await row.getByRole("link").boundingBox();
        const cdp = await page.context().newCDPSession(page);
        const touch = (type, points) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points });
        await touch("touchStart", [{ x: Math.round(box.x + 10), y: Math.round(box.y + box.height / 2) }]);
        await page.getByRole("menu").waitFor({ timeout: 5000 }); // the menu opens while the finger is still down, when the long-press timer fires
        await touch("touchEnd", []);
      } else {
        await row.hover();
        await row.getByRole("button", { name: /^More actions/ }).click();
      }
      await page.getByRole("menu").waitFor({ timeout: 5000 });
    };
    const layoutMenu = async () => {
      await page.getByRole("button", { name: /^List options/ }).first().click();
      await page.getByRole("menu").waitFor({ timeout: 5000 });
    };

    await step(`${tag} K`, async () => {
      const added = await t.addLocalFeed(page, `${base}/coffee.xml`, FEED, (items) => items.length >= STORIES.length);
      if (added.error) return t.fail(`${tag} K setup`, added.error);
      const made = await page.request.post("/api/folders", { data: { name: FOLDER }, headers: t.WRITE });
      if (!made.ok()) return t.fail(`${tag} K setup`, `the folder was not made (${made.status()})`);
      const folder = (await t.boot(page)).folders.find((x) => x.name === FOLDER);
      const moved = await page.request.patch(`/api/feeds/${added.feedId}`, { data: { folder_id: folder.id }, headers: t.WRITE });
      if (!moved.ok()) return t.fail(`${tag} K setup`, `the feed was not moved into the folder (${moved.status()})`);
      // The article ids by the story's number, and the one with no text must really have none (else Unknown length proves nothing).
      const all = (await (await page.request.get(`/api/items?view=all&feed=${added.feedId}`, { headers: { Accept: "application/json" } })).json()).items;
      const idOf = (n) => all.find((a) => a.title === `UAT coffee story ${n}`)?.id;
      const words = (n) => all.find((a) => a.title === `UAT coffee story ${n}`)?.word_count;
      if (STORIES.some(({ n }) => !idOf(n)) || words(8) !== 0 || !(words(4) > 0 && words(4) <= 460) || !(words(5) > 460 && words(5) <= 1610) || !(words(7) > 1610)) {
        return t.fail(`${tag} K setup`, `the stories did not arrive with the word counts the flow needs: ${STORIES.map(({ n }) => `${n}:${words(n)}`).join(" ")}`);
      }
      const set = await patch({ "client.layout": "coffee", "client.digest_images": true, "client.digest_rows": 3 });
      if (set !== 200) return t.fail(`${tag} K setup`, `the Coffee break was not set as the layout (${set})`);
      const url = `/l/unread?folder=${folder.id}`;
      const open = async () => {
        await page.goto(url, { waitUntil: "load" });
        await page.locator("section[data-section] article[data-item-id]").first().waitFor({ timeout: 20000 });
        await settled(() => !document.querySelector('[data-testid="digest-skeleton"]'), 15000);
      };
      try {
        await open();

        // K1
        const heads = await headings();
        check(heads.join() === SECTIONS.join(), `${tag} K1 sections`, "the four sections in order", `the sections are: ${heads.join(", ")}`);
        const want = [[1, 2, 3], [5, 6], [7], [8]].map((l) => l.map(idOf));
        const got = [await idsOf(SECTIONS[0]), await idsOf(SECTIONS[1]), await idsOf(SECTIONS[2]), await idsOf(SECTIONS[3])];
        check(JSON.stringify(got) === JSON.stringify(want), `${tag} K1 articles`, "each article is in the section of its reading time", `drawn ${JSON.stringify(got)}, wanted ${JSON.stringify(want)}`);
        const more = section(SECTIONS[0]).getByRole("button", { name: "1 more, Under 3 minutes", exact: true });
        check((await more.count()) === 1 && (await more.innerText()).trim() === "1 more", `${tag} K1 more`, "\"1 more\" in the first section, which the heading names", `${await more.count()} button(s), text "${await section(SECTIONS[0]).locator("[data-stop-id]").allInnerTexts()}"`);
        await t.shot(page, `${tag}-coffee`);

        // K2
        await more.click();
        const opened = await settled(() => document.querySelectorAll("section[data-section] article[data-item-id]").length >= 8);
        const first = await idsOf(SECTIONS[0]);
        check(opened && first.length === 4 && (await section(SECTIONS[0]).getByRole("button", { name: /^(\d+ more|More), / }).count()) === 0, `${tag} K2 expand`, "the section opens in place to all four articles and its button goes", `${first.length} articles, ${await section(SECTIONS[0]).getByRole("button", { name: /^(\d+ more|More), / }).count()} button(s)`);
        check((await headings()).join() === SECTIONS.join(), `${tag} K2 order`, "the sections keep their order", (await headings()).join(", "));

        // K3: Mark below on the first article of the opened section; then Mark above on the last of 3 to 7 minutes.
        await openMenu(first[0]);
        await page.getByRole("menuitem", { name: /^Mark below as read/ }).click();
        const below = await page.waitForFunction(
          async (ids) => (await Promise.all(ids.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true))).every(Boolean),
          first.slice(1),
          { timeout: 8000 },
        ).then(() => true, () => false);
        const rest = await readState([first[0], ...[5, 6, 7, 8].map(idOf)]);
        check(below && rest.every((r) => !r), `${tag} K3 below`, "Mark below marks the rest of its section and no other", `below read: ${below}; the article and the other sections read: ${rest.join()}`);
        const mid = await idsOf(SECTIONS[1]);
        await openMenu(mid[mid.length - 1]);
        await page.getByRole("menuitem", { name: /^Mark above as read/ }).click();
        const above = await page.waitForFunction(
          async (ids) => (await Promise.all(ids.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true))).every(Boolean),
          mid.slice(0, -1),
          { timeout: 8000 },
        ).then(() => true, () => false);
        const others = await readState([mid[mid.length - 1], idOf(1), idOf(7), idOf(8)]);
        check(above && others.every((r) => !r), `${tag} K3 above`, "Mark above marks the earlier articles of its section and no other", `above read: ${above}; the article and the other sections read: ${others.join()}`);
        await unreadAll(STORIES.map(({ n }) => idOf(n)));
        // The mark-read request the Unknown length section sends for Mark all in it: the folder, and unknown_length.
        const unknown = await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { scope: { view: "unread", folder_id: folder.id, unknown_length: true }, read: true } });
        const afterUnknown = await readState(STORIES.map(({ n }) => idOf(n)));
        check(unknown.ok() && afterUnknown.join() === STORIES.map(({ n }) => n === 8).join(), `${tag} K3 unknown`, "marking the unknown-length scope reads the article with no text and nothing else", `status ${unknown.status()}, read: ${afterUnknown.join()}`);
        await unreadAll(STORIES.map(({ n }) => idOf(n)));

        // K4: Mark all, then Undo.
        await open();
        await page.getByRole("button", { name: "List actions" }).click();
        await page.getByRole("menuitem", { name: /^Mark (all|everything)/ }).click();
        await page.getByRole("dialog").getByRole("button", { name: /^Mark (all|everything)/ }).click();
        const ids = STORIES.map(({ n }) => idOf(n));
        const allRead = await page.waitForFunction(
          async (list) => (await Promise.all(list.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true))).every(Boolean),
          ids,
          { timeout: 10000 },
        ).then(() => true, () => false);
        check(allRead, `${tag} K4 mark all`, "Mark all marks every section", `read: ${(await readState(ids)).join()}`);
        await page.getByRole("button", { name: "Undo" }).first().click();
        const back = await page.waitForFunction(
          async (list) => (await Promise.all(list.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true))).every((r) => !r),
          ids,
          { timeout: 10000 },
        ).then(() => true, () => false);
        check(back, `${tag} K4 undo`, "one Undo puts every section back", `read: ${(await readState(ids)).join()}`);

        // K5: a fresh page; one Tab stop; the arrow keys follow the drawn order.
        await open();
        const stops = await page.locator('section[data-section] article[data-item-id] a:not([tabindex="-1"])').count();
        check(stops === 1, `${tag} K5 tab stop`, "the page has one Tab stop", `${stops} article links are in the Tab order`);
        const drawn = await page.locator("[data-digest] article[data-item-id], [data-digest] [data-stop-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId ?? e.dataset.stopId));
        const focusId = () => page.evaluate(() => document.activeElement?.closest("[data-item-id],[data-stop-id]")?.getAttribute("data-item-id") ?? document.activeElement?.closest("[data-stop-id]")?.getAttribute("data-stop-id") ?? null);
        // After an arrow key, focus has landed on another row; if it never does, the check that follows reports what it found.
        const focusLeft = (id) => expect.poll(async () => { const now = await focusId(); return now !== null && now !== id; }, { timeout: 3000, intervals: [50] }).toBe(true).catch(() => {});
        await page.locator("[data-digest] article[data-item-id] a").first().focus();
        const visited = [await focusId()];
        for (let i = 1; i < Math.min(drawn.length, 7); i++) {
          await page.keyboard.press("ArrowDown");
          await focusLeft(visited[visited.length - 1]);
          visited.push(await focusId());
        }
        check(visited.join() === drawn.slice(0, visited.length).join(), `${tag} K5 arrows`, "ArrowDown visits articles and the \"more\" button in the drawn order", `visited ${visited.join(" ")}, drawn ${drawn.slice(0, visited.length).join(" ")}`);
        await page.keyboard.press("ArrowUp");
        await focusLeft(visited[visited.length - 1]);
        check((await focusId()) === drawn[visited.length - 2], `${tag} K5 up`, "ArrowUp goes back one", `focus is on ${await focusId()}, wanted ${drawn[visited.length - 2]}`);

        // K6
        await layoutMenu();
        const menu = page.getByRole("menu");
        const groups = await menu.getByRole("group").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
        check(groups.includes("Images") && groups.includes("Rows per section") && !groups.includes("Sections") && (await menu.getByText("Coffee break, for this device").count()) === 1, `${tag} K6 menu`, "Images and Rows per section, and no choice of sections", `groups: ${groups.join(", ")}`);
        await menu.getByRole("menuitemradio", { name: "5", exact: true }).click();
        const five = await settled(() => document.querySelectorAll('section[data-section] article[data-item-id]').length >= 7);
        check(five && (await idsOf(SECTIONS[0])).length === 4, `${tag} K6 rows`, "5 rows per section shows the first section whole", `${(await idsOf(SECTIONS[0])).length} articles in it`);
        await patch({ "client.digest_rows": 3 });

        // K7
        for (const [what, path] of [["All", `/l/all?folder=${folder.id}`], ["a reading-time filter", `${url}&len=short`]]) {
          await page.goto(path, { waitUntil: "load" });
          await page.locator("article[data-item-id]").first().waitFor({ timeout: 20000 });
          const sections = await page.locator("section[data-section]").count();
          await layoutMenu();
          const said = await page.getByText(/Shown as rows: the Coffee break groups Unread/).count();
          await page.keyboard.press("Escape");
          check(sections === 0 && said === 1, `${tag} K7 ${what}`, `${what} is a plain list, and the menu says why`, `${sections} section(s); explanation shown: ${said}`);
        }

        // K8
        await page.goto(`${url}&len=under3`, { waitUntil: "load" });
        await page.locator("section[data-section] article[data-item-id]").first().waitFor({ timeout: 20000 });
        check((await headings()).join() === SECTIONS.join(), `${tag} K8 len`, "a section's length in the address changes nothing", (await headings()).join(", "));

        const bad = requests.filter((p) => /export|download|\.opml/i.test(p));
        check(bad.length === 0, `${tag} K9 no export`, "no export or download address was asked for", `asked for ${bad.join(", ")}`);
      } finally {
        await unreadAll(STORIES.map(({ n }) => idOf(n)));
        await patch({ "client.layout": null, "client.digest_images": null, "client.digest_rows": null });
      }
    });
  });
} finally {
  server.close();
}

await t.finish("Coffee break layout");
