/** A version as far as ordering needs it: `v0.5.0-beta.1` is 0.5.0 with the prerelease identifiers ["beta", 1]. */
export interface Semver {
  major: number;
  minor: number;
  patch: number;
  pre: (string | number)[];
}

const SEMVER = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/;

/** Parse a release version, with or without the leading "v". Null for "dev", "" and anything that is not one. */
export function parseSemver(v: string): Semver | null {
  const m = SEMVER.exec(v.trim());
  if (!m) return null;
  const pre = m[4] ? m[4].split(".").map((p) => (/^\d+$/.test(p) ? Number(p) : p)) : [];
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]), pre };
}

/** SemVer 2.0.0 precedence: a prerelease sorts before its release, numeric identifiers before words. Negative when a < b. */
function compareSemver(a: Semver, b: Semver): number {
  for (const k of ["major", "minor", "patch"] as const) {
    if (a[k] !== b[k]) return a[k] < b[k] ? -1 : 1;
  }
  if (a.pre.length === 0 || b.pre.length === 0) return a.pre.length === b.pre.length ? 0 : a.pre.length === 0 ? 1 : -1;
  for (let i = 0; i < Math.min(a.pre.length, b.pre.length); i++) {
    const x = a.pre[i]!;
    const y = b.pre[i]!;
    if (x === y) continue;
    if (typeof x === "number" && typeof y === "number") return x < y ? -1 : 1;
    if (typeof x === "number") return -1;
    if (typeof y === "number") return 1;
    return x < y ? -1 : 1;
  }
  return a.pre.length === b.pre.length ? 0 : a.pre.length < b.pre.length ? -1 : 1;
}

/** compareSemver on two version strings; null when either is not a version. */
export function compareVersions(a: string, b: string): number | null {
  const x = parseSemver(a);
  const y = parseSemver(b);
  return x && y ? compareSemver(x, y) : null;
}
