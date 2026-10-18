// Shared set-up for the single-purpose UAT flows (feeds.mjs, states.mjs): options, the loopback guard, sign-in at a
// width, and the checks that read the server's own view of the library. run.mjs and the older flows keep their own copy.
//
// A flow's file starts with `const t = await start(import.meta.url, 16)` (the second argument is how many header
// lines `--help` prints) and ends with `await t.finish("what passed")`.
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { chromium, firefox, request, webkit } from "@playwright/test";
import { faultReason, faultStart } from "./faults.mjs";

const ENGINES = { chromium, firefox, webkit };
/** The session cookie the server sets at sign-in (internal/api). */
const SESSION_COOKIE = "kipple_session";
export const SEP = " › ";
export const VIEWPORTS = [
  { id: "desktop", width: 1280, height: 800, mobile: false },
  { id: "phone", width: 375, height: 812, mobile: true },
];
/** The feed address every disposable feed uses: it never resolves, so nothing is fetched and nothing leaves the machine. */
export const deadFeed = (n) => `http://uat-feed-${n}.invalid/feed.xml`;

/**
 * The URL of WebKit's "Fetch API cannot load <url> due to access control checks." console error, as Playwright hands it
 * to `pageerror`, or null for any other error. Playwright splits a WebKit console error at its first colon into a name
 * and a message (dropping the two characters after the colon), so the text arrives as "Fetch API cannot load http:
 * /host/path ..." and the scheme is put back together here.
 */
export function accessControlUrl(err) {
  const m = /^Fetch API cannot load (https?):\s*\/*(\S+) due to access control checks\.$/.exec(String(err));
  if (!m) return null;
  try {
    return new URL(`${m[1]}://${m[2]}`);
  } catch {
    return null;
  }
}

/**
 * The read-only URLs the app legitimately has in flight when the main frame navigates away, so WebKit may refuse them
 * as the page is torn down (issue #370). Anything else, and above all a write the old page starts on pagehide (the
 * theme save, the stats keepalive), still counts: WebKit refusing one of those is a lost write, not noise.
 *   /api/saved-searches: the sidebar's saved-search list, fetched on mount and on focus; the only URL seen refused in
 *   the observed runs.
 */
export const LEAVING_READS = ["/api/saved-searches"];

/**
 * Returns `(err) => boolean`: whether a `pageerror` is WebKit refusing a same-origin fetch of one of LEAVING_READS that
 * the page being left started while the main frame was navigating away (issue #370). Measured with
 * `web/uat/measure-unload-refusal.mjs`: the error arrives after the main frame's navigation request and before the new
 * document commits, WebKit reports no request at all for it, and it does not come with a `requestfailed` for the same
 * URL; it is the other outcome of the same race in which WebKit reports that request as `requestfailed: Load request
 * cancelled`, which the flows already do not count. So the pairing is the navigation in progress, not a request
 * event, and the message text alone never decides: outside a navigation, for another origin, another path, or any
 * other text, the error counts. A rejection the page left unhandled is a separate "Unhandled Promise Rejection: ..."
 * error that WebKit adds, and it always counts.
 *
 * The window closes on the commit (framenavigated), a failed navigation (requestfailed), or a main-frame response
 * that ends without a commit (204, 205 or a download).
 */
export function unloadRefusal(page, origin) {
  let navigating = false;
  const main = (r) => r.isNavigationRequest() && r.frame() === page.mainFrame();
  page.on("request", (r) => {
    if (main(r)) navigating = true;
  });
  page.on("requestfailed", (r) => {
    if (main(r)) navigating = false;
  });
  page.on("response", (res) => {
    const r = res.request();
    if (!main(r)) return;
    const s = res.status();
    if (s === 204 || s === 205 || res.headers()["content-disposition"]?.toLowerCase().startsWith("attachment")) navigating = false;
  });
  page.on("framenavigated", (f) => {
    if (f === page.mainFrame()) navigating = false;
  });
  return (err) => {
    const u = navigating ? accessControlUrl(err) : null;
    return !!u && u.origin === origin && LEAVING_READS.includes(u.pathname);
  };
}

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

  /** `{ text, jar }`: `jar` is filled in by `attempt` (see faults.mjs). */
  const findings = [];
  /** The widths that ran again after a browser fault, and why: printed by `finish`, even on a green run. */
  const reruns = [];
  /** The signed-in context of the attempt running now: each finding notes whether it still held its session cookie. */
  let watching = null;
  /**
   * Records a finding. The browser is asked for its cookies in the same call, before the flow sends any later command
   * (Playwright sends a context's commands in order), so the answer is the jar as it was when the finding was recorded,
   * within one round trip. Returns that answer; `step` and `attempt` wait for it. A real finding the browser happens to
   * lose its cookie right after is set aside, but the rerun runs that check again, so a real failure comes back.
   */
  const fail = (where, what) => {
    const w = watching;
    const jar = w ? w.context.cookies(origin).then((cs) => cs.some((c) => c.name === SESSION_COOKIE && c.value === w.session.value), () => false) : null;
    findings.push({ text: `${where}: ${what}`, jar });
    console.error(`FAIL ${where}: ${what}`);
    return jar;
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
  /**
   * Adds a feed the flow serves on loopback (`title` should start "UAT feed " so cleanup removes it), refreshes, and
   * polls its articles until `ready(items)` holds (30 s). Returns `{ feedId, items }`, or `{ error }` saying what failed.
   */
  const addLocalFeed = async (page, url, title, ready) => {
    const add = await page.request.post("/api/feeds", { headers: WRITE, data: { url, title, allow_private_net: true } });
    if (!add.ok()) return { error: `the feed was not added: ${add.status()} ${await add.text()}` };
    const feedId = (await add.json()).id ?? (await boot(page)).feeds.find((f) => f.title === title)?.id;
    await page.request.post("/api/refresh", { headers: WRITE, data: {} });
    let items = [];
    const end = Date.now() + 30000;
    while (Date.now() < end) {
      const r = await page.request.get(`/api/items?view=all&feed=${feedId}`, { headers: { Accept: "application/json" } });
      items = r.ok() ? ((await r.json()).items ?? []) : [];
      if (ready(items)) return { feedId, items };
      await page.waitForTimeout(500);
    }
    return { error: "the feed's articles never arrived (was it fetched?)", items };
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

  /**
   * Whether the server still accepts `session`, asked without the browser. `error` when it could not be asked: the
   * caller records that as a finding after deciding what to set aside, so it is never set aside itself.
   */
  const serverAccepts = async (session) => {
    let probe;
    try {
      probe = await request.newContext({ baseURL: origin, extraHTTPHeaders: { Cookie: `${SESSION_COOKIE}=${session.value}`, Accept: "application/json" } });
      return { ok: (await probe.get("/api/bootstrap")).ok() };
    } catch (e) {
      return { ok: false, error: `could not ask the server whether the session it issued is still valid: ${String(e.message).split("\n")[0]}` };
    } finally {
      await probe?.dispose().catch(() => {});
    }
  };

  /**
   * One signed-in run of `body` at a width, in a fresh browser context. Returns `fault`: where a browser fault began
   * among the findings it added (faults.mjs: WebKit's internal error, or the session cookie gone from the browser while
   * the server still accepts it) and why, or null; and `probeError` when the server could not be asked.
   */
  const attempt = async (body, vp) => {
    const context = await browser.newContext({ baseURL: origin, viewport: { width: vp.width, height: vp.height }, isMobile: vp.mobile, hasTouch: vp.mobile });
    const mark = findings.length;
    let session = null;
    try {
      const page = await context.newPage();
      await page.goto("/", { waitUntil: "load" });
      await page.getByLabel("Username").fill(opt.user);
      await page.getByLabel("Password").fill(opt.password);
      await page.getByRole("button", { name: "Sign in" }).click();
      await page.getByRole("navigation", { name: "Primary" }).first().waitFor({ timeout: 15000 });
      session = (await context.cookies(origin)).find((c) => c.name === SESSION_COOKIE) ?? null;
      if (session) watching = { context, session };
      await cleanup(page); // what an earlier run that stopped half-way left behind
      await body(page, vp);
    } catch (e) {
      const page = context.pages()[0];
      if (page) await shot(page, `${vp.id}-stopped`);
      await fail(vp.id, `the run stopped: ${String(e.message).split("\n").slice(0, 3).join(" ")}`);
    }
    watching = null;
    try {
      const found = findings.slice(mark);
      for (const f of found) f.jar = await f.jar;
      const asked = session && found.some((f) => f.jar === false) ? await serverAccepts(session) : { ok: false };
      const start = faultStart(found, asked.ok);
      return { fault: start < 0 ? null : { start, reason: faultReason(found[start]) }, probeError: asked.error };
    } finally {
      const page = context.pages()[0];
      if (page) await cleanup(page).catch(() => {});
      await context.close();
    }
  };

  /**
   * Runs `body(page, vp)` once per width, each in a fresh signed-in browser context, then cleans up what it made. When
   * the browser failed under an attempt, the findings from the fault on are set aside (those before it stay: the browser
   * still worked) and the width runs again in a fresh context, once: every check runs again and must pass, and the
   * browser failing a second time is a finding. Each rerun is counted and printed by `finish`, also on a green run.
   */
  const eachViewport = async (body) => {
    for (const vp of VIEWPORTS) {
      const mark = findings.length;
      const { fault, probeError } = await attempt(body, vp);
      if (!fault) {
        if (probeError) fail(vp.id, probeError);
        continue;
      }
      const kept = findings.slice(mark, mark + fault.start).map((f) => f.text);
      console.log(
        `note ${vp.id}: ${fault.reason}; the ${findings.length - mark - fault.start} finding(s) from then on are set aside` +
          `${kept.length ? ` (the ${kept.length} before it stay)` : ""} and ${vp.id} runs again in a fresh browser context`,
      );
      findings.length = mark + fault.start;
      if (probeError) fail(vp.id, probeError);
      reruns.push(`${vp.id}: ${fault.reason}`);
      const keep = findings.length;
      const again = await attempt(body, vp);
      // A finding kept from the first attempt that the rerun finds again is still one finding.
      const rerun = findings.splice(keep).filter((f) => !kept.includes(f.text));
      findings.push(...rerun);
      if (again.probeError) fail(vp.id, again.probeError);
      if (again.fault) fail(vp.id, `the browser failed under the run again: ${again.fault.reason}`);
    }
  };

  /** A visible text of a locator for a failure message, never throwing: the thing may not be there at all. */
  const textOf = (loc, max = 200) => loc.innerText({ timeout: 2000 }).then((s) => s.replace(/\s+/g, " ").slice(0, max), () => "(nothing there)");
  /** Runs one section of a flow; if it throws, that is a finding and the next section still runs. */
  const step = async (name, fn) => {
    try {
      await fn();
    } catch (e) {
      await fail(name, `stopped: ${String(e.message).split("\n").slice(0, 2).join(" ")}`);
    }
  };

  return {
    textOf, step, opt, origin, WRITE, check, fail, pass, shot, boot, settle, importOpml, addLocalFeed, opml, eachViewport, setupError,
    async finish(what) {
      await browser.close();
      if (reruns.length) {
        const said = `${reruns.length} width(s) ran again after a suspected browser fault: ${reruns.join("; ")}`;
        console.log(`\n${said}`);
        // An annotation on the workflow run, so a green run that needed a rerun is still seen and counted.
        if (process.env.GITHUB_ACTIONS === "true") console.log(`::warning title=Browser UAT rerun (${what})::${said}`);
      }
      console.log(findings.length ? `\n${findings.length} finding(s)` : `\nall ${what} checks passed`);
      process.exit(findings.length ? 1 : 0);
    },
  };
}
