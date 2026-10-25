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
//       reachable above the tab bar and a tap on it opens it; in Chromium, with results, a real swipe over them moves the
//       page (not the list) up and back down;
//   W4  Settings > Statistics > Export at 375x667, 402x874 and 440x956: Download data dictionary is a 44 px target.
// On the phone width (900, 912 and 956 wide, held sideways) and the desktop width (1000, 1100 and 1280 wide):
//   W3  an article open beside the list: the article toolbar fits its pane (More actions whole, the toolbar not
//       scrolled), the list header's controls stay inside the list column (wrapping where it is narrow), and opening More
//       leaves the toolbar where it was;
//       again with Large targets and with larger text sizes (the controls grow, so the columns need more; where the window
//       cannot hold even their floors, the toolbar still shows every control and reaches each by scrolling), and, in
//       Chromium, on a phone with a notch at both sides (the window is narrower by the insets). Each row first waits for
//       its setting to be in force on the page and fails with what it saw if it never is.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { deadFeed, start } from "./common.mjs";
import { installPageHelpers, tapProbe } from "./probes.mjs";

const t = await start(import.meta.url, 23);
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
          // With results the header takes over half the screen, so the page and the list scroll as one (the short
          // layout). A real swipe over the results moves the page first: up takes the header off the screen while the
          // list stays at its top, down brings it back. Touch input reaches the page through the browser's own input
          // pipeline only in Chromium (a DevTools command), so the other engines leave this to the scroll below.
          if (t.opt.browser === "chromium") {
            const box = page.getByRole("searchbox").first();
            // The seed's Go Blog feed always matches.
            await box.fill("go");
            const found = await page.locator(".search-results [data-list-scroller] [data-item-id]").first().waitFor({ timeout: 10000 }).then(() => true, () => false);
            check(found, `${where} swipe results`, "a search for go has results to swipe over", "no result row appeared within 10 s");
            if (found) {
              const cdp = await page.context().newCDPSession(page);
              const where2 = await page.evaluate(() => {
                const r = document.querySelector(".search-results").getBoundingClientRect();
                const bar = [...document.querySelectorAll('nav[aria-label="Primary"]')].pop().getBoundingClientRect();
                return { x: Math.round(r.left + r.width / 2), y: Math.round((Math.max(r.top, 0) + Math.min(r.bottom, bar.top)) / 2) };
              });
              const swipe = async (dy) => {
                const pt = (y) => [{ x: where2.x, y }];
                await cdp.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: pt(where2.y) });
                for (let i = 1; i <= 6; i++) await cdp.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: pt(where2.y - (dy * i) / 6) });
                await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
                await page.waitForTimeout(200);
                return page.evaluate(() => {
                  const shell = document.querySelector(".search-shell");
                  return { top: Math.round(shell.scrollTop), list: Math.round(shell.querySelector("[data-list-scroller]").scrollTop), short: shell.hasAttribute("data-short") };
                });
              };
              const up = await swipe(60);
              const down = await swipe(-60);
              await cdp.detach();
              check(up.short && up.top > 0 && up.list === 0 && down.top === 0, `${where} swipe`, "a swipe up over the results moves the page, not the list, and a swipe down brings it back", `after up the page is at ${up.top} px and the list at ${up.list} (short layout ${up.short}), after down the page is at ${down.top} px`);
            }
            await box.fill("");
            await page.getByRole("heading", { name: "Search your articles" }).waitFor({ timeout: 5000 });
          }
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

  // How many controls the article toolbar has at the default settings; the rows that cannot fit must still show them all.
  const ctl = { count: null };
  const geometry = () =>
    page.evaluate(() => {
      const tb = document.querySelector('[role="toolbar"][aria-label="Article actions"]');
      const col = document.querySelector('[role="separator"][aria-label="Resize article list"]')?.parentElement;
      const edge = (e) => Math.round(e.getBoundingClientRect().right);
      const buttons = tb.querySelectorAll("button");
      const root = document.documentElement;
      return {
        vw: innerWidth,
        rem: parseFloat(getComputedStyle(root).fontSize),
        targets: root.dataset.targets,
        count: buttons.length,
        scrollable: ["auto", "scroll"].includes(getComputedStyle(tb).overflowX),
        need: tb.scrollWidth,
        have: tb.clientWidth,
        scrolled: tb.scrollLeft,
        toolbarLeft: Math.round(tb.getBoundingClientRect().left),
        toolbarRight: edge(tb),
        firstLeft: Math.round(buttons[0].getBoundingClientRect().left),
        moreRight: edge(tb.querySelector('button[aria-label="More actions"]')),
        columnRight: col ? edge(col) : null,
        // The rightmost control of the list header, wherever its controls wrapped to.
        headerRight: Math.max(...[...document.querySelector('main header button[aria-label="List actions"]').closest("header").querySelectorAll("button, a, select")].map(edge)),
      };
    });
  const toolbarFits = (g) => g.need <= g.have && g.firstLeft >= g.toolbarLeft && g.moreRight <= g.toolbarRight && g.toolbarRight <= g.vw;
  const headerFits = (g) => g.columnRight !== null && g.headerRight <= g.columnRight;
  /**
   * Opens the first article beside its list at w x h and checks the article toolbar and (unless `header` is false) the
   * list header against its column. Device settings reach the page after it loads, and the columns follow the window through a
   * ResizeObserver, so it first waits until the setting under test is in force on the page (`targets` and `text` are the
   * Large targets and text size wanted, `inset` the right safe-area inset in px, or null), failing loudly with what it saw
   * if it never is, and then polls the condition under test, to a cap, and checks what it ended with.
   * With `degrade` the window cannot hold the columns at their floors, so no fit is asked: the toolbar must still show
   * every control and reach each of them by scrolling. Returns the geometry and the toolbar.
   */
  const beside = async (where, w, h, { header = true, targets = "normal", text = 1, inset = null, degrade = false } = {}) => {
    await at(w, h, "/l/all");
    const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await row.waitFor({ timeout: 15000 });
    // Opened by its address: a click would scroll the row into view, and at the largest text size on a short screen that
    // scrolls the (hidden-overflow) shell itself, which no one using the app can do.
    await page.goto(await row.locator("a").first().getAttribute("href"), { waitUntil: "load" });
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    const bar = page.getByRole("toolbar", { name: "Article actions" });
    await bar.waitFor({ timeout: 10000 });
    const inForce = async () => {
      const seen = await page.evaluate(() => {
        const main = document.getElementById("main");
        return { targets: document.documentElement.dataset.targets, rem: parseFloat(getComputedStyle(document.documentElement).fontSize), inset: main ? parseFloat(getComputedStyle(main).paddingRight) : null };
      });
      return { seen, ok: seen.targets === targets && Math.abs(seen.rem - 16 * text) < 0.05 && (inset === null || (seen.inset !== null && Math.abs(seen.inset - inset) < 0.5)) };
    };
    let force;
    await until(async () => (force = await inForce()).ok, 5000);
    if (!force.ok) throw new Error(`${where}: the setting never took effect (wanted targets ${targets}, root ${16 * text} px${inset === null ? "" : `, right inset ${inset} px`}; the page has targets ${force.seen.targets}, root ${force.seen.rem} px, right padding of #main ${force.seen.inset})`);
    let g;
    if (degrade) {
      // What the toolbar shows with its end scrolled to: where its last control lies against its own edge.
      const reach = () =>
        page.evaluate(() => {
          const tb = document.querySelector('[role="toolbar"][aria-label="Article actions"]');
          tb.scrollLeft = tb.scrollWidth;
          const out = { lastRight: Math.round([...tb.querySelectorAll("button")].at(-1).getBoundingClientRect().right), toolbarRight: Math.round(tb.getBoundingClientRect().right) };
          tb.scrollLeft = 0;
          return out;
        });
      let r;
      const holds = async () => {
        g = await geometry();
        r = await reach();
        return g.count === ctl.count && g.scrollable && g.toolbarRight <= g.vw && r.lastRight <= r.toolbarRight;
      };
      await until(async () => (await holds()) && (!header || headerFits(g)), 3000);
      const state = `root ${g.rem} px, Large targets ${g.targets === "large" ? "on" : "off"}`;
      check(await holds(), `${where} degrades`, `the window is too narrow for the columns at their floors (${state}), and the toolbar still has all ${g.count} controls, inside the screen, the last one reached by scrolling`, `${g.count} of ${ctl.count} controls, scrollable ${g.scrollable}, the toolbar ends at ${g.toolbarRight} of ${g.vw}, the last control ends at ${r.lastRight} of ${r.toolbarRight} once scrolled (${state})`);
      if (header) check(headerFits(g), `${where} list header`, `the list header's controls are inside the list column, wrapped if need be (${state})`, `its last control ends at ${g.headerRight}, the list column at ${g.columnRight} (${state})`);
      return { g, bar };
    }
    await until(async () => {
      g = await geometry();
      return toolbarFits(g) && (!header || headerFits(g));
    }, 3000);
    const state = `root ${g.rem} px, Large targets ${g.targets === "large" ? "on" : "off"}`;
    check(toolbarFits(g), `${where} toolbar`, `the article toolbar fits its pane, More actions whole (${state}, ${g.need} of ${g.have} px)`, `the toolbar needs ${g.need} px of ${g.have}, starts at ${g.firstLeft} (pane ${g.toolbarLeft}), More actions ends at ${g.moreRight}, the pane at ${g.toolbarRight}, the screen at ${g.vw} (${state})`);
    if (header) check(headerFits(g), `${where} list header`, `the list header's controls are inside the list column, wrapped if need be (${state})`, `its last control ends at ${g.headerRight}, the list column at ${g.columnRight} (${state})`);
    return { g, bar };
  };
  const device = (body) => page.request.patch("/api/device", { headers: t.WRITE, data: body });
  const DEFAULTS = { "client.large_targets": false, "client.text_size": 1 };

  await step(`${tag} W3`, async () => {
    for (const [w, h] of PANES[vp.mobile ? "phone" : "desktop"]) {
      const where = `${tag} W3 ${w}x${h}`;
      const { g, bar } = await beside(where, w, h);
      ctl.count = g.count;
      await t.shot(page, `${tag}-reader-${w}x${h}`);
      await bar.getByRole("button", { name: "More actions" }).click();
      await page.getByRole("menu").waitFor({ timeout: 5000 });
      const after = await geometry();
      check(after.scrolled === g.scrolled, `${where} menu`, "opening More leaves the toolbar where it was", `the toolbar moved from ${g.scrolled} to ${after.scrolled} px`);
      await page.keyboard.press("Escape");
    }
  });

  // The controls grow with Large targets (56 px) and with the text size (their size is in rem), and the columns need
  // more. The article keeps what its toolbar needs and the list gives way to its 260 px floor, after the sidebar has
  // gone to its 200 px floor, so a window holds the columns whole when 200 + 260 + two gutters (1.5rem each) + the
  // toolbar fits in it: 908 px with Large targets (400 px toolbar), 870 px at text 1.125 (356), 915 px at 1.25 (395) and
  // 1006 px at 1.5 (474). The rows below sit on one side of that: they ask the toolbar to fit, or, where the window is too
  // narrow, that it degrades without hiding a control. Every row asks the list header to stay inside the list column:
  // where the column is narrower than the header's controls, they wrap to a second line.
  // (The toolbar's buttons may shrink a few px before it overflows, so a window a few px under the floor still shows a
  // whole toolbar; the rows keep clear of that margin.)
  // mode: "fit" asks the toolbar, "degrade" the controls and their reach; both ask the list header.
  const SETTINGS = [
    { what: "Large targets", prefs: { "client.large_targets": true }, targets: "large", text: 1, rows: vp.mobile ? [[956, 440, "fit"], [912, 420, "fit"]] : [[1280, 800, "fit"], [1000, 440, "fit"]] },
    vp.mobile
      ? { what: "larger text", prefs: { "client.text_size": 1.125 }, targets: "normal", text: 1.125, rows: [[956, 440, "fit"], [912, 420, "fit"]] }
      : { what: "larger text", prefs: { "client.text_size": 1.25 }, targets: "normal", text: 1.25, rows: [[1280, 800, "fit"], [1000, 440, "fit"]] },
    { what: "largest text", prefs: { "client.text_size": 1.5 }, targets: "normal", text: 1.5, rows: vp.mobile ? [[956, 440, "degrade"], [912, 420, "degrade"]] : [[1280, 800, "fit"], [920, 440, "degrade"]] },
  ];
  for (const { what, prefs, targets, text, rows } of SETTINGS) {
    await step(`${tag} W3 ${what}`, async () => {
      const set = await device(prefs);
      if (!set.ok()) return t.fail(`${tag} W3 ${what} setup`, `the setting was not saved (${set.status()})`);
      try {
        for (const [w, h, mode] of rows) {
          await beside(`${tag} W3 ${what} ${w}x${h}`, w, h, { degrade: mode === "degrade", targets, text });
          await t.shot(page, `${tag}-reader-${what.replace(" ", "-")}-${w}x${h}`);
        }
      } finally {
        // A setting left behind would carry into the later flows on the same instance, so a failed reset is a finding.
        const reset = await device(DEFAULTS);
        if (!reset.ok()) t.fail(`${tag} W3 ${what} reset`, `the device settings were not restored (${reset.status()})`);
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
        // 47 px leaves room for everything at 956; at 62 px (the widest phone) the list header wraps its controls.
        for (const inset of [47, 62]) {
          await insets(inset);
          await beside(`${tag} W3 notch ${inset} px 956x440`, 956, 440, { inset });
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
