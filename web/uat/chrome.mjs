// UAT: toasts stand above a screen's bottom bar, and a list picture that cannot be shown leaves no empty box.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:chrome                 in another
//   npm run uat:chrome -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and checks:
//   C1  on Feeds, "Folder created" followed at once by Select: the toast and the selection bar (Select all, Move to
//       folder, Delete) do not overlap, and the toast is still on screen;
//   G1  the flow serves its own feed whose lead picture is a BMP (the image proxy refuses it): in each of the Gazette,
//       Editorial, Cards and Inbox layouts that article's row has no picture left, and the Gazette keeps no empty box.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { Buffer } from "node:buffer";
import { start, startSelect } from "./common.mjs";

const t = await start(import.meta.url, 13);
const { check, step } = t;

const BMP = Buffer.from("Qk06AAAAAAAAADYAAAAoAAAAAQAAAAEAAAABABgAAAAAAAQAAAAAAAAAAAAAAAAAAAAAAAAA////AA==", "base64");
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
let base = "";
const server = createServer((req, res) => {
  const path = (req.url ?? "").split("?")[0];
  if (path === "/feed.xml") {
    const items = Array.from({ length: 8 }, (_, i) => {
      const title = i === 0 ? "UAT bmp lead" : `UAT chrome story ${i}`;
      const body = `<p>${"A paragraph of text for a story with a picture. ".repeat(6)}</p><p><img src="${base}/lead.bmp?n=${i}" alt="A lead"></p>`;
      return `<item><title>${title}</title><link>${base}/page/${i}</link><guid isPermaLink="false">uat-chrome-${i}</guid><pubDate>${new Date(Date.now() - i * 60000).toUTCString()}</pubDate><description>${esc(body)}</description></item>`;
    }).join("");
    res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(`<?xml version="1.0"?><rss version="2.0"><channel><title>UAT chrome</title><link>${base}/</link><description>t</description>${items}</channel></rss>`);
  } else if (path === "/lead.bmp") {
    res.writeHead(200, { "Content-Type": "image/bmp" }).end(BMP);
  } else {
    res.writeHead(404).end("not found");
  }
});
await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
base = `http://127.0.0.1:${server.address().port}`;

const overlap = (a, b) => a && b && a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const run = `${tag}-${Date.now()}`;
  await step(`${tag} C1`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Feed actions" }).click();
    await page.getByRole("menuitem", { name: "New folder" }).click();
    await page.getByLabel("Name").fill(`UAT toast folder ${run}`);
    await page.getByRole("button", { name: "Create" }).click();
    const toast = page.getByTestId("toast-region").getByText("Folder created");
    await toast.waitFor({ timeout: 10000 });
    await startSelect(page);
    const bar = page.getByRole("region", { name: "Selected feeds" });
    await bar.waitFor({ timeout: 5000 });
    await page.waitForTimeout(400);
    const toastBox = await toast.boundingBox();
    const hits = [];
    for (const name of ["Select all", "Move", "Delete"]) {
      if (overlap(toastBox, await bar.getByRole("button", { name }).boundingBox())) hits.push(name);
    }
    check(toastBox !== null && hits.length === 0, `${tag} C1 toast`, "the toast stands above the selection bar's buttons", `the toast ${JSON.stringify(toastBox)} covers ${hits.join(", ") || "nothing, but it is gone"}`);
    await t.shot(page, `${tag}-toast-above-bar`);
    await page.getByRole("button", { name: "Done" }).click();
  });

  await step(`${tag} G1`, async () => {
    const added = await t.addLocalFeed(page, `${base}/feed.xml`, "UAT feed chrome", (items) => items.length >= 8 && items.some((i) => i.title === "UAT bmp lead" && i.image));
    if (added.error) {
      const imgs = JSON.stringify((added.items ?? []).slice(0, 2).map((i) => i.image));
      return t.fail(`${tag} G1 setup`, `the feed's articles never arrived with a lead picture (images: ${imgs})`);
    }
    const ids = added.items.map((i) => String(i.id));
    await page.goto("/l/all", { waitUntil: "load" });
    const layoutBtn = page.locator('button[aria-label^="List options, "]').first();
    await layoutBtn.waitFor({ timeout: 15000 });
    for (const layout of ["Gazette", "Editorial", "Cards", "Inbox"]) {
      if (!(await layoutBtn.getAttribute("aria-label")).startsWith(`List options, ${layout} `)) {
        await layoutBtn.click();
        await page.getByRole("menuitemradio", { name: new RegExp(`^${layout}(?![A-Za-z])`) }).click();
        await page.keyboard.press("Escape");
      }
      const row = page.locator("[data-item-id]", { hasText: "UAT bmp lead" }).first();
      await row.waitFor({ timeout: 15000 });
      await row.scrollIntoViewIfNeeded();
      // A list picture is given its address only near the screen and three at a time, so one without an address is
      // not yet asked for (an img with no src reports complete with no width); only pictures that have one are judged.
      await page.waitForFunction((id) => [...document.querySelectorAll(`[data-item-id="${id}"] img`)].every((i) => i.closest("a") || i.hasAttribute("src")), await row.getAttribute("data-item-id"), { timeout: 10000 }).catch(() => {});
      // Give each picture time to be refused, then look for what is left of it.
      await page.waitForFunction(
        () => [...document.querySelectorAll("[data-item-id] img")].every((i) => i.closest("a") || !i.hasAttribute("src") || i.complete),
        undefined,
        { timeout: 10000 },
      ).catch(() => {});
      await page.waitForTimeout(1500);
      const left = await page.locator("[data-item-id]").evaluateAll((rows, ids) =>
        rows
          .filter((r) => ids.includes(r.getAttribute("data-item-id")))
          .flatMap((r) => [...r.querySelectorAll("img")].filter((i) => !i.src.includes("/icon") && i.hasAttribute("src") && i.complete).map((i) => ({ box: i.parentElement.getBoundingClientRect().height, shown: i.naturalWidth > 0 })))
          .filter((x) => !x.shown),
      ids);
      const boxes = await page.locator("[data-item-id]").evaluateAll(
        (rows, ids) =>
          rows
            .filter((r) => ids.includes(r.getAttribute("data-item-id")))
            .flatMap((r) => [...r.querySelectorAll('div[class*="aspect-"]')])
            .filter((el) => ![...el.querySelectorAll("img")].some((i) => i.naturalWidth > 0 || !i.complete || !i.hasAttribute("src"))).length,
        ids,
      );
      check(left.length === 0 && boxes === 0, `${tag} G1 ${layout}`, `${layout}: no picture or empty box is left for a picture that cannot be shown`, `${layout}: ${left.length} failed picture(s) and ${boxes} empty box(es) remain`);
      if (layout === "Gazette") await t.shot(page, `${tag}-gazette-no-box`);
    }
  });
});

server.close();
await t.finish("chrome");
