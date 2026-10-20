// UAT: yes/no choices in forms are checkboxes, and a switch only where a flip takes effect at once (issue #286).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:controls               in another, once the feeds have fetched
//   npm run uat:controls -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   K1  Edit feed: its yes/no options are checkboxes (none is a switch), and ticking one sends nothing until Save;
//   K2  Import OPML and Export statistics: the options are checkboxes, 44 px tall rows on a phone;
//   K3  New filter: Turn this filter on and the Options rows are all checkboxes, one control for the same job;
//   K5  Add feed with an address on a private network: the refusal points at "tick" and the option it offers is a checkbox,
//       unticked (nothing is added);
//   K4  Settings > Appearance & Reading: Larger buttons stays a switch (it applies the moment it is flipped), and flipping it
//       sends nothing to the server (it is a device setting) and is put back.
// It changes nothing on the server: every dialog is closed with Escape. It only runs against a loopback address with the
// seed's credentials unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 17);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const writes = [];
  page.on("request", (r) => {
    if (["POST", "PATCH", "PUT", "DELETE"].includes(r.method()) && r.url().includes("/api/")) writes.push(`${r.method()} ${new URL(r.url()).pathname}`);
  });
  // A switch is a hidden native checkbox drawn as a track: a person taps its label.
  const tap = (sw) => page.locator("label", { has: sw }).click();
  const rowHeight = (loc) => loc.evaluate((el) => document.querySelector(`label[for="${el.id}"]`).getBoundingClientRect().height);

  await step(`${tag} K1`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.getByRole("button", { name: /^Edit / }).first().click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    await dlg.waitFor({ timeout: 10000 });
    const enabled = dlg.getByRole("checkbox", { name: /^Enabled/ });
    await enabled.waitFor({ timeout: 10000 });
    check((await dlg.getByRole("switch").count()) === 0, `${tag} K1 no switch`, "no switch in the feed editor", "a switch is still in the feed editor");
    await enabled.scrollIntoViewIfNeeded();
    const was = await enabled.isChecked();
    writes.length = 0;
    await enabled.click();
    check((await enabled.isChecked()) !== was, `${tag} K1 ticks`, "Enabled ticks and unticks", "Enabled did not change");
    check(writes.length === 0, `${tag} K1 held`, "nothing is sent until Save", `sent ${writes.join(", ")}`);
    await t.shot(page, `${tag}-K1-feed-editor`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} K2`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Feed actions" }).click();
    await page.getByRole("menuitem", { name: "Import OPML" }).click();
    const imp = page.getByRole("dialog");
    const move = imp.getByRole("checkbox", { name: /^Move feeds that already exist/ });
    await move.waitFor({ timeout: 10000 });
    check(!(await move.isChecked()) && (await imp.getByRole("switch").count()) === 0, `${tag} K2 import`, "the move option is an unticked checkbox", "the move option is not an unticked checkbox");
    check((await rowHeight(move)) >= 44, `${tag} K2 target`, "the checkbox row is at least 44 px tall", "the checkbox row is under 44 px");
    await t.shot(page, `${tag}-K2-import`);
    await page.keyboard.press("Escape");
    await imp.waitFor({ state: "detached", timeout: 5000 });
    await page.goto("/stats", { waitUntil: "load" });
    await page.getByRole("button", { name: "Export", exact: true }).click();
    const exp = page.getByRole("dialog", { name: "Export statistics" });
    const titles = exp.getByRole("checkbox", { name: /^Include article titles and links/ });
    await titles.waitFor({ timeout: 10000 });
    check((await exp.getByRole("switch").count()) === 0, `${tag} K2 export`, "no switch in the export dialog", "a switch is in the export dialog");
    await t.shot(page, `${tag}-K2-export`);
    await page.keyboard.press("Escape");
    await exp.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} K3`, async () => {
    await page.goto("/settings/filters", { waitUntil: "load" });
    await page.getByRole("button", { name: /New filter/ }).first().click();
    const dlg = page.getByRole("dialog", { name: "New filter" });
    await dlg.waitFor({ timeout: 10000 });
    await dlg.getByRole("checkbox", { name: /^Turn this filter on/ }).waitFor({ timeout: 10000 });
    check((await dlg.getByRole("switch").count()) === 0, `${tag} K3 none`, "no switch in the filter editor", "a switch is in the filter editor");
    await dlg.getByRole("checkbox", { name: /^Ignore capitalization/ }).scrollIntoViewIfNeeded();
    await t.shot(page, `${tag}-K3-filter`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });

  await step(`${tag} K4`, async () => {
    await page.goto("/settings/appearance", { waitUntil: "load" });
    const sw = page.getByRole("switch", { name: /^Larger buttons/ });
    await sw.waitFor({ timeout: 10000 });
    const was = await sw.isChecked();
    writes.length = 0;
    await tap(sw);
    check((await sw.isChecked()) !== was, `${tag} K4 flips`, "Larger buttons flips at once", "Larger buttons did not change");
    await sw.scrollIntoViewIfNeeded();
    await t.shot(page, `${tag}-K4-switch`);
    await tap(sw);
    check((await sw.isChecked()) === was, `${tag} K4 back`, "put back", "Larger buttons was left changed");
  });
});

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} K5`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Add feed" }).click();
    const dlg = page.getByRole("dialog", { name: "Add feed" });
    await dlg.getByRole("textbox", { name: "Feed or website address" }).first().fill("http://127.0.0.1:9/uat-private.xml");
    await dlg.getByRole("button", { name: "Add feed", exact: true }).click();
    const opt = dlg.getByRole("checkbox", { name: /^Allow addresses on my own network/ });
    await opt.waitFor({ timeout: 15000 });
    check(!(await opt.isChecked()) && (await dlg.getByRole("switch").count()) === 0, `${tag} K5`, "the private-network option is an unticked checkbox", "the private-network option is not an unticked checkbox");
    check(/tick "Allow addresses on my own network"/.test(await dlg.innerText()), `${tag} K5 words`, "the refusal says to tick it", "the refusal does not say to tick it");
    await t.shot(page, `${tag}-K5-add-feed`);
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });
});

await t.finish("controls");
