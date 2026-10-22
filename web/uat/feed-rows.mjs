// UAT: the rows of the Feeds screen in a real browser: counts, Favorites, the unread-only view.
//
//   npm run seed                       in one terminal
//   npm run uat:feed-rows              in another, once the feeds have fetched
//   npm run uat:feed-rows -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and, on the Feeds screen:
//   R1  a feed with unread articles shows its count on its row, and a feed with none shows no count;
//   R2  starring that feed puts it in Favorites with the same count, and nothing in Favorites says "Feed" or "Folder";
//       the star is taken off again afterwards;
//   R3  "Show only unread feeds" (Feed actions menu) leaves out the feeds with nothing unread and says so, and "Show all"
//       in that note brings them back; nothing scrolls sideways at either state;
//   R4  a plain visit shows no help sentence under the title.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check, step } = t;

const escape = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await page.goto("/feeds", { waitUntil: "load" });
  await page.getByRole("heading", { level: 1, name: "Feeds" }).waitFor({ timeout: 15000 });
  const main = page.getByRole("main");
  const b = await t.boot(page);
  const feeds = b.feeds.filter((f) => !f.is_archive);
  const withNews = feeds.find((f) => f.unread > 0);
  // The seed leaves every feed with unread articles: one is read for this run (and put back after), so R1 and R3 have a read feed.
  const spare = feeds.filter((f) => f.unread > 0 && f !== withNews).sort((x, y) => x.unread - y.unread)[0];
  let read = feeds.find((f) => f.unread === 0);
  let restore = [];
  if (!read && spare) {
    const res = await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { scope: { feed_id: spare.id, view: "unread" }, read: true, reason: "bulk" } });
    restore = (await res.json()).changed ?? [];
    await page.reload({ waitUntil: "load" });
    read = { ...spare, unread: 0 };
  }
  const row = (f) => main.getByRole("link", { name: new RegExp(`^${escape(f.title)}`) }).first();
  const sideways = () => page.evaluate(() => globalThis.document.documentElement.scrollWidth > globalThis.document.documentElement.clientWidth);

  await step(`${tag} R1`, async () => {
    if (!withNews) return t.setupError(`${tag}: the seed has no feed with unread articles`);
    const text = (await row(withNews).innerText()).replace(/\s+/g, " ");
    const shown = withNews.unread > 99 ? "99+" : String(withNews.unread);
    check(text.includes(shown), `${tag} R1 count`, `the row of ${withNews.title} shows ${shown}`, `the row reads "${text}"`);
    if (read) {
      const quiet = (await row(read).innerText()).replace(/\s+/g, " ");
      check(!/\d/.test(quiet.replace(read.title, "")), `${tag} R1 none`, `the row of ${read.title} shows no count`, `the row reads "${quiet}"`);
    }
  });

  await step(`${tag} R2`, async () => {
    if (!withNews) return;
    const star = main.getByRole("button", { name: `Add ${withNews.title} to favorites`, exact: true }).first();
    await star.click();
    const fav = main.getByRole("region", { name: "Favorites" });
    await fav.waitFor({ timeout: 5000 });
    await t.shot(page, `${tag}-feed-rows-favorite`);
    const text = (await fav.innerText()).replace(/\s+/g, " ");
    const shown = withNews.unread > 99 ? "99+" : String(withNews.unread);
    check(text.includes(withNews.title) && text.includes(shown), `${tag} R2 count`, "the favorite shows its count", `Favorites reads "${text}"`);
    check(!/\b(Feed|Folder)\b/.test(text.replace(/^Favorites/, "")), `${tag} R2 label`, "no Feed or Folder label in Favorites", `Favorites reads "${text}"`);
    await main.getByRole("button", { name: `Remove ${withNews.title} from favorites`, exact: true }).first().click();
    await fav.waitFor({ state: "detached", timeout: 5000 }).catch(() => {});
  });

  await step(`${tag} R3`, async () => {
    if (!read) return;
    check(!(await sideways()), `${tag} R3 scroll all`, "nothing scrolls sideways", "the page scrolls sideways");
    await page.getByRole("button", { name: "Feed actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Show only unread feeds", exact: true }).click();
    await page.getByText("Showing only feeds with unread articles.").waitFor({ timeout: 5000 });
    await t.shot(page, `${tag}-feed-rows-unread-only`);
    const gone = (await main.getByText(read.title, { exact: true }).count()) === 0;
    check(gone, `${tag} R3 hides`, `${read.title} is left out`, `${read.title} is still listed`);
    check(!(await sideways()), `${tag} R3 scroll unread`, "nothing scrolls sideways", "the page scrolls sideways");
    await page.getByRole("button", { name: "Show all", exact: true }).click();
    await main.getByText(read.title, { exact: true }).first().waitFor({ timeout: 5000 });
    check(true, `${tag} R3 back`, "Show all brings the feed back", "");
  });

  if (restore.length) {
    await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: restore, read: false, undo: true, reason: "bulk" } });
  }

  await step(`${tag} R4`, async () => {
    const help = await page.getByText(/arrow beside a folder/).count();
    check(help === 0, `${tag} R4 help`, "no help sentence on a plain visit", "the old help sentence is back");
  });
});

await t.finish("Feed rows");
