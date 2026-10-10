// node --test scripts/ci-gate.test.mjs
// The four required checks (go, web, security, docker) must fail open to "run everything", never to "skipped": a job
// skipped by `if` counts as passed, so a gate that skips when the `changes` job fails would let unchecked code merge.
// The Go tests run in the `go-test` matrix job; the required check `go` only aggregates its legs, so the gate rules for
// the work (skip only on an explicit false, prune first) apply to `go-test` and the aggregation rule to `go`.
// This reads ci.yml as text (two-space job keys, four-space job fields, as the file is written); it does not need a
// YAML library.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
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
const needsChanges = (job) => /^    needs:[ ]*(changes|\[[ ]*(?:[a-z0-9-]+[ ]*,[ ]*)*changes[ ]*(?:,[ ]*[a-z0-9-]+[ ]*)*\])[ ]*(#.*)?$/m.test(jobs[job]);
const REQUIRED = ['go', 'web', 'security', 'docker'];

test('the required checks and the gate exist', () => {
  for (const j of ['changes', 'go-test', ...REQUIRED]) assert.ok(jobs[j], `job ${j} is missing from ci.yml`);
});

test('no condition tests the gate for "true" (an empty output must run everything)', () => {
  assert.doesNotMatch(text, /outputs\.code\s*==/);
});

test('every required check waits for the gate and survives its failure', () => {
  for (const j of [...REQUIRED, 'go-test']) {
    assert.ok(needsChanges(j), `${j}: needs changes`);
    const cond = jobIf(j);
    assert.ok(cond, `${j}: needs a job-level if, or a failed gate skips it`);
    // The go aggregate runs even in a cancelled run, so it reports failed instead of skipped (a skipped required
    // check counts as passed); the others skip on a cancel, which leaves nothing mergeable behind.
    if (j === 'go') assert.equal(cond, '${{ always() }}', 'go: the job-level if must be exactly always()');
    else assert.ok(cond.includes('!cancelled()'), `${j}: the job-level if must contain !cancelled()`);
    // success() is implied when an if has no status function; written out, or tested through the gate job's result,
    // it skips the job when the gate fails.
    assert.doesNotMatch(cond, /(^|[^!])\bsuccess\(\)/, `${j}: success() in the job-level if skips the job when the gate fails`);
    assert.doesNotMatch(cond, /needs\.changes\.result/, `${j}: needs.changes.result in the job-level if skips the job when the gate fails`);
  }
});

test('go-test and docker skip whole only on an explicit false', () => {
  for (const j of ['go-test', 'docker']) assert.ok(jobIf(j).includes(GUARD), `${j}: if must contain ${GUARD}`);
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

test('the prune step runs in go-test, web and docker, for real, before the build and test steps', () => {
  const STEP = /^      - (?:if: .*\n        )?run: bash (?:[.][.]\/)?scripts\/ci-prune-prose[.]sh --delete$/m;
  for (const j of ['go-test', 'web', 'docker']) {
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

// A failed, cancelled or skipped leg must fail the required check; the only skip that passes is the gate's explicit
// false. The aggregate reads the legs' result and the gate's output through env and tests exactly that.
const AGG = '[ "$RESULT" = success ] || { [ "$RESULT" = skipped ] && [ "$CODE" = false ]; }';
test('the required go check passes only when every go-test leg passed or the gate skipped them', () => {
  assert.match(jobs.go, /^    needs:[ ]*\[[ ]*changes[ ]*,[ ]*go-test[ ]*\][ ]*$/m, 'go: needs [changes, go-test]');
  assert.match(jobs.go, /^          CODE: \$\{\{ needs\.changes\.outputs\.code \}\}$/m, 'go: CODE must be the gate output');
  assert.match(jobs.go, /^          RESULT: \$\{\{ needs\.go-test\.result \}\}$/m, 'go: RESULT must be the go-test result');
  const lines = jobs.go.split('\n').map((l) => l.trim());
  assert.ok(lines.includes(AGG), `go: the run step must end with exactly: ${AGG}`);
  assert.equal(lines.filter((l) => l && !/^[a-zA-Z_-]+:|^- |^#|^echo /.test(l)).at(-1), AGG, 'go: the check must be the last command, so nothing after it masks a failure');
  assert.doesNotMatch(jobs.go, /continue-on-error|[|][|][ ]*true/, 'go: the aggregate must be able to fail');
  assert.doesNotMatch(jobs['go-test'], /continue-on-error/, 'go-test: a leg must be able to fail');
  assert.match(jobs['go-test'], /^      fail-fast: false$/m, 'go-test: every leg runs to the end');
});

// The aggregate's verdict for every result a needed job can have (success, failure, cancelled, skipped), run through
// bash as the runner runs it.
test('the go aggregate fails a cancelled, failed or wrongly skipped leg', () => {
  const passes = (RESULT, CODE) => spawnSync('bash', ['-c', AGG], { env: { ...process.env, RESULT, CODE } }).status === 0;
  for (const code of ['true', 'false', '']) assert.ok(passes('success', code), `success with code=${code} passes`);
  assert.ok(passes('skipped', 'false'), 'skipped by the prose-only gate passes');
  for (const code of ['true', '']) assert.ok(!passes('skipped', code), `skipped with code=${code} fails`);
  for (const result of ['failure', 'cancelled', '']) {
    for (const code of ['true', 'false', '']) assert.ok(!passes(result, code), `${result || 'no result'} with code=${code} fails`);
  }
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
  jobs.__t = '    needs: [go-test]\n';
  assert.ok(!needsChanges('__t'), 'a needs list without changes must not pass');
  jobs.__t = '    needs: [changes, go-test]\n';
  assert.ok(needsChanges('__t'), 'a needs list with changes passes');
  delete jobs.__t;
});
