// UAT: on a small laptop the sidebar is an icon rail and the reader keeps a readable column.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/pane-widths.mjs           in another
//   node uat/pane-widths.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the desktop run, with an article open beside the list:
//   P1  at 1024 px the sidebar is a rail (about 64 px) with a Show sidebar button, and the article pane is at least
//       480 px wide;
//   P2  Show sidebar opens the sidebar, focus lands on the Hide sidebar button that replaces it, and the choice is still
//       there after a reload (it is kept on this device); Hide sidebar goes back and focus lands on Show sidebar;
//   P3  at 1100 px the sidebar is the full one (240 px) with no toggle, whatever was kept.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 15);
const { check, step } = t;

const patch = (page, b) =>
  page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);

await t.eachViewport(async (page, vp) => {
  if (vp.mobile) return;
  page.setDefaultTimeout(8000);
  const open = async (w) => {
    await page.setViewportSize({ width: w, height: 768 });
    await page.goto("/l/all", { waitUntil: "load" });
    const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
    await row.waitFor({ timeout: 15000 });
    await page.goto(await row.locator("a").first().getAttribute("href"), { waitUntil: "load" });
    await page.getByRole("toolbar", { name: "Article actions" }).waitFor({ timeout: 10000 });
  };
  const geometry = () =>
    page.evaluate(() => {
      const nav = document.querySelector('nav[aria-label="Primary"]');
      const bar = document.querySelector('[role="toolbar"][aria-label="Article actions"]');
      return {
        sidebar: nav ? Math.round(nav.getBoundingClientRect().width) : 0,
        reader: bar ? Math.round(bar.getBoundingClientRect().width) : 0,
        focus: document.activeElement?.getAttribute("aria-label") ?? document.activeElement?.tagName ?? "",
      };
    });
  await patch(page, { "client.sidebar_expanded": null });

  await step("rail at 1024", async () => {
    await open(1024);
    const g = await geometry();
    check(await page.getByRole("button", { name: "Show sidebar" }).isVisible(), "P1 toggle", "a Show sidebar button is on screen at 1024 px", "there is none");
    check(g.sidebar <= 80, "P1 rail", "the sidebar is an icon rail at 1024 px", `the sidebar is ${g.sidebar} px wide`);
    check(g.reader >= 479, "P1 reader", "the article pane is at least 480 px wide at 1024 px", `the article pane is ${g.reader} px wide`);
    await t.shot(page, "pane-widths-1024-rail");
  });

  await step("open and close the rail", async () => {
    await page.getByRole("button", { name: "Show sidebar" }).focus();
    await page.keyboard.press("Enter");
    await page.getByRole("button", { name: "Hide sidebar" }).waitFor();
    let g = await geometry();
    check(g.focus === "Hide sidebar", "P2 focus open", "focus is on Hide sidebar after Show sidebar", `focus is on ${g.focus}`);
    check(g.sidebar >= 200, "P2 open", "the sidebar is the full one once opened", `the sidebar is ${g.sidebar} px wide`);
    await t.shot(page, "pane-widths-1024-open");
    await page.reload({ waitUntil: "load" });
    await page.getByRole("button", { name: "Hide sidebar" }).waitFor();
    check(true, "P2 kept", "the open sidebar is kept after a reload", "");
    await page.getByRole("button", { name: "Hide sidebar" }).focus();
    await page.keyboard.press("Enter");
    await page.getByRole("button", { name: "Show sidebar" }).waitFor();
    g = await geometry();
    check(g.focus === "Show sidebar", "P2 focus close", "focus is on Show sidebar after Hide sidebar", `focus is on ${g.focus}`);
  });

  await step("full sidebar at 1100", async () => {
    await patch(page, { "client.sidebar_expanded": false });
    await open(1100);
    const g = await geometry();
    check((await page.getByRole("button", { name: /sidebar$/ }).count()) === 0, "P3 toggle", "no sidebar toggle at 1100 px", "a toggle is on screen");
    check(g.sidebar >= 200, "P3 sidebar", "the sidebar is the full one at 1100 px", `the sidebar is ${g.sidebar} px wide`);
  });
  await patch(page, { "client.sidebar_expanded": null });
});

await t.finish("pane-widths");
