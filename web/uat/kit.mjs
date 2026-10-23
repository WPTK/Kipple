// UAT: the shared controls (issues #609, #619, #627, #628, #686).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:kit                    in another
//   npm run uat:kit -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// At each width (desktop 1280x800, phone 375x812) it checks:
//   K1  an open menu is inside the main landmark, and axe-core finds no aria-hidden-focus or region violation with it open;
//   K2  the password fields of Settings have a Show control that reveals and hides the value, and no "again" field;
//   K3  a keyboard-focused control draws the accent outline with a page-colored gap (the one focus ring);
//   K4  an input's border is the field color, not the hairline color;
//   K5  a keyboard-focused list row draws its ring around the row and the link gets no box of its own.
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
    const show = page.getByRole("button", { name: "Show password" });
    await show.click();
    check((await cur.getAttribute("type")) === "text", `${tag} K2 show`, "Show reveals the value", `type is ${await cur.getAttribute("type")}`);
    await page.getByRole("button", { name: "Hide password" }).click();
    check((await cur.getAttribute("type")) === "password", `${tag} K2 hide`, "Hide covers it again", "still shown");
    const body = await textOf(page.locator("body"), 4000);
    check(!/password again/i.test(body), `${tag} K2 again`, "there is no second password field", "a \"password again\" field is still there");
    await t.shot(page, `${tag}-account-password`);
  });

  await step(`${tag} K3`, async () => {
    await openPasswordDialog(page);
    await page.keyboard.press("Tab");
    const r = await page.evaluate(() => {
      // A scheme token as the rgb() the browser draws it in: put it on a probe element and read it back.
      const rgb = (token) => {
        const p = document.createElement("i");
        p.style.color = `var(--kp-${token})`;
        document.body.append(p);
        const v = getComputedStyle(p).color;
        p.remove();
        return v;
      };
      const el = document.activeElement;
      const s = el ? getComputedStyle(el) : null;
      return s && { tag: el.tagName, outlineColor: s.outlineColor, outlineWidth: parseFloat(s.outlineWidth), shadow: s.boxShadow, accent: rgb("accent"), bg: rgb("bg") };
    });
    // The one two-tone ring: an accent outline with a gap of the page color between it and the control (#619).
    const gap = r && /s(d+(?:.d+)?)px$/.exec(r.shadow);
    check(!!r && r.outlineColor === r.accent && r.outlineWidth >= 2 && r.shadow.startsWith(r.bg) && !!gap && Number(gap[1]) >= 2, `${tag} K3`, `the focused ${r?.tag} has an accent outline over a ${gap?.[1]}px page-colored gap`, `not the two-tone ring: ${JSON.stringify(r)}`);
  });

  await step(`${tag} K4`, async () => {
    const input = await openPasswordDialog(page);
    const r = await input.evaluate((el) => {
      const rgb = (token) => {
        const p = document.createElement("i");
        p.style.color = `var(--kp-${token})`;
        document.body.append(p);
        const v = getComputedStyle(p).color;
        p.remove();
        return v;
      };
      return { border: getComputedStyle(el).borderTopColor, field: rgb("field"), line: rgb("border") };
    });
    // The input's boundary is the field token (3:1 against the page, tested with the schemes), not the hairline.
    check(r.border === r.field && r.field !== r.line, `${tag} K4`, `the input border is the field color ${r.field}, not the hairline ${r.line}`, `the input border is ${r.border}; field ${r.field}, hairline ${r.line}`);
  });

  await step(`${tag} K5`, async () => {
    await page.goto("/", { waitUntil: "load" });
    const row = page.locator("a.row-link").first();
    await row.waitFor({ timeout: 15000 });
    await page.keyboard.press("Tab");
    await row.focus();
    const r = await row.evaluate((el) => {
      const s = getComputedStyle(el);
      const b = getComputedStyle(el, "::before");
      return { focused: el.matches(":focus-visible"), shadow: s.boxShadow, outline: s.outlineStyle, ring: parseFloat(b.outlineWidth), ringStyle: b.outlineStyle };
    });
    // The row draws its ring on the row's ::before: the link itself has neither an outline nor the two-tone gap's box (#619).
    check(r.focused && r.shadow === "none" && r.outline === "none" && r.ring >= 2 && r.ringStyle === "solid", `${tag} K5`, "a focused row link has no box of its own and draws the ring around the row", `focused row link: ${JSON.stringify(r)}`);
  });
});

await t.finish("menus sit in the main landmark, the password field has Show, focus is drawn, and inputs have a visible border");
