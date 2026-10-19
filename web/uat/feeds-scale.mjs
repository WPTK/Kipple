// UAT: a long library on the Feeds screen, feeds that share a title, and the empty default folder.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:feeds-scale            in another
//   npm run uat:feeds-scale -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow imports 340 feeds at dead addresses, two of them with the same title (the shared cleanup removes them). For each width (desktop 1280x800,
// phone 375x812) it checks:
//   L1  the Feeds screen opens with part of the library drawn and says how many feeds are not shown yet;
//   L2  scrolling to the end of the list draws the rest;
//   L3  the two feeds with the same title show their address on their rows and in the delete confirmation;
//   L3b (desktop, where the sidebar shows) the sidebar tells the two feeds apart by the text of their items and by their names;
//   L4  the empty default folder is not shown, except in Edit, where it says what it is for.
//   L5  (desktop) ArrowDown on the last drawn sidebar row draws the next step and focuses the row after it.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { deadFeed, start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

const COUNT = 340;
const TWIN = "UAT feed twin";
const TWINS = [deadFeed(9001), deadFeed(9002)];
const short = (u) => u.replace(/^https?:\/\//, "");
const outline = (title, url) => `<outline type="rss" text="${title}" title="${title}" xmlUrl="${url}"/>`;
const xml = `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body><outline text="UAT scale">${Array.from(
  { length: COUNT },
  (_, i) => outline(`UAT feed ${i + 1}`, deadFeed(i + 1)),
).join("")}${TWINS.map((u) => outline(TWIN, u)).join("")}</outline></body></opml>`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} L setup`, async () => {
    await t.importOpml(page, xml);
  });
  const main = page.getByRole("main");
  const numbered = main.getByRole("link", { name: /^UAT feed \d+\b/ });
  const more = main.getByRole("button", { name: /^Show more feeds/ });

  await step(`${tag} L1`, async () => {
    await page.goto("/feeds");
    await numbered.first().waitFor({ timeout: 15000 });
    const drawn = await numbered.count();
    const label = (await more.count()) ? await more.first().innerText() : "";
    check(drawn < COUNT && /not shown yet/.test(label), `${tag} L1`, `${drawn} of ${COUNT} drawn; "${label}"`, `${drawn} of ${COUNT} feeds drawn at first, and the button reads "${label}"`);
    await t.shot(page, `${tag}-feeds-scale-first`);
  });

  await step(`${tag} L2`, async () => {
    const last = main.getByRole("link", { name: new RegExp(`^UAT feed ${COUNT}\\b`) });
    const end = Date.now() + 15000;
    while (Date.now() < end && !(await last.count())) {
      await page.locator("main [data-reveal-root]").evaluate((el) => el.scrollTo(0, el.scrollHeight));
      await page.mouse.wheel(0, 4000);
      await page.waitForTimeout(250);
    }
    const shown = (await last.count()) > 0;
    check(shown && (await more.count()) === 0, `${tag} L2`, `scrolling drew all ${await numbered.count()} feeds`, shown ? "every feed is drawn, but the Show more button stays" : `scrolling to the end did not draw "UAT feed ${COUNT}"`);
  });

  await step(`${tag} L3`, async () => {
    const rows = main.getByRole("link", { name: new RegExp(`^${TWIN}`) });
    const texts = await Promise.all((await rows.all()).map((r) => t.textOf(r)));
    const onRows = TWINS.every((u) => texts.some((x) => x.includes(short(u))));
    check(texts.length === 2 && onRows, `${tag} L3 rows`, `the rows say ${JSON.stringify(texts)}`, `the rows of the two "${TWIN}" feeds say ${JSON.stringify(texts)}, without their addresses`);

    await page.getByRole("button", { name: "Select", exact: true }).click();
    for (const u of TWINS) await main.getByRole("checkbox", { name: `Select ${TWIN}, ${short(u)}` }).check();
    await page.getByRole("button", { name: "Delete", exact: true }).click();
    const list = page.getByRole("list", { name: "Feeds to delete" });
    await list.waitFor({ timeout: 10000 });
    const items = await list.getByRole("listitem").allInnerTexts();
    const inDialog = TWINS.every((u) => items.some((x) => x.includes(short(u))));
    check(inDialog, `${tag} L3 dialog`, `the confirmation lists ${JSON.stringify(items)}`, `the delete confirmation lists ${JSON.stringify(items)}, without the addresses`);
    await t.shot(page, `${tag}-feeds-scale-delete`);
    await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("button", { name: "Done", exact: true }).click();
  });

  await step(`${tag} L3b`, async () => {
    const tree = page.getByRole("navigation", { name: "Primary" }).getByRole("tree", { name: "Feeds" });
    if (vp.mobile) {
      check((await tree.count()) === 0, `${tag} L3b`, "no sidebar tree on the phone", "the sidebar tree shows on the phone");
      return;
    }
    await page.goto("/feeds");
    // The twins sit at the end of a long tree, past the rows drawn at first: End draws the rest.
    await tree.getByRole("treeitem").first().focus();
    await page.keyboard.press("End");
    const items = tree.getByRole("treeitem", { name: new RegExp(`^${TWIN}`) });
    await items.first().waitFor({ timeout: 15000 });
    const names = await items.evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
    const texts = await Promise.all((await items.all()).map((r) => t.textOf(r)));
    const apart = TWINS.every((u) => names.some((n) => n?.includes(short(u))) && texts.some((x) => x.includes(short(u))));
    check(names.length === 2 && apart, `${tag} L3b`, `the sidebar items are ${JSON.stringify(names)}`, `the sidebar items are named ${JSON.stringify(names)} and read ${JSON.stringify(texts)}`);
    await t.shot(page, `${tag}-sidebar-twins`);
  });
  await step(`${tag} L4`, async () => {
    const b = await t.boot(page);
    const def = b.folders.find((f) => f.is_default);
    const name = def?.name ?? "Uncategorized";
    const row = main.getByRole("link", { name: new RegExp(`^${name}\\b`) });
    if (b.feeds.some((f) => f.folder_id === def?.id)) {
      // An earlier flow left a feed there: the folder is shown, and there is no empty state to check.
      check((await row.count()) > 0, `${tag} L4`, `"${name}" holds a feed and is shown`, `"${name}" holds a feed but is not shown`);
      return;
    }
    const hidden = (await row.count()) === 0;
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const inEdit = await row.first().waitFor({ timeout: 5000 }).then(() => true, () => false);
    const hint = (await main.getByText("Empty. Feeds added without a folder go here.").count()) > 0;
    check(hidden && inEdit && hint, `${tag} L4`, `"${name}" hidden, shown in Edit with its hint`, `"${name}": hidden outside Edit ${hidden}, shown in Edit ${inEdit}, hint ${hint}`);
    await page.getByRole("button", { name: "Done editing" }).click();
  });
  await step(`${tag} L5`, async () => {
    if (vp.mobile) return;
    await page.goto("/feeds");
    const tree = page.getByRole("navigation", { name: "Primary" }).getByRole("tree", { name: "Feeds" });
    const items = tree.getByRole("treeitem");
    await items.first().waitFor({ timeout: 15000 });
    const before = await items.count();
    const lastKey = await items.last().getAttribute("data-tree-key");
    await items.last().focus();
    await page.keyboard.press("ArrowDown");
    const end = Date.now() + 10000;
    while (Date.now() < end && (await items.count()) <= before) await page.waitForTimeout(100);
    const at = await tree.locator(":focus").getAttribute("data-tree-key");
    const all = await items.evaluateAll((els) => els.map((e) => e.getAttribute("data-tree-key")));
    check(before < all.length && at === all[before], `${tag} L5`, `focus moved from ${lastKey} to ${at}, the row after it`, `focus is on ${at}; the row after ${lastKey} is ${all[before]}`);
  });
});

await t.finish("feeds scale");
