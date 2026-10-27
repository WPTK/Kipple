// UAT: the list's loading placeholder waits, then takes each layout's own row shape (#612).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/skeletons.mjs             in another, once the feeds have fetched
//   node uat/skeletons.mjs --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) and each row layout, with the list's answer held:
//   K1  nothing is drawn for the first moments (no placeholder flashes for a quick answer);
//   K2  a placeholder then appears, made of that layout's rows (taller for Editorial than for Headlines);
//   K3  once the answer lands the placeholder is gone and the real rows are there.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0
// clean, 1 findings, 2 setup error.
import { poll, start } from "./common.mjs";

// Playwright cannot hold a request the service worker makes, so it is blocked here (common.mjs, options.serviceWorkers).
const t = await start(import.meta.url, 10, { serviceWorkers: "block" });
const { check, step } = t;

const LAYOUTS = ["headlines", "compact", "inbox", "cards", "magazine"];
const heights = {};

/** The layout is a device setting kept on the server, so it is set there and the page loaded again. */
async function setLayout(page, id) {
  await page.goto("/l/unread", { waitUntil: "load" });
  const status = await page.evaluate((layout) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify({ "client.layout": layout }) }).then((r) => r.status), id);
  if (status !== 200) throw new Error(`the layout was not set (${status})`);
}

await t.eachViewport(async (page, vp) => {
  for (const layout of LAYOUTS) {
    const tag = `${vp.id} ${layout}`;
    await step(tag, async () => {
      await setLayout(page, layout);
      let release;
      const held = new Promise((r) => (release = r));
      await page.route("**/api/items?*", async (route) => {
        await held;
        await route.continue();
      });
      try {
        const asked = page.waitForRequest((r) => /\/api\/items\?/.test(r.url()), { timeout: 15000 });
        asked.catch(() => {});
        await page.goto("/l/unread", { waitUntil: "load" });
        await asked;
        await poll(120);
        const early = await page.locator("[data-skeleton]").count();
        check(early === 0, `${tag} K1`, "no placeholder in the first moments of a load", `${early} placeholder(s) at 120 ms`);
        const sk = page.locator("[data-skeleton]").first();
        await sk.waitFor({ timeout: 5000 });
        const h = await sk.evaluate((e) => e.firstElementChild.nextElementSibling?.getBoundingClientRect().height ?? 0);
        heights[`${vp.id}:${layout}`] = h;
        check(h > 20, `${tag} K2`, "the placeholder is made of visible rows", `first row ${Math.round(h)} px`);
        await t.shot(page, `${tag}-loading`);
      } finally {
        release();
      }
      await page.locator("[data-item-id]").first().waitFor({ timeout: 15000 });
      await page.waitForFunction(() => !document.querySelector("[data-skeleton]"), null, { timeout: 5000 });
      check(true, `${tag} K3`, "the placeholder gives way to the real rows", "");
      await page.unroute("**/api/items?*");
    });
  }
  const g = (l) => heights[`${vp.id}:${l}`] ?? 0;
  check(g("headlines") < g("magazine") && g("compact") < g("magazine"), `${vp.id} shapes`, "Editorial's placeholder row is taller than Headlines' and Compact's", `headlines ${Math.round(g("headlines"))}, compact ${Math.round(g("compact"))}, inbox ${Math.round(g("inbox"))}, cards ${Math.round(g("cards"))}, editorial ${Math.round(g("magazine"))} px`);
});

await t.finish("skeletons");
