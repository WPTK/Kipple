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
//   X3  the list's Edit address button opens Edit feed with the reason on top and the address field focused, and Try now
//       fetches the feed at once;
//   X4  a second failing feed that still has an article shows a note at the top of its list, and its folder's list
//       shows one too (issue #511): the feed's own reason, a count of at least two with the feed named, two clamped
//       lines, not a live region, scrolling away with the list (the list keeps its height), and a link to Feed health;
//   X5  a long list scrolled past the note comes back to the same article when the note is gone on return (the feed
//       recovered while the reader was on Feed health) and when it is the same.
// Each width adds its own two feeds and finds them by the id the add returned (never by title), then deletes them, so
// one width never sees the other's feeds.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 13);
const { check, step } = t;

const TITLE = "UAT feed failing";
const TITLE2 = "UAT feed failing with articles";
// What an HTTP 500 is called for the reader.
const REASON_500 = "The site had a problem answering";

// Each feed is its own small server on 127.0.0.1 (its own port, so Kipple's slow-down of a host that keeps failing
// treats them apart) and answers only /feed.xml. Adding a feed checks that it answers, so it works until `broken` is
// set; then it answers `status`.
const servers = [];
async function serve(status, articles) {
  const feed = { broken: false, url: "" };
  const item = Array.from({ length: articles }, (_, i) => `<item><title>${articles === 1 ? "An older article" : `Article ${i}`}</title><link>http://127.0.0.1/a${i}</link><guid>uat-failing-${i}</guid><description>${"words ".repeat(articles === 1 ? 1 : 40)}</description></item>`).join("");
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

/** Adds a feed the flow serves and returns its id, or an error string. */
async function addFeed(page, srv, title) {
  const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url: srv.url, title, allow_private_net: true } });
  if (!add.ok()) return { error: `the feed was not added: ${add.status()} ${await add.text()}` };
  const id = (await add.json()).feed?.id;
  return id ? { id } : { error: "the add did not answer with the feed's id" };
}

/** Asks for a refresh until the feed with `id` records a failed fetch (a refresh that lands during the add's own first fetch can miss it). */
async function failed(page, id, tries = 3) {
  let s = null;
  for (let i = 0; i < tries; i++) {
    await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
    s = await t.settle(page, (b) => b.feeds.some((f) => f.id === id && f.last_error), i === 0 ? 30000 : 12000);
    const feed = s.b.feeds.find((f) => f.id === id && f.last_error);
    if (feed) return { feed, b: s.b };
  }
  return { feed: null, b: s?.b };
}

/** How many feeds the bootstrap says are failing in a folder and the folders inside it: what a folder's note counts. */
function failingIn(b, folderId) {
  const inside = new Set([folderId]);
  for (let grew = true; grew; ) {
    grew = false;
    for (const f of b.folders) if (f.parent_id && inside.has(f.parent_id) && !inside.has(f.id)) grew = !!inside.add(f.id);
  }
  return b.feeds.filter((f) => inside.has(f.folder_id) && f.last_error).length;
}

await t.eachViewport(async (page, vp) => {
  const added = [];
  try {
    await viewport(page, vp, added);
  } finally {
    for (const id of added) await page.request.delete(`/api/feeds/${id}`, { headers: t.WRITE }).catch(() => {});
  }
});

async function viewport(page, vp, added) {
  const tag = vp.id;
  // A different status per feed, so each feed's note has to name its own reason.
  const first = await serve(500, 0);
  const second = await serve(410, 1);
  let feed = null;
  await step(`${tag} X setup`, async () => {
    const add = await addFeed(page, first, TITLE);
    if (add.error) return t.fail(`${tag} X setup`, add.error);
    added.push(add.id);
    first.broken = true;
    const r = await failed(page, add.id);
    feed = r.feed;
    if (!feed) t.fail(`${tag} X setup`, `the feed never recorded a failed fetch (feed: ${JSON.stringify((r.b?.feeds ?? []).filter((f) => f.id === add.id).map((f) => [f.status, f.last_error]))})`);
  });
  if (!feed) return;

  await step(`${tag} X1`, async () => {
    await page.goto("/feeds");
    const row = page.getByRole("main").getByRole("link", { name: new RegExp(`^${TITLE}`) }).first();
    await row.waitFor({ timeout: 10000 });
    const text = await t.textOf(row);
    check(text.includes("Having trouble") && text.includes(REASON_500) && !text.includes(feed.last_error), `${tag} X1`, `the row says "${text}"`, `the row says "${text}", without the status and the reason in words "${REASON_500}" (or with the server's own text "${feed.last_error}")`);
    await t.shot(page, `${tag}-feed-failure-row`);
  });

  await step(`${tag} X2`, async () => {
    await page.goto(`/l?feed=${feed.id}`);
    const box = page.getByRole("status").filter({ hasText: "Kipple can't fetch this feed" }).first();
    const shown = await box.waitFor({ timeout: 10000 }).then(() => true, () => false);
    const text = shown ? await t.textOf(box, 400) : await t.textOf(page.getByRole("main").first(), 400);
    check(shown && text.includes(REASON_500) && !text.includes(feed.last_error), `${tag} X2`, `the list says "${text}"`, `the list says "${text}", not that the feed can't be fetched and why in words`);
    const caughtUp = await page.getByText("All caught up").count();
    check(caughtUp === 0, `${tag} X2`, `no "All caught up"`, `"All caught up" is still shown`);
    await t.shot(page, `${tag}-feed-failure-list`);
  });

  await step(`${tag} X3`, async () => {
    const repair = page.getByTestId("feed-repair");
    check(await repair.getByRole("button", { name: /^Try .* now$/ }).isVisible(), `${tag} X3`, "the list has Try now", "the list has no Try now");
    await t.shot(page, `${tag}-feed-failure-repair`);
    await repair.getByRole("button", { name: /^Edit address/ }).click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    const reason = await dlg.getByTestId("feed-reason").waitFor({ timeout: 10000 }).then(() => true, () => false);
    check(reason && (await t.textOf(dlg.getByTestId("feed-reason"))).includes(REASON_500), `${tag} X3`, "Edit feed opens with the reason on top", "Edit feed shows no reason on top");
    const focused = await dlg.getByLabel("Feed address").evaluate((el) => el === el.ownerDocument.activeElement).catch(() => false);
    check(focused, `${tag} X3`, "the address field is focused", "the address field is not focused");
    await t.shot(page, `${tag}-feed-failure-edit`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "hidden", timeout: 10000 });
    const refresh = page.waitForResponse((r) => /\/api\/feeds\/[^/]+\/refresh/.test(r.url()) && r.request().method() === "POST", { timeout: 20000 });
    await repair.getByRole("button", { name: /^Try .* now$/ }).click();
    check(!!(await refresh.catch(() => null)), `${tag} X3`, "Try now fetches the feed at once", "Try now sent no fetch");
  });

  await step(`${tag} X4`, async () => {
    const add = await addFeed(page, second, TITLE2);
    if (add.error) return t.fail(`${tag} X4`, add.error);
    added.push(add.id);
    second.broken = true;
    const { feed: f2, b: boot2 } = await failed(page, add.id);
    if (!f2) return t.fail(`${tag} X4`, "the second feed never recorded a failed fetch");
    // What the folder's note must count: every failing feed in the folder as the server reports it now (this width's two, and any other).
    const expected = failingIn(boot2, f2.folder_id);
    for (const [what, path] of [["feed", `/l/all?feed=${f2.id}`], ["folder", `/l/all?folder=${f2.folder_id}`]]) {
      await page.goto(path);
      const note = page.getByTestId("feed-failure-note");
      const shown = await note.waitFor({ timeout: 10000 }).then(() => true, () => false);
      const text = shown ? await t.textOf(note, 400) : "";
      // The feed's own reason (its 410, not the other feed's 500); a folder says its count once (as many as are failing in it,
      // at least this width's two) and names up to three, the rest as " and N more." at the end.
      const folderOk = () => {
        const m = /^(\d+) feeds in this folder have fetch problems: (.*)$/.exec(text.replace(/\s*Feed health\s*$/, ""));
        if (!m || Number(m[1]) !== expected || expected < 2) return false;
        return expected > 3 ? m[2].endsWith(` and ${expected - 3} more.`) : m[2].endsWith(".") && !/ and \d+ more/.test(m[2]) && m[2].includes(TITLE2) && m[2].includes(TITLE);
      };
      const reasons = what === "feed" ? text.includes("This feed is gone: The site says this feed no longer exists.") && !text.includes(REASON_500) : folderOk();
      check(shown && reasons && text.includes("Feed health"), `${tag} X4`, `the ${what} list's note says "${text}"`, `the ${what} list's note says "${text}", not its own reason (feed) or the count once (${expected} expected) with the failing feeds named (folder), with the Feed health link`);
      check(shown && (await note.evaluate((el) => !el.closest("[role=status],[aria-live]"))), `${tag} X4`, `the ${what} note is not a live region`, `the ${what} note is a live region`);
      const line = shown ? await note.locator("p").first().evaluate((el) => el.scrollHeight <= el.clientHeight + 1 || globalThis.getComputedStyle(el).webkitLineClamp === "2") : false;
      check(line, `${tag} X4`, `the ${what} note is at most two lines`, `the ${what} note is more than two lines`);
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
    await page.getByTestId("feed-failure-note").getByRole("link", { name: "Feed health" }).click();
    const there = await page.getByRole("heading", { name: "Feed health", level: 1 }).waitFor({ timeout: 10000 }).then(() => true, () => false);
    check(there, `${tag} X4`, "the note's link opens Feed health", `the note's link went to ${page.url()}`);
  });

  await step(`${tag} X5`, async () => {
    const long = await serve(500, 40);
    const title = `UAT feed failing long ${tag}`;
    const add = await addFeed(page, long, title);
    if (add.error) return t.fail(`${tag} X5`, add.error);
    added.push(add.id);
    const scroll = page.getByTestId("list-scroll");
    // The article at the top of the list and how far into it the list is scrolled.
    const top = () =>
      scroll.evaluate((el) => {
        const edge = el.getBoundingClientRect().top + 1;
        let best = null;
        for (const row of el.querySelectorAll("[data-item-id]")) {
          const r = row.getBoundingClientRect();
          if (r.bottom > edge && (!best || r.top < best.y)) best = { id: row.dataset.itemId, y: Math.round(r.top - edge) };
        }
        return best;
      });
    for (const recovers of [true, false]) {
      const what = recovers ? "the note gone on return" : "the same note on return";
      long.broken = true;
      const failing = await failed(page, add.id);
      if (!failing.feed) return t.fail(`${tag} X5`, "the long feed never recorded a failed fetch");
      await page.goto(`/l/all?feed=${add.id}`);
      await page.getByTestId("feed-failure-note").waitFor({ timeout: 10000 });
      await scroll.evaluate((el) => {
        el.scrollTop = 900;
      });
      await page.waitForTimeout(600);
      const before = await top();
      // Away to Feed health inside the app (the list's memory lives there), and back.
      await page.evaluate(() => globalThis.document.querySelector("[data-testid=feed-failure-note] a")?.click());
      await page.getByRole("heading", { name: "Feed health", level: 1 }).waitFor({ timeout: 10000 });
      if (recovers) {
        long.broken = false;
        await page.getByRole("button", { name: `Actions for ${title}` }).click();
        await page.getByRole("menuitem", { name: /Refresh/ }).click();
        const ok = await t.settle(page, (b) => b.feeds.some((f) => f.id === add.id && !f.last_error), 30000);
        if (!ok.ok) return t.fail(`${tag} X5`, "the long feed never recovered");
        await page.waitForTimeout(1500);
      }
      await page.goBack();
      await scroll.waitFor({ timeout: 10000 });
      await page.waitForTimeout(1200);
      const after = await top();
      const noteBack = await page.getByTestId("feed-failure-note").count();
      check(noteBack === (recovers ? 0 : 1), `${tag} X5`, `${what}: the note is ${recovers ? "gone" : "there"}`, `${what}: the note count is ${noteBack}`);
      check(!!before && !!after && before.id === after.id && Math.abs(before.y - after.y) < 3, `${tag} X5`, `${what}: the same article is at the top`, `${what}: the top article was ${JSON.stringify(before)} and is ${JSON.stringify(after)}`);
    }
  });
}

for (const s of servers) s.close();
await t.finish("feed failure");
