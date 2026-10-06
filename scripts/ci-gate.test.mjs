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

test('the prune step runs in go, web and docker before the build and test steps', () => {
  for (const j of ['go', 'web', 'docker']) {
    const i = jobs[j].indexOf('ci-prune-prose.sh --delete');
    assert.ok(i >= 0, `${j}: no prune step`);
    const later = jobs[j].slice(i);
    assert.ok(/go vet|npm ci|build-image/.test(later), `${j}: the prune step must come before the build and test steps`);
  }
  assert.ok(!jobs.security.includes('ci-prune-prose'), 'security keeps the full tree (gitleaks scans history)');
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
