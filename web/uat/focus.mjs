// UAT: where keyboard focus goes when articles leave the list.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:focus                  in another, once the feeds have fetched
//   npm run uat:focus -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   F1  Mute similar from a row's menu in an Unread list, opened from the keyboard: Escape gives focus back to the
//       row; then tap a suggestion, Save. The row leaves the list and focus moves to the row that took its place, not
//       to the page, so the next Tab or arrow key goes on from there.
//   F2  (desktop, Editorial) Open an article in the reading pane, move focus to the next row with the Down arrow, then
//       put focus on the page and press Shift+A. The rows leave, the row the list remembers among them, and focus
//       stays on the page instead of jumping into the list, so Space and Page Down still scroll the article. z then
//       puts the rows back.
// Afterwards it deletes the filter it made with unmute=unread and marks the articles it used unread again. It only
// runs against a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0 clean,
// 1 findings, 2 setup error. First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check, step, textOf } = t;
const JSON_ACCEPT = { Accept: "application/json" };

const filterIds = async (page) => ((await (await page.request.get("/api/filters", { headers: JSON_ACCEPT })).json()).filters ?? []).map((f) => f.id);
const unreadOf = async (page, feed) =>
  ((await (await page.request.get("/api/items", { params: { view: "unread", limit: 40, ...(feed ? { feed } : {}) }, headers: JSON_ACCEPT })).json()).items ?? []);
const setRead = (page, ids, read) => page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids, read } });
/** What has focus: "body", "row <id>", "list" (the list itself) or a short description of anything else. */
const focusOf = (page) =>
  page.evaluate(() => {
    const a = document.activeElement;
    if (!a || a === document.body) return "body";
    const row = a.closest("[data-item-id]");
    if (row) return `row ${row.getAttribute("data-item-id")}`;
    if (a.closest("[data-testid=list-scroll]")) return "list";
    return `${a.tagName.toLowerCase()}${a.getAttribute("aria-label") ? ` "${a.getAttribute("aria-label")}"` : ""}`;
  });

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const before = new Set(await filterIds(page));
  // A feed with at least three unread articles whose title offers a word to tap.
  const all = (await unreadOf(page)).filter((i) => /\p{L}{5,}/u.test(i.title ?? ""));
  const byFeed = new Map();
  for (const i of all) byFeed.set(i.feed_id, [...(byFeed.get(i.feed_id) ?? []), i]);
  const picks = [...byFeed.values()].find((l) => l.length >= 3);
  if (!picks) return t.setupError(`${tag}: no feed in the seed has three unread articles with a word to suggest (have the feeds fetched?)`);
  const feed = picks[0].feed_id;
  const touched = (await unreadOf(page, feed)).map((i) => i.id);

  try {
    await step(`${tag} F1`, async () => {
      const it = picks[0];
      await page.goto(`/l/unread?feed=${feed}`, { waitUntil: "load" });
      const row = page.locator(`[data-item-id="${it.id}"]`).first();
      await row.waitFor({ timeout: 15000 });
      const dialog = page.getByRole("dialog", { name: "New filter" });
      // From the keyboard, as a keyboard user reaches the row's menu (a click or a long press opens it with focus
      // nowhere, and focus is then left where it was).
      const open = async () => {
        await row.getByRole("button", { name: "More actions" }).focus();
        await page.keyboard.press("Enter");
        await page.getByRole("menuitem", { name: "Mute similar…" }).focus();
        await page.keyboard.press("Enter");
        await dialog.waitFor({ timeout: 10000 });
      };
      // Cancel first: focus goes back to the row the editor was opened from.
      await open();
      await page.keyboard.press("Escape");
      await dialog.waitFor({ state: "detached", timeout: 10000 });
      await page.waitForTimeout(300);
      const cancelled = await focusOf(page);
      check(cancelled === `row ${it.id}`, `${tag} F1 cancel`, "Escape gives focus back to the row", `focus is on ${cancelled}, not on row ${it.id}`);
      await open();
      await dialog.getByRole("region", { name: "Suggestions from this article" }).getByRole("button").first().click();
      await dialog.getByRole("button", { name: "Save filter" }).click();
      await dialog.waitFor({ state: "detached", timeout: 10000 });
      const left = await row.waitFor({ state: "detached", timeout: 5000 }).then(() => true, () => false);
      check(left, `${tag} F1 list`, "the muted row leaves Unread", "the muted row is still in the Unread list");
      await page.waitForTimeout(500);
      const at = await focusOf(page);
      check(at.startsWith("row ") && at !== `row ${it.id}`, `${tag} F1 focus`, `focus moves to the row that took its place (${at})`, `focus is on ${at}, not on a row of the list`);
      await t.shot(page, `${tag}-F1`);
    });
    if (vp.mobile) return;
    await step(`${tag} F2`, async () => {
      await page.goto(`/l/unread?feed=${feed}`, { waitUntil: "load" });
      // Editorial: rows that leave collapse out of the list (a page layout fades them in place instead).
      const layoutBtn = page.locator('button[aria-label^="List options, "]').first();
      await layoutBtn.waitFor({ timeout: 15000 });
      if ((await layoutBtn.getAttribute("aria-label")) !== "List options, Editorial layout") {
        await layoutBtn.click();
        await page.getByRole("menuitemradio", { name: /^Editorial\b/ }).click();
        await page.keyboard.press("Escape");
      }
      const first = page.locator("[data-item-id]").first();
      await first.waitFor({ timeout: 15000 });
      await first.locator("a").first().click();
      await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
      // Down arrow moves focus to the next row without opening it: the open article stays in the list, the row the
      // list remembers as focused leaves with Shift+A.
      await page.keyboard.press("ArrowDown");
      await page.waitForTimeout(300);
      const body = page.locator("article[aria-labelledby='article-title']").first();
      await body.waitFor({ timeout: 10000 });
      await body.click({ position: { x: 40, y: 120 } });
      // Focus on the page itself (as after a toast or a menu that went away): the hardest case, since a row that
      // leaves also leaves focus on the page.
      await page.evaluate(() => document.activeElement instanceof HTMLElement && document.activeElement.blur());
      const was = await focusOf(page);
      await page.keyboard.press("Shift+A");
      const gone = await page.waitForFunction(() => document.querySelectorAll("[data-item-id]").length === 0 || ![...document.querySelectorAll(".kp-unread-dot")].some((d) => d.dataset.unread === "true"), undefined, { timeout: 8000 }).then(() => true, () => false);
      await page.waitForTimeout(800);
      const at = await focusOf(page);
      check(gone && !at.startsWith("row ") && at !== "list", `${tag} F2 focus`, `focus stays out of the list (${was} before, ${at} after)`, `marked=${gone}; focus moved from ${was} to ${at}`);
      await t.shot(page, `${tag}-F2`);
      await page.keyboard.press("z");
      await page.waitForTimeout(1500);
      const back = (await unreadOf(page, feed)).length;
      check(back > 0, `${tag} F2 undo`, `z marks them unread again (${back})`, `nothing unread in the feed after z (page: ${await textOf(page.locator("[role=status]"))})`);
    });
  } finally {
    for (const id of (await filterIds(page)).filter((id) => !before.has(id))) {
      await page.request.delete(`/api/filters/${id}?unmute=unread`, { headers: t.WRITE }).catch(() => {});
    }
    await setRead(page, touched, false).catch(() => {});
  }
});

await t.finish("focus when articles leave the list");
