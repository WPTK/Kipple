// UAT: the Feed index (#39).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:feed-index             in another
//   npm run uat:feed-index -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The flow serves three feeds on a loopback port: "b" with five stories and "z" with two (both in the folder "UAT Index
// A"), and "c" with one story that stops answering after it is added (alone in "UAT Index B"). For each width (desktop
// 1280x800, phone 375x812) it checks:
//   I1  the way in: the sidebar's "Feed index" entry on a wide screen; on a phone the Feeds tab, then "Feed index" on it,
//       with the Feeds tab still the current one;
//   I2  one block for each feed, in the sidebar's order (folder A by title, then folder B), each with its unread count and
//       its newest three headlines, newest first (fewer when the feed has fewer);
//   I3  the feed's name opens that feed's list;
//   I4  the feed that can't be fetched says so, and the others say nothing;
//   I5  the page is one Tab stop and the arrow keys visit the feed names and the headlines in the order drawn;
//   I6  opening a headline shows the article, and coming back shows that headline as read;
//   I7  "Quiet Feeds First" in the Order menu keeps feeds that have no pace yet (all of these are new) in the sidebar's
//       order, after any feed that has one.
// Chromium only. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

const FOLDER_A = "UAT Index A";
const FOLDER_B = "UAT Index B";
const TITLES = { b: "UAT feed index b", z: "UAT feed index z", c: "UAT feed index c" };
const COUNT = { b: 5, z: 2, c: 1 };
const REASON_500 = "The site had a problem answering";

let base = "";
let gen = 0; // each width serves feeds of its own, so the second width does not find the first one's stories
const broken = { c: false };
const server = createServer((req, res) => {
  const which = /^\/(b|z|c)\.xml$/.exec(new URL(req.url ?? "/", "http://x").pathname)?.[1] ?? null;
  if (!which) return void res.writeHead(404).end();
  if (broken[which]) return void res.writeHead(500, { "Content-Type": "text/plain" }).end("broken on purpose");
  // Story n is the n-th oldest; the newest has the highest number.
  const item = (n) =>
    `<item><title>UAT index ${which} story ${n}</title><link>${base}/page/${which}/${n}</link><guid isPermaLink="false">uat-index-${gen}-${which}-${n}</guid><pubDate>${new Date(Date.now() - (60 - n) * 60_000).toUTCString()}</pubDate><description>&lt;p&gt;Story ${n}.&lt;/p&gt;</description></item>`;
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>${TITLES[which]}</title><link>${base}/</link><description>t</description>${Array.from({ length: COUNT[which] }, (_, i) => item(i + 1)).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    const wide = vp.id.startsWith("desktop");
    gen++;
    broken.c = false;
    const settled = (fn, arg, ms = 10000) => page.waitForFunction(fn, arg, { timeout: ms }).then(() => true, () => false);
    // The names of my three blocks, in the order they are drawn (the seeded library has feeds of its own).
    const mine = () => page.locator("section[data-feed-block] > h2 a").evaluateAll((els) => els.map((e) => e.textContent).filter((s) => s?.toLowerCase().startsWith("uat feed index")));
    const block = (title) => page.locator("section[data-feed-block]").filter({ has: page.getByRole("link", { name: title, exact: true }) });
    const headlinesOf = (title) => block(title).locator("article[data-item-id] h3").allTextContents();
    const ready = async () => {
      await page.getByTestId("feed-index-scroll").locator("[data-feed-index]").waitFor({ timeout: 20000 });
      return settled(() => document.querySelectorAll("section[data-feed-block] article[data-item-id]").length > 0);
    };

    await step(`${tag} I`, async () => {
      const added = {};
      for (const [k, folder] of [["b", FOLDER_A], ["z", FOLDER_A], ["c", FOLDER_B]]) {
        const r = await t.addLocalFeed(page, `${base}/${k}.xml?g=${gen}`, TITLES[k], (items) => items.length >= COUNT[k]);
        if (r.error) return t.fail(`${tag} I setup`, r.error);
        added[k] = { ...r, folder };
      }
      for (const name of [FOLDER_A, FOLDER_B]) {
        const made = await page.request.post("/api/folders", { data: { name }, headers: t.WRITE });
        if (!made.ok()) return t.fail(`${tag} I setup`, `the folder ${name} was not made (${made.status()})`);
      }
      const b0 = await t.boot(page);
      for (const k of ["b", "z", "c"]) {
        const folder = b0.folders.find((x) => x.name === added[k].folder);
        const moved = await page.request.patch(`/api/feeds/${added[k].feedId}`, { data: { folder_id: folder.id }, headers: t.WRITE });
        if (!moved.ok()) return t.fail(`${tag} I setup`, `the feed was not moved into ${added[k].folder} (${moved.status()})`);
      }
      // Feed c stops answering: refresh until it records the failure.
      broken.c = true;
      let failing = null;
      for (let i = 0; i < 3 && !failing; i++) {
        await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
        const s = await t.settle(page, (b) => b.feeds.some((f) => f.id === added.c.feedId && f.last_error), i === 0 ? 30000 : 12000);
        failing = s?.b?.feeds.find((f) => f.id === added.c.feedId && f.last_error) ?? null;
      }
      if (!failing) return t.fail(`${tag} I setup`, "feed c never recorded a failed fetch");

      // I1: the way in.
      await page.goto("/l/unread", { waitUntil: "load" });
      if (wide) {
        const entry = page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Feed index", exact: true });
        await entry.waitFor({ timeout: 20000 });
        await entry.click();
      } else {
        await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Feeds", exact: true }).click();
        await page.waitForURL("**/feeds", { timeout: 10000 });
        await page.getByRole("main").getByRole("link", { name: "Feed index", exact: true }).click();
      }
      await page.waitForURL("**/feeds/index", { timeout: 10000 });
      const heading = await page.getByRole("heading", { level: 1, name: "Feed index" }).waitFor({ timeout: 10000 }).then(() => true, () => false);
      const current = await page
        .getByRole("navigation", { name: "Primary" })
        .getByRole("link", { name: wide ? "Feed index" : "Feeds", exact: true })
        .getAttribute("aria-current");
      check(heading && current === "page", `${tag} I1 entry`, wide ? "the sidebar's Feed index opens the page and is marked current" : "Feeds, then Feed index opens the page and the Feeds tab stays current", `heading ${heading}, aria-current ${current}`);
      check(await ready(), `${tag} I2 ready`, "the blocks loaded", "no headline appeared");

      // I2
      const names = await mine();
      check(JSON.stringify(names) === JSON.stringify([TITLES.b, TITLES.z, TITLES.c]), `${tag} I2 order`, "the feeds in the sidebar's order (folder A by title, then folder B)", names.join(" | "));
      const hb = await headlinesOf(TITLES.b);
      check(hb.length === 3 && hb[0].includes("story 5") && hb[2].includes("story 3"), `${tag} I2 three`, "feed b shows its newest three, newest first", hb.join(" | "));
      const hz = await headlinesOf(TITLES.z);
      check(hz.length === 2 && hz[0].includes("story 2"), `${tag} I2 fewer`, "feed z, with two stories, shows two", hz.join(" | "));
      const unreadB = await block(TITLES.b).getByTestId("feed-index-unread").textContent();
      check(unreadB?.trim() === "5 unread", `${tag} I2 count`, 'feed b says "5 unread"', `it says "${unreadB}"`);
      await t.shot(page, `${tag}-feed-index`);

      // I4
      const note = block(TITLES.c).getByTestId("feed-index-failure");
      const noteText = (await note.textContent().catch(() => "")) ?? "";
      check(noteText.includes("Kipple can't fetch this feed") && noteText.includes(REASON_500), `${tag} I4 failing`, "the failing feed says Kipple can't fetch it, and why in words", `it says "${noteText}"`);
      check((await block(TITLES.b).getByTestId("feed-index-failure").count()) === 0 && (await block(TITLES.z).getByTestId("feed-index-failure").count()) === 0, `${tag} I4 others`, "the feeds that work say nothing", "a working feed shows a failure line");

      // I5: one Tab stop, the arrow keys follow the drawn order.
      const stops = await page.locator("[data-feed-index] a").evaluateAll((els) => els.filter((a) => a.tabIndex >= 0).length);
      check(stops === 1, `${tag} I5 one stop`, "exactly one link of the page is in the Tab order", `${stops} links are`);
      const drawn = await page.locator("[data-feed-index] article[data-item-id], [data-feed-index] [data-stop-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId ?? e.dataset.stopId));
      const focusId = () => page.evaluate(() => document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id") ?? document.activeElement?.closest("[data-stop-id]")?.getAttribute("data-stop-id") ?? null);
      await page.locator("[data-feed-index] a").first().focus();
      const visited = [await focusId()];
      for (let i = 1; i < Math.min(drawn.length, 6); i++) {
        const before = visited[visited.length - 1];
        await page.keyboard.press("ArrowDown");
        await page
          .waitForFunction((b) => (document.activeElement?.closest("[data-item-id]")?.getAttribute("data-item-id") ?? document.activeElement?.closest("[data-stop-id]")?.getAttribute("data-stop-id") ?? null) !== b, before, { timeout: 3000 })
          .catch(() => {});
        visited.push(await focusId());
      }
      check(visited.join() === drawn.slice(0, visited.length).join(), `${tag} I5 arrows`, "ArrowDown visits feed names and headlines in the drawn order", `visited ${visited.join(" ")}, drawn ${drawn.slice(0, visited.length).join(" ")}`);
      const stopAfter = await page.locator("[data-feed-index] a").evaluateAll((els) => els.filter((a) => a.tabIndex >= 0).map((a) => a.closest("[data-item-id],[data-stop-id]")?.getAttribute("data-item-id") ?? a.closest("[data-stop-id]")?.getAttribute("data-stop-id")));
      check(stopAfter.length === 1 && stopAfter[0] === visited[visited.length - 1], `${tag} I5 stop follows`, "the one Tab stop is the row last visited", `the stop is ${stopAfter.join(",")}, last visited ${visited[visited.length - 1]}`);
      await page.keyboard.press("Home");
      await settled((id) => document.activeElement?.closest("[data-stop-id]")?.getAttribute("data-stop-id") === id, drawn[0], 3000);
      await page.keyboard.press("Tab");
      const leftList = await page.evaluate(() => !document.activeElement?.closest("[data-feed-index]"));
      check(leftList, `${tag} I5 tab`, "Tab leaves the page's rows in one press", "Tab went to another row");

      // I3: the name opens the feed's list.
      await block(TITLES.b).getByRole("link", { name: TITLES.b, exact: true }).click();
      await page.waitForURL(/\/l\/[a-z]+\?.*feed=/, { timeout: 10000 });
      const listed = await page.getByRole("heading", { name: TITLES.b }).first().waitFor({ timeout: 10000 }).then(() => true, () => false);
      check(listed, `${tag} I3 feed`, "the feed's name opens that feed's list", `at ${page.url()}`);
      await page.goBack();
      await ready();

      // I6: open a headline, come back, see it read.
      const first = block(TITLES.b).locator("article[data-item-id]").first();
      const id = await first.getAttribute("data-item-id");
      const wasRead = await first.getAttribute("data-read");
      await first.getByRole("link").click();
      await page.waitForURL(`**/i/${id}?*`, { timeout: 10000 });
      const open = await page.getByRole("heading", { name: /UAT index b story 5/ }).first().waitFor({ timeout: 15000 }).then(() => true, () => false);
      await t.shot(page, `${tag}-feed-index-article`);
      await page.goBack();
      await ready();
      const nowRead = await settled((i) => document.querySelector(`article[data-item-id="${i}"]`)?.hasAttribute("data-read"), id, 15000);
      check(open && wasRead === null && nowRead, `${tag} I6 open`, "the headline opens the article and shows as read when you come back", `opened ${open}, read before ${wasRead}, read after ${nowRead}`);

      // I7: Quiet Feeds First keeps new feeds (no pace yet) in the sidebar's order.
      await page.getByRole("button", { name: "Order", exact: true }).click();
      await page.getByRole("menuitemradio", { name: /Quiet Feeds First/ }).click();
      await page.keyboard.press("Escape");
      const quiet = await mine();
      check(JSON.stringify(quiet) === JSON.stringify([TITLES.b, TITLES.z, TITLES.c]), `${tag} I7 quiet`, "feeds with no pace yet keep the sidebar's order", quiet.join(" | "));
      // The choice is the device's: the menu shows it chosen when it is opened again.
      await page.getByRole("button", { name: "Order", exact: true }).click();
      const chosen = await page.getByRole("menuitemradio", { name: /Quiet Feeds First/ }).getAttribute("aria-checked");
      check(chosen === "true", `${tag} I7 chosen`, "Quiet Feeds First shows as chosen", `aria-checked is ${chosen}`);
      await t.shot(page, `${tag}-feed-index-quiet`);
      await page.getByRole("menuitemradio", { name: /Sidebar order/ }).click();
      await page.keyboard.press("Escape");
    });
  });
} finally {
  server.close();
}

await t.finish("Feed index");
