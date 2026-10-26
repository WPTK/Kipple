// UAT: the shared controls (issues #609, #619, #627, #628, #686).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:kit                    in another
//   npm run uat:kit -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// At each width (desktop 1280x800, phone 375x812) it checks:
//   K1  an open menu is inside the main landmark, and axe-core finds no aria-hidden-focus or region violation with it open;
//   K2  the password fields of Settings have a Show control that reveals and hides the value, and no "again" field; (uat-labels: ignore)
//   K3  a keyboard-focused control draws the accent outline with a page-colored gap (the one focus ring);
//   K4  an input's border is the field color, not the hairline color;
//   K5  a keyboard-focused list row draws its ring around the row and the link gets no box of its own;
//   K6  a destructive button underlines on hover while it has keyboard focus (#728);
//   K7  a number setting (how often to check feeds) is a row of choices with Custom, not a bare stepper (#686);
//   K8  every row of the Show and sort and Article length menus is the one menu row (44 px tall, rounded, highlight on focus) (#627).
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { start } from "./common.mjs";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const t = await start(import.meta.url, 14);
const { check, step, textOf } = t;

/** Opens the Change web password dialog on Settings, Account & Devices; returns the Current password input. */ // uat-labels: ignore
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
    const opener = page.getByRole("button", { name: "Show and sort" });
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
    // Each Show button is named after its field and controls its own input: two identical buttons cannot be told apart.
    const names = await page.getByRole("button", { name: /^Show .*password$/ }).evaluateAll((bs) => bs.map((b) => `${b.getAttribute("aria-label")}>${b.getAttribute("aria-controls") === b.parentElement.querySelector("input").id}`));
    check(names.length >= 2 && new Set(names).size === names.length && names.every((x) => x.endsWith(">true")), `${tag} K2 names`, "each Show button has its own name and controls its own input", names.join(" | "));
    const show = cur.locator("xpath=..").getByRole("button", { name: "Show current password" });
    await show.click();
    check((await cur.getAttribute("type")) === "text", `${tag} K2 show`, "Show reveals the value", `type is ${await cur.getAttribute("type")}`);
    await cur.locator("xpath=..").getByRole("button", { name: "Hide current password" }).click();
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
    const gap = r && /\s(\d+(?:\.\d+)?)px$/.exec(r.shadow);
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
    await page.goto("/l/unread", { waitUntil: "load" });
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

  // Hover exists with a mouse only: on the phone the browser reports hover: none and the hover styles are off.
  if (tag === "desktop") await step(`${tag} K6`, async () => {
    await page.goto("/settings/account", { waitUntil: "load" });
    const danger = page.locator("button.text-danger").first();
    await danger.waitFor({ timeout: 15000 });
    await danger.scrollIntoViewIfNeeded();
    await page.keyboard.press("Tab");
    await danger.focus();
    await danger.hover();
    const r = await danger.evaluate((el) => ({ focused: el.matches(":focus-visible"), line: getComputedStyle(el).textDecorationLine }));
    check(r.focused && r.line.includes("underline"), `${tag} K6`, "a focused destructive button underlines on hover", `focused ${r.focused}, text-decoration ${r.line}`);
    await t.shot(page, `${tag}-danger-hover-focus`);
  });

  await step(`${tag} K7`, async () => {
    await page.goto("/settings/sync", { waitUntil: "load" });
    const group = page.getByRole("group", { name: "How often to check feeds" });
    await group.waitFor({ timeout: 15000 });
    const checked = await group.getByRole("radio", { checked: true }).count();
    const custom = await group.getByRole("radio", { name: "Custom" }).count();
    const stepper = await page.getByRole("spinbutton", { name: "How often to check feeds" }).count();
    check(checked === 1 && custom === 1 && stepper === 0, `${tag} K7`, "the interval is one chosen preset with Custom, and no stepper until asked", `checked ${checked}, custom ${custom}, steppers ${stepper}`);
    await t.shot(page, `${tag}-number-presets`);
  });

  await step(`${tag} K8`, async () => {
    await page.goto("/l/unread", { waitUntil: "load" });
    for (const name of [/^Show and sort/, /^Article length/]) {
      const opener = page.getByRole("button", { name });
      await opener.waitFor({ timeout: 15000 });
      await opener.click();
      const menu = page.getByRole("menu");
      await menu.waitFor({ timeout: 10000 });
      const rows = await menu.locator("[role=menuitem],[role=menuitemradio]").evaluateAll((els) => els.map((e) => ({ h: Math.round(e.getBoundingClientRect().height), r: getComputedStyle(e).borderTopLeftRadius })));
      const bad = rows.filter((r) => r.h < 44 || r.r === "0px");
      check(rows.length > 0 && bad.length === 0, `${tag} K8 ${name.source.slice(1, 8)}`, `${rows.length} rows, each at least 44 px tall and rounded`, `rows off the menu row: ${JSON.stringify(bad)}`);
      await t.shot(page, `${tag}-menu-${name.source.slice(1, 5).trim()}`);
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached", timeout: 5000 });
    }
  });
});

await t.finish("menus sit in the main landmark, the password field has Show, focus is drawn, and inputs have a visible border");
