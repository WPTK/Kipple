// UAT: the Digest layout (#39).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:digest                 in another
//   npm run uat:digest -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The flow serves two feeds on a loopback port (eight stories, two with a picture; and four), puts each in a folder of its
// own ("UAT Digest A", "UAT Digest B") and opens Unread with the Digest as the layout. For each width (desktop 1280x800,
// phone 375x812) it checks:
//   G1  each folder is a section with its name as a heading, in sidebar order, and shows its first three unread stories
//       and "5 more in UAT Digest A" (the Rows per section setting);
//   G2  Images: a story with a picture shows it on the right; Text only (set in the Layout menu) shows none;
//   G3  Quiet Feeds First is a choice in the Layout menu and is saved for the device. A feed's pace is counted from days
//       of new articles and a seeded library has none, so the order itself (slowest first) is checked by the planner's unit
//       tests, and here only that the sections stay in sidebar order;
//   G4  "5 more" opens the section in place: all eight stories, the button gone, the other section unmoved in order;
//   G5  Mark below as read on a story marks the stories after it in its section and no other; Mark above likewise;
//   G6  the arrow keys visit the stories and the "more" button in the order they are drawn, and the page has one Tab stop;
//   G7  opening a story and going Back keeps the section open;
//   G6b Enter on the "more" button opens the section and puts focus on the first article it brought in;
//   G9  with more than six sections, the later ones load when the end of the page is reached;
//   G10 mark as read while scrolling marks the articles scrolled past, and only those;
//   and no request goes to an export or download address.
// Chromium only. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { Buffer } from "node:buffer";
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

const A = "UAT Digest A";
const B = "UAT Digest B";
const EXTRA = Array.from({ length: 16 }, (_, i) => `x${i + 1}`); // ten-story feeds in folders of their own: with the rest, more than the six sections a page opens with
const FEEDS = { a: { title: "UAT feed digest a", count: 8, withPicture: [1, 2] }, b: { title: "UAT feed digest b", count: 4, withPicture: [] } };
// A 1x1 GIF, so a picture's address answers.
const GIF = Buffer.from("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7", "base64");

let base = "";
const server = createServer((req, res) => {
  const path = new URL(req.url ?? "/", "http://x").pathname;
  if (path.startsWith("/img/")) return void res.writeHead(200, { "Content-Type": "image/gif" }).end(GIF);
  const which = /^\/(a|b|x\d+)\.xml$/.exec(path)?.[1] ?? null;
  if (!which) return void res.writeHead(404).end();
  const f = FEEDS[which] ?? { title: `UAT feed digest ${which}`, count: 10, withPicture: [] };
  const item = (n) => {
    const body = f.withPicture.includes(n) ? `&lt;p&gt;Story ${n}.&lt;/p&gt;&lt;img src="${base}/img/${which}${n}.gif" width="600" height="400"/&gt;` : `&lt;p&gt;Story ${n}.&lt;/p&gt;`;
    return `<item><title>UAT digest ${which} story ${n}</title><link>${base}/page/${which}/${n}</link><guid isPermaLink="false">uat-digest-${which}-${n}</guid><pubDate>${new Date(Date.now() - (n + (which === "b" ? 100 : 0)) * 3600_000).toUTCString()}</pubDate><description>${body}</description></item>`;
  };
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>${f.title}</title><link>${base}/</link><description>t</description>${Array.from({ length: f.count }, (_, i) => item(i + 1)).join("")}</channel></rss>`);
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
    // Every section draws its heading; only the ones asked for have articles.
    const loaded = () => page.locator("section[data-section]:has(article[data-item-id]) > h2").allTextContents();
    const headings = () => page.locator("section[data-section] > h2").allTextContents();
    const mine = async () => (await headings()).filter((h) => h === A || h === B);
    const isRead = (id) => page.evaluate(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true, id);
    const readOf = async (ids) => Promise.all(ids.map(isRead));
    const settled = (fn, ms = 8000) => page.waitForFunction(fn, null, { timeout: ms }).then(() => true, () => false);
    const openMenu = async (id) => {
      const row = page.locator(`article[data-item-id="${id}"]`);
      await row.scrollIntoViewIfNeeded();
      if (vp.mobile) {
        // A long press, as a real touch sequence: the More button is not tapped on touch.
        await page.waitForTimeout(300);
        const box = await row.getByRole("link").boundingBox();
        const cdp = await page.context().newCDPSession(page);
        const touch = (type, points) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points });
        await touch("touchStart", [{ x: Math.round(box.x + 10), y: Math.round(box.y + box.height / 2) }]);
        await page.waitForTimeout(800);
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
    const open = async () => {
      await page.goto("/l/unread", { waitUntil: "load" });
      await page.locator("section[data-section] article[data-item-id]").first().waitFor({ timeout: 20000 });
      await settled(() => !document.querySelector('[data-testid="digest-skeleton"]'), 15000);
    };

    await step(`${tag} G`, async () => {
      // Set up: two feeds, each in a folder of its own.
      const added = {};
      for (const [k, f] of Object.entries(FEEDS)) {
        const r = await t.addLocalFeed(page, `${base}/${k}.xml`, f.title, (items) => items.length >= f.count);
        if (r.error) return t.fail(`${tag} G setup`, r.error);
        added[k] = r;
      }
      for (const [name, k] of [[A, "a"], [B, "b"]]) {
        const made = await page.request.post("/api/folders", { data: { name }, headers: t.WRITE });
        if (!made.ok()) return t.fail(`${tag} G setup`, `the folder ${name} was not made (${made.status()})`);
        const folder = (await t.boot(page)).folders.find((x) => x.name === name);
        const moved = await page.request.patch(`/api/feeds/${added[k].feedId}`, { data: { folder_id: folder.id }, headers: t.WRITE });
        if (!moved.ok()) return t.fail(`${tag} G setup`, `the feed was not moved into ${name} (${moved.status()})`);
      }
      for (const k of EXTRA) {
        const r = await t.addLocalFeed(page, `${base}/${k}.xml`, `UAT feed digest ${k}`, (items) => items.length >= 1);
        if (r.error) return t.fail(`${tag} G setup`, r.error);
        const name = `UAT Digest ${k.toUpperCase()}`;
        const made = await page.request.post("/api/folders", { data: { name }, headers: t.WRITE });
        const folder = made.ok() ? (await t.boot(page)).folders.find((x) => x.name === name) : null;
        const moved = folder ? await page.request.patch(`/api/feeds/${r.feedId}`, { data: { folder_id: folder.id }, headers: t.WRITE }) : null;
        if (!moved?.ok()) return t.fail(`${tag} G setup`, `the folder ${name} was not made with its feed in it`);
      }
      // G9 needs sections past the first six; with six or fewer it would pass without testing anything.
      const withUnread = (await t.boot(page)).folders.filter((f) => f.unread > 0).length;
      if (withUnread <= 6) return t.fail(`${tag} G setup`, `only ${withUnread} sections have unread articles; G9 needs more than six`);
      const set = await patch({ "client.layout": "digest", "client.digest_sections": "folder", "client.digest_order": "sidebar", "client.digest_images": true, "client.digest_rows": 3 });
      if (set !== 200) return t.fail(`${tag} G setup`, `the Digest was not set as the layout (${set})`);
      try {
        await open();

        // G1
        const order = await mine();
        check(order.join() === [A, B].join(), `${tag} G1 sections`, "each folder is a section, in sidebar order", `the sections are: ${(await headings()).join(", ")}`);
        const shownA = await idsOf(A);
        const more = section(A).getByRole("button", { name: `5 more in ${A}`, exact: true });
        check(shownA.length === 3 && (await more.count()) === 1, `${tag} G1 rows`, "three stories and \"5 more in UAT Digest A\"", `${shownA.length} stories, ${await more.count()} more button(s)`);
        await t.shot(page, `${tag}-digest`);

        // G2
        const pictures = () => section(A).locator("article[data-item-id] img").count();
        const withPictures = await pictures();
        check(withPictures >= 1, `${tag} G2 images`, "a story with a picture shows it", "no picture is shown with Images on");
        await layoutMenu();
        await page.getByRole("menuitemradio", { name: /Text only/ }).click();
        const gone = await settled(() => !document.querySelector('section[data-section] article[data-item-id] img'));
        check(gone, `${tag} G2 text only`, "Text only shows no picture", `${await pictures()} picture(s) still shown`);
        await t.shot(page, `${tag}-digest-text-only`);

        // G3
        await layoutMenu();
        await page.getByRole("menuitemradio", { name: /Quiet Feeds First/ }).click();
        await layoutMenu();
        const saved = await page.getByRole("menuitemradio", { name: /Quiet Feeds First/ }).getAttribute("aria-checked");
        await page.keyboard.press("Escape");
        await settled(() => !document.querySelector('[data-testid="digest-skeleton"]') && document.querySelectorAll("section[data-section]").length > 0);
        const again = await mine();
        check(saved === "true", `${tag} G3 saved`, "Quiet Feeds First is saved for the device", `the menu shows it as ${saved}`);
        check(again.join() === [A, B].join(), `${tag} G3 order`, "sections without a pace stay in sidebar order", `the sections are: ${again.join(", ")}`);
        await patch({ "client.digest_order": "sidebar" });
        await open();

        // G4
        const bBefore = await idsOf(B);
        await section(A).getByRole("button", { name: `5 more in ${A}`, exact: true }).click();
        const opened = await settled(() => document.querySelectorAll('section[data-section] article[data-item-id]').length >= 11);
        const allA = await idsOf(A);
        check(opened && allA.length === 8 && (await section(A).getByRole("button", { name: /more in/ }).count()) === 0, `${tag} G4 expand`, "the section opens in place to all eight stories and its button goes", `${allA.length} stories, ${await section(A).getByRole("button", { name: /more in/ }).count()} button(s)`);
        const bAfter = await idsOf(B);
        check(bBefore.length === 3 && JSON.stringify(bAfter) === JSON.stringify(bBefore), `${tag} G4 other`, "the other section keeps its three stories", `it had ${bBefore.join(" ")}, now ${bAfter.join(" ")}`);
        const orderNow = await mine();
        check(orderNow.join() === [A, B].join(), `${tag} G4 order`, "the sections keep their order", orderNow.join(", "));

        // G6: a fresh page; one Tab stop; the arrow keys follow the drawn order.
        await open();
        const stops = await page.locator('section[data-section] article[data-item-id] a:not([tabindex="-1"])').count();
        check(stops === 1, `${tag} G6 tab stop`, "the page has one Tab stop", `${stops} story links are in the Tab order`);
        const drawn = await page.locator("[data-digest] article[data-item-id], [data-digest] [data-stop-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId ?? e.dataset.stopId));
        const focusId = () => page.evaluate(() => document.activeElement?.closest("[data-item-id],[data-stop-id]")?.getAttribute("data-item-id") ?? document.activeElement?.closest("[data-stop-id]")?.getAttribute("data-stop-id") ?? null);
        await page.locator("[data-digest] article[data-item-id] a").first().focus();
        const visited = [await focusId()];
        for (let i = 1; i < Math.min(drawn.length, 6); i++) {
          await page.keyboard.press("ArrowDown");
          await page.waitForTimeout(80);
          visited.push(await focusId());
        }
        check(visited.join() === drawn.slice(0, visited.length).join(), `${tag} G6 arrows`, "ArrowDown visits stories and \"more\" buttons in the drawn order", `visited ${visited.join(" ")}, drawn ${drawn.slice(0, visited.length).join(" ")}`);

        // G6b: Enter on the button, which is reached by the arrow keys and is not a Tab stop.
        const shownBefore = await idsOf(A);
        await section(A).getByRole("button", { name: /^[0-9]+ more in UAT Digest A$/ }).focus();
        await page.keyboard.press("Enter");
        await settled(() => document.querySelectorAll("section[data-section] article[data-item-id]").length >= 8);
        const afterEnter = await idsOf(A);
        await settled(`document.activeElement?.closest("article[data-item-id]")?.getAttribute("data-item-id") === ${JSON.stringify(afterEnter[shownBefore.length] ?? "")}`, 3000);
        const focused = await page.evaluate(() => document.activeElement?.closest("article[data-item-id]")?.getAttribute("data-item-id") ?? null);
        check(afterEnter.length === 8 && focused === afterEnter[shownBefore.length], `${tag} G6b focus`, "Enter on \"more\" opens the section and focus goes to the first article it brought in", `${afterEnter.length} stories; focus is on ${focused}, wanted ${afterEnter[shownBefore.length]}`);

        // G7
        const wide = (await idsOf(A)).length;
        const target = (await idsOf(A))[wide - 1];
        await page.locator(`article[data-item-id="${target}"] a`).first().click();
        await page.waitForURL(`**/i/${target}?*`, { timeout: 10000 });
        await page.goBack();
        await page.locator("section[data-section] article[data-item-id]").first().waitFor({ timeout: 15000 });
        check((await idsOf(A)).length === wide, `${tag} G7 back`, "going Back keeps the section open", `${(await idsOf(A)).length} stories in the section, ${wide} before`);

        // G5 runs on a fresh page, with the first section opened.
        await open();
        await section(A).getByRole("button", { name: /^[0-9]+ more in UAT Digest A$/ }).click();
        await settled(() => document.querySelectorAll("section[data-section] article[data-item-id]").length >= 11);

        // G5: mark below on the third story of A: stories 4..8 read, 1..3 unread, B untouched.
        await openMenu(allA[2]);
        await page.getByRole("menuitem", { name: /^Mark below as read/ }).click();
        const readBelow = await page.waitForFunction(
          async (ids) => (await Promise.all(ids.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true))).every(Boolean),
          allA.slice(3),
          { timeout: 8000 },
        ).then(() => true, () => false);
        const upper = await readOf(allA.slice(0, 3));
        const bRead = await readOf(await idsOf(B));
        check(readBelow && upper.every((r) => !r) && bRead.every((r) => !r), `${tag} G5 below`, "Mark below marks the rest of its section and nothing else", `below read: ${readBelow}; above read: ${upper.join()}; other section read: ${bRead.join()}`);
        // Mark above on the last story of B: B's earlier stories read, A untouched by it.
        const idsB = await idsOf(B);
        if (idsB.length >= 2) {
          await openMenu(idsB[idsB.length - 1]);
          await page.getByRole("menuitem", { name: /^Mark above as read/ }).click();
          const readAbove = await page.waitForFunction(
            async (ids) => (await Promise.all(ids.map(async (i) => (await (await fetch(`/api/items/${i}`)).json()).read === true))).every(Boolean),
            idsB.slice(0, -1),
            { timeout: 8000 },
          ).then(() => true, () => false);
          const last = await isRead(idsB[idsB.length - 1]);
          const aUpper = await readOf(allA.slice(0, 3));
          check(readAbove && !last && aUpper.every((r) => !r), `${tag} G5 above`, "Mark above marks the earlier stories of its section and nothing else", `above read: ${readAbove}; the story itself read: ${last}; other section read: ${aUpper.join()}`);
        }

        // G9: the sections below the first six load when the end of the page is reached.
        // Tall sections (ten rows), so the first six fill more than the page and its look-ahead and the rest are left unasked.
        if ((await patch({ "client.digest_rows": 10 })) !== 200) return t.fail(`${tag} G9 setup`, "Rows per section was not set");
        // The page remembers where the reader was, and opening it again goes back there, loading what it needs to; this checks a first visit.
        await page.evaluate(() => localStorage.removeItem("kipple.listPlace.v1"));
        await open();
        const last = `UAT Digest ${EXTRA[EXTRA.length - 1].toUpperCase()}`;
        const scroller = page.getByTestId("list-scroll");
        const beforeScroll = await loaded();
        check(!beforeScroll.includes(last), `${tag} G9 not yet`, "the last section has no articles before the end of the page is reached", `the sections with articles at open are: ${beforeScroll.join(", ")}`);
        for (let i = 0; i < 6 && !(await loaded()).includes(last); i++) {
          const n = (await loaded()).length;
          await scroller.evaluate((e) => e.scrollTo(0, e.scrollHeight));
          await settled(`document.querySelectorAll("section[data-section]:has(article[data-item-id])").length > ${n}`, 3000);
        }
        await settled(() => !document.querySelector('[data-testid="digest-skeleton"]'), 15000);
        check((await loaded()).includes(last), `${tag} G9 later sections`, "a section past the sixth loads when the end of the page is reached", `the sections with articles are: ${(await loaded()).join(", ")}`);
        await t.shot(page, `${tag}-digest-later-sections`);
        await patch({ "client.digest_rows": 3 });

        // G10: mark as read while scrolling marks what was scrolled past, in the Digest too. What it marked is put back.
        const wasRead = new Set();
        try {
          const on = await patch({ "ui.mark_read_on_scroll": true });
          if (on !== 200) return t.fail(`${tag} G10 setup`, `the setting was not set (${on})`);
          await page.evaluate(() => localStorage.removeItem("kipple.listPlace.v1")); // the G9 scroll is remembered; start at the top
          await open();
          const shown = await page.locator("section[data-section] article[data-item-id]").evaluateAll((els) => els.map((e) => e.dataset.itemId));
          // The page notes where it stands before the reader scrolls: wait until its height has stopped changing (a scroll
          // made while sections are still arriving would be the page's first look and count nothing as passed).
          await page.waitForFunction(
            () => {
              const h = document.querySelector('[data-testid="list-scroll"]')?.scrollHeight;
              const still = window.__uatHeight === h;
              window.__uatHeight = h;
              return still;
            },
            null,
            { timeout: 10000, polling: 300 },
          );
          await scroller.evaluate((e) => e.scrollTo(0, 700));
          // Wait for the server to show the first article read (the page marks what was passed once scrolling settles).
          for (let i = 0; i < 40 && !(await isRead(shown[0])); i++) await page.waitForTimeout(250);
          // Then for the marking to finish: the count of read articles stops changing.
          let seen = -1;
          for (let i = 0; i < 20; i++) {
            const n = (await readOf(shown)).filter(Boolean).length;
            if (n === seen) break;
            seen = n;
            await page.waitForTimeout(250);
          }          const flags = await readOf(shown);
          const marked = shown.filter((_, i) => flags[i]);
          marked.forEach((id) => wasRead.add(id));
          const firstMarked = shown.indexOf(marked[0]);
          check(marked.length > 0 && shown[0] === marked[0] && !flags[shown.length - 1], `${tag} G10 scroll`, "the articles scrolled past are marked read and the ones not reached are not", `${marked.length} of ${shown.length} marked (first marked at ${firstMarked}); the last is ${flags[shown.length - 1] ? "read" : "unread"}`);
        } finally {
          await patch({ "ui.mark_read_on_scroll": null });
          if (wasRead.size) await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [...wasRead], read: false } }).catch(() => {});
        }

        const bad = requests.filter((p) => /export|download|\.opml/i.test(p));
        check(bad.length === 0, `${tag} G8 no export`, "no export or download address was asked for", `asked for ${bad.join(", ")}`);
      } finally {
        await patch({ "client.layout": null, "client.digest_sections": null, "client.digest_order": null, "client.digest_images": null, "client.digest_rows": null });
      }
    });
  });
} finally {
  server.close();
}

await t.finish("Digest layout");
