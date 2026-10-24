// UAT: the New page (#39).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:catchup                in another
//   npm run uat:catchup -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The flow serves two feeds on a loopback port (eight stories and four), puts each in a folder of its own ("UAT New A",
// "UAT New B") and, for each width (desktop 1280x800, phone 375x812), checks:
//   C1  the New pill is the first of the Show pills on the library and opens /l/new;
//   C2  each folder is a section with "N new" in its heading and its newest three stories, then "N more in <folder>";
//   C3  "more" opens the section in place, with focus on the first story it brought in (Enter on the button);
//   C4  the arrow keys visit the stories and buttons in the order they are drawn;
//   C5  opening a story from the page and coming back leaves the account's mark where it was;
//   C6  leaving the page moves the mark to the newest story the page showed;
//   C7  after two more stories arrive in one folder, the page shows that folder with "2 new" and the other not at all;
//   C8  with nothing new the page says so.
// Chromium only. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

const A = "UAT New A";
const B = "UAT New B";
const TITLES = { a: "UAT feed new a", b: "UAT feed new b" };

let base = "";
let gen = 0; // each width serves feeds of its own, so the second width does not find the first one's stories
const served = { a: 8, b: 4 }; // how many stories each feed has right now; more arrive when this is raised
const server = createServer((req, res) => {
  const which = /^\/(a|b)\.xml$/.exec(new URL(req.url ?? "/", "http://x").pathname)?.[1] ?? null;
  if (!which) return void res.writeHead(404).end();
  // Story n is the n-th oldest; the newest has the highest number, so stories that arrive later are the newest.
  const item = (n) =>
    `<item><title>UAT new ${which} story ${n}</title><link>${base}/page/${which}/${n}</link><guid isPermaLink="false">uat-new-${gen}-${which}-${n}</guid><pubDate>${new Date(Date.now() - (60 - n) * 60_000).toUTCString()}</pubDate><description>&lt;p&gt;Story ${n}.&lt;/p&gt;</description></item>`;
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>${TITLES[which]}</title><link>${base}/</link><description>t</description>${Array.from({ length: served[which] }, (_, i) => item(i + 1)).join("")}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

try {
  await t.eachViewport(async (page, vp) => {
    const tag = vp.id;
    gen++;
    const section = (name) => page.locator("section[data-section]").filter({ has: page.getByRole("heading", { level: 2, name: new RegExp(`^${name}`) }) });
    const idsOf = (name) => section(name).locator("article[data-item-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId));
    const settled = (fn, ms = 8000) => page.waitForFunction(fn, null, { timeout: ms }).then(() => true, () => false);
    const mark = () => page.evaluate(async () => (await (await fetch("/api/catchup", { headers: { Accept: "application/json" } })).json()).mark);
    const asOf = () => page.evaluate(async () => (await (await fetch("/api/catchup", { headers: { Accept: "application/json" } })).json()).as_of);
    // Ids are numbers past 2^53 / 10, so compare as BigInt.
    const gt = (a, b) => BigInt(a) > BigInt(b);
    const ready = async () => {
      await settled(() => !document.querySelector('[data-testid="digest-skeleton"]') && !document.querySelector('[role="status"]'), 15000);
    };
    const openNew = async () => {
      await page.getByRole("link", { name: "New", exact: true }).first().click();
      await page.waitForURL("**/l/new*", { timeout: 10000 });
      await section(A).or(page.getByText("Nothing new")).first().waitFor({ timeout: 20000 });
      await ready();
    };
    const leave = async () => {
      await page.getByRole("link", { name: "Unread", exact: true }).first().click();
      await page.waitForURL("**/l/unread*", { timeout: 10000 });
    };

    await step(`${tag} C`, async () => {
      const added = {};
      for (const k of ["a", "b"]) {
        const r = await t.addLocalFeed(page, `${base}/${k}.xml?g=${gen}`, TITLES[k], (items) => items.length >= served[k]);
        if (r.error) return t.fail(`${tag} C setup`, r.error);
        added[k] = r;
      }
      for (const [name, k] of [[A, "a"], [B, "b"]]) {
        const made = await page.request.post("/api/folders", { data: { name }, headers: t.WRITE });
        if (!made.ok()) return t.fail(`${tag} C setup`, `the folder ${name} was not made (${made.status()})`);
        const folder = (await t.boot(page)).folders.find((x) => x.name === name);
        const moved = await page.request.patch(`/api/feeds/${added[k].feedId}`, { data: { folder_id: folder.id }, headers: t.WRITE });
        if (!moved.ok()) return t.fail(`${tag} C setup`, `the feed was not moved into ${name} (${moved.status()})`);
      }
      await page.goto("/l/unread", { waitUntil: "load" });
      await page.getByRole("link", { name: "New", exact: true }).first().waitFor({ timeout: 20000 });

      // C1
      const pills = await page.getByRole("navigation", { name: "Show" }).getByRole("link").allTextContents().catch(() => null);
      const firstPill = pills ? pills[0] : await page.locator("select option").first().textContent().catch(() => null);
      check(firstPill?.trim() === "New", `${tag} C1 pill`, "New is the first of the Show pills", `the pills are: ${pills?.join(", ") ?? firstPill}`);

      await openNew();
      const startMark = await mark();

      // C2
      const heading = async (name) => (await section(name).getByRole("heading", { level: 2 }).textContent().catch(() => "")) ?? "";
      const shownA = await idsOf(A);
      const shownB = await idsOf(B);
      const moreA = section(A).getByRole("button", { name: `5 more in ${A}`, exact: true });
      check(/8 new/.test(await heading(A)) && shownA.length === 3 && (await moreA.count()) === 1, `${tag} C2 section A`, '"8 new", three stories and "5 more in UAT New A"', `heading "${await heading(A)}", ${shownA.length} stories, ${await moreA.count()} more button(s)`);
      check(/4 new/.test(await heading(B)) && shownB.length === 3, `${tag} C2 section B`, '"4 new" and three stories', `heading "${await heading(B)}", ${shownB.length} stories`);
      const titles = await section(A).locator("article[data-item-id]").allTextContents();
      check(titles[0]?.includes("story 8") && titles[2]?.includes("story 6"), `${tag} C2 newest`, "the newest three stories, newest first", titles.join(" | "));
      await t.shot(page, `${tag}-new`);

      // C4: the arrow keys follow the drawn order.
      const drawn = await page.locator("[data-digest] article[data-item-id], [data-digest] [data-stop-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId ?? e.dataset.stopId));
      const focusId = () => page.evaluate(() => document.activeElement?.closest("[data-item-id],[data-stop-id]")?.getAttribute("data-item-id") ?? document.activeElement?.closest("[data-stop-id]")?.getAttribute("data-stop-id") ?? null);
      await page.locator("[data-digest] article[data-item-id] a").first().focus();
      const visited = [await focusId()];
      for (let i = 1; i < Math.min(drawn.length, 5); i++) {
        await page.keyboard.press("ArrowDown");
        await page.waitForTimeout(80);
        visited.push(await focusId());
      }
      check(visited.join() === drawn.slice(0, visited.length).join(), `${tag} C4 arrows`, "ArrowDown visits stories and buttons in the drawn order", `visited ${visited.join(" ")}, drawn ${drawn.slice(0, visited.length).join(" ")}`);

      // C3
      await moreA.focus();
      await page.keyboard.press("Enter");
      await settled(() => document.querySelectorAll("section[data-section] article[data-item-id]").length >= 11);
      const allA = await idsOf(A);
      await settled(`document.activeElement?.closest("article[data-item-id]")?.getAttribute("data-item-id") === ${JSON.stringify(allA[3] ?? "")}`, 3000);
      const focused = await page.evaluate(() => document.activeElement?.closest("article[data-item-id]")?.getAttribute("data-item-id") ?? null);
      check(allA.length === 8 && (await section(A).getByRole("button", { name: /more in/ }).count()) === 0 && focused === allA[3], `${tag} C3 expand`, "the section opens in place to eight stories, its button goes and focus is on the first new one", `${allA.length} stories; focus on ${focused}, wanted ${allA[3]}`);
      check(JSON.stringify(await idsOf(B)) === JSON.stringify(shownB), `${tag} C3 other`, "the other section is unchanged", "it changed");

      // C5
      await page.locator(`article[data-item-id="${allA[0]}"] a`).first().click();
      await page.waitForURL(`**/i/${allA[0]}?*`, { timeout: 10000 });
      await page.waitForTimeout(500);
      const duringArticle = await mark();
      await page.goBack();
      await section(A).waitFor({ timeout: 15000 });
      check(duringArticle === startMark && (await mark()) === startMark, `${tag} C5 article`, "opening a story and coming back leaves the mark where it was", `the mark was ${startMark}, then ${duringArticle}, then ${await mark()}`);

      // C6
      const pageAsOf = await asOf();
      await leave();
      let moved = false;
      for (let i = 0; i < 30 && !moved; i++) {
        moved = gt(await mark(), startMark);
        if (!moved) await page.waitForTimeout(200);
      }
      // Other feeds may have fetched since the page opened, so the mark is at least the newest story the page showed and no more than the newest there is.
      const nowMark = await mark();
      check(moved && !gt(allA[0], nowMark) && !gt(nowMark, pageAsOf), `${tag} C6 leave`, "leaving the page moves the mark to the newest story it knew of", `the mark was ${startMark}, is ${nowMark}; the page showed up to ${allA[0]}, the newest story is ${pageAsOf}`);

      // C7: two stories arrive in A.
      served.a = 10;
      await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
      const arrived = await (async () => {
        for (let i = 0; i < 40; i++) {
          const r = await page.request.get(`/api/items?view=all&feed=${added.a.feedId}`, { headers: { Accept: "application/json" } });
          if (((await r.json()).items ?? []).length >= 10) return true;
          await page.waitForTimeout(500);
        }
        return false;
      })();
      if (!arrived) return t.fail(`${tag} C7 setup`, "the two new stories never arrived");
      await openNew();
      const newA = await idsOf(A);
      check(/2 new/.test(await heading(A)) && newA.length === 2 && (await section(B).count()) === 0, `${tag} C7 later`, '"2 new" with the two newest stories, and the other folder not shown', `heading "${await heading(A)}", ${newA.length} stories, ${await section(B).count()} section B`);
      const endLine = await page.getByTestId("digest-end").count();
      check(endLine === 1, `${tag} C7 end`, "the page ends with a line saying so", `${endLine} end line(s)`);
      await t.shot(page, `${tag}-new-later`);

      // C8: nothing new.
      await leave();
      for (let i = 0; i < 30 && !gt(await mark(), startMark); i++) await page.waitForTimeout(200);
      await openNew();
      check((await page.getByText("Nothing new").count()) >= 1, `${tag} C8 empty`, "with nothing new the page says so", "no empty message");
      await t.shot(page, `${tag}-new-empty`);
      await leave();
    });
    served.a = 8;
  });
} finally {
  server.close();
}

await t.finish("New page");
