// UAT: four layout defects the 1.5.0 phone walk found (#767).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/layout-walk.mjs           in another, once the feeds have fetched
//   node uat/layout-walk.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the phone width of the run:
//   W1  the Feed index at 375x667, 402x874 and 440x956: a feed with a long name ends in an ellipsis (the text's own box
//       carries it), its unread count stays on screen beside it, and the name is still a 44 px target;
//   W2  Search at 667x375, 852x393 and 874x402 with a saved search: the empty page scrolls, so the saved search is
//       reachable above the tab bar and a tap on it opens it;
//   W4  Settings > Statistics > Export at 375x667, 402x874 and 440x956: Download data dictionary is a 44 px target.
// On the phone width (900, 912 and 956 wide, held sideways) and the desktop width (1000, 1100 and 1280 wide):
//   W3  an article open beside the list: the article toolbar fits its pane (More actions whole, the toolbar not
//       scrolled), the list header's controls stay inside the list column, and opening More leaves the toolbar where it was;
//       again with Large targets and with a larger text size (the controls grow, so the columns need more), and, in
//       Chromium, on a phone with a notch at both sides (the window is narrower by the insets).
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { deadFeed, start } from "./common.mjs";
import { installPageHelpers, tapProbe } from "./probes.mjs";

const t = await start(import.meta.url, 19);
const { check, step } = t;

const LONG = "UAT feed with a name too long to fit beside its unread count on a phone";
const SEARCH = "UAT feed saved search";
const PORTRAIT = [[375, 667], [402, 874], [440, 956]];
const LANDSCAPE = [[667, 375], [852, 393], [874, 402]];
const PANES = { phone: [[956, 440], [912, 420], [900, 420]], desktop: [[1280, 800], [1100, 480], [1000, 440]] };

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  // A control that is not there fails its step in seconds instead of waiting out Playwright's 30 s.
  page.setDefaultTimeout(8000);
  const at = async (w, h, path) => {
    await page.setViewportSize({ width: w, height: h });
    await page.goto(path, { waitUntil: "load" });
  };
  /** Polls `holds` until it is true, for at most `ms`; the answer may need the page, so it is asked here and not in a waitForFunction. */
  const until = async (holds, ms = 10000) => {
    for (const end = Date.now() + ms; Date.now() < end; await page.waitForTimeout(100)) if (await holds()) return true;
    return false;
  };

  if (vp.mobile) {
    await step(`${tag} W1`, async () => {
      // One feed at an address that never resolves, so nothing is fetched, with a name no phone has room for.
      await t.importOpml(page, `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body><outline type="rss" text="${LONG}" title="${LONG}" xmlUrl="${deadFeed("walk")}"/></body></opml>`);
      for (const [w, h] of PORTRAIT) {
        const where = `${tag} W1 ${w}x${h}`;
        await at(w, h, "/feeds/index");
        const head = page.locator("section[data-feed-block] > h2").filter({ hasText: LONG });
        await head.waitFor({ timeout: 15000 });
        const m = await head.evaluate((h2) => {
          const a = h2.querySelector("a");
          const count = h2.querySelector("[data-testid=feed-index-unread]");
          // The box that holds the name's text is the one that has to carry the ellipsis.
          const text = document.createTreeWalker(a, NodeFilter.SHOW_TEXT, { acceptNode: (n) => (n.textContent?.trim() ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT) }).nextNode();
          const box = text?.parentElement ?? a;
          const cs = getComputedStyle(box);
          const link = a.getBoundingClientRect();
          const c = count.getBoundingClientRect();
          return { cut: box.scrollWidth > box.clientWidth, ellipsis: cs.textOverflow === "ellipsis" && cs.overflowX !== "visible" && !/flex|grid/.test(cs.display), display: cs.display, countRight: c.right, countLeft: c.left, linkRight: link.right, linkHeight: link.height, vw: innerWidth };
        });
        check(m.cut, `${where} cut`, "the name is longer than its room (the check below means something)", "the name fits, so the ellipsis was not tested");
        check(m.ellipsis, `${where} ellipsis`, "the text's own box ends the name in an ellipsis", `the box holding the name is display ${m.display} and does not ellipsize`);
        check(m.countRight <= m.vw && m.countLeft >= m.linkRight - 0.5, `${where} count`, "the unread count is on screen, after the name", `count ${Math.round(m.countLeft)}..${Math.round(m.countRight)}, name ends at ${Math.round(m.linkRight)}, screen ${m.vw}`);
        check(m.linkHeight >= 43.5, `${where} target`, "the name is a 44 px target", `the name is ${Math.round(m.linkHeight)} px tall`);
        await t.shot(page, `${tag}-feed-index-${w}x${h}`);
      }
    });

    await step(`${tag} W2`, async () => {
      const made = await page.request.post("/api/saved-searches", { headers: t.WRITE, data: { name: SEARCH, q: "uat" } });
      if (!made.ok()) return t.fail(`${tag} W2 setup`, `the saved search was not made (${made.status()})`);
      const id = (await made.json()).id;
      try {
        for (const [w, h] of LANDSCAPE) {
          const where = `${tag} W2 ${w}x${h}`;
          await at(w, h, "/search");
          await page.getByRole("heading", { name: "Search your articles" }).waitFor({ timeout: 15000 });
          // The last of the saved searches the empty page lists (the seed may already have some, and it lists five).
          const link = page.getByRole("list", { name: "Saved searches" }).getByRole("link").last();
          await link.waitFor({ timeout: 15000 });
          // The page's own scrolling brings it into view (a page that cannot scroll leaves it where it was).
          await link.scrollIntoViewIfNeeded();
          const place = () =>
            link.evaluate((a) => {
              const r = a.getBoundingClientRect();
              const bar = [...document.querySelectorAll('nav[aria-label="Primary"]')].pop().getBoundingClientRect();
              const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
              return { top: Math.round(r.top), bottom: Math.round(r.bottom), bar: Math.round(bar.top), onTop: a.contains(hit) };
            });
          const p = await place();
          check(p.bottom <= p.bar && p.onTop, `${where} reach`, "scrolled to, the saved search is above the tab bar and is what a tap hits", `the saved search is at ${p.top}..${p.bottom}, the tab bar starts at ${p.bar}, a tap there ${p.onTop ? "hits it" : "hits something else"}`);
          if (p.bottom > p.bar || !p.onTop) continue;
          await t.shot(page, `${tag}-search-${w}x${h}`);
          await link.click();
          const opened = await page.waitForURL((u) => u.searchParams.has("ss"), { timeout: 5000 }).then(() => true, () => false);
          check(opened, `${where} opens`, "a tap on the saved search opens it", `the address is ${page.url()}`);
        }
      } finally {
        await page.request.delete(`/api/saved-searches/${id}`, { headers: t.WRITE }).catch(() => {});
      }
    });
  }

  const geometry = () =>
    page.evaluate(() => {
      const tb = document.querySelector('[role="toolbar"][aria-label="Article actions"]');
      const col = document.querySelector('[role="separator"][aria-label="Resize article list"]')?.parentElement;
      const edge = (e) => Math.round(e.getBoundingClientRect().right);
      const buttons = tb.querySelectorAll("button");
      return {
        vw: innerWidth,
        need: tb.scrollWidth,
        have: tb.clientWidth,
        scrolled: tb.scrollLeft,
        toolbarLeft: Math.round(tb.getBoundingClientRect().left),
        toolbarRight: edge(tb),
        firstLeft: Math.round(buttons[0].getBoundingClientRect().left),
        moreRight: edge(tb.querySelector('button[aria-label="More actions"]')),
        columnRight: col ? edge(col) : null,
        actionsRight: edge(document.querySelector('main header button[aria-label="List actions"]')),
      };
    });
  const toolbarFits = (g) => g.need <= g.have && g.firstLeft >= g.toolbarLeft && g.moreRight <= g.toolbarRight && g.toolbarRight <= g.vw;
  const headerFits = (g) => g.columnRight !== null && g.actionsRight <= g.columnRight;
  /**
   * Opens the first article beside its list at w x h and checks the article toolbar (and with `header` the list header)
   * against its column. The columns follow the window through a ResizeObserver, so it polls the condition under test, to a
   * cap, and checks what it ended with. Returns the geometry and the toolbar.
   */
  const beside = async (where, w, h, { header = true } = {}) => {
    await at(w, h, "/l/all");
    const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await row.waitFor({ timeout: 15000 });
    await row.locator("a").first().click();
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    const bar = page.getByRole("toolbar", { name: "Article actions" });
    await bar.waitFor({ timeout: 10000 });
    let g;
    await until(async () => {
      g = await geometry();
      return toolbarFits(g) && (!header || headerFits(g));
    }, 3000);
    check(toolbarFits(g), `${where} toolbar`, "the article toolbar fits its pane, More actions whole", `the toolbar needs ${g.need} px of ${g.have}, starts at ${g.firstLeft} (pane ${g.toolbarLeft}), More actions ends at ${g.moreRight}, the pane at ${g.toolbarRight}, the screen at ${g.vw}`);
    if (header) check(headerFits(g), `${where} list header`, "the list header's last control is inside the list column", `List actions ends at ${g.actionsRight}, the list column at ${g.columnRight}`);
    return { g, bar };
  };
  const device = (body) => page.request.patch("/api/device", { headers: t.WRITE, data: body });

  await step(`${tag} W3`, async () => {
    for (const [w, h] of PANES[vp.mobile ? "phone" : "desktop"]) {
      const where = `${tag} W3 ${w}x${h}`;
      const { g, bar } = await beside(where, w, h);
      await t.shot(page, `${tag}-reader-${w}x${h}`);
      await bar.getByRole("button", { name: "More actions" }).click();
      await page.getByRole("menu").waitFor({ timeout: 5000 });
      const after = await geometry();
      check(after.scrolled === g.scrolled, `${where} menu`, "opening More leaves the toolbar where it was", `the toolbar moved from ${g.scrolled} to ${after.scrolled} px`);
      await page.keyboard.press("Escape");
    }
  });

  // The controls grow with Large targets (56 px) and with the text size (their size is in rem), and the columns need
  // more. The article keeps what its toolbar needs and the list gives way first, so the list header is asked to fit only
  // where the window can hold the sidebar at its floor, the list and the article (a 1280 px window, not 956 px).
  const sizes = vp.mobile ? [[956, 440, false], [912, 420, false]] : [[1280, 800, true], [1000, 440, false]];
  for (const [what, prefs] of [
    ["Large targets", { "client.large_targets": true }],
    ["larger text", { "client.text_size": vp.mobile ? 1.25 : 1.5 }],
  ]) {
    await step(`${tag} W3 ${what}`, async () => {
      const set = await device(prefs);
      if (!set.ok()) return t.fail(`${tag} W3 ${what} setup`, `the setting was not saved (${set.status()})`);
      try {
        for (const [w, h, header] of sizes) {
          await beside(`${tag} W3 ${what} ${w}x${h}`, w, h, { header });
          await t.shot(page, `${tag}-reader-${what.replace(" ", "-")}-${w}x${h}`);
        }
      } finally {
        await device({ "client.large_targets": false, "client.text_size": 1 });
      }
    });
  }

  // A phone with a notch held sideways: the window the columns share is narrower by the safe-area insets at both sides.
  // Only Chromium can set them (a DevTools command), so the other engines leave this to the unit tests.
  if (vp.mobile && t.opt.browser === "chromium") {
    await step(`${tag} W3 notch`, async () => {
      const cdp = await page.context().newCDPSession(page);
      const insets = (n) => cdp.send("Emulation.setSafeAreaInsetsOverride", { insets: { top: 0, left: n, right: n, bottom: 0 } });
      try {
        // 47 px leaves room for everything at 956; 62 px (the widest phone) is 6 px short of the list header, so only the toolbar is asked.
        for (const [inset, header] of [[47, true], [62, false]]) {
          await insets(inset);
          await beside(`${tag} W3 notch ${inset} px 956x440`, 956, 440, { header });
          await t.shot(page, `${tag}-reader-notch-${inset}`);
        }
      } finally {
        await insets(0).catch(() => {});
      }
    });
  }

  if (vp.mobile) {
    await step(`${tag} W4`, async () => {
      for (const [w, h] of PORTRAIT) {
        const where = `${tag} W4 ${w}x${h}`;
        await at(w, h, "/settings/statistics");
        await page.getByRole("button", { name: "Export…" }).click();
        const link = page.getByRole("link", { name: /data dictionary/ });
        await link.waitFor({ timeout: 10000 });
        await page.evaluate(installPageHelpers);
        const isLink = (d) => /data dictionary/i.test(d);
        // The dialog opens with a short animation, so the probe runs again until it has measured the link itself (to a cap).
        let probe;
        await until(async () => {
          probe = await page.evaluate(tapProbe, { feed: { body: ".no-feed-html", own: ".no-feed-html" }, min: 44, root: '[role="dialog"]' });
          return probe.seen.some(isLink);
        }, 3000);
        const { small, seen, covered } = probe;
        const mine = small.filter((s) => isLink(s.desc));
        check(seen.some(isLink), `${where} measured`, "the data dictionary link itself was measured", covered.some(isLink) ? "the link is covered at its centre, so it could not be measured" : "the link was not among the controls measured");
        check(mine.length === 0, `${where} target`, "Download data dictionary is a 44 px target", `it is ${mine[0]?.w} x ${mine[0]?.h} px`);
        await t.shot(page, `${tag}-export-${w}x${h}`);
      }
    });
  }
});

await t.finish("layout-walk");
