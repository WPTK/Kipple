// UAT: the layout menu's groups, the one Pictures in lists setting, and Order and Group by greyed where a layout sets its own.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/layout-menu.mjs           in another
//   node uat/layout-menu.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks, on the device-default lists:
//   L1  the layout menu is grouped Pictures, Lists and Pages, its button is named "Show and sort, <layout> layout" (and
//       there is still a separate "List actions" button), and the open menu fits the screen with no sideways scroll;
//   L2  Pictures in lists (Show or Hide) is in the menu's Pictures group and in Settings, Appearance, Lists: choosing Hide in
//       the menu saves client.list_images false, Settings shows Hide checked, and choosing Show in Settings is the menu's value;
//   L3  in the Digest, Order and Group by stay in the menu: every choice is aria-disabled (not disabled), can take focus,
//       is described by the one-line reason, and choosing one changes nothing; on a wide screen the header's Oldest first
//       button is aria-disabled with the same reason and a click leaves the order alone (a phone has no such button);
//   L4  in Compact the same choices and the button are live;
//   L5  Settings, Sync names the server image group "Image cache".
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { expect } from "@playwright/test";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 17);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (b) =>
    page.evaluate((x) => fetch("/api/device", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(x) }).then((r) => r.status), b);
  const profile = () => page.evaluate(() => fetch("/api/device").then((r) => r.json())).then((d) => d.profile ?? {});
  const trigger = () => page.locator('button[aria-label^="Show and sort, "]').first();
  const openMenu = async () => {
    await trigger().click();
    const menu = page.getByRole("menu");
    await menu.waitFor({ timeout: 10000 });
    return menu;
  };
  const closeMenu = async () => {
    await page.keyboard.press("Escape");
    await page.getByRole("menu").waitFor({ state: "detached", timeout: 5000 }).catch(() => {});
  };
  const open = async (layout) => {
    const set = await patch({ "client.layout": layout });
    if (set !== 200) throw new Error(`the layout was not set (${set})`);
    await page.goto("/l/unread", { waitUntil: "load" });
    await trigger().waitFor({ timeout: 20000 });
  };
  /** The one-line reason a radio is described by, read from the element its aria-describedby names. */
  const reasonOf = (radio) =>
    radio.evaluate((el) => {
      const id = el.getAttribute("aria-describedby");
      return id ? (document.getElementById(id)?.textContent ?? "").trim() : "";
    });

  await step(`${tag} L`, async () => {
    try {
      // L1
      await open("magazine");
      const name = (await trigger().getAttribute("aria-label")) ?? "";
      const actions = await page.getByRole("button", { name: "List actions", exact: true }).count();
      check(name === "Show and sort, Editorial layout" && actions === 1, `${tag} L1 name`, "the menu button reads \"Show and sort, Editorial layout\", beside a separate \"List actions\"", `button "${name}", ${actions} "List actions"`);
      const menu = await openMenu();
      const headings = await menu.evaluate((m) => [...m.querySelectorAll("div, p, span")].map((e) => (e.children.length === 0 ? (e.textContent ?? "").trim() : "")).filter((x) => ["Pictures", "Lists", "Pages"].includes(x)));
      check(headings.join() === "Pictures,Lists,Pages", `${tag} L1 groups`, "the layouts sit under Pictures, Lists and Pages, in that order", `headings: ${headings.join(", ")}`);
      const groupNames = await menu.getByRole("group").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label") ?? ""));
      check(["Layout, Pictures", "Layout, Lists", "Layout, Pages", "Pictures in lists"].every((n) => groupNames.includes(n)), `${tag} L1 group names`, "Pictures, Lists and Pages are labelled groups, and Pictures in lists is its own group", `groups: ${groupNames.join(" | ")}`);
      const fit = await menu.evaluate((m) => {
        const r = m.getBoundingClientRect();
        return { left: r.left, right: r.right, width: window.innerWidth, overflow: document.documentElement.scrollWidth - window.innerWidth };
      });
      check(fit.left >= 0 && fit.right <= fit.width + 0.5 && fit.overflow <= 0, `${tag} L1 fit`, "the open menu fits the screen with no sideways scroll", `menu ${Math.round(fit.left)}..${Math.round(fit.right)} of ${fit.width}, page overflow ${fit.overflow}`);
      await t.shot(page, `${tag}-layout-menu-groups`);
      await closeMenu();
      const feedId = await page.evaluate(() => fetch("/api/bootstrap").then((r) => r.json()).then((b) => b.feeds?.[0]?.id));
      await page.goto(`/l/unread?feed=${feedId}`, { waitUntil: "load" });
      await trigger().waitFor({ timeout: 20000 });
      const fm = await openMenu();
      const pos = await fm.evaluate((m) => {
        const at = (label) => [...m.querySelectorAll('[role="group"]')].findIndex((g) => (g.getAttribute("aria-label") ?? "").startsWith(label));
        return { pages: at("Layout, Pages"), opens: at("View this"), order: at("Order") };
      });
      check(pos.pages >= 0 && pos.opens > pos.pages && pos.order > pos.opens, `${tag} L1 opens in`, "on a feed, Opens in sits under Pages and before Order", `group positions: Pages ${pos.pages}, Opens in ${pos.opens}, Order ${pos.order}`);
      await closeMenu();
      await open("magazine");

      // L2
      const menu2 = await openMenu();
      const pictures = menu2.getByRole("group", { name: "Pictures in lists" });
      check((await pictures.count()) === 1 && (await pictures.getByRole("menuitemradio", { name: /^Show/ }).getAttribute("aria-checked")) === "true", `${tag} L2 menu`, "the Pictures group offers Pictures in lists with Show chosen", "no such group or Show is not chosen");
      await pictures.getByRole("menuitemradio", { name: "Hide", exact: true }).click();
      await expect.poll(async () => (await profile())["client.list_images"], { timeout: 10000, intervals: [250] }).toBe(false).catch(() => {});
      check((await profile())["client.list_images"] === false, `${tag} L2 saved`, "Hide saves client.list_images false for this device", `the profile holds ${JSON.stringify((await profile())["client.list_images"])}`);
      await page.goto("/settings/appearance", { waitUntil: "load" });
      const setting = page.getByRole("group", { name: "Pictures in lists" });
      await setting.waitFor({ timeout: 15000 });
      check((await setting.getByRole("radio", { name: "Hide", exact: true }).isChecked()) && (await page.getByRole("group", { name: "Thumbnails in Inbox" }).count()) === 0, `${tag} L2 settings`, "Settings shows Pictures in lists with Hide chosen, and no Thumbnails in Inbox", "Hide is not chosen there, or the old setting is still drawn");
      if (vp.mobile) await setting.scrollIntoViewIfNeeded();
      await t.shot(page, `${tag}-settings-pictures-in-lists`);
      await setting.locator("label", { hasText: /^Show$/ }).click();
      await expect.poll(async () => (await profile())["client.list_images"] !== false, { timeout: 10000, intervals: [250] }).toBe(true).catch(() => {});
      await page.goto("/l/unread", { waitUntil: "load" });
      await trigger().waitFor({ timeout: 20000 });
      const again = await openMenu();
      check((await again.getByRole("group", { name: "Pictures in lists" }).getByRole("menuitemradio", { name: /^Show/ }).getAttribute("aria-checked")) === "true", `${tag} L2 same value`, "Show chosen in Settings is Show in the menu", "the menu does not show Show chosen");
      await closeMenu();

      // L3
      await open("digest");
      const digest = await openMenu();
      const radios = [];
      for (const g of ["Order", "Group by"]) {
        const group = digest.getByRole("group", { name: g, exact: true });
        if ((await group.count()) !== 1) return t.fail(`${tag} L3 ${g}`, `the ${g} group is not in the Digest's menu`);
        for (const r of await group.getByRole("menuitemradio").all()) radios.push({ g, r });
      }
      const states = [];
      for (const { g, r } of radios) states.push({ g, n: ((await r.textContent()) ?? "").trim(), aria: await r.getAttribute("aria-disabled"), native: await r.getAttribute("disabled"), why: await reasonOf(r) });
      const bad = states.filter((s) => s.aria !== "true" || s.native !== null || s.why === "");
      check(states.length >= 5 && bad.length === 0, `${tag} L3 greyed`, "every Order and Group by choice is aria-disabled, not disabled, and described by a reason", bad.map((s) => `${s.g}/${s.n}: aria-disabled ${s.aria}, disabled ${s.native}, reason "${s.why}"`).join("; ") || `only ${states.length} choices`);
      const oldest = digest.getByRole("group", { name: "Order", exact: true }).getByRole("menuitemradio", { name: /^Oldest first/ });
      check((await reasonOf(oldest)) === "The Digest is always newest first.", `${tag} L3 reason`, "the reason reads \"The Digest is always newest first.\"", `reads "${await reasonOf(oldest)}"`);
      await oldest.focus();
      check(await oldest.evaluate((el) => document.activeElement === el), `${tag} L3 focus`, "a greyed choice can take focus", "focus did not land on it");
      await t.shot(page, `${tag}-layout-menu-greyed-digest`);
      await oldest.click({ force: true });
      const order = (await profile())["client.order"];
      check(order === undefined || order === "newest", `${tag} L3 inert`, "choosing a greyed Oldest first changes nothing", `client.order is ${JSON.stringify(order)}`);
      if (await page.getByRole("menu").count()) await closeMenu();
      const header = page.getByRole("button", { name: "Oldest first", exact: true });
      if (vp.mobile) {
        check((await header.count()) === 0, `${tag} L3 header`, "a phone has no Oldest first button in its header", `${await header.count()} found`);
      } else {
        const state = await header.evaluate((el) => ({ aria: el.getAttribute("aria-disabled"), native: el.hasAttribute("disabled"), why: (document.getElementById(el.getAttribute("aria-describedby") ?? "")?.textContent ?? "").trim() }));
        check(state.aria === "true" && !state.native && state.why === "The Digest is always newest first.", `${tag} L3 header`, "the header's Oldest first is aria-disabled with the same reason", JSON.stringify(state));
        await header.click({ force: true });
        const after = (await profile())["client.order"];
        check(after === undefined || after === "newest", `${tag} L3 header inert`, "a click on the greyed Oldest first changes nothing", `client.order is ${JSON.stringify(after)}`);
        await t.shot(page, `${tag}-header-oldest-greyed`);
      }

      // L4
      await open("compact");
      const live = await openMenu();
      const cradios = await live.getByRole("group", { name: "Order", exact: true }).getByRole("menuitemradio").all();
      let anyGrey = 0;
      for (const r of cradios) if ((await r.getAttribute("aria-disabled")) === "true") anyGrey++;
      check(cradios.length === 2 && anyGrey === 0, `${tag} L4 live`, "in Compact the Order choices are live", `${cradios.length} choices, ${anyGrey} greyed`);
      await closeMenu();
      if (!vp.mobile) {
        const h = page.getByRole("button", { name: "Oldest first", exact: true });
        check((await h.getAttribute("aria-disabled")) === null, `${tag} L4 header`, "in Compact the header's Oldest first is live", `aria-disabled ${await h.getAttribute("aria-disabled")}`);
      }

      // L5
      await page.goto("/settings/sync", { waitUntil: "load" });
      await page.getByRole("heading", { name: "Storage", exact: true }).waitFor({ timeout: 15000 });
      const cache = await page.getByRole("heading", { name: "Image cache", exact: true }).count();
      check(cache === 1, `${tag} L5 image cache`, "Settings, Sync has an Image cache group", `${cache} Image cache`);
    } finally {
      await patch({ "client.layout": null, "client.list_images": null, "client.order": null });
    }
  });
});

await t.finish("layout menu");
