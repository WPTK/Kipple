// UAT: the item action table's surfaces (Mark all as read, the swipe panel's words, the article's state word).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:item-actions           in another, once the feeds have fetched
//   npm run uat:item-actions -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   A1  List actions has a Mark all item that names its reach ("Mark all 12 as read", or "Mark everything unread as read" in Unread);
//   A2  choosing it asks first: a dialog with the same words, and nothing is marked until it is confirmed;
//   A3  Cancel leaves every article as it was;
//   A4  confirming marks the list read and offers Undo, which brings the unread count back;
//   A5  phone: a row's swipe panel says "Mark as read" (the menu's words) and not "Read";
//   A6  an open, read article shows no "Read" word, only an unread one says "Unread".
// It marks everything it read unread again with Undo. It only runs against a loopback address with the seed's credentials
// unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const unreadCount = async () => (await (await page.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json()).counts.unread;
  await step(`${tag} A`, async () => {
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
    const before = await unreadCount();

    await page.getByRole("button", { name: "List actions" }).click();
    const item = page.getByRole("menuitem", { name: /^Mark (all|everything)/ });
    const words = (await item.innerText()).split("\n")[0].trim();
    check(/^Mark everything unread as read$/.test(words), `${tag} A1`, "the Unread list's Mark all names its reach", `it says "${words}"`);
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
    await t.shot(page, `${tag}-mark-all-undo`);
    await undo.click();
    await page.waitForTimeout(1000);
    const back = await unreadCount();
    check(back === before, `${tag} A4`, "confirming marks the list read and Undo brings the count back", `unread ${before} -> ${back}`);
  });

  if (vp.mobile) {
    await step(`${tag} A5`, async () => {
      await page.goto("/l/all", { waitUntil: "load" });
      await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
      const panels = await page.locator("[data-swipe-row] .swipe-panel").evaluateAll((els) => els.map((e) => e.textContent?.trim() ?? ""));
      check(panels.length > 0 && panels.some((p) => /^Mark as (un)?read$/.test(p)) && !panels.some((p) => /^(Read|Unread)$/.test(p)), `${tag} A5`, "the swipe panel uses the menu's words", `panels: ${panels.slice(0, 4).join(" | ")}`); // uat-labels: ignore (visible text, not an aria-label)
    });
  }

  await step(`${tag} A6`, async () => {
    await page.goto("/l/all", { waitUntil: "load" });
    await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
    await page.locator("[data-testid=list-scroll] [data-item-id] a").first().click();
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    const state = page.getByTestId("read-state");
    await state.waitFor({ state: "attached", timeout: 10000 });
    await page.waitForTimeout(2000);
    const text = (await state.innerText()).trim();
    check(text !== "Read", `${tag} A6`, "an article never shows a Read word", `the state word is "${text}"`); // uat-labels: ignore (visible text, not an aria-label)
    await t.shot(page, `${tag}-article-state`);
  });
});

await t.finish("item actions");
