// The design review's library: feeds built to stress how Kipple looks, served offline by scripts/fixture-feeds.mjs
// under /design/. Everything is invented, dates are relative to now, and the pictures are generated PNGs, so the set is
// the same on every machine and needs no outside network.
//
//   KIPPLE_SEED_SET=design npm run seed     starts the feed server itself and seeds this set (see seed.mjs)
//
// What it stresses: a feed with hundreds of unread articles, titles from one word to three lines, a long feed name,
// pictures in landscape, portrait, panorama, square and too small to lead, articles with no picture or no body, long
// articles with every block type, Unicode and right-to-left text, three nested folders, an empty folder, an empty feed,
// a quiet feed, an unfiled feed and a feed whose address fails. seed.mjs then stars, reads and mutes some articles and
// saves a search, so those views have content too.
import { Buffer } from "node:buffer";
import { crc32, deflateSync } from "node:zlib";

const HOUR = 3600_000;
const WORDS = "harbor lantern meadow orchard ridge summit thistle valley willow cedar ember fjord grove island juniper kettle pebble quartz".split(" ");
const TOPICS = ["bridges", "gardens", "satellites", "maps", "weather", "rivers", "clocks", "libraries", "engines", "harbors", "trains", "lighthouses"];

/** A title of about `n` words, the same for the same `i`. */
function title(i, n) {
  const w = [];
  for (let k = 0; k < n; k++) w.push(WORDS[(i * 7 + k * 3) % WORDS.length]);
  const s = w.join(" ");
  return s[0].toUpperCase() + s.slice(1);
}
const para = (i, topic) =>
  `<p>The ${topic} story continues: a paragraph long enough to fill a preview line and then some, about ${WORDS[i % WORDS.length]} and ${WORDS[(i + 5) % WORDS.length]}, with a second sentence so the summary has to be cut somewhere sensible.</p>`;
const img = (w, h, seed, alt = "") => `<p><img src="{base}/design/img/${w}x${h}-${seed}.png" width="${w}" height="${h}" alt="${alt}"></p>`;

/** Varied title lengths: mostly 4 to 9 words, every 9th a single word, every 11th long enough to wrap three lines. */
const titleLen = (i) => (i % 9 === 4 ? 1 : i % 11 === 6 ? 24 : 4 + ((i * 5) % 6));

const LONG_BODY = [
  "<p>This article has every block an article can have, so the reading view shows how each one is set.</p>",
  "<h2>A second-level heading</h2>",
  para(1, "bridges"),
  "<h3>A third-level heading</h3>",
  "<blockquote><p>A quotation that runs over two lines at most widths, so the indent and the rule beside it show.</p></blockquote>",
  "<ul><li>An item in a list</li><li>A second item, long enough to wrap onto another line at a phone width so the hanging indent shows</li><li>A third</li></ul>",
  "<ol><li>First step</li><li>Second step</li><li>Third step</li></ol>",
  "<pre><code>func main() {\n\tfmt.Println(\"a line of code wide enough to need sideways scrolling on a phone screen, which it should do\")\n}</code></pre>",
  "<p>Inline <code>code</code>, <strong>bold</strong>, <em>italic</em>, a <a href=\"https://example.com/\">link</a>, and an address with no spaces: https://example.com/a/very/long/path/that/never/breaks/naturally/because/it/has/no/spaces/at/all.</p>",
  "<table><thead><tr><th>River</th><th>Length</th><th>Bridges</th><th>Notes</th></tr></thead><tbody><tr><td>Long one</td><td>6,650 km</td><td>41</td><td>A cell with more text than the others</td></tr><tr><td>Short one</td><td>12 km</td><td>3</td><td>-</td></tr></tbody></table>",
  "<p>Pneumonoultramicroscopicsilicovolcanoconiosis is one word with no break in it.</p>",
  ...Array.from({ length: 8 }, (_, i) => para(i + 2, TOPICS[i])),
].join("");

/**
 * The library: folders (a path, outermost first; [] is unfiled) and each feed's articles. `count` articles are made by
 * `item(i)`; `fails` is a feed at an address that never resolves (designFeedUrl).
 */
export const DESIGN_FEEDS = [
  {
    slug: "ledger", title: "The Morning Ledger", folder: ["News"], count: 240, spacing: HOUR / 3,
    item: (i) => ({
      title: i % 17 === 3 ? `Sponsored: ${title(i, 5)}` : title(i, titleLen(i)),
      author: i % 3 ? "Ada Fieldwright" : "",
      body: (i % 4 === 2 ? "" : img(1200, 675, i)) + para(i, TOPICS[i % TOPICS.length]) + para(i + 1, TOPICS[(i + 3) % TOPICS.length]),
    }),
  },
  {
    slug: "longname", title: "Example Site - Full RSS for subscribers | Example Site Daily Digest and Commentary", folder: ["News"], count: 20, spacing: 2 * HOUR,
    item: (i) => ({ title: title(i + 40, 14 + (i % 8)), body: img(1200, 800, 300 + i) + para(i, "maps") }),
  },
  {
    slug: "portrait", title: "Portrait Gallery", folder: ["Pictures"], count: 15, spacing: 5 * HOUR,
    item: (i) => ({ title: title(i + 80, 3 + (i % 4)), body: img(900, 1350, 500 + i, "A tall picture") + para(i, "gardens") }),
  },
  {
    slug: "shapes", title: "Wide Horizons", folder: ["Pictures"], count: 15, spacing: 5 * HOUR,
    item: (i) => {
      const [w, h] = [[2400, 600], [1000, 1000], [48, 48]][i % 3];
      return { title: title(i + 120, 4 + (i % 5)), body: img(w, h, 700 + i) + para(i, "weather") };
    },
  },
  {
    slug: "essays", title: "Long Essays", folder: ["Text only"], count: 12, spacing: 9 * HOUR,
    item: (i) => ({ title: title(i + 160, 6 + (i % 4)), author: "M. Longhand", body: LONG_BODY }),
  },
  {
    slug: "notes", title: "Short Notes", folder: ["Text only"], count: 30, spacing: 90 * 60_000,
    item: (i) => ({ title: i % 5 === 0 ? "Ok" : title(i + 200, 2 + (i % 3)), body: i % 2 ? "" : "<p>One line.</p>" }),
  },
  {
    slug: "unicode", title: "Ünïcödé Nöte – 日本語のニュース", folder: ["Elsewhere", "Deeper", "Deepest"], count: 12, spacing: 4 * HOUR,
    item: (i) =>
      [
        { title: "東京の新しい橋が開通しました、交通が大きく変わります", body: "<p>日本語の本文です。長い段落が続きます。</p>" },
        { title: "عنوان باللغة العربية يمتد عبر السطر", body: '<p dir="rtl">نص عربي في المقالة، فقرة كاملة من اليمين إلى اليسار.</p>' },
        { title: "Crème brûlée, naïve façades and the café 🚲🌉", body: "<p>Accents, ligatures and emoji 🙂 in one paragraph.</p>" },
        { title: "Ελληνικά και Русский текст в одном заголовке", body: "<p>Mixed scripts.</p>" },
      ][i % 4],
  },
  {
    slug: "quiet", title: "Monthly Letter", folder: ["Quiet"], count: 2, spacing: 25 * 24 * HOUR, offset: 20 * 24 * HOUR,
    item: (i) => ({ title: title(i + 240, 6), body: para(i, "clocks") }),
  },
  { slug: "empty", title: "Empty Feed", folder: ["Quiet"], count: 0 },
  {
    slug: "solo", title: "Unfiled Feed", folder: [], count: 6, spacing: 6 * HOUR,
    item: (i) => ({ title: title(i + 260, 5), body: img(1200, 675, 900 + i) + para(i, "trains") }),
  },
  // Kipple refuses to add a feed that fails, so seed.mjs imports this one in an OPML file, as a dead feed arrives.
  { slug: "broken", title: "Broken Feed", folder: [], fails: true },
];
/** A folder with nothing in it. */
export const DESIGN_EMPTY_FOLDERS = ["Empty Folder"];

/** The address of a design feed: on the feed server at `base`, or one that never resolves for a feed that `fails`. */
export const designFeedUrl = (f, base) => (f.fails ? `http://design-${f.slug}.invalid/feed.xml` : `${base}/design/feed/${f.slug}.xml`);

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;");

function feedXml(f, base) {
  const now = Date.now();
  const items = Array.from({ length: f.count }, (_, i) => {
    const it = f.item(i);
    const when = new Date(now - (f.offset ?? 0) - (i + 0.5) * f.spacing).toUTCString();
    const body = it.body.replaceAll("{base}", base);
    return (
      `<item><title>${esc(it.title)}</title><link>${base}/design/page/${f.slug}/${i}</link><guid isPermaLink="false">design-${f.slug}-${i}</guid>` +
      `<pubDate>${when}</pubDate>${it.author ? `<author>${esc(it.author)}</author>` : ""}<description>${esc(body)}</description></item>`
    );
  }).join("");
  return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>${esc(f.title)}</title><link>${base}/design/page/${f.slug}</link><description>${esc(f.title)} (design review feed)</description>${items}</channel></rss>`;
}

/** A w x h PNG: a diagonal gradient from the seed's hue with a pale disc, so neighbouring pictures look different. */
function png(w, h, seed) {
  const hue = (seed * 47) % 360;
  const rgb = (l) => {
    const c = (1 - Math.abs(2 * l - 1)) * 0.55;
    const x = c * (1 - Math.abs(((hue / 60) % 2) - 1));
    const [r, g, b] = hue < 60 ? [c, x, 0] : hue < 120 ? [x, c, 0] : hue < 180 ? [0, c, x] : hue < 240 ? [0, x, c] : hue < 300 ? [x, 0, c] : [c, 0, x];
    const m = l - c / 2;
    return [r + m, g + m, b + m].map((v) => Math.round(v * 255));
  };
  const raw = Buffer.alloc((w * 3 + 1) * h);
  const cx = w * 0.7, cy = h * 0.35, rr = (Math.min(w, h) * 0.22) ** 2;
  for (let y = 0; y < h; y++) {
    const row = y * (w * 3 + 1);
    for (let x = 0; x < w; x++) {
      const disc = (x - cx) ** 2 + (y - cy) ** 2 < rr;
      const [r, g, b] = rgb(disc ? 0.85 : 0.3 + 0.35 * ((x / w + y / h) / 2));
      raw.writeUInt8(r, row + 1 + x * 3);
      raw.writeUInt8(g, row + 2 + x * 3);
      raw.writeUInt8(b, row + 3 + x * 3);
    }
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const td = Buffer.concat([Buffer.from(type), data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(td));
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr.set([8, 2, 0, 0, 0], 8); // 8-bit RGB
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}
const pngCache = new Map();

/**
 * Answers a /design/ request: /design/feed/<slug>.xml, /design/img/<w>x<h>-<seed>.png or /design/page/... (an article's
 * own page). Returns false for any other path, so the caller can answer it.
 */
export function designRoute(req, res, base) {
  const url = req.url ?? "";
  let m = /^\/design\/feed\/([a-z]+)\.xml$/.exec(url);
  if (m) {
    const f = DESIGN_FEEDS.find((x) => x.slug === m[1]);
    if (!f || f.fails) res.writeHead(404).end("not found");
    else res.writeHead(200, { "Content-Type": "application/rss+xml; charset=utf-8" }).end(feedXml(f, base));
    return true;
  }
  m = /^\/design\/img\/(\d{1,4})x(\d{1,4})-(\d{1,4})\.png$/.exec(url);
  if (m) {
    const key = m.slice(1).join("-");
    if (!pngCache.has(key)) pngCache.set(key, png(Number(m[1]), Number(m[2]), Number(m[3])));
    res.writeHead(200, { "Content-Type": "image/png", "Cache-Control": "max-age=86400" }).end(pngCache.get(key));
    return true;
  }
  if (url.startsWith("/design/page/")) {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" }).end("<!doctype html><title>Article page</title><p>The article's own page.</p>");
    return true;
  }
  return false;
}
