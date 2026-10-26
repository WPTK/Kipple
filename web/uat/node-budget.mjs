// UAT: a long list stays cheap to draw. A 1,000-article list holds the page under 1,400 DOM nodes in every layout.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/node-budget.mjs           in another, once the feeds have fetched
//   node uat/node-budget.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) and each layout, the page's own request for the Unread list is
// answered with 1,000 articles (copies of the first real page, each with its own id; a client-side stand-in, nothing is
// written to the server), and the page's elements are counted once the list has drawn.
//   N1  the row layouts and the Digest stay at 1,400 nodes or fewer (the limit web.dev flags as an excessive DOM): they
//       draw the rows on screen and not the whole list;
//   N2  the Gazette prints every loaded article, so it is held to its own ceiling of 20 nodes per article (it measures
//       about 15), which still catches a page growing heavier.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start, waitForPageStill } from "./common.mjs";

const t = await start(import.meta.url, 14, { serviceWorkers: "block" });
const { check, step } = t;

const COUNT = 1000;
const BUDGET = 1400;
const GAZETTE_PER_ARTICLE = 20;
const LAYOUTS = ["magazine", "cards", "compact", "inbox", "headlines", "gazette", "digest"];

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
  /** Answers the list request with COUNT articles: the real first page, repeated, with an id of its own on each copy. */
  const stub = async (route) => {
    const real = await (await route.fetch()).json();
    const base = real.items ?? [];
    if (base.length === 0) return route.fulfill({ json: real });
    const items = Array.from({ length: COUNT }, (_, i) => ({ ...base[i % base.length], id: String(90_000_000 - i) }));
    return route.fulfill({ json: { ...real, items, next_cursor: null } });
  };
  const isList = (u) => u.pathname === "/api/items";
  await page.route(isList, stub);

  await step(`${tag} N`, async () => {
    try {
      for (const layout of LAYOUTS) {
        if ((await patch({ "client.layout": layout })) !== 200) throw new Error(`the layout was not set (${layout})`);
        await page.goto("/l/unread", { waitUntil: "load" });
        const first = page.locator("article").first();
        await first.waitFor({ timeout: 20000 });
        await waitForPageStill(page, 400);
        const m = await page.evaluate(() => ({ nodes: document.querySelectorAll("*").length, rows: document.querySelectorAll("article").length }));
        if (layout === "gazette") {
          const limit = COUNT * GAZETTE_PER_ARTICLE;
          check(m.nodes <= limit, `${tag} N2 gazette`, `the Gazette holds ${COUNT} articles in at most ${limit} nodes`, `${m.nodes} nodes for ${m.rows} articles`);
        } else {
          check(m.nodes <= BUDGET, `${tag} N1 ${layout}`, `${layout} holds the 1,000-article list in at most ${BUDGET} nodes`, `${m.nodes} nodes, ${m.rows} rows`);
        }
        console.log(`  ${tag} ${layout}: ${m.nodes} nodes, ${m.rows} articles drawn`);
      }
    } finally {
      await page.unroute(isList, stub).catch(() => {});
      await patch({ "client.layout": null });
    }
  });
});

await t.finish("node budget");
