// UAT: the Gazette keeps the reader's place when the page changes under them, with the browser's scroll anchoring
// off (the page keeps the story at the top still itself, web/src/screens/pageSeen.ts).
//
//   npm run seed                            in one terminal (serves the seed on 127.0.0.1:1919)
//   npm run uat:gazette-place               in another
//   npm run uat:gazette-place -- --url http://127.0.0.1:7091 --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812), in the Gazette of All, scrolled a few screens down:
//   G0  the list's scroller computes overflow-anchor: none (the browser does not move the scroll position on its own);
//   G1  the text gets smaller (the root font size, which is what the text size setting changes): the same story is at
//       the top, where it was;
//   G2  the text goes back to its size: the same story is at the top again;
//   G3  a story above the screen gets shorter (as when its picture fails and its box goes): the same story is at the
//       top, where it was; and none of G1 to G3 marked anything read (mark as read while scrolling is on);
//   G4  desktop: a story gets shorter above the story at the top, in a column shorter than the tallest in its block (the
//       page's height does not change): the same story is at the top, where it was, and nothing is marked read;
//   G5  the reader scrolls a screen on: the story that was at the top is marked read.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 8);
const { check, step } = t;

const scroller = (page) => page.getByTestId("list-scroll");
/**
 * The story at the top of the screen (the highest one whose top is in the list, where the reader's eye is), its top's
 * distance from the list's top, and how far the list is scrolled.
 */
const topOf = (page) =>
  scroller(page).evaluate((el) => {
    const top = el.getBoundingClientRect().top;
    let best = null;
    for (const a of el.querySelectorAll("article[data-item-id]")) {
      const r = a.getBoundingClientRect();
      if (r.top < top - 0.5 || r.top >= el.getBoundingClientRect().bottom) continue;
      if (!best || r.top < best.y) best = { id: a.getAttribute("data-item-id"), y: r.top };
    }
    return { id: best?.id ?? null, y: best ? Math.round(best.y - top) : null, scrollTop: Math.round(el.scrollTop) };
  });
const readerScroll = async (page, px) => {
  for (let done = 0; done < px; done += 400) {
    await scroller(page).evaluate((el, by) => {
      el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: by }));
      el.scrollBy(0, by);
    }, Math.min(400, px - done));
    await page.waitForTimeout(60);
  }
  await page.waitForTimeout(1200);
};
const same = (a, b) => a.id === b.id && Math.abs(a.y - b.y) <= 2;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
  // The stories the list marks read as the reader scrolls past them.
  const marks = new Set();
  const onRequest = (r) => {
    if (r.method() !== "POST" || !r.url().endsWith("/api/items/mark-read")) return;
    const body = r.postDataJSON();
    if (body?.reason === "scroll") for (const id of body.ids ?? []) marks.add(String(id));
  };
  await step(`${tag} G`, async () => {
    const on = await patch({ "client.layout": "gazette", "ui.mark_read_on_scroll": true });
    if (on !== 200) return t.fail(`${tag} G setup`, `the Gazette and mark as read while scrolling not set (${on})`);
    page.on("request", onRequest);
    try {
      await page.goto("/l/all", { waitUntil: "load" });
      await page.evaluate(() => localStorage.removeItem("kipple.listPlace.v1"));
      await page.reload({ waitUntil: "load" });
      await scroller(page).locator("article[data-item-id]").first().waitFor({ timeout: 15000 });
      await page.waitForTimeout(500);
      check((await scroller(page).evaluate((el) => getComputedStyle(el).overflowAnchor)) === "none", `${tag} G0`, "the Gazette's scroller turns scroll anchoring off");
      // A third of the way down: smaller text shortens the page, and a place near its end would be clamped by it.
      await readerScroll(page, Math.min(1600, Math.round((await scroller(page).evaluate((el) => el.scrollHeight - el.clientHeight)) / 3)));
      const here = await topOf(page);
      if (!here.id || here.scrollTop < 300) return t.fail(`${tag} G setup`, `the Gazette did not scroll far enough: ${JSON.stringify(here)}`);
      const before = new Set(marks);

      await page.evaluate(() => (document.documentElement.style.fontSize = "13px"));
      await page.waitForTimeout(800);
      const smaller = await topOf(page);
      check(same(here, smaller), `${tag} G1`, "smaller text keeps the story at the top where it was", `${JSON.stringify(here)} -> ${JSON.stringify(smaller)}`);
      await page.evaluate(() => (document.documentElement.style.fontSize = ""));
      await page.waitForTimeout(800);
      const back = await topOf(page);
      check(same(here, back), `${tag} G2`, "the text back to its size keeps it there too", `${JSON.stringify(here)} -> ${JSON.stringify(back)}`);

      // A story above the screen, in the column of the story at the top, gets half as tall, as one does when its
      // picture fails to load and its box goes (the seed's feeds carry no pictures).
      const shrunk = await scroller(page).evaluate((el, id) => {
        const k = el.querySelector(`[data-item-id="${CSS.escape(id)}"]`).getBoundingClientRect();
        const above = [...el.querySelectorAll("article[data-item-id]")].find((a) => {
          const r = a.getBoundingClientRect();
          return r.bottom <= Math.min(k.top, el.getBoundingClientRect().top) && r.left < k.right && r.right > k.left && r.height > 60;
        });
        if (!above) return null;
        above.style.height = `${Math.round(above.getBoundingClientRect().height / 2)}px`;
        above.style.overflow = "hidden";
        return above.getAttribute("data-item-id");
      }, back.id);
      if (!shrunk) return t.fail(`${tag} G3 setup`, "no story above the screen to shorten");
      await page.waitForTimeout(1200);
      const after = await topOf(page);
      check(same(back, after), `${tag} G3`, "a story above the screen getting shorter keeps the story at the top where it was", `${JSON.stringify(back)} -> ${JSON.stringify(after)}`);
      const moved = [...marks].filter((id) => !before.has(id));
      check(moved.length === 0, `${tag} G1-G3 marks`, "nothing the page moved was marked read", `marked ${moved.join(", ")}`);

      // A column shorter than the tallest in its block: a story in it getting shorter moves the stories under it and
      // leaves the page's height as it was. On a phone every block has one column.
      const target = await scroller(page).evaluate((el) => {
        const list = el.getBoundingClientRect().top;
        const seen = [];
        for (const grid of el.querySelectorAll("[data-page] .grid")) {
          seen.push([...grid.children].map((c) => c.querySelectorAll("article[data-item-id]").length).join("+"));
          const cols = [...grid.children].map((c) => [...c.querySelectorAll("article[data-item-id]")]);
          if (cols.length < 2) continue;
          const ends = cols.map((c) => (c.length ? c[c.length - 1].getBoundingClientRect().bottom : -Infinity));
          const tallest = Math.max(...ends);
          for (const [i, c] of cols.entries()) {
            // Not the one tallest column (another as tall holds the block's height when this one shrinks).
            if (ends[i] > tallest - 1 && ends.filter((e) => e > tallest - 1).length === 1) continue;
            for (let j = 1; j < c.length; j++) {
              const at = Math.round(c[j].getBoundingClientRect().top - list + el.scrollTop - 4);
              if (c[j - 1].getBoundingClientRect().height > 60 && at >= 300 && at <= el.scrollHeight - el.clientHeight - 50)
                return { id: c[j].getAttribute("data-item-id"), at };
            }
          }
        }
        return { blocks: seen.join(" ") };
      });
      if (!target.id && vp.width >= 640) return t.fail(`${tag} G4 setup`, `no story under another in a column shorter than the tallest (stories per column: ${target.blocks})`);
      if (target.id) await inAShorterColumn(target);
      else t.pass(`${tag} G4`, "one column on a phone: nothing shorter than the tallest");

      // The reader scrolls a screen on: the unread stories in view that the scroll carries above the top are marked read.
      // First to the first unread story below the top (what was passed on the way here is read).
      await scroller(page).evaluate((el) => {
        const list = el.getBoundingClientRect().top;
        const next = [...el.querySelectorAll("article[data-item-id]:not([data-read])")].find((a) => a.getBoundingClientRect().top > list);
        if (!next) return;
        const at = Math.round(next.getBoundingClientRect().top - list + el.scrollTop - 50);
        el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: at - el.scrollTop }));
        el.scrollTop = at;
      });
      await page.waitForTimeout(1200);
      const on5 = await scroller(page).evaluate((el) => {
        const by = Math.min(el.clientHeight + 100, el.scrollHeight - el.clientHeight - el.scrollTop);
        const list = el.getBoundingClientRect();
        const ids = [...el.querySelectorAll("article[data-item-id]:not([data-read])")]
          .filter((a) => {
            const r = a.getBoundingClientRect();
            return r.bottom > list.top && r.top < list.bottom && r.bottom - list.top <= by;
          })
          .map((a) => a.getAttribute("data-item-id"));
        return { by, ids, scrollTop: el.scrollTop, end: el.scrollHeight - el.clientHeight };
      });
      if (!on5.ids.length) return t.fail(`${tag} G5 setup`, `no unread story in view to scroll past (${JSON.stringify(on5)})`);
      await readerScroll(page, on5.by);
      await page.waitForTimeout(800);
      const missed = on5.ids.filter((id) => !marks.has(id));
      check(missed.length === 0, `${tag} G5`, "the stories the reader scrolls past are still marked read", `not marked: ${missed.join(", ")}`);
    } finally {
      page.off("request", onRequest);
      await page.evaluate(() => (document.documentElement.style.fontSize = ""));
      await patch({ "client.layout": null, "ui.mark_read_on_scroll": null });
      // Put back what this flow marked read, for the flows after it.
      if (marks.size) await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: [...marks], read: false } }).catch(() => {});
    }
  });

  /** G4: the reader scrolls `target` to the top, and the story above it in its column gets half as tall. */
  async function inAShorterColumn(target) {
    // Set, not scrollBy: in WebKit a scrollBy of -1183.5 from 1553 landed at the top.
    await scroller(page).evaluate((el, at) => {
      el.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: at - el.scrollTop }));
      el.scrollTop = at;
    }, target.at);
    await page.waitForTimeout(1200);
    const there = await topOf(page);
    if (there.id !== target.id) return t.fail(`${tag} G4 setup`, `the story is not at the top: ${JSON.stringify(there)} (wanted ${target.id})`);
    const marked = new Set(marks);
    const grew = await scroller(page).evaluate((el, id) => {
      const above = el.querySelector(`[data-item-id="${CSS.escape(id)}"]`).previousElementSibling;
      const height = el.scrollHeight;
      above.style.height = `${Math.round(above.getBoundingClientRect().height / 2)}px`;
      above.style.overflow = "hidden";
      return el.scrollHeight - height;
    }, target.id);
    await page.waitForTimeout(1200);
    const kept = await topOf(page);
    check(grew === 0, `${tag} G4 height`, "the page's height did not change", `it changed by ${grew} px`);
    check(same(there, kept), `${tag} G4`, "a story getting shorter in a column shorter than the tallest keeps the story at the top where it was", `${JSON.stringify(there)} -> ${JSON.stringify(kept)}`);
    const moved = [...marks].filter((id) => !marked.has(id));
    check(moved.length === 0, `${tag} G4 marks`, "nothing the page moved was marked read", `marked ${moved.join(", ")}`);
  }
});

await t.finish("Gazette place");
