// UAT: the time zone is empty until someone chooses one (UTC is used meanwhile), and a choice stays.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/time-zone.mjs             in another
//   node uat/time-zone.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// The setup wizard's own adoption of the browser's zone is in uat/wizard.mjs. For each width (desktop 1280x800, phone
// 375x812) this checks the Settings side, from an instance where no zone has been chosen (it clears the setting first and (uat-labels: ignore)
// puts it back at the end):
//   Z1  Settings > Stats shows an empty Time zone box that says UTC applies until one is chosen, the server reports an (uat-labels: ignore)
//       empty value, and the Stats summary is drawn in UTC meanwhile;
//   Z2  typing Asia/Tokyo and pressing Enter saves it, the box and the server say Asia/Tokyo, the Stats summary follows
//       it, and a reload shows it again;
//   Z3  choosing UTC on purpose is kept as UTC (it is not the same as empty) across a reload;
//   Z4  a name that is no time zone is refused in words, the box keeps what was typed and the saved zone is unchanged;
//   Z5  the box fits the screen on a phone (no sideways scroll);
//   Z6  on a device whose zone is not UTC, an unchosen zone shows "Use this device's time zone (...)" beside the box; pressing
//       it saves that zone, the Stats summary follows and the button goes (a chosen zone, UTC included, shows none);
//   Z7  the Stats screen says days are shown in UTC and offers the same button, drawn with the first chart and not after it;
//       pressing it saves the zone and the line goes, and "Not now" hides the line, saves nothing and survives a reload.
// The whole run is a browser whose zone is America/New_York, so the offer applies throughout; Z1 to Z5 do not depend on it.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const DEVICE_ZONE = "America/New_York";
const t = await start(import.meta.url, 16, { timezoneId: DEVICE_ZONE });
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const patch = (body) =>
    page.evaluate(
      (b) => fetch("/api/settings", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) }).then((r) => r.status),
      body,
    );
  const saved = () => page.evaluate(async () => (await (await fetch("/api/settings")).json()).settings.find((s) => s.key === "tz")?.value);
  const summaryZone = () => page.evaluate(async () => (await (await fetch("/api/stats/summary?range=week")).json()).tz);
  const box = () => page.getByRole("textbox", { name: "Time zone", exact: true });
  const open = async () => {
    await page.goto("/settings/statistics", { waitUntil: "load" });
    await box().waitFor({ timeout: 15000 });
  };
  /** Types a value and saves it with Enter, then waits for the server to answer the save. */
  const choose = async (value) => {
    const asked = page.waitForResponse((r) => r.request().method() === "PATCH" && new URL(r.url()).pathname === "/api/settings", { timeout: 10000 }).catch(() => null);
    await box().fill(value);
    await box().press("Enter");
    return asked;
  };

  const forgetLater = () => page.evaluate(() => localStorage.removeItem("kipple.zoneOfferDismissed.v1")).catch(() => {});

  try {
    await page.goto("/", { waitUntil: "load" });
    check((await patch({ tz: null })) === 200, `${tag} Z1 setup`, "the zone is cleared to its unchosen state", "clearing the zone was refused");

    await step(`${tag} Z1`, async () => {
      await open();
      const help = await page.locator("[data-setting=tz]").innerText();
      check((await box().inputValue()) === "", `${tag} Z1 empty`, "the Time zone box is empty until one is chosen", `the box reads "${await box().inputValue()}"`);
      check(/UTC until you choose one/.test(help), `${tag} Z1 help`, "the help says UTC applies until one is chosen", `the help reads "${help.replace(/\s+/g, " ")}"`);
      check((await saved()) === "", `${tag} Z1 server`, "the server reports no zone chosen", `the server reports "${await saved()}"`);
      check((await summaryZone()) === "UTC", `${tag} Z1 utc`, "the Stats summary uses UTC meanwhile", `the Stats summary uses ${await summaryZone()}`);
      await t.shot(page, `${tag}-time-zone-unset`);
    });

    await step(`${tag} Z2`, async () => {
      const r = await choose("Asia/Tokyo");
      check(r?.status() === 200, `${tag} Z2 save`, "choosing Asia/Tokyo is accepted", `the save answered ${r?.status()}`);
      check((await saved()) === "Asia/Tokyo" && (await summaryZone()) === "Asia/Tokyo", `${tag} Z2 server`, "the server and the Stats summary use Asia/Tokyo", `the server says ${await saved()}, the summary ${await summaryZone()}`);
      await page.reload({ waitUntil: "load" });
      await box().waitFor({ timeout: 15000 });
      check((await box().inputValue()) === "Asia/Tokyo", `${tag} Z2 reload`, "a reload shows Asia/Tokyo", `after a reload the box reads "${await box().inputValue()}"`);
      await t.shot(page, `${tag}-time-zone-chosen`);
    });

    await step(`${tag} Z3`, async () => {
      const r = await choose("UTC");
      check(r?.status() === 200, `${tag} Z3 save`, "choosing UTC is accepted", `the save answered ${r?.status()}`);
      await page.reload({ waitUntil: "load" });
      await box().waitFor({ timeout: 15000 });
      check((await box().inputValue()) === "UTC" && (await saved()) === "UTC", `${tag} Z3 reload`, "UTC chosen on purpose is kept as UTC after a reload", `the box reads "${await box().inputValue()}", the server "${await saved()}"`);
    });

    await step(`${tag} Z4`, async () => {
      const r = await choose("Nowhere/City");
      check(r?.status() === 400, `${tag} Z4 refused`, "a name that is no time zone is refused", `the save answered ${r?.status()}`);
      const alert = await page.locator("[data-setting=tz]").getByText("Kipple does not know that time zone.", { exact: true }).waitFor({ timeout: 5000 }).then(() => true, () => false);
      check(alert && (await box().inputValue()) === "Nowhere/City", `${tag} Z4 words`, "the refusal is said in words and the box keeps what was typed", `the refusal is ${alert ? "shown" : "not shown"}, the box reads "${await box().inputValue()}"`);
      check((await saved()) === "UTC", `${tag} Z4 kept`, "the saved zone is unchanged", `the saved zone is "${await saved()}"`);
    });

    await step(`${tag} Z5`, async () => {
      await open();
      const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      const r = await box().boundingBox();
      check(over <= 0 && r && r.x >= 0 && r.x + r.width <= vp.width + 1, `${tag} Z5 fit`, "the page does not scroll sideways and the box is inside the screen", `overflow ${over}px, the box is at ${JSON.stringify(r)}`);
    });

    const offer = () => page.getByRole("button", { name: `Use this device's time zone (${DEVICE_ZONE})` });
    const stats = async () => {
      await page.goto("/stats", { waitUntil: "load" });
      await page.getByRole("heading", { name: "Daily activity" }).or(page.getByText("Nothing read yet")).first().waitFor({ timeout: 15000 });
    };
    const line = () => page.getByText("Days are shown in UTC.", { exact: true });
    const forget = () => page.evaluate(() => localStorage.removeItem("kipple.zoneOfferDismissed.v1"));

    await step(`${tag} Z6`, async () => {
      await patch({ tz: null });
      await open();
      await offer().waitFor({ timeout: 5000 });
      const r = await offer().boundingBox();
      check(!!r && r.height >= 24 && r.x >= 0 && r.x + r.width <= vp.width + 1, `${tag} Z6 button`, "the button is offered beside the empty box and fits the screen", `the button is at ${JSON.stringify(r)}`);
      await t.shot(page, `${tag}-time-zone-offer`);
      const asked = page.waitForResponse((x) => x.request().method() === "PATCH" && new URL(x.url()).pathname === "/api/settings", { timeout: 10000 }).catch(() => null);
      await offer().click();
      check((await asked)?.status() === 200, `${tag} Z6 save`, "pressing it is accepted", "the save was not accepted");
      check((await saved()) === DEVICE_ZONE && (await summaryZone()) === DEVICE_ZONE, `${tag} Z6 server`, `the server and the Stats summary use ${DEVICE_ZONE}`, `the server says ${await saved()}, the summary ${await summaryZone()}`);
      await box().waitFor({ timeout: 5000 });
      check((await box().inputValue()) === DEVICE_ZONE && (await offer().count()) === 0, `${tag} Z6 gone`, "the box shows the zone and the button is gone", `the box reads "${await box().inputValue()}", ${await offer().count()} button(s)`);
      await patch({ tz: "UTC" });
      await open();
      check((await offer().count()) === 0, `${tag} Z6 utc`, "a zone chosen on purpose (UTC) shows no button", `${await offer().count()} button(s) with UTC chosen`);
    });

    await step(`${tag} Z7`, async () => {
      await patch({ tz: null });
      await page.goto("/", { waitUntil: "load" });
      await forget();
      await stats();
      const first = await page.evaluate(() => !!document.body.innerText.includes("Days are shown in UTC."));
      check(first, `${tag} Z7 line`, "the line is there as soon as the screen has drawn", "the line is not on the screen");
      const yBefore = await page.getByRole("heading", { name: "Daily activity" }).boundingBox().then((b) => b?.y ?? null, () => null);
      await t.shot(page, `${tag}-stats-zone-offer`);
      const bar = await page.getByRole("button", { name: `Use this device's time zone (${DEVICE_ZONE})?` }).boundingBox();
      const later = await page.getByRole("button", { name: "Not now" }).boundingBox();
      check(!!bar && !!later && bar.height >= 24 && later.height >= 24 && later.x + later.width <= vp.width + 1, `${tag} Z7 targets`, "both controls are at least 24 px tall and inside the screen", `bar ${JSON.stringify(bar)}, Not now ${JSON.stringify(later)}`);
      await page.waitForTimeout(500);
      const yAfter = await page.getByRole("heading", { name: "Daily activity" }).boundingBox().then((b) => b?.y ?? null, () => null);
      check(yBefore === yAfter, `${tag} Z7 still`, "the chart does not move after it has drawn", `the heading moved from ${yBefore} to ${yAfter}`);

      await page.getByRole("button", { name: "Not now" }).click();
      await line().waitFor({ state: "detached", timeout: 5000 });
      check((await saved()) === "", `${tag} Z7 later`, "Not now saves no zone", `the server says "${await saved()}"`);
      await stats();
      check((await line().count()) === 0, `${tag} Z7 kept`, "Not now is remembered across a reload", "the line came back after a reload");

      await forget();
      await stats();
      await page.getByRole("button", { name: `Use this device's time zone (${DEVICE_ZONE})?` }).click();
      await line().waitFor({ state: "detached", timeout: 10000 });
      check((await saved()) === DEVICE_ZONE && (await summaryZone()) === DEVICE_ZONE, `${tag} Z7 save`, `pressing the button saves ${DEVICE_ZONE} and the Stats summary follows`, `the server says ${await saved()}, the summary ${await summaryZone()}`);
    });
  } finally {
    await forgetLater();
    await patch({ tz: null }).catch(() => {}); // leave the instance as it was found
  }
});

await t.finish("time zone");
