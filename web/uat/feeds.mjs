// UAT: the Feeds screen in a real browser: selecting, bulk delete, folder dialogs and the OPML import.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:feeds                  in another, once the feeds have fetched
//   npm run uat:feeds -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and, on the Feeds screen:
//   F1  Select, then tap a feed's title (not only its checkbox): the feed is ticked, the count says 1 selected and
//       the address is still /feeds, so selecting never opens a feed; Done leaves select mode;
//   F2  bulk delete of disposable feeds in a folder with a subfolder: the dialog names the folders it will remove,
//       the one progress bar keeps one fixed total while it runs, a folder that still holds feeds stays, and the
//       folders left empty are gone afterwards, with the library otherwise untouched;
//   F3  New folder and Rename folder both save on Enter in the name field;
//   F4  Import OPML refuses a file that is not OPML, by its look (JSON named .json), on the server when it is
//       an RSS feed or when it is JSON named .opml, says so in plain words and adds nothing.
// Disposable feeds point at an address that never resolves, so nothing is fetched. A run that stopped half-way is
// cleaned up by the next one. It only runs against a loopback address with the seed's credentials unless
// --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 22);
const { check, step, textOf } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const root = `UAT Bulk ${tag}`;
  const child = `UAT Child ${tag}`;
  await page.goto("/feeds", { waitUntil: "load" });
  await page.getByRole("heading", { level: 1, name: "Feeds" }).waitFor({ timeout: 15000 });

  // F1
  const b0 = await t.boot(page);
  const real = b0.feeds.find((f) => !f.is_archive);
  if (!real) return t.setupError(`${tag}: the seed has no feeds`);
  await step(`${tag} F1`, async () => {
  await page.getByRole("button", { name: "Select", exact: true }).click();
  // The row on the Feeds screen itself (the desktop sidebar lists the same feed as a link, which this must not hit).
  const box = page.getByRole("checkbox", { name: `Select ${real.title}`, exact: true }).first();
  // A middle-click or a long-press "Open" would open a link in a new tab however the plain click is handled, so the
  // row must not be a link at all while selecting.
  const links = await page.getByRole("main").getByRole("link", { name: new RegExp(`^${real.title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) }).count();
  check(links === 0, `${tag} F1 no link`, "a feed row is not a link while selecting", `${links} link(s) to the feed on screen`);
  await page.getByRole("main").getByText(real.title, { exact: true }).first().click();
  check(new URL(page.url()).pathname === "/feeds", `${tag} F1 stays`, "still on /feeds (selecting never opens a feed)", `went to ${page.url()}`);
  check(await box.isChecked({ timeout: 2000 }).catch(() => false), `${tag} F1 tick`, "tapping the title ticks the feed", "the feed is not ticked");
  check(await page.getByText("1 selected").isVisible().catch(() => false), `${tag} F1 count`, "1 selected", "the count does not read 1 selected");
  await t.shot(page, `${tag}-feeds-select`);
  // After the plain click, whose result it must not disturb: an engine may treat a middle-click on a label as a click.
  const pages = page.context().pages().length;
  await page.getByRole("main").getByText(real.title, { exact: true }).first().click({ button: "middle" }).catch(() => {});
  await page.waitForTimeout(500);
  check(page.context().pages().length === pages, `${tag} F1 no tab`, "a middle-click opens nothing", "a middle-click opened a new tab");
  await page.getByRole("button", { name: "Done", exact: true }).click();
  check(await page.getByRole("button", { name: "Select", exact: true }).isVisible(), `${tag} F1 done`, "Done leaves select mode", "still selecting");
  });

  // F2: root holds feed 1; child holds feeds 2 and 3.
  await step(`${tag} F2`, async () => {
  await t.importOpml(page, t.opml([{ folder: root, feeds: [1], children: [{ folder: child, feeds: [2, 3] }] }]));
  await page.reload({ waitUntil: "load" });
  const seeded = await t.settle(page, (b) => b.feeds.filter((f) => f.title.startsWith("UAT feed ")).length === 3);
  if (!seeded.ok) return t.fail(`${tag} F2`, "the disposable feeds were not imported");
  const titleOf = (n) => `UAT feed ${n}`;
  const pick = async (...ns) => {
    await page.getByRole("button", { name: "Select", exact: true }).click();
    for (const n of ns) await page.getByRole("checkbox", { name: `Select ${titleOf(n)}`, exact: true }).first().click();
  };
  const progressSpy = () =>
    page.evaluate(() => {
      const seen = { max: new Set(), most: 0 };
      window.__progress = seen;
      new MutationObserver(() => {
        const bars = document.querySelectorAll("progress");
        seen.most = Math.max(seen.most, bars.length);
        bars.forEach((p) => seen.max.add(p.max));
      }).observe(document.body, { subtree: true, childList: true, attributes: true });
    });
  const deleteSelected = async (n) => {
    await page.getByRole("region", { name: "Selected feeds" }).getByRole("button", { name: "Delete", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("heading", { name: `Delete ${n} feed${n === 1 ? "" : "s"}?` }).waitFor({ timeout: 5000 });
    const text = await textOf(dialog, 2000);
    await progressSpy();
    await dialog.getByRole("button", { name: `Delete ${n} feed${n === 1 ? "" : "s"}`, exact: true }).click();
    await dialog.waitFor({ state: "detached", timeout: 15000 });
    return { text, spy: await page.evaluate(() => ({ max: [...window.__progress.max], most: window.__progress.most })) };
  };

  // The root's own feed alone: its subfolder still holds feeds, so the root stays.
  await pick(1);
  let r = await deleteSelected(1);
  check(!/deleted too/.test(r.text), `${tag} F2 one`, "no folder is announced while a subfolder still has feeds", `the dialog says "${r.text.slice(0, 160)}"`);
  let s = await t.settle(page, (b) => !b.feeds.some((f) => f.title === titleOf(1)));
  check(s.ok && s.b.folders.some((f) => f.name === root) && s.b.folders.some((f) => f.name === child), `${tag} F2 kept`, "both folders stay", "a folder with feeds below it went");
  check(r.spy.max.join() === "1" && r.spy.most <= 1, `${tag} F2 bar one`, "one progress bar with total 1", `bars: ${r.spy.max.length ? `totals ${r.spy.max.join(",")}` : "none shown"}, at most ${r.spy.most} at once`);

  // The last two: the subfolder and then the root are left empty and go with them.
  const done = page.getByRole("button", { name: "Done", exact: true });
  if (await done.isVisible()) await done.click();
  await pick(2, 3);
  r = await deleteSelected(2);
  check(r.text.includes(child) || r.text.includes(root), `${tag} F2 names`, "the dialog names the folders it will remove", `the dialog says "${r.text.slice(0, 200)}"`);
  check(r.spy.max.join() === "2" && r.spy.most === 1, `${tag} F2 bar two`, "one progress bar with a fixed total of 2", `bars: ${r.spy.max.length ? `totals ${r.spy.max.join(",")}` : "none shown"}, at most ${r.spy.most} at once`);
  s = await t.settle(page, (b) => !b.feeds.some((f) => f.title.startsWith("UAT feed ")) && !b.folders.some((f) => f.name === root || f.name === child));
  check(s.ok, `${tag} F2 emptied`, "the feeds and the folders left empty are gone", "a disposable feed or folder is still there");
  check(s.b.feeds.length === b0.feeds.length && s.b.folders.length === b0.folders.length, `${tag} F2 rest`, "the rest of the library is untouched", `feeds ${b0.feeds.length} -> ${s.b.feeds.length}, folders ${b0.folders.length} -> ${s.b.folders.length}`);
  await t.shot(page, `${tag}-feeds-after-delete`);

  });

  // F3
  await step(`${tag} F3`, async () => {
  const named = `UAT Enter ${tag}`;
  const renamed = `UAT Renamed ${tag}`;
  await page.goto("/feeds", { waitUntil: "load" });
  await page.getByRole("button", { name: "Feed actions" }).click();
  await page.getByRole("menuitem", { name: "New folder" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "Name" }).fill(named);
  await dialog.getByRole("textbox", { name: "Name" }).press("Enter");
  check(await dialog.waitFor({ state: "detached", timeout: 10000 }).then(() => true, () => false), `${tag} F3 new`, "Enter creates the folder", "the dialog is still open after Enter");
  let s = await t.settle(page, (b) => b.folders.some((f) => f.name === named));
  check(s.ok, `${tag} F3 created`, "the folder exists", "no folder was created");
  if (!s.ok) {
    // Carry on to the rename with a folder made the other way, so one finding does not hide the next.
    await page.keyboard.press("Escape");
    await page.request.post("/api/folders", { data: { name: named }, headers: t.WRITE });
    await page.goto("/feeds", { waitUntil: "load" });
  }
  await page.getByRole("button", { name: `Folder actions for ${named}`, exact: true }).click();
  await page.getByRole("menuitem", { name: "Rename or set layout" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "Name" }).fill(renamed);
  await dialog.getByRole("textbox", { name: "Name" }).press("Enter");
  check(await dialog.waitFor({ state: "detached", timeout: 10000 }).then(() => true, () => false), `${tag} F3 rename`, "Enter saves the new name", "the dialog is still open after Enter");
  s = await t.settle(page, (b) => b.folders.some((f) => f.name === renamed) && !b.folders.some((f) => f.name === named));
  check(s.ok, `${tag} F3 renamed`, "the folder has its new name", "the folder was not renamed");

  });

  // F4
  await step(`${tag} F4`, async () => {
  await page.goto("/feeds", { waitUntil: "load" });
  const feedsBefore = (await t.boot(page)).feeds.length;
  const refuse = async (name, mimeType, body, wantText, label) => {
    await page.getByRole("button", { name: "Feed actions" }).click();
    await page.getByRole("menuitem", { name: "Import OPML" }).click();
    const dlg = page.getByRole("dialog");
    await dlg.getByLabel("OPML file").setInputFiles({ name, mimeType, buffer: Buffer.from(body) });
    if (label !== "look") await dlg.getByRole("button", { name: "Import", exact: true }).click();
    const note = dlg.getByText(wantText);
    check(await note.waitFor({ timeout: 10000 }).then(() => true, () => false), `${tag} F4 ${label}`, "a plain refusal is shown", `no message matching ${wantText} in "${await textOf(dlg)}"`);
    if (label === "look") check(await dlg.getByRole("button", { name: "Import", exact: true }).isDisabled(), `${tag} F4 button`, "Import stays off", "Import is on for a file refused by its look");
    await t.shot(page, `${tag}-opml-${label}`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  };
  await refuse("export.json", "application/json", '{"error":"origin"}', /doesn't look like an OPML file/, "look");
  await refuse("feed.xml", "text/xml", '<rss version="2.0"><channel/></rss>', /That is not an OPML file/, "server");
  await refuse("export.opml", "text/x-opml", '{"error":"origin"}', /damaged or cut short/, "damaged");
  check((await t.boot(page)).feeds.length === feedsBefore, `${tag} F4 nothing`, "nothing was imported", "a refused file changed the library");
  });
});

await t.finish("Feeds screen");
