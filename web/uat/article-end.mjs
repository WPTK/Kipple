// UAT: the end of an article (#650) and the Previous and Next arrows (#653).
//
//   npm run seed                  in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/article-end.mjs      in another, once the feeds have fetched
//   node uat/article-end.mjs --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it opens one feed's list in All and checks:
//   E1  the first row's article ends with "Article k of N" and a "Next: <title>" button that opens article k+1;
//   E2  the article at N of N ends with "End of articles", has no Next button there, and the toolbar's Next is off;
//   E3  Previous and Next are drawn as up and down arrows, and on the phone the top bar's button is named Back.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 12);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} E`, async () => {
    const get = async (q) => (await page.request.get(`/api/items?${q}`, { headers: { Accept: "application/json" } })).json();
    // A feed whose whole list fits one page, so its last article is the end of the list.
    const all = await get("view=all&limit=50");
    let feed;
    let ids = [];
    for (const f of [...new Set(all.items.map((i) => i.feed_id))]) {
      const r = await get(`view=all&feed=${f}&limit=50`);
      if (!r.next_cursor && r.items.length >= 3) {
        feed = f;
        ids = r.items.map((i) => i.id);
        break;
      }
    }
    if (!feed) return t.fail(`${tag} E setup`, "no seeded feed with 3 to 50 articles");

    // The reading order is the layout's (a Gazette or Digest differs from the API's newest first), so positions are read
    // from the footer, never assumed from the API order.
    const end = page.getByTestId("article-end");
    // The footer, not the body: a feed's article can be title-only, which shows an empty state instead of a body. Waited
    // for, scrolled to and read in one step in the page: the previous article's footer can still be attached for a
    // moment after Next. `want` waits for the footer that shows that text.
    const footer = async (want = "Article ") => {
      const h = await page.waitForFunction(
        (w) => {
          const el = document.querySelector('[data-testid="article-end"]');
          if (!el?.textContent?.includes(w)) return null;
          el.scrollIntoView({ block: "end" });
          return el.textContent;
        },
        want,
        { timeout: 15000 },
      );
      return String(await h.jsonValue()).replace(/\s+/g, " ");
    };
    const posOf = (text) => Number(/Article (\d+) of /.exec(text)?.[1] ?? NaN);

    await page.goto(`/l/all?feed=${feed}`, { waitUntil: "load" });
    await page.locator("[data-item-id] a").first().click();
    const from = new URL(page.url()).searchParams.get("from");
    if (!from) return t.fail(`${tag} E setup`, "the article opened from the list carries no from=");
    const text = await footer();
    const nextBtn = end.getByRole("button", { name: /^Next: / });
    const named = (await nextBtn.count()) === 1;
    const at = posOf(text);
    check(text.includes(`of ${ids.length}`) && at >= 1 && named, `${tag} E1 footer`, `"Article k of ${ids.length}" and a "Next: <title>" button`, text);
    await t.shot(page, `${tag}-article-end-first`);
    if (named) {
      const was = new URL(page.url()).pathname;
      await nextBtn.click();
      await page.waitForURL((u) => u.pathname !== was, { timeout: 15000 }).catch(() => {});
      // Beside the list focus stays in the pane, on the toolbar's Next; on a phone the new article's title takes it.
      const after = posOf(await footer(`Article ${at + 1} of`).catch(() => "")); // loaded: on a phone its title has taken focus by now
      const focused = await page.evaluate(() => document.activeElement?.getAttribute("aria-label") || document.activeElement?.id || "");
      const focusOk = tag === "desktop" ? focused === "Next article" : focused === "article-title";
      check(after === at + 1 && focusOk, `${tag} E1 next`, `the footer's Next opens article ${at + 1} and focus lands on ${tag === "desktop" ? "the toolbar's Next" : "its title"}`, `now at article ${after}, focus on ${focused || "body"}`);
    }

    // The end of the list in its reading order: the article whose footer says "N of N".
    let last = "";
    for (const id of [...ids].reverse()) {
      await page.goto(`/i/${id}?${new URLSearchParams({ from })}`, { waitUntil: "load" });
      last = await footer();
      if (last.includes(`Article ${ids.length} of ${ids.length}`)) break;
    }
    const off = await page.getByRole("toolbar", { name: "Article actions" }).getByRole("button", { name: "Next article" }).getAttribute("aria-disabled");
    check(last.includes("End of articles") && (await end.getByRole("button").count()) === 0 && off === "true", `${tag} E2 end`, '"End of articles", no footer button, toolbar Next off', `${last}; Next aria-disabled=${off}`);
    await t.shot(page, `${tag}-article-end-last`);

    const bar = page.getByRole("toolbar", { name: "Article actions" });
    const arrows = await bar.evaluate((el) =>
      ["Previous article", "Next article"].map((n) => el.querySelector(`button[aria-label="${n}"] svg`)?.getAttribute("class") ?? ""),
    );
    const upDown = arrows[0].includes("chevron-up") && arrows[1].includes("chevron-down");
    const back = vp.id.startsWith("phone") ? (await page.getByRole("button", { name: "Back", exact: true }).count()) === 1 : true;
    check(upDown && back, `${tag} E3 arrows`, "up and down arrows; the phone's Back is named Back", `${arrows.join(" | ")}; Back ok=${back}`);
  });
});

await t.finish("Article end");
