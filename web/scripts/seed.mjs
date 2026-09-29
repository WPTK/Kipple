// Local dev backend with content.
//
//   npm run seed                 build and start Kipple on 127.0.0.1:7080 with a
//                                throwaway data dir, then import a few real feeds
//   npm run seed -- --keep       reuse the data dir from the last run
//   KIPPLE_DEV_DATA=D:\tmp\k npm run seed
//                                a directory this script did not create (no
//                                .kipple-dev-seed file) and that is not empty is
//                                never deleted unless you add -- --force
//
//   KIPPLE_SEED_SET=fresh npm run seed
//                                a server with NO account and no feeds, so it starts in setup mode: the setup wizard
//                                (docs/setup-wizard-design.md). Its stderr, with the setup code in the banner, is also
//                                written to <data dir>/server-stderr.log. Nothing is imported.
//
// Then, in another terminal, `npm run dev` (Vite proxies /api and /img to
// 127.0.0.1:7080) and sign in as dev / dev-password-only-for-local-testing.
// The credentials below are for this throwaway local instance only; nothing
// here is used in production. Needs Go on PATH and network access for the feeds.
import { spawn, spawnSync } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, parse, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../", import.meta.url));
const dataDir = process.env.KIPPLE_DEV_DATA || join(tmpdir(), "kipple-dev");
const port = process.env.KIPPLE_DEV_PORT || "7080";
const addr = `127.0.0.1:${port}`;
const keep = process.argv.includes("--keep");
const force = process.argv.includes("--force");
const SENTINEL = ".kipple-dev-seed";

// KIPPLE_SEED_SET=fresh: no account (so Kipple starts in setup mode) and no import.
const fresh = process.env.KIPPLE_SEED_SET === "fresh";

const env = {
  ...process.env,
  KIPPLE_ADDR: addr,
  KIPPLE_DATA: join(dataDir, "data"),
  KIPPLE_USERNAME: "dev",
  KIPPLE_PASSWORD: "dev-password-only-for-local-testing",
  KIPPLE_LOG_LEVEL: "warn",
};
if (fresh) {
  delete env.KIPPLE_USERNAME;
  delete env.KIPPLE_PASSWORD;
  delete env.KIPPLE_API_PASSWORD;
}

const FEEDS = [
  ["Go Blog", "https://go.dev/blog/feed.atom"],
  ["Hacker News", "https://hnrss.org/frontpage"],
  ["xkcd", "https://xkcd.com/rss.xml"],
  ["Ars Technica", "https://feeds.arstechnica.com/arstechnica/index"],
  ["NASA", "https://www.nasa.gov/feed/"],
  ["BBC World", "https://feeds.bbci.co.uk/news/world/rss.xml"],
];

// KIPPLE_SEED_SET=site seeds the picture-forward set the kipple.cc screenshots are taken from (scripts/site-shots.mjs),
// in a folder called "Less noise", instead of the default developer set.
const SITE_FEEDS = [
  ["Wikimedia Picture of the Day", "https://commons.wikimedia.org/w/api.php?action=featuredfeed&feed=potd&feedformat=atom"],
  ["Wikipedia Featured Article", "https://en.wikipedia.org/w/api.php?action=featuredfeed&feed=featured&feedformat=atom"],
  ["NASA Image of the Day", "https://www.nasa.gov/feeds/iotd-feed/"],
  ["Go Blog", "https://go.dev/blog/feed.atom"],
  ["Hacker News", "https://hnrss.org/frontpage"],
];
const site = process.env.KIPPLE_SEED_SET === "site";
const SEED = site ? { folder: "Less noise", feeds: SITE_FEEDS } : { folder: "Dev seed", feeds: FEEDS };

// The data dir is wiped on every run without --keep, so only ever delete a directory this script
// created (it leaves a sentinel file; older runs are recognised by their seed.opml layout) or an
// empty one. Anything else needs --force, and even then
// never a filesystem root, the home directory or the temp directory itself.
function wipeDataDir() {
  if (!existsSync(dataDir)) return;
  const resolved = resolve(dataDir);
  const forbidden = [parse(resolved).root, resolve(homedir()), resolve(tmpdir())].map((p) => p.toLowerCase());
  if (forbidden.includes(resolved.toLowerCase())) {
    console.error(`refusing to delete ${resolved}: set KIPPLE_DEV_DATA to a dedicated directory`);
    process.exit(1);
  }
  const entries = readdirSync(dataDir);
  const empty = entries.length === 0;
  // Runs from before the sentinel existed left seed.opml plus only these entries.
  const legacy =
    entries.includes("seed.opml") && entries.every((e) => ["data", "kipple", "kipple.exe", "seed.opml"].includes(e));
  const ours = entries.includes(SENTINEL);
  if (!ours && !empty && !legacy && !force) {
    console.error(
      `refusing to delete ${resolved}: it was not created by this script (no ${SENTINEL}).\n` +
        "Point KIPPLE_DEV_DATA at a new directory, run with --keep, or pass --force to delete it anyway.",
    );
    process.exit(1);
  }
  rmSync(dataDir, { recursive: true, force: true });
}

if (!keep) wipeDataDir();
mkdirSync(join(dataDir, "data"), { recursive: true });
writeFileSync(join(dataDir, SENTINEL), "Created by web/scripts/seed.mjs; deleted and recreated on each run without --keep.\n");

const bin = join(dataDir, process.platform === "win32" ? "kipple.exe" : "kipple");
console.log("building Kipple ...");
const build = spawnSync("go", ["build", "-o", bin, "./cmd/kipple"], { cwd: root, stdio: "inherit" });
if (build.status !== 0) process.exit(build.status ?? 1);

const opmlPath = join(dataDir, "seed.opml");
const outlines = SEED.feeds.map(([t, u]) => `    <outline type="rss" text="${t}" title="${t}" xmlUrl="${u}"/>`).join("\n");
writeFileSync(opmlPath, `<?xml version="1.0"?>\n<opml version="2.0"><head><title>Kipple seed</title></head><body>\n  <outline text="${SEED.folder}">\n${outlines}\n  </outline>\n</body></opml>\n`);

const server = spawn(bin, ["serve"], { env, stdio: fresh ? ["ignore", "inherit", "pipe"] : "inherit" });
if (fresh) {
  // The setup code is printed once, to stderr: keep a copy where a script (web/uat/wizard.mjs) or a person can read it.
  const log = createWriteStream(join(dataDir, "server-stderr.log"));
  server.stderr.on("data", (chunk) => {
    process.stderr.write(chunk);
    log.write(chunk);
  });
}
const stop = () => {
  server.kill();
  process.exit(0);
};
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
server.on("exit", (code) => process.exit(code ?? 0));

async function waitReady() {
  for (let i = 0; i < 100; i++) {
    try {
      const r = await fetch(`http://${addr}/healthz`);
      if (r.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error("server did not become ready");
}

await waitReady();
if (fresh) {
  console.log(`\nKipple is running at http://${addr} in SETUP MODE (no account). The setup code is in ${join(dataDir, "server-stderr.log")}.`);
  console.log("Open the address, or the #setup= link from the banner. Ctrl-C stops the server.");
} else {
  const imp = spawnSync(bin, ["import", opmlPath], { env, stdio: "inherit" });
  if (imp.status !== 0) console.error("import failed; feeds were not added");
  console.log(`\nKipple is running at http://${addr}  (sign in as dev / dev-password-only-for-local-testing)`);
  console.log("Feeds fetch over the next minute. Run `npm run dev` for the hot-reloading UI. Ctrl-C stops the server.");
}
