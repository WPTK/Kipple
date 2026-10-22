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
//       top, where it was.
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
  await step(`${tag} G`, async () => {
    const on = await patch({ "client.layout": "gazette" });
    if (on !== 200) return t.fail(`${tag} G setup`, `the Gazette not set (${on})`);
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
      await page.waitForTimeout(800);
      const after = await topOf(page);
      check(same(back, after), `${tag} G3`, "a story above the screen getting shorter keeps the story at the top where it was", `${JSON.stringify(back)} -> ${JSON.stringify(after)}`);
    } finally {
      await page.evaluate(() => (document.documentElement.style.fontSize = ""));
      await patch({ "client.layout": null });
    }
  });
});

await t.finish("Gazette place");
