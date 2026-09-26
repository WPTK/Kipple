import { describe, expect, it } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

const SRC = resolve(__dirname, "..");

/** The source modules a file imports (static imports and re-exports, @/ and relative only). */
function deps(file: string): string[] {
  const text = readFileSync(file, "utf8");
  const out: string[] = [];
  for (const m of text.matchAll(/(?:import|export)\s[^;]*?from\s+"([^"]+)"/g)) {
    const spec = m[1]!;
    if (/^import\s+type\s/.test(m[0]) || /^export\s+type\s/.test(m[0])) continue;
    let base: string;
    if (spec.startsWith("@/")) base = resolve(SRC, spec.slice(2));
    else if (spec.startsWith(".")) base = resolve(dirname(file), spec);
    else continue;
    const hit = [".ts", ".tsx", "/index.ts", "/index.tsx"].map((e) => base + e).find((p) => existsSync(p));
    if (hit) out.push(hit);
  }
  return out;
}

function reachable(from: string): Set<string> {
  const seen = new Set<string>();
  const stack = [from];
  while (stack.length) {
    const f = stack.pop()!;
    for (const d of deps(f)) {
      if (!seen.has(d)) {
        seen.add(d);
        stack.push(d);
      }
    }
  }
  return seen;
}

describe("module graph", () => {
  it("lib/offline.ts does not import api/queries.ts, which imports it (no cycle)", () => {
    const offline = resolve(SRC, "lib/offline.ts");
    const queries = resolve(SRC, "api/queries.ts");
    expect(reachable(queries).has(offline)).toBe(true);
    expect(reachable(offline).has(queries)).toBe(false);
  });
});
