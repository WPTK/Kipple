// UAT: a feed Kipple cannot fetch says so, and why, where you meet it.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:feed-failure           in another
//   npm run uat:feed-failure -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves a feed on a loopback port, adds it, then makes it answer HTTP 500 and refreshes. For each width
// (desktop 1280x800, phone 375x812) it checks:
//   X1  the feed's row on Manage feeds shows its status and the reason the last fetch failed;
//   X2  the feed's own list says its last fetch failed, with the reason, instead of "All caught up";
//   X3  the list's "Open Feed health" link opens Feed health.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 13);
const { check, step } = t;

const TITLE = "UAT feed failing";
// Adding a feed checks that it answers, so it works until the flow breaks it.
let broken = false;
const server = createServer((req, res) => {
  if (broken) return res.writeHead(500, { "Content-Type": "text/plain" }).end("broken on purpose");
  res.writeHead(200, { "Content-Type": "application/rss+xml" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT failing</title><link>http://127.0.0.1/</link><description>t</description></channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
const url = `http://127.0.0.1:${server.address().port}/feed.xml`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  let feed = null;
  await step(`${tag} X setup`, async () => {
    broken = false;
    const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url, title: TITLE, allow_private_net: true } });
    if (!add.ok()) return t.fail(`${tag} X setup`, `the feed was not added: ${add.status()} ${await add.text()}`);
    broken = true;
    await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
    const s = await t.settle(page, (b) => b.feeds.some((f) => f.title === TITLE && f.last_error), 30000);
    feed = s.b.feeds.find((f) => f.title === TITLE && f.last_error) ?? null;
    if (!feed) t.fail(`${tag} X setup`, "the feed never recorded a failed fetch");
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
});

server.close();
await t.finish("feed failure");
