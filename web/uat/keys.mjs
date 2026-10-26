// UAT: keyboard focus stays where the keyboard user is (issues #492, #493, #494, #495, #390).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:keys                   in another, once the feeds have fetched
//   npm run uat:keys -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and, from the keyboard:
//   R1  Opens an article from All and presses Enter on Next article twice: focus stays on Next beside the list (the
//       article that arrived is announced), or moves to the new article's title full screen; never to the page.
//   R2  Full text: Enter keeps focus on the button while the full text loads, and Enter again turns it back; on and off differ
//       in the glyph (check-mark page vs plain page), not only the tint (#563).
//   R3  Previous onto the first article: the button is switched off (aria-disabled) and keeps focus.
//   R4  (desktop) After Next the open article is the list's Tab stop, not the row focused before (#390).
//   H1  (desktop) Refresh all feeds and Oldest first keep focus when pressed; the order is put back.
//   L1  The list: End, Home and PageDown move focus to the last, first and a later row on screen; Space opens a row.
//   C1  (desktop) Cards: a focused card's ring follows its rounded corners; the layout is put back.
//   D1  Manage feeds > Edit > a feed's pencil: the Edit feed sheet opens with focus on itself or the Title field, and
//       the first Tab reaches Title (never the footer first). Escape closes it.
//   T1  (desktop) The sidebar tree: Enter on a feed and Space on a folder open their lists, and focus stays on the
//       tree item.
//   S1  Search: ArrowDown in the search field moves focus to the first result.
//   G1  Settings: Home and End on the Reduce motion choices pick the first and last; the choice is put back.
//   P1  (desktop) The Reading appearance panel fits the window: its last line is inside the panel, which is inside the
//       window, once scrolled to the end.
// It opens articles on the seed's account and marks the ones that were unread unread again afterwards. It only runs
// against a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0 clean,
// 1 findings, 2 setup error. First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 26);
const { check, step } = t;
const JSON_ACCEPT = { Accept: "application/json" };

const unreadIds = async (page) =>
  ((await (await page.request.get("/api/items", { params: { view: "unread", limit: 500 }, headers: JSON_ACCEPT })).json()).items ?? []).map((i) => i.id);
/** What has focus: "body", "row <id>", the button's or heading's name, or a short description of anything else. */
const focusOf = (page) =>
  page.evaluate(() => {
    const a = document.activeElement;
    if (!a || a === document.body) return "body";
    const row = a.closest("[data-item-id]");
    if (row && a.closest("[data-testid=list-scroll]")) return `row ${row.getAttribute("data-item-id")}`;
    if (a.getAttribute("role") === "dialog") return "dialog";
    if (a.getAttribute("role") === "treeitem") return `treeitem ${a.getAttribute("aria-label")}`;
    if (a.tagName === "H1") return "h1";
    return `${a.tagName.toLowerCase()}${a.getAttribute("aria-label") ? ` "${a.getAttribute("aria-label")}"` : ""}`;
  });
const toolbarButton = (page, name) => page.getByRole("toolbar", { name: "Article actions" }).getByRole("button", { name, exact: true });
const rowIds = (page) => page.evaluate(() => [...document.querySelectorAll("[data-testid=list-scroll] [data-item-id]")].map((r) => r.getAttribute("data-item-id")));
const layoutButton = (page) => page.locator('button[aria-label^="List options, "]').first();
async function pickLayout(page, name) {
  await layoutButton(page).click();
  await page.getByRole("menuitemradio", { name: new RegExp(`^${name}\\b`) }).click();
  await page.keyboard.press("Escape");
}

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const wasUnread = await unreadIds(page);
  const listUrl = "/l/all";
  const openFirst = async (n = 0) => {
    await page.goto(listUrl, { waitUntil: "load" });
    const row = page.locator("[data-testid=list-scroll] [data-item-id]").nth(n);
    await row.waitFor({ timeout: 15000 });
    await row.locator("a").first().focus();
    await page.keyboard.press("Enter");
    await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
    await page.locator("#article-title").waitFor({ timeout: 10000 });
  };
  const originalLayout = vp.mobile ? null : await (async () => {
    await page.goto(listUrl, { waitUntil: "load" });
    await layoutButton(page).waitFor({ timeout: 15000 });
    return (await layoutButton(page).getAttribute("aria-label")).replace(/^List options, | layout$/g, "");
  })();
  try {
    await step(`${tag} R1`, async () => {
      if (!vp.mobile) {
        // Inbox has a reading pane beside the list at this width.
        await page.goto(listUrl, { waitUntil: "load" });
        if (originalLayout !== "Inbox") await pickLayout(page, "Inbox");
      }
      await openFirst();
      for (const n of [1, 2]) {
        const url = page.url();
        await toolbarButton(page, "Next article").focus();
        await page.keyboard.press("Enter");
        await page.waitForURL((u) => u.href !== url, { timeout: 10000 });
        const early = await focusOf(page);
        await page.locator("#article-title").waitFor({ timeout: 10000 });
        await page.waitForTimeout(400);
        const late = await focusOf(page);
        const want = vp.mobile ? ["h1", 'button "Next article"'] : ['button "Next article"'];
        check(
          (vp.mobile || early === 'button "Next article"') && want.includes(late),
          `${tag} R1 next ${n}`,
          `focus after Next: ${early}, then ${late}`,
          `focus after Next: ${early}, then ${late} (want ${want.join(" or ")})`,
        );
      }
      if (!vp.mobile) {
        const said = await page.getByTestId("live-region").textContent();
        const title = await page.locator("#article-title").textContent();
        check(!!title && said.includes(title.trim()), `${tag} R1 announce`, "the new article's title is announced", `live region says "${said}", title "${title}"`);
      }
      await t.shot(page, `${tag}-R1`);
    });
    await step(`${tag} R2`, async () => {
      const ft = toolbarButton(page, "Full text");
      const on = (await ft.getAttribute("aria-pressed")) === "true";
      // The state shows in the glyph as well as the tint: a page with a check mark when on, a plain page when off.
      const glyph = () => ft.locator("svg").evaluate((el) => el.getAttribute("class") ?? "");
      const glyphOk = (pressed, cls) => (pressed ? /lucide-file-check/.test(cls) : /lucide-file-text/.test(cls));
      const before = await glyph();
      check(glyphOk(on, before), `${tag} R2 glyph`, `Full text ${on ? "on" : "off"} has the ${on ? "check-mark" : "plain"} page`, `Full text aria-pressed=${on} but the glyph is "${before}"`);
      await t.shot(page, `${tag}-R2-fulltext-${on ? "on" : "off"}`);
      await ft.focus();
      await page.keyboard.press("Enter");
      const samples = [];
      for (const ms of [100, 900, 2500]) {
        // real time: the check samples where focus is at fixed delays after the press
        await page.waitForTimeout(ms - (samples.length ? [100, 900][samples.length - 1] : 0));
        samples.push(await focusOf(page));
      }
      check(samples.every((s) => s === 'button "Full text"'), `${tag} R2 focus`, "focus stays on Full text while it loads", `focus at 0.1, 0.9, 2.5 s: ${samples.join(", ")}`);
      await page.waitForFunction(() => document.querySelector('[aria-label="Full text"]')?.getAttribute("aria-disabled") !== "true", undefined, { timeout: 20000 }).catch(() => {});
      const now = (await ft.getAttribute("aria-pressed")) === "true";
      const after = await glyph();
      check(glyphOk(now, after), `${tag} R2 glyph after`, "the glyph follows the state after the flip", `aria-pressed=${now} but the glyph is "${after}"`);
      check(now === on || after !== before, `${tag} R2 differs`, "on and off differ in more than color", `state flipped but the glyph stayed "${after}"`);
      await t.shot(page, `${tag}-R2-fulltext-${now ? "on" : "off"}-after`);
      if (now !== on) {
        await page.keyboard.press("Enter");
        await page.waitForTimeout(500);
      }
      check((await focusOf(page)) === 'button "Full text"', `${tag} R2 back`, "Enter again works from the same place", `focus is on ${await focusOf(page)}`);
    });
    await step(`${tag} R3`, async () => {
      await openFirst(1);
      await toolbarButton(page, "Previous article").focus();
      await page.keyboard.press("Enter");
      await page.waitForTimeout(800);
      const prev = toolbarButton(page, "Previous article");
      const off = await prev.getAttribute("aria-disabled");
      const at = await focusOf(page);
      const ok = vp.mobile ? off === "true" && (at === "h1" || at === 'button "Previous article"') : off === "true" && at === 'button "Previous article"';
      check(ok, `${tag} R3`, `on the first article Previous is aria-disabled and focus is on ${at}`, `aria-disabled=${off}; focus is on ${at}`);
    });
    if (!vp.mobile) {
      await step(`${tag} R4`, async () => {
        await openFirst();
        await toolbarButton(page, "Next article").click();
        await toolbarButton(page, "Next article").click();
        await page.waitForTimeout(800);
        const open = page.url().match(/\/i\/(\d+)/)?.[1];
        const stops = await page.evaluate(() => [...document.querySelectorAll('[data-testid=list-scroll] [data-item-id] a:not([tabindex="-1"])')].map((a) => a.closest("[data-item-id]").getAttribute("data-item-id")));
        check(stops.length === 1 && stops[0] === open, `${tag} R4`, `the list's Tab stop is the open article (${open})`, `Tab stops ${stops.join(",")}, open article ${open}`);
      });
      await step(`${tag} H1`, async () => {
        await page.goto(listUrl, { waitUntil: "load" });
        await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
        const refresh = page.getByRole("button", { name: /^Refresh/ }).first();
        await refresh.focus();
        await page.keyboard.press("Enter");
        const r = [];
        for (const ms of [50, 400, 1500]) {
          // real time: sampled at fixed delays after the press
          await page.waitForTimeout(ms);
          r.push(await focusOf(page));
        }
        check(r.every((s) => /^button "Refresh/.test(s)), `${tag} H1 refresh`, `focus stays on Refresh (${r.join(", ")})`, `focus after Refresh: ${r.join(", ")}`);
        const order = page.getByRole("button", { name: "Oldest first" });
        const was = await order.getAttribute("aria-pressed");
        await order.focus();
        await page.keyboard.press("Enter");
        await page.waitForTimeout(600);
        const a1 = await focusOf(page);
        await page.keyboard.press("Enter");
        await page.waitForTimeout(600);
        const a2 = await focusOf(page);
        const back = await order.getAttribute("aria-pressed");
        check(a1 === 'button "Oldest first"' && a2 === a1 && back === was, `${tag} H1 order`, "Oldest first keeps focus both ways", `focus ${a1}, then ${a2}; pressed ${was} -> ${back}`);
      });
    }
    await step(`${tag} L1`, async () => {
      await page.goto(listUrl, { waitUntil: "load" });
      const first = page.locator("[data-testid=list-scroll] [data-item-id]").first();
      await first.waitFor({ timeout: 15000 });
      const ids = await rowIds(page);
      await first.locator("a").first().focus();
      // End focuses the last loaded row, which can load the next page: pressed until focus stays, it reaches the end.
      let end = await focusOf(page);
      for (let i = 0; i < 10; i++) {
        await page.keyboard.press("End");
        await page.waitForTimeout(700);
        const at = await focusOf(page);
        if (at === end) break;
        end = at;
      }
      // The list is windowed: the last row is the lowest one drawn, not the last in the DOM.
      const lowest = await page.evaluate(() => [...document.querySelectorAll("[data-testid=list-scroll] [data-item-id]")].sort((a, b) => b.getBoundingClientRect().top - a.getBoundingClientRect().top)[0]?.getAttribute("data-item-id"));
      const inView = () => page.evaluate(() => {
        const r = document.activeElement?.closest("[data-item-id]")?.getBoundingClientRect();
        const box = document.querySelector("[data-testid=list-scroll]").getBoundingClientRect();
        return !!r && r.bottom > box.top && r.top < box.bottom;
      });
      check(end.startsWith("row ") && end !== `row ${ids[0]}` && end === `row ${lowest}` && (await inView()), `${tag} L1 End`, `End focuses the last loaded row (${end})`, `End: focus on ${end}, last row drawn ${lowest}`);
      await page.keyboard.press("Home");
      await page.waitForTimeout(500);
      const home = await focusOf(page);
      check(home === `row ${ids[0]}` && (await inView()), `${tag} L1 Home`, "Home focuses the first row", `Home: focus on ${home}`);
      await page.keyboard.press("PageDown");
      await page.waitForTimeout(500);
      const pd = await focusOf(page);
      check(pd.startsWith("row ") && pd !== home && (await inView()), `${tag} L1 PageDown`, `PageDown focuses a later row on screen (${pd})`, `PageDown: focus on ${pd}`);
      await page.keyboard.press("Space");
      const opened = await page.waitForURL(/\/i\/\d+/, { timeout: 5000 }).then(() => true, () => false);
      check(opened && page.url().includes(`/i/${pd.slice(4)}`), `${tag} L1 Space`, "Space opens the focused row", `Space: ${page.url()}`);
      await t.shot(page, `${tag}-L1`);
    });
    if (!vp.mobile) {
      await step(`${tag} C1`, async () => {
        await page.goto(listUrl, { waitUntil: "load" });
        await layoutButton(page).waitFor({ timeout: 15000 });
        await pickLayout(page, "Cards");
        const link = page.locator("[data-testid=list-scroll] [data-item-id] a.row-link").first();
        await link.waitFor({ timeout: 10000 });
        await link.focus();
        await page.keyboard.press("ArrowDown");
        await page.keyboard.press("ArrowUp");
        await page.waitForTimeout(300);
        const ring = await page.evaluate(() => {
          const a = document.activeElement;
          if (!a?.classList.contains("row-link")) return null;
          const s = getComputedStyle(a, "::before");
          const host = getComputedStyle(a.closest("[data-item-id]"));
          return { outline: s.outlineStyle, radius: s.borderTopLeftRadius, host: host.borderTopLeftRadius };
        });
        check(!!ring && ring.outline === "solid" && ring.radius !== "0px" && ring.radius === ring.host, `${tag} C1`, `the card's ring is rounded like the card (${ring?.radius})`, `ring ${JSON.stringify(ring)}`);
        await t.shot(page, `${tag}-C1`);
      });
    }
    await step(`${tag} D1`, async () => {
      await page.goto("/feeds", { waitUntil: "load" });
      await page.getByRole("button", { name: "Edit" }).click();
      const pencil = page.getByRole("button", { name: /^Edit / }).first();
      await pencil.waitFor({ timeout: 10000 });
      await pencil.focus();
      await page.keyboard.press("Enter");
      const dialog = page.getByRole("dialog", { name: "Edit feed" });
      await dialog.waitFor({ timeout: 10000 });
      await page.waitForTimeout(150);
      const first = await focusOf(page);
      await dialog.getByRole("textbox", { name: "Title" }).waitFor({ timeout: 10000 });
      // Tab from the sheet reaches Title before any footer button. (Firefox stops on the scrolling body first, as it does
      // on every scrollable region.)
      const stops = [];
      for (let i = 0; i < 3 && first === "dialog"; i++) {
        await page.keyboard.press("Tab");
        const at = await page.evaluate(() => {
          const a = document.activeElement;
          if (a?.matches("[data-sheet-body]")) return "body";
          return a?.tagName === "INPUT" ? `input ${a.getAttribute("aria-label") ?? a.labels?.[0]?.textContent ?? ""}` : `${a?.tagName.toLowerCase()} ${a?.textContent?.trim().slice(0, 20)}`;
        });
        stops.push(at);
        if (at !== "body") break;
      }
      const onTitle = await dialog.getByRole("textbox", { name: "Title" }).evaluate((el) => el === document.activeElement);
      check((first === "dialog" || first.startsWith("input")) && onTitle, `${tag} D1`, `the sheet opens on ${first}; Tab: ${stops.join(", ") || "-"}; Title has focus`, `opened on ${first}; then Tab: ${stops.join(", ")}`);
      await t.shot(page, `${tag}-D1`);
      await page.keyboard.press("Escape");
      await dialog.waitFor({ state: "detached", timeout: 10000 });
      await page.getByRole("button", { name: "Done editing" }).click();
    });
    if (!vp.mobile) {
      await step(`${tag} T1`, async () => {
        await page.goto(listUrl, { waitUntil: "load" });
        const tree = page.getByRole("tree", { name: "Feeds" });
        const feed = tree.locator('[role=treeitem]:not([data-folder-id])').first();
        await feed.waitFor({ timeout: 15000 });
        const name = await feed.getAttribute("aria-label");
        await feed.focus();
        await page.keyboard.press("Enter");
        await page.waitForURL(/feed=/, { timeout: 10000 });
        await page.waitForTimeout(600);
        const a1 = await focusOf(page);
        check(a1 === `treeitem ${name}`, `${tag} T1 Enter`, "Enter on a feed opens it and focus stays on the feed", `focus is on ${a1}`);
        const folder = tree.locator("[role=treeitem][data-folder-id]").first();
        const fname = await folder.getAttribute("aria-label");
        await folder.focus();
        await page.keyboard.press("Space");
        await page.waitForURL(/folder=/, { timeout: 10000 });
        await page.waitForTimeout(600);
        const a2 = await focusOf(page);
        check(a2 === `treeitem ${fname}`, `${tag} T1 Space`, "Space on a folder opens it and focus stays on the folder", `focus is on ${a2}`);
        // A click is not walking the tree: focus goes to the new list's heading, as for any arrival.
        await tree.locator("[role=treeitem]:not([data-folder-id]) a").last().click();
        await page.waitForURL(/feed=/, { timeout: 10000 });
        await page.waitForTimeout(600);
        const a3 = await focusOf(page);
        check(a3 === "h1", `${tag} T1 click`, "a click on a feed moves focus to the list's heading", `focus is on ${a3}`);
      });
    }
    await step(`${tag} S1`, async () => {
      await page.goto("/search?q=the", { waitUntil: "load" });
      await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
      await page.getByRole("searchbox", { name: "Search articles" }).focus();
      await page.keyboard.press("ArrowDown");
      await page.waitForTimeout(300);
      const at = await focusOf(page);
      check(at.startsWith("row "), `${tag} S1`, `ArrowDown goes into the results (${at})`, `focus is on ${at}`);
    });
    await step(`${tag} G1`, async () => {
      await page.goto("/settings/appearance", { waitUntil: "load" });
      const group = page.getByRole("group", { name: "Reduce motion" });
      await group.waitFor({ timeout: 15000 });
      const radios = group.getByRole("radio");
      const n = await radios.count();
      const was = await radios.evaluateAll((rs) => rs.findIndex((r) => r.checked));
      await radios.nth(was).focus();
      const y = await page.evaluate(() => document.scrollingElement.scrollTop);
      await page.keyboard.press("End");
      const end = await radios.evaluateAll((rs) => [rs.findIndex((r) => r === document.activeElement), rs.findIndex((r) => r.checked)]);
      await page.keyboard.press("Home");
      const home = await radios.evaluateAll((rs) => [rs.findIndex((r) => r === document.activeElement), rs.findIndex((r) => r.checked)]);
      const y2 = await page.evaluate(() => document.scrollingElement.scrollTop);
      check(end[0] === n - 1 && end[1] === n - 1 && home[0] === 0 && home[1] === 0, `${tag} G1`, `End and Home pick the last and first choice (page moved ${y2 - y} px)`, `End -> ${end}, Home -> ${home} of ${n}`);
      await radios.nth(was).focus();
      await page.keyboard.press("Space");
    });
    if (!vp.mobile) {
      await step(`${tag} P1`, async () => {
        await page.goto(listUrl, { waitUntil: "load" });
        await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
        await page.getByRole("button", { name: "Reading appearance" }).first().click();
        const panel = page.getByRole("dialog", { name: "Reading appearance" });
        await panel.waitFor({ timeout: 10000 });
        const fit = await panel.evaluate((el) => {
          el.scrollTop = el.scrollHeight;
          const p = el.getBoundingClientRect();
          const last = el.lastElementChild.getBoundingClientRect();
          return { panelBottom: Math.round(p.bottom), lastBottom: Math.round(last.bottom), vh: window.innerHeight };
        });
        check(fit.panelBottom <= fit.vh && fit.lastBottom <= fit.panelBottom, `${tag} P1`, `the panel fits the window (${JSON.stringify(fit)})`, `panel ${JSON.stringify(fit)}`);
        await t.shot(page, `${tag}-P1`);
        await page.keyboard.press("Escape");
      });
    }
  } finally {
    if (originalLayout) {
      await page.goto(listUrl, { waitUntil: "load" }).catch(() => {});
      const now = await layoutButton(page).getAttribute("aria-label").catch(() => null);
      if (now && !now.includes(`, ${originalLayout} layout`)) await pickLayout(page, originalLayout).catch(() => {});
    }
    const nowUnread = new Set(await unreadIds(page));
    const read = wasUnread.filter((id) => !nowUnread.has(id));
    if (read.length) await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: read, read: false } }).catch(() => {});
  }
});

await t.finish("keyboard focus");
