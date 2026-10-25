// UAT: Next on the last loaded article when the next page of the list cannot be loaded.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/next-page-failure.mjs     in another, once the feeds have fetched
//   node uat/next-page-failure.mjs --url http://127.0.0.1:7091 --screenshots <dir> --headed
//
// For each width (desktop 1280x800, phone 375x812) it opens the last article of the first page of the library, makes every
// later page of the list fail (as a dropped connection does) and checks:
//   N1  Next says the articles could not be loaded, not "End of list", and stays on the article;
//   N2  once the pages load again, Next goes on to the first article of the next page.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 10, { serviceWorkers: "block" }); // the service worker would answer what page.route fails
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  await step(`${tag} N`, async () => {
    // The list asks for 50 articles at a time; the seed holds more than that.
    const first = await (await page.request.get("/api/items?view=all&limit=50", { headers: { Accept: "application/json" } })).json();
    if (!first.next_cursor) return t.fail(`${tag} N setup`, "the library holds no second page of articles");
    const last = first.items.at(-1).id;
    const laterPages = (u) => u.pathname === "/api/items" && u.searchParams.has("cursor");
    await page.route(laterPages, (route) => route.abort("connectionfailed"));

    await page.goto(`/i/${last}?from=all`, { waitUntil: "load" });
    const bar = page.getByRole("toolbar", { name: "Article actions" });
    await bar.waitFor({ timeout: 15000 });
    await page.getByTestId("article-body").waitFor({ timeout: 15000 });
    await bar.getByRole("button", { name: "Next article" }).click();
    const said = await page
      .getByText(/Couldn.t load more articles/)
      .first()
      .waitFor({ timeout: 10000 })
      .then(() => true, () => false);
    const ended = await page.getByText("End of list").count();
    const where = new URL(page.url()).pathname;
    check(said && ended === 0 && where === `/i/${last}`, `${tag} N1 failure`, 'Next says the articles could not be loaded, not "End of list", and stays on the article', `${said ? "said so" : "no such message"}, ${ended} "End of list", at ${where}`);
    await t.shot(page, `${tag}-next-page-failure`);

    await page.unroute(laterPages);
    await bar.getByRole("button", { name: "Next article" }).click();
    const moved = await page.waitForURL((u) => /^\/i\/\d+$/.test(u.pathname) && u.pathname !== `/i/${last}`, { timeout: 15000 }).then(() => true, () => false);
    check(moved, `${tag} N2 retry`, "once the pages load again, Next goes on to the next article", `still at ${new URL(page.url()).pathname}`);
  });
});

await t.finish("Next page failure");
