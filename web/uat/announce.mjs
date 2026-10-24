// UAT: what a screen reader is told, and what a slow tap shows (issues #482, #491, #497, #498, #499, #500).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:announce               in another, once the feeds have fetched
//   npm run uat:announce -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and:
//   A1  Taps Refresh all feeds (on a phone, the item in the actions menu) with the request held back 3 s: the icon spins at once (on a phone, the actions menu button spins) and the button says Refreshing.
//   A2  Settings > Appearance & Reading: no switch's name carries its visible On or Off.
//   A3  Signs in with a wrong password in a second, signed-out browser: both fields are aria-invalid and described by
//       the error, and the password field has focus.
//   A4  Manage feeds > Add feed with "not a url !!": the address field is aria-invalid, described by the error, focused.
//   A5  Opens an article, marks it unread from the toolbar, then Next: the "Marked unread" toast is gone.
//   A6  Settings > Account & Devices: the Cloudflare Access state is not a live region.
//   A7  The list: a row's Star and More actions name its article, its link starts with the title, and the Unread
//       link in the sidebar (desktop) reads "Unread N", the word once.
//   A8  (desktop) Cocoa Mid: a selected row's secondary text is at least 4.5:1 on the selection; the theme is put back.
// It opens articles on the seed's account and marks the ones that were unread unread again afterwards. It only runs
// against a loopback address with the seed's credentials unless --allow-remote is given. Exit code: 0 clean,
// 1 findings, 2 setup error. First time on a machine: `npx playwright install chromium` (webkit, firefox for --browser).
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;
const JSON_ACCEPT = { Accept: "application/json" };

const unreadIds = async (page) =>
  ((await (await page.request.get("/api/items", { params: { view: "unread", limit: 500 }, headers: JSON_ACCEPT })).json()).items ?? []).map((i) => i.id);
const described = (page, sel) =>
  page.evaluate((s) => {
    const el = document.querySelector(s);
    if (!el) return null;
    const ids = (el.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
    return {
      invalid: el.getAttribute("aria-invalid"),
      text: ids.map((id) => document.getElementById(id)?.textContent?.trim() ?? "").join(" "),
      focused: el === document.activeElement,
    };
  }, sel);
const luminance = (rgb) => {
  const [r, g, b] = rgb.match(/\d+(\.\d+)?/g).slice(0, 3).map(Number).map((v) => v / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const ratio = (a, b) => {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
};

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const wasUnread = await unreadIds(page);
  try {
    await step(`${tag} A1`, async () => {
      // Its own browser with the service worker blocked: WebKit's request routing does not see what the worker sends.
      const ctx = await page.context().browser().newContext({ baseURL: t.origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile, serviceWorkers: "block" });
      try {
        const p = await signIn(ctx);
        await p.route("**/api/refresh", async (route) => {
          await new Promise((r) => setTimeout(r, 3000));
          await route.continue().catch(() => {});
        });
        await p.goto("/l/all", { waitUntil: "load" });
        await p.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
        const answered = p.waitForResponse("**/api/refresh", { timeout: 10000 }).catch(() => null);
        // Wide: the header button. A phone has no such button: the actions menu has the item, and its trigger spins.
        if (vp.mobile) {
          await p.getByRole("button", { name: "List actions" }).click();
          await p.getByRole("menuitem", { name: "Refresh all feeds" }).click();
        } else await p.getByRole("button", { name: "Refresh all feeds" }).first().click();
        await p.waitForTimeout(300);
        const state = await p.evaluate((mobile) => {
          const b = document.querySelector(mobile ? 'button[aria-label="List actions"]' : 'button[aria-label="Refreshing"]');
          return { label: mobile ? true : !!b, spin: !!b?.querySelector("svg.animate-spin") };
        }, vp.mobile);
        check(state.label && state.spin, `${tag} A1`, vp.mobile ? "the actions menu button spins 0.3 s after the tap" : "the button spins and says Refreshing 0.3 s after the tap", `0.3 s after the tap: ${JSON.stringify(state)}`);
        await t.shot(p, `${tag}-A1`);
        await answered;
      } finally {
        await ctx.close();
      }
    });
    await step(`${tag} A2`, async () => {
      await page.goto("/settings/appearance", { waitUntil: "load" });
      await page.getByRole("switch").first().waitFor({ timeout: 15000 });
      const names = await page.locator("main").ariaSnapshot();
      const glued = names.split("\n").filter((l) => /switch ".*(On|Off)"/.test(l));
      check(glued.length === 0, `${tag} A2`, "no switch name carries On or Off", `switch names: ${glued.join("; ")}`);
    });
    await step(`${tag} A3`, async () => {
      const ctx = await page.context().browser().newContext({ baseURL: t.origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });
      try {
        const p = await ctx.newPage();
        await p.goto("/", { waitUntil: "load" });
        await p.getByLabel("Username").fill(t.opt.user);
        await p.getByLabel("Password", { exact: true }).fill("not-the-password");
        await p.getByRole("button", { name: "Sign in" }).click();
        await p.getByRole("alert").first().waitFor({ timeout: 10000 });
        await p.waitForTimeout(200);
        const user = await described(p, "input[name=username]");
        const pass = await described(p, "input[name=password]");
        check(
          user?.invalid === "true" && pass?.invalid === "true" && /didn't match/.test(pass.text) && user.text === pass.text && pass.focused,
          `${tag} A3`,
          "both fields are invalid and described by the error; the password has focus",
          `username ${JSON.stringify(user)}; password ${JSON.stringify(pass)}`,
        );
        await t.shot(p, `${tag}-A3`);
      } finally {
        await ctx.close();
      }
    });
    await step(`${tag} A4`, async () => {
      await page.goto("/feeds", { waitUntil: "load" });
      await page.getByRole("button", { name: "Add feed" }).first().click();
      const dlg = page.getByRole("dialog", { name: "Add feed" });
      await dlg.waitFor({ timeout: 10000 });
      const field = dlg.getByLabel("Feed or website address");
      await field.fill("not a url !!");
      await field.press("Enter");
      await dlg.getByRole("alert").first().waitFor({ timeout: 15000 });
      await page.waitForTimeout(200);
      const id = await field.getAttribute("id");
      const d = await described(page, `#${CSS_escape(id)}`);
      check(d?.invalid === "true" && d.text.length > 0 && d.focused, `${tag} A4`, `the address field is invalid, described ("${d?.text}") and focused`, `field ${JSON.stringify(d)}`);
      await t.shot(page, `${tag}-A4`);
      await page.keyboard.press("Escape");
    });
    await step(`${tag} A5`, async () => {
      await page.goto("/l/all", { waitUntil: "load" });
      const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
      await row.waitFor({ timeout: 15000 });
      await row.locator("a").first().click();
      await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
      const bar = page.getByRole("toolbar", { name: "Article actions" });
      const more = bar.getByRole("button", { name: "More actions" });
      await more.waitFor({ timeout: 10000 });
      await more.click();
      const read = page.getByRole("menuitem", { name: /^Mark as (un)?read$/ });
      await read.waitFor({ timeout: 10000 });
      if ((await read.textContent())?.trim() === "Mark as read") { // uat-labels: ignore (a menu item's text, not an aria-label)
        await read.click();
        await page.waitForTimeout(500);
        await more.click();
      }
      await page.getByRole("menuitem", { name: "Mark as unread" }).click(); // uat-labels: ignore (a menu item's text, not an aria-label)
      await page.waitForTimeout(400);
      const before = (await page.getByTestId("undo-region").textContent()) ?? "";
      const url = page.url();
      await bar.getByRole("button", { name: "Next article" }).click();
      await page.waitForURL((u) => u.href !== url, { timeout: 10000 });
      await page.waitForTimeout(400);
      const after = (await page.getByTestId("undo-region").textContent()) ?? "";
      check(before.includes("Marked unread") && !after.includes("Marked unread"), `${tag} A5`, "Next takes down the Marked unread toast", `before "${before}", after Next "${after}"`);
    });
    await step(`${tag} A6`, async () => {
      await page.goto("/settings/advanced", { waitUntil: "load" });
      const fold = page.getByRole("button", { name: "For servers behind a proxy" });
      await fold.waitFor({ timeout: 15000 });
      if ((await fold.getAttribute("aria-expanded")) === "false") await fold.click();
      await page.getByText(/Kipple (does not check|checks the) Access token/).first().waitFor({ timeout: 15000 });
      const live = await page.evaluate(() => [...document.querySelectorAll('[role="status"],[aria-live]')].some((r) => /Access token/.test(r.textContent ?? "")));
      check(!live, `${tag} A6`, "the Access state is not in a live region", "the Access state is inside a live region");
    });
    await step(`${tag} A7`, async () => {
      await page.goto("/l/all", { waitUntil: "load" });
      const row = page.locator("[data-testid=list-scroll] [data-item-id]").first();
      await row.waitFor({ timeout: 15000 });
      const id = await row.getAttribute("data-item-id");
      const title = (await (await page.request.get(`/api/items/${id}`, { headers: JSON_ACCEPT })).json()).title;
      const star = await row.getByRole("button", { name: `Star: ${title}` }).count();
      const more = await row.getByRole("button", { name: `More actions: ${title}` }).count();
      const link = await row.getByRole("link", { name: new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(, unread)?$`) }).count();
      let nav = 1;
      if (!vp.mobile) nav = await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: /^Unread( \d+\+?)?$/ }).count();
      check(star === 1 && more === 1 && link >= 1 && nav === 1, `${tag} A7`, "row controls name their article; the link starts with the title; Unread reads its count once", `star ${star}, more ${more}, link ${link}, nav ${nav} (title "${title}")`);
    });
    if (!vp.mobile) {
      await step(`${tag} A8`, async () => {
        await page.goto("/l/all", { waitUntil: "load" });
        await page.locator("[data-testid=list-scroll] [data-item-id]").first().waitFor({ timeout: 15000 });
        await page.getByRole("button", { name: "Reading appearance" }).first().click();
        const theme = page.getByRole("dialog", { name: "Reading appearance" }).getByRole("combobox").first();
        const was = await theme.inputValue();
        try {
          await theme.selectOption("cocoa-mid");
          await page.keyboard.press("Escape");
          await page.locator("[data-testid=list-scroll] [data-item-id] a").first().click();
          await page.waitForURL(/\/i\/\d+/, { timeout: 10000 });
          await page.waitForTimeout(500);
          const colors = await page.evaluate(() => {
            const row = document.querySelector("[data-testid=list-scroll] [data-selected]");
            if (!row) return null;
            let bgEl = row;
            while (bgEl && getComputedStyle(bgEl).backgroundColor === "rgba(0, 0, 0, 0)") bgEl = bgEl.parentElement;
            const meta = [...row.querySelectorAll("*")].find((e) => e.children.length === 0 && (e.textContent ?? "").trim() && getComputedStyle(e).color !== getComputedStyle(row).color);
            return { bg: bgEl ? getComputedStyle(bgEl).backgroundColor : null, fg: meta ? getComputedStyle(meta).color : null };
          });
          const r = colors?.bg && colors.fg ? ratio(colors.fg, colors.bg) : 0;
          check(r >= 4.5, `${tag} A8`, `Cocoa Mid selected-row secondary text ${r.toFixed(2)}:1`, `Cocoa Mid selected row ${JSON.stringify(colors)} -> ${r.toFixed(2)}:1`);
          await t.shot(page, `${tag}-A8`);
        } finally {
          await page.keyboard.press("Escape");
          await page.getByRole("button", { name: "Reading appearance" }).first().click();
          await page.getByRole("dialog", { name: "Reading appearance" }).getByRole("combobox").first().selectOption(was);
          await page.keyboard.press("Escape");
        }
      });
    }
  } finally {
    const nowUnread = new Set(await unreadIds(page));
    const read = wasUnread.filter((id) => !nowUnread.has(id));
    if (read.length) await page.request.post("/api/items/mark-read", { headers: t.WRITE, data: { ids: read, read: false } }).catch(() => {});
  }
});

async function signIn(ctx) {
  const p = await ctx.newPage();
  await p.goto("/", { waitUntil: "load" });
  await p.getByLabel("Username").fill(t.opt.user);
  await p.getByLabel("Password", { exact: true }).fill(t.opt.password);
  await p.getByRole("button", { name: "Sign in" }).click();
  await p.waitForURL(/\/l\//, { timeout: 15000 });
  return p;
}

function CSS_escape(id) {
  return String(id).replace(/[^\w-]/g, (c) => `\\${c}`);
}

await t.finish("announcements");
