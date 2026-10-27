// UAT: the list header contract (#639, #614): named controls, titles that wrap, tabs that fit, empty states that name their list.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/header-contract.mjs       in another
//   node uat/header-contract.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   C1  every control in the list header has an accessible name and a tooltip, and the previous and next feed arrows name the feed they go to;
//   C2  a feed with a 60-character name shows it whole in the header (the heading is not cut), the page does not scroll sideways, and
//       the heading's accessible name is the whole name;
//   C3  (phone) at text size 1.5 the Show tabs sit on one row or scroll sideways, never two rows;
//   C5  (phone) the controls drop to their own row at text size 1.5 and come back at 1, with the page open;
//   C6  (phone) the focus ring of a Show tab has room inside the tabs box;
//   C7  (phone, 568x320, text 1.5) nothing in the title row runs past its edge;
//   C4  an empty feed says "this feed", never "these feeds", and Starred, when nothing is starred, is the shared empty block.
// A disposable feed points at an address that never resolves, so nothing is fetched. A run that stopped half-way is cleaned up by
// the next one. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { deadFeed, start } from "./common.mjs";

const t = await start(import.meta.url, 21);
const { check, step } = t;

const NAME = "A rather long name for a feed that needs two lines";
const EMPTY = `UAT feed ${NAME}`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  page.setDefaultTimeout(8000);
  const patchDevice = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
  };
  const boot = await t.boot(page);
  const feeds = boot.feeds;
  if (feeds.length < 2) return t.fail(`${tag} setup`, "the seeded instance needs two feeds");
  const feed = feeds[0];

  await step(`${tag} C1`, async () => {
    await open(`/l/unread?feed=${feed.id}`);
    const header = page.locator("main header").first();
    const controls = await header.getByRole("button").evaluateAll((bs) =>
      bs.filter((b) => b.getClientRects().length).map((b) => ({ name: b.getAttribute("aria-label") || b.textContent.trim(), title: b.getAttribute("title") })),
    );
    const bare = controls.filter((c) => !c.name || !c.title);
    check(controls.length > 0 && bare.length === 0, `${tag} C1 named`, `${controls.length} controls, each with a name and a tooltip`, `without a name or tooltip: ${JSON.stringify(bare)}`);
    const arrows = await header.getByRole("group", { name: "Switch feed" }).getByRole("button").evaluateAll((bs) => bs.map((b) => ({ name: b.getAttribute("aria-label"), off: b.disabled })));
    const live = arrows.filter((a) => !a.off);
    const titles = feeds.map((f) => f.title);
    check(live.length > 0 && live.every((a) => titles.includes(a.name.replace(/^(Previous|Next) feed: /, ""))), `${tag} C1 arrows`, `the arrows name their feeds: ${live.map((a) => a.name).join("; ")}`, `the arrows are ${JSON.stringify(arrows)}`);
  });

  await step(`${tag} C2`, async () => {
    const was = (await (await page.request.get(`/api/feeds/${feed.id}`, { headers: { Accept: "application/json" } })).json()).custom_title ?? null;
    try {
      const r = await page.request.patch(`/api/feeds/${feed.id}`, { data: { custom_title: NAME }, headers: t.WRITE });
      if (!r.ok()) return t.fail(`${tag} C2 setup`, `the feed was not renamed: ${r.status()}`);
      for (const [w, h] of vp.mobile ? [[375, 812], [320, 568]] : [[1280, 800]]) {
        await page.setViewportSize({ width: w, height: h });
        await open(`/l/unread?feed=${feed.id}`);
        const heading = page.getByRole("heading", { name: NAME, exact: true });
        await heading.waitFor({ timeout: 15000 });
        const m = await heading.evaluate((h1) => {
          const span = h1.querySelector("span.line-clamp-2") ?? h1;
          return { cut: span.scrollHeight > span.clientHeight + 1, text: span.textContent, sideways: document.scrollingElement.scrollWidth > innerWidth };
        });
        check(!m.cut && m.text === NAME, `${tag} C2 ${w}x${h} whole`, "the whole name is shown", `the name is cut: ${m.text}`);
        check(!m.sideways, `${tag} C2 ${w}x${h} fit`, "the page does not scroll sideways", "the page scrolls sideways");
        await t.shot(page, `${tag}-long-name-${w}x${h}`);
      }
    } finally {
      await page.request.patch(`/api/feeds/${feed.id}`, { data: { custom_title: was }, headers: t.WRITE });
    }
  });

  if (vp.mobile) {
    await step(`${tag} C3`, async () => {
      if ((await patchDevice({ "client.text_size": 1.5 })) !== 200) return t.fail(`${tag} C3 setup`, "text size was not saved");
      try {
        await page.setViewportSize({ width: 375, height: 812 });
        await open("/l/unread");
        const nav = page.getByRole("navigation", { name: "Show" });
        await nav.waitFor();
        const m = await nav.evaluate((n) => {
          const tops = [...n.querySelectorAll("a")].map((a) => Math.round(a.getBoundingClientRect().top));
          return { tops, rows: new Set(tops).size, scrolls: n.scrollWidth > n.clientWidth + 1 };
        });
        check(m.rows === 1, `${tag} C3 one row`, `the tabs are one row${m.scrolls ? " (it scrolls sideways)" : ""}`, `the tabs are on ${m.rows} rows: tops ${m.tops.join(", ")}`);
        await t.shot(page, `${tag}-tabs-text-1-5`);
      } finally {
        await patchDevice({ "client.text_size": 1 });
      }
    });
  }

  if (vp.mobile) {
    // The controls drop to their own row when the title needs the width, and come back when it does not: a text size change
    // with the page open (the root font size, as the Text size setting sets it) re-measures.
    await step(`${tag} C5`, async () => {
      const NAMES = ["Feed name", "Medium feed name", "Medium length feed", "Medium length feed name", "A medium length feed name"];
      const was = (await (await page.request.get(`/api/feeds/${feed.id}`, { headers: { Accept: "application/json" } })).json()).custom_title ?? null;
      try {
        await page.setViewportSize({ width: 375, height: 812 });
        const below = () =>
          page.evaluate(() => {
            const row = document.querySelector("main header [data-title-row]");
            const h = row.querySelector("h1, h2").getBoundingClientRect();
            const c = row.querySelector('button[aria-label="List actions"]').getBoundingClientRect();
            return c.top >= h.bottom - 1;
          });
        const until = async (want) => {
          for (const end = Date.now() + 5000; Date.now() < end; await new Promise((r) => setTimeout(r, 100))) if ((await below()) === want) return true;
          return false;
        };
        // The longest name that still leaves the controls beside it at the normal size.
        let chosen = null;
        for (const name of NAMES) {
          await page.request.patch(`/api/feeds/${feed.id}`, { data: { custom_title: name }, headers: t.WRITE });
          await open(`/l/unread?feed=${feed.id}`);
          if (await until(false)) chosen = name;
          else break;
        }
        if (!chosen) return t.fail(`${tag} C5 setup`, "even the shortest name has the controls on their own row");
        await page.request.patch(`/api/feeds/${feed.id}`, { data: { custom_title: chosen }, headers: t.WRITE });
        await open(`/l/unread?feed=${feed.id}`);
        check(await until(false), `${tag} C5 beside`, `at the normal text size the controls sit beside "${chosen}"`, "the controls are already on their own row");
        const inline = await page.evaluate(() => document.documentElement.style.fontSize);
        await page.evaluate(() => (document.documentElement.style.fontSize = "36px"));
        check(await until(true), `${tag} C5 drop`, "at a larger text size the controls drop to their own row", "the controls stayed beside the title");
        await page.evaluate((v) => (document.documentElement.style.fontSize = v), inline);
        check(await until(false), `${tag} C5 back`, "back at the normal size the controls return beside the title", "the controls stayed on their own row");
      } finally {
        await page.request.patch(`/api/feeds/${feed.id}`, { data: { custom_title: was }, headers: t.WRITE });
      }
    });

    // The Show tabs scroll inside their own box: the focus ring of a tab is not cut by it.
    await step(`${tag} C6`, async () => {
      await page.setViewportSize({ width: 375, height: 812 });
      await open("/l/unread");
      const link = page.getByRole("navigation", { name: "Show" }).getByRole("link", { name: "All" });
      await link.focus();
      await page.keyboard.press("Shift+Tab");
      await page.keyboard.press("Tab");
      const m = await link.evaluate((a) => {
        const nav = a.closest("nav");
        const cs = getComputedStyle(a);
        const ring = Math.max(parseFloat(cs.outlineWidth) + parseFloat(cs.outlineOffset), 0) * (cs.outlineStyle === "none" ? 0 : 1);
        return { room: a.getBoundingClientRect().top - nav.getBoundingClientRect().top, ring, style: cs.outlineStyle };
      });
      check(m.room >= m.ring - 0.5, `${tag} C6 ring`, `the focus ring has room inside the tabs box (${m.room} px for ${m.ring} px)`, `the tabs box leaves ${m.room} px above a tab for a ${m.ring} px ring`);
    });

    // A short phone has the Show select, the feed switch and the controls on the title row: nothing runs past the edge at text size 1.5.
    await step(`${tag} C7`, async () => {
      if ((await patchDevice({ "client.text_size": 1.5 })) !== 200) return t.fail(`${tag} C7 setup`, "text size was not saved");
      try {
        await page.setViewportSize({ width: 568, height: 320 });
        await open(`/l/unread?feed=${feed.id}`);
        const m = await page.evaluate(() => {
          const row = document.querySelector("main header [data-title-row]");
          const edge = Math.max(...[...row.querySelectorAll("*")].filter((e) => e.getClientRects().length).map((e) => e.getBoundingClientRect().right));
          return { edge: Math.round(edge), right: Math.round(row.getBoundingClientRect().right), vw: innerWidth, sideways: document.scrollingElement.scrollWidth > innerWidth, select: !!document.querySelector('main header select[aria-label="Show"]') };
        });
        check(m.edge <= m.right + 1 && m.edge <= m.vw && !m.sideways, `${tag} C7 fit`, `nothing in the title row runs past its edge (select ${m.select})`, `the title row ends at ${m.right}, something reaches ${m.edge} of ${m.vw}, page sideways ${m.sideways}`);
        await t.shot(page, `${tag}-short-text-1-5`);
      } finally {
        await patchDevice({ "client.text_size": 1 });
      }
    });
  }

  await step(`${tag} C4`, async () => {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const xml = `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body><outline type="rss" text="${EMPTY}" title="${EMPTY}" xmlUrl="${deadFeed("empty-copy")}"/></body></opml>`;
    await t.importOpml(page, xml);
    const id = (await t.settle(page, (b) => b.feeds.some((f) => f.title === EMPTY))).b.feeds.find((f) => f.title === EMPTY)?.id;
    if (!id) return t.fail(`${tag} C4 setup`, "the empty feed was not added");
    await open(`/l/all?feed=${id}`);
    const empty = page.getByRole("status").filter({ has: page.getByRole("heading", { level: 2 }) }).first();
    await empty.waitFor({ timeout: 15000 });
    const text = await empty.innerText();
    check(/this feed/.test(text) && !/these feeds/.test(text), `${tag} C4 feed`, "an empty feed's message says this feed", `the message says: ${text.replace(/\s+/g, " ")}`);
    await t.shot(page, `${tag}-empty-feed`);
    if ((await t.boot(page)).counts.starred === 0) {
      await open("/l/starred");
      const starred = page.getByRole("status").filter({ has: page.getByRole("heading", { level: 2, name: "No starred articles" }) });
      check(await starred.isVisible({ timeout: 8000 }).catch(() => false), `${tag} C4 starred`, "Starred, empty, is the shared empty block", "Starred shows no empty block");
      await t.shot(page, `${tag}-empty-starred`);
    }
  });
});

await t.finish("header-contract");
