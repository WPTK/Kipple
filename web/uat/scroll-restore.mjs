// UAT: a reload (or the app reopened) comes back to the same place in the article list.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:scroll-restore         in another
//   npm run uat:scroll-restore -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// Uses the seed's articles (All needs about three screens of them). For each width (desktop 1280x800, phone 375x812)
// it checks:
//   P1  All, scrolled down, then reloaded: the same article is at the top of the list again;
//   P2  an article opened from there, the page reloaded on the article, then Back: the list is at that place;
//   P3  scrolled back to the top and reloaded: the list starts at the top;
//   P4  Unread with mark as read on scroll on, scrolled and reloaded: back at the place, and the restore itself marks
//       nothing read (the unread count is the same before and after).
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
/** Waits until `id` is the top article (the restore pages in and scrolls); returns the last top seen. */
const waitForTop = async (page, id, ms = 8000) => {
  const end = Date.now() + ms;
  let now = await topOf(page);
  while (now.id !== id && Date.now() < end) {
    await page.waitForTimeout(250);
    now = await topOf(page);
  }
  return now;
};
const ready = async (page) => {
  await scroller(page).locator("[data-item-id]").first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(300);
};
/** Scrolls the list down by about `px`, as a reader would (wheel), and lets the place be recorded. */
const scrollDown = async (page, px) => {
  const box = await scroller(page).boundingBox();
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  for (let done = 0; done < px; done += 400) {
    await page.mouse.wheel(0, 400);
    await page.waitForTimeout(60);
  }
  await page.waitForTimeout(1200);
};

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);

  let place = null;
  await step(`${tag} P1`, async () => {
    await page.goto("/l/all", { waitUntil: "load" });
    await ready(page);
    await scrollDown(page, 3000);
    place = await topOf(page);
    if (!place.id || place.scrollTop < 500) return t.fail(`${tag} P1 setup`, `the list did not scroll far enough: ${JSON.stringify(place)}`);
    await page.reload({ waitUntil: "load" });
    await ready(page);
    const back = await waitForTop(page, place.id);
    check(back.id === place.id, `${tag} P1`, "after a reload the same article is at the top", `top was ${place.id}, after the reload ${back.id} (scrolled ${back.scrollTop})`);
    await t.shot(page, `${tag}-reloaded`);
  });

  await step(`${tag} P2`, async () => {
    if (!place?.id) return t.fail(`${tag} P2 setup`, "no place from P1");
    await scroller(page).locator(`[data-item-id="${place.id}"] a.row-link`).first().click();
    await page.getByTestId("article-body").waitFor({ timeout: 15000 });
    await page.reload({ waitUntil: "load" });
    await page.getByTestId("article-body").waitFor({ timeout: 15000 });
    if (vp.mobile) await page.goBack({ waitUntil: "load" });
    else await page.goto("/l/all", { waitUntil: "load" });
    await ready(page);
    const back = await waitForTop(page, place.id);
    check(back.id === place.id, `${tag} P2`, "back from an article reloaded, the list is at its place", `top was ${place.id}, now ${back.id} (scrolled ${back.scrollTop})`);
  });

  await step(`${tag} P3`, async () => {
    await page.goto("/l/all", { waitUntil: "load" });
    await ready(page);
    await scroller(page).evaluate((el) => el.scrollTo(0, 0));
    await page.mouse.wheel(0, -400);
    await page.waitForTimeout(1200);
    await page.reload({ waitUntil: "load" });
    await ready(page);
    await page.waitForTimeout(1500);
    const now = await topOf(page);
    check(now.scrollTop === 0, `${tag} P3`, "a list left at its top starts at the top", `the list starts scrolled ${now.scrollTop} px`);
  });

  await step(`${tag} P4`, async () => {
    const on = await patch({ "ui.mark_read_on_scroll": true });
    if (on !== 200) return t.fail(`${tag} P4 setup`, `mark as read on scroll not turned on (${on})`);
    try {
      await page.goto("/l/unread", { waitUntil: "load" });
      await ready(page);
      await scrollDown(page, 2400);
      const here = await topOf(page);
      await page.waitForTimeout(1500); // the marks of the scroll itself land
      const before = (await t.boot(page)).counts.unread;
      await page.reload({ waitUntil: "load" });
      await ready(page);
      const back = await waitForTop(page, here.id);
      await page.waitForTimeout(2000);
      const after = (await t.boot(page)).counts.unread;
      check(back.id === here.id, `${tag} P4 place`, "Unread comes back to its place", `top was ${here.id}, now ${back.id}`);
      check(after === before, `${tag} P4 marks`, "the restore marks nothing read", `unread went from ${before} to ${after}`);
    } finally {
      await patch({ "ui.mark_read_on_scroll": null });
    }
  });
});

await t.finish("scroll restore");
