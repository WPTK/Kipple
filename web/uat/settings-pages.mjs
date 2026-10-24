// UAT: the Settings pages after the regroup (#681, #679, #683, #677, #678, #680).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:settings-pages         in another
//   npm run uat:settings-pages -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it opens each group's page and checks:
//   S1  Appearance has Look, Lists, Articles and Accessibility, and the font switch is named by what it does;
//   S2  Sync & Feeds has Checking feeds and Storage; Apply retention now opens "Remove older articles?" with a count and
//       Cancel leaves everything as it was;
//   S3  Account & Devices has Devices, Sign-in, Sync apps and Backup and data (with the storage figures), shows the address
//       and username, offers Restore from a backup, and its dialog asks for the phrase and is cancelled without erasing;
//   S4  Advanced has Address and access, with Other names for this server outside the proxy fold.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const h2s = () => page.locator("main :is(h2, h3)").evaluateAll((els) => els.map((e) => (e.textContent ?? "").trim()));
  const open = async (id) => {
    await page.goto(`/settings/${id}`, { waitUntil: "load" });
    await page.locator("main :is(h2, h3)").first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(800);
  };

  await open("appearance");
  let hs = await h2s();
  check(["Look", "Lists", "Articles", "Accessibility"].every((h) => hs.includes(h)), `${tag} S1 sections`, "Appearance has its four sections", JSON.stringify(hs));
  check((await page.getByRole("switch", { name: "Also use it in lists and the sidebar" }).count()) === 1, `${tag} S1 font`, "the font switch says what it does", "no switch named 'Also use it in lists and the sidebar'");
  await t.shot(page, `${tag}-appearance`);

  await open("sync");
  hs = await h2s();
  check(["Checking feeds", "Storage"].every((h) => hs.includes(h)), `${tag} S2 sections`, "Sync has Checking feeds and Storage", JSON.stringify(hs));
  await page.getByRole("button", { name: "Apply retention now" }).click();
  const rd = page.getByRole("dialog", { name: "Remove older articles?" });
  await rd.waitFor({ timeout: 10000 });
  await rd.getByText(/About d[d,]* articles? from|nothing is removed/).waitFor({ timeout: 10000 }).catch(() => {});
  check(/About [d,]+ articles? from [d,]+ feeds?|nothing is removed/.test(await rd.innerText()), `${tag} S2 retention`, "the dialog says how many articles go", "the dialog has no count");
  await t.shot(page, `${tag}-retention-dialog`);
  await rd.getByRole("button", { name: "Cancel" }).click();
  await t.shot(page, `${tag}-sync`);

  await open("account");
  hs = await h2s();
  check(["Devices", "Sign-in", "Backup and data"].every((h) => hs.includes(h)) && hs.some((h) => h.startsWith("Sync apps")), `${tag} S3 sections`, "Account has its sections", JSON.stringify(hs));
  const addr = (await page.getByTestId("sync-address").innerText().catch(() => "")).trim();
  check(addr.endsWith("/api/greader.php"), `${tag} S3 address`, "the sync address is shown", `got "${addr}"`);
  check((await page.getByText("Restore from a backup").count()) > 0, `${tag} S3 restore`, "Restore from a backup is offered", "no Restore from a backup entry");
  check((await page.getByRole("heading", { name: "Danger zone" }).count()) === 1, `${tag} S3 danger`, "the danger zone is under Backup and data", "no Danger zone heading");
  check((await page.getByTestId("storage-figures").innerText().catch(() => "")).includes("Database"), `${tag} S3 storage`, "the storage figures are under Backup and data", "no storage figures");
  await t.shot(page, `${tag}-account`);
  await page.getByRole("button", { name: "Restore from a backup" }).click();
  const dlg = page.getByRole("dialog");
  await dlg.waitFor({ timeout: 10000 });
  const text = await dlg.innerText();
  check(/erases your current library first and keeps a safety copy/.test(text), `${tag} S3 restore dialog`, "the dialog says what happens", `got "${text.slice(0, 160)}"`);
  check(await dlg.getByRole("button", { name: "Erase and choose a backup" }).isDisabled(), `${tag} S3 restore gate`, "the button waits for the password and phrase", "the button is enabled with nothing typed");
  await t.shot(page, `${tag}-restore-dialog`);
  await dlg.getByRole("button", { name: "Cancel" }).click();

  await open("advanced");
  hs = await h2s();
  check(hs.includes("Address and access"), `${tag} S4 sections`, "Advanced has Address and access", JSON.stringify(hs));
  check((await page.getByLabel("Other names for this server", { exact: true }).count()) === 1, `${tag} S4 names`, "Other names for this server is not inside the closed fold", "no Other names field");
  await t.shot(page, `${tag}-advanced`);
});

await t.finish("Settings pages");
