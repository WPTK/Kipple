// Liveness check for starter/feeds.json: fetches every feed URL and reports the
// ones that are dead, redirected, or not RSS/Atom with items. Not part of CI
// (network flakiness must not gate unrelated PRs); run it before a release.
//
//   node scripts/check-starter-feeds.mjs
import { readFile } from "node:fs/promises";

const file = new URL("../starter/feeds.json", import.meta.url);
const list = JSON.parse(await readFile(file, "utf8"));

let bad = 0;
for (const cat of list.categories) {
  for (const feed of cat.feeds) {
    let note = "";
    try {
      const res = await fetch(feed.url, {
        headers: { "User-Agent": "Mozilla/5.0 (compatible; kipple-feedcheck)" },
        signal: AbortSignal.timeout(30_000),
      });
      const body = await res.text();
      const items = (body.match(/<(item|entry)[\s>]/g) ?? []).length;
      const isFeed = /<(rss|feed|rdf:RDF)[\s>]/.test(body.slice(0, 4096));
      if (!res.ok) note = `HTTP ${res.status}`;
      else if (!isFeed) note = "not an RSS or Atom document";
      else if (items === 0) note = "no items";
      else if (res.redirected) note = `redirected to ${res.url} (update the file)`;
      if (!note) console.log(`ok    ${feed.id} (${items} items)`);
    } catch (e) {
      note = String(e?.message ?? e);
    }
    if (note) {
      bad++;
      console.log(`FAIL  ${feed.id}: ${note}`);
    }
  }
}
process.exit(bad ? 1 : 0);
