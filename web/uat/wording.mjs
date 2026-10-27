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
//   W5  Settings > Appearance > Articles has no mark on the section: each setting says This device or All devices;
//   W6  helper text: the lines that only restate a label are gone from Settings > Appearance, Settings > Account & Devices
//       and the feed editor, and the reasons stay (the font switch says why it is off, a retention note says what is left alone).
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 15);
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

  await step(`${tag} W6`, async () => {
    const gone = async (where, text, words) => {
      const found = words.filter((w) => text.includes(w));
      check(found.length === 0, `${tag} W6 ${where}`, "the lines that restate a label are gone", `still there: ${found.join(" | ")}`);
    };
    await page.goto("/settings/appearance", { waitUntil: "load" });
    const sw = page.getByRole("switch", { name: "Also use it in lists and the sidebar" });
    await sw.waitFor({ timeout: 15000 });
    const appearance = (await page.locator("main").innerText()).replace(/\s+/g, " ");
    await gone("appearance", appearance, ["Makes all text larger or smaller", "Used for article text on this device", "How much space between lines and paragraphs", "Makes buttons and tap targets bigger", "Most people never need", "Swipe a row right to mark it read"]);
    const why = await sw.evaluate((el) => ({ off: el.disabled || el.getAttribute("aria-disabled") === "true", text: (el.getAttribute("aria-describedby") ?? "").split(" ").map((id) => document.getElementById(id)?.textContent ?? "").join(" ") }));
    check(why.off && /Choose a reading font first/.test(why.text), `${tag} W6 reason`, "the disabled font switch says why", `disabled: ${why.off}, described by: ${JSON.stringify(why.text)}`);

    await page.goto("/settings/account", { waitUntil: "load" });
    await page.getByRole("heading", { name: "Devices", exact: true }).waitFor({ timeout: 15000 });
    const account = (await page.locator("main").innerText()).replace(/\s+/g, " ");
    await gone("account", account, ["Open Kipple on another browser", "Shown in this list so you can tell", "Opens the setup steps again", "as one file you keep"]);
    check(/follows the default, so it stays in step/.test(account), `${tag} W6 default`, "the default-for-new-devices note stays: it says what happens", "the note is missing");

    await page.goto("/l/all", { waitUntil: "load" });
    const first = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await first.waitFor({ timeout: 15000 });
    await first.click();
    await page.getByRole("button", { name: "More actions" }).first().click();
    await page.getByRole("menuitem", { name: "Edit feed", exact: true }).click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    await dlg.waitFor({ timeout: 10000 });
    await dlg.getByRole("button", { name: "Advanced" }).click();
    const editor = (await dlg.innerText()).replace(/\s+/g, " ");
    await gone("editor", editor, ["How Kipple decides that two entries", "Kipple stops checking this feed", "Change this to point at a new address"]);
    check(/Starred and muted articles are never touched/.test(editor), `${tag} W6 left alone`, "the editor says starred and muted articles are never touched", "the note is missing");
    await t.shot(page, `${tag}-helper-text-editor`);
    await page.keyboard.press("Escape");
  });
});

await t.finish("wording");
