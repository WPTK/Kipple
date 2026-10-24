// UAT: the feed management screens say what they mean (design review #666 #667 #669 #670 #671 #674).
//
//   npm run seed                       in one terminal (KIPPLE_SEED_SET=design gives a library with a failing feed)
//   npm run uat:feed-repair            in another
//   npm run uat:feed-repair -- --url http://127.0.0.1:7091 --screenshots <dir> --browser webkit --headed
//
// For each width (desktop 1280x800, phone 375x812) it signs in with a fresh browser and checks:
//   R1  Feed health: a one-line summary that doubles as the filter, "Update list" in the header, the first feed within
//       reach on a phone, and no storage figures at the bottom;
//   R2  Feed health: a failing feed's row has Edit address and Try now, the reason in words, and the server's own text
//       only behind Details;
//   R3  Edit feed: grouped into Basics, This feed's schedule and On this device; every inherited choice reads "Same as ..."
//       with its value; Save is "Save" and off until something changes; the off switch is "Pause this feed";
//   R4  Delete feed: the confirmation says how many articles go and that it can't be undone, and the button names it;
//   R5  Add feed: Title and Folder sit under Options; an address on the reader's own network shows its checkbox as it is
//       typed; while Kipple looks, the line says so and the address is read-only.
// It changes nothing on the server: dialogs are closed with Escape and the add is answered by the browser itself. It
// only runs against a loopback address with the seed's credentials unless --allow-remote is given.
// Exit code: 0 clean, 1 findings, 2 setup error.
import { start } from "./common.mjs";

const t = await start(import.meta.url, 20);
const { check, step } = t;

await t.eachViewport(async (page, vp) => {
  const tag = vp.id;
  const writes = [];
  page.on("request", (r) => {
    if (["POST", "PATCH", "PUT", "DELETE"].includes(r.method()) && r.url().includes("/api/") && !r.url().includes("/delete-preview") && new globalThis.URL(r.url()).pathname !== "/api/feeds" && !r.url().includes("/api/stats")) writes.push(`${r.method()} ${new globalThis.URL(r.url()).pathname}`);
  });
  const boot = await (await page.request.get("/api/bootstrap")).json();
  const failing = boot.feeds.find((f) => f.last_error);
  const working = boot.feeds.find((f) => !f.last_error && f.status === "ok");

  await step(`${tag} R1`, async () => {
    await page.goto("/health", { waitUntil: "load" });
    const summary = page.getByTestId("health-summary");
    await summary.waitFor({ timeout: 15000 });
    const text = await t.textOf(summary);
    check(/\d+ feeds? needs? attention/.test(text) && /\d+ working/.test(text), `${tag} R1 summary`, `the summary says "${text}"`, `the summary says "${text}", not how many need attention and how many work`);
    check((await page.getByRole("button", { name: "Update list" }).count()) === 1 && (await page.getByRole("button", { name: "Reload", exact: true }).count()) === 0, `${tag} R1 update`, "Update list replaces Reload", "Reload is still there or Update list is missing");
    const body = await t.textOf(page.getByRole("main").first(), 100000);
    check(!/Database [\d.]+ [KM]?B/.test(body) && !/image cache/.test(body), `${tag} R1 storage`, "no storage figures", "storage figures are still on the page");
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
    if (!failing) return t.fail(`${tag} R2`, "the library has no failing feed (seed it with KIPPLE_SEED_SET=design)");
    await page.goto("/health", { waitUntil: "load" });
    await page.getByRole("button", { name: /^Edit address of/ }).first().waitFor({ timeout: 15000 });
    check((await page.getByRole("button", { name: /^Try .* now$/ }).count()) > 0, `${tag} R2 try`, "Try now is on the row", "no Try now on the row");
    const details = page.locator("details").first();
    const raw = await details.locator("p").first().evaluate((el) => el.textContent ?? "");
    check(raw.length > 0 && !(await details.locator("p").first().isVisible()), `${tag} R2 details`, "the server's text is behind Details", "the server's text is not behind Details");
    await details.locator("summary").click();
    check(await details.locator("p").first().isVisible(), `${tag} R2 open`, "Details opens", "Details did not open");
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
      check(/^Same as Settings \(.+\)$/.test(first ?? ""), `${tag} R3 ${label}`, `${label} reads "${first}"`, `${label} reads "${first}", not "Same as Settings (value)"`);
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

  check(writes.length === 0, `${tag} writes`, "nothing was written to the server", `the flow wrote: ${writes.join(", ")}`);
});

await t.finish("feed-repair");
