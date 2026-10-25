// UAT: the end of an article (#650) and the Previous and Next arrows (#653).
//
//   npm run seed                  in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/article-end.mjs      in another, once the feeds have fetched
//   node uat/article-end.mjs --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it opens one feed's list in All and checks:
//   E1  the first article ends with "Article 1 of N" and a "Next: <title>" button that opens the second article;
//   E2  the feed's last article ends with "End of articles", has no Next button there, and the toolbar's Next is off;
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

    await page.goto(`/l/all?feed=${feed}`, { waitUntil: "load" });
    await page.locator(`[data-item-id="${ids[0]}"] a`).first().click();
    await page.getByTestId("article-body").waitFor({ timeout: 15000 });
    const from = new URL(page.url()).searchParams.get("from");

    const end = page.getByTestId("article-end");
    await end.scrollIntoViewIfNeeded();
    const text = (await end.innerText()).replace(/\s+/g, " ");
    const nextBtn = end.getByRole("button", { name: /^Next: / });
    const named = (await nextBtn.count()) === 1;
    check(text.includes(`Article 1 of ${ids.length}`) && named, `${tag} E1 footer`, `"Article 1 of ${ids.length}" and a "Next: <title>" button`, text);
    await t.shot(page, `${tag}-article-end-first`);
    if (named) {
      await nextBtn.click();
      const moved = await page.waitForURL((u) => u.pathname === `/i/${ids[1]}`, { timeout: 15000 }).then(() => true, () => false);
      check(moved, `${tag} E1 next`, "the footer's Next opens the second article", `at ${new URL(page.url()).pathname}`);
    }

    await page.goto(`/i/${ids.at(-1)}?${new URLSearchParams({ from })}`, { waitUntil: "load" });
    // The footer, not the body: a feed's article can be title-only, which shows an empty state instead of a body.
    await end.waitFor({ timeout: 15000 });
    await end.scrollIntoViewIfNeeded();
    const last = (await end.innerText()).replace(/\s+/g, " ");
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
