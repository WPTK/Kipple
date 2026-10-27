// UAT: the shared words (Spacing, Edit feed, layout hints, Mark all) and the scope marks in Settings.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/wording.mjs               in another, once the feeds have fetched
//   node uat/wording.mjs --url http://127.0.0.1:7400 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   W1  Settings > Appearance has one Spacing picker whose help names the layouts that ignore it, and no Density;
//   W2  each layout in the Show and sort menu says what it is for (Headlines: "For catching up fast");
//   W3  an open article's More menu says Edit feed, and it opens the feed's editor;
//   W4  a folder's menu on Feeds says "Mark all N as read", the same words as the list's own menu;
//   W5  Settings > Appearance > Articles has no mark on the section: each setting says This device or All devices.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);

  await step(`${tag} W1`, async () => {
    await page.goto("/settings/appearance", { waitUntil: "load" });
    const group = page.getByRole("group", { name: "Spacing", exact: true });
    await group.waitFor({ timeout: 15000 });
    const text = (await group.innerText()).replace(/\s+/g, " ");
    check(/Cards and Gazette/.test(text), `${tag} W1 help`, "the Spacing help names Cards and Gazette", `help: ${text}`);
    const old = await page.getByRole("group", { name: /^(Density|List spacing)$/ }).count();
    check(old === 0, `${tag} W1 one word`, "no Density or List spacing picker is left", `${old} found`);
    await t.shot(page, `${tag}-settings-spacing`);
  });

  await step(`${tag} W2`, async () => {
    const set = await patch({ "client.layout": "headlines" });
    if (set !== 200) throw new Error(`the layout was not set (${set})`);
    await page.goto("/l/unread", { waitUntil: "load" });
    await page.locator('button[aria-label^="Show and sort, "]').first().click();
    const menu = page.getByRole("menu");
    await menu.waitFor({ timeout: 10000 });
    const text = (await menu.innerText()).replace(/\s+/g, " ");
    const want = ["Big picture, short summary", "For catching up fast", "Pages, newest first", "Sections by folder, feed or reading time"];
    const missing = want.filter((w) => !text.includes(w));
    check(missing.length === 0, `${tag} W2 hints`, "each layout says what it is for", `missing: ${missing.join(" | ")}`);
    await t.shot(page, `${tag}-layout-hints`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} W3`, async () => {
    await page.goto("/l/all", { waitUntil: "load" });
    const first = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await first.waitFor({ timeout: 15000 });
    await first.click();
    await page.getByRole("button", { name: "More actions" }).first().click();
    const item = page.getByRole("menuitem", { name: "Edit feed", exact: true });
    await item.waitFor({ timeout: 5000 });
    check(true, `${tag} W3 menu`, "the article's More menu says Edit feed", "");
    await t.shot(page, `${tag}-edit-feed`);
    await item.click();
    await page.getByRole("dialog", { name: "Edit feed" }).waitFor({ timeout: 10000 });
    check(true, `${tag} W3 editor`, "it opens the feed's editor", "");
    await page.keyboard.press("Escape");
  });

  await step(`${tag} W4`, async () => {
    const b = await (await page.request.get("/api/bootstrap", { headers: { Accept: "application/json" } })).json();
    const folder = b.folders.find((f) => f.unread > 0);
    if (!folder) throw new Error("the seed has no folder with unread articles");
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: `Folder actions for ${folder.name}` }).click();
    const item = page.getByRole("menuitem", { name: /^Mark all/ });
    await item.waitFor({ timeout: 5000 });
    const words = (await item.innerText()).trim();
    check(words === `Mark all ${folder.unread} as read`, `${tag} W4 folder`, `the folder menu says "Mark all ${folder.unread} as read"`, `it says "${words}"`);
    await t.shot(page, `${tag}-folder-mark-all`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} W5`, async () => {
    await page.goto("/settings/appearance", { waitUntil: "load" });
    const section = page.getByRole("region", { name: "Articles", exact: true });
    await section.waitFor({ timeout: 15000 });
    const marks = await section.locator("span").evaluateAll((els) => els.map((e) => (e.textContent ?? "").trim()).filter((x) => x === "This device" || x === "All devices"));
    check(marks.join() === "This device,This device,All devices", `${tag} W5 marks`, "Article width and Open links in say This device, the tracking setting says All devices", `marks: ${marks.join(", ")}`);
    await section.scrollIntoViewIfNeeded();
    await t.shot(page, `${tag}-settings-articles`);
  });
});

await t.finish("wording");
