// UAT: the Settings pages after the regroup (#681, #679, #683, #677, #678, #680).
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:settings-pages         in another
//   npm run uat:settings-pages -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it opens each group's page and checks:
//   S1  Appearance has Look, Lists, Articles and Accessibility, and the font switch is named by what it does;
//   S2  Sync & Feeds has Checking feeds and Storage; Apply retention now says nothing is removed; choosing 50 with a 60-article feed present opens "Remove older articles?" with the count, and Cancel keeps the old choice;
//   S3  Account & Devices has Devices, Sign-in, Sync apps and Backup and data (with the storage figures), shows the address
//       and username, offers Restore from a backup, and its dialog asks for the phrase and is cancelled without erasing;
//   S4  Advanced has Address and access, with Other names for this server outside the proxy fold.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 16);
const { check } = t;

// A feed of 60 articles served on loopback: 10 more than the smallest "articles to keep" choice, so lowering the choice to 50 has
// something to remove and the retention dialog must say so.
const FEED_TITLE = "UAT feed settings pages";
const server = createServer((req, res) => {
  const base = "http://" + req.headers.host;
  const item = (n) => `<item><title>UAT settings story ${n}</title><link>${base}/page/${n}</link><guid isPermaLink="false">uat-settings-${n}</guid><pubDate>${new Date(Date.now() - n * 60000).toUTCString()}</pubDate><description>&lt;p&gt;Story ${n}.&lt;/p&gt;</description></item>`;
  res.setHeader("Content-Type", "application/rss+xml");
  res.end(`<?xml version="1.0"?><rss version="2.0"><channel><title>${FEED_TITLE}</title><link>${base}/</link><description>x</description>${Array.from({ length: 60 }, (_, i) => item(i + 1)).join("")}</channel></rss>`);
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const feedUrl = "http://127.0.0.1:" + server.address().port + "/feed.xml";

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
  // Apply retention now against the saved default (250) removes nothing and says so.
  await page.getByRole("button", { name: "Apply retention now" }).click();
  let rd = page.getByRole("dialog", { name: "Remove older articles?" });
  await rd.waitFor({ timeout: 10000 });
  await rd.getByText("nothing is removed").waitFor({ timeout: 10000 }).catch(() => {});
  check((await rd.innerText()).includes("nothing is removed"), `${tag} S2 apply`, "Apply retention now says nothing is removed", "no 'nothing is removed' in the dialog");
  await rd.getByRole("button", { name: "Cancel" }).click();
  // Lowering the default to 50 with a 60-article feed present: the dialog counts the 10 that would go.
  const added = await t.addLocalFeed(page, feedUrl, FEED_TITLE, (items) => items.length >= 50); // one page is at most 50 articles
  await page.waitForTimeout(2000); // the rest of the 60 are stored in the same fetch
  if (added.error) t.fail(`${tag} S2 setup`, added.error);
  else {
    await page.reload({ waitUntil: "load" });
    await page.getByRole("combobox", { name: "Articles to keep per feed" }).selectOption("50");
    rd = page.getByRole("dialog", { name: "Remove older articles?" });
    await rd.waitFor({ timeout: 10000 });
    await rd.getByText(/About [0-9,]+ articles? from [0-9,]+ feeds?/).waitFor({ timeout: 10000 }).catch(() => {});
    const text = await rd.innerText();
    const m = text.match(/About ([0-9,]+) articles? from ([0-9,]+) feeds?/);
    check(m !== null && Number(m[1].replace(/,/g, "")) >= 10, `${tag} S2 count`, "the dialog counts the articles that would go", `got "${text.slice(0, 200)}"`);
    await t.shot(page, `${tag}-retention-dialog`);
    await rd.getByRole("button", { name: "Cancel" }).click();
    const kept = await page.getByRole("combobox", { name: "Articles to keep per feed" }).inputValue();
    check(kept === "250", `${tag} S2 cancel`, "Cancel leaves the setting as it was", `the choice reads ${kept}`);
    for (const f of (await t.boot(page)).feeds.filter((x) => x.title === FEED_TITLE)) await page.request.delete(`/api/feeds/${f.id}`, { headers: t.WRITE });
  }
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

server.close();
await t.finish("Settings pages");
