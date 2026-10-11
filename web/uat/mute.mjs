// UAT: "Mute similar..." mutes the article it was started from when the saved filter matches it.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:mute                   in another, once the feeds have fetched
//   npm run uat:mute -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and, on four unread articles:
//   M1  an unread article, from its row menu in the list: tap a suggestion, Save; the toast says it is muted and the
//       server has it read and muted by the new filter; its row leaves the Unread list at once and after a reload;
//   M2  a read article opened from its feed's All list, from the article's More actions: the same, it stays read,
//       and Next still opens the next article although this one has left the list;
//   M3  a starred article: the filter is saved, the toast says it is starred and was not muted, and it is still starred;
//   M4  an unread article whose rule is edited to a word not in it: the filter is saved, the toast says the filter no
//       longer matches it, and the article is unchanged;
//   M5  Restore on M2's article in Muted: the toast says it stays read, and the server has it read and not muted.
// Afterwards it deletes the filters it made with unmute=unread and puts every article back as it found it. It only runs
// against a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0 clean, 1
// findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step, textOf } = t;
const JSON_ACCEPT = { Accept: "application/json" };

const filterIds = async (page) => ((await (await page.request.get("/api/filters", { headers: JSON_ACCEPT })).json()).filters ?? []).map((f) => f.id);
const item = async (page, id) => (await page.request.get(`/api/items/${id}`, { headers: JSON_ACCEPT })).json();
const setRead = (page, id, read) => page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [id], read } });
const setStar = (page, id, starred) => page.request.put(`/api/items/${id}/star`, { headers: t.WRITE, data: { starred } });

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const list = await (await page.request.get("/api/items", { params: { view: "unread", limit: 40 }, headers: JSON_ACCEPT })).json();
  // Articles whose title offers a word to tap, so the rule has something of the article in it.
  const picks = (list.items ?? []).filter((i) => /\p{L}{5,}/u.test(i.title ?? "")).slice(0, 4);
  if (picks.length < 4) return t.setupError(`${tag}: the seed has fewer than four unread articles with a word to suggest (have the feeds fetched?)`);
  const [unread, read, starred, edited] = picks;
  const before = new Set(await filterIds(page));
  await setRead(page, read.id, true);
  await setStar(page, starred.id, true);

  /** Opens "Mute similar..." for the article, taps the first suggestion and returns the dialog. */
  const openEditor = async (it, fromRow) => {
    if (fromRow) {
      await page.goto(`/l/unread?feed=${it.feed_id}`, { waitUntil: "load" });
      const row = page.locator(`[data-item-id="${it.id}"]`).first();
      await row.waitFor({ timeout: 15000 });
      await row.getByRole("button", { name: "More actions" }).click();
    } else {
      // Opened from the feed's All list, as a reader would, so the article has a list to page through.
      await page.goto(`/l/all?feed=${it.feed_id}`, { waitUntil: "load" });
      const row = page.locator(`[data-item-id="${it.id}"]`).first();
      await row.waitFor({ timeout: 15000 });
      await row.locator("a").first().click();
      await page.waitForURL(new RegExp(`/i/${it.id}\\b`), { timeout: 10000 });
      await page.getByRole("toolbar", { name: "Article actions" }).getByRole("button", { name: "More actions" }).click();
    }
    await page.getByRole("menuitem", { name: "Mute similar…" }).click();
    const dialog = page.getByRole("dialog", { name: "New filter" });
    await dialog.waitFor({ timeout: 10000 });
    await dialog.getByRole("region", { name: "Suggestions from this article" }).getByRole("button").first().click();
    return dialog;
  };
  const save = async (dialog, where, message) => {
    await dialog.getByRole("button", { name: "Save filter" }).click();
    const closed = await dialog.waitFor({ state: "detached", timeout: 10000 }).then(() => true, () => false);
    check(closed, `${tag} ${where} saved`, "Save closes the editor", `the editor stayed open: ${await textOf(dialog)}`);
    const toast = page.getByText(message).first();
    check(await toast.waitFor({ timeout: 10000 }).then(() => true, () => false), `${tag} ${where} toast`, `says "${message}"`, `no "${message}" (page: ${await textOf(page.locator("body"), 400)})`);
    await t.shot(page, `${tag}-${where}`);
  };

  try {
    await step(`${tag} M1`, async () => {
      await save(await openEditor(unread, true), "M1", "Filter saved. This article is muted.");
      const it = await item(page, unread.id);
      check(it.read === true && !!it.muted_by, `${tag} M1 state`, "read and muted", `read=${it.read} muted_by=${it.muted_by}`);
      // The editor was opened from this Unread list, so the row must already be gone, and stay gone on a reload.
      const row = () => page.locator(`[data-item-id="${unread.id}"]`);
      const left = await row().first().waitFor({ state: "detached", timeout: 5000 }).then(() => true, () => false);
      check(left, `${tag} M1 list`, "the row leaves Unread", "the muted row is still in the Unread list");
      await page.goto(`/l/unread?feed=${unread.feed_id}`, { waitUntil: "load" });
      await page.getByRole("main").waitFor({ timeout: 10000 }).catch(() => {});
      await page.waitForTimeout(1000);
      check((await row().count()) === 0, `${tag} M1 reload`, "the row stays out of Unread after a reload", "the muted row came back after a reload");
    });
    await step(`${tag} M2`, async () => {
      await save(await openEditor(read, false), "M2", "Filter saved. This article is muted.");
      const it = await item(page, read.id);
      check(it.read === true && !!it.muted_by, `${tag} M2 state`, "muted and still read", `read=${it.read} muted_by=${it.muted_by}`);
      // The article left its list; Next still goes on from where it was (the seed's feeds hold more than four articles).
      await page.getByRole("button", { name: "Next article" }).first().click({ timeout: 5000 }).catch(() => {});
      const moved = await page.waitForURL((u) => /^\/i\/\d+/.test(u.pathname) && !u.pathname.endsWith(`/${read.id}`), { timeout: 5000 }).then(() => true, () => false);
      check(moved, `${tag} M2 paging`, "Next opens the next article", `still on ${page.url()}`);
    });
    await step(`${tag} M3`, async () => {
      await save(await openEditor(starred, false), "M3", "Filter saved. This article is starred, so it was not muted.");
      const it = await item(page, starred.id);
      check(it.starred === true && !it.muted_by, `${tag} M3 state`, "still starred, not muted", `starred=${it.starred} muted_by=${it.muted_by}`);
    });
    await step(`${tag} M4`, async () => {
      const dialog = await openEditor(edited, true);
      // Take the tapped word out again and type one the article does not have.
      await dialog.getByRole("list", { name: "Terms in this filter" }).getByRole("button").first().click();
      const words = dialog.getByRole("textbox", { name: "Words or phrases" });
      await words.fill("zzqxuatnomatch");
      await words.press("Enter");
      await save(dialog, "M4", "Filter saved. This article was not muted because the filter no longer matches it.");
      const it = await item(page, edited.id);
      check(!it.muted_by, `${tag} M4 state`, "not muted", `muted_by=${it.muted_by}`);
    });
    await step(`${tag} M5`, async () => {
      await page.goto("/l/muted", { waitUntil: "load" });
      const row = page.locator(`[data-item-id="${read.id}"]`).first();
      await row.waitFor({ timeout: 15000 });
      // The "Muted by" strip with Restore sits under the row, not inside it, and is labelled with the title.
      await page.getByRole("button", { name: `Restore ${read.title}`, exact: true }).first().click();
      const message = "Restored. It stays read, as it was before it was muted.";
      const said = await page.getByText(message).first().waitFor({ timeout: 10000 }).then(() => true, () => false);
      check(said, `${tag} M5 toast`, `says "${message}"`, `no "${message}" (page: ${await textOf(page.locator("body"), 400)})`);
      await t.shot(page, `${tag}-M5`);
      const it = await item(page, read.id);
      check(it.read === true && !it.muted_by, `${tag} M5 state`, "read and not muted", `read=${it.read} muted_by=${it.muted_by}`);
    });
  } finally {
    for (const id of (await filterIds(page)).filter((id) => !before.has(id))) {
      await page.request.delete(`/api/filters/${id}?unmute=unread`, { headers: t.WRITE }).catch(() => {});
    }
    await setStar(page, starred.id, false).catch(() => {});
    for (const it of picks) await setRead(page, it.id, false).catch(() => {});
  }
});

await t.finish("Mute similar source article");
