// UAT: one Section, one number control (#627, #686) and the List options sheet on a short phone (#660).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/kit-adopt.mjs             in another
//   node uat/kit-adopt.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   K1  Settings, Sync: every section is a landmark named by its heading, the headings are one level (h2 on a phone, h3 beside the
//       rail), and the page does not scroll sideways;
//   K2  Mark old articles as read, Preview: "Try a different number of days" is the number control (a spinbutton with Decrease and
//       Increase buttons), not a free text box, and Increase moves it by one without saving anything;
//   K3  (phone, 375x667) the List options sheet is held to the screen, and Order and Group by can both be reached by scrolling it
//       and are then inside the screen.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  page.setDefaultTimeout(8000);
  const sideways = () => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

  await step(`${tag} K1`, async () => {
    await page.goto("/settings/sync", { waitUntil: "load" });
    await page.locator("main section").first().waitFor({ timeout: 15000 });
    const info = await page.locator("main section").evaluateAll((els) =>
      els.map((s) => {
        const h = document.getElementById(s.getAttribute("aria-labelledby") ?? "");
        return h ? h.tagName : "";
      }),
    );
    const levels = [...new Set(info)];
    const want = vp.mobile ? "H2" : "H3";
    check(info.length > 0 && levels.length === 1 && levels[0] === want, `${tag} K1 headings`, `${info.length} sections, each named by an ${want} heading`, `heading tags: ${info.join(",")}`);
    check((await sideways()) <= 0, `${tag} K1 fit`, "the page does not scroll sideways", `${await sideways()} px sideways`);
    await t.shot(page, `${tag}-settings-sync`);
  });

  await step(`${tag} K2`, async () => {
    await page.goto("/settings/sync", { waitUntil: "load" });
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    const dlg = page.getByRole("dialog", { name: "Mark old articles as read" });
    await dlg.waitFor();
    const box = dlg.getByRole("spinbutton", { name: "Try a different number of days" });
    await box.waitFor();
    check((await dlg.getByRole("textbox", { name: "Try a different number of days" }).count()) === 0, `${tag} K2 control`, "the what-if number is a number control", "it is still a free text box");
    const before = Number(await box.inputValue());
    await dlg.getByRole("button", { name: "Increase Try a different number of days" }).click();
    await page.waitForFunction(([b]) => [...document.querySelectorAll('input[type="number"]')].some((i) => Number(i.value) === b + 1), [before]);
    check(Number(await box.inputValue()) === before + 1, `${tag} K2 step`, "Increase adds one", `${before} then ${await box.inputValue()}`);
    await t.shot(page, `${tag}-what-if`);
    await dlg.getByRole("button", { name: "Close" }).click();
  });

  if (vp.mobile) {
    await step(`${tag} K3`, async () => {
      await page.setViewportSize({ width: 375, height: 667 });
      await page.goto("/l/unread", { waitUntil: "load" });
      await page.locator('button[aria-label^="Show and sort, "]').first().click();
      const menu = page.getByRole("menu");
      await menu.waitFor();
      const room = await menu.evaluate((m) => ({ h: m.getBoundingClientRect().height, scrolls: m.scrollHeight > m.clientHeight + 1, vh: innerHeight }));
      check(room.h <= room.vh * 0.71, `${tag} K3 held`, "the sheet is at most 70% of the screen", `${room.h} of ${room.vh}`);
      for (const name of ["Order", "Group by"]) {
        const group = menu.getByRole("group", { name, exact: true });
        await group.scrollIntoViewIfNeeded();
        const r = await group.evaluate((g) => {
          const b = g.getBoundingClientRect();
          const m = g.closest('[role="menu"]').getBoundingClientRect();
          return { top: b.top, bottom: b.bottom, mTop: m.top, mBottom: m.bottom, vh: innerHeight };
        });
        check(r.top >= r.mTop - 1 && r.bottom <= r.mBottom + 1 && r.bottom <= r.vh + 1, `${tag} K3 ${name}`, `${name} can be scrolled to and is inside the sheet and the screen`, JSON.stringify(r));
      }
      await t.shot(page, `${tag}-sheet-667`);
      await page.keyboard.press("Escape");
      await page.setViewportSize({ width: vp.width, height: vp.height });
    });
  }
});

await t.finish("kit-adopt");
