// UAT: the phone's header and back buttons (#420, #422, #424).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:phone-header           in another
//   npm run uat:phone-header -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the phone width of the run it checks:
//   L1  at 812x375 (landscape) and 568x320 the list header and the tab bar take under a third of the height, for the
//       Unread list, a feed and a folder: the title, the Show control (the pills; a select where the screen is narrower
//       than 700 px) and the feed switch sit on one row, in a Muted view too (the extra Muted pill), the tab bar is one
//       row of icon and name 44 px tall, and nothing runs past the edge;
//   L2  at 375x812 the pills are still on a row of their own below the title, and the tabs are icon above name; a
//       phone held upright whose height shrinks to 420 px (a keyboard) keeps that layout;
//   L3  (Large targets on) at 812x375 and 568x320 the layout is the same row, with controls of 44 px;
//   L4  (text size Largest) at 568x320 and 320x568 no tab name is cut off, and at 320 wide Your year is shown whole;
//   I1  the list header has four controls or fewer and no Refresh button; the actions menu has Refresh all feeds, and (uat-labels: ignore)
//       choosing it asks the server for a refresh, and while it runs the menu button stays as it is and a line under the header shows it;
//   B1  Your year, a Settings group and Feed health each go back with a chevron before the title (a control with no (uat-labels: ignore)
//       text of its own, named Back to ..., at least 44 px) that goes to the parent, and no text link says Back to Stats;
//   B2  from the Settings list into a group the chevron goes back to the list with focus on the row just left; from (uat-labels: ignore)
//       Stats (off) through its Settings link the chevron lands on the screen its label names, and the system Back (uat-labels: ignore)
//       does not return to the group;
//   R1  the Stats ranges are Week, Month, Year, Monthly and All, each on one line, each on one line at 375 wide, and a select with the same five under 360;
//   D1  (desktop) the list header has five controls or fewer, no Refresh button and no Settings link; the actions menu has (uat-labels: ignore)
//       Refresh all feeds. (uat-labels: ignore)
//
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start, waitForStillFor } from "./common.mjs";

const t = await start(import.meta.url, 27, { serviceWorkers: "block" });
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  // A control that is not there fails its step in seconds (a step stops at its first error) instead of waiting out Playwright's 30 s.
  page.setDefaultTimeout(8000);
  // The page has stopped changing: main's size and the number of elements held for 200 ms (the Your year page keeps a
  // staggered entrance animation running, so "no animations" is not the signal).
  const laidOut = () =>
    waitForStillFor(page, 200, () =>
      page.evaluate(() => {
        const m = document.querySelector("main");
        const r = m?.getBoundingClientRect();
        return [Math.round(r?.width ?? 0), Math.round(r?.height ?? 0), m?.scrollHeight ?? 0, document.querySelectorAll("*").length, innerWidth, innerHeight];
      }),
      { label: "the page layout to stop changing" },
    );
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await laidOut();
  };
  const feed = (await t.boot(page)).feeds[0];
  if (!feed) return t.fail(`${tag} setup`, "the seeded instance has no feed");
  const list = `/l/unread?feed=${feed.id}`;

  if (!vp.mobile) {
    await step(`${tag} D1`, async () => {
      await open("/l/unread");
      const header = page.locator("main header").first();
      const names = await header.getByRole("button").evaluateAll((bs) => bs.filter((b) => b.getClientRects().length).map((b) => b.getAttribute("aria-label") || b.textContent));
      check(names.length <= 5, `${tag} D1 count`, `${names.length} controls: ${names.join(", ")}`, `${names.length} controls: ${names.join(", ")}`);
      check(!names.some((n) => /Refresh/.test(n)), `${tag} D1 button`, "the list header has no Refresh button", "the list header has a Refresh button");
      check((await header.getByRole("link", { name: "Settings" }).count()) === 0, `${tag} D1 settings`, "the list header has no Settings link", "the list header has a Settings link"); // uat-labels: ignore
      await header.getByRole("button", { name: "List actions" }).click();
      const items = await page.getByRole("menuitem").allInnerTexts();
      check(items.some((s) => /Refresh all feeds/.test(s)), `${tag} D1 menu`, "the actions menu has Refresh all feeds", `the actions menu has ${items.join(", ")}`); // uat-labels: ignore
      await page.keyboard.press("Escape");
      await t.shot(page, `${tag}-list-header`);
    });
    return;
  }

  const patchDevice = (body) =>
    page.evaluate((b) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) }).then((r) => r.status), body);
  const folder = (await t.boot(page)).folders[0];
  const scopes = [["unread", "/l/unread"], ["feed", list], ...(folder ? [["folder", `/l/unread?folder=${folder.id}`]] : []), ["muted feed", `/l/muted?feed=${feed.id}`]];

  /** What the header and the tab bar take, where the Show control and the feed switch are, and whether anything is cut off. */
  const rows = () =>
    page.evaluate(() => {
      const header = document.querySelector("main header");
      const h1 = header.querySelector("h1");
      const titleRow = h1.closest("[data-title-row]");
      const show = header.querySelector('nav[aria-label="Show"], select[aria-label="Show"]');
      const nav = [...document.querySelectorAll('nav[aria-label="Primary"]')].pop();
      const links = [...nav.querySelectorAll("a")];
      const tabs = links.map((a) => a.getBoundingClientRect());
      const range = document.createRange();
      const cut = links.filter((a) => {
        range.selectNodeContents(a);
        const text = range.getBoundingClientRect();
        const box = a.getBoundingClientRect();
        return text.left < box.left - 0.5 || text.right > box.right + 0.5;
      }).map((a) => a.textContent.trim());
      const shown = (e) => e.getClientRects().length > 0;
      return {
        header: Math.round(header.getBoundingClientRect().height),
        bar: Math.round(nav.getBoundingClientRect().height),
        win: window.innerHeight,
        titleRow: Math.round(titleRow.getBoundingClientRect().height),
        isSelect: show.tagName === "SELECT",
        sameRow: Math.abs(h1.getBoundingClientRect().top - show.getBoundingClientRect().top) < h1.getBoundingClientRect().height,
        showBelow: show.getBoundingClientRect().top >= h1.getBoundingClientRect().bottom - 1,
        switchOnRow: (() => { const g = header.querySelector('[role="group"]'); return !g || g.parentElement === titleRow; })(),
        tabRow: tabs.every((r) => Math.abs(r.top - tabs[0].top) < 1) && getComputedStyle(links[0]).flexDirection === "row",
        tabHeight: Math.round(Math.min(...tabs.map((r) => r.height))),
        cut,
        smallest: Math.round(Math.min(...[...header.querySelectorAll("button, a, select")].filter(shown).map((e) => e.getBoundingClientRect().height))),
        title: h1.scrollWidth > h1.clientWidth,
        pastRight: [...header.querySelectorAll("button, a, select")].filter((b) => shown(b) && b.getBoundingClientRect().right > window.innerWidth + 0.5).map((b) => b.getAttribute("aria-label") || b.textContent),
      };
    });

  await step(`${tag} L1`, async () => {
    for (const [w, h] of [[812, 375], [568, 320]]) {
      await page.setViewportSize({ width: w, height: h });
      for (const [name, path] of scopes) {
        await open(path);
        const m = await rows();
        const share = (m.header + m.bar) / m.win;
        const where = `${tag} L1 ${w}x${h} ${name}`;
        const said = `the header and tab bar take ${Math.round(share * 100)}% (${m.header} + ${m.bar} px of ${m.win})`;
        // A Muted view has a line of explanation under the title row (not a wrap): its title row is what is checked there.
        if (name !== "muted feed") check(share < 0.33, where, said, `${said}, a third or more`);
        check(m.titleRow < 56, `${where} row`, `the title row is one row (${m.titleRow} px)`, `the title row wraps (${m.titleRow} px)`);
        check(m.sameRow && m.switchOnRow, `${where} pills`, `the Show ${m.isSelect ? "select" : "pills"}${name === "unread" ? "" : " and the feed switch"} ${name === "unread" && m.isSelect ? "is" : "are"} on the title row`, "the Show control or the feed switch is on a row below the title");
        check(m.isSelect === w < 700, `${where} select`, `the Show control is ${m.isSelect ? "a select" : "the pills"}`, `the Show control is ${m.isSelect ? "a select" : "the pills"} at ${w} px wide`);
        check(m.tabRow && m.tabHeight >= 44 && m.bar <= 46, `${where} tabs`, `the tabs are one row of icon and name, ${m.bar} px tall`, `the tabs are not one 44 px row (${m.bar} px tall, row ${m.tabRow})`);
        check(m.pastRight.length === 0, `${where} edge`, "no header control runs past the screen", `past the screen edge: ${m.pastRight.join(", ")}`);
      }
      if (w === 812) await t.shot(page, `${tag}-landscape-list`);
      if (w === 568) await t.shot(page, `${tag}-landscape-568-feed`);
    }
  });

  await step(`${tag} L2`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/l/unread");
    const m = await rows();
    check(m.showBelow, `${tag} L2`, "upright, the Show pills are on their own row", "upright, the Show pills are on the title row");
    check(!m.tabRow, `${tag} L2 tabs`, "upright, each tab is its icon above its name", "upright, the tabs are a row of icon and name");
    await t.shot(page, `${tag}-portrait-list`);
    // The keyboard takes the height of an upright phone: the layout stays.
    await page.setViewportSize({ width: 375, height: 420 });
    await laidOut();
    const k = await rows();
    check(k.showBelow && !k.tabRow, `${tag} L2 keyboard`, "upright with the keyboard open (375x420), the layout stays", "upright with the keyboard open, the header and tabs switched to the sideways layout");
  });

  await step(`${tag} L3`, async () => {
    if ((await patchDevice({ "client.large_targets": true })) !== 200) return t.fail(`${tag} L3 setup`, "Large targets not set");
    try {
      for (const [w, h] of [[812, 375], [568, 320]]) {
        await page.setViewportSize({ width: w, height: h });
        for (const [name, path] of scopes.slice(0, 2)) {
          await open(path);
          const m = await rows();
          const where = `${tag} L3 ${w}x${h} ${name} with Large targets`;
          check(m.titleRow < 56 && m.tabRow && m.bar <= 46, where, `the same row layout: title row ${m.titleRow} px, tab bar ${m.bar} px`, `title row ${m.titleRow} px, tab bar ${m.bar} px, tabs in a row: ${m.tabRow}`);
          check(m.smallest >= 44 && m.tabHeight >= 44, `${where} targets`, `controls are at least 44 px (smallest ${m.smallest}, tabs ${m.tabHeight})`, `a control is under 44 px (smallest ${m.smallest}, tabs ${m.tabHeight})`);
          check(m.cut.length === 0, `${where} names`, "no tab name is cut off", `cut off: ${m.cut.join(", ")}`);
        }
      }
      await t.shot(page, `${tag}-large-targets-568`);
    } finally {
      await patchDevice({ "client.large_targets": false });
    }
  });

  await step(`${tag} L4`, async () => {
    if ((await patchDevice({ "client.text_size": 1.5 })) !== 200) return t.fail(`${tag} L4 setup`, "text size not set");
    try {
      for (const [w, h] of [[568, 320], [320, 568]]) {
        await page.setViewportSize({ width: w, height: h });
        for (const [name, path] of scopes.slice(0, 2)) {
          await open(path);
          const m = await rows();
          check(m.cut.length === 0, `${tag} L4 ${w}x${h} ${name}`, "at Largest no tab name is cut off", `at Largest these tab names are cut off: ${m.cut.join(", ")}`);
        }
      }
      await t.shot(page, `${tag}-largest-568-tabs`);
      await page.setViewportSize({ width: 320, height: 568 });
      await open("/stats/wrapped");
      const h1 = await page.getByRole("heading", { level: 1, name: "Your year" }).evaluate((e) => ({ cut: e.scrollWidth > e.clientWidth, w: Math.round(e.getBoundingClientRect().width) }));
      check(!h1.cut, `${tag} L4 Your year`, `at Largest and 320 wide Your year is shown whole (${h1.w} px)`, `Your year is cut off (${h1.w} px wide)`);
      await t.shot(page, `${tag}-largest-your-year`);
    } finally {
      await patchDevice({ "client.text_size": 1 });
    }
  });
  await step(`${tag} I1`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/l/unread");
    const header = page.locator("main header").first();
    const names = await header.getByRole("button").evaluateAll((bs) => bs.filter((b) => b.getClientRects().length).map((b) => b.getAttribute("aria-label") || b.textContent));
    check(names.length <= 4, `${tag} I1 count`, `${names.length} controls: ${names.join(", ")}`, `${names.length} controls: ${names.join(", ")}`);
    check(!names.some((n) => /Refresh/.test(n)), `${tag} I1 button`, "there is no Refresh button", "there is a Refresh button");
    await header.getByRole("button", { name: "List actions" }).click();
    const item = page.getByRole("menuitem", { name: "Refresh all feeds" }); // uat-labels: ignore
    await item.waitFor({ timeout: 5000 });
    await t.shot(page, `${tag}-actions-menu`);
    await page.keyboard.press("Escape");
    // The request is held back until the checks of the running state are done, so the refresh is still running when
    // the menu button is looked at.
    let release;
    const held = new Promise((r) => (release = r));
    try {
      await page.route("**/api/refresh", async (route) => {
        await held;
        await route.continue().catch(() => {});
      });
      const asked = page.waitForRequest((r) => r.method() === "POST" && new URL(r.url()).pathname === "/api/refresh", { timeout: 5000 }).then(() => true, () => false);
      await header.getByRole("button", { name: "List actions" }).click();
      await page.getByRole("menuitem", { name: "Refresh all feeds" }).click(); // uat-labels: ignore
      check(await asked, `${tag} I1 refresh`, "Refresh all feeds in the menu asks for a refresh", "Refresh all feeds in the menu sent no request"); // uat-labels: ignore
      await header.getByTestId("refresh-line").waitFor({ timeout: 3000 }).catch(() => {});
      check(await header.getByTestId("refresh-line").count() === 1, `${tag} I1 line`, "a thin line under the header shows the refresh", "nothing shows that a refresh is running");
      check(await header.getByRole("button", { name: "List actions" }).evaluate((b) => !b.querySelector("svg.animate-spin")), `${tag} I1 steady`, "the actions menu button does not change while the refresh runs", "the actions menu button changed into a spinner");
      await header.getByRole("button", { name: "List actions" }).focus();
      await page.keyboard.press("Enter");
      const busy = page.getByRole("menuitem", { name: "Refreshing" }); // uat-labels: ignore
      await busy.waitFor({ timeout: 5000 });
      check((await busy.getAttribute("aria-disabled")) === "true" && (await busy.evaluate((e) => e === document.activeElement)), `${tag} I1 busy`, "while it runs the item is reached by the keyboard and marked disabled", "while it runs the item is skipped by the keyboard or not marked disabled");
      await page.keyboard.press("Escape");
    } finally {
      release(); // a throwing step must not leave the held request hanging
      await page.unroute("**/api/refresh").catch(() => {});
    }
  });

  await step(`${tag} B1`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    const cases = [
      ["/stats/wrapped", "Back to Stats", "/stats"],
      ["/settings/appearance", "Back to Settings", "/settings"], // uat-labels: ignore
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

  await step(`${tag} B2`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    const pathOf = () => new URL(page.url()).pathname;
    const chevron = () => page.getByRole("main").getByRole("link", { name: /^Back to / }).first();
    await open("/settings");
    await page.getByRole("main").getByRole("link", { name: /^Appearance/ }).first().click();
    await page.waitForURL((u) => u.pathname === "/settings/appearance", { timeout: 5000 });
    await page.getByRole("main").getByRole("heading").first().waitFor({ timeout: 5000 });
    await chevron().click({ timeout: 5000 });
    await page.waitForURL((u) => u.pathname === "/settings", { timeout: 5000 }).catch(() => {});
    const focusText = () => page.evaluate(() => document.activeElement?.textContent?.trim().slice(0, 40) ?? "");
    await waitForStillFor(page, 200, focusText, { label: "the focused element to stop changing" }); // focus arrives on the row a frame or two after the route changes
    check(pathOf() === "/settings", `${tag} B2 list`, "the chevron in a group opened from the Settings list goes back to the list", `it went to ${pathOf()}`); // uat-labels: ignore
    const focused = await focusText();
    check(/^Appearance/.test(focused), `${tag} B2 focus`, "focus arrives on the row of the group just left", `focus is on "${focused}"`);

    const isSummary = (u) => u.pathname === "/api/stats/summary";
    await page.route(isSummary, (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ enabled: false }) }));
    await open("/stats");
    await page.getByRole("main").getByRole("link", { name: "Settings > Stats" }).click({ timeout: 5000 }); // uat-labels: ignore
    await page.waitForURL((u) => u.pathname === "/settings/statistics", { timeout: 5000 });
    const back = chevron();
    await back.waitFor({ timeout: 5000 });
    const label = (await back.getAttribute("aria-label")) ?? "";
    await back.click({ timeout: 5000 });
    await page.waitForURL((u) => u.pathname !== "/settings/statistics", { timeout: 5000 }).catch(() => {});
    await waitForStillFor(page, 200, pathOf, { label: "the pathname to stop changing after the chevron" });
    const landed = pathOf() === "/settings" ? "Settings" : pathOf() === "/stats" ? "Stats" : pathOf(); // uat-labels: ignore
    check(label === `Back to ${landed}`, `${tag} B2 label`, `the chevron lands where its label says (${label})`, `the label says "${label}" and it landed on ${landed}`);
    await page.goBack();
    await waitForStillFor(page, 200, pathOf, { label: "the pathname to stop changing after Back" });
    check(pathOf() !== "/settings/statistics", `${tag} B2 history`, "the system Back does not return to the group just left", "the system Back returned to the group");
    await page.unroute(isSummary);
  });

  await step(`${tag} R1`, async () => {
    for (const w of [375, 320]) {
      await page.setViewportSize({ width: w, height: 812 });
      await open("/stats");
      if (w < 360) {
        // Under 360 px the Range is a select, which cannot wrap.
        const options = await page.getByRole("combobox", { name: "Range" }).locator("option").allInnerTexts();
        check(JSON.stringify(options) === JSON.stringify(["Week", "Month", "Year", "Monthly", "All"]), `${tag} R1 ${w} labels`, `the Range select offers ${options.join(", ")}`, `the Range select offers ${options.join(", ")}`);
        continue;
      }
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
