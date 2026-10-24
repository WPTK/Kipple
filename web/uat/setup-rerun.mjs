// UAT: "Run setup again" opens the steps in this browser only, and the time zone list finds a zone by its current name.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:setup-rerun            in another
//   npm run uat:setup-rerun -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   R1  Settings, Account & Devices, Run setup again opens the time zone step, the server still says setup is done, and a
//       reload in the middle of the steps resumes them;
//   R2  a second browser (another device) that signs in while the steps are open lands in the app, not in the steps;
//   R3  searching the time zone list for "kolkata" and "kyiv" finds those zones, whichever spelling this browser lists;
//   R4  "Skip the rest of setup" ends in the reader, and a reload stays there.
// It changes no setting. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { URL } from "node:url";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step, opt, origin } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const pending = async (p) => (await p.request.get("/api/auth/me", { headers: { Accept: "application/json" } }).then((r) => r.json())).setup_pending === true;

  await step(`${tag} R1`, async () => {
    await page.goto("/settings/account", { waitUntil: "load" });
    await page.getByRole("button", { name: "Run setup again" }).click();
    await page.getByRole("heading", { name: "Choose your time zone" }).waitFor({ timeout: 10000 });
    check(new URL(page.url()).pathname === "/welcome/timezone", `${tag} R1 path`, "the time zone step is open", `the page is at ${page.url()}`);
    check(!(await pending(page)), `${tag} R1 server`, "the server still says setup is done", "the server now says setup is pending");
    await t.shot(page, `${tag}-setup-rerun-open`);
    await page.reload({ waitUntil: "load" });
    const back = await page.getByRole("heading", { name: "Choose your time zone" }).waitFor({ timeout: 10000 }).then(() => true, () => false);
    check(back, `${tag} R1 reload`, "a reload in the middle of the steps resumes them", `after a reload the page is at ${page.url()}`);
  });

  await step(`${tag} R2`, async () => {
    const other = await page.context().browser().newContext({ baseURL: origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });
    try {
      const p = await other.newPage();
      await p.goto("/", { waitUntil: "load" });
      await p.getByLabel("Username").fill(opt.user);
      await p.getByLabel("Password", { exact: true }).fill(opt.password);
      await p.getByRole("button", { name: "Sign in" }).click();
      await p.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
      check(!new URL(p.url()).pathname.startsWith("/welcome"), `${tag} R2`, "the other device lands in the app", `the other device was sent to ${p.url()}`);
      await t.shot(p, `${tag}-setup-rerun-other-device`);
    } finally {
      await other.close();
    }
  });

  await step(`${tag} R3`, async () => {
    const search = page.getByRole("searchbox", { name: "Search time zones" }).or(page.getByLabel("Search time zones"));
    const list = page.getByRole("listbox", { name: "Time zones" });
    for (const [word, found] of [
      ["kolkata", /Kolkata|Calcutta/],
      ["kyiv", /Kyiv|Kiev/],
      ["ho chi minh", /Ho Chi Minh|Saigon/],
    ]) {
      await search.first().fill(word);
      const hit = await list.getByRole("option", { name: found }).first().waitFor({ state: "attached", timeout: 5000 }).then(() => true, () => false);
      check(hit, `${tag} R3 ${word}`, `"${word}" finds a zone`, `no zone for "${word}" in the list`);
    }
    await t.shot(page, `${tag}-setup-rerun-search`);
  });

  await step(`${tag} R4`, async () => {
    await page.getByRole("button", { name: "Skip the rest of setup" }).click();
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
    check(!new URL(page.url()).pathname.startsWith("/welcome"), `${tag} R4`, "the app is open", `the page is at ${page.url()}`);
    await page.reload({ waitUntil: "load" });
    await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
    check(!new URL(page.url()).pathname.startsWith("/welcome"), `${tag} R4 reload`, "a reload stays in the app", `the reload went to ${page.url()}`);
  });
});

await t.finish("setup rerun");
