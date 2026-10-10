// UAT: the screens people reach with an old or repeated action: a duplicate saved search and out-of-date addresses.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:states                 in another, once the feeds have fetched
//   npm run uat:states -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   Q1  saves a search, then saves the same search again under another name: the second is refused with a plain
//       message in the dialog and the list of saved searches still has one;
//   U1  opens the list address of a feed that was deleted, and of one that never existed: each says the feed no
//       longer exists, with a way back to Unread and not an empty list;
//   U2  the same for a deleted folder;
//   U3  opens an article address that is gone: it says so and offers the list;
//   U4  opens a path nothing answers: "Page not found" with a way to Unread.
// Disposable feeds point at an address that never resolves, so nothing is fetched. A run that stopped half-way is
// cleaned up by the next one. It only runs against a loopback address with the seed's credentials unless
// --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step, textOf } = t;

const savedSearches = async (page) => (await (await page.request.get("/api/saved-searches", { params: { counts: 0 } })).json()).saved_searches ?? [];
const dropSearches = async (page) => {
  for (const s of (await savedSearches(page)).filter((x) => x.name.startsWith("UAT "))) await page.request.delete(`/api/saved-searches/${s.id}`, { headers: t.WRITE }).catch(() => {});
};

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await dropSearches(page);
  try {
    // Q1
    await page.goto("/search?q=the", { waitUntil: "load" });
    const save = page.getByRole("button", { name: "Save this search" });
    await save.waitFor({ timeout: 15000 });
    if (!(await save.isEnabled({ timeout: 10000 }).catch(() => false))) return t.setupError(`${tag}: the seed's feeds have no article matching "the" yet (have they fetched?)`);
    const saveAs = async (name) => {
      await save.click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("textbox", { name: "Name" }).fill(name);
      await dialog.getByRole("button", { name: "Save", exact: true }).click();
      return dialog;
    };
    let dialog = await saveAs(`UAT first ${tag}`);
    check(await dialog.waitFor({ state: "detached", timeout: 10000 }).then(() => true, () => false), `${tag} Q1 first`, "the first save closes the dialog", "the dialog stayed open");
    dialog = await saveAs(`UAT second ${tag}`);
    const refusal = dialog.getByText(/already saved/i);
    check(await refusal.waitFor({ timeout: 10000 }).then(() => true, () => false), `${tag} Q1 refused`, "the duplicate is refused in the dialog", `no refusal in "${await textOf(dialog)}"`);
    await t.shot(page, `${tag}-saved-search-duplicate`);
    await page.keyboard.press("Escape");
    const mine = (await savedSearches(page)).filter((s) => s.name.startsWith("UAT "));
    check(mine.length === 1, `${tag} Q1 count`, "one saved search, not two", `${mine.length} saved searches`);
  } catch (e) {
    t.fail(`${tag} Q1`, `stopped: ${String(e.message).split("\n").slice(0, 2).join(" ")}`);
  } finally {
    await dropSearches(page);
  }

  // U1 and U2: a feed and a folder that existed and were deleted, and ids that never did.
  await step(`${tag} U`, async () => {
  await t.importOpml(page, t.opml([{ folder: `UAT Stale ${tag}`, feeds: [1] }]));
  const seeded = await t.settle(page, (b) => b.feeds.some((f) => f.title === "UAT feed 1") && b.folders.some((f) => f.name === `UAT Stale ${tag}`));
  if (!seeded.ok) return t.fail(`${tag} U1`, "the disposable feed was not imported");
  const feedId = seeded.b.feeds.find((f) => f.title === "UAT feed 1").id;
  const folderId = seeded.b.folders.find((f) => f.name === `UAT Stale ${tag}`).id;
  for (const id of [feedId]) await page.request.delete(`/api/feeds/${id}`, { headers: t.WRITE });
  await page.request.delete(`/api/folders/${folderId}`, { headers: t.WRITE });
  const gone = async (where, path, title) => {
    await page.goto(path, { waitUntil: "load" });
    const heading = page.getByText(title, { exact: true });
    check(await heading.waitFor({ timeout: 15000 }).then(() => true, () => false), `${tag} ${where}`, `says "${title}"`, `no "${title}" at ${path} (page text: ${await textOf(page.locator("body"))})`);
    const back = page.getByRole("link", { name: "Go to Unread" });
    check(await back.isVisible().catch(() => false), `${tag} ${where} way back`, "a Go to Unread link is offered", "no way back to Unread");
    await t.shot(page, `${tag}-${where.replace(/\s/g, "-")}`);
  };
  await gone("U1 deleted feed", `/l/unread?feed=${feedId}`, "This feed no longer exists");
  await gone("U1 unknown feed", "/l/unread?feed=00000000-0000-0000-0000-000000000000", "This feed no longer exists");
  await gone("U2 deleted folder", `/l/unread?folder=${folderId}`, "This folder no longer exists");

  // U3
  await page.goto("/i/00000000-0000-0000-0000-000000000000", { waitUntil: "load" });
  check(await page.getByText("This article is no longer available", { exact: true }).waitFor({ timeout: 15000 }).then(() => true, () => false), `${tag} U3`, "a gone article says so", "no 'no longer available' message");
  await t.shot(page, `${tag}-U3-article-gone`);

  // U4
  await gone("U4 unknown path", "/no-such-screen", "Page not found");
  });
});

await t.finish("saved search and stale address");
