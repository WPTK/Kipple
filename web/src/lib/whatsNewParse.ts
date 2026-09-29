/** One release of CHANGELOG.md as the "What's new" panel shows it. */
export interface Release {
  /** Without the leading "v": `0.5.0-beta.1`. */
  version: string;
  date: string;
  /** The paragraph under the heading, if the release has one. */
  intro: string;
  groups: { kind: string; items: string[] }[];
}

/** Plain text from the little Markdown a changelog line uses: links, code, bold and italics lose their marks. */
export function plain(md: string): string {
  return md
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/(^|[\s(])_([^_]+)_(?=[\s).,;:]|$)/g, "$1$2")
    .replace(/\s+/g, " ")
    .trim();
}

/**
 * The newest `max` versioned sections of a Keep a Changelog file (`## [1.2.3] - 2026-01-02`, newest first, as the
 * file is kept). "Unreleased" and the link references at the bottom are skipped. Runs at build time (vite.config.ts)
 * and in tests; it never throws on an odd file, it just finds fewer releases.
 */
export function parseChangelog(text: string, max = 10): Release[] {
  const releases: Release[] = [];
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  let cur: Release | null = null;
  let group: Release["groups"][number] | null = null;
  let item: string[] | null = null;
  let intro: string[] = [];
  const flushItem = () => {
    if (group && item && item.length) group.items.push(plain(item.join(" ")));
    item = null;
  };
  const flushRelease = () => {
    flushItem();
    if (cur) {
      cur.intro = plain(intro.join(" "));
      cur.groups = cur.groups.filter((g) => g.items.length > 0);
      releases.push(cur);
    }
    cur = null;
    group = null;
    intro = [];
  };
  for (const line of lines) {
    const h2 = /^## \[([^\]]+)\](?:\s+-\s+(\S+))?/.exec(line);
    if (h2) {
      flushRelease();
      if (h2[1]!.toLowerCase() !== "unreleased") cur = { version: h2[1]!, date: h2[2] ?? "", intro: "", groups: [] };
      continue;
    }
    if (!cur) continue;
    if (/^\[[^\]]+\]:\s/.test(line)) continue; // a link reference at the foot of the file
    const h3 = /^### (.+)$/.exec(line);
    if (h3) {
      flushItem();
      group = { kind: h3[1]!.trim(), items: [] };
      cur.groups.push(group);
      continue;
    }
    if (!group) {
      if (line.trim()) intro.push(line.trim());
      continue;
    }
    const bullet = /^- (.*)$/.exec(line);
    if (bullet) {
      flushItem();
      item = [bullet[1]!];
    } else if (line.trim() === "") {
      flushItem();
    } else if (item) {
      item.push(line.trim()); // a wrapped line of the same entry
    }
  }
  flushRelease();
  return releases.slice(0, max);
}
