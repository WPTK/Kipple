// Runs every UAT flow listed in flows.json against one seeded instance and prints one verdict per flow.
//
//   node uat/all.mjs --check                       only checks that every web/uat/*.mjs is a flow or excluded
//   node uat/all.mjs [--engine chromium|webkit]    runs the flows of that engine (default chromium), all of them
//                                                  even when one fails; the address and credentials are
//                                                  KIPPLE_UAT_URL and the seed's, as for each flow
//
// Exit code: 0 all clean, 1 a flow had findings, 2 the manifest is out of date or a flow could not run.
// The Browser UAT workflow (.github/workflows/browser-uat.yml) runs this, so a flow listed here runs in CI.
import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const dir = dirname(fileURLToPath(import.meta.url));
const { values } = parseArgs({ options: { check: { type: "boolean", default: false }, engine: { type: "string", default: "chromium" } } });
const manifest = JSON.parse(readFileSync(join(dir, "flows.json"), "utf8"));
const files = readdirSync(dir).filter((f) => f.endsWith(".mjs"));
const known = new Set([...manifest.chromium, ...manifest.webkit, ...Object.keys(manifest.excluded)]);
const problems = [
  ...files.filter((f) => !known.has(f)).map((f) => `${f} is neither a flow nor excluded: add it to uat/flows.json`),
  ...[...known].filter((f) => !files.includes(f)).map((f) => `${f} is in uat/flows.json but does not exist`),
  ...manifest.webkit.filter((f) => !manifest.chromium.includes(f)).map((f) => `${f} runs under WebKit but not Chromium`),
];
if (problems.length) {
  console.error(problems.join("\n"));
  process.exit(2);
}
if (values.check) {
  console.log(`uat/flows.json: ${files.length} files, ${manifest.chromium.length} flows, all accounted for`);
  process.exit(0);
}
const list = manifest[values.engine];
if (!list) {
  console.error(`unknown --engine ${values.engine} (chromium or webkit)`);
  process.exit(2);
}
let worst = 0;
const verdicts = [];
for (const flow of list) {
  console.log(`::group::uat ${flow} (${values.engine})`);
  const r = spawnSync(process.execPath, [join(dir, flow), ...(values.engine === "chromium" ? [] : ["--browser", values.engine])], { stdio: "inherit", cwd: join(dir, "..") });
  console.log("::endgroup::");
  const code = r.status ?? 2;
  worst = Math.max(worst, code);
  verdicts.push(`${code === 0 ? "ok  " : "FAIL"} ${flow} (exit ${code}${r.error ? `, ${r.error.message}` : ""})`);
}
console.log(`\n${verdicts.join("\n")}`);
process.exit(worst);
