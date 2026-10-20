// UAT: the Stats summary tiles and the feed's Read rate look tappable, and a tap opens what they open (issue #562).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:stats-tiles            in another, once the feeds have fetched
//   npm run uat:stats-tiles -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   T1  the three summary tiles each carry a chevron, drawn without any hover, with a 3:1 contrast against the tile
//       and against its hover/pressed tint in all 20 color schemes, and each tile is at least 44 px tall and wide;
//   T2  a tile takes the selection tint on hover (a mouse) and while pressed; tapping it opens its previous-period note (the
//       real note text, not a loading state; the tile grows; the chevron turns over) and a second tap closes it;
//   T3  the same for the Read rate in a feed's detail (one article is opened and read for 12 s through the API so the
//       feed has a row under Sources): its card is at least 44 px, takes the tint, and the chevron turns on a tap;
//   T4  Large targets (tiles at least 56 px) with the largest text size and a long value ("12h 40m") at this width:
//       nothing is clipped and the value does not run under the chevron;
//   T5  on the phone, a 667x375 landscape screen: tiles at least 44 px.
// Afterwards it marks the article unread again. It only runs against a loopback address with the seed's credentials
// unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { readFileSync } from "node:fs";
import { start } from "./common.mjs";

const SCHEMES = JSON.parse(readFileSync(new URL("../src/theme/schemes.json", import.meta.url), "utf8"));
const t = await start(import.meta.url, 19);
const { check, step } = t;
const JSON_ACCEPT = { Accept: "application/json" };
/** What changeLabel (statsFormat.ts) or the thin-history rule shows once the comparison has loaded. */
const NOTE = /^(Not enough history|(up|no change) from .+|[+-]\d+% from .+)$/;

/** The chevron's contrast against the tile's idle background, or with `tint` against the scheme's selection colour. */
function chevronContrast(btn, tint) {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 1;
  const c = canvas.getContext("2d", { willReadFrequently: true });
  const rgba = (css) => {
    c.clearRect(0, 0, 1, 1);
    c.fillStyle = css;
    c.fillRect(0, 0, 1, 1);
    return [...c.getImageData(0, 0, 1, 1).data];
  };
  const chan = (v) => (v / 255 <= 0.03928 ? v / 255 / 12.92 : ((v / 255 + 0.055) / 1.055) ** 2.4);
  const lum = ([r, g, b]) => 0.2126 * chan(r) + 0.7152 * chan(g) + 0.0722 * chan(b);
  const ink = rgba(getComputedStyle(btn.querySelector("svg[data-cue]")).color);
  let el = btn;
  let bg = [0, 0, 0, 0];
  while (el && bg[3] === 0) {
    bg = rgba(getComputedStyle(el).backgroundColor);
    el = el.parentElement;
  }
  // The hover and pressed tint is bg-selection, the scheme's --kp-selection.
  if (tint) bg = rgba(getComputedStyle(document.documentElement).getPropertyValue("--kp-selection"));
  const [hi, lo] = [lum(ink), lum(bg)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** Whether `el`'s own background, or the background of its nearest tinted ancestor, is the scheme's selection colour. */
function showsTint(el) {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 1;
  const c = canvas.getContext("2d", { willReadFrequently: true });
  const rgba = (css) => {
    c.clearRect(0, 0, 1, 1);
    c.fillStyle = css;
    c.fillRect(0, 0, 1, 1);
    return c.getImageData(0, 0, 1, 1).data.join();
  };
  const want = rgba(getComputedStyle(document.documentElement).getPropertyValue("--kp-selection"));
  return rgba(getComputedStyle(el).backgroundColor) === want;
}

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const list = await (await page.request.get("/api/items", { params: { view: "unread", limit: 5 }, headers: JSON_ACCEPT })).json();
  const pick = (list.items ?? [])[0];
  if (!pick) return t.setupError(`${tag}: the seed has no unread article (have the feeds fetched?)`);
  if ((await t.boot(page)).settings?.["stats.enabled"] === false) return t.setupError(`${tag}: statistics are off on this instance`);
  const tiles = () => page.locator("button[aria-expanded]", { has: page.locator("svg[data-cue]") });
  const away = () => page.mouse.move(2, 2);

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
          const idle = await btn.evaluate(chevronContrast, false);
          const tint = await btn.evaluate(chevronContrast, true);
          const box = await btn.boundingBox();
          const cue = await btn.locator("svg[data-cue]").evaluate((s) => ({ op: getComputedStyle(s).opacity, vis: getComputedStyle(s).visibility, w: s.getBoundingClientRect().width }));
          if (idle < 3 || tint < 3 || box.height < 44 || box.width < 44 || cue.op !== "1" || cue.vis !== "visible" || cue.w < 12) {
            weak.push(`${id}/${i}: ratio ${idle.toFixed(2)} idle, ${tint.toFixed(2)} tinted, ${Math.round(box.width)}x${Math.round(box.height)}`);
          }
        }
      }
      await page.evaluate((s) => document.documentElement.setAttribute("data-theme", s), was);
      check(weak.length === 0, `${tag} T1 cue`, "every tile shows a visible chevron (3:1, idle and tinted) and is at least 44 px, in every scheme", `weak tiles: ${weak.slice(0, 5).join("; ")}`);
      await t.shot(page, `${tag}-T1-tiles`);
    });

    await step(`${tag} T2`, async () => {
      const first = tiles().first();
      check((await first.getAttribute("aria-expanded")) === "false", `${tag} T2 closed`, "the tile starts closed", "the tile does not start closed");
      const idleHeight = (await first.boundingBox()).height;
      check(!(await first.evaluate(showsTint)), `${tag} T2 idle`, "an idle tile has no tint", "an idle tile already shows the selection tint");
      // Tailwind gates hover: to (hover: hover), so a touch screen has no hover tint and shows the pressed one only.
      if (!vp.mobile) {
        await first.hover();
        check(await first.evaluate(showsTint), `${tag} T2 hover`, "a hovered tile takes the selection tint", "a hovered tile does not take the selection tint");
      }
      await away();
      const box = await first.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.mouse.down();
      check(await first.evaluate(showsTint), `${tag} T2 pressed`, "a pressed tile takes the selection tint", "a pressed tile does not take the selection tint");
      await away();
      await page.mouse.up();
      check((await first.getAttribute("aria-expanded")) === "false", `${tag} T2 drag off`, "pressing and sliding off does not open it", "pressing and sliding off opened the tile");
      await first.click();
      check((await first.getAttribute("aria-expanded")) === "true", `${tag} T2 open`, "a tap opens the previous-period note", "a tap did not open the note");
      const note = first.locator("span").last();
      await note.waitFor({ timeout: 10000 });
      await page.waitForFunction((el) => !/^(Loading|Unavailable)$/.test(el.textContent.trim()), await note.elementHandle(), { timeout: 15000 }).catch(() => {});
      const text = (await note.innerText()).trim();
      check(NOTE.test(text), `${tag} T2 note`, `the note reads "${text}"`, `the note reads "${text}", not a comparison or "Not enough history"`);
      check((await first.boundingBox()).height > idleHeight, `${tag} T2 grew`, "the tile grows to hold the note", "the tile did not grow when it opened");
      check(await first.locator("svg[data-cue]").evaluate((s) => s.classList.contains("rotate-180")), `${tag} T2 turned`, "the chevron turns over", "the chevron did not turn over");
      await page.waitForTimeout(400); // the chevron's turn is a short transition
      await away();
      await t.shot(page, `${tag}-T2-open`);
      await first.click();
      check((await first.getAttribute("aria-expanded")) === "false", `${tag} T2 close`, "a second tap closes it", "a second tap did not close it");
      await away();
    });

    await step(`${tag} T3`, async () => {
      const row = page.locator('button[aria-haspopup="dialog"]').first();
      await row.waitFor({ timeout: 10000 });
      await row.click();
      const dlg = page.getByRole("dialog");
      const rate = dlg.getByRole("button", { name: /^Read rate/ });
      await rate.waitFor({ timeout: 10000 });
      // The whole card is the target (the button's overlay covers it), so the card is what is measured.
      const card = rate.locator("xpath=ancestor::div[1]");
      const box = await card.boundingBox();
      check(box.height >= 44, `${tag} T3 target`, `the Read rate card is ${Math.round(box.height)} px tall`, `the Read rate card is ${Math.round(box.height)} px tall, under 44`);
      const cue = rate.locator("svg[data-cue]");
      check((await cue.count()) === 1, `${tag} T3 cue`, "the Read rate carries a chevron", "the Read rate has no chevron");
      check((await rate.getAttribute("aria-expanded")) === "false", `${tag} T3 closed`, "the counts start hidden", "the counts do not start hidden");
      check(!(await cue.evaluate((s) => s.classList.contains("rotate-180"))), `${tag} T3 up`, "the chevron starts pointing down", "the chevron starts turned");
      if (!vp.mobile) {
        await card.hover();
        check(await card.evaluate(showsTint), `${tag} T3 hover`, "a hovered Read rate card takes the selection tint", "a hovered Read rate card does not take the selection tint");
      }
      await card.scrollIntoViewIfNeeded();
      const cb = await card.boundingBox();
      await page.mouse.move(cb.x + cb.width / 2, cb.y + cb.height / 2);
      await page.mouse.down();
      check(await card.evaluate(showsTint), `${tag} T3 pressed`, "a pressed Read rate card takes the selection tint", "a pressed Read rate card does not take the selection tint");
      await away();
      await page.mouse.up();
      await rate.click();
      check((await rate.getAttribute("aria-expanded")) === "true", `${tag} T3 open`, "a tap shows the counts behind the rate", "a tap did not show the counts");
      check(await cue.evaluate((s) => s.classList.contains("rotate-180")), `${tag} T3 turned`, "the chevron turns over", "the chevron did not turn over");
      await page.waitForTimeout(400);
      await away();
      await t.shot(page, `${tag}-T3-read-rate`);
      await page.keyboard.press("Escape");
    });

    await step(`${tag} T4`, async () => {
      await page.goto("/stats", { waitUntil: "load" });
      await tiles().first().waitFor({ timeout: 10000 });
      await page.evaluate(() => {
        document.documentElement.setAttribute("data-targets", "large");
        document.documentElement.style.setProperty("--kp-scale", "1.5");
        // The longest value a tile shows ("<n>h <m>m", statsFormat.ts durationLabel), in the middle tile.
        document.querySelectorAll("button[aria-expanded] > span.text-xl")[1].textContent = "12h 40m";
      });
      const clipped = [];
      for (let i = 0; i < 3; i++) {
        const btn = tiles().nth(i);
        const box = await btn.boundingBox();
        if (box.height < 56 || box.width < 56) clipped.push(`tile ${i} is ${Math.round(box.width)}x${Math.round(box.height)}`);
        const m = await btn.evaluate((b) => {
          const v = b.querySelector("span.text-xl").getBoundingClientRect();
          const r = b.getBoundingClientRect();
          const chev = b.querySelector("svg[data-cue]").getBoundingClientRect();
          // Text can wrap but must stay inside the tile and clear of the chevron.
          return { overflow: b.scrollWidth - b.clientWidth, rightGap: chev.left - v.right, inside: v.right <= r.right && v.left >= r.left };
        });
        if (m.overflow > 0 || !m.inside) clipped.push(`tile ${i} overflows by ${m.overflow}`);
        if (i === 1 && m.rightGap < 0) clipped.push(`the long value runs ${-Math.round(m.rightGap)} px under the chevron`);
      }
      check(clipped.length === 0, `${tag} T4 large`, "with Large targets and the largest text the tiles are at least 56 px and a long value is not clipped", clipped.join("; "));
      await away();
      await t.shot(page, `${tag}-T4-large-long`);
      await page.evaluate(() => {
        document.documentElement.removeAttribute("data-targets");
        document.documentElement.style.setProperty("--kp-scale", "1");
      });
    });

    if (vp.id === "phone") {
      await step(`${tag} T5`, async () => {
        await page.setViewportSize({ width: 667, height: 375 });
        await page.goto("/stats", { waitUntil: "load" });
        await tiles().first().waitFor({ timeout: 10000 });
        const small = [];
        for (let i = 0; i < 3; i++) {
          const box = await tiles().nth(i).boundingBox();
          if (box.height < 44 || box.width < 44) small.push(`tile ${i} is ${Math.round(box.width)}x${Math.round(box.height)}`);
        }
        check(small.length === 0, `${tag} T5 landscape`, "on a 667x375 landscape screen the tiles are at least 44 px", small.join("; "));
        await t.shot(page, `${tag}-T5-landscape`);
        await page.setViewportSize({ width: vp.width, height: vp.height });
      });
    }
  } finally {
    await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [pick.id], read: false } });
  }
});

await t.finish("Stats tile affordance");
