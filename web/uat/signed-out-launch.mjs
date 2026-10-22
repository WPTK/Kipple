// UAT: a launch without the session marker settles the sign-in in one question, and neither it nor a sign-out sends
// anything that answers 401.
//
//   npm run seed                            in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:signed-out-launch           in another
//   npm run uat:signed-out-launch -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   L1  a browser with no cookies opens Kipple: the sign-in form shows after exactly one GET /api/instance, with no
//       request for the bootstrap and no answer of 401 (the browser logs those to the console);
//   L2  a signed-in browser that has lost only the marker opens Kipple: the reader shows after one GET /api/instance,
//       with no 401, and the server sets the marker again;
//   L3  a browser that signs in and then chooses Sign out (Settings, Account & Devices) reaches the sign-in form with no
//       answer of 401 after the click.
// It changes nothing but the session it makes for L3. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check, step, opt, origin } = t;
// The session marker's name: __Host- prefixed over https (web/src/api/sessionProbe.ts).
const MARKER = new URL(origin).protocol === "https:" ? "__Host-kipple_signed_in" : "kipple_signed_in";

/** Records the /api answers a page gets: the path of each, and those that answered 401. */
function watch(page) {
  const seen = { asked: [], unauthorized: [] };
  page.on("response", (r) => {
    const u = new URL(r.url());
    if (!u.pathname.startsWith("/api/")) return;
    seen.asked.push(u.pathname);
    if (r.status() === 401) seen.unauthorized.push(`${r.request().method()} ${u.pathname}`);
  });
  return seen;
}
const count = (seen, path) => seen.asked.filter((p) => p === path).length;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const newContext = () => page.context().browser().newContext({ baseURL: origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });

  await step(`${tag} L1`, async () => {
    const ctx = await newContext();
    try {
      const p = await ctx.newPage();
      const seen = watch(p);
      await p.goto("/", { waitUntil: "load" });
      await p.getByRole("button", { name: "Sign in" }).waitFor({ timeout: 15000 });
      await p.waitForLoadState("networkidle");
      check(count(seen, "/api/instance") === 1, `${tag} L1 instance`, "one GET /api/instance before the sign-in form", `GET /api/instance was asked ${count(seen, "/api/instance")} times`);
      check(count(seen, "/api/bootstrap") === 0, `${tag} L1 bootstrap`, "no request for the bootstrap", "the bootstrap was asked for");
      check(seen.unauthorized.length === 0, `${tag} L1 401`, "no answer of 401", `answered 401: ${seen.unauthorized.join(", ")}`);
      await t.shot(p, `${tag}-signed-out-launch`);
    } finally {
      await ctx.close();
    }
  });

  await step(`${tag} L2`, async () => {
    const ctx = await newContext();
    try {
      const session = (await page.context().cookies(origin)).filter((c) => c.name !== MARKER);
      await ctx.addCookies(session);
      const p = await ctx.newPage();
      const seen = watch(p);
      await p.goto("/", { waitUntil: "load" });
      await p.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
      check(count(seen, "/api/instance") === 1, `${tag} L2 instance`, "one GET /api/instance, then the reader", `GET /api/instance was asked ${count(seen, "/api/instance")} times`);
      check(seen.unauthorized.length === 0, `${tag} L2 401`, "no answer of 401", `answered 401: ${seen.unauthorized.join(", ")}`);
      const marked = (await ctx.cookies(origin)).some((c) => c.name === MARKER);
      check(marked, `${tag} L2 marker`, "the server set the marker again", "no marker after the launch");
    } finally {
      await ctx.close();
    }
  });

  await step(`${tag} L3`, async () => {
    const ctx = await newContext();
    try {
      const p = await ctx.newPage();
      await p.goto("/", { waitUntil: "load" });
      await p.getByLabel("Username").fill(opt.user);
      await p.getByLabel("Password", { exact: true }).fill(opt.password);
      await p.getByRole("button", { name: "Sign in" }).click();
      await p.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
      await p.goto("/settings/account", { waitUntil: "load" });
      const out = p.getByRole("button", { name: "Sign out" });
      await out.scrollIntoViewIfNeeded({ timeout: 15000 });
      const seen = watch(p);
      await out.click();
      await p.getByRole("button", { name: "Sign in" }).waitFor({ timeout: 15000 });
      await p.waitForLoadState("networkidle");
      check(seen.unauthorized.length === 0, `${tag} L3 401`, "no answer of 401 after Sign out", `answered 401: ${seen.unauthorized.join(", ")}`);
      check(count(seen, "/api/bootstrap") === 0, `${tag} L3 bootstrap`, "the bootstrap was not asked for again", `the bootstrap was asked for ${count(seen, "/api/bootstrap")} times`);
      await t.shot(p, `${tag}-signed-out-after-sign-out`);
    } finally {
      await ctx.close();
    }
  });
});

await t.finish("signed-out launch");
