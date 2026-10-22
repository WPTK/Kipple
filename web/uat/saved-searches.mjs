// UAT: a saved-searches list that cannot be loaded says so, with a Try again button, instead of vanishing.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:saved-searches         in another
//   npm run uat:saved-searches -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it saves one search, blocks the list request in the browser and
// opens the Feeds list:
//   S1  the "Saved searches" section is there and says "Couldn't load your saved searches." with a Try again button;
//   S2  with the block lifted, Try again shows the saved search and the message is gone.
// Afterwards it deletes the search it made. It only runs against a loopback address with the seed's credentials unless
// --allow-remote is given. Exit code: 0 clean, 1 findings, 2 setup error.
// First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 13);
const { check, step, textOf } = t;
const NAME = "UAT list failure";

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const made = await page.request.post("/api/saved-searches", { headers: t.WRITE, data: { name: NAME, q: "uat" } });
  if (!made.ok()) return t.setupError(`${tag}: could not save a search (${made.status()})`);
  const id = (await made.json()).id;
  // The app's service worker answers from its own fetch, which a route cannot block: the page under test runs in a
  // second context, signed in with the same session, that does not register it.
  const ctx = await page.context().browser().newContext({
    baseURL: new URL(page.url()).origin,
    viewport: { width: vp.width, height: vp.height },
    isMobile: vp.mobile,
    hasTouch: vp.mobile,
    serviceWorkers: "block",
    storageState: await page.context().storageState(),
  });
  try {
    const view = await ctx.newPage();
    let blocked = true;
    await view.route(/\/api\/saved-searches\?counts=0/, (route) => (blocked ? route.fulfill({ status: 500, contentType: "application/json", body: '{"error":"boom"}' }) : route.continue()));
    await view.goto("/feeds", { waitUntil: "load" });
    // The wide layout shows the sidebar and the Feeds list together; either one is checked.
    const section = view.getByRole("group", { name: "Saved searches" }).first();
    await step(`${tag} S1`, async () => {
      await section.waitFor({ timeout: 15000 });
      const said = await section.getByText("Couldn't load your saved searches.").isVisible();
      check(said, `${tag} S1 message`, "the section says it could not load", `no message (section: ${await textOf(section, 300)})`);
      const retry = await section.getByRole("button", { name: /^Try again/ }).isVisible();
      check(retry, `${tag} S1 retry`, "Try again is offered", "no Try again button");
      await t.shot(view, `${tag}-S1`);
    });
    await step(`${tag} S2`, async () => {
      blocked = false;
      await section.getByRole("button", { name: /^Try again/ }).click();
      await section.getByRole("link", { name: new RegExp(NAME) }).waitFor({ timeout: 10000 });
      const gone = !(await section.getByText("Couldn't load your saved searches.").isVisible());
      check(gone, `${tag} S2 message`, "the message is gone after a successful retry", "the message is still there");
      await t.shot(view, `${tag}-S2`);
    });
  } finally {
    await ctx.close().catch(() => {});
    await page.request.delete(`/api/saved-searches/${id}`, { headers: t.WRITE }).catch(() => {});
  }
});

await t.finish("Saved searches list failure");
