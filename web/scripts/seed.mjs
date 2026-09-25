// Local dev backend with content.
//
//   npm run seed                 build and start Kipple on 127.0.0.1:7080 with a
//                                throwaway data dir, then import a few real feeds
//   npm run seed -- --keep       reuse the data dir from the last run
//   KIPPLE_DEV_DATA=D:\tmp\k npm run seed
//
// Then, in another terminal, `npm run dev` (Vite proxies /api and /img to
// 127.0.0.1:7080) and sign in as dev / dev-password-only-for-local-testing.
// The credentials below are for this throwaway local instance only; nothing
// here is used in production. Needs Go on PATH and network access for the feeds.
import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../", import.meta.url));
const dataDir = process.env.KIPPLE_DEV_DATA || join(tmpdir(), "kipple-dev");
const port = process.env.KIPPLE_DEV_PORT || "7080";
const addr = `127.0.0.1:${port}`;
const keep = process.argv.includes("--keep");

const env = {
  ...process.env,
  KIPPLE_ADDR: addr,
  KIPPLE_DATA: join(dataDir, "data"),
  KIPPLE_USERNAME: "dev",
  KIPPLE_PASSWORD: "dev-password-only-for-local-testing",
  KIPPLE_LOG_LEVEL: "warn",
};

const FEEDS = [
  ["Go Blog", "https://go.dev/blog/feed.atom"],
  ["Hacker News", "https://hnrss.org/frontpage"],
  ["xkcd", "https://xkcd.com/rss.xml"],
  ["Ars Technica", "https://feeds.arstechnica.com/arstechnica/index"],
  ["NASA", "https://www.nasa.gov/feed/"],
  ["BBC World", "https://feeds.bbci.co.uk/news/world/rss.xml"],
];

if (!keep) rmSync(dataDir, { recursive: true, force: true });
mkdirSync(join(dataDir, "data"), { recursive: true });

const bin = join(dataDir, process.platform === "win32" ? "kipple.exe" : "kipple");
console.log("building Kipple ...");
const build = spawnSync("go", ["build", "-o", bin, "./cmd/kipple"], { cwd: root, stdio: "inherit" });
if (build.status !== 0) process.exit(build.status ?? 1);

const opmlPath = join(dataDir, "seed.opml");
const outlines = FEEDS.map(([t, u]) => `    <outline type="rss" text="${t}" title="${t}" xmlUrl="${u}"/>`).join("\n");
writeFileSync(opmlPath, `<?xml version="1.0"?>\n<opml version="2.0"><head><title>Kipple seed</title></head><body>\n  <outline text="Dev seed">\n${outlines}\n  </outline>\n</body></opml>\n`);

const server = spawn(bin, ["serve"], { env, stdio: "inherit" });
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
const imp = spawnSync(bin, ["import", opmlPath], { env, stdio: "inherit" });
if (imp.status !== 0) console.error("import failed; feeds were not added");
console.log(`\nKipple is running at http://${addr}  (sign in as dev / dev-password-only-for-local-testing)`);
console.log("Feeds fetch over the next minute. Run `npm run dev` for the hot-reloading UI. Ctrl-C stops the server.");
