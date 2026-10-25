// UAT: menus are bottom sheets on a touch screen and popovers beside their button with a mouse (#423).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:touch-menus            in another
//   npm run uat:touch-menus -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800 with a mouse, phone 375x812 with touch, and the phone turned to 852x393) it opens the
// List options menu, the Reading time menu and a row's More actions menu, and checks:
//   D1  (desktop) each is a popover: no dimmed page, narrower than the screen, not pinned to the bottom edge;
//   P1  (phone) each is a sheet: the screen's width up to 512 px, touching the bottom edge, no taller than 70% of the
//       screen, over a dimmed page, and List options names its Layout and Order sections;
//   P2  focus is inside the sheet when it opens, Escape closes it and focus returns to the button that opened it;
//   P3  a tap on the dimmed page closes the sheet and does not reach the page under it (the address does not change);
//   P4  a tap on a choice applies it, and in landscape, where the sheet is shorter than its content, the last choice of
//       List options can be scrolled to;
//   P6  focus moving to the page behind an open sheet, after the press that opened it is over, closes the sheet (#727);
//   A1  (phone, portrait and landscape) the Reading appearance panel of an open article is a short sheet: touching the
//       bottom edge, no taller than 45% of the screen, with no dimmed page, and the article title shows above it;
//   A2  (desktop) beside an open article there is one Reading appearance button, and its panel is a popover.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 21);
const { check, step } = t;

const LAYOUT = /^List options/;
const LENGTH = /^Reading time/;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1").first().waitFor({ timeout: 15000 });
    await page.locator("main article[data-item-id]").first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(600);
  };
  const menuBox = () =>
    page.evaluate(() => {
      const m = document.querySelector('[role="menu"]');
      const r = m.getBoundingClientRect();
      return {
        sheet: m.hasAttribute("data-menu-sheet"),
        left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height,
        win: { w: window.innerWidth, h: window.innerHeight },
        dim: !!document.querySelector("[data-menu-backdrop]"),
        focusIn: m.contains(document.activeElement),
      };
    });
  const buttons = () => ({
    layout: page.getByRole("button", { name: LAYOUT }),
    length: page.getByRole("button", { name: LENGTH }),
    row: page.locator("main article[data-item-id]").first().getByRole("button", { name: /^More actions/ }),
  });
  const press = (loc) => (vp.mobile ? loc.tap() : loc.click());
  // An article opened from the list (by its address, so no tap lands on whatever the list scrolled to).
  const openArticle = async () => {
    await open("/l/unread");
    await page.goto(await page.locator("main article[data-item-id] a").first().getAttribute("href"), { waitUntil: "load" });
    await page.locator("#article-title").waitFor({ timeout: 10000 });
    await page.waitForTimeout(400);
  };
  const panelBox = () =>
    page.evaluate(() => {
      const d = document.querySelector('[role="dialog"][aria-label="Reading appearance"]');
      const r = d.getBoundingClientRect();
      const title = document.getElementById("article-title").getBoundingClientRect();
      return { sheet: d.hasAttribute("data-menu-sheet"), top: r.top, bottom: r.bottom, height: r.height, width: r.width, h: innerHeight, titleBottom: title.bottom, dim: !!document.querySelector("[data-menu-backdrop]") };
    });
  const panel = () => page.getByRole("dialog", { name: "Reading appearance" });
  const menu = () => page.getByRole("menu");

  if (!vp.mobile) {
    await step(`${tag} D1`, async () => {
      await open("/l/unread");
      const b = buttons();
      for (const id of ["layout", "length", "row"]) {
        await press(b[id]);
        await menu().waitFor({ timeout: 5000 });
        await page.waitForTimeout(300);
        const m = await menuBox();
        check(!m.sheet && !m.dim && m.width < 400 && m.bottom < m.win.h - 1, `${tag} D1 ${id}`, `the ${id} menu is a ${Math.round(m.width)} px popover`, `the ${id} menu is a sheet or pinned to the bottom (sheet ${m.sheet}, dimmed ${m.dim}, ${Math.round(m.width)} px wide, bottom ${Math.round(m.bottom)})`);
        if (id === "layout") await t.shot(page, `${tag}-layout-popover`);
        await page.keyboard.press("Escape");
        await menu().waitFor({ state: "detached", timeout: 5000 });
      }
    });
    await step(`${tag} A2`, async () => {
      await openArticle();
      const aa = page.getByRole("button", { name: "Reading appearance" });
      const n = await aa.count();
      check(n === 1, `${tag} A2 one`, "beside an open article there is one Reading appearance button", `${n} buttons`);
      await aa.first().click();
      await panel().waitFor({ timeout: 5000 });
      const m = await panelBox();
      check(!m.sheet && m.width < 400 && m.bottom < m.h - 1, `${tag} A2 popover`, `the panel is a ${Math.round(m.width)} px popover`, JSON.stringify(m));
      await page.keyboard.press("Escape");
    });
    return;
  }

  for (const [label, size] of [["portrait", [375, 812]], ["landscape", [852, 393]]]) {
    await page.setViewportSize({ width: size[0], height: size[1] });
    await open("/l/unread");
    const b = buttons();
    for (const id of ["layout", "length", "row"]) {
      await step(`${tag} ${label} ${id}`, async () => {
        const name = `${tag} ${label} ${id}`;
        await press(b[id]);
        await menu().waitFor({ timeout: 5000 });
        await page.waitForTimeout(400);
        const m = await menuBox();
        const full = Math.abs(m.width - Math.min(m.win.w, 512)) <= 1 && Math.abs(m.bottom - m.win.h) <= 1;
        check(m.sheet && full && m.top >= 0 && m.height <= 0.7 * m.win.h + 1 && m.dim, `P1 ${name}`, `the menu is a sheet ${Math.round(m.width)}x${Math.round(m.height)} px on the bottom edge`, `the menu is not a bottom sheet (sheet ${m.sheet}, ${Math.round(m.left)}-${Math.round(m.right)} x ${Math.round(m.top)}-${Math.round(m.bottom)} in ${m.win.w}x${m.win.h}, dimmed ${m.dim})`);
        check(m.focusIn, `P2 ${name} focus`, "focus is inside the sheet", "focus is outside the sheet");
        // A toast or the save notice (both fixed, z-50) must not cover the bottom of the sheet or take its taps.
        const z = await page.evaluate(() => {
          const n = (el) => Number(getComputedStyle(el).zIndex);
          return { menu: n(document.querySelector('[role="menu"]')), dim: n(document.querySelector("[data-menu-backdrop]")), toast: n(document.querySelector('[data-testid="toast-region"]')) };
        });
        check(z.menu > z.toast && z.dim > z.toast, `P5 ${name}`, "the sheet and its dimmed page are above the toasts", `z-index: sheet ${z.menu}, dimmed page ${z.dim}, toasts ${z.toast}`);
        if (id === "layout") {
          const text = await menu().innerText();
          check(/Layout/i.test(text) && /Order/i.test(text), `P1 ${name} sections`, "the sheet names its Layout and Order sections", `the sheet text is ${text.replace(/\s+/g, " ").slice(0, 80)}`);
        }
        await t.shot(page, `${tag}-${label}-${id}`);

        if (id === "layout" && label === "landscape") {
          const last = menu().getByRole("menuitemradio").last();
          await last.scrollIntoViewIfNeeded();
          const r = await last.evaluate((el) => {
            const x = el.getBoundingClientRect();
            return { top: x.top, bottom: x.bottom, h: innerHeight };
          });
          check(r.top >= 0 && r.bottom <= r.h + 1, `P4 ${name} reach`, "the last choice scrolls into view", `the last choice sits at ${Math.round(r.top)}-${Math.round(r.bottom)} of ${r.h} px`);
          await t.shot(page, `${tag}-${label}-layout-scrolled`);
        }

        await page.keyboard.press("Escape");
        await menu().waitFor({ state: "detached", timeout: 5000 });
        const want = await b[id].getAttribute("aria-label");
        // Radix returns focus a tick after the menu is gone, so wait for it rather than read the moment the menu detaches.
        await page.waitForFunction((w) => document.activeElement?.getAttribute("aria-label") === w, want, { timeout: 2000 }).catch(() => {});
        const back = await page.evaluate(() => document.activeElement?.getAttribute("aria-label") ?? document.activeElement?.tagName);
        check(back === want, `P2 ${name} return`, `focus returns to "${want}"`, `focus is on "${back}"`);

        // A tap on the dimmed page closes the sheet and does not open the row under it.
        await press(b[id]);
        await menu().waitFor({ timeout: 5000 });
        const before = page.url();
        await page.touchscreen.tap(size[0] / 2, 12);
        await menu().waitFor({ state: "detached", timeout: 5000 });
        await page.waitForTimeout(300);
        check(page.url() === before, `P3 ${name}`, "a tap on the dimmed page only closes the sheet", `the address changed to ${page.url()}`);
      });
    }
  }

  await step(`${tag} P4 choice`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/l/unread");
    const openMenu = async () => {
      await page.getByRole("button", { name: LAYOUT }).tap();
      await menu().waitFor({ timeout: 5000 });
    };
    await openMenu();
    await menu().getByRole("menuitemradio", { name: "Oldest first" }).tap();
    await menu().waitFor({ state: "detached", timeout: 5000 });
    await openMenu();
    const oldest = await menu().getByRole("menuitemradio", { name: "Oldest first" }).getAttribute("aria-checked");
    check(oldest === "true", `${tag} P4 oldest`, "a tap on Oldest first applies it", `Oldest first is aria-checked ${oldest}`);
    await menu().getByRole("menuitemradio", { name: "Newest first" }).tap();
    await menu().waitFor({ state: "detached", timeout: 5000 });
  });

  for (const [label, size] of [["portrait", [375, 812]], ["landscape", [852, 393]]]) {
    await step(`${tag} A1 ${label}`, async () => {
      await page.setViewportSize({ width: size[0], height: size[1] });
      await openArticle();
      await page.getByRole("toolbar", { name: "Article actions" }).getByRole("button", { name: "Reading appearance" }).tap();
      await panel().waitFor({ timeout: 5000 });
      await page.waitForTimeout(300);
      const m = await panelBox();
      check(m.sheet && Math.abs(m.bottom - m.h) <= 1 && m.height <= 0.45 * m.h + 1 && !m.dim, `${tag} A1 ${label} sheet`, "the panel is a short sheet on the bottom edge over an undimmed page", JSON.stringify(m));
      check(m.titleBottom <= m.top, `${tag} A1 ${label} title`, "the article title shows above the sheet", `title ends at ${Math.round(m.titleBottom)}, the sheet starts at ${Math.round(m.top)}`);
      await t.shot(page, `${tag}-aa-sheet-${label}`);
      await page.keyboard.press("Escape");
      await panel().waitFor({ state: "detached", timeout: 5000 });
    });
  }

  await step(`${tag} P6 focus leaves`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/l/unread");
    await page.getByRole("button", { name: LAYOUT }).tap();
    await menu().waitFor({ timeout: 5000 });
    // A screen reader moving to an article row: well after the opening tap, focus lands on the page behind the sheet.
    await page.waitForTimeout(700);
    await page.evaluate(() => document.querySelector("a.row-link")?.focus());
    await menu().waitFor({ state: "detached", timeout: 5000 }).catch(() => {});
    const still = await menu().count();
    check(still === 0, `${tag} P6`, "the sheet closes when focus moves to the page behind it", "the sheet is still open over the page");
    await t.shot(page, `${tag}-focus-left-sheet`);
  });
});

await t.finish("touch-menus");
