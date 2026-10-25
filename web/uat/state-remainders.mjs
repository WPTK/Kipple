// UAT: the loading placeholders of the Account and Devices page and of the reading pane do not move what is around them.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:state-remainders       in another, once the feeds have fetched
//   npm run uat:state-remainders -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   S1  the devices list loads: while the answer is held the placeholder is a visible box as tall as the name form, this
//       device and one other, so the section grows only by the devices after the first other (and a placeholder that
//       is oversized, shrinking or hidden fails);
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
    // Hidden: the placeholder is a box of its own with a visible pulsing block over it.
    const shown = await sk.evaluate((e) => {
      const pulse = e.querySelector(".animate-pulse");
      const box = e.getBoundingClientRect();
      const cs = pulse ? getComputedStyle(pulse) : null;
      return { height: box.height, pulse: !!pulse && cs.visibility === "visible" && cs.display !== "none" && Number(cs.opacity) > 0 && pulse.getBoundingClientRect().height >= box.height - 1 };
    });
    const loading = await sectionH(sk);
    await t.shot(page, `${tag}-devices-loading`);
    release();
    const list = page.getByRole("list", { name: "Your devices" });
    await list.waitFor({ timeout: 15000 });
    await page.waitForTimeout(400);
    const loaded = await sectionH(list);
    const rows = await list.locator("> li").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().height));
    const others = rows.length - 1;
    // The placeholder holds the name form, this device and one other: the section then grows by one device for each
    // other device after the first, and by nothing when there is exactly one.
    const unit = rows.length > 1 ? rows[rows.length - 1] + 8 : 0;
    const expected = loaded - Math.max(0, others - 1) * unit;
    check(shown.height >= 100 && shown.pulse, `${tag} S1 shown`, "the devices placeholder is a visible box of real height", `${Math.round(shown.height)} px tall, pulsing block ${shown.pulse ? "visible" : "hidden or too small"}`);
    check(others >= 1 ? Math.abs(loading - expected) <= 4 : loading >= loaded - 1, `${tag} S1`, "the placeholder is as tall as the name form, this device and one other (not oversized, not shrinking)", `${Math.round(loading)} px while loading, ${Math.round(loaded)} px loaded with ${others} other device(s), expected ${Math.round(expected)} px`);    await page.unroute("**/api/devices");
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
