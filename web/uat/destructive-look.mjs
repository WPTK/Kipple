// UAT: every destructive action has the one danger look, and the shared tick box and disclosure are used.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/destructive-look.mjs      in another
//   node uat/destructive-look.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   B1  in Select mode on Feeds the bottom bar's Delete is the danger button and sits last;
//   B2  Delete folder in a folder's menu is the last row, apart behind a separator, in the danger colour; reached with
//       the End key it shows an underline and the focus ring, and no selection fill;
//   B3  Run setup again in Settings, Account is not destructive: it has the normal button look, no danger text or border;
//   B4  Feed health in Select mode shows the shared tick boxes (20 px, named "Select ...") and holds no native details
//       element; the error text of a failing feed opens from the shared disclosure's Details button.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { expect } from "@playwright/test";
import { startSelect, start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

const color = (loc) => loc.evaluate((el) => getComputedStyle(el).color);
// The scheme's danger colour as the browser computes it (the token is a hex string; a probe resolves it to rgb()).
const dangerColor = (page) =>
  page.evaluate(() => {
    const p = document.createElement("span");
    p.style.color = "var(--kp-danger)";
    document.body.append(p);
    const c = getComputedStyle(p).color;
    p.remove();
    return c;
  });

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;

  await step(`${tag} B1`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await startSelect(page);
    const bar = page.getByRole("region", { name: "Selected feeds", exact: true });
    const del = bar.getByRole("button", { name: "Delete", exact: true });
    await del.waitFor({ timeout: 10000 });
    const last = await bar.getByRole("button").last().textContent();
    check(last?.trim() === "Delete", `${tag} B1 last`, "Delete is the last button of the select bar", `the last button is ${last}`);
    const border = await del.evaluate((el) => getComputedStyle(el).borderTopColor);
    const danger = await dangerColor(page);
    check(border === danger && (await color(del)) === danger, `${tag} B1 look`, "Delete has danger text and a danger border", `border ${border}, text ${await color(del)}, danger ${danger}`);
    await t.shot(page, `${tag}-select-bar-delete`);
  });

  await step(`${tag} B2`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    const buttons = page.getByRole("button", { name: /^Folder actions for / });
    await buttons.first().waitFor({ timeout: 15000 });
    const menu = page.getByRole("menu");
    let row = null;
    for (let i = 0; i < (await buttons.count()) && !row; i++) {
      await buttons.nth(i).click();
      await menu.waitFor({ timeout: 10000 });
      const items = menu.getByRole("menuitem");
      const n = await items.count();
      if (((await items.nth(n - 1).textContent()) ?? "").trim() === "Delete folder") row = items.nth(n - 1);
      else await page.keyboard.press("Escape");
    }
    check(row !== null, `${tag} B2 row`, "a folder menu ends with Delete folder", "no folder menu has Delete folder");
    if (!row) return;
    check((await menu.getByRole("separator").count()) > 0, `${tag} B2 apart`, "a separator sits before Delete folder", "no separator in the folder menu");
    const danger = await dangerColor(page);
    await page.keyboard.press("End");
    await expect(row).toHaveAttribute("data-highlighted", "", { timeout: 5000 });
    const s = await row.evaluate((el) => {
      const c = getComputedStyle(el);
      return { text: c.color, deco: c.textDecorationLine, bg: c.backgroundColor, outStyle: c.outlineStyle, outColor: c.outlineColor, outW: c.outlineWidth };
    });
    check(s.text === danger, `${tag} B2 colour`, "the row text is the danger colour", `text ${s.text}, danger ${danger}`);
    check(s.deco.includes("underline"), `${tag} B2 underline`, "the highlighted row is underlined", `text-decoration ${s.deco}`);
    check(s.bg === "rgba(0, 0, 0, 0)", `${tag} B2 fill`, "the highlighted row has no selection fill", `background ${s.bg}`);
    check(s.outStyle === "solid" && s.outW !== "0px", `${tag} B2 ring`, "the highlighted row shows the focus ring", `outline ${s.outStyle} ${s.outW} ${s.outColor}`);
    await t.shot(page, `${tag}-delete-folder-row`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} B3`, async () => {
    await page.goto("/settings/account", { waitUntil: "load" });
    const btn = page.getByRole("button", { name: "Run setup again", exact: true });
    await btn.waitFor({ timeout: 10000 });
    const danger = await dangerColor(page);
    const border = await btn.evaluate((el) => getComputedStyle(el).borderTopColor);
    check(border !== danger && (await color(btn)) !== danger, `${tag} B3 look`, "Run setup again has the normal button look, not the danger look", `border ${border}, text ${await color(btn)}, danger ${danger}`);
  });

  await step(`${tag} B4`, async () => {
    await page.goto("/health", { waitUntil: "load" });
    await page.getByTestId("health-summary").waitFor({ timeout: 15000 });
    await page.getByRole("button", { name: "Select", exact: true }).click();
    const box = page.getByRole("checkbox", { name: /^Select / }).first();
    await box.waitFor({ timeout: 15000 });
    const w = await box.evaluate((el) => el.getBoundingClientRect().width);
    check(w === 20, `${tag} B4 box`, "the tick box is the shared 20 px box", `width ${w}`);
    check((await page.locator("details").count()) === 0, `${tag} B4 details`, "no native details element on Feed health", "a native details element is left");
    const details = page.getByRole("button", { name: "Details", exact: true }).first();
    await details.waitFor({ timeout: 15000 });
    await details.click();
    await expect(details).toHaveAttribute("aria-expanded", "true", { timeout: 5000 });
    const panel = page.locator(`[id="${await details.getAttribute("aria-controls")}"]`);
    const text = ((await panel.textContent()) ?? "").trim().length > 0 && (await panel.isVisible());
    check(text, `${tag} B4 open`, "Details opens the server's error text", "the error text is not visible after opening Details");
    await t.shot(page, `${tag}-health-details`);
  });
});

await t.finish("destructive look");
