// UAT: yes/no choices in forms are checkboxes, and a switch only where a flip takes effect at once (issue #286).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:controls               in another, once the feeds have fetched
//   npm run uat:controls -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   K1  Edit feed: its yes/no options are checkboxes (none is a switch), and ticking one sends nothing until Save;
//   K2  Import OPML and Export statistics: the options are checkboxes, 44 px tall rows on a phone;
//   K3  New filter: Use this filter and the Options rows are all checkboxes, one control for the same job;
//   K6  A checkbox row is one hit area (a tap in the box column or the gap toggles it), 44 px tall (56 px with Large targets),
//       with the box centred on the text, at both widths;
//   K7  A radio card (New filter, the scope choices) is one hit area, 44 px tall (56 px with Large targets), the 20 px radio beside
//       its first line, and a tap in its padding picks it (the cards with fields under them are measured in wizard.mjs);
//   K5  Add feed with an address on a private network: the refusal points at "tick" and the option it offers is a checkbox,
//       unticked (nothing is added);
//   K4  Settings > Appearance & Reading: Larger buttons stays a switch (it applies the moment it is flipped), and flipping it
//       sends nothing to the server (it is a device setting) and is put back;
//   K8  Settings > Appearance & Reading: Layout is seven radio cards in one column (none wraps beside another or runs past
//       the screen), each 44 px tall (56 px with Large targets) and named with what it looks like; picking one writes
//       only the device's own profile (PATCH /api/device) and is put back.
// It changes nothing on the server: every dialog is closed with Escape. It only runs against a loopback address with the
// seed's credentials unless --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 22);
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
    const enabled = dlg.getByRole("checkbox", { name: /^Pause this feed/ });
    await enabled.waitFor({ timeout: 10000 });
    check((await dlg.getByRole("switch").count()) === 0, `${tag} K1 no switch`, "no switch in the feed editor", "a switch is still in the feed editor");
    await enabled.scrollIntoViewIfNeeded();
    const was = await enabled.isChecked();
    writes.length = 0;
    await enabled.click();
    check((await enabled.isChecked()) !== was, `${tag} K1 ticks`, "Pause this feed ticks and unticks", "Pause this feed did not change");
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
    await dlg.getByRole("checkbox", { name: /^Use this filter/ }).waitFor({ timeout: 10000 });
    check((await dlg.getByRole("switch").count()) === 0, `${tag} K3 none`, "no switch in the filter editor", "a switch is in the filter editor");
    await dlg.getByRole("checkbox", { name: /^Use this filter/ }).scrollIntoViewIfNeeded();
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

// K6: the whole row is one hit area, and the box lines up with the text at both target sizes.
await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} K6`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.getByRole("button", { name: /^Edit / }).first().click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    const box = dlg.getByRole("checkbox", { name: /^Pause this feed/ });
    await box.waitFor({ timeout: 10000 });
    await box.scrollIntoViewIfNeeded();
    for (const large of [false, true]) {
      await page.evaluate((on) => (on ? document.documentElement.setAttribute("data-targets", "large") : document.documentElement.removeAttribute("data-targets")), large);
      const want = large ? 56 : 44;
      const tagL = `${tag} K6 ${large ? "large" : "normal"}`;
      const m = await box.evaluate((el) => {
        const label = document.querySelector(`label[for="${el.id}"]`);
        const l = label.getBoundingClientRect();
        const b = el.getBoundingClientRect();
        const text = label.lastElementChild.getBoundingClientRect();
        return { ly: l.top, lh: l.height, lw: l.width, bx: b.left, by: b.top, bw: b.width, bh: b.height, tTop: text.top, tHeight: text.height };
      });
      check(m.lh >= want - 0.5, `${tagL} height`, `the row is ${m.lh.toFixed(1)} px tall (at least ${want})`, `the row is ${m.lh.toFixed(1)} px tall, under ${want}`);
      check(m.lw >= want, `${tagL} width`, `the row is ${m.lw.toFixed(0)} px wide`, `the row is ${m.lw.toFixed(0)} px wide, under ${want}`);
      const boxMid = m.by + m.bh / 2;
      const lineMid = m.tTop + Math.min(m.tHeight, want) / 2; // the first row of the text cell, which is `want` tall
      check(Math.abs(boxMid - lineMid) <= 1, `${tagL} aligned`, `the box centre is within 1 px of the text centre (${boxMid.toFixed(1)} v ${lineMid.toFixed(1)})`, `the box centre ${boxMid.toFixed(1)} is not on the text centre ${lineMid.toFixed(1)}`);
      check(Math.round(m.bw) === 20 && Math.round(m.bh) === 20, `${tagL} box`, "the box stays 20 px", `the box is ${m.bw} x ${m.bh} px`);
      // A tap beside the box (above it in the box column, then in the gap before the text) toggles it.
      const was = await box.isChecked();
      await page.mouse.click(m.bx + m.bw / 2, m.ly + 2);
      check((await box.isChecked()) !== was, `${tagL} tap above`, "a tap in the box column outside the box toggles it", "a tap in the box column outside the box did nothing");
      await page.mouse.click(m.bx + m.bw + 6, m.ly + m.lh / 2);
      check((await box.isChecked()) === was, `${tagL} tap gap`, "a tap in the gap between the box and the text toggles it", "a tap in the gap did nothing");
      if (large) await t.shot(page, `${tag}-K6-large`);
    }
    await page.evaluate(() => document.documentElement.removeAttribute("data-targets"));
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
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

// K7: a radio card (RadioCard) is one hit area, 44 px tall (56 px with Large targets), with the 20 px radio beside the
// first line of its title, and the whole card (its padding too) picks it.
await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} K7`, async () => {
    await page.goto("/settings/filters", { waitUntil: "load" });
    await page.getByRole("button", { name: /New filter/ }).first().click();
    const dlg = page.getByRole("dialog", { name: "New filter" });
    const every = dlg.getByRole("radio", { name: /^Everywhere/ });
    const folder = dlg.getByRole("radio", { name: /^A folder/ });
    await every.waitFor({ timeout: 10000 });
    await every.scrollIntoViewIfNeeded();
    for (const large of [false, true]) {
      await page.evaluate((on) => (on ? document.documentElement.setAttribute("data-targets", "large") : document.documentElement.removeAttribute("data-targets")), large);
      const want = large ? 56 : 44;
      const tagL = `${tag} K7 ${large ? "large" : "normal"}`;
      const m = await folder.evaluate((el) => {
        const card = el.closest("label").getBoundingClientRect();
        const b = el.getBoundingClientRect();
        const title = el.closest("label").querySelector("span > span").getBoundingClientRect();
        return { x: card.left, y: card.top, h: card.height, w: card.width, bw: b.width, bh: b.height, boxMid: b.top + b.height / 2, lineMid: title.top + Math.min(title.height, 20) / 2 };
      });
      check(m.h >= want - 0.5, `${tagL} height`, `the card is ${m.h.toFixed(1)} px tall (at least ${want})`, `the card is ${m.h.toFixed(1)} px tall, under ${want}`);
      check(Math.round(m.bw) === 20 && Math.round(m.bh) === 20, `${tagL} radio`, "the radio stays 20 px", `the radio is ${m.bw} x ${m.bh} px`);
      // the 20 px radio sits 2 px low on a 20 px line (mt-0.5), as it always has
      check(Math.abs(m.boxMid - m.lineMid) <= 3, `${tagL} aligned`, `the radio is beside the first line (${m.boxMid.toFixed(1)} v ${m.lineMid.toFixed(1)})`, `the radio centre ${m.boxMid.toFixed(1)} is off the first line ${m.lineMid.toFixed(1)}`);
      await page.mouse.click(m.x + m.w / 2, m.y + 4); // the card's top padding, above the text and clear of the rounded corners
      check(await folder.isChecked(), `${tagL} tap padding`, "a tap in the card's padding picks it", "a tap in the card's padding did nothing");
      await every.check();
      if (large) await t.shot(page, `${tag}-K7-large`);
    }
    await page.evaluate(() => document.documentElement.removeAttribute("data-targets"));
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  });
});

// K8: the default layout is a column of radio cards, not a segmented control that wraps.
await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} K8`, async () => {
    const writes = [];
    const onRequest = (r) => {
      if (["POST", "PATCH", "PUT", "DELETE"].includes(r.method()) && r.url().includes("/api/")) writes.push(`${r.method()} ${new URL(r.url()).pathname}`);
    };
    page.on("request", onRequest);
    let was;
    try {
    await page.goto("/settings/appearance", { waitUntil: "load" });
    const group = page.getByRole("group", { name: "Layout", exact: true });
    await group.waitFor({ timeout: 10000 });
    const radios = group.getByRole("radio");
    check((await radios.count()) === 7, `${tag} K8 seven`, "seven layouts", `${await radios.count()} layouts`);
    await group.scrollIntoViewIfNeeded();
    for (const large of [false, true]) {
      await page.evaluate((on) => (on ? document.documentElement.setAttribute("data-targets", "large") : document.documentElement.removeAttribute("data-targets")), large);
      const want = large ? 56 : 44;
      const tagL = `${tag} K8 ${large ? "large" : "normal"}`;
      const cards = await radios.evaluateAll((els) => els.map((el) => el.closest("label").getBoundingClientRect()).map((r) => ({ l: r.left, r: r.right, t: r.top, b: r.bottom, h: r.height })));
      const vw = await page.evaluate(() => document.documentElement.clientWidth);
      check(cards.every((c) => Math.abs(c.l - cards[0].l) < 1 && Math.abs(c.r - cards[0].r) < 1), `${tagL} column`, "the cards share one left and right edge", "the cards are not in one column");
      check(cards.every((c, i) => i === 0 || c.t >= cards[i - 1].b - 0.5), `${tagL} stacked`, "no card overlaps the one above", "a card overlaps the one above");
      check(cards.every((c) => c.h >= want - 0.5), `${tagL} height`, `every card is at least ${want} px tall`, `a card is under ${want} px tall`);
      check(cards.every((c) => c.l >= 0 && c.r <= vw + 0.5), `${tagL} fits`, "every card fits the screen", "a card runs past the screen");
      check(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), `${tagL} no sideways scroll`, "the page does not scroll sideways", "the page scrolls sideways");
    }
    await page.evaluate(() => document.documentElement.removeAttribute("data-targets"));
    was = await radios.evaluateAll((els) => els.find((e) => e.checked)?.value);
    check(!!was, `${tag} K8 current`, `the layout in effect is ${was}`, "no layout card is checked");
    if (!was) return;
    const other = was === "headlines" ? "cards" : "headlines";
    writes.length = 0;
    try {
      await group.locator(`input[value="${other}"]`).check();
      check(await group.locator(`input[value="${other}"]`).isChecked(), `${tag} K8 pick`, "a card picks its layout", "a card did not pick its layout");
      await page.waitForTimeout(1200); // the device profile is saved 500 ms after the last change
      check(writes.every((w) => w === "PATCH /api/device"), `${tag} K8 device`, "picking writes only the device profile", `picking sent ${writes.join(", ")}`);
      await t.shot(page, `${tag}-K8-layout`);
    } finally {
      await group.locator(`input[value="${was}"]`).check(); // even if a check above threw
      await page.waitForTimeout(1200);
    }
    check(await group.locator(`input[value="${was}"]`).isChecked(), `${tag} K8 back`, "put back", "the layout was left changed");
    await page.reload({ waitUntil: "load" }); // and it is what the server holds, not only what the page shows
    check(await page.getByRole("group", { name: "Layout", exact: true }).locator(`input[value="${was}"]`).isChecked(), `${tag} K8 saved`, "the original layout is saved", "the original layout was not saved");
    } finally {
      page.off("request", onRequest);
    }
  });
});

await t.finish("controls");
