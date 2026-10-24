// UAT: the loading placeholders of the Account and Devices page and of the reading pane do not move what is around them.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:state-remainders       in another, once the feeds have fetched
//   npm run uat:state-remainders -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   S1  the devices list loads: while the answer is held the placeholder is one box, and when it arrives the section
//       grows by no more than one more device (the placeholder holds the name form, this device and one other);
//   S2  desktop: the reading pane's "Select an article to read it here." sits where it does once the list has loaded,
//       whether the list answer is held or not.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0
// clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;

  await step(`${tag} S1`, async () => {
    let release;
    const held = new Promise((r) => (release = r));
    await page.route("**/api/devices", async (route) => {
      await held;
      await route.continue();
    });
    await page.goto("/settings/account", { waitUntil: "load" });
    const sk = page.locator("[data-skeleton]").filter({ hasText: "Loading devices" });
    await sk.waitFor({ timeout: 15000 });
    const sectionH = (loc) => loc.evaluate((e) => e.parentElement.getBoundingClientRect().height);
    const loading = await sectionH(sk);
    await t.shot(page, `${tag}-devices-loading`);
    release();
    const list = page.getByRole("list", { name: "Your devices" });
    await list.waitFor({ timeout: 15000 });
    await page.waitForTimeout(400);
    const loaded = await sectionH(list);
    const oneDevice = await list.locator("> li").last().evaluate((e) => e.getBoundingClientRect().height + 8);
    check(loaded - loading <= oneDevice, `${tag} S1`, "the section grows by no more than one device when the list arrives", `${Math.round(loading)} px while loading, ${Math.round(loaded)} px loaded (one device is ${Math.round(oneDevice)} px)`);
    await page.unroute("**/api/devices");
  });

  if (vp.width < 900) return;
  await step(`${tag} S2`, async () => {
    const place = page.getByText("Select an article to read it here.");
    await page.goto("/l/unread", { waitUntil: "load" });
    await place.waitFor({ timeout: 15000 });
    await page.waitForTimeout(400);
    const settled = await place.boundingBox();
    let release;
    const held = new Promise((r) => (release = r));
    await page.route("**/api/items?*", async (route) => {
      await held;
      await route.continue();
    });
    await page.goto("/l/all", { waitUntil: "load" });
    await page.waitForTimeout(600);
    const first = (await place.count()) ? await place.boundingBox() : null;
    release();
    await place.waitFor({ timeout: 15000 });
    await page.waitForTimeout(400);
    const after = await place.boundingBox();
    const moved = first ? Math.abs(first.y - after.y) + Math.abs(first.x - after.x) : 0;
    check(moved < 1 && Math.abs(after.y - settled.y) < 1, `${tag} S2`, "the placeholder does not move when the list loads", `moved ${Math.round(moved)} px (${first ? "shown while loading" : "not shown while loading"})`);
    await page.unroute("**/api/items?*");
  });
});

await t.finish("state remainders");
