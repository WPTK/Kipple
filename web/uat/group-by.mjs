// UAT: the list's "Group by" choice, Day, Week or None (#39).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:group-by               in another
//   npm run uat:group-by -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed on a loopback port: eight articles over the last twenty days, two of them today. It opens
// the feed's list in the Headlines layout (every row on screen at once) and, for each width (desktop 1280x800, phone
// 375x812), checks:
//   G1  the default is one header per day, in newest-first order, and each header is a level 2 heading;
//   G2  Group by Week in the list's options menu shows Today, Yesterday, This week and Earlier, each once and only the ones
//       that have articles, and the feed's own choice is stored in the device profile;
//   G3  Oldest first turns the week buckets round (Earlier first, Today last);
//   G4  Group by None shows the articles and no header, and every article is still there;
//   G5  the choice survives a reload; the menu's first choice names the device default and clears the feed's own choice;
//   G6  desktop: ArrowDown steps focus through the articles one by one in every grouping, never stopping on a header.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 18);
const { check, step } = t;

const FEED_TITLE = "UAT feed group by";
const NOW = Date.now();
const DAY_MS = 86_400_000;
// Local noon of a day `back` days ago, but never later than a minute ago; the second article of a day is an hour earlier.
const stamp = (back, nth) => {
  const d = new Date(NOW);
  const noon = new Date(d.getFullYear(), d.getMonth(), d.getDate() - back, 12).getTime();
  return Math.min(noon, NOW - 60_000) - nth * 3600_000;
};
const ARTICLES = [
  { n: 1, at: stamp(0, 0) },
  { n: 2, at: stamp(0, 1) },
  { n: 3, at: stamp(1, 0) },
  { n: 4, at: stamp(2, 0) },
  { n: 5, at: stamp(4, 0) },
  { n: 6, at: stamp(9, 0) },
  { n: 7, at: stamp(14, 0) },
  { n: 8, at: stamp(20, 0) },
];

/** The header a week bucket should have, worked out here from the calendar rather than with the app's own code. */
function weekHeader(at) {
  const now = new Date(NOW);
  const startOf = (ms) => {
    const d = new Date(ms);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  };
  const ago = Math.round((startOf(NOW) - startOf(at)) / DAY_MS);
  if (ago <= 0) return "Today";
  if (ago === 1) return "Yesterday";
  return ago <= (now.getDay() + 6) % 7 ? "This week" : "Earlier";
}
const expectedWeek = (articles) => {
  const out = [];
  for (const a of articles) {
    const h = weekHeader(a.at);
    if (out[out.length - 1] !== h) out.push(h);
  }
  return out;
};
const dayCount = new Set(ARTICLES.map((a) => new Date(a.at).toDateString())).size;

let base = "";
const server = createServer((req, res) => {
  const path = new URL(req.url ?? "/", "http://x").pathname;
  if (path !== "/feed.xml") return void res.writeHead(404).end();
  const item = (a) =>
    `<item><title>UAT group by story ${a.n}</title><link>${base}/page/${a.n}</link><guid isPermaLink="false">uat-group-by-${a.n}</guid><pubDate>${new Date(a.at).toUTCString()}</pubDate><description>&lt;p&gt;Story ${a.n}.&lt;/p&gt;</description></item>`;
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT group by</title><link>${base}/</link><description>t</description>${ARTICLES.map(item).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    const patch = (b) =>
      page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
    const profile = () => page.evaluate(() => fetch("/api/device").then((r) => r.json())).then((d) => d.profile ?? {});
    const scroller = '[data-testid="list-scroll"]';
    // textContent: the headers are drawn in capitals, and innerText would return them so.
    const headers = () => page.locator(`${scroller} h2`).evaluateAll((els) => els.map((e) => (e.textContent ?? "").trim()));
    const ownGroup = async (feedId, want) => {
      let entry;
      for (let i = 0; i < 40; i++) {
        entry = (await profile())["client.list_overrides"]?.feed?.[feedId];
        if (entry?.group === want) break;
        await page.waitForTimeout(250);
      }
      return entry;
    };
    const headerTags = () => page.locator(`${scroller} h2`).evaluateAll((els) => els.map((e) => e.tagName));
    const stories = () => page.locator(`${scroller} [data-item-id]`).evaluateAll((els) => els.map((e) => e.dataset.itemId));
    const settled = (want) => page.waitForFunction((n) => document.querySelectorAll('[data-testid="list-scroll"] [data-item-id]').length >= n, want, { timeout: 20000 });
    /** Choose one radio of the list options menu by its group and visible name. */
    const choose = async (group, name) => {
      await page.locator('button[aria-label^="List options, "]').first().click();
      await page.getByRole("menuitemradio", { name }).and(page.getByRole("group", { name: group }).getByRole("menuitemradio")).click();
    };

    await step(`${tag} G`, async () => {
      for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED_TITLE)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
      const added = await t.addLocalFeed(page, `${base}/feed.xml`, FEED_TITLE, (items) => items.length >= ARTICLES.length);
      if (added.error) return t.fail(`${tag} G setup`, added.error);
      const set = await patch({ "client.layout": "headlines" });
      if (set !== 200) return t.fail(`${tag} G setup`, `the Headlines layout was not set (${set})`);
      try {
        await page.goto(`/l/all?feed=${added.feedId}`, { waitUntil: "load" });
        await settled(ARTICLES.length);

        // G1
        const days = await headers();
        const tags = await headerTags();
        check(
          days.length === dayCount && days[0] === "Today" && tags.every((x) => x === "H2"),
          `${tag} G1`,
          `one level 2 heading per day (${dayCount}), Today first`,
          `headings ${JSON.stringify(days)} as ${tags.join(",")}, want ${dayCount} days`,
        );
        await t.shot(page, `${tag}-group-day`);

        // G2
        await choose(/^Grouping of this feed$/, /^Week/);
        await page.waitForFunction(() => [...document.querySelectorAll('[data-testid="list-scroll"] h2')].some((h) => /^(This week|Earlier)$/.test(h.textContent.trim())), null, { timeout: 8000 }).catch(() => {});
        const newestFirst = ARTICLES.slice().sort((a, b) => b.at - a.at);
        const week = await headers();
        check(
          week.join("|") === expectedWeek(newestFirst).join("|"),
          `${tag} G2`,
          `Week shows ${expectedWeek(newestFirst).join(", ")}`,
          `headings ${JSON.stringify(week)}, want ${JSON.stringify(expectedWeek(newestFirst))}`,
        );
        const own = await ownGroup(added.feedId, "week");
        check(own?.group === "week", `${tag} G2 stored`, "the feed's own choice is in the device profile", `profile entry ${JSON.stringify(own)}`);
        await t.shot(page, `${tag}-group-week`);

        // G3
        await choose(/^Order of this feed$/, /^Oldest first/);
        const oldestFirst = ARTICLES.slice().sort((a, b) => a.at - b.at);
        const want3 = expectedWeek(oldestFirst);
        await page.waitForFunction((first) => document.querySelector('[data-testid="list-scroll"] h2')?.textContent.trim() === first, want3[0], { timeout: 8000 }).catch(() => {});
        const flipped = await headers();
        check(flipped.join("|") === want3.join("|"), `${tag} G3`, `Oldest first reads ${want3.join(", ")}`, `headings ${JSON.stringify(flipped)}, want ${JSON.stringify(want3)}`);
        await t.shot(page, `${tag}-group-week-oldest`);

        // G6 (desktop: a mouse and a keyboard; a phone opens the article full screen)
        if (!vp.mobile) {
          const seq = async (group) => {
            await page.goto(`/l/all?feed=${added.feedId}`, { waitUntil: "load" });
            await settled(ARTICLES.length);
            const order = await stories();
            await page.locator(`${scroller} article[data-item-id] a`).first().focus();
            const seen = [];
            for (let i = 0; i < 5; i++) {
              await page.keyboard.press("ArrowDown");
              const at = await page
                .waitForFunction((prev) => {
                  const id = document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id");
                  return id && id !== prev ? id : null;
                }, seen.at(-1) ?? order[0], { timeout: 3000 })
                .then((h) => h.jsonValue())
                .catch(() => null);
              seen.push(at);
            }
            check(
              seen.join() === order.slice(1, 6).join(),
              `${tag} G6 ${group}`,
              "ArrowDown steps through the next five articles one by one",
              `focus reached ${seen.join(" ")}, the list reads ${order.slice(1, 6).join(" ")}`,
            );
          };
          await seq("week");
          await choose(/^Grouping of this feed$/, /^Day/);
          await seq("day");
        }

        // G4
        await choose(/^Grouping of this feed$/, /^None/);
        await page.waitForFunction(() => document.querySelectorAll('[data-testid="list-scroll"] h2').length === 0, null, { timeout: 8000 }).catch(() => {});
        const none = await headers();
        const all = await stories();
        check(none.length === 0 && all.length === ARTICLES.length, `${tag} G4`, `None shows all ${ARTICLES.length} articles and no header`, `${none.length} headers, ${all.length} articles`);
        await t.shot(page, `${tag}-group-none`);

        // G5
        await page.reload({ waitUntil: "load" });
        await settled(ARTICLES.length);
        check((await headers()).length === 0, `${tag} G5 reload`, "None survives a reload", `headings ${JSON.stringify(await headers())}`);
        await page.locator('button[aria-label^="List options, "]').first().click();
        const first = page.getByRole("group", { name: /^Grouping of this feed$/ }).getByRole("menuitemradio").first();
        const firstName = (await first.innerText()).trim();
        check(/^Use device default \(Day\)/.test(firstName), `${tag} G5 name`, "the first choice names the device default", `the first choice reads "${firstName}"`);
        await first.click();
        await page.waitForFunction(() => document.querySelectorAll('[data-testid="list-scroll"] h2').length > 0, null, { timeout: 8000 }).catch(() => {});
        const back = await headers();
        const left = await ownGroup(added.feedId, undefined);
        check(back.length === dayCount && left?.group === undefined, `${tag} G5 clear`, "the first choice clears the feed's choice and the days return", `headings ${JSON.stringify(back)}, entry ${JSON.stringify(left)}`);
      } finally {
        await patch({ "client.layout": null, "client.list_overrides": { feed: {}, folder: {} } });
      }
    });
  });
} finally {
  server.close();
}

await t.finish("Group by");
