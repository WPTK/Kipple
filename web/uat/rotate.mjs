// UAT: turning the device (or resizing the window) keeps the list on the article the reader was looking at, and marks
// nothing read.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:rotate                 in another
//   npm run uat:rotate -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// Uses the seed's articles (All needs about three screens of them). For each width (desktop 1280x800, phone 375x812),
// with mark as read on scroll on:
//   R1  All, scrolled down and settled, then turned (width and height swapped): the same article is at the top;
//       run twice, from the width's own size and from the turned one, so the rows grow in one run and shrink in the other;
//   R2  turned back: the same article is at the top again;
//   R3  the turns, and a short scroll after them, marked no article below the one at the top read.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 15);
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
const ready = async (page) => {
  await scroller(page).locator("[data-item-id]").first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(300);
};
/** Scrolls the list by `px` as a reader would (a wheel event, then the scroll, in steps), and lets the marks land. */
const readerScroll = async (page, px) => {
  for (let done = 0; done < px; done += 400) {
    await scroller(page).evaluate((el) => {
      el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: 400 }));
      el.scrollBy(0, 400);
    });
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

  /** The loaded articles of All in list order, and which are read. */
  const library = async () => {
    const r = await page.request.get(`/api/items?${new URLSearchParams({ view: "all", limit: "500" })}`, { headers: { Accept: "application/json" } });
    const items = (await r.json()).items ?? [];
    return { order: items.map((i) => i.id), read: new Set(items.filter((i) => i.read).map((i) => i.id)) };
  };

  // Both ways round: rows taller after the turn, and rows shorter (which carries rows on screen above the top).
  for (const [id, w, h] of [["", vp.width, vp.height], ["-turned-first", vp.height, vp.width]]) {
  const tag = `${vp.id}${id}`;
  await step(`${tag} R`, async () => {
    const on = await patch({ "ui.mark_read_on_scroll": true });
    if (on !== 200) return t.fail(`${tag} R setup`, `mark as read on scroll not turned on (${on})`);
    try {
      await page.setViewportSize({ width: w, height: h });
      await page.goto("/l/all", { waitUntil: "load" });
      await ready(page);
      await readerScroll(page, 2400);
      const here = await topOf(page);
      if (!here.id || here.scrollTop < 400) return t.fail(`${tag} R setup`, `the list did not scroll far enough: ${JSON.stringify(here)}`);
      const before = await library();

      await turn(page, h, w);
      const turned = await topOf(page);
      check(turned.id === here.id, `${tag} R1`, "turned, the same article is at the top", `top was ${here.id}, turned ${turned.id} (scrolled ${turned.scrollTop})`);
      await t.shot(page, `${tag}-turned`);

      await turn(page, w, h);
      const back = await topOf(page);
      check(back.id === here.id, `${tag} R2`, "turned back, the same article is at the top", `top was ${here.id}, turned back ${back.id} (scrolled ${back.scrollTop})`);
      await t.shot(page, `${tag}-turned-back`);

      // A short scroll afterwards settles as any scroll does: it may mark the article at the top, never one below it.
      await readerScroll(page, 40);
      const after = await library();
      const limit = before.order.indexOf(here.id);
      const below = [...after.read].filter((id) => !before.read.has(id) && before.order.indexOf(id) > limit);
      check(below.length === 0, `${tag} R3`, "turning marks nothing read the reader did not scroll past", `${below.length} article(s) below the top were marked read: ${below.slice(0, 5).join(", ")}`);
    } finally {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await patch({ "ui.mark_read_on_scroll": null });
    }
  });
  }
});

await t.finish("rotate");
