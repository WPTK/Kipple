// UAT: the Feeds header, its menu and edit mode, and the Feed health selection bar (trackers #759, #668 #672 #673 #675 #676 #617 #735).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/feeds-organize.mjs        in another, once the feeds have fetched
//   node uat/feeds-organize.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser, imports one disposable feed in a
// disposable folder, and on the Feeds screen checks:
//   O1  the header: Edit, Add feed and the menu in the plain state; the saved note, Done editing and the menu in edit
//       mode (Add feed and Edit hidden); Done alone while selecting; nothing in any state scrolls sideways or runs
//       past the window; Edit and Done are one button, so the keyboard focus stays on it after each press;
//   O2  the menu: the groups Organize and Files, the names "Import feeds from a file (OPML)" and "Export feeds to a
//       file (OPML)", no "Show move buttons" item, and the menu inside the window;
//   O3  edit mode: the help line has a "Show move buttons" button that shows the move buttons and hides them again;
//   O4  a new folder: the dialog is titled New folder, the folder says Empty in place of a count, and its dialog is
//       titled Folder settings;
//   O5  the selection bar: Select all, Move and Delete inside the window (on one row at desktop; the phone's system font
//       decides whether the bar needs a second row), Delete drawn differently from Move;
//   O6  the import dialog: a file picker at least 44 px tall and a days field that says what it does;
//   O7  Add feed: Options cannot be collapsed while the title is too long, stays open when the title is shortened under
//       the focused field, and Add feed stays off;
//   O11 a folder's note about its one failing feed (the feed is at an address that does not exist) says when Kipple
//       tries it again on a line of its own that is not clipped;
// and on Feed health:
//   O8  turning Select on does not move the feed names (desktop);
//   O9  the selection bar has Retry now (off until a feed is ticked), Resume, Pause and a red Delete inside the window;
//       Retry now fetches the ticked feed and says how many were retried;
//   O10 axe (the WCAG 2.0 A and AA rules) finds nothing on Feed health, with Select off and with Select on.
// The folder and the feed are removed when the width is done. It changes nothing else (O11 asks for one refresh of every
// feed so the disposable feed records its failed fetch): every dialog is closed with
// Escape and nothing is exported. It only runs against a loopback address with the seed's credentials unless
// --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { start } from "./common.mjs";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const t = await start(import.meta.url, 32);
const { check, step } = t;

const FOLDER = "UAT organize";
const EMPTY = "UAT organize empty";
const FEED = "UAT feed 7101";

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const inWindow = (box) => !!box && box.x >= -1 && box.x + box.width <= page.viewportSize().width + 1;
  const sideways = () => page.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
  const menuButton = () => page.getByRole("button", { name: "Feed actions", exact: true });

  // A feed and a folder of this flow's own; an empty folder is made in the New folder step.
  await t.importOpml(page, t.opml([{ folder: FOLDER, feeds: [7101] }]));
  await page.goto("/feeds", { waitUntil: "load" });
  await menuButton().waitFor({ timeout: 15000 });

  await step(`${tag} O1`, async () => {
    const edit = page.getByRole("button", { name: "Edit", exact: true });
    const add = page.getByRole("button", { name: "Add feed", exact: true });
    const done = page.getByRole("button", { name: "Done editing", exact: true });
    check((await edit.isVisible()) && (await add.isVisible()) && (await menuButton().isVisible()), `${tag} O1 plain`, "Edit, Add feed and the menu show", "one of Edit, Add feed or the menu is missing");
    check((await sideways()) <= 0, `${tag} O1 plain width`, "nothing scrolls sideways", `the page is ${await sideways()}px wider than the window`);
    for (const b of [edit, add, menuButton()]) check(inWindow(await b.boundingBox()), `${tag} O1 plain fit`, "the control is inside the window", "a header control runs past the window");
    await t.shot(page, `${tag}-O1-plain`);

    await edit.click();
    await done.waitFor({ timeout: 5000 });
    check((await page.getByTestId("saved-note").count()) === 1, `${tag} O1 edit note`, "edit mode has the saved note", "the saved note is missing");
    check((await add.count()) === 0 && (await edit.count()) === 0, `${tag} O1 edit hides`, "Add feed and Edit are hidden while editing", "Add feed or Edit still shows in edit mode");
    check((await menuButton().isVisible()) && inWindow(await done.boundingBox()) && inWindow(await menuButton().boundingBox()), `${tag} O1 edit fit`, "Done editing and the menu are inside the window", "Done editing or the menu is missing or past the window");
    check((await sideways()) <= 0, `${tag} O1 edit width`, "nothing scrolls sideways in edit mode", `the page is ${await sideways()}px wider than the window`);
    await t.shot(page, `${tag}-O1-edit`);

    await done.click();
    await edit.waitFor({ timeout: 5000 });
    check(await add.isVisible(), `${tag} O1 back`, "Done restores Edit and Add feed", "Add feed did not come back");

    // The keyboard path: Edit and Done are one button, so the focus is still on it after each press (a click does not
    // focus a button in every engine, so this uses Enter).
    const focused = (loc) => loc.evaluate((el) => el === el.ownerDocument.activeElement).catch(() => false);
    await edit.focus();
    await page.keyboard.press("Enter");
    await done.waitFor({ timeout: 5000 });
    check(await focused(done), `${tag} O1 focus edit`, "the focus is on Done editing after Edit is pressed", "the focus left the button when edit mode started");
    await page.keyboard.press("Enter");
    await edit.waitFor({ timeout: 5000 });
    check(await focused(edit), `${tag} O1 focus done`, "the focus is on Edit after Done editing is pressed", "the focus left the button when edit mode ended");

    await menuButton().click();
    await page.getByRole("menuitem", { name: "Select feeds", exact: true }).click();
    const selectDone = page.getByRole("button", { name: "Done", exact: true });
    await selectDone.waitFor({ timeout: 5000 });
    check((await edit.count()) === 0 && (await add.count()) === 0, `${tag} O1 select`, "Done alone shows while selecting", "Edit or Add feed shows while selecting");

    // O5 shares the select mode.
    const bar = page.getByRole("region", { name: "Selected feeds" });
    await bar.waitFor({ timeout: 5000 });
    const names = ["Select all", "Move", "Delete"];
    const boxes = [];
    for (const n of names) boxes.push(await bar.getByRole("button", { name: n, exact: true }).boundingBox());
    check(boxes.every(Boolean) && boxes.every(inWindow), `${tag} O5 fit`, "Select all, Move and Delete are inside the window", `the buttons' boxes: ${JSON.stringify(boxes)}`);
    // The bar wraps by design. One row is only checked where the width leaves room for any font: on the phone it depends on
    // the system font (the CI machine's is wider than most phones'), and the fit check above is what must hold there.
    if (tag === "desktop") check(boxes.every((b) => b && Math.abs(b.y - boxes[0].y) <= 2), `${tag} O5 row`, "Select all, Move and Delete sit on one row", `their tops: ${boxes.map((b) => b && Math.round(b.y))}`);
    const look = (n) => bar.getByRole("button", { name: n, exact: true }).evaluate((el) => {
      const s = getComputedStyle(el);
      return [s.color, s.backgroundColor, s.borderTopColor].join("|");
    });
    check((await look("Delete")) !== (await look("Move")), `${tag} O5 danger`, "Delete is drawn differently from Move", "Delete looks the same as Move");
    await t.shot(page, `${tag}-O5-bar`);
    await selectDone.click();
    await edit.waitFor({ timeout: 5000 });
  });

  await step(`${tag} O2`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await menuButton().click();
    const menu = page.getByRole("menu");
    await menu.waitFor({ timeout: 5000 });
    for (const g of ["Organize", "Files"]) check(await menu.getByText(g, { exact: true }).isVisible(), `${tag} O2 group ${g}`, `the menu has the ${g} group`, `the ${g} group is missing`);
    for (const n of ["Select feeds", "New folder", "Import feeds from a file (OPML)", "Export feeds to a file (OPML)", "Feed health"]) {
      check((await menu.getByRole("menuitem", { name: n, exact: true }).count()) === 1, `${tag} O2 ${n}`, `the menu has "${n}"`, `"${n}" is not in the menu once`);
    }
    check((await menu.getByRole("menuitem", { name: /move buttons/i }).count()) === 0, `${tag} O2 no move buttons`, "no Show move buttons item in the menu", "the menu still has a move buttons item");
    check(inWindow(await menu.boundingBox()), `${tag} O2 fit`, "the menu is inside the window", "the menu runs past the window");
    await t.shot(page, `${tag}-O2-menu`);
    await page.keyboard.press("Escape");
    await menu.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} O3`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const show = page.getByRole("button", { name: "Show move buttons", exact: true });
    await show.waitFor({ timeout: 5000 });
    const moves = page.getByRole("button", { name: /^Move .* (up|down)$/ });
    const before = await moves.count();
    await show.click();
    await page.getByRole("button", { name: "Hide move buttons", exact: true }).waitFor({ timeout: 5000 });
    const shown = await moves.count();
    check(before === 0 && shown > 0, `${tag} O3 shows`, `Show move buttons adds the ${shown} move buttons`, `move buttons went from ${before} to ${shown}`);
    await t.shot(page, `${tag}-O3-move-buttons`);
    await page.getByRole("button", { name: "Hide move buttons", exact: true }).click();
    await show.waitFor({ timeout: 5000 });
    check((await moves.count()) === 0, `${tag} O3 hides`, "Hide move buttons removes them again", `${await moves.count()} move buttons are still there`);
    await page.getByRole("button", { name: "Done editing", exact: true }).click();
  });

  await step(`${tag} O4`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await menuButton().click();
    await page.getByRole("menuitem", { name: "New folder", exact: true }).click();
    let dlg = page.getByRole("dialog");
    await dlg.getByRole("heading", { name: "New folder" }).waitFor({ timeout: 5000 });
    // The title has room above the first field (no description under it).
    const title = await dlg.getByRole("heading", { name: "New folder" }).boundingBox();
    const field = await dlg.getByRole("textbox", { name: "Name" }).boundingBox();
    check(!!title && !!field && field.y - (title.y + title.height) >= 8, `${tag} O4 room`, "the title has room above the Name field", `the gap is ${title && field ? Math.round(field.y - title.y - title.height) : "?"}px`);
    await t.shot(page, `${tag}-O4-new`);
    await dlg.getByRole("textbox", { name: "Name" }).fill(EMPTY);
    await dlg.getByRole("button", { name: "Create" }).click();
    await dlg.waitFor({ state: "detached", timeout: 10000 });

    const line = page.locator("main span.truncate").filter({ hasText: new RegExp("^\\s*" + EMPTY + "\\s*$") }).first();
    await line.waitFor({ timeout: 10000 });
    const row = await line.locator("xpath=..").innerText();
    check(/\bEmpty\b/.test(row.replace(EMPTY, "")), `${tag} O4 empty`, "the empty folder says Empty", `its line reads ${JSON.stringify(row)}`);
    const other = await page.locator("main span.truncate").filter({ hasText: new RegExp("^\\s*" + FOLDER + "\\s*$") }).first().locator("xpath=..").innerText();
    check(!/\bEmpty\b/.test(other.replace(FOLDER, "")), `${tag} O4 not empty`, "a folder with a feed does not say Empty", `its line reads ${JSON.stringify(other)}`);

    await page.getByRole("button", { name: `Folder actions for ${EMPTY}`, exact: true }).click();
    await page.getByRole("menuitem", { name: "Folder settings", exact: true }).click();
    dlg = page.getByRole("dialog");
    check(await dlg.getByRole("heading", { name: "Folder settings" }).isVisible(), `${tag} O4 settings`, "the folder's dialog is titled Folder settings", "the dialog has another title");
    await t.shot(page, `${tag}-O4-settings`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} O6`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await menuButton().click();
    await page.getByRole("menuitem", { name: "Import feeds from a file (OPML)", exact: true }).click();
    const dlg = page.getByRole("dialog");
    const file = dlg.getByLabel("OPML file");
    await file.waitFor({ timeout: 5000 });
    const h = (await file.boundingBox())?.height ?? 0;
    check(h >= 44, `${tag} O6 picker`, `the file picker is ${Math.round(h)}px tall`, `the file picker is ${Math.round(h)}px tall`);
    const days = (await dlg.innerText()).replace(/\s+/g, " ");
    check(/A number of days, for example 7\. Articles older than that arrive already read/.test(days), `${tag} O6 days`, "the days field says what it does", `the dialog reads ${JSON.stringify(days.slice(0, 300))}`);
    await t.shot(page, `${tag}-O6-import`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} O7`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Add feed", exact: true }).click();
    const dlg = page.getByRole("dialog");
    await dlg.getByRole("textbox", { name: /Feed or website address/ }).fill("https://example.com/feed.xml");
    const options = dlg.getByRole("button", { name: /^Options/ });
    await options.click();
    const titleBox = dlg.getByRole("textbox", { name: /^Title/ });
    await titleBox.fill("x".repeat(201));
    const warn = dlg.getByText("A title can be at most 200 characters; this one has 201.");
    check(await warn.isVisible(), `${tag} O7 warns`, "a too-long title is named", "no warning about the title");
    await options.click();
    check((await warn.isVisible()) && (await titleBox.isVisible()), `${tag} O7 stays open`, "Options stays open while the title is too long", "Options collapsed and hid the warning");
    check(await dlg.getByRole("button", { name: /^Add feed$/ }).isDisabled(), `${tag} O7 off`, "Add feed stays off", "Add feed is on with a too-long title");
    // Shortening the title with the cursor in it does not fold Options away.
    await titleBox.press("End");
    await titleBox.press("Backspace");
    check((await titleBox.isVisible()) && (await titleBox.inputValue()).length === 200 && (await options.getAttribute("aria-expanded")) === "true", `${tag} O7 shortened`, "Options stays open when the title is shortened to the limit", "Options collapsed under the focused Title field");
    await t.shot(page, `${tag}-O7-title`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} O11`, async () => {
    // The disposable feed is at an address that does not exist, so a refresh records a failed fetch for it.
    const before = await t.boot(page);
    const feed = before.feeds.find((f) => f.title === FEED);
    const folder = before.folders.find((f) => f.name === FOLDER);
    if (!feed || !folder) return t.fail(`${tag} O11`, "the disposable feed or folder is missing");
    let failing = null;
    for (let i = 0; i < 3 && !failing; i++) {
      await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
      failing = (await t.settle(page, (b) => b.feeds.some((f) => f.id === feed.id && f.last_error), i === 0 ? 30000 : 12000)).b.feeds.find((f) => f.id === feed.id && f.last_error) ?? null;
    }
    if (!failing) return t.fail(`${tag} O11`, "the feed never recorded a failed fetch");
    await page.goto(`/l/all?folder=${folder.id}`, { waitUntil: "load" });
    const note = page.getByTestId("feed-failure-note");
    await note.waitFor({ timeout: 15000 });
    const retry = note.getByTestId("feed-failure-retry");
    const arrived = await retry.getByText(/^Trying again/).waitFor({ timeout: 15000 }).then(() => true, () => false);
    const text = await t.textOf(note, 400);
    check(arrived, `${tag} O11 says`, `the folder's note says when it tries again (${JSON.stringify(text)})`, `the folder's note has no "Trying again" line (${JSON.stringify(text)})`);
    const box = await retry.boundingBox();
    check(!!box && box.height >= 16 && inWindow(box), `${tag} O11 visible`, "the retry line is visible and inside the window", `the retry line's box: ${JSON.stringify(box)}`);
    check(await retry.evaluate((el) => el.scrollHeight <= el.clientHeight + 1 && getComputedStyle(el).webkitLineClamp === "none"), `${tag} O11 whole`, "the retry line is not clipped", "the retry line is clamped");
    check(await note.locator("p").first().evaluate((el) => !el.textContent.includes("Trying again")), `${tag} O11 apart`, "the failure sentence does not carry the retry", "the retry time is inside the clamped sentence");
    await t.shot(page, `${tag}-O11-folder-note`);
  });

  await step(`${tag} O8`, async () => {
    await page.goto("/health", { waitUntil: "load" });
    const search = page.getByRole("searchbox", { name: "Search feeds" });
    await search.waitFor({ timeout: 15000 });
    await search.fill(FEED);
    const first = page.getByRole("table").getByRole("rowheader").filter({ hasText: FEED }).first();
    const phoneCard = vp.mobile;
    if (!phoneCard) {
      await first.waitFor({ timeout: 10000 });
      const x0 = (await first.boundingBox())?.x;
      await page.getByRole("button", { name: "Select", exact: true }).click();
      await page.getByRole("checkbox", { name: `Select ${FEED}` }).waitFor({ timeout: 5000 });
      const x1 = (await first.boundingBox())?.x;
      check(x0 !== undefined && x1 !== undefined && Math.abs(x1 - x0) <= 1, `${tag} O8 still`, "the feed names do not move when Select turns on", `the name column moved from ${x0} to ${x1}`);
      await page.getByRole("button", { name: "Done", exact: true }).click();
    }
  });

  await step(`${tag} O9`, async () => {
    await page.goto("/health", { waitUntil: "load" });
    const search = page.getByRole("searchbox", { name: "Search feeds" });
    await search.waitFor({ timeout: 15000 });
    await search.fill(FEED);
    await page.getByRole("button", { name: "Select", exact: true }).click();
    const bar = page.getByRole("region", { name: "Selected feeds" });
    await bar.waitFor({ timeout: 5000 });
    const retry = bar.getByRole("button", { name: "Retry now", exact: true });
    check(await retry.isDisabled(), `${tag} O9 off`, "Retry now is off with nothing ticked", "Retry now is on with nothing ticked");
    await page.getByRole("checkbox", { name: `Select ${FEED}` }).first().check();
    check(await retry.isEnabled(), `${tag} O9 on`, "Retry now is on once a feed is ticked", "Retry now is still off");
    const buttons = ["Select all", "Retry now", "Resume", "Pause", "Delete"];
    const boxes = [];
    for (const n of buttons) boxes.push(await bar.getByRole("button", { name: n, exact: true }).boundingBox());
    check(boxes.every(inWindow), `${tag} O9 fit`, "the five buttons are inside the window", `the buttons' boxes: ${JSON.stringify(boxes)}`);
    const rows = new Set(boxes.map((b) => Math.round((b?.y ?? 0) / 10)));
    console.log(`note ${tag} O9: the selection bar has ${buttons.length} buttons on ${rows.size} row(s)`);
    check((await sideways()) <= 0, `${tag} O9 width`, "nothing scrolls sideways", `the page is ${await sideways()}px wider than the window`);
    const look = (n) => bar.getByRole("button", { name: n, exact: true }).evaluate((el) => {
      const s = getComputedStyle(el);
      return [s.color, s.backgroundColor, s.borderTopColor].join("|");
    });
    check((await look("Delete")) !== (await look("Pause")), `${tag} O9 danger`, "Delete is drawn differently from Pause", "Delete looks the same as Pause");
    await t.shot(page, `${tag}-O9-bar`);
    await retry.click();
    const said = page.getByText(/^Retried 1 feed\./);
    check(await said.first().waitFor({ timeout: 30000 }).then(() => true, () => false), `${tag} O9 retried`, "Retry now says how many feeds it retried", "no \"Retried 1 feed.\" note within 30 s");
    console.log(`note ${tag} O9: ${JSON.stringify(await t.textOf(said.first()))}`);
    await t.shot(page, `${tag}-O9-retried`);
    await page.getByRole("button", { name: "Done", exact: true }).click();
  });

  await step(`${tag} O10`, async () => {
    await page.goto("/health", { waitUntil: "load" });
    await page.getByRole("searchbox", { name: "Search feeds" }).waitFor({ timeout: 15000 });
    await page.evaluate(AXE);
    const violations = () =>
      page.evaluate(() =>
        window.axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa"] } }).then((r) => r.violations.map((v) => `${v.id}: ${v.nodes.slice(0, 2).map((n) => n.html.slice(0, 100)).join(" | ")}`)),
      );
    let bad = await violations();
    check(bad.length === 0, `${tag} O10 plain`, "axe finds nothing on Feed health", `axe: ${bad.join(" ;; ")}`);
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await page.getByRole("region", { name: "Selected feeds" }).waitFor({ timeout: 5000 });
    bad = await violations();
    check(bad.length === 0, `${tag} O10 select`, "axe finds nothing on Feed health with Select on", `axe: ${bad.join(" ;; ")}`);
    await page.getByRole("button", { name: "Done", exact: true }).click();
  });
});

await t.finish("feeds organize");
