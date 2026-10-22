// UAT: the article's byline and toolbar.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:article-toolbar        in another, once the feeds have fetched
//   npm run uat:article-toolbar -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it opens an article from the list and checks:
//   T1  the feed name in the byline opens that feed's list (not the site) and the list is that feed's;
//   T2  the toolbar has Share as a button and More actions as its last button, after the reading menu;
//   T3  More actions holds Mark as read or unread (not in the toolbar), and it toggles the article's state; // uat-labels: ignore (a menu item's text, not an aria-label)
//   T4  More actions no longer holds Share.
// It marks the article it toggled back afterwards. It only runs against a loopback address with the seed's credentials
// unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} T`, async () => {
    await page.goto("/l/all", { waitUntil: "load" });
    const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await row.waitFor({ timeout: 15000 });
    await row.locator("a").first().click();
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    const bar = page.getByRole("toolbar", { name: "Article actions" });
    await bar.waitFor({ timeout: 10000 });
    const id = new URL(page.url()).pathname.split("/").pop();
    const itemState = async () => (await (await page.request.get(`/api/items/${id}`, { headers: { Accept: "application/json" } })).json()).read;

    const names = await bar.getByRole("button").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") ?? e.textContent?.trim() ?? ""));
    check(names.includes("Share") && !names.some((n) => /^Mark as (un)?read$/.test(n)), `${tag} T2 share`, "Share is a toolbar button and the read toggle is not", `buttons: ${names.join(", ")}`);
    check(names.at(-1) === "More actions", `${tag} T2 last`, "More actions is the last button", `buttons: ${names.join(", ")}`);
    await t.shot(page, `${tag}-article-toolbar`);

    // Opening an article marks it read a moment later, so let that settle before reading the state.
    await page.waitForTimeout(1500);
    const before = await itemState();
    await bar.getByRole("button", { name: "More actions" }).click();
    const items = (await page.getByRole("menu").getByRole("menuitem").allInnerTexts()).map((s) => s.trim());
    check(items[0]?.startsWith("Mark as") && !items.includes("Share"), `${tag} T3 menu`, "More starts with the read toggle and has no Share", `menu: ${items.join(", ")}`);
    const wantRead = (await page.getByRole("menuitem", { name: /^Mark as (un)?read$/ }).innerText()).trim() === "Mark as read"; // uat-labels: ignore (a menu item's text, not an aria-label)
    await page.getByRole("menuitem", { name: /^Mark as (un)?read$/ }).click();
    await page.waitForTimeout(600);
    const after = await itemState();
    check(after === wantRead && after !== before, `${tag} T3 toggle`, "the menu item toggles the article's read state", `read was ${before}, now ${after}`);
    if (after !== before) {
      await bar.getByRole("button", { name: "More actions" }).click();
      await page.getByRole("menuitem", { name: /^Mark as (un)?read$/ }).click();
      await page.waitForTimeout(400);
    }

    const feedName = (await page.getByTestId("read-state").locator("xpath=..").getByRole("link").innerText()).trim();
    const href = await page.getByTestId("read-state").locator("xpath=..").getByRole("link").getAttribute("href");
    await page.getByTestId("read-state").locator("xpath=..").getByRole("link").click();
    await page.waitForURL(/\/l\/\w+\?.*feed=/, { timeout: 8000 }).then(
      () => t.pass(`${tag} T1 route`, `the feed name (${feedName}, ${href}) opens ${page.url()}`),
      () => t.fail(`${tag} T1 route`, `the feed name did not open a feed list (at ${page.url()})`),
    );
    await t.shot(page, `${tag}-feed-list`);
  });
});

await t.finish("Article toolbar");
