// UAT: a feed Kipple cannot fetch says so, and why, where you meet it.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:feed-failure           in another
//   npm run uat:feed-failure -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves feeds on loopback ports, adds them, then makes them answer HTTP 500 and 410 and refreshes. For each
// width (desktop 1280x800, phone 375x812) it checks:
//   X1  the feed's row on Manage feeds shows its status and the reason the last fetch failed;
//   X2  the feed's own list says its last fetch failed, with the reason, instead of "All caught up";
//   X3  the list's "Open Feed health" link opens Feed health;
//   X4  a second failing feed that still has an article shows a note at the top of its list, and its folder's list
//       shows one too (issue #511): the feed's own reason, a count of at least two with the feed named, one clamped
//       line, not a live region, scrolling away with the list (the list keeps its height), and a link to Feed health.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 13);
const { check, step } = t;

const TITLE = "UAT feed failing";
const TITLE2 = "UAT feed failing with articles";

// Each feed is its own small server on 127.0.0.1 (its own port, so Kipple's slow-down of a host that keeps failing
// treats them apart) and answers only /feed.xml. Adding a feed checks that it answers, so it works until `broken` is
// set; then it answers `status`.
const servers = [];
async function serve(status, withItem) {
  const feed = { broken: false, url: "" };
  const item = withItem ? `<item><title>An older article</title><link>http://127.0.0.1/a</link><guid>uat-failing-1</guid><description>old</description></item>` : "";
  const server = createServer((req, res) => {
    if (req.url !== "/feed.xml") return res.writeHead(404).end();
    if (feed.broken) return res.writeHead(status, { "Content-Type": "text/plain" }).end("broken on purpose");
    res.writeHead(200, { "Content-Type": "application/rss+xml" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT failing</title><link>http://127.0.0.1/</link><description>t</description>${item}</channel></rss>`);
  });
  await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
  servers.push(server);
  feed.url = `http://127.0.0.1:${server.address().port}/feed.xml`;
  return feed;
}

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  // A different status per feed, so each feed's note has to name its own reason.
  const first = await serve(500, false);
  const second = await serve(410, true);
  let feed = null;
  await step(`${tag} X setup`, async () => {
    const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url: first.url, title: TITLE, allow_private_net: true } });
    if (!add.ok()) return t.fail(`${tag} X setup`, `the feed was not added: ${add.status()} ${await add.text()}`);
    first.broken = true;
    const rf = await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
    const s = await t.settle(page, (b) => b.feeds.some((f) => f.title === TITLE && f.last_error), 30000);
    feed = s.b.feeds.find((f) => f.title === TITLE && f.last_error) ?? null;
    if (!feed) t.fail(`${tag} X setup`, `the feed never recorded a failed fetch (refresh answered ${rf.status()} ${(await rf.text()).slice(0, 120)}; feed: ${JSON.stringify(s.b.feeds.filter((f) => f.title === TITLE).map((f) => [f.status, f.last_error]))})`);
  });
  if (!feed) return;

  await step(`${tag} X1`, async () => {
    await page.goto("/feeds");
    const row = page.getByRole("main").getByRole("link", { name: new RegExp(`^${TITLE}`) }).first();
    await row.waitFor({ timeout: 10000 });
    const text = await t.textOf(row);
    check(text.includes("Having trouble") && text.includes(feed.last_error), `${tag} X1`, `the row says "${text}"`, `the row says "${text}", without the status and the reason "${feed.last_error}"`);
    await t.shot(page, `${tag}-feed-failure-row`);
  });

  await step(`${tag} X2`, async () => {
    await page.goto(`/l?feed=${feed.id}`);
    const box = page.getByRole("status").filter({ hasText: "The last fetch of this feed failed" }).first();
    const shown = await box.waitFor({ timeout: 10000 }).then(() => true, () => false);
    const text = shown ? await t.textOf(box, 400) : await t.textOf(page.getByRole("main").first(), 400);
    check(shown && text.includes(feed.last_error), `${tag} X2`, `the list says "${text}"`, `the list says "${text}", not that the feed can't be fetched and why`);
    const caughtUp = await page.getByText("All caught up").count();
    check(caughtUp === 0, `${tag} X2`, `no "All caught up"`, `"All caught up" is still shown`);
    await t.shot(page, `${tag}-feed-failure-list`);
  });

  await step(`${tag} X3`, async () => {
    await page.getByRole("link", { name: "Open Feed health" }).click();
    const h = page.getByRole("heading", { name: "Feed health", level: 1 });
    const there = await h.waitFor({ timeout: 10000 }).then(() => true, () => false);
    check(there && /\/health(\?|#|$)/.test(page.url()), `${tag} X3`, "the link opens Feed health", `the link went to ${page.url()}`);
  });

  await step(`${tag} X4`, async () => {
    const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url: second.url, title: TITLE2, allow_private_net: true } });
    if (!add.ok()) return t.fail(`${tag} X4`, `the feed was not added: ${add.status()} ${await add.text()}`);
    second.broken = true;
    // A refresh that lands while the add's own first fetch or an earlier run is still going can miss the broken feed: ask again.
    let f2 = null;
    for (let tries = 0; tries < 3 && !f2; tries++) {
      await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
      const s = await t.settle(page, (b) => b.feeds.some((f) => f.title === TITLE2 && f.last_error), 12000);
      f2 = s.b.feeds.find((f) => f.title === TITLE2 && f.last_error) ?? null;
    }
    if (!f2) return t.fail(`${tag} X4`, "the second feed never recorded a failed fetch");
    for (const [what, path] of [["feed", `/l/all?feed=${f2.id}`], ["folder", `/l/all?folder=${f2.folder_id}`]]) {
      await page.goto(path);
      const note = page.getByTestId("feed-failure-note");
      const shown = await note.waitFor({ timeout: 10000 }).then(() => true, () => false);
      const text = shown ? await t.textOf(note, 400) : "";
      // The feed's own reason (its 410, not the other feed's 500); a folder says its count once, at least two, naming a failing feed.
      const reasons = what === "feed" ? text.includes("This feed is gone: 410 Gone") && !text.includes("HTTP 500") : /\b([2-9]|\d\d+) feeds in this folder have fetch problems: .*UAT feed failing/.test(text) && !/\(and \d+ more\)/.test(text);
      check(shown && reasons && text.includes("Open Feed health"), `${tag} X4`, `the ${what} list's note says "${text}"`, `the ${what} list's note says "${text}", not its own reason (feed) or the count once with a failing feed named (folder), with the Feed health link`);
      check(shown && (await note.evaluate((el) => !el.closest("[role=status],[aria-live]"))), `${tag} X4`, `the ${what} note is not a live region`, `the ${what} note is a live region`);
      const line = shown ? await note.locator("p").evaluate((el) => el.scrollHeight <= el.clientHeight + 1 || globalThis.getComputedStyle(el).webkitLineClamp === "1") : false;
      check(line, `${tag} X4`, `the ${what} note is one clamped line`, `the ${what} note is more than one line`);
      const row = await page.getByText("An older article").first().waitFor({ timeout: 10000 }).then(() => true, () => false);
      check(row, `${tag} X4`, `the ${what} list still shows its article`, `the ${what} list lost its article`);
      // The note is part of the scrolling content: the list keeps its whole height, and the note leaves when the list scrolls.
      const scroll = page.getByTestId("list-scroll");
      const box = await scroll.boundingBox();
      check(!!box && box.height >= vp.height * 0.6, `${tag} X4`, `the ${what} list is ${Math.round(box?.height ?? 0)}px of ${vp.height}px`, `the ${what} list is only ${Math.round(box?.height ?? 0)}px of ${vp.height}px high`);
      await t.shot(page, `${tag}-feed-failure-note-${what}`);
      const before = shown ? (await note.boundingBox())?.y : null;
      await scroll.evaluate((el) => {
        const pad = globalThis.document.createElement("div");
        pad.style.height = "4000px";
        pad.dataset.uatPad = "";
        el.firstElementChild?.appendChild(pad);
        el.scrollTop = 300;
      });
      const after = shown ? (await note.boundingBox())?.y : null;
      await scroll.evaluate((el) => el.querySelector("[data-uat-pad]")?.remove());
      check(before != null && after != null && after < before, `${tag} X4`, `the ${what} note scrolls away with the list`, `the ${what} note stayed in place when the list scrolled (${before} -> ${after})`);
    }
    await page.goto(`/l/all?feed=${f2.id}`);
    await page.getByTestId("feed-failure-note").getByRole("link", { name: "Open Feed health" }).click();
    const there = await page.getByRole("heading", { name: "Feed health", level: 1 }).waitFor({ timeout: 10000 }).then(() => true, () => false);
    check(there, `${tag} X4`, "the note's link opens Feed health", `the note's link went to ${page.url()}`);
  });
});

for (const s of servers) s.close();
await t.finish("feed failure");
