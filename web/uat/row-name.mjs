// UAT: the name of a list row's link (issue #513).
//
//   npm run seed                       in one terminal
//   npm run uat:row-name               in another
//   npm run uat:row-name -- --url http://127.0.0.1:7091 --browser webkit --headed
//
// The flow serves its own feed on a loopback port: two articles whose titles are far longer than a phone's row and
// carry the search word ("the") past the point where a one-line layout cuts the title, one left unread and one read.
// For each width (375 phone, 768 tablet, 1280 desktop) and each layout it opens the list that shows the layout's rows and checks:
//   N1  axe's label-content-name-mismatch finds nothing in the results (a link whose aria-label had to contain the
//       visible text failed it when a mark past the edge dropped out of the visible text);
//   N2  the link's accessible name, as the browser computes it (waited for, not read once), is the title for a read row and
//       "<title>, unread" for an unread one, and the link's text is the title alone;
//   N3  the generated ", unread" takes no room: nothing in the page scrolls sideways.
// The layout is the device's default (/l/all has no feed or folder override to set), and the search draws the default.
// Each pass says which layout it checks before it checks anything: on /l/all from the Show and sort button ("List
// options, <layout> layout"); the search page has no such button, so there from the device default the app has stored
// (the one value its list reads; an unset one is Editorial, "magazine"), which must be the layout the pass set.
// Every layout is checked on /l/all, where its own rows render; a search always draws its text rows (layouts/results.tsx),
// so it is checked once more, last.
// The layout the device started with is put back when a width is done, so later flows find the layout they expect.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { expect } from "@playwright/test";
import { URL } from "node:url";
import { start } from "./common.mjs";

const require = createRequire(import.meta.url);
const AXE = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
const t = await start(import.meta.url, 20);
const { check, step } = t;

const SEARCH = "/search?q=the";
// A layout and the page that draws it as its own rows (see the header).
const LAYOUTS = [
  { label: "Editorial", id: "magazine", path: "/l/all" },
  { label: "Cards", id: "cards", path: "/l/all" },
  { label: "Compact", id: "compact", path: "/l/all" },
  { label: "Inbox", id: "inbox", path: "/l/all" },
  { label: "Headlines", id: "headlines", path: "/l/all" },
  { label: "Gazette", id: "gazette", path: "/l/all" },
  // A search always draws its own text rows, whatever the device reads in; the Inbox pass is the device default there.
  { label: "Inbox", id: "inbox", path: SEARCH },
];
const FEED_TITLE = "UAT feed row name";
const WIDTHS = { desktop: [768, 1280], phone: [375] };
const UNREAD = "UAT row name: the long unread title that goes on and on and on, past the edge of the row, about the harness and the brain – and the end";
const READ = "UAT row name: the long read title that goes on and on and on, past the edge of the row, about the harness and the brain – and the end";

let base = "";
const server = createServer((req, res) => {
  const path = new URL(req.url ?? "/", "http://x").pathname;
  if (path !== "/feed.xml") return void res.writeHead(404).end();
  const item = (title, n, ago) =>
    `<item><title>${title}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-rowname-${n}</guid><pubDate>${new Date(Date.now() - ago).toUTCString()}</pubDate><description>&lt;p&gt;The body.&lt;/p&gt;</description></item>`;
  res
    .writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" })
    .end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT row name</title><link>${base}/</link><description>t</description>${item(UNREAD, 1, 0)}${item(READ, 2, 60000)}</channel></rss>`);
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

async function setLayout(page, label) {
  const btn = page.locator('button[aria-label^="Show and sort, "]').first();
  await btn.waitFor({ timeout: 15000 });
  if ((await btn.getAttribute("aria-label")) === `Show and sort, ${label} layout`) return;
  await btn.click();
  await page.getByRole("group", { name: "Layout" }).getByRole("menuitemradio", { name: new RegExp(`^${label}\\b`) }).click();
  await page.locator(`button[aria-label="Show and sort, ${label} layout"]`).first().waitFor({ timeout: 10000 });
  if (await page.getByRole("menu").isVisible().catch(() => false)) await page.keyboard.press("Escape");
}

/** The layout in effect, from the Show and sort button. */
async function layoutOf(page) {
  const btn = page.locator('button[aria-label^="Show and sort, "]').first();
  await btn.waitFor({ timeout: 15000 });
  return ((await btn.getAttribute("aria-label")) ?? "").replace(/^Show and sort, /, "").replace(/ layout$/, "");
}

try {
  await t.eachViewport(async (page, vp) => {
  await step(`${vp.id} N`, async () => {
    // A feed of this flow's own that an earlier run left behind (cleanup normally removes it) would make the add fail.
    for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED_TITLE)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
    const added = await t.addLocalFeed(page, `${base}/feed.xml`, FEED_TITLE, (items) => items.length >= 2);
    if (added.error) return t.fail(`${vp.id} N setup`, added.error);
    // The second article is read, the first stays unread.
    const read = added.items.find((i) => i.title === READ);
    if (!read) return t.fail(`${vp.id} N setup`, `the read article was not among the feed's items: ${added.items.map((i) => JSON.stringify(i.title)).join(", ")}`);
    const marked = await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [read.id], read: true } });
    if (!marked.ok()) return t.fail(`${vp.id} N setup`, `the article was not marked read: ${marked.status()}`);
    await page.goto("/l/all", { waitUntil: "load" });
    const startedWith = await layoutOf(page);
    try {
    for (const width of WIDTHS[vp.id]) {
      // Only when the width differs from the context's own: in Firefox a touch context that is resized (even to its own size) stops opening menus on click.
      if (page.viewportSize()?.width !== width) await page.setViewportSize({ width, height: 812 });
      for (const { label: layout, id, path } of LAYOUTS) {
        const tag = `${vp.id} ${width} ${layout}`;
        await page.goto("/l/all", { waitUntil: "load" });
        await setLayout(page, layout);
        await page.goto(path, { waitUntil: "load" });
        const shown =
          path === SEARCH
            ? await page.evaluate(() => { try { return JSON.parse(localStorage.getItem("kipple.device.v2") ?? "{}").layout ?? "magazine"; } catch { return undefined; } })
            : await layoutOf(page);
        check(shown === (path === SEARCH ? id : layout), `${tag} layout`, `the page checked is drawn as ${layout}`, `it is drawn as ${JSON.stringify(shown)}`);
        const unread = page.locator("a.row-link", { hasText: "long unread title" }).first();
        const readLink = page.locator("a.row-link", { hasText: "long read title" }).first();
        await unread.waitFor({ timeout: 15000 });
        await readLink.waitFor({ timeout: 15000 });

        await page.evaluate(AXE);
        const bad = await page.evaluate(() =>
          window.axe.run(document, { runOnly: { type: "rule", values: ["label-content-name-mismatch"] } }).then((r) => r.violations.flatMap((v) => v.nodes.map((n) => n.html.slice(0, 120)))),
        );
        check(bad.length === 0, `${tag} N1 axe`, "no row link has a label that disagrees with its visible text", `${bad.length} link(s): ${bad.join(" | ")}`);

        // WebKit has, now and then, given an empty aria snapshot for a link that was attached and visible (#783; the cause is
        // not known). The name is a state of the link, so the flow waits for it (toHaveAccessibleName retries for up to 5 s)
        // and does not read one snapshot. When the name never arrives, the detail carries the raw aria snapshot and the
        // element's state at that moment.
        const nameIs = async (link, want) => {
          try {
            await expect(link).toHaveAccessibleName(want, { timeout: 5000 });
            return { name: want };
          } catch (e) {
            const snap = await link.ariaSnapshot().catch((err) => `no snapshot: ${String(err)}`);
            const m = snap.match(/- '?link "((?:[^"\\]|\\.)*)"/);
            const state = await link
              .evaluate((a) => {
                const r = a.getBoundingClientRect();
                const cs = globalThis.getComputedStyle(a);
                return { box: [r.x, r.y, r.width, r.height].map(Math.round), display: cs.display, visibility: cs.visibility, opacity: cs.opacity, attached: a.isConnected, textLength: a.textContent?.length ?? 0, rowLinks: a.ownerDocument.querySelectorAll("a.row-link").length };
              })
              .catch((err) => ({ error: String(err) }));
            return { name: m ? m[1].replace(/\\"/g, '"') : undefined, snap, state, why: String(e.message).split("\n")[0] };
          }
        };
        const named = (r) => `named ${JSON.stringify(r.name)}${r.snap === undefined ? "" : `; snapshot ${JSON.stringify(r.snap)}; element ${JSON.stringify(r.state)}; ${r.why}`}`;
        // The word is the flag's value (src/layouts/parts.tsx sets it, index.css draws it): read it from the page, so the
        // flow and the app cannot drift.
        const word = await unread.getAttribute("data-row-unread");
        check(!!word, `${tag} N2 flag`, "an unread row's link carries the word that names it", `data-row-unread is ${JSON.stringify(word)}`);
        check((await readLink.getAttribute("data-row-unread")) === null, `${tag} N2 read flag`, "a read row's link carries none", "a read row is flagged");
        const [un, rd] = [await nameIs(unread, `${UNREAD}, ${word}`), await nameIs(readLink, READ)];
        check(un.name === `${UNREAD}, ${word}`, `${tag} N2 unread name`, "an unread row's link is named the title, then unread", named(un));
        check(rd.name === READ, `${tag} N2 read name`, "a read row's link is named the title alone", named(rd));
        const text = await unread.evaluate((a) => a.textContent);
        check(text === UNREAD, `${tag} N2 text`, "the link's text is the title alone", `text ${JSON.stringify(text)}`);

        const over = await page.evaluate(() => document.scrollingElement.scrollWidth - window.innerWidth);
        check(over <= 0, `${tag} N3 width`, "nothing scrolls sideways", `the page is ${over}px wider than the window`);
        // For a person to look at (--screenshots): the unread row with its keyboard focus ring.
        await unread.focus();
        await page.keyboard.press("ArrowDown");
        await page.keyboard.press("ArrowUp");
        await t.shot(page, tag.replace(/ /g, "-"));
      }
    }
    } finally {
      // The next flows start from the layout this one found.
      await page.goto("/l/all", { waitUntil: "load" });
      await setLayout(page, startedWith).catch((e) => t.fail(`${vp.id} N restore`, `the ${startedWith} layout was not put back: ${String(e.message).split("\n")[0]}`));
    }
  });
});

} finally {
  server.close();
}
await t.finish("row name");
