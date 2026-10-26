// Runs every UAT flow listed in flows.json against one seeded instance and prints one verdict per flow.
//
//   node uat/all.mjs --check                       only checks that every web/uat/*.mjs is a flow or excluded
//   node uat/all.mjs [--engine chromium|webkit|firefox]    runs the flows of that engine (default chromium), all of them
//                                                  even when one fails; the address and credentials are
//                                                  KIPPLE_UAT_URL and the seed's, as for each flow
//
// Exit code: 0 all clean, 1 a flow had findings, 2 the manifest is out of date, a flow could not run, or the instance
// stopped answering /healthz between flows (the run stops there instead of failing every remaining flow).
// The summary repeats the FAIL lines of the flows that failed, and goes to the Actions step summary when there is one.
// The Browser UAT workflow (.github/workflows/browser-uat.yml) runs this, so a flow listed here runs in CI.
import { spawn } from "node:child_process";
import { appendFileSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const dir = dirname(fileURLToPath(import.meta.url));
const { values } = parseArgs({ options: { check: { type: "boolean", default: false }, engine: { type: "string", default: "chromium" } } });
const manifest = JSON.parse(readFileSync(join(dir, "flows.json"), "utf8"));
const files = readdirSync(dir).filter((f) => f.endsWith(".mjs"));
const known = new Set([...manifest.chromium, ...manifest.webkit, ...manifest.firefox, ...Object.keys(manifest.excluded)]);
const problems = [
  ...files.filter((f) => !known.has(f)).map((f) => `${f} is neither a flow nor excluded: add it to uat/flows.json`),
  ...[...known].filter((f) => !files.includes(f)).map((f) => `${f} is in uat/flows.json but does not exist`),
  ...manifest.webkit.filter((f) => !manifest.chromium.includes(f)).map((f) => `${f} runs under WebKit but not Chromium`),
  ...manifest.firefox.filter((f) => !manifest.chromium.includes(f)).map((f) => `${f} runs under Firefox but not Chromium`),
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
  console.error(`unknown --engine ${values.engine} (chromium, webkit or firefox)`);
  process.exit(2);
}
// The instance every flow signs in to: the same address a flow uses (common.mjs `--url` default).
const base = process.env.KIPPLE_UAT_URL || "http://127.0.0.1:1919";
const alive = async () => {
  try {
    return (await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(5000) })).ok;
  } catch {
    return false;
  }
};
/** Runs one flow; its output goes through to this process's, and the stderr lines starting with "FAIL " are kept. */
const run = (flow) =>
  new Promise((resolve) => {
    let err = "";
    const args = [join(dir, flow), ...(values.engine === "chromium" ? [] : ["--browser", values.engine])];
    const child = spawn(process.execPath, args, { stdio: ["inherit", "inherit", "pipe"], cwd: join(dir, "..") });
    child.stderr.on("data", (d) => {
      process.stderr.write(d);
      err += d;
    });
    child.on("error", (e) => resolve({ code: 2, error: e, failures: [] }));
    child.on("close", (code) => resolve({ code: code ?? 2, failures: err.split(/\r?\n/).filter((l) => l.startsWith("FAIL ")) }));
  });
let worst = 0;
const verdicts = [];
const failed = [];
for (const flow of list) {
  if (!(await alive())) {
    console.error(`instance at ${base} is gone before ${flow}; stopping`);
    process.exit(2);
  }
  console.log(`::group::uat ${flow} (${values.engine})`);
  const r = await run(flow);
  console.log("::endgroup::");
  worst = Math.max(worst, r.code);
  verdicts.push(`${r.code === 0 ? "ok  " : "FAIL"} ${flow} (exit ${r.code}${r.error ? `, ${r.error.message}` : ""})`);
  if (r.failures.length) failed.push({ flow, lines: r.failures });
}
console.log(`\n${verdicts.join("\n")}`);
if (failed.length) {
  console.log("\nFailures");
  for (const f of failed) console.log(`${f.flow}\n${f.lines.map((l) => `  ${l}`).join("\n")}`);
  if (process.env.GITHUB_STEP_SUMMARY) {
    const body = failed.map((f) => `**${f.flow}**\n\n${f.lines.map((l) => `- \`${l.replace(/`/g, "'")}\``).join("\n")}`).join("\n\n");
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### Browser UAT failures (${values.engine})\n\n${body}\n`);
  }
}
process.exit(worst);
