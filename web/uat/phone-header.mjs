// UAT: the phone's header and back buttons (#420, #422, #424).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:phone-header           in another
//   npm run uat:phone-header -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the phone width of the run it checks:
//   L1  at 812x375 (landscape) and 568x320 the list header and the tab bar take less than 33% of the height (a feed's list at 568 wide wraps its switch to a second row: under 50%), the
//       Show pills sit on the title row, the tab bar is one row of icon and name, and nothing runs past the edge;
//   L2  at 375x812 the pills are still on a row of their own below the title, and the tabs are icon above name;
//   I1  the list header has four controls or fewer and no Refresh button; the actions menu has Refresh all feeds and
//       choosing it asks the server for a refresh;
//   B1  Your year, a Settings group and Feed health each go back with a chevron before the title (a control with no
//       text of its own, named Back to ..., at least 44 px) that goes to the parent, and no text link says Back to Stats;
//   R1  the Stats ranges are Week, Month, Year, Monthly and All, each on one line, at 375 and 320 wide;
//   D1  (desktop) the Refresh button is in the list header and the actions menu does not repeat it.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 17);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(600);
  };
  const feed = (await t.boot(page)).feeds[0];
  if (!feed) return t.fail(`${tag} setup`, "the seeded instance has no feed");
  const list = `/l/unread?feed=${feed.id}`;

  if (!vp.mobile) {
    await step(`${tag} D1`, async () => {
      await open("/l/unread");
      const header = page.locator("main header").first();
      check(await header.getByRole("button", { name: "Refresh all feeds" }).isVisible(), `${tag} D1 button`, "the list header has Refresh all feeds", "the list header has no Refresh all feeds");
      await header.getByRole("button", { name: "List actions" }).click();
      const items = await page.getByRole("menuitem").allInnerTexts();
      check(!items.some((s) => /Refresh/.test(s)), `${tag} D1 menu`, "the actions menu does not repeat Refresh", `the actions menu has ${items.join(", ")}`);
      await page.keyboard.press("Escape");
      await t.shot(page, `${tag}-list-header`);
    });
    return;
  }

  const rows = () =>
    page.evaluate(() => {
      const header = document.querySelector("main header");
      const h1 = header.querySelector("h1");
      const pills = header.querySelector('nav[aria-label="Show"]');
      const nav = [...document.querySelectorAll('nav[aria-label="Primary"]')].pop();
      const tabs = [...nav.querySelectorAll("a")].map((a) => a.getBoundingClientRect());
      const tabLink = nav.querySelector("a");
      return {
        header: Math.round(header.getBoundingClientRect().height),
        bar: Math.round(nav.getBoundingClientRect().height),
        win: window.innerHeight,
        sameRow: Math.abs(h1.getBoundingClientRect().top - pills.getBoundingClientRect().top) < h1.getBoundingClientRect().height,
        pillsBelow: pills.getBoundingClientRect().top >= h1.getBoundingClientRect().bottom - 1,
        tabRow: tabs.every((r) => Math.abs(r.top - tabs[0].top) < 1) && getComputedStyle(tabLink).flexDirection === "row",
        pastRight: [...header.querySelectorAll("button, a")].filter((b) => b.getClientRects().length && b.getBoundingClientRect().right > window.innerWidth + 0.5).map((b) => b.getAttribute("aria-label") || b.textContent),
      };
    });

  await step(`${tag} L1`, async () => {
    for (const [w, h] of [[812, 375], [568, 320]]) {
      await page.setViewportSize({ width: w, height: h });
      for (const path of ["/l/unread", list]) {
        await open(path);
        const m = await rows();
        const share = (m.header + m.bar) / m.win;
        const where = `${tag} L1 ${w}x${h} ${path.includes("feed") ? "feed" : "unread"}`;
        const said = `the header and tab bar take ${Math.round(share * 100)}% (${m.header} + ${m.bar} px of ${m.win})`;
        // A feed's list has the feed switch on the row too: on a 568 px screen the row does not hold it all and wraps.
        const limit = w < 700 && path.includes("feed") ? 0.5 : 0.33;
        check(share < limit, where, said, `${said}, ${Math.round(limit * 100)}% or more`);
        if (limit === 0.33) check(m.sameRow, `${where} pills`, "the Show pills are on the title row", "the Show pills are on a row below the title");
        check(m.tabRow, `${where} tabs`, "the tabs are one row of icon and name", "the tab icons sit above their names");
        check(m.pastRight.length === 0, `${where} edge`, "no header control runs past the screen", `past the screen edge: ${m.pastRight.join(", ")}`);
      }
      if (w === 812) await t.shot(page, `${tag}-landscape-list`);
    }
  });

  await step(`${tag} L2`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/l/unread");
    const m = await rows();
    check(m.pillsBelow, `${tag} L2`, "upright, the Show pills are on their own row", "upright, the Show pills are on the title row");
    check(!m.tabRow, `${tag} L2 tabs`, "upright, each tab is its icon above its name", "upright, the tabs are a row of icon and name");
    await t.shot(page, `${tag}-portrait-list`);
  });

  await step(`${tag} I1`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/l/unread");
    const header = page.locator("main header").first();
    const names = await header.getByRole("button").evaluateAll((bs) => bs.filter((b) => b.getClientRects().length).map((b) => b.getAttribute("aria-label") || b.textContent));
    check(names.length <= 4, `${tag} I1 count`, `${names.length} controls: ${names.join(", ")}`, `${names.length} controls: ${names.join(", ")}`);
    check(!names.some((n) => /Refresh/.test(n)), `${tag} I1 button`, "there is no Refresh button", "there is a Refresh button");
    await header.getByRole("button", { name: "List actions" }).click();
    const item = page.getByRole("menuitem", { name: "Refresh all feeds" });
    await item.waitFor({ timeout: 5000 });
    await t.shot(page, `${tag}-actions-menu`);
    const asked = page.waitForRequest((r) => r.method() === "POST" && new URL(r.url()).pathname === "/api/refresh", { timeout: 5000 }).then(() => true, () => false);
    await item.click();
    check(await asked, `${tag} I1 refresh`, "Refresh all feeds in the menu asks for a refresh", "Refresh all feeds in the menu sent no request");
  });

  await step(`${tag} B1`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    const cases = [
      ["/stats/wrapped", "Back to Stats", "/stats"],
      ["/settings/appearance", "Back to Settings", "/settings"],
      ["/health", "Back to Feeds", "/feeds"],
    ];
    for (const [path, name, to] of cases) {
      await open(path);
      const main = page.getByRole("main");
      const back = main.getByRole("link", { name, exact: true }).or(main.getByRole("button", { name, exact: true })).first();
      const text = await back.innerText().catch(() => null);
      check(text === "", `${tag} B1 ${path}`, `${name} is a chevron with no text`, text === null ? `no ${name} control` : `${name} shows "${text}"`);
      const box = await back.boundingBox();
      check(!!box && box.width >= 44 && box.height >= 44, `${tag} B1 ${path} size`, `${name} is at least 44 px`, `${name} is ${box ? `${Math.round(box.width)}x${Math.round(box.height)}` : "not on screen"}`);
      await back.click();
      await page.waitForURL((u) => u.pathname === to, { timeout: 5000 }).catch(() => {});
      check(new URL(page.url()).pathname === to, `${tag} B1 ${path} goes`, `${name} goes to ${to}`, `${name} went to ${new URL(page.url()).pathname}`);
    }
    await open("/stats/wrapped");
    const words = await page.getByRole("main").getByText("Back to Stats").count();
    check(words === 0, `${tag} B1 text`, "no text on screen says Back to Stats", "a text link says Back to Stats");
    await t.shot(page, `${tag}-your-year`);
  });

  await step(`${tag} R1`, async () => {
    for (const w of [375, 320]) {
      await page.setViewportSize({ width: w, height: 812 });
      await open("/stats");
      const radios = await page.getByRole("radio").evaluateAll((rs) => rs.map((r) => ({ name: (r.closest("label") ?? r).innerText.trim(), h: (r.closest("label") ?? r).getBoundingClientRect().height })));
      const labels = radios.map((r) => r.name);
      check(JSON.stringify(labels.slice(0, 5)) === JSON.stringify(["Week", "Month", "Year", "Monthly", "All"]), `${tag} R1 ${w} labels`, `the ranges are ${labels.slice(0, 5).join(", ")}`, `the ranges are ${labels.join(", ")}`);
      const tall = radios.slice(0, 5).filter((r) => r.h > radios[0].h + 4).map((r) => r.name);
      check(tall.length === 0, `${tag} R1 ${w} lines`, "every range is on one line", `taller than the rest (wrapped): ${tall.join(", ")}`);
      if (w === 375) await t.shot(page, `${tag}-stats-ranges`);
    }
  });
});

await t.finish("phone-header");
