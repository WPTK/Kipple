// UAT: leaving an article by a full-page navigation still records the active reading time.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:stats                  in another, once the feeds have fetched
//   npm run uat:stats -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and, on one unread article:
//   S1  opens it, stays about 12.5 s, then reloads the page: the server's active seconds grow by 11 to 13 (the
//       pagehide beacon carries the open interval; no visibilitychange comes before it on a same-tab navigation);
//   S2  the same, leaving by typing another address (a full-page load of the Unread list).
// The beacon is sent as the page unloads, so the browser's own request log does not list it: the check asks the
// server (GET /api/stats/summary) before and after. Afterwards it marks the article unread again. It only runs against
// a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0 clean, 1 findings,
// 2 setup error. First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;
const JSON_ACCEPT = { Accept: "application/json" };

const activeSeconds = async (page) => (await (await page.request.get("/api/stats/summary", { headers: JSON_ACCEPT })).json()).totals.active_seconds;
const setRead = (page, id, read) => page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [id], read } });

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const list = await (await page.request.get("/api/items", { params: { view: "unread", limit: 5 }, headers: JSON_ACCEPT })).json();
  const picks = (list.items ?? []).slice(0, 2);
  if (picks.length < 2) return t.setupError(`${tag}: the seed has fewer than two unread articles (have the feeds fetched?)`);
  const enabled = (await t.boot(page)).settings?.["stats.enabled"] !== false;
  if (!enabled) return t.setupError(`${tag}: statistics are off on this instance`);

  /** Reads the server's active seconds until they have not changed for `quietMs` (late beacons have landed), up to `maxMs`. */
  const settled = async (quietMs, maxMs) => {
    const end = Date.now() + maxMs;
    let value = await activeSeconds(page);
    let since = Date.now();
    while (Date.now() - since < quietMs && Date.now() < end) {
      await page.waitForTimeout(250);
      const now = await activeSeconds(page);
      if (now !== value) {
        value = now;
        since = Date.now();
      }
    }
    return value;
  };

  /**
   * Opens the article, reads for about 12.5 s, leaves with `leave`, and returns how many seconds the server gained.
   * It starts from a neutral page (the list, not a reading session) and waits for the totals to stop moving, so a
   * beacon still on its way from the previous step (the page before this one was a reading session) cannot land
   * between "before" and the read; and it ends when the totals stop moving, not at the first gain.
   */
  const readThenLeave = async (it, leave) => {
    await page.goto("/l/unread", { waitUntil: "load" });
    const before = await settled(3000, 15000);
    await page.goto(`/i/${it.id}`, { waitUntil: "load" });
    await page.getByTestId("article-body").waitFor({ timeout: 15000 });
    // The service worker controls the page, as it does for a reader: the beacon has to get past it.
    check(await page.evaluate(() => !!navigator.serviceWorker?.controller), `${tag} worker`, "the service worker controls the page", "no service worker controls the page, so the check below would not cover it");
    await page.waitForTimeout(12500); // real time: the flow reads active reading seconds, which only pass on the clock
    await leave();
    // The beacon is delivered by the browser after the unload (0.5 to 2 s): wait until the totals have been still for 3 s.
    return (await settled(3000, 15000)) - before;
  };
  const inRange = (n) => n >= 11 && n <= 13;

  try {
    await step(`${tag} S1`, async () => {
      const gained = await readThenLeave(picks[0], () => page.reload({ waitUntil: "load" }));
      check(inRange(gained), `${tag} S1 reload`, `the server gained ${gained} s`, `expected 11 to 13 s of active time after a reload, the server gained ${gained}`);
    });
    await step(`${tag} S2`, async () => {
      const gained = await readThenLeave(picks[1], () => page.goto("/l/unread", { waitUntil: "load" }));
      check(inRange(gained), `${tag} S2 typed address`, `the server gained ${gained} s`, `expected 11 to 13 s of active time after typing another address, the server gained ${gained}`);
      await t.shot(page, `${tag}-S2`);
    });
  } finally {
    for (const it of picks) await setRead(page, it.id, false);
  }
});

await t.finish("full-page navigation records reading time");
