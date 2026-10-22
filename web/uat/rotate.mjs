// UAT: turning the device (or resizing the window) keeps the list on the article the reader was looking at, and marks
// nothing read.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:rotate                 in another
//   npm run uat:rotate -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// Uses the seed's articles (All needs about three screens of them), marked unread before each run. For each width
// (desktop 1280x800, phone 375x812), in Magazine and in the Gazette, with mark as read on scroll on, each run twice
// (from the width's own size and from the turned one, so the rows grow in one run and shrink in the other):
//   R0  All, scrolled down and settled: the scroll marked the articles it passed (the control for R3);
//   R1  turned (width and height swapped): the same article is at the top (Magazine);
//   R2  turned back: the same article is at the top again (Magazine);
//   R3  the turns, and a short scroll after them, marked no article below the one at the top read.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check, step } = t;

const scroller = (page) => page.getByTestId("list-scroll");
/** The article whose row is highest in view in the list, and how far the list is scrolled. */
const topOf = (page) =>
  scroller(page).evaluate((el) => {
    const top = el.getBoundingClientRect().top + 1;
    let best = null;
    for (const row of el.querySelectorAll("[data-item-id]")) {
      const r = row.getBoundingClientRect();
      if (r.bottom <= top) continue;
      if (!best || r.top < best.y) best = { id: row.getAttribute("data-item-id"), y: r.top };
    }
    return { id: best?.id ?? null, scrollTop: Math.round(el.scrollTop) };
  });
/** Where article `id` is, for a failure message: its row's top relative to the list's top, and the list's width. */
const whereIs = (page, id) =>
  scroller(page).evaluate((el, id) => {
    const row = el.querySelector(`[data-item-id="${CSS.escape(id)}"]`);
    const at = row ? `${Math.round(row.getBoundingClientRect().top - el.getBoundingClientRect().top)} px from the top` : "not on screen";
    return `${id} is ${at}, list ${el.clientWidth} px wide`;
  }, id);
/** How far down All the server's view of it is read (the flow scrolls a few screens from wherever All opens). */
const MAX_ARTICLES = 1000;
const ready = async (page) => {
  await scroller(page).locator("[data-item-id]").first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(300);
};
/** Scrolls the list by `px` as a reader would (a wheel event, then the scroll, in steps of at most 400), and lets the marks land. */
const readerScroll = async (page, px) => {
  for (let done = 0; done < px; done += 400) {
    await scroller(page).evaluate(
      (el, by) => {
        el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: by }));
        el.scrollBy(0, by);
      },
      Math.min(400, px - done),
    );
    await page.waitForTimeout(60);
  }
  await page.waitForTimeout(1500);
};
/** Turns the page to `w` x `h` and waits well past the mark-on-scroll settle (700 ms). */
const turn = async (page, w, h) => {
  await page.setViewportSize({ width: w, height: h });
  await page.waitForTimeout(2000);
};

await t.eachViewport(async (page, vp) => {
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
  /** All in list order, and which are read: the server's pages (at most 100 articles each) up to MAX_ARTICLES. */
  const library = async () => {
    const order = [];
    const read = new Set();
    let cursor = null;
    do {
      const q = new URLSearchParams({ view: "all", limit: "100", ...(cursor ? { cursor } : {}) });
      const r = await page.request.get(`/api/items?${q}`, { headers: { Accept: "application/json" } });
      const body = await r.json();
      for (const i of body.items ?? []) {
        order.push(i.id);
        if (i.read) read.add(i.id);
      }
      cursor = body.next_cursor;
    } while (cursor && order.length < MAX_ARTICLES);
    return { order, read };
  };
  /** Marks `ids` unread again, so every run scrolls over unread articles (the seed's All is only a few screens long). */
  const markUnread = async (ids) => {
    for (let i = 0; i < ids.length; i += 100) {
      const status = await page.evaluate(
        (x) => fetch("/api/items/mark-read", { method: "POST", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify({ ids: x, read: false }) }).then((r) => r.status),
        ids.slice(i, i + 100),
      );
      if (status !== 200) throw new Error(`marking articles unread answered ${status}`);
    }
  };

  for (const layout of ["magazine", "gazette"]) {
    // Both ways round: rows taller after the turn, and rows shorter (which carries rows on screen above the top).
    for (const [id, w, h] of [["", vp.width, vp.height], ["-turned-first", vp.height, vp.width]]) {
      const tag = `${vp.id}${layout === "gazette" ? "-gazette" : ""}${id}`;
      await step(`${tag} R`, async () => {
        const on = await patch({ "ui.mark_read_on_scroll": true, "client.layout": layout });
        if (on !== 200) return t.fail(`${tag} R setup`, `mark as read on scroll and the ${layout} layout not set (${on})`);
        try {
          await page.setViewportSize({ width: w, height: h });
          await page.goto("/l/all", { waitUntil: "load" });
          await markUnread([...(await library()).read]);
          // Each run starts at the top of All, not at the place the run before left it: a place near the end of the
          // shorter pages leaves nothing to scroll over, so nothing is passed (the restore itself marks nothing, P5 in
          // scroll-restore.mjs covers coming back to a place with room).
          await page.evaluate(() => localStorage.removeItem("kipple.listPlace.v1"));
          await page.reload({ waitUntil: "load" });
          await ready(page);
          const opened = await library();
          await readerScroll(page, 2400);
          const here = await topOf(page);
          if (!here.id || here.scrollTop < 400) return t.fail(`${tag} R setup`, `the list did not scroll far enough: ${JSON.stringify(here)}`);
          const before = await library();
          const limit = before.order.indexOf(here.id);
          if (limit < 0) return t.fail(`${tag} R setup`, `the top article ${here.id} is not among the first ${before.order.length} of All`);
          // The control: the scroll itself marked what it passed, so the check below can tell marking from none at all.
          const passed = [...before.read].filter((x) => !opened.read.has(x));
          check(passed.length > 0, `${tag} R0`, "scrolling down marks the articles it passes", "the scroll marked nothing read: mark as read on scroll is not working, so R3 proves nothing");

          await turn(page, h, w);
          const turned = await topOf(page);
          // The Gazette lays its pages out again for the new size, so only what it marks is checked there.
          if (layout !== "gazette") {
            check(turned.id === here.id, `${tag} R1`, "turned, the same article is at the top", `top was ${here.id} (scrolled ${here.scrollTop}), turned ${turned.id} (scrolled ${turned.scrollTop}; ${await whereIs(page, here.id)})`);
          }
          await t.shot(page, `${tag}-turned`);

          await turn(page, w, h);
          const back = await topOf(page);
          if (layout !== "gazette") {
            check(back.id === here.id, `${tag} R2`, "turned back, the same article is at the top", `top was ${here.id} (scrolled ${here.scrollTop}), turned back ${back.id} (scrolled ${back.scrollTop}; ${await whereIs(page, here.id)})`);
          }
          await t.shot(page, `${tag}-turned-back`);

          // A short scroll afterwards settles as any scroll does: it may mark the article at the top, never one below it.
          await readerScroll(page, 40);
          const after = await library();
          let below = [...after.read].filter((x) => !before.read.has(x) && before.order.indexOf(x) > limit);
          if (layout === "gazette") {
            // The Gazette reads in its printed order, not the list's: a story in another column can be passed (wholly above the
            // top of the list) and sit later in All. Judge by where the stories are: nothing marked may reach the top or lie below it.
            const marked = [...after.read].filter((x) => !before.read.has(x));
            const finalTop = (await topOf(page)).id;
            below = await scroller(page).evaluate(
              (el, [ids, topId]) => {
                const top = el.getBoundingClientRect().top + 1;
                return ids.filter((id) => {
                  const a = el.querySelector(`[data-item-id="${CSS.escape(id)}"]`);
                  return a && id !== topId && a.getBoundingClientRect().bottom > top;
                });
              },
              [marked, finalTop],
            );
          }
          check(below.length === 0, `${tag} R3`, "turning marks nothing read the reader did not scroll past", `${below.length} article(s) below the top were marked read: ${below.slice(0, 5).join(", ")}`);
        } finally {
          await page.setViewportSize({ width: vp.width, height: vp.height });
          await patch({ "ui.mark_read_on_scroll": null, "client.layout": null });
        }
      });
    }
  }
});

await t.finish("rotate");
