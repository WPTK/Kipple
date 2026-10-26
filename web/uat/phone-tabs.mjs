// UAT: the phone's first tab is named for what it holds (#421).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:phone-tabs             in another
//   npm run uat:phone-tabs -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// On the phone width of the run (the desktop pass has nothing to check) it checks:
//   A1  the first tab is named Articles, no tab is named Unread, and it opens the Unread list;
//   A2  Articles is the current tab on Unread, All, Starred and Muted, and not on a feed's list (that is Feeds);
//   A3  the pills under the title are New, Unread, All and Starred, tapping Starred keeps Articles current, and tapping
//       Articles from another tab opens Unread;
//   A4  the Feeds screen lists Feed index and Feed health (each opens its screen) and repeats none of the whole-library lists;
//   A5  at 812x375 and at 320x568 with text size Largest the five tabs fit with their names whole.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start, waitForStillFor } from "./common.mjs";

const t = await start(import.meta.url, 15);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  if (!vp.mobile) return;
  const tag = vp.id;
  const feed = (await t.boot(page)).feeds[0];
  if (!feed) return t.fail(`${tag} setup`, "the seeded instance has no feed");
  const open = async (path) => {
    await page.goto(path, { waitUntil: "load" });
    await page.locator("main h1, main h2").first().waitFor({ timeout: 15000 });
    await tabsStill();
  };
  const bar = () => page.locator('nav[aria-label="Primary"]').last();
  const tabNames = () => bar().locator("a").evaluateAll((as) => as.map((a) => a.innerText.replace(/\s+/g, " ").replace(/^\d+\+?\s*/, "").trim()));
  const current = () => bar().locator('a[aria-current="page"]').evaluateAll((as) => as.map((a) => a.innerText.replace(/\s+/g, " ").replace(/^\d+\+?\s*/, "").trim()));

  // The tab bar's names and current tab have not changed for 300 ms: the bar has rendered for the route.
  const tabsStill = () => waitForStillFor(page, 300, async () => [await tabNames(), await current()], { label: "the tab bar to settle" });

  await step(`${tag} A1`, async () => {
    await page.setViewportSize({ width: 375, height: 812 });
    await open("/feeds");
    const names = await tabNames();
    check(names[0] === "Articles", `${tag} A1 name`, `the first tab is ${names[0]}`, `the first tab is ${names[0]}, not Articles`);
    check(!names.includes("Unread"), `${tag} A1 no Unread`, "no tab is named Unread", "a tab is named Unread");
    await bar().getByRole("link", { name: /Articles/ }).click();
    await page.waitForURL((u) => u.pathname === "/l/unread", { timeout: 5000 }).catch(() => {});
    check(new URL(page.url()).pathname === "/l/unread", `${tag} A1 opens`, "Articles opens the Unread list", `Articles opened ${new URL(page.url()).pathname}`);
    await t.shot(page, `${tag}-articles-tab`);
  });

  await step(`${tag} A2`, async () => {
    for (const [path, want] of [["/l/unread", ["Articles"]], ["/l/all", ["Articles"]], ["/l/starred", ["Articles"]], ["/l/muted", ["Articles"]], [`/l/unread?feed=${feed.id}`, ["Feeds"]]]) {
      await open(path);
      const got = await current();
      check(JSON.stringify(got) === JSON.stringify(want), `${tag} A2 ${path.split("?")[0]}${path.includes("feed") ? " (feed)" : ""}`, `${want[0]} is the current tab`, `the current tab is ${got.join(", ") || "none"}`);
    }
  });

  await step(`${tag} A3`, async () => {
    await open("/l/unread");
    const pills = await page.getByRole("navigation", { name: "Show" }).getByRole("link").allInnerTexts();
    check(JSON.stringify(pills.slice(0, 4)) === JSON.stringify(["New", "Unread", "All", "Starred"]), `${tag} A3 pills`, `the pills start ${pills.slice(0, 4).join(", ")}`, `the pills are ${pills.join(", ")}`);
    await page.getByRole("navigation", { name: "Show" }).getByRole("link", { name: "Starred" }).click();
    await page.waitForURL((u) => u.pathname === "/l/starred", { timeout: 5000 }).catch(() => {});
    await tabsStill();
    const got = await current();
    check(got[0] === "Articles", `${tag} A3 Starred`, "Articles stays current on Starred", `the current tab on Starred is ${got.join(", ") || "none"}`);
    await t.shot(page, `${tag}-starred-pill`);
  });

  await step(`${tag} A4`, async () => {
    await open("/feeds");
    for (const [name, to] of [["Feed index", "/feeds/index"], ["Feed health", "/health"]]) {
      const link = page.getByRole("main").getByRole("link", { name, exact: true }).first();
      check(await link.isVisible().catch(() => false), `${tag} A4 ${name} listed`, `Feeds lists ${name}`, `Feeds does not list ${name}`);
      await link.click().catch(() => {});
      await page.waitForURL((u) => u.pathname === to, { timeout: 5000 }).catch(() => {});
      check(new URL(page.url()).pathname === to, `${tag} A4 ${name} opens`, `${name} opens ${to}`, `${name} opened ${new URL(page.url()).pathname}`);
      await open("/feeds");
    }
    for (const name of ["Unread", "All articles", "Starred", "Muted"]) {
      check((await page.getByRole("main").getByRole("link", { name: new RegExp(`^${name}`) }).count()) === 0, `${tag} A4 no ${name}`, `Feeds does not repeat ${name}`, `Feeds lists ${name}`);
    }
    await t.shot(page, `${tag}-feeds-lists`);
  });

  await step(`${tag} A5`, async () => {
    const fits = async (where) => {
      const m = await bar().locator("a").evaluateAll((as) => as.map((a) => ({ name: a.innerText.replace(/\s+/g, " ").trim(), over: a.scrollWidth > a.clientWidth + 1 })));
      const cut = m.filter((x) => x.over).map((x) => x.name);
      check(cut.length === 0, `${tag} A5 ${where}`, "all tab names are whole", `cut off: ${cut.join(", ")}`);
    };
    await page.setViewportSize({ width: 812, height: 375 });
    await open("/l/unread");
    await fits("812x375");
    await t.shot(page, `${tag}-landscape-tabs`);
    const status = await page.evaluate((b) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) }).then((r) => r.status), { "client.text_size": 1.5 });
    if (status !== 200) return t.fail(`${tag} A5 setup`, `text size not set (${status})`);
    try {
      await page.setViewportSize({ width: 320, height: 568 });
      await open("/l/unread");
      await fits("320x568 Largest");
      await t.shot(page, `${tag}-320-largest-tabs`);
    } finally {
      await page.evaluate((b) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) }), { "client.text_size": 1 });
    }
  });
});

await t.finish("phone-tabs");
