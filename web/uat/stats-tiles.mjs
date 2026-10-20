// UAT: the Stats summary tiles and the feed's Read rate look tappable, and a tap opens what they open (issue #562).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:stats-tiles            in another, once the feeds have fetched
//   npm run uat:stats-tiles -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   T1  the three summary tiles each carry a chevron, drawn without any hover, with a 3:1 contrast against the tile in
//       all 20 color schemes, and each tile is at least 44 px tall and wide;
//   T2  tapping a tile opens its previous-period note (aria-expanded true, the chevron turns over) and a second tap
//       closes it;
//   T3  the same for the Read rate in a feed's detail (one article is opened and read for 12 s through the API so the
//       feed has a row under Sources), and the button is at least 44 px tall.
// Afterwards it marks the article unread again. It only runs against a loopback address with the seed's credentials
// unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { readFileSync } from "node:fs";
import { start } from "./common.mjs";

const SCHEMES = JSON.parse(readFileSync(new URL("../src/theme/schemes.json", import.meta.url), "utf8"));

const t = await start(import.meta.url, 16);
const { check, step } = t;
const JSON_ACCEPT = { Accept: "application/json" };

/** The tile's chevron colour against the tile's own background, in the scheme the page shows: a contrast ratio. */
function chevronContrast(btn) {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 1;
  const c = canvas.getContext("2d", { willReadFrequently: true });
  const rgba = (css) => {
    c.clearRect(0, 0, 1, 1);
    c.fillStyle = css;
    c.fillRect(0, 0, 1, 1);
    return [...c.getImageData(0, 0, 1, 1).data];
  };
  const chan = (v) => ((v / 255) <= 0.03928 ? v / 255 / 12.92 : (((v / 255) + 0.055) / 1.055) ** 2.4);
  const lum = ([r, g, b]) => 0.2126 * chan(r) + 0.7152 * chan(g) + 0.0722 * chan(b);
  const ink = rgba(getComputedStyle(btn.querySelector("svg[data-cue]")).color);
  let el = btn;
  let bg = [0, 0, 0, 0];
  while (el && bg[3] === 0) {
    bg = rgba(getComputedStyle(el).backgroundColor);
    el = el.parentElement;
  }
  const [hi, lo] = [lum(ink), lum(bg)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const list = await (await page.request.get("/api/items", { params: { view: "unread", limit: 5 }, headers: JSON_ACCEPT })).json();
  const pick = (list.items ?? [])[0];
  if (!pick) return t.setupError(`${tag}: the seed has no unread article (have the feeds fetched?)`);
  if ((await t.boot(page)).settings?.["stats.enabled"] === false) return t.setupError(`${tag}: statistics are off on this instance`);
  const tiles = () => page.locator("button[aria-expanded]", { has: page.locator("svg[data-cue]") });

  try {
    // One read, through the API: an open (which gives the session key) and 12 s of active time.
    const opened = await page.request.post(`/api/items/${pick.id}/open`, { headers: t.WRITE, data: { via: "tap" } });
    const key = (await opened.json()).session_key;
    await page.request.post("/api/stats/events", {
      headers: t.WRITE,
      data: { client: "web", events: [{ kind: "read_time", item_id: pick.id, session_key: key, value: 12, event_id: `uat-tiles-${Date.now()}` }] },
    });
    await page.goto("/stats", { waitUntil: "load" });

    await step(`${tag} T1`, async () => {
      await tiles().first().waitFor({ timeout: 10000 });
      check((await tiles().count()) === 3, `${tag} T1 count`, "three tiles carry a chevron", `${await tiles().count()} tiles carry a chevron, expected 3`);
      const ids = SCHEMES.map((x) => x.id);
      check(ids.length === 20, `${tag} T1 schemes`, "20 color schemes (schemes.json)", `schemes.json has ${ids.length} color schemes, expected 20`);
      const was = await page.evaluate(() => document.documentElement.dataset.theme);
      const weak = [];
      for (const id of ids) {
        await page.evaluate((s) => document.documentElement.setAttribute("data-theme", s), id);
        for (let i = 0; i < 3; i++) {
          const btn = tiles().nth(i);
          const ratio = await btn.evaluate(chevronContrast);
          const box = await btn.boundingBox();
          const cue = await btn.locator("svg[data-cue]").evaluate((s) => ({ op: getComputedStyle(s).opacity, vis: getComputedStyle(s).visibility, w: s.getBoundingClientRect().width }));
          if (ratio < 3 || box.height < 44 || box.width < 44 || cue.op !== "1" || cue.vis !== "visible" || cue.w < 12) {
            weak.push(`${id}/${i}: ratio ${ratio.toFixed(2)}, ${Math.round(box.width)}x${Math.round(box.height)}`);
          }
        }
      }
      await page.evaluate((s) => document.documentElement.setAttribute("data-theme", s), was);
      check(weak.length === 0, `${tag} T1 cue`, "every tile shows a visible chevron (3:1) and is at least 44 px, in every scheme", `weak tiles: ${weak.slice(0, 5).join("; ")}`);
      await t.shot(page, `${tag}-T1-tiles`);
    });

    await step(`${tag} T2`, async () => {
      const first = tiles().first();
      check((await first.getAttribute("aria-expanded")) === "false", `${tag} T2 closed`, "the tile starts closed", "the tile does not start closed");
      await first.click();
      check((await first.getAttribute("aria-expanded")) === "true", `${tag} T2 open`, "a tap opens the previous-period note", "a tap did not open the note");
      check((await page.getByRole("status").filter({ hasText: /compared with|Complete days only/ }).count()) > 0 || /history|Loading|Unavailable|%|same|more|less|up|down/i.test(await first.innerText()), `${tag} T2 note`, "the note shows", "no note shows");
      check(await first.locator("svg[data-cue]").evaluate((s) => s.classList.contains("rotate-180")), `${tag} T2 turned`, "the chevron turns over", "the chevron did not turn over");
      await page.waitForTimeout(400); // the chevron's turn is a short transition
      await t.shot(page, `${tag}-T2-open`);
      await first.click();
      check((await first.getAttribute("aria-expanded")) === "false", `${tag} T2 close`, "a second tap closes it", "a second tap did not close it");
    });

    await step(`${tag} T3`, async () => {
      const row = page.locator('button[aria-haspopup="dialog"]').first();
      await row.waitFor({ timeout: 10000 });
      await row.click();
      const dlg = page.getByRole("dialog");
      const rate = dlg.getByRole("button", { name: /^Read rate/ });
      await rate.waitFor({ timeout: 10000 });
      const box = await rate.boundingBox();
      check(box.height >= 44, `${tag} T3 target`, `the Read rate button is ${Math.round(box.height)} px tall`, `the Read rate button is ${Math.round(box.height)} px tall, under 44`);
      check((await rate.locator("svg[data-cue]").count()) === 1, `${tag} T3 cue`, "the Read rate carries a chevron", "the Read rate has no chevron");
      check((await rate.getAttribute("aria-expanded")) === "false", `${tag} T3 closed`, "the counts start hidden", "the counts do not start hidden");
      await rate.click();
      check((await rate.getAttribute("aria-expanded")) === "true", `${tag} T3 open`, "a tap shows the counts behind the rate", "a tap did not show the counts");
      await page.waitForTimeout(400);
      await t.shot(page, `${tag}-T3-read-rate`);
      await page.keyboard.press("Escape");
    });
  } finally {
    await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [pick.id], read: false } });
  }
});

await t.finish("Stats tile affordance");
