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
//       Each layout also proves the stub served 1,000 articles and that rows were drawn, so the budget cannot pass on an
//       empty page (N3), and the check itself is shown to fail on a budget of 1 node (N4);
//   N2  the Gazette prints every loaded article, so it is held to its own ceiling of 20 nodes per article (it measures
//       about 15), which still catches a page growing heavier.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start, waitForPageStill } from "./common.mjs";

const t = await start(import.meta.url, 16, { serviceWorkers: "block" });
const { check, step } = t;

const COUNT = 1000;
const BUDGET = 1400;
const GAZETTE_PER_ARTICLE = 20;
/** Why a measurement does not meet its budget, or null when it does. The one place the rule lives, so N4 can try it with a limit it must fail. */
const verdict = (m, limit) => {
  if (m.served !== COUNT) return `the stub served ${m.served} articles in ${m.requests} request(s), not ${COUNT}`;
  if (m.rows <= 0) return "no article was drawn";
  return m.nodes <= limit ? null : `${m.nodes} nodes for ${m.rows} articles, over ${limit}`;
};
const LAYOUTS = ["magazine", "cards", "compact", "inbox", "headlines", "gazette", "digest"];

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
  /** Answers the list request with COUNT articles: the real first page, repeated, with an id of its own on each copy. */
  const seen = { requests: 0, served: 0 };
  const stub = async (route) => {
    const real = await (await route.fetch()).json();
    const base = real.items ?? [];
    seen.requests++;
    if (base.length === 0) return route.fulfill({ json: real });
    const items = Array.from({ length: COUNT }, (_, i) => ({ ...base[i % base.length], id: String(90_000_000 - i) }));
    seen.served = Math.max(seen.served, items.length);
    return route.fulfill({ json: { ...real, items, next_cursor: null } });
  };
  const isList = (u) => u.pathname === "/api/items";
  await page.route(isList, stub);

  await step(`${tag} N`, async () => {
    try {
      for (const layout of LAYOUTS) {
        if ((await patch({ "client.layout": layout })) !== 200) throw new Error(`the layout was not set (${layout})`);
        seen.requests = 0;
        seen.served = 0;
        await page.goto("/l/unread", { waitUntil: "load" });
        const first = page.locator("article").first();
        await first.waitFor({ timeout: 20000 });
        await waitForPageStill(page, 400);
        const m = { ...(await page.evaluate(() => ({ nodes: document.querySelectorAll("*").length, rows: document.querySelectorAll("article").length }))), ...seen };
        if (layout === "gazette") {
          const limit = COUNT * GAZETTE_PER_ARTICLE;
          const why = verdict(m, limit);
          check(why === null && m.rows === COUNT, `${tag} N2 gazette`, `the Gazette draws all ${COUNT} articles in at most ${limit} nodes`, why ?? `${m.rows} articles drawn, not ${COUNT}`);
        } else {
          const why = verdict(m, BUDGET);
          check(why === null, `${tag} N1 ${layout}`, `${layout} holds the 1,000-article list in at most ${BUDGET} nodes`, why);
        }
        if (layout === "magazine") {
          // N4: the rule can fail. The same measurement against a budget of 1 node, and against an unserved stub, must be refused.
          check(verdict(m, 1) !== null && verdict({ ...m, served: 0 }, BUDGET) !== null && verdict({ ...m, rows: 0 }, BUDGET) !== null, `${tag} N4 can fail`, "the budget refuses 1 node, an unserved stub and an empty page", "the budget accepted a measurement it must refuse");
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
