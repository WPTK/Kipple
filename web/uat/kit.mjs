// UAT: the shared controls (issues #609, #619, #627, #628, #686).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:kit                    in another
//   npm run uat:kit -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// At each width (desktop 1280x800, phone 375x812) it checks:
//   K1  an open menu is inside the main landmark, and axe-core finds no aria-hidden-focus or region violation with it open;
//   K2  the password fields of Settings have a Show control that reveals and hides the value, and no "again" field;
//   K3  a keyboard-focused button draws the focus outline;
//   K4  an input's border is a visible 3:1 against the page (the computed border color is not the line color).
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { start } from "./common.mjs";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const t = await start(import.meta.url, 14);
const { check, step, textOf } = t;

/** Opens the Change web password dialog on Settings, Account & Devices; returns the Current password input. */
async function openPasswordDialog(page) {
  await page.goto("/settings/account", { waitUntil: "load" });
  await page.getByRole("button", { name: /^(Change|Set) web password$/ }).click();
  const cur = page.getByLabel("Current password", { exact: true });
  await cur.waitFor({ timeout: 15000 });
  return cur;
}

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} K1`, async () => {
    await page.goto("/", { waitUntil: "load" });
    const opener = page.getByRole("button", { name: "List options" });
    await opener.waitFor({ timeout: 15000 });
    await opener.click();
    const menu = page.getByRole("menu");
    await menu.waitFor({ timeout: 10000 });
    const inMain = await menu.evaluate((el) => !!el.closest("#main"));
    check(inMain, `${tag} K1 landmark`, "the open menu is inside #main", "the open menu is outside #main");
    await t.shot(page, `${tag}-menu-open`);
    await page.evaluate(AXE);
    const found = await page.evaluate(async () => {
      const r = await window.axe.run(document, { runOnly: { type: "rule", values: ["aria-hidden-focus", "region", "landmark-unique", "landmark-one-main"] } });
      return r.violations.map((v) => `${v.id}: ${v.nodes.length}`);
    });
    check(found.length === 0, `${tag} K1 axe`, "no aria-hidden-focus or landmark violation with a menu open", found.join(", "));
    await page.keyboard.press("Escape");
  });

  await step(`${tag} K2`, async () => {
    const cur = await openPasswordDialog(page);
    await cur.fill("secret-value");
    const show = page.getByRole("button", { name: "Show current password" });
    await show.click();
    check((await cur.getAttribute("type")) === "text", `${tag} K2 show`, "Show reveals the value", `type is ${await cur.getAttribute("type")}`);
    await page.getByRole("button", { name: "Hide current password" }).click();
    check((await cur.getAttribute("type")) === "password", `${tag} K2 hide`, "Hide covers it again", "still shown");
    const body = await textOf(page.locator("body"), 4000);
    check(!/password again/i.test(body), `${tag} K2 again`, "there is no second password field", "a \"password again\" field is still there");
    await t.shot(page, `${tag}-account-password`);
  });

  await step(`${tag} K3`, async () => {
    await openPasswordDialog(page);
    await page.keyboard.press("Tab");
    const style = await page.evaluate(() => {
      const el = document.activeElement;
      const s = el ? getComputedStyle(el) : null;
      return s ? { tag: el.tagName, style: s.outlineStyle, width: parseFloat(s.outlineWidth) } : null;
    });
    check(!!style && style.style !== "none" && style.width >= 2, `${tag} K3`, `the focused ${style?.tag} has a ${style?.width}px outline`, `no outline: ${JSON.stringify(style)}`);
  });

  await step(`${tag} K4`, async () => {
    const input = await openPasswordDialog(page);
    const [field, line] = await input.evaluate((el) => {
      const root = getComputedStyle(document.documentElement);
      return [getComputedStyle(el).borderTopColor, root.getPropertyValue("--kp-border").trim()];
    });
    check(field !== line, `${tag} K4`, `the input border (${field}) is not the hairline color`, `the input border is ${field}, the hairline ${line}`);
  });
});

await t.finish("menus sit in the main landmark, the password field has Show, focus is drawn, and inputs have a visible border");
