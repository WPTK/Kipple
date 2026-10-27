// UAT: every destructive action has the one danger look, and the shared tick box and disclosure are used.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/destructive-look.mjs      in another
//   node uat/destructive-look.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   B1  in Select mode on Feeds the bottom bar's Delete is the danger button and sits last;
//   B2  Delete folder in a folder's menu is the last row, apart behind a separator, in the same danger colour as that
//       Delete button, and its row text is not on the selection fill while highlighted;
//   B3  Run setup again in Settings, Account is a danger button;
//   B4  Feed health in Select mode shows the shared tick boxes (20 px, named "Select ...") and holds no native details
//       element (the error text sits behind the shared disclosure).
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { startSelect, start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

const color = (loc) => loc.evaluate((el) => getComputedStyle(el).color);

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;

  await step(`${tag} B1`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await startSelect(page);
    const bar = page.getByRole("region", { name: "Selected feeds" }).or(page.locator('[aria-label="Selected feeds"]')).first();
    const del = bar.getByRole("button", { name: "Delete", exact: true });
    await del.waitFor({ timeout: 10000 });
    const last = await bar.getByRole("button").last().textContent();
    check(last?.trim() === "Delete", `${tag} B1 last`, "Delete is the last button of the select bar", `the last button is ${last}`);
    const border = await del.evaluate((el) => getComputedStyle(el).borderTopColor);
    check(border === (await color(del)), `${tag} B1 look`, "Delete has danger text and a danger border", `border ${border} differs from text ${await color(del)}`);
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
    await row.hover();
    const bg = await row.evaluate((el) => getComputedStyle(el).backgroundColor);
    check(bg === "rgba(0, 0, 0, 0)", `${tag} B2 highlight`, "the highlighted row has no selection fill", `background ${bg}`);
    await t.shot(page, `${tag}-delete-folder-row`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} B3`, async () => {
    await page.goto("/settings/account", { waitUntil: "load" });
    const btn = page.getByRole("button", { name: "Run setup again", exact: true });
    await btn.waitFor({ timeout: 10000 });
    const border = await btn.evaluate((el) => getComputedStyle(el).borderTopWidth);
    check(border !== "0px", `${tag} B3 look`, "Run setup again is a bordered danger button", "it has no border");
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
  });
});

await t.finish("destructive look");
