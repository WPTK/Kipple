// node --test scripts/ci-gate.test.mjs
// The four required checks (go, web, security, docker) must fail open to "run everything", never to "skipped": a job
// skipped by `if` counts as passed, so a gate that skips when the `changes` job fails would let unchecked code merge.
// This reads ci.yml as text (two-space job keys, four-space job fields, as the file is written); it does not need a
// YAML library.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const text = readFileSync(fileURLToPath(new URL('../.github/workflows/ci.yml', import.meta.url)), 'utf8').replace(/\r\n/g, '\n');
const jobsText = text.slice(text.indexOf('\njobs:\n') + 1);
const jobs = {};
for (const m of jobsText.matchAll(/^  ([a-z][a-z0-9-]*):[ ]*(?:#.*)?\n((?:(?:    .*|)\n)*)/gm)) jobs[m[1]] = m[2];
const GUARD = "needs.changes.outputs.code != 'false'";

// The job-level `if`, whether written on one line or folded (`if: >-` with the expression on the next lines).
function jobIf(job) {
  const lines = jobs[job].split('\n');
  const i = lines.findIndex((l) => /^    if:/.test(l));
  if (i < 0) return null;
  const first = lines[i].replace(/^    if:[ ]*/, '');
  if (!/^[>|][-+]?$/.test(first.trim())) return first;
  const rest = [];
  for (let k = i + 1; k < lines.length && /^      /.test(lines[k]); k++) rest.push(lines[k].trim());
  return rest.join(' ');
}
const needsChanges = (job) => /^    needs:[ ]*(changes|\[[ ]*changes[ ]*\])[ ]*(#.*)?$/m.test(jobs[job]);
const REQUIRED = ['go', 'web', 'security', 'docker'];

test('the required checks and the gate exist', () => {
  for (const j of ['changes', ...REQUIRED]) assert.ok(jobs[j], `job ${j} is missing from ci.yml`);
});

test('no condition tests the gate for "true" (an empty output must run everything)', () => {
  assert.doesNotMatch(text, /outputs\.code\s*==/);
});

test('every required check waits for the gate and survives its failure', () => {
  for (const j of REQUIRED) {
    assert.ok(needsChanges(j), `${j}: needs changes`);
    const cond = jobIf(j);
    assert.ok(cond, `${j}: needs a job-level if, or a failed gate skips it`);
    assert.ok(cond.includes('!cancelled()'), `${j}: the job-level if must contain !cancelled()`);
    // success() is implied when an if has no status function; written out, or tested through the gate job's result,
    // it skips the job when the gate fails.
    assert.doesNotMatch(cond, /(^|[^!])\bsuccess\(\)/, `${j}: success() in the job-level if skips the job when the gate fails`);
    assert.doesNotMatch(cond, /needs\.changes\.result/, `${j}: needs.changes.result in the job-level if skips the job when the gate fails`);
  }
});

test('go and docker skip whole only on an explicit false', () => {
  for (const j of ['go', 'docker']) assert.ok(jobIf(j).includes(GUARD), `${j}: if must contain ${GUARD}`);
});

test('web and security gate their heavy steps the same way and keep gitleaks and the changelog check ungated', () => {
  for (const j of ['web', 'security']) assert.ok(jobs[j].includes(`if: ${GUARD}`), `${j}: no gated steps`);
  const step = (job, needle) => {
    const i = jobs[job].indexOf(needle);
    assert.ok(i >= 0, `${job}: ${needle} not found`);
    return jobs[job].slice(jobs[job].lastIndexOf('\n      - ', i), i);
  };
  assert.doesNotMatch(step('security', 'name: gitleaks'), /if:/, 'gitleaks must always run');
  assert.doesNotMatch(step('web', 'changelog.mjs check'), /if:/, 'the changelog check must always run');
});

test('the tooling job waits for the gate, skips only on an explicit false and survives the gate failing', () => {
  assert.ok(jobs.tooling, 'job tooling is missing from ci.yml');
  assert.ok(needsChanges('tooling'), 'tooling: needs changes');
  const cond = jobIf('tooling');
  assert.ok(cond, 'tooling: needs a job-level if, or a failed gate skips it');
  assert.ok(cond.includes('!cancelled()'), 'tooling: the job-level if must contain !cancelled()');
  assert.ok(cond.includes("needs.changes.outputs.scripts != 'false'"), "tooling: must skip only on scripts == 'false'");
  assert.ok(cond.includes("github.event_name == 'pull_request'"), 'tooling: no Windows runner outside pull requests');
  assert.doesNotMatch(cond, /(^|[^!])\bsuccess\(\)/, 'tooling: success() in the job-level if skips the job when the gate fails');
  assert.doesNotMatch(cond, /needs\.changes\.result/, 'tooling: needs.changes.result skips the job when the gate fails');
  assert.doesNotMatch(text, /outputs\.scripts\s*==/, 'no condition tests the scripts output for "true"');
  assert.match(jobs.changes, /scripts: \$\{\{ steps\.filter\.outputs\.scripts \}\}/, 'the changes job must export scripts');
  assert.match(jobs.changes, /^          scripts=true$/m, 'scripts must default to true (fail open)');
});

test('the prune step runs in go, web and docker, for real, before the build and test steps', () => {
  const STEP = /^      - (?:if: .*\n        )?run: bash (?:[.][.]\/)?scripts\/ci-prune-prose[.]sh --delete$/m;
  for (const j of ['go', 'web', 'docker']) {
    const m = STEP.exec(jobs[j]);
    assert.ok(m, `${j}: no run step that is exactly the prune with --delete (a dry run or a comment does not count)`);
    const rest = jobs[j].slice(m.index);
    assert.ok(/go vet|npm ci|build-image/.test(rest), `${j}: the prune step must come before the build and test steps`);
    const end = rest.indexOf('\n      - ', 1);
    const block = end < 0 ? rest : rest.slice(0, end);
    assert.doesNotMatch(block, /continue-on-error|[|][|]/, `${j}: the prune step must be able to fail the job`);
  }
  assert.ok(!jobs.security.includes('ci-prune-prose'), 'security keeps the full tree (gitleaks scans history)');
});

test('web checks the changelog fragments before the prune deletes them', () => {
  const check = jobs.web.indexOf('changelog.mjs check');
  const prune = jobs.web.indexOf('ci-prune-prose.sh --delete');
  assert.ok(check >= 0 && prune >= 0 && check < prune, 'the changelog fragment check must come before the prune step: changelog.mjs reads changes/');
});

// The checks above must fail on the regressions they name.
test('the job-level matcher rejects the regressions', () => {
  const sample = (ifLine) => {
    jobs.__t = `    needs: [changes]\n${ifLine}\n    runs-on: x\n`;
    return jobIf('__t');
  };
  sample("    if: success() && !cancelled() && needs.changes.outputs.code != 'false'");
  assert.ok(needsChanges('__t'));
  assert.match(sample("    if: success() && !cancelled() && needs.changes.outputs.code != 'false'"), /(^|[^!])\bsuccess\(\)/);
  assert.match(sample("    if: ${{ !cancelled() && needs.changes.result == 'success' }}"), /needs\.changes\.result/);
  assert.equal(sample('    if: >-\n      !cancelled() &&\n      needs.changes.outputs.code != \'false\''), "!cancelled() && needs.changes.outputs.code != 'false'");
  delete jobs.__t;
});
