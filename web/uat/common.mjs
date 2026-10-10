// Shared set-up for the single-purpose UAT flows (feeds.mjs, states.mjs): options, the loopback guard, sign-in at a
// width, and the checks that read the server's own view of the library. run.mjs and the older flows keep their own copy.
//
// A flow's file starts with `const t = await start(import.meta.url, 16)` (the second argument is how many header
// lines `--help` prints) and ends with `await t.finish("what passed")`.
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium, firefox, webkit } from "@playwright/test";

const ENGINES = { chromium, firefox, webkit };
export const SEP = " › ";
export const VIEWPORTS = [
  { id: "desktop", width: 1280, height: 800, mobile: false },
  { id: "phone", width: 375, height: 812, mobile: true },
];
/** The feed address every disposable feed uses: it never resolves, so nothing is fetched and nothing leaves the machine. */
export const deadFeed = (n) => `http://uat-feed-${n}.invalid/feed.xml`;

export async function start(metaUrl, helpLines) {
  const setupError = (m) => {
    console.error(m);
    process.exit(2);
  };
  let opt;
  try {
    opt = parseArgs({
      options: {
        url: { type: "string", default: process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919" },
        user: { type: "string", default: process.env.KIPPLE_UAT_USER || "dev" },
        // The seed's throwaway local credentials (web/scripts/seed.mjs), not a secret.
        password: { type: "string", default: process.env.KIPPLE_UAT_PASSWORD || "dev-password-only-for-local-testing" },
        browser: { type: "string", default: process.env.KIPPLE_UAT_BROWSER || "chromium" },
        screenshots: { type: "string" },
        headed: { type: "boolean", default: false },
        "allow-remote": { type: "boolean", default: false },
        help: { type: "boolean", default: false },
      },
    }).values;
  } catch (e) {
    return setupError(`bad arguments: ${e.message}`);
  }
  if (opt.help) {
    console.log(readFileSync(fileURLToPath(metaUrl), "utf8").split(/\r?\n/).slice(0, helpLines).map((l) => l.replace(/^\/\/ ?/, "")).join("\n"));
    process.exit(0);
  }
  const origin = new URL(opt.url).origin;
  const loopback = ["127.0.0.1", "[::1]"].includes(new URL(origin).hostname);
  if (!opt["allow-remote"] && (!loopback || opt.user !== "dev" || opt.password !== "dev-password-only-for-local-testing")) {
    setupError("only a loopback --url with the seed's account, unless --allow-remote (point it at a throwaway instance)");
  }
  if (!ENGINES[opt.browser]) setupError(`unknown --browser ${opt.browser} (chromium, firefox or webkit)`);
  if (opt.screenshots) mkdirSync(opt.screenshots, { recursive: true });

  const findings = [];
  const fail = (where, what) => {
    findings.push(`${where}: ${what}`);
    console.error(`FAIL ${where}: ${what}`);
  };
  const pass = (where, what) => console.log(`ok   ${where}: ${what}`);
  const check = (cond, where, ok, bad) => (cond ? pass(where, ok) : fail(where, bad));
  // What the server asks of a write that is not from the app's own fetch: the client header and a same-origin Origin.
  const WRITE = { "X-Kipple-Client": "web", Origin: origin };

  const browser = await ENGINES[opt.browser].launch({ headless: !opt.headed }).catch((e) => setupError(`no browser: ${e.message} (npx playwright install ${opt.browser})`));

  const shot = async (page, name) => {
    if (opt.screenshots) await page.screenshot({ path: join(opt.screenshots, `${name}.png`) }).catch(() => {});
  };
  const boot = (page) => page.request.get("/api/bootstrap", { headers: { Accept: "application/json" } }).then((r) => r.json());
  /** Polls the bootstrap until `fn(b)` is truthy; returns the last bootstrap and whether it held. */
  const settle = async (page, fn, ms = 10000) => {
    const end = Date.now() + ms;
    let b;
    do {
      b = await boot(page);
      if (fn(b)) return { b, ok: true };
      await page.waitForTimeout(250);
    } while (Date.now() < end);
    return { b, ok: false };
  };
  /** Imports an OPML document the way the app does (a multipart upload); returns the result. */
  const importOpml = async (page, xml) => {
    const r = await page.request.post("/api/opml", { headers: WRITE, multipart: { file: { name: "uat.opml", mimeType: "text/x-opml", buffer: Buffer.from(xml) } } });
    if (!r.ok()) throw new Error(`OPML import failed: ${r.status()} ${await r.text()}`);
    return r.json();
  };
  /** An OPML document: `tree` is a list of { folder, feeds: [n...], children: [...] }. */
  const opml = (tree) => {
    const walk = (nodes) =>
      nodes
        .map((n) => `<outline text="${n.folder}">${(n.feeds ?? []).map((i) => `<outline type="rss" text="UAT feed ${i}" title="UAT feed ${i}" xmlUrl="${deadFeed(i)}"/>`).join("")}${walk(n.children ?? [])}</outline>`)
        .join("");
    return `<?xml version="1.0"?><opml version="2.0"><head><title>uat</title></head><body>${walk(tree)}</body></opml>`;
  };
  /** Deletes the disposable library this run made: every feed at a dead address and every folder named "UAT ...". */
  const cleanup = async (page) => {
    const b = await boot(page);
    for (const f of b.feeds.filter((x) => x.url?.includes("uat-feed-") || x.feed_url?.includes("uat-feed-") || x.title?.startsWith("UAT feed "))) {
      await page.request.delete(`/api/feeds/${f.id}`, { headers: WRITE }).catch(() => {});
    }
    for (const f of b.folders.filter((x) => x.name.startsWith("UAT ") && (x.parent_id ?? null) === null)) {
      await page.request.delete(`/api/folders/${f.id}`, { headers: WRITE }).catch(() => {});
    }
  };

  /** Runs `body(page, vp)` once per width, each in a fresh signed-in browser context, then cleans up what it made. */
  const eachViewport = async (body) => {
    for (const vp of VIEWPORTS) {
      const context = await browser.newContext({ baseURL: origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });
      try {
        const page = await context.newPage();
        await page.goto("/", { waitUntil: "load" });
        await page.getByLabel("Username").fill(opt.user);
        await page.getByLabel("Password").fill(opt.password);
        await page.getByRole("button", { name: "Sign in" }).click();
        await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
        await cleanup(page); // what an earlier run that stopped half-way left behind
        await body(page, vp);
      } catch (e) {
        const page = context.pages()[0];
        if (page) await shot(page, `${vp.id}-stopped`);
        fail(vp.id, `the run stopped: ${String(e.message).split("\n").slice(0, 3).join(" ")}`);
      } finally {
        const page = context.pages()[0];
        if (page) await cleanup(page).catch(() => {});
        await context.close();
      }
    }
  };

  return {
    opt, origin, WRITE, check, fail, pass, shot, boot, settle, importOpml, opml, eachViewport, setupError,
    async finish(what) {
      await browser.close();
      console.log(findings.length ? `\n${findings.length} finding(s)` : `\nall ${what} checks passed`);
      process.exit(findings.length ? 1 : 0);
    },
  };
}
