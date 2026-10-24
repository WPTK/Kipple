// UAT: the Stats and Your year screens (design review, STATS package).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:stats-screen           in another
//   npm run uat:stats-screen -- --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// The summary the screens read is answered by a stand-in in the browser (uat/statsFixture.mjs: a seeded library has no
// reading history); everything else is the real app on the real server. For each width (desktop 1280x800, phone 375x812):
//   T1  nothing ever read: one "Nothing read yet" explanation with what counts as a read, and no Summary, charts, Sources
//       or "Your year" entry; a range with no reading after earlier reading keeps the Summary and says so once;
//   T2  Daily activity: bars are fixed width (a single month is one narrow bar, not a block), the largest value is labelled
//       at the top and 0 at the baseline, and a tap (or the arrow keys) on a bar shows its date and number under the chart;
//   T3  the heatmap labels every third hour and a tap on a square shows its day and hour under the grid; the Busiest day and
//       hour lines are not repeated under Reading habits, whose Longest read is a link to the article;
//   T4  Sources: a name (two lines at most), a bar and one number with its unit per row; Skimmed, Went to the original and
//       Read rate are in the feed's sheet, with "Not enough data yet" where there is no figure; nothing scrolls sideways;
//   T5  the summary values are in reader words (Articles read), Never opened is under Sources and leaves out a feed added
//       yesterday, and Export is the last thing on the page;
//   T6  under 360 px the Range is a select and the header does not scroll sideways;
//   T7  Your year: the value alone under each card's title, in the order of the share image; with under a week of reading
//       no superlative cards and no Share; with nothing read one "Nothing read in <year> yet";
//   T8  the share sheet: the two options come before the preview, the preview fits the sheet, Download image is the one
//       solid button, nothing scrolls sideways, and the downloaded image is a 1080x1350 PNG named for the year.
// Chromium only. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { readFileSync } from "node:fs";
import { start } from "./common.mjs";
import { route, today } from "./statsFixture.mjs";

const t = await start(import.meta.url, 24, { serviceWorkers: "block" });
const { check, step } = t;

/** Whether `loc`'s page or element scrolls sideways. */
const overflowsX = (loc) => loc.evaluate((el) => el.scrollWidth > el.clientWidth + 1);

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const mode = await route(page, "rich");
  const open = async (path, m) => {
    mode(m);
    await page.goto(path, { waitUntil: "load" });
  };

  await step(`${tag} T1`, async () => {
    await open("/stats", "empty");
    await page.getByRole("heading", { name: "Nothing read yet" }).waitFor({ timeout: 10000 });
    check(await page.getByText(/counts as read after 10 seconds/).isVisible(), `${tag} T1 rule`, "the read rule is said with the explanation", "the read rule is missing");
    const gone = ["Summary", "Daily activity", "Streaks", "When you read", "Reading habits", "Sources", "Never opened"];
    const shown = [];
    for (const name of gone) if (await page.getByRole("heading", { name, exact: true }).count()) shown.push(name);
    check(shown.length === 0, `${tag} T1 sections`, "no section shows before anything was read", `these show with nothing read: ${shown.join(", ")}`);
    check((await page.getByRole("link", { name: /Your year/ }).count()) === 0, `${tag} T1 entry`, "no Your year entry yet", "the Your year entry shows with nothing read");
    await t.shot(page, `${tag}-T1-empty`);
    await open("/stats", "quiet");
    await page.getByRole("heading", { name: "Nothing read in this range" }).waitFor({ timeout: 10000 });
    check((await page.getByRole("region", { name: "Summary" }).count()) === 1, `${tag} T1 quiet`, "a quiet range keeps the Summary and says so once", "a quiet range lost its Summary");
  });

  await step(`${tag} T2`, async () => {
    await open("/stats", "rich");
    const chart = page.getByRole("group", { name: /Articles read per day/ });
    await chart.waitFor({ timeout: 10000 });
    const bars = await chart.locator("> div").evaluateAll((els) => els.map((e) => e.getBoundingClientRect().width));
    check(Math.max(...bars) <= 24 && bars.length >= 28, `${tag} T2 bars`, `${bars.length} bars, none wider than ${Math.round(Math.max(...bars))} px`, `bars are ${bars.map(Math.round).join(",")} px`);
    const axis = await page.getByRole("region", { name: "Daily activity" }).locator("div[aria-hidden]").first().innerText();
    check(/^\d+\s+0$/.test(axis.replace(/\s+/g, " ").trim()), `${tag} T2 axis`, "the largest value is at the top and 0 at the baseline", `the axis reads "${axis}"`);
    const box = await chart.boundingBox();
    await page.mouse.click(box.x + box.width * 0.5, box.y + box.height - 2);
    const line = page.getByRole("region", { name: "Daily activity" }).getByRole("status");
    const said = await line.innerText();
    check(/^[A-Z][a-z]{2} \d+: \d+ articles?, /.test(said), `${tag} T2 tap`, `a tap shows "${said}"`, `a tap on a bar shows "${said}"`);
    await chart.focus();
    await page.keyboard.press("Home");
    const first = await line.innerText();
    await page.keyboard.press("ArrowRight");
    check((await line.innerText()) !== first, `${tag} T2 keys`, "the arrow keys move from bar to bar", "the arrow keys did not change the bar");
    // One month alone is one bar of at most 40 px, not a stretched block.
    await page.locator("label", { hasText: /^Monthly$/ }).click();
    const monthly = page.getByRole("group", { name: /Articles read per month/ });
    await monthly.waitFor();
    const wide = await monthly.locator("> div").evaluateAll((els) => Math.max(...els.map((e) => e.getBoundingClientRect().width)));
    check(wide <= 40, `${tag} T2 monthly`, `monthly bars are at most ${Math.round(wide)} px wide`, `a monthly bar is ${Math.round(wide)} px wide`);
    await page.locator("label", { hasText: /^Month$/ }).click();
    await t.shot(page, `${tag}-T2-bars`);
  });

  await step(`${tag} T3`, async () => {
    await open("/stats", "rich");
    const region = page.getByRole("region", { name: "When you read" });
    await region.waitFor({ timeout: 10000 });
    const ticks = await region.locator("thead th span[aria-hidden]").evaluateAll((els) => els.map((e) => e.textContent).filter(Boolean));
    check(ticks.length === 8, `${tag} T3 hours`, `hours are labelled every 3 h (${ticks.join(" ")})`, `expected 8 hour labels, found ${ticks.length}: ${ticks.join(" ")}`);
    const cell = region.locator("td.heat-cell").nth(2 * 24 + 20);
    await cell.scrollIntoViewIfNeeded();
    await cell.click();
    const said = await region.getByRole("status").innerText();
    check(/^[A-Z][a-z]+day, \d+ [ap]m: /.test(said), `${tag} T3 tap`, `a tap shows "${said}"`, `a tap on a square shows "${said}"`);
    check((await page.getByText(/Busiest (day|hour)/).count()) === 0, `${tag} T3 once`, "the busiest day and hour are said once, by the heatmap", "Busiest day or hour is repeated as text");
    const longest = page.getByRole("region", { name: "Reading habits" }).getByRole("link", { name: /A very long essay/ });
    check(/\/i\/77/.test((await longest.getAttribute("href")) ?? ""), `${tag} T3 longest`, "the longest read links to its article", "the longest read is not a link to the article");
    await t.shot(page, `${tag}-T3-heatmap`);
  });

  await step(`${tag} T4`, async () => {
    await open("/stats", "rich");
    const sources = page.getByRole("region", { name: "Sources" });
    await sources.waitFor({ timeout: 10000 });
    const rows = sources.getByRole("list", { name: /by articles read/ }).getByRole("listitem");
    const first = (await rows.first().innerText()).replace(/\s+/g, " ");
    check(/Gamma Daily 44 articles/.test(first) && !/Skimmed|Went to the original|Read rate|Stars/.test(first), `${tag} T4 row`, `a row is a name and one number: "${first}"`, `a row reads "${first}"`);
    check((await sources.locator("table").count()) === 0, `${tag} T4 one list`, "there is one list, no table", "a table is still drawn");
    const long = await rows.nth(1).evaluate((li) => li.getBoundingClientRect().height);
    check(long < 140, `${tag} T4 long name`, `a very long name takes two lines (row ${Math.round(long)} px)`, `a long name makes the row ${Math.round(long)} px tall`);
    check(!(await overflowsX(page.locator(".overflow-y-auto").first())), `${tag} T4 sideways`, "the screen does not scroll sideways", "the screen scrolls sideways");
    await page.getByRole("button", { name: /Alpha Blog/ }).click();
    const sheet = page.getByRole("dialog", { name: "Alpha Blog" });
    await sheet.getByRole("region", { name: "Engagement" }).waitFor({ timeout: 10000 });
    const text = await sheet.innerText();
    check(/Skimmed/.test(text) && /Went to the original/.test(text) && /Read rate/.test(text), `${tag} T4 sheet`, "the sheet has Skimmed, Went to the original and Read rate", "the sheet lacks one of Skimmed, Went to the original, Read rate");
    check(!/Quick bounce|Opened original/.test(text), `${tag} T4 words`, "no analytics words in the sheet", "Quick bounce or Opened original is still in the sheet");
    await t.shot(page, `${tag}-T4-feed-sheet`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} T5`, async () => {
    await open("/stats", "rich");
    const summary = page.getByRole("region", { name: "Summary" });
    await summary.waitFor({ timeout: 10000 });
    const text = await summary.innerText();
    check(/Articles read/.test(text) && /Current streak/.test(text) && !/Items/.test(text), `${tag} T5 summary`, "the Summary has Articles read and the streaks", `the Summary reads: ${text.replace(/\s+/g, " ").slice(0, 120)}`);
    const sources = page.getByRole("region", { name: "Sources" });
    check((await sources.getByText("Quiet Feed").count()) === 1 && (await sources.getByText("Brand New Feed").count()) === 0, `${tag} T5 never opened`, "Never opened is under Sources and leaves out a feed from yesterday", "Never opened is misplaced or lists a feed from yesterday");
    const last = await page.getByRole("region").last().getAttribute("aria-labelledby");
    const heading = await page.locator(`[id="${last}"]`).innerText();
    check(heading === "Your data" && (await page.getByRole("button", { name: "Export" }).count()) === 1, `${tag} T5 export`, "Export is the last thing on the page", `the last section is "${heading}"`);
    await page.getByRole("button", { name: "Export" }).click();
    check(await page.getByRole("dialog", { name: "Export statistics" }).isVisible(), `${tag} T5 dialog`, "Export opens its dialog", "Export did not open its dialog");
    await page.keyboard.press("Escape");
  });

  await step(`${tag} T6`, async () => {
    await page.setViewportSize({ width: 340, height: vp.height });
    await open("/stats", "rich");
    await page.getByRole("heading", { name: "Stats" }).waitFor({ timeout: 10000 });
    check((await page.getByRole("combobox", { name: "Range" }).count()) === 1, `${tag} T6 select`, "the Range is a select under 360 px", "the Range is not a select under 360 px");
    check(!(await overflowsX(page.locator("header").first())), `${tag} T6 header`, "the header does not scroll sideways", "the header scrolls sideways");
    await t.shot(page, `${tag}-T6-narrow`);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    if (vp.width < 500) {
      // 1.5x text: the phone has the room of a 250 px one. The Range is a select, the tiles stack, no value wraps.
      await page.evaluate(() => globalThis.document.documentElement.style.setProperty("--kp-scale", "1.5"));
      await page.getByRole("combobox", { name: "Range" }).waitFor({ timeout: 10000 });
      const wrapped = await page.locator("button[aria-expanded] > span.text-2xl").evaluateAll((els) => els.filter((e) => e.getBoundingClientRect().height > parseFloat(globalThis.getComputedStyle(e).lineHeight) * 1.5).map((e) => e.textContent));
      check(wrapped.length === 0, `${tag} T6 large text`, "with 1.5x text the Range is a select and no tile value wraps", `tile values wrap: ${wrapped.join(", ")}`);
      check(!(await overflowsX(page.locator("header").first())), `${tag} T6 large header`, "the header does not scroll sideways at 1.5x text", "the header scrolls sideways at 1.5x text");
      await t.shot(page, `${tag}-T6-large-text`);
      await page.evaluate(() => globalThis.document.documentElement.style.removeProperty("--kp-scale"));
    }
  });

  await step(`${tag} T7`, async () => {
    await open("/stats/wrapped", "rich");
    const hero = page.getByRole("region", { name: /Articles read in \d{4}/ });
    await hero.waitFor({ timeout: 10000 });
    const titles = await page.locator("section.wrapped-card h2").allInnerTexts();
    const want = [/^Articles read in/, /^Active reading$/, /^Days with reading$/, /^Longest streak$/, /^Busiest day$/, /^Busiest hour$/, /^Busiest month$/, /^Top sources$/, /^Longest read$/];
    check(want.length === titles.length && want.every((re, i) => re.test(titles[i])), `${tag} T7 order`, "the cards follow the share image", `cards: ${titles.join(" | ")}`);
    const value = await page.getByRole("region", { name: "Busiest day" }).locator("p").first().innerText();
    check(/^[A-Z][a-z]+day$/.test(value), `${tag} T7 value`, `a card's value stands alone ("${value}")`, `a busiest day card reads "${value}"`);
    const clamp = await page.getByRole("region", { name: "Longest read" }).locator("p").first().evaluate((el) => el.scrollHeight <= el.clientHeight * 1.05 || globalThis.getComputedStyle(el).webkitLineClamp === "2");
    check(clamp, `${tag} T7 clamp`, "the longest read's title is held to two lines", "the longest read's title is not clamped");
    check((await page.getByRole("button", { name: "Share" }).count()) === 1, `${tag} T7 share`, "Share is offered with a week or more of reading", "no Share with a year of reading");
    await t.shot(page, `${tag}-T7-year`);

    await open("/stats/wrapped", "low");
    await page.getByRole("region", { name: /Articles read in/ }).waitFor({ timeout: 10000 });
    const some = [];
    for (const name of ["Longest streak", "Busiest day", "Busiest hour", "Busiest month"]) if (await page.getByRole("region", { name }).count()) some.push(name);
    check(some.length === 0 && (await page.getByRole("button", { name: "Share" }).count()) === 0, `${tag} T7 low`, "under a week: totals only, no superlatives, no Share", `under a week still shows: ${some.join(", ") || "Share"}`);
    check((await page.getByText(/of reading in [0-9]{4} so far/).count()) === 1, `${tag} T7 keep`, "one note says how little there is so far", "the low-data note is missing or said twice");
    await t.shot(page, `${tag}-T7-low`);

    await open("/stats/wrapped", "empty");
    await page.getByRole("heading", { name: `Nothing read in ${today().slice(0, 4)} yet` }).waitFor({ timeout: 10000 });
    check((await page.locator("section.wrapped-card").count()) === 0, `${tag} T7 empty`, "an empty year is one message, not cards", "an empty year still draws cards");
  });

  await step(`${tag} T8`, async () => {
    await open("/stats/wrapped", "rich");
    await page.getByRole("button", { name: "Share" }).click();
    const dlg = page.getByRole("dialog", { name: "Share your year" });
    await dlg.waitFor({ timeout: 10000 });
    const tops = await dlg.evaluate((d) => {
      const y = (sel) => d.querySelector(sel)?.getBoundingClientRect().top ?? -1;
      return { boxes: Math.max(...[...d.querySelectorAll('input[type="checkbox"]')].map((c) => c.getBoundingClientRect().top)), preview: y('[data-testid="wrapped-preview"]') };
    });
    check(tops.boxes < tops.preview, `${tag} T8 order`, "the options come before the preview", "the options come after the preview");
    const fits = await dlg.evaluate((d) => {
      const p = d.querySelector('[data-testid="wrapped-preview"]').getBoundingClientRect();
      const r = d.getBoundingClientRect();
      return p.bottom <= r.bottom && p.height <= globalThis.innerHeight * 0.5;
    });
    check(fits, `${tag} T8 preview`, "the preview fits inside the sheet", "the preview runs past the sheet or takes over half the window");
    await dlg.getByRole("checkbox", { name: /top sources/ }).check();
    await dlg.getByRole("checkbox", { name: /longest read/ }).check();
    const fills = await dlg.getByRole("button").evaluateAll((bs) => bs.map((b) => [b.textContent, globalThis.getComputedStyle(b).backgroundColor]));
    const main = fills.find(([n]) => n === "Download image")?.[1];
    const others = fills.filter(([n]) => /^(Copy as text|Share|Close)$/.test(n)).map(([, bg]) => bg);
    check(main != null && others.length > 0 && others.every((bg) => bg !== main), `${tag} T8 primary`, "Download image is the one solid button", `button fills: ${JSON.stringify(fills)}`);
    const order = await dlg.getByRole("button").evaluateAll((bs) => bs.map((b) => b.textContent).filter((n) => /^(Download image|Share|Copy as text|Close)$/.test(n)));
    check(order[0] === "Download image", `${tag} T8 tab order`, "Download image is first in the tab order, matching its place on screen", `tab order: ${order.join(", ")}`);
    check(!(await overflowsX(dlg)), `${tag} T8 sideways`, "the sheet does not scroll sideways", "the sheet scrolls sideways");
    const download = dlg.getByRole("button", { name: "Download image" });
    await download.waitFor();
    await page.waitForTimeout(1500);
    await t.shot(page, `${tag}-T8-share`);
    const [file] = await Promise.all([page.waitForEvent("download", { timeout: 15000 }), download.click()]);
    const path = await file.path();
    const png = readFileSync(path);
    const isPng = png.subarray(1, 4).toString() === "PNG";
    const [w, h] = [png.readUInt32BE(16), png.readUInt32BE(20)];
    check(isPng && w === 1080 && h === 1350 && file.suggestedFilename() === `kipple-${today().slice(0, 4)}.png`, `${tag} T8 image`, `the download is ${file.suggestedFilename()}, a ${w}x${h} PNG`, `the download is ${file.suggestedFilename()} (${isPng ? "PNG" : "not a PNG"}, ${w}x${h})`);
  });
});

await t.finish("Stats and Your year");
