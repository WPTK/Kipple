// UAT: the app's chrome on a phone: the tab bar and headers at the largest text size, the offline banner, the undo
// toast and the list menu, and the reader's read state.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:phone-chrome           in another
//   npm run uat:phone-chrome -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The flow serves its own feed of twelve articles on a loopback port. For each width (desktop 1280x800, phone 375x812)
// it checks:
//   C1  (phone, at 320x568 with text size Largest) on Feeds, Search, Stats and a list, the screen title is shown (whole,
//       or a long feed name with an ellipsis), all five tabs fit with their names whole, and no header control or
//       view pill runs past the screen;
//   C2  a setting changed while offline shows no "Couldn't save your settings" message over the offline banner, and
//       is saved once the device is back online;
//   C3  with the undo toast on screen, the end of the list scrolls clear of it (the last row is above the toast);
//   C4  in a list with nothing unread, List actions offers Mark all as read off with "Nothing unread here", and after
//       a reload Undo is off with "Nothing to undo";
//   C5  the reader's Read or Unread is plain text, not a pill that looks like a button.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 19);
const { check, step } = t;

const TITLES = Array.from({ length: 12 }, (_, i) => `UAT chrome article ${i + 1}`);
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
let base = "";
const server = createServer((req, res) => {
  if ((req.url ?? "").split("?")[0] !== "/feed.xml") return res.writeHead(404).end("not found");
  const items = TITLES.map(
    (title, i) =>
      `<item><title>${title}</title><link>${base}/page/${i}</link><guid isPermaLink="false">uat-phone-chrome-${i}</guid><pubDate>${new Date(Date.now() - i * 60000).toUTCString()}</pubDate><description>${esc(`<p>Body of ${title}.</p>`)}</description></item>`,
  ).join("");
  res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT phone chrome</title><link>${base}/</link><description>t</description>${items}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

/** PATCH this browser's device profile, as the app does. */
const patchDevice = (page, body) =>
  page.evaluate((b) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) }).then((r) => r.status), body);

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  let feedId = null;
  await step(`${tag} setup`, async () => {
    const added = await t.addLocalFeed(page, `${base}/feed.xml`, "UAT feed phone chrome", (items) => items.length >= TITLES.length);
    if (added.error) return t.fail(`${tag} setup`, added.error);
    feedId = added.feedId;
  });
  if (!feedId) return;
  const feedList = `/l/all?feed=${feedId}`;
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(500);
  };

  if (vp.mobile) {
    await step(`${tag} C1`, async () => {
      const status = await patchDevice(page, { "client.text_size": 1.5 });
      if (status !== 200) return t.fail(`${tag} C1 setup`, `text size not set (${status})`);
      await page.setViewportSize({ width: 320, height: 568 });
      // Put back in a finally: a step that stops half-way must not leave the later ones on a tiny screen at Largest.
      try {
      for (const path of ["/feeds", "/search", "/stats", feedList]) {
        await open(path);
        const m = await page.evaluate(() => {
          const h = document.querySelector("main h1");
          const nav = [...document.querySelectorAll('nav[aria-label="Primary"]')].pop();
          const tabs = nav ? [...nav.querySelectorAll("a")].map((a) => ({ name: a.innerText.replace(/\s+/g, " ").trim(), sw: a.scrollWidth, cw: a.clientWidth })) : [];
          const controls = [...document.querySelectorAll("main header button, main header a")].filter((b) => b.getClientRects().length);
          return {
            title: h ? { text: h.textContent, sw: h.scrollWidth, cw: h.clientWidth, ellipsis: getComputedStyle(h).textOverflow === "ellipsis" } : null,
            nav: nav ? { sw: nav.scrollWidth, cw: nav.clientWidth } : null,
            tabs,
            past: controls.filter((b) => b.getBoundingClientRect().right > window.innerWidth + 0.5).map((b) => b.getAttribute("aria-label") || b.textContent),
          };
        });
        const where = `${tag} C1 ${path.split("?")[0]}`;
        // A short title is whole; a long one (a feed's name) may end in an ellipsis, but never collapses to nothing.
        const shown = m.title && m.title.cw >= 40 && (m.title.sw <= m.title.cw || m.title.ellipsis);
        check(shown, `${where} title`, `the "${m.title?.text}" title is shown`, `the title is cut off or gone: ${JSON.stringify(m.title)}`);
        const cut = m.tabs.filter((x) => x.sw > x.cw);
        check(m.nav && m.nav.sw <= m.nav.cw && m.tabs.length === 5 && cut.length === 0, `${where} tabs`, "all five tabs fit, names whole", `tabs overflow: ${JSON.stringify(m)}`);
        check(m.past.length === 0, `${where} controls`, "no header control runs past the screen", `past the right edge: ${m.past.join(", ")}`);
        await t.shot(page, `${tag}-largest-${path.split("?")[0].replace(/\//g, "-")}`);
      }
      } finally {
        await patchDevice(page, { "client.text_size": 1 });
        await page.setViewportSize({ width: vp.width, height: vp.height });
      }
    });
  }

  await step(`${tag} C2`, async () => {
    // A whole list: on a feed's list the layout menu sets that feed's own layout, not the device's.
    await open("/l/all");
    const context = page.context();
    await context.setOffline(true);
    try {
      await page.waitForTimeout(500);
      const layoutBtn = page.locator('button[aria-label^="List options, "]').first();
      await layoutBtn.click();
      await page.getByRole("menuitemradio", { name: /^Cards(?![A-Za-z])/ }).click();
      await page.keyboard.press("Escape");
      await page.waitForTimeout(3000);
      check((await page.getByTestId("save-status").count()) === 0, `${tag} C2 offline`, "a setting changed offline shows no save error", `"${await t.textOf(page.getByTestId("save-status"))}" is shown while offline`);
      await t.shot(page, `${tag}-offline-change`);
    } finally {
      await context.setOffline(false);
    }
    let saved = false;
    const end = Date.now() + 10000;
    while (!saved && Date.now() < end) {
      await page.waitForTimeout(500);
      const b = await t.boot(page);
      saved = b.device?.merged?.["client.layout"] === "cards";
    }
    check(saved, `${tag} C2 online`, "back online, the change is saved", "the layout chosen offline never reached the server");
    check((await page.getByTestId("save-status").count()) === 0, `${tag} C2 no error`, "no save error after coming back", "a save error is shown after coming back online");
    await patchDevice(page, { "client.layout": null });
  });

  await step(`${tag} C3`, async () => {
    await open(feedList);
    const rows = page.locator("[data-item-id]");
    await rows.first().waitFor({ timeout: 15000 });
    const first = TITLES[0];
    await page.getByRole("button", { name: `More actions: ${first}` }).first().click({ force: true });
    await page.getByRole("menuitem", { name: "Mark as read" }).click();
    const toast = page.getByTestId("undo-region").getByRole("button", { name: "Undo" });
    await toast.waitFor({ timeout: 10000 });
    await page.getByTestId("list-scroll").evaluate((el) => el.scrollTo(0, el.scrollHeight));
    await page.waitForTimeout(400);
    const m = await page.evaluate(() => {
      const rs = [...document.querySelectorAll("[data-item-id]")];
      const last = rs[rs.length - 1]?.getBoundingClientRect();
      const box = document.querySelector('[data-testid="undo-region"] > div')?.getBoundingClientRect();
      return { lastBottom: last ? Math.round(last.bottom) : null, toastTop: box ? Math.round(box.top) : null };
    });
    check(m.lastBottom !== null && m.toastTop !== null && m.lastBottom <= m.toastTop, `${tag} C3`, "the list's end scrolls clear of the undo toast", `the last row ends at ${m.lastBottom}, the toast starts at ${m.toastTop}`);
    await t.shot(page, `${tag}-toast-end`);
  });

  await step(`${tag} C4`, async () => {
    await open(feedList);
    await page.getByRole("button", { name: "List actions" }).click();
    await page.getByRole("menuitem", { name: /Mark all as read/ }).click();
    const settled = await t.settle(page, (b) => b.feeds.find((f) => f.id === feedId)?.unread === 0);
    if (!settled.ok) return t.fail(`${tag} C4 setup`, "the feed did not reach 0 unread");
    await page.reload({ waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await page.getByRole("button", { name: "List actions" }).click();
    const mark = page.getByRole("menuitem", { name: /Mark all as read/ });
    const undo = page.getByRole("menuitem", { name: /^Undo/ });
    check(
      (await mark.getAttribute("aria-disabled")) === "true" && /Nothing unread here/.test(await mark.innerText()),
      `${tag} C4 mark`,
      "Mark all as read is off, saying there is nothing unread",
      `Mark all as read: disabled ${await mark.getAttribute("aria-disabled")}, "${await mark.innerText()}"`,
    );
    check(
      (await undo.getAttribute("aria-disabled")) === "true" && /Nothing to undo/.test(await undo.innerText()),
      `${tag} C4 undo`,
      "after a reload Undo is off, saying there is nothing to undo",
      `Undo: disabled ${await undo.getAttribute("aria-disabled")}, "${await undo.innerText()}"`,
    );
    await t.shot(page, `${tag}-list-actions`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} C5`, async () => {
    await open(feedList);
    await page.locator("a.row-link").filter({ hasText: TITLES[1] }).first().click();
    const state = page.getByTestId("read-state");
    await state.waitFor({ timeout: 15000 });
    const s = await state.evaluate((el) => {
      const cs = getComputedStyle(el);
      return { border: cs.borderTopWidth, radius: cs.borderTopLeftRadius, bg: cs.backgroundColor, tag: el.tagName, role: el.getAttribute("role") };
    });
    check(
      s.border === "0px" && s.radius === "0px" && s.tag === "SPAN" && !s.role,
      `${tag} C5`,
      "the read state is plain text",
      `the read state looks like a control: ${JSON.stringify(s)}`,
    );
  });
});

server.close();
await t.finish("phone chrome");
