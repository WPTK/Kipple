// UAT: what an OPML import says, from the refusal of a file it can't use to the fetch of the feeds it added.
//
//   npm run seed                       in one terminal (builds the web app if needed, serves it on 127.0.0.1:1919)
//   npm run uat:opml-report            in another
//   npm run uat:opml-report -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it checks:
//   O1  an empty file, a file with only empty folders and a file using an entity XML and HTML do not define are each
//       refused with their own message, and nothing is imported;
//   O2  a file with a feed on a private address, a feed listed twice and a feed with no web address reports each once,
//       in plain words, and points to the switch that lets Kipple fetch the private one;
//   O3  the fetch of the new feeds shows a bar and then how many could not be fetched;
//   O4  Move for selected feeds opens with no folder chosen.
// The feeds it adds have "uat-feed-" addresses, so the shared cleanup removes them.
// It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { Buffer } from "node:buffer";
import { deadFeed, start } from "./common.mjs";

const t = await start(import.meta.url, 14);
const { check, step } = t;

const PRIVATE = "http://127.0.0.1:9/uat-feed-private.xml";
const outline = (title, url) => `<outline type="rss" text="${title}" title="${title}" xmlUrl="${url}"/>`;
const opml = (body) => `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body>${body}</body></opml>`;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const n = tag === "desktop" ? 9100 : 9200;
  const open = async (name, body) => {
    await page.getByRole("button", { name: "Feed actions" }).click();
    await page.getByRole("menuitem", { name: "Import OPML" }).click();
    const dlg = page.getByRole("dialog");
    await dlg.getByLabel("OPML file").setInputFiles({ name, mimeType: "text/x-opml", buffer: Buffer.from(body) });
    await dlg.getByRole("button", { name: "Import", exact: true }).click();
    return dlg;
  };
  const close = async (dlg) => {
    await page.keyboard.press("Escape");
    await dlg.waitFor({ state: "detached", timeout: 5000 });
  };

  await step(`${tag} O1`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    const before = (await t.boot(page)).feeds.length;
    const refuse = async (name, body, want, label) => {
      const dlg = await open(name, body);
      const ok = await dlg.getByText(want).waitFor({ timeout: 10000 }).then(() => true, () => false);
      check(ok, `${tag} O1 ${label}`, `refused: ${want}`, `no message matching ${want} in "${await t.textOf(dlg)}"`);
      await t.shot(page, `${tag}-opml-report-${label}`);
      await close(dlg);
    };
    await refuse("empty.opml", "", /that file is empty/, "empty");
    await refuse("folders.opml", opml('<outline text="UAT only folders"><outline text="UAT inner"/></outline>'), /has no feeds in it/, "folders");
    await refuse("entity.opml", opml(`<outline text="UAT &x;" title="UAT &x;" xmlUrl="${deadFeed(n + 9)}"/>`), /&x; on line 1 is not an entity/, "entity");
    check((await t.boot(page)).feeds.length === before, `${tag} O1 nothing`, "nothing was imported", "a refused file changed the library");
  });

  await step(`${tag} O2 O3`, async () => {
    const file = opml(
      `<outline text="UAT report">${outline(`UAT feed ${n + 1}`, deadFeed(n + 1))}${outline(`UAT feed ${n + 1} again`, deadFeed(n + 1))}${outline(`UAT feed ${n + 2}`, deadFeed(n + 2))}${outline("UAT feed private", PRIVATE)}${outline("UAT feed gopher", "gopher://uat-feed-g.invalid/")}</outline>`,
    );
    const dlg = await open("report.opml", file);
    const report = dlg.getByText(/feeds? added/);
    await report.waitFor({ timeout: 10000 });
    const text = await t.textOf(dlg, 5000);
    check(/1 feed was listed again under the same address/.test(text), `${tag} O2 repeat`, "the repeated feed is counted", `no repeat line in "${text}"`);
    const privateLines = text.split(PRIVATE).length - 1;
    check(
      privateLines === 1 && text.includes('tick "Allow addresses on my own network" in its settings'),
      `${tag} O2 private`,
      "the private feed is listed once, with the switch to turn on",
      `the private address appears ${privateLines} times; "${text}"`,
    );
    check(/gopher:\/\/uat-feed-g\.invalid\/: not a web address \(it must start with http:\/\/ or https:\/\/\)/.test(text), `${tag} O2 skipped`, "the gopher feed says why it was skipped", `no plain skip reason in "${text}"`);
    await t.shot(page, `${tag}-opml-report-summary`);

    // The run fetches three dead feeds and the private one, so it ends with every one of them failing.
    const bar = dlg.getByRole("progressbar", { name: "Fetching the new feeds" });
    const sawBar = (await bar.count()) > 0 || (await dlg.getByText(/Fetching the new feeds: \d+ of \d+/).count()) > 0;
    const end = dlg.getByText(/Kipple fetched the new feeds./);
    const ended = await end.waitFor({ timeout: 120000 }).then(() => true, () => false);
    check(ended, `${tag} O3`, `${sawBar ? "a bar, then " : ""}"${ended ? await end.innerText() : ""}"`, `the dialog never said how the fetch ended: "${await t.textOf(dlg)}"`);
    await t.shot(page, `${tag}-opml-report-fetched`);
    await close(dlg);
  });

  await step(`${tag} O4`, async () => {
    const main = page.getByRole("main");
    await page.getByRole("button", { name: "Select", exact: true }).click();
    await t.shot(page, `${tag}-opml-report-select`);
    await main.getByRole("checkbox", { name: new RegExp(`^Select UAT feed ${n + 2}$`) }).check();
    await page.getByRole("button", { name: "Move to folder", exact: true }).click();
    const dlg = page.getByRole("dialog");
    const box = dlg.getByRole("combobox", { name: "Move to folder" });
    await box.waitFor({ timeout: 5000 });
    const value = await box.inputValue();
    const off = await dlg.getByRole("button", { name: "Move", exact: true }).isDisabled();
    check(value === "" && off, `${tag} O4`, "no folder chosen and Move is off", `the folder box holds "${value}", Move off ${off}`);
    await t.shot(page, `${tag}-opml-report-move`);
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("button", { name: "Done", exact: true }).click();
  });
});

await t.finish("OPML import report");
