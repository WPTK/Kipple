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
//   A6  an unread article shows "Unread" in plain text and a read one shows no state word;
//   A7  Feeds: a folder's Mark all asks first with the same words, marks nothing until confirmed, and its More button
//       draws the same glyph as the list's;
//   A8  a row's More menu lists its actions in the table's order, fills Star once starred, and offers Share only where
//       the browser can share (Copy link stays).
// It opens one article and toggles its read state (A6), leaving it read. It only runs against a loopback address with the seed's credentials unless
// --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
import { expect } from "@playwright/test";
import { start, waitForAnimationsDone } from "./common.mjs";

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
    // The count comes back once the server has taken the Undo; the check below reports it if it never does.
    await expect.poll(unreadCount, { timeout: 10000, intervals: [200] }).toBe(before).catch(() => {});
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
        // Not at the screen edge, where a swipe is Back.
        const from = Math.round(box.x + 40);
        const to = Math.round(from + armAt(box.width) + 12);
        await touch("touchStart", [{ x: from, y }]);
        for (let x = from; x <= to; x += 8) await touch("touchMove", [{ x, y }]);
        await row.locator(".swipe-panel[data-armed='true']").waitFor({ timeout: 3000 }).catch(() => {}); // the check below reports it if the panel never arms
        await waitForAnimationsDone(page);
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
        await row.locator(".swipe-panel[data-armed='true']").waitFor({ state: "hidden", timeout: 3000 }); // the row springs back: the panel disarms
      });
    }
    await page.setViewportSize({ width: vp.width, height: vp.height });
  }

  await step(`${tag} A7`, async () => {
    const b = await boot();
    const folder = b.folders.find((f) => f.unread > 0);
    if (!folder) return t.fail(`${tag} A7 setup`, "the seed has no folder with unread articles");
    const before = await unreadCount();
    await page.goto("/l/unread", { waitUntil: "load" });
    const listGlyph = await page.getByRole("button", { name: "List actions" }).locator("svg").getAttribute("class");
    await page.goto("/feeds", { waitUntil: "load" });
    const target = page.getByRole("button", { name: `Folder actions for ${folder.name}`, exact: true });
    await target.waitFor({ timeout: 15000 });
    const glyph = await target.locator("svg").getAttribute("class");
    await target.click();
    const want = `Mark all ${folder.unread} as read`;
    await page.getByRole("menuitem", { name: want }).click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor({ timeout: 5000 });
    const title = (await dialog.getByRole("heading").innerText()).trim();
    check(title === want && (await unreadCount()) === before, `${tag} A7 asks`, "the folder's Mark all asks first with the same words and marks nothing yet", `dialog "${title}", unread ${before} -> ${await unreadCount()}`);
    check(glyph === listGlyph, `${tag} A7 glyph`, "the folder's More button draws the list's glyph", `list "${listGlyph}", folder "${glyph}"`);
    await t.shot(page, `${tag}-folder-mark-all-confirm`);
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await dialog.waitFor({ state: "hidden", timeout: 5000 });
    check((await unreadCount()) === before, `${tag} A7 cancel`, "Cancel marks nothing", `unread ${before} -> ${await unreadCount()}`);
  });

  await step(`${tag} A8`, async () => {
    await page.goto("/l/all", { waitUntil: "load" });
    const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await row.waitFor({ timeout: 15000 });
    const id = await row.getAttribute("data-item-id");
    const starred = async () => (await (await page.request.get(`/api/items/${id}`)).json()).starred;
    const canShare = await page.evaluate(() => typeof navigator.share === "function");
    const open = async () => {
      await row.hover();
      await row.getByRole("button", { name: /^More actions/ }).click();
      await page.getByRole("menu").waitFor({ timeout: 5000 });
    };
    await open();
    const names = (await page.getByRole("menu").getByRole("menuitem").allInnerTexts()).map((x) => x.trim());
    const order = ["Star|Unstar", "Mark as read|Mark as unread", "Mark above as read", "Mark below as read", "Mute similar…", "Edit feed", "Open original", "Copy link", "Share"]; // uat-labels: ignore (menu item text)
    const rank = names.map((n) => order.findIndex((o) => o.split("|").includes(n)));
    check(!rank.includes(-1) && rank.every((r, k) => k === 0 || r > rank[k - 1]), `${tag} A8 order`, "the row menu lists the table's actions in order", `the menu offers: ${names.join(", ")}`);
    check(names.includes("Share") === canShare && names.includes("Copy link"), `${tag} A8 share`, "Share is offered only where the browser can share, and Copy link always", `canShare ${canShare}, menu: ${names.join(", ")}`);
    const wasStarred = await starred();
    await page.getByRole("menuitem", { name: wasStarred ? "Unstar" : "Star", exact: true }).click();
    await expect.poll(starred, { timeout: 8000, intervals: [200] }).toBe(!wasStarred);
    await open();
    const filled = await page.getByRole("menuitem", { name: wasStarred ? "Star" : "Unstar", exact: true }).locator("svg").getAttribute("fill");
    check(wasStarred ? filled === "none" : filled === "currentColor", `${tag} A8 fill`, "the menu's star is filled while the article is starred", `fill "${filled}" with starred ${!wasStarred}`);
    await t.shot(page, `${tag}-row-menu`);
    await page.keyboard.press("Escape");
    await open();
    await page.getByRole("menuitem", { name: wasStarred ? "Star" : "Unstar", exact: true }).click(); // put it back
    await expect.poll(starred, { timeout: 8000, intervals: [200] }).toBe(wasStarred);
  });

  await step(`${tag} A6`, async () => {
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
    await page.locator("[data-testid=list-scroll] [data-item-id] a").first().click();
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    // Opening marks it read at once, and a read article has no state word at all.
    const state = page.getByTestId("read-state");
    await state.waitFor({ state: "detached", timeout: 10000 }).catch(() => {});
    check((await state.count()) === 0, `${tag} A6 read`, "a read article shows no state word", `the state word is "${await state.innerText().catch(() => "")}"`);
    await t.shot(page, `${tag}-article-read`);
    // Marked unread from the toolbar's More menu, it says so in plain text; marked read again, it is gone.
    const bar = page.getByRole("toolbar", { name: "Article actions" });
    await bar.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Mark as unread" }).click();
    await state.waitFor({ timeout: 5000 });
    const word = (await state.innerText()).trim();
    const plain = await state.evaluate((el) => ({ border: getComputedStyle(el).borderTopWidth, radius: getComputedStyle(el).borderTopLeftRadius }));
    check(word === "Unread" && plain.border === "0px" && plain.radius === "0px", `${tag} A6 unread`, "an unread article shows Unread in plain text", `the state word is "${word}", ${JSON.stringify(plain)}`);
    await t.shot(page, `${tag}-article-state`);
    await bar.getByRole("button", { name: "More actions" }).click();
    await page.getByRole("menuitem", { name: "Mark as read" }).click();
    await state.waitFor({ state: "detached", timeout: 5000 }).catch(() => {});
    check((await state.count()) === 0, `${tag} A6 back`, "marked read again, the state word goes", `${await state.count()} state words`);  });
});

await t.finish("item actions");
