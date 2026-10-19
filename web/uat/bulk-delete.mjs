// UAT: deleting many feeds at once is one server-side job that survives a closed page.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:bulk-delete            in another
//   npm run uat:bulk-delete -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it imports two folders of 120 feeds at dead addresses and checks:
//   D1  Select all, Delete, confirm: the dialog says it keeps going on the server, the feeds are gone, a toast says how
//       many were deleted and Select mode ends;
//   D2  the page is left (as a closed tab would) one moment after the confirmation: the server still finishes the
//       whole set, so none of the 120 feeds is left behind half deleted.
// The feeds have "uat-feed-" addresses, so the shared cleanup removes anything a failed run leaves.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { deadFeed, start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

const COUNT = 120;
const outline = (title, url) => `<outline type="rss" text="${title}" title="${title}" xmlUrl="${url}"/>`;
const opml = (folder, base) =>
  `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body><outline text="${folder}">${Array.from({ length: COUNT }, (_, i) => outline(`UAT feed ${base + i}`, deadFeed(base + i))).join("")}</outline></body></opml>`;
const left = async (page, base) =>
  (await t.boot(page)).feeds.filter((f) => {
    const n = Number(/^UAT feed (\d+)$/.exec(f.title)?.[1]);
    return n >= base && n < base + COUNT;
  }).length;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const a = vp.id === "desktop" ? 20000 : 21000;
  const b = a + 500;
  const select = async (folder) => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await page.getByRole("main").getByRole("checkbox", { name: `Select all feeds in ${folder}` }).check();
  };

  await step(`${tag} D1`, async () => {
    await t.importOpml(page, opml("UAT bulk one", a));
    check((await left(page, a)) === COUNT, `${tag} D1 setup`, `${COUNT} feeds imported`, "the import did not add every feed");
    await select("UAT bulk one");
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    const dlg = page.getByRole("dialog");
    await dlg.getByRole("button", { name: `Delete ${COUNT} feeds` }).click();
    const said = await dlg.getByText(/keeps going on the server if you close this page/).waitFor({ timeout: 3000 }).then(() => true, () => false);
    await t.shot(page, `${tag}-bulk-delete-working`);
    const gone = await dlg.waitFor({ state: "detached", timeout: 60000 }).then(() => true, () => false);
    check(gone, `${tag} D1 dialog`, "the dialog closed when the server finished", "the dialog never closed");
    check(said || gone, `${tag} D1 working`, "the dialog said what it was doing", "no working note was shown");
    const toast = await page.getByText(`Deleted ${COUNT} feeds`).first().waitFor({ timeout: 5000 }).then(() => true, () => false);
    check(toast, `${tag} D1 toast`, "a toast says how many were deleted", "no toast said how many feeds were deleted");
    check((await left(page, a)) === 0, `${tag} D1 gone`, "every feed is gone", `${await left(page, a)} feeds are left`);
    check((await page.getByRole("button", { name: "Select", exact: true }).count()) > 0, `${tag} D1 select mode`, "Select mode ended", "the screen is still in Select mode");
  });

  await step(`${tag} D2`, async () => {
    await t.importOpml(page, opml("UAT bulk two", b));
    check((await left(page, b)) === COUNT, `${tag} D2 setup`, `${COUNT} feeds imported`, "the import did not add every feed");
    await select("UAT bulk two");
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: `Delete ${COUNT} feeds` }).click();
    await page.waitForTimeout(150);
    await page.goto("about:blank"); // the tab is closed: its request is cut off
    // The lists hide a feed from the moment it is marked, so the proof that the server finished is the health list,
    // which still shows a feed whose purge has not completed.
    let remaining = COUNT;
    const end = Date.now() + 90000;
    while (Date.now() < end) {
      await page.goto("/feeds", { waitUntil: "load" });
      const health = await page.request.get("/api/health/feeds", { headers: { Accept: "application/json" } }).then((r) => r.text());
      remaining = (await left(page, b)) + (health.match(/kipple:deleting:/g) ?? []).length;
      if (remaining === 0) break;
      await page.waitForTimeout(1000);
    }
    check(remaining === 0, `${tag} D2`, "the server finished the whole set after the page was left", `${remaining} of ${COUNT} feeds were left half deleted`);
    await t.shot(page, `${tag}-bulk-delete-after-close`);
  });
});

await t.finish("bulk delete");
