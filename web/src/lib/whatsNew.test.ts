import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { compareVersions, parseSemver } from "./semver";
import { releasesToShow, whatsNewDue } from "./whatsNew";
import { parseChangelog, plain, type Release } from "./whatsNewParse";

describe("semver", () => {
  it("parses releases, prereleases and a leading v; refuses the rest", () => {
    expect(parseSemver("v0.5.0-beta.1")).toEqual({ major: 0, minor: 5, patch: 0, pre: ["beta", 1] });
    expect(parseSemver("1.2.3")).toEqual({ major: 1, minor: 2, patch: 3, pre: [] });
    expect(parseSemver("1.2.3+build.5")?.pre).toEqual([]);
    for (const bad of ["dev", "", "1.2", "v1.2.x", "latest"]) expect(parseSemver(bad)).toBeNull();
  });

  it("orders by SemVer 2.0.0 precedence, prereleases included", () => {
    const order = ["0.3.0-alpha.1", "0.3.0-alpha.2", "0.3.0-alpha.10", "0.3.0-beta.1", "0.3.0-beta.2", "0.3.0-rc.1", "0.3.0", "0.3.1", "0.10.0", "1.0.0"];
    for (let i = 0; i < order.length - 1; i++) {
      expect(compareVersions(order[i]!, order[i + 1]!), `${order[i]} < ${order[i + 1]}`).toBeLessThan(0);
      expect(compareVersions(order[i + 1]!, order[i]!)).toBeGreaterThan(0);
    }
    expect(compareVersions("v1.0.0", "1.0.0")).toBe(0);
    expect(compareVersions("dev", "1.0.0")).toBeNull();
  });
});

describe("parseChangelog", () => {
  const text = `# Changelog

## [Unreleased]

Changes not yet in a release are one file each.

## [0.3.0-beta.2] - 2026-09-29

Fixes and polish. No schema migration.

### Added

- First entry with a [link](https://example.test/x) and \`code\`. (#58)
- Second entry that wraps
  onto a second line. (#60)

### Fixed

- A fix. (#61)

### Removed

## [0.3.0-beta.1] - 2026-09-27

Intro on
two lines.

### Changed

- Only change.

[Unreleased]: https://example.test/compare/v0.3.0-beta.2...HEAD
[0.3.0-beta.2]: https://example.test/compare/v0.3.0-beta.1...v0.3.0-beta.2
`;

  it("finds the versioned sections newest first, skipping Unreleased and link references", () => {
    const r = parseChangelog(text);
    expect(r.map((x) => x.version)).toEqual(["0.3.0-beta.2", "0.3.0-beta.1"]);
    expect(r[0]!.date).toBe("2026-09-29");
    expect(r[0]!.intro).toBe("Fixes and polish. No schema migration.");
    expect(r[1]!.intro).toBe("Intro on two lines.");
  });

  it("groups entries by kind, joins wrapped lines, drops empty groups, and strips Markdown", () => {
    const [r] = parseChangelog(text);
    expect(r!.groups.map((g) => g.kind)).toEqual(["Added", "Fixed"]); // Removed had nothing
    expect(r!.groups[0]!.items).toEqual(["First entry with a link and code. (#58)", "Second entry that wraps onto a second line. (#60)"]);
    expect(parseChangelog(text)[1]!.groups[0]!.items).toEqual(["Only change."]);
  });

  it("keeps at most the newest N and copes with CRLF and an empty file", () => {
    expect(parseChangelog(text, 1)).toHaveLength(1);
    expect(parseChangelog(text.replace(/\n/g, "\r\n"))).toHaveLength(2);
    expect(parseChangelog("")).toEqual([]);
    expect(parseChangelog("no headings at all")).toEqual([]);
  });

  it("plain() removes the marks a changelog line carries", () => {
    expect(plain("**Bold** and `code` and [text](http://x.test/a_b_c) done")).toBe("Bold and code and text done");
  });

  it("reads the real CHANGELOG.md: ten releases at most, each with a date and something to say", () => {
    const real = parseChangelog(readFileSync(resolve(process.cwd(), "../CHANGELOG.md"), "utf8"));
    expect(real.length).toBeGreaterThan(0);
    expect(real.length).toBeLessThanOrEqual(10);
    for (const r of real) {
      expect(parseSemver(r.version), r.version).not.toBeNull();
      expect(r.groups.length + (r.intro ? 1 : 0), r.version).toBeGreaterThan(0);
    }
  });
});

const rel = (version: string): Release => ({ version, date: "2026-01-01", intro: "", groups: [{ kind: "Added", items: [version] }] });
const ALL = ["0.5.0", "0.5.0-beta.2", "0.5.0-beta.1", "0.4.0", "0.3.0"].map(rel);

describe("releasesToShow", () => {
  it("shows every release newer than the last one seen, up to the running one, newest first", () => {
    expect(releasesToShow(ALL, "0.3.0", "v0.5.0-beta.2").map((r) => r.version)).toEqual(["0.5.0-beta.2", "0.5.0-beta.1", "0.4.0"]);
    expect(releasesToShow(ALL, "0.4.0", "0.5.0").map((r) => r.version)).toEqual(["0.5.0", "0.5.0-beta.2", "0.5.0-beta.1"]);
  });

  it("counts prereleases: beta.1 to beta.2 is an upgrade, beta.2 to the release too", () => {
    expect(releasesToShow(ALL, "0.5.0-beta.1", "0.5.0-beta.2").map((r) => r.version)).toEqual(["0.5.0-beta.2"]);
    expect(releasesToShow(ALL, "0.5.0-beta.2", "0.5.0").map((r) => r.version)).toEqual(["0.5.0"]);
  });

  it("shows nothing when already seen, on a downgrade, or for a development bundle", () => {
    expect(releasesToShow(ALL, "0.5.0", "0.5.0")).toEqual([]);
    expect(releasesToShow(ALL, "0.5.0", "0.4.0")).toEqual([]);
    expect(releasesToShow(ALL, "0.3.0", "dev")).toEqual([]);
  });

  it("with nothing seen yet shows just the running release's section", () => {
    expect(releasesToShow(ALL, "", "0.4.0").map((r) => r.version)).toEqual(["0.4.0"]);
    expect(releasesToShow(ALL, "", "0.4.5").map((r) => r.version)).toEqual(["0.4.0"]); // newest at or before it
  });
});

describe("whatsNewDue", () => {
  it("skips a development bundle and anything that is not a version", () => {
    expect(whatsNewDue("0.3.0", "dev", true)).toBe("no");
    expect(whatsNewDue("", "dev", false)).toBe("no");
  });

  it("marks a first run silently and shows it to a library that had no record", () => {
    expect(whatsNewDue("", "0.5.0", false)).toBe("mark");
    expect(whatsNewDue("", "0.5.0", true)).toBe("show");
  });

  it("shows after an upgrade, once", () => {
    expect(whatsNewDue("0.4.0", "0.5.0", true)).toBe("show");
    expect(whatsNewDue("0.5.0", "0.5.0", true)).toBe("no");
    expect(whatsNewDue("0.6.0", "0.5.0", true)).toBe("no");
    expect(whatsNewDue("garbage", "0.5.0", true)).toBe("no");
  });
});
