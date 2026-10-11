// A tiny local feed server, so a throwaway Kipple can be seeded with no outside network (CI, offline work).
//
//   node scripts/fixture-feeds.mjs [--port 1994]       serves http://127.0.0.1:1994/feed/<name>.xml
//   KIPPLE_SEED_FEEDS_URL=http://127.0.0.1:1994 npm run seed      imports those feeds instead of the public ones
//
// Five RSS feeds with a dozen dated articles each (newest first, dated from now, so nothing is old). Loopback only,
// and read-only: it never writes anything.
import { createServer } from "node:http";
import { parseArgs } from "node:util";

export const FIXTURE_FEEDS = [
  ["Go Blog", "go-blog"],
  ["Hacker News", "news"],
  ["xkcd", "comics"],
  ["Ars Technica", "tech"],
  ["NASA", "space"],
];
const TOPICS = ["compilers", "gardens", "satellites", "maps", "bridges", "weather", "type systems", "rivers", "clocks", "libraries", "engines", "harbors"];

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");
function feed(name, title, base) {
  const now = Date.now();
  const items = TOPICS.map((topic, i) => {
    const when = new Date(now - (i * 3 + 1) * 3600_000).toUTCString();
    const t = `The story of ${topic} in ${title}`;
    return `<item><title>${esc(t)}</title><link>${base}/${name}/${i}</link><guid isPermaLink="false">${name}-${i}</guid><pubDate>${when}</pubDate><description>${esc(`<p>The long read about ${topic}, from ${title}. A paragraph of text so the article has a body, and a second sentence about the same thing.</p>`)}</description></item>`;
  }).join("");
  return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>${esc(title)}</title><link>${base}/${name}</link><description>${esc(title)} (test feed)</description>${items}</channel></rss>`;
}

export function serve(port) {
  const server = createServer((req, res) => {
    const m = /^\/feed\/([a-z-]+)\.xml$/.exec(req.url ?? "");
    const f = m && FIXTURE_FEEDS.find(([, n]) => n === m[1]);
    if (!f) {
      res.writeHead(404).end("not found");
      return;
    }
    res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(feed(f[1], f[0], `http://127.0.0.1:${port}/page`));
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", () => resolve(server));
  });
}

if (process.argv[1]?.endsWith("fixture-feeds.mjs")) {
  const { values } = parseArgs({ options: { port: { type: "string", default: "1994" } } });
  await serve(Number(values.port));
  console.log(`fixture feeds at http://127.0.0.1:${values.port}/feed/<name>.xml (${FIXTURE_FEEDS.map(([, n]) => n).join(", ")})`);
}
