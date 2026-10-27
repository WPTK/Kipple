// UAT: the feed management screens say what they mean (design review #666 #667 #669 #670 #671 #674).
//
//   npm run seed                       in one terminal
//   npm run uat:feed-repair            in another
//   npm run uat:feed-repair -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and checks:
//   R1  Feed health: a one-line summary that doubles as the filter, and "Update list" in the header, within reach at the top;
//   R2  Feed health: a failing feed's row has Edit address and Try now, the reason in words, and the server's own text
//       only behind Details;
//   R3  Edit feed: grouped into Basics, This feed's schedule and On this device; every inherited choice names what it follows
//       with its value; Save is "Save" and off until something changes; the off switch is "Pause this feed";
//   R4  Delete feed: the confirmation says how many articles go and that it can't be undone, and the button names it;
//   R5  Add feed: Title and Folder sit under Options; an address on the reader's own network shows its checkbox as it is
//       typed; while Kipple looks, the line says so and the address is read-only.
// It adds one feed that fails (a small server of its own on 127.0.0.1, answering 500) and removes it afterwards; every
// dialog is closed with Escape and the Add feed submit is answered by the browser itself. It only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { createServer } from "node:http";
import { start } from "./common.mjs";

const t = await start(import.meta.url, 21);
const { check, step } = t;

const TITLE = "UAT repair failing";
const servers = [];

/** A feed server on 127.0.0.1 that answers until `broken` is set, then a 500. */
async function serve() {
  const feed = { broken: false, url: "" };
  const server = createServer((req, res) => {
    if (req.url !== "/feed.xml") return res.writeHead(404).end();
    if (feed.broken) return res.writeHead(500, { "Content-Type": "text/plain" }).end("broken on purpose");
    res.writeHead(200, { "Content-Type": "application/rss+xml" }).end('<?xml version="1.0"?><rss version="2.0"><channel><title>UAT repair</title><link>http://127.0.0.1/</link><description>t</description><item><title>An article</title><link>http://127.0.0.1/a</link><guid>uat-repair-1</guid><description>words</description></item></channel></rss>');
  });
  await new Promise((resolve, reject) => server.once("error", reject).listen(0, "127.0.0.1", resolve));
  servers.push(server);
  feed.url = `http://127.0.0.1:${server.address().port}/feed.xml`;
  return feed;
}

/** Adds the feed, then breaks it and asks for refreshes until it records a failed fetch. Returns its id or null. */
async function addFailing(page) {
  const srv = await serve();
  const add = await page.request.post("/api/feeds", { headers: t.WRITE, data: { url: srv.url, title: TITLE, allow_private_net: true } });
  if (!add.ok()) return null;
  const id = (await add.json()).feed?.id;
  srv.broken = true;
  for (let i = 0; id && i < 3; i++) {
    await page.request.post("/api/refresh", { headers: t.WRITE, data: {} });
    const s = await t.settle(page, (b) => b.feeds.some((f) => f.id === id && f.last_error), i === 0 ? 30000 : 12000);
    if (s.b.feeds.some((f) => f.id === id && f.last_error)) return id;
  }
  return id ?? null;
}

await t.eachViewport(async (page, vp) => {
  let id = null;
  try {
    id = await addFailing(page);
    await viewport(page, vp, id);
  } finally {
    if (id) await page.request.delete(`/api/feeds/${id}`, { headers: t.WRITE }).catch(() => {});
  }
});
for (const s of servers) s.close();

async function viewport(page, vp, failingId) {
  const tag = vp.id;
  const writes = [];
  page.on("request", (r) => {
    if (["POST", "PATCH", "PUT", "DELETE"].includes(r.method()) && r.url().includes("/api/") && !r.url().includes("/delete-preview") && new globalThis.URL(r.url()).pathname !== "/api/feeds" && !r.url().includes("/api/stats")) writes.push(`${r.method()} ${new globalThis.URL(r.url()).pathname}`);
  });
  const boot = await (await page.request.get("/api/bootstrap")).json();
  const failing = boot.feeds.find((f) => f.id === failingId && f.last_error);
  const working = boot.feeds.find((f) => !f.last_error && f.status === "ok" && f.id !== failingId);

  await step(`${tag} R1`, async () => {
    await page.goto("/health", { waitUntil: "load" });
    const summary = page.getByTestId("health-summary");
    await summary.waitFor({ timeout: 15000 });
    const text = await t.textOf(summary);
    check(/\d+ feeds? needs? attention/.test(text) && /\d+ working/.test(text), `${tag} R1 summary`, `the summary says "${text}"`, `the summary says "${text}", not how many need attention and how many work`);
    check((await page.getByRole("button", { name: "Update list" }).count()) === 1 && (await page.getByRole("button", { name: "Reload", exact: true }).count()) === 0, `${tag} R1 update`, "Update list replaces Reload", "Reload is still there or Update list is missing");
    const y = (await summary.boundingBox())?.y ?? 9999;
    check(y < 200, `${tag} R1 reach`, `the summary starts at ${Math.round(y)}px`, `the summary starts ${Math.round(y)}px down, too far for the first feed to be in reach`);
    await t.shot(page, `${tag}-health-summary`);
    const attention = summary.getByRole("button", { name: /need.* attention/ });
    if (await attention.count()) {
      await attention.click();
      check((await attention.getAttribute("aria-pressed")) === "true", `${tag} R1 filter`, "the count filters to the feeds that need attention", "the count did not filter");
      await t.shot(page, `${tag}-health-filtered`);
      await summary.getByRole("button", { name: "Show all" }).click();
    }
  });

  await step(`${tag} R2`, async () => {
    if (!failing) return t.fail(`${tag} R2`, "the added feed never recorded a failed fetch");
    await page.goto("/health", { waitUntil: "load" });
    await page.getByRole("button", { name: /^Edit address of/ }).first().waitFor({ timeout: 15000 });
    check((await page.getByRole("button", { name: /^Try .* now$/ }).count()) > 0, `${tag} R2 try`, "Try now is on the row", "no Try now on the row");
    const details = page.getByRole("button", { name: "Details", exact: true }).first();
    await details.waitFor({ timeout: 15000 });
    const panelId = await details.getAttribute("aria-controls");
    const panel = page.locator(`[id="${panelId}"]`);
    check((await details.getAttribute("aria-expanded")) === "false" && (await panel.count()) === 0, `${tag} R2 details`, "the server's text is behind Details", "the server's text is not behind Details");
    await details.click();
    await panel.waitFor({ timeout: 5000 });
    const raw = (await panel.textContent()) ?? "";
    check(raw.length > 0 && (await panel.isVisible()), `${tag} R2 open`, "Details opens", "Details did not open");
    await t.shot(page, `${tag}-health-failing-row`);
    await page.getByRole("button", { name: /^Edit address of/ }).first().click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    await dlg.getByTestId("feed-reason").waitFor({ timeout: 10000 });
    const focused = await dlg.getByLabel("Feed address").evaluate((el) => el === el.ownerDocument.activeElement);
    check(focused, `${tag} R2 focus`, "Edit feed opens on the address field", "the address field is not focused");
    await t.shot(page, `${tag}-edit-failing-feed`);
    await page.keyboard.press("Escape");
  });

  await step(`${tag} R3`, async () => {
    if (!working) return t.fail(`${tag} R3`, "the library has no working feed");
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.getByRole("button", { name: new RegExp(`^Edit ${working.title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) }).first().click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    await dlg.getByLabel("Feed address").waitFor({ timeout: 10000 });
    for (const name of ["Basics", "This feed's schedule", "On this device"]) {
      check((await dlg.getByRole("region", { name }).count()) === 1, `${tag} R3 ${name}`, `the ${name} group is there`, `no ${name} group`);
    }
    const save = dlg.getByRole("button", { name: "Save", exact: true });
    check(await save.isDisabled(), `${tag} R3 save off`, "Save is off until something changes", "Save is on with nothing changed");
    for (const label of ["Check for new articles", "Articles to keep", "Mark as read after"]) {
      const first = await dlg.getByLabel(label).locator("option").first().textContent();
      check(/^Same as Settings \(.+\)$/.test(first ?? ""), `${tag} R3 ${label}`, `${label} reads "${first}"`, `${label} reads "${first}", not "Same as Settings (value)"`); // uat-labels: ignore
    }
    check((await dlg.getByRole("checkbox", { name: /^Pause this feed/ }).count()) === 1, `${tag} R3 pause`, "Pause this feed", "no Pause this feed");
    await dlg.getByLabel("Title").fill("A changed title");
    check(await save.isEnabled(), `${tag} R3 save on`, "Save turns on after a change", "Save stayed off after a change");
    await t.shot(page, `${tag}-edit-feed`);
    await dlg.getByLabel("Title").fill("");
    await dlg.getByLabel("Title").press("Escape");
  });

  await step(`${tag} R4`, async () => {
    if (!working) return t.fail(`${tag} R4`, "the library has no working feed");
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await page.getByRole("button", { name: new RegExp(`^Edit ${working.title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) }).first().click();
    const dlg = page.getByRole("dialog", { name: "Edit feed" });
    await dlg.getByRole("button", { name: "Delete feed" }).click();
    const del = page.getByRole("dialog", { name: /^Delete / });
    const button = del.getByRole("button", { name: /^Delete feed( and \d+ articles?)?$/ });
    await button.waitFor({ timeout: 10000 });
    await page.waitForFunction(() => /Removes \d+ articles?\./.test(globalThis.document.querySelector("[role=dialog]")?.textContent ?? "") || /Removes no articles\./.test(globalThis.document.querySelector("[role=dialog]")?.textContent ?? ""), null, { timeout: 10000 }).catch(() => {});
    const text = await t.textOf(del, 600);
    check(/Removes (\d+ articles?|no articles)\. This can't be undone\./.test(text) && !/sync apps/.test(text), `${tag} R4 words`, `the dialog says "${text.slice(0, 160)}"`, `the dialog says "${text.slice(0, 160)}", without the count, "can't be undone" or with "sync apps"`);
    const label = (await button.textContent()) ?? "";
    check(/^Delete feed and \d+ articles?$/.test(label) || /^Delete feed$/.test(label), `${tag} R4 button`, `the button reads "${label}"`, `the button reads "${label}"`);
    await t.shot(page, `${tag}-delete-feed`);
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
  });

  await step(`${tag} R5`, async () => {
    await page.goto("/feeds", { waitUntil: "load" });
    await page.getByRole("button", { name: "Add feed" }).first().click();
    const dlg = page.getByRole("dialog", { name: "Add feed" });
    const address = dlg.getByLabel("Feed or website address");
    await address.waitFor({ timeout: 10000 });
    check((await dlg.getByLabel("Title (optional)").count()) === 0, `${tag} R5 options closed`, "Title is under Options", "Title is shown before Options is opened");
    check((await address.getAttribute("placeholder"))?.startsWith("https://example.com/") === true, `${tag} R5 example`, "the address has an example", "the address has no example");
    check((await dlg.getByRole("checkbox", { name: /own network/ }).count()) === 0, `${tag} R5 no private`, "no own-network option for a public address", "the own-network option is shown early");
    await address.fill("http://192.168.1.50/feed.xml");
    await dlg.getByRole("checkbox", { name: /own network/ }).waitFor({ timeout: 5000 });
    check(true, `${tag} R5 private`, "the own-network option appears as the address is typed", "");
    await t.shot(page, `${tag}-add-private`);
    await address.fill("https://example.com/feed.xml");
    check((await dlg.getByRole("checkbox", { name: /own network/ }).count()) === 0, `${tag} R5 public`, "the option goes when the address is public", "the option stays for a public address");
    // The add is answered here, after a pause, so the working state can be seen; nothing reaches the server.
    await page.route("**/api/feeds", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      await new Promise((r) => globalThis.setTimeout(r, 2500));
      return route.fulfill({ status: 422, contentType: "application/json", body: JSON.stringify({ error: "no_feed", message: "Kipple found a web page at that address, but the page does not link to a feed." }) });
    });
    await dlg.getByRole("button", { name: "Add feed" }).click();
    const looking = dlg.getByText(/^Looking for a feed at example\.com/);
    check(await looking.waitFor({ timeout: 3000 }).then(() => true, () => false), `${tag} R5 looking`, "a line says Kipple is looking", "no line says Kipple is looking");
    check(await address.evaluate((el) => el.readOnly), `${tag} R5 readonly`, "the address is read-only while it looks", "the address is still editable while it looks");
    await t.shot(page, `${tag}-add-looking`);
    await dlg.getByText(/does not link to a feed/).waitFor({ timeout: 8000 });
    await page.unroute("**/api/feeds");
    await dlg.getByRole("button", { name: "Options" }).click();
    check((await dlg.getByLabel("Title (optional)").count()) === 1 && (await dlg.getByLabel("Folder").count()) === 1, `${tag} R5 options`, "Options holds Title and Folder", "Options is missing Title or Folder");
    await page.keyboard.press("Escape");
  });

  check(writes.length === 0, `${tag} writes`, "the screens wrote nothing to the server", `the screens wrote: ${writes.join(", ")}`);
}

await t.finish("feed-repair");
