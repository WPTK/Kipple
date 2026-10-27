// UAT: Settings saves one way, the theme has a mode control, and help is one sentence (12W-2 A: #682, #684, #685, #632).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   node uat/settings-save-model.mjs   in another
//   node uat/settings-save-model.mjs --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   M1  Appearance has the mode control (One theme, Follow device, On a schedule) above the swatches, and Follow device
//       shows a Day and a Night grid of swatches, not selects;
//   M2  at text size Largest the size buttons are whole (none clipped) and the page does not scroll sideways;
//   V1  a switch that saves shows a visible Saved beside it;
//   V2  a text setting saves with Save, Revert puts the saved text back, and a refused value keeps what was typed with
//       the reason in words;
//   V3  a number that is not valid goes back to the saved one, with the reason;
//   H1  a setting's help is its first sentence, with the rest behind More;
//   A1  Preview under "Mark old articles as read after" opens a dialog that holds the field for another number of days;
//   D1  the device buttons in Account are told apart by when each device was last seen;
//   Y1  the sync address in Account does not run past the screen.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start, waitForStillFor } from "./common.mjs";

const t = await start(import.meta.url, 19);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  page.setDefaultTimeout(8000);
  const open = async (id) => {
    await page.goto(`/settings/${id}`, { waitUntil: "load" });
    await page.locator("main :is(h2, h3)").first().waitFor({ timeout: 15000 });
    await waitForStillFor(page, 300, () => page.locator("main").innerText(), { label: "the settings page to finish loading" });
  };
  /** Picks a segmented option by clicking its label (the radio itself is drawn off-screen). */
  const pick = (group, name) => group.locator("label").filter({ hasText: new RegExp(`^${name}$`) }).click();
  const sideways = () => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  const settingsPatch = (body) =>
    page.evaluate(
      (b) => fetch("/api/settings", { method: "PATCH", headers: { "Content-Type": "application/json", "X-Kipple-Client": "web" }, body: JSON.stringify(b) }).then((r) => r.status),
      body,
    );

  await step(`${tag} M1`, async () => {
    await open("appearance");
    const mode = page.getByRole("group", { name: "Theme", exact: true });
    for (const n of ["One theme", "Follow device", "On a schedule"]) check((await mode.getByRole("radio", { name: n }).count()) === 1, `${tag} M1 ${n}`, `the mode control offers ${n}`, "it is missing");
    await pick(mode, "Follow device");
    await page.getByRole("group", { name: "Day theme" }).waitFor();
    const night = page.getByRole("group", { name: "Night theme" });
    await night.waitFor();
    check((await page.getByRole("combobox", { name: /^(Day|Night) theme$/ }).count()) === 0, `${tag} M1 swatches`, "Day and Night are grids of swatches, not selects", "a select is still there");
    check((await night.getByRole("radio").count()) >= 5, `${tag} M1 grid`, "the Night grid offers several swatches", `${await night.getByRole("radio").count()} found`);
    await t.shot(page, `${tag}-theme-modes`);
    await pick(mode, "One theme");
    await page.getByRole("group", { name: "Theme colors" }).waitFor();
  });

  await step(`${tag} M2`, async () => {
    await open("appearance");
    await pick(page.getByRole("group", { name: "Text size" }), "Largest");
    await waitForStillFor(page, 300, () => page.locator("main").innerText(), { label: "the page to settle at the larger text" });
    const clipped = await page.getByRole("group", { name: "Text size" }).locator("label").evaluateAll((els) => els.filter((l) => l.scrollWidth > l.clientWidth + 1).map((l) => l.textContent));
    check(clipped.length === 0, `${tag} M2 sizes`, "no text size button is clipped", `clipped: ${JSON.stringify(clipped)}`);
    check((await sideways()) <= 0, `${tag} M2 fit`, "the page does not scroll sideways", `${await sideways()}px over`);
    await t.shot(page, `${tag}-text-largest`);
    await pick(page.getByRole("group", { name: "Text size" }), "Default");
  });

  await step(`${tag} V1`, async () => {
    await settingsPatch({ "links.strip_tracking": false });
    await open("appearance");
    await page.locator("label").filter({ hasText: "Remove tracking from links" }).click();
    await page.getByText("Saved", { exact: true }).first().waitFor({ timeout: 5000 });
    check(await page.getByText("Saved", { exact: true }).first().isVisible(), `${tag} V1 saved`, "a switch that saved shows a visible Saved", "no Saved on screen");
    await settingsPatch({ "links.strip_tracking": false });
  });

  await step(`${tag} V2`, async () => {
    await settingsPatch({ "fetch.user_agent": "" });
    await open("sync");
    const box = page.getByRole("textbox", { name: "Custom browser identity", exact: true });
    const save = page.getByRole("button", { name: "Save Custom browser identity" });
    check(await save.isDisabled(), `${tag} V2 idle`, "Save waits for a change", "Save is enabled with nothing typed");
    await box.fill("x".repeat(201));
    await save.click();
    await page.getByText("Use one line of at most 200 characters.").waitFor({ timeout: 5000 });
    check((await box.inputValue()).length === 201, `${tag} V2 kept`, "a refused text stays in the box, with its reason in words", "the box lost what was typed");
    await page.getByRole("button", { name: "Revert Custom browser identity" }).click();
    check((await box.inputValue()) === "", `${tag} V2 revert`, "Revert puts the saved text back", `the box reads "${(await box.inputValue()).slice(0, 20)}"`);
    await box.fill("UAT reader");
    await save.click();
    await page.getByText("Saved", { exact: true }).first().waitFor({ timeout: 5000 });
    await settingsPatch({ "fetch.user_agent": "" });
  });

  await step(`${tag} V3`, async () => {
    await open("sync");
    await pick(page.locator("[data-setting=\"refresh.interval_minutes\"]"), "Custom");
    const num = page.getByRole("spinbutton", { name: "How often to check feeds" });
    const before = await num.inputValue();
    await num.fill("1");
    await num.press("Enter");
    await page.getByText(/Kept \d+\./).waitFor({ timeout: 5000 });
    check((await num.inputValue()) === before, `${tag} V3 revert`, "a number out of range goes back to the saved one, with the reason", `the box reads ${await num.inputValue()}, saved ${before}`);
  });

  await step(`${tag} H1`, async () => {
    await open("sync");
    const more = page.getByRole("button", { name: "More about Mark old articles as read after…" });
    check((await more.count()) === 1, `${tag} H1 more`, "the long help has a More button", "no More button");
    check(!(await page.locator("main").innerText()).includes("Changing it marks nothing until"), `${tag} H1 folded`, "the exception is not shown until More is chosen", "the whole paragraph is on screen");
    await more.click();
    check((await page.locator("main").innerText()).includes("Changing it marks nothing until"), `${tag} H1 open`, "More shows the rest", "the rest did not appear");
  });

  await step(`${tag} A1`, async () => {
    await open("sync");
    check((await page.getByRole("textbox", { name: /Preview as if/ }).count()) === 0, `${tag} A1 folded`, "the what-if field is not on the page", "it is still on the page");
    await page.getByRole("button", { name: "Preview", exact: true }).click();
    const dlg = page.getByRole("dialog", { name: "Mark old articles as read" });
    await dlg.waitFor();
    await dlg.getByRole("spinbutton", { name: "Try a different number of days" }).waitFor();
    await t.shot(page, `${tag}-preview-dialog`);
    await dlg.getByRole("button", { name: "Close" }).click();
  });

  await step(`${tag} D1 Y1`, async () => {
    await open("account");
    const forgets = await page.getByRole("button", { name: /^Forget / }).evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
    check(forgets.every((n) => /, last seen /.test(n ?? "")) && new Set(forgets).size === forgets.length, `${tag} D1 names`, "each Forget button names its device and when it was last seen", JSON.stringify(forgets));
    const addr = page.getByTestId("sync-address");
    const box = await addr.boundingBox();
    check(box !== null && box.x >= 0 && box.x + box.width <= vp.width + 1 && (await sideways()) <= 0, `${tag} Y1 fit`, "the sync address stays inside the screen", `box ${JSON.stringify(box)}`);
    await t.shot(page, `${tag}-account`);
  });
});

await t.finish("settings save model");
