// UAT: Mark all as read, the swipe panel's words and the article's state word.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:item-actions           in another, once the feeds have fetched
//   npm run uat:item-actions -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   A1  List actions has a Mark all item that names its reach: "Mark all N as read" in a feed or a folder (N is the
//       unread count the server gives), "Mark everything unread as read" in Unread, and the plain words where no count
//       is known (Starred, a reading-time filter);
//   A2  choosing it asks first: a dialog with the same words, and nothing is marked until it is confirmed;
//   A3  Cancel leaves every article as it was;
//   A4  confirming marks the list read and offers Undo, which brings the unread count back;
//   A5  phone, at 375 and 320 wide: a real touch swipe right on a row shows "Mark as read" or "Mark as unread" whole,
//       inside the panel, once the swipe is far enough to arm;
//   A6  an unread article shows "Unread" in plain text and a read one shows no state word.
// It reads one article by opening it (A6). It only runs against a loopback address with the seed's credentials unless
// --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check, step } = t;

// The row swipe arms at 40% of the row's width, between 96 and 176 px (src/gestures/tracking.ts).
const armAt = (width) => Math.min(176, Math.max(96, width * 0.4));

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const boot = async () => (await page.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
  const unreadCount = async () => (await boot()).counts.unread;
  const firstLine = async (item) => (await item.innerText()).split("\n")[0].trim();

  await step(`${tag} A1`, async () => {
    const b = await boot();
    const feed = b.feeds.find((f) => f.unread > 0);
    const folder = b.folders.find((f) => f.unread > 0);
    const cases = [
      ["Unread", "/l/unread", "Mark everything unread as read"],
      ...(feed ? [["a feed", `/l/all?feed=${feed.id}`, `Mark all ${feed.unread} as read`]] : []),
      ...(folder ? [["a folder", `/l/all?folder=${folder.id}`, `Mark all ${folder.unread} as read`]] : []),
      ["Starred", "/l/starred", "Mark all as read"],
      ["a reading-time filter", "/l/all?len=short", "Mark all as read"],
    ];
    check(!!feed && !!folder, `${tag} A1 setup`, "the seed has a feed and a folder with unread articles", `feed ${!!feed}, folder ${!!folder}`);
    for (const [what, path, want] of cases) {
      await page.goto(path, { waitUntil: "load" });
      await page.getByRole("button", { name: "List actions" }).waitFor({ timeout: 15000 });
      await page.getByRole("button", { name: "List actions" }).click();
      const item = page.getByRole("menuitem", { name: /^Mark (all|everything)/ });
      await item.waitFor({ timeout: 5000 });
      const words = await firstLine(item);
      check(words === want, `${tag} A1 ${what}`, `Mark all in ${what} says "${want}"`, `it says "${words}"`);
      await page.keyboard.press("Escape");
    }
  });

  await step(`${tag} A2`, async () => {
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
    const before = await unreadCount();

    await page.getByRole("button", { name: "List actions" }).click();
    const item = page.getByRole("menuitem", { name: /^Mark (all|everything)/ });
    const words = await firstLine(item);
    await item.click();

    const dialog = page.getByRole("dialog");
    await dialog.waitFor({ timeout: 5000 });
    const title = (await dialog.getByRole("heading").innerText()).trim();
    check(title === words && (await unreadCount()) === before, `${tag} A2`, "it asks first with the same words and marks nothing yet", `dialog "${title}", unread ${before} -> ${await unreadCount()}`);
    await t.shot(page, `${tag}-mark-all-confirm`);

    await dialog.getByRole("button", { name: "Cancel" }).click();
    await dialog.waitFor({ state: "hidden", timeout: 5000 });
    check((await unreadCount()) === before, `${tag} A3`, "Cancel marks nothing", `unread ${before} -> ${await unreadCount()}`);

    await page.getByRole("button", { name: "List actions" }).click();
    await page.getByRole("menuitem", { name: /^Mark (all|everything)/ }).click();
    await page.getByRole("dialog").getByRole("button", { name: words }).click();
    const undo = page.getByRole("button", { name: "Undo" }).first();
    await undo.waitFor({ timeout: 5000 });
    const marked = await unreadCount();
    await t.shot(page, `${tag}-mark-all-undo`);
    await undo.click();
    await page.waitForTimeout(1000);
    const back = await unreadCount();
    check(marked === 0 && back === before, `${tag} A4`, "confirming marks the list read and Undo brings the count back", `unread ${before} -> ${marked} -> ${back}`);
  });

  if (vp.mobile) {
    for (const width of [375, 320]) {
      await step(`${tag} A5 ${width}`, async () => {
        await page.setViewportSize({ width, height: 700 });
        await page.goto("/l/all", { waitUntil: "load" });
        const row = page.locator("[data-swipe-row]").first();
        await row.waitFor({ timeout: 15000 });
        const box = await row.boundingBox();
        const cdp = await page.context().newCDPSession(page);
        const touch = (type, points) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points });
        const y = Math.round(box.y + Math.min(box.height / 2, 60));
        const from = Math.round(box.x + 6);
        const to = Math.round(from + armAt(box.width) + 12);
        await touch("touchStart", [{ x: from, y }]);
        for (let x = from; x <= to; x += 8) await touch("touchMove", [{ x, y }]);
        await page.waitForTimeout(150);
        const seen = await row.evaluate((el) => {
          const panel = el.querySelector(".swipe-panel");
          const label = panel?.querySelector("span:not(.swipe-icon)");
          const p = panel?.getBoundingClientRect();
          const l = label?.getBoundingClientRect();
          return { text: label?.textContent?.trim() ?? "", fits: !!p && !!l && l.right <= p.right + 0.5 && l.left >= p.left && getComputedStyle(panel).visibility === "visible", armed: panel?.dataset.armed };
        });
        await t.shot(page, `${tag}-swipe-${width}`);
        await touch("touchCancel", []);
        check(/^Mark as (un)?read$/.test(seen.text) && seen.fits && seen.armed === "true", `${tag} A5 ${width}`, `a swipe at ${width} px wide shows the whole "Mark as read" or "Mark as unread" in an armed panel`, `panel: ${JSON.stringify(seen)}`);
        await page.waitForTimeout(400);
      });
    }
    await page.setViewportSize({ width: vp.width, height: vp.height });
  }

  await step(`${tag} A6`, async () => {
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
    await page.locator("[data-testid=list-scroll] [data-item-id] a").first().click();
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    const state = page.getByTestId("read-state");
    await state.waitFor({ timeout: 10000 });
    const word = (await state.innerText()).trim();
    check(word === "Unread", `${tag} A6 unread`, "an unread article shows Unread", `the state word is "${word}"`);
    await t.shot(page, `${tag}-article-state`);
    // Opening it marks it read a moment later; a read article has no state word at all.
    await page.waitForFunction(() => !document.querySelector('[data-testid="read-state"]'), undefined, { timeout: 8000 }).then(
      () => check(true, `${tag} A6 read`, "once read the article shows no state word", ""),
      () => check(false, `${tag} A6 read`, "once read the article shows no state word", "the state word stayed"),
    );
  });
});

await t.finish("item actions");
