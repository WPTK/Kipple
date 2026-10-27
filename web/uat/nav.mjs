// UAT: one destination is current, the resize handles are reachable, focus survives Back and Forward, and the landscape
// tab badge sits after the name (the navigation package of the design review: #638, #631, #641, #643).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/nav.mjs                   in another, once the feeds have fetched
//   node uat/nav.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the desktop width of the run:
//   A1  the sidebar marks exactly one destination: Unread on Unread, Feeds on Feed health, none on a feed's list;
//   A2  the sidebar has no "Manage feeds", and All articles has an icon of its own;
//   A3  "Resize sidebar" is at least 24 px wide, the sidebar's controls end at least 8 px before it, and "Resize article list"
//       starts where the list ends (the list's scrollbar is not under it), and no control of the next column is under either strip;
//   A4  open an article from the list, press Back: focus is on the row left, not the body; Forward: not the body.
// On the phone width of the run:
//   B1  at 812x375 the unread badge follows the Articles name and is not on top of it;
//   B2  at 844x390 (a landscape phone) the tab bar shows and there is no sidebar;
//   B3  the current tab's bar (3 px, drawn in a band of its own above the tab's content) ends at least 2 px above the icon and
//       above the "99+" badge, at 375x812 and 320x568 with default settings, text size 1.5 and Large targets, and with no badge;
//   B5  at 812x375 with text size 1.5 the tabs (icon beside the name, 99+ badge) are not cut off vertically;
//   B4  at 568x320 with text size 1.5 and a "99+" badge the five tab names are whole and the badge stays inside the bar,
//       and the tabs do not move sideways when the unread count goes from 5 to 150.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start, waitForPageStill, waitForStillFor } from "./common.mjs";

const t = await start(import.meta.url, 19, { serviceWorkers: "block" });
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const feed = (await t.boot(page)).feeds[0];
  if (!feed) return t.fail(`${tag} setup`, "the seeded instance has no feed");
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await waitForPageStill(page);
  };
  if (!vp.mobile) {
    const side = page.locator('nav[aria-label="Primary"]').first();
    const current = () => side.locator('a[aria-current="page"]').evaluateAll((as) => as.map((a) => a.innerText.replace(/\s+/g, " ").replace(/\s*\d+\+?$/, "").trim()));
    await step(`${tag} A1`, async () => {
      for (const [path, want] of [["/l/unread", ["Unread"]], ["/l/all", ["All articles"]], ["/health", ["Feeds"]], [`/l/unread?feed=${feed.id}`, []]]) {
        await open(path);
        const got = await current();
        check(JSON.stringify(got) === JSON.stringify(want), `${tag} A1 ${path.split("?")[0]}${path.includes("feed") ? " (feed)" : ""}`, `current: ${want.join(", ") || "none"}`, `current: ${got.join(", ") || "none"}`);
      }
      await t.shot(page, `${tag}-sidebar-feed-list`);
    });
    await step(`${tag} A2`, async () => {
      await open("/l/unread");
      const names = await side.locator("a").allInnerTexts();
      check(!names.some((n) => /Manage feeds/.test(n)), `${tag} A2 name`, "no Manage feeds", "Manage feeds is still there");
      const icons = await side.locator("a svg").evaluateAll((s) => s.map((e) => e.getAttribute("class") ?? "").slice(0, 6));
      check(new Set(icons).size === icons.length, `${tag} A2 icons`, "each destination has its own icon", `icons repeat: ${icons.join(" | ")}`);
    });
    await step(`${tag} A3`, async () => {
      await open("/l/unread");
      const handle = page.getByRole("separator", { name: "Resize sidebar" });
      const box = await handle.boundingBox();
      check(!!box && box.width >= 24, `${tag} A3 width`, `the handle is ${box?.width} px wide`, `the handle is ${box?.width} px wide, under 24`);
      const gap = await side.evaluate((nav) => {
        const hit = nav.querySelector('[role="separator"]').getBoundingClientRect();
        return Math.min(...[...nav.querySelectorAll("a, button")].map((e) => hit.left - e.getBoundingClientRect().right));
      });
      check(gap >= 8, `${tag} A3 clear`, `controls end ${Math.round(gap)} px before the handle`, `a control ends ${Math.round(gap)} px before the handle, under 8`);
      // The list's own scrollbar (15 px where it is not an overlay) is not under the list's handle.
      await page.locator("[data-item-id]").first().waitFor();
      // A headless browser hides its scrollbars (the report says how wide this one is), so the check is the geometry that
      // keeps any scrollbar clear: the handle starts at or after the right edge of the list's whole box.
      const list = await page.evaluate(() => {
        const col = document.querySelector('[data-testid="list-scroll"]');
        const hit = document.querySelector('[role="separator"][aria-label="Resize article list"]');
        if (!col || !hit) return null;
        return { right: col.getBoundingClientRect().right, left: hit.getBoundingClientRect().left, bar: col.offsetWidth - col.clientWidth };
      });
      // The other side: no control of the next column is under a handle's strip (the handle is the only thing there).
      const under = await page.evaluate(() => {
        const hits = [];
        for (const sep of document.querySelectorAll('[role="separator"]')) {
          const r = sep.getBoundingClientRect();
          for (const el of document.querySelectorAll("main a, main button, main input, main select, main textarea, main [tabindex]")) {
            if (el === sep || sep.contains(el)) continue;
            const b = el.getBoundingClientRect();
            if (b.width > 0 && b.height > 0 && b.left < r.right && b.right > r.left && b.top < r.bottom && b.bottom > r.top) hits.push(`${sep.getAttribute("aria-label")}: ${el.getAttribute("aria-label") ?? el.textContent?.trim().slice(0, 20) ?? el.tagName}`);
          }
        }
        return hits;
      });
      check(under.length === 0, `${tag} A3 neighbour`, "no control sits under a resize handle", `under a handle: ${under.slice(0, 4).join("; ")}`);
      check(list !== null && list.left >= list.right - 0.5, `${tag} A3 scrollbar`, `the list handle starts at the list's edge (scrollbar ${list?.bar} px)`, `the list handle starts ${list ? Math.round(list.right - list.left) : "?"} px inside the list's edge`);
    });
    await step(`${tag} A4`, async () => {
      await open("/l/all");
      const row = page.locator("[data-item-id] a").first();
      await row.waitFor();
      const id = await row.evaluate((a) => a.closest("[data-item-id]").dataset.itemId);
      await row.click();
      await page.waitForURL(/\/i\//, { timeout: 5000 });
      await page.goBack();
      await page.waitForURL(/\/l\/all/, { timeout: 5000 });
      const where = () => page.evaluate(() => ({ body: document.activeElement === document.body, row: document.activeElement?.closest("[data-item-id]")?.dataset.itemId ?? null, tag: document.activeElement?.tagName }));
      // Focus is put back on the row a frame or two after the route changes: wait until it has stopped moving.
      let w = await waitForStillFor(page, 300, where, { label: "focus to stop moving after Back" });
      check(!w.body && w.row === id, `${tag} A4 back`, "Back puts focus on the row left", `focus is on ${w.row ?? w.tag}`);
      await page.goForward();
      await page.waitForURL(/\/i\//, { timeout: 5000 });
      // A negative ("focus is not dropped to the body"): hold the window longer than the 700 ms mark delay plus a round trip.
      w = await waitForStillFor(page, 1000, where, { label: "focus to stop moving after Forward" });
      check(!w.body, `${tag} A4 forward`, "Forward does not drop focus to the body", "Forward left focus on the body");
    });
  } else {
    await step(`${tag} B1`, async () => {
      await page.setViewportSize({ width: 812, height: 375 });
      await open("/l/unread");
      const r = await page.locator('nav[aria-label="Primary"]').last().locator("a").first().evaluate((a) => {
        const badge = a.querySelector('[data-testid="unread-count"], [data-testid="unread-dot"]');
        const walker = document.createTreeWalker(a, NodeFilter.SHOW_TEXT);
        let label = null;
        for (let n = walker.nextNode(); n; n = walker.nextNode()) if (/Articles/.test(n.textContent)) label = n;
        if (!badge || !label) return null;
        const range = document.createRange();
        range.selectNodeContents(label);
        return { badgeLeft: badge.getBoundingClientRect().left, labelRight: range.getBoundingClientRect().right };
      });
      check(r !== null && r.badgeLeft >= r.labelRight - 1, `${tag} B1`, "the badge follows the name", `${r ? `the badge starts at ${r.badgeLeft} inside the name that ends at ${r.labelRight}` : "no badge or no name was found"}`);
      await t.shot(page, `${tag}-landscape-badge`);
      // A landscape phone (844x390) stays on the phone layout: the tab bar, no sidebar.
      await page.setViewportSize({ width: 844, height: 390 });
      await open("/l/unread");
      const bar = await page.locator('nav[aria-label="Primary"]').count();
      const sidebar = await page.getByRole("separator", { name: "Resize sidebar" }).count();
      check(bar === 1 && sidebar === 0, `${tag} B2`, "844x390 shows the tab bar and no sidebar", `${bar} Primary navs, ${sidebar} sidebar handles`);
      await t.shot(page, `${tag}-landscape-844x390`);
    });

    const patch = (b) =>
      page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
    /** Answers the bootstrap with this many unread, so the Articles badge shows a count of that size. */
    let unread = 150;
    await page.route((u) => u.pathname === "/api/bootstrap", async (route) => {
      const real = await (await route.fetch()).json();
      await route.fulfill({ json: { ...real, counts: { ...real.counts, unread } } });
    });
    const articlesTab = () => page.locator('nav[aria-label="Primary"]').last().locator("a").first();
    const reopen = async (w, h) => {
      await page.setViewportSize({ width: w, height: h });
      await open("/l/unread");
      await (unread > 0 ? articlesTab().locator('[data-testid="unread-count"]') : articlesTab()).waitFor({ timeout: 10000 });
    };

    await step(`${tag} B3`, async () => {
      try {
        for (const [what, prefs] of [["default", {}], ["text 1.5", { "client.text_size": 1.5 }], ["Large targets", { "client.large_targets": true }]]) {
          if ((await patch({ "client.text_size": 1, "client.large_targets": false, ...prefs })) !== 200) throw new Error(`${what}: the setting was not saved`);
          for (const [count, w, h] of [[150, 375, 812], [0, 375, 812], [150, 320, 568], [0, 320, 568]]) {
            unread = count;
            await reopen(w, h);
            const m = await articlesTab().evaluate((a) => {
              const cs = getComputedStyle(a, "::before");
              const bar = a.getBoundingClientRect().top + parseFloat(cs.top) + parseFloat(cs.height);
              const icon = a.querySelector("svg").getBoundingClientRect();
              const badge = a.querySelector('[data-testid="unread-count"]')?.getBoundingClientRect();
              return { currentTab: a.getAttribute("aria-current") === "page", barHeight: parseFloat(cs.height), bar, icon: icon.top, badge: badge ? badge.top : null };
            });
            const where = `${tag} B3 ${w}x${h} ${what}, ${count ? "99+ badge" : "no badge"}`;
            check(m.currentTab && m.barHeight > 0, `${where} bar`, "the current tab draws its bar", `current ${m.currentTab}, bar height ${m.barHeight}`);
            check(m.icon - m.bar >= 2, `${where} icon`, "the bar ends 2 px or more above the icon", `the bar ends at ${m.bar.toFixed(1)}, the icon starts at ${m.icon.toFixed(1)}`);
            if (count) check(m.badge !== null && m.badge - m.bar >= 2, `${where} badge`, "the bar ends 2 px or more above the badge", `the bar ends at ${m.bar.toFixed(1)}, the badge starts at ${m.badge}`);
            await t.shot(page, `${tag}-tabs-bar-${w}-${what.replace(/\W+/g, "-")}-${count ? "badge" : "plain"}`);
          }
        }
      } finally {
        await patch({ "client.text_size": 1, "client.large_targets": false });
      }
    });

    await step(`${tag} B5`, async () => {
      try {
        if ((await patch({ "client.text_size": 1.5 })) !== 200) throw new Error("text size was not saved");
        unread = 150;
        await reopen(812, 375);
        const m = await page.locator('nav[aria-label="Primary"]').last().evaluate((nav) => {
          const bar = nav.getBoundingClientRect();
          return [...nav.querySelectorAll("a")].map((a) => {
            const r = a.getBoundingClientRect();
            const inner = [...a.querySelectorAll("svg, span")].map((e) => e.getBoundingClientRect()).filter((x) => x.height > 0);
            const top = Math.min(r.bottom, ...inner.map((x) => x.top));
            const bottom = Math.max(r.top, ...inner.map((x) => x.bottom));
            return { name: a.innerText.replace(/\s+/g, " ").trim(), clipped: a.scrollHeight > a.clientHeight + 1 || top < bar.top - 0.5 || bottom > bar.bottom + 0.5, icon: !!a.querySelector("svg") && a.querySelector("svg").getBoundingClientRect().width > 0 };
          });
        });
        const clipped = m.filter((x) => x.clipped).map((x) => x.name);
        check(clipped.length === 0 && m.length >= 4, `${tag} B5 clipped`, "at 812x375 and text 1.5 no tab is cut off vertically", `cut off: ${clipped.join(", ")} (${m.length} tabs)`);
        check(m.every((x) => x.icon), `${tag} B5 icons`, "the icon sits beside each name", `no icon on: ${m.filter((x) => !x.icon).map((x) => x.name).join(", ")}`);
        await t.shot(page, `${tag}-landscape-tabs-text-1-5`);
      } finally {
        await patch({ "client.text_size": 1 });
      }
    });

    await step(`${tag} B4`, async () => {
      try {
        if ((await patch({ "client.text_size": 1.5 })) !== 200) throw new Error("text size was not saved");
        const lefts = [];
        for (const count of [5, 150]) {
          unread = count;
          await reopen(568, 320);
          const m = await page.locator('nav[aria-label="Primary"]').last().evaluate((nav) => {
            const bar = nav.getBoundingClientRect();
            const tabs = [...nav.querySelectorAll("a")].map((a) => ({ name: a.innerText.replace(/\s+/g, " ").trim(), left: Math.round(a.getBoundingClientRect().left), cut: a.scrollWidth > a.clientWidth + 1 }));
            const badge = nav.querySelector('[data-testid="unread-count"]')?.getBoundingClientRect();
            return { tabs, badgeInside: !badge || (badge.left >= bar.left && badge.right <= bar.right), barRight: bar.right };
          });
          lefts.push(m.tabs.map((x) => x.left));
          const cut = m.tabs.filter((x) => x.cut).map((x) => x.name);
          check(cut.length === 0 && m.tabs.length >= 4, `${tag} B4 ${count} whole`, "all tab names are whole", `cut off: ${cut.join(", ")} (${m.tabs.length} tabs)`);
          check(m.badgeInside, `${tag} B4 ${count} badge`, "the badge is inside the bar", "the badge runs past the bar");
          await t.shot(page, `${tag}-short-tabs-largest-${count}`);
        }
        check(JSON.stringify(lefts[0]) === JSON.stringify(lefts[1]), `${tag} B4 steady`, "the tabs keep their places when the count grows", `tab lefts ${lefts[0]} then ${lefts[1]}`);
      } finally {
        await patch({ "client.text_size": 1 });
      }
    });
  }
});
await t.finish("nav");
