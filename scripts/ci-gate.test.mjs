// node --test scripts/ci-gate.test.mjs
// The four required checks (go, web, security, docker) must fail open to "run everything", never to "skipped": a job
// skipped by `if` counts as passed, so a gate that skips when the `changes` job fails would let unchecked code merge.
// This reads ci.yml as text; it does not need a YAML library.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const text = readFileSync(fileURLToPath(new URL('../.github/workflows/ci.yml', import.meta.url)), 'utf8').replace(/\r\n/g, '\n');
const jobsText = text.slice(text.indexOf('\njobs:\n') + 1);
const jobs = {};
for (const m of jobsText.matchAll(/^  ([a-z][a-z0-9-]*):\n((?:(?:    .*|)\n)*)/gm)) jobs[m[1]] = m[2];
const GUARD = "needs.changes.outputs.code != 'false'";

test('the required checks and the gate exist', () => {
  for (const j of ['changes', 'go', 'web', 'security', 'docker']) assert.ok(jobs[j], `job ${j} is missing from ci.yml`);
});

test('no condition tests the gate for "true" (an empty output must run everything)', () => {
  assert.doesNotMatch(text, /outputs\.code\s*==/);
});

test('every required check waits for the gate and survives its failure', () => {
  for (const j of ['go', 'web', 'security', 'docker']) {
    assert.match(jobs[j], /^    needs: changes$/m, `${j}: needs: changes`);
    const jobIf = /^    if: (.*)$/m.exec(jobs[j]);
    assert.ok(jobIf, `${j}: needs a job-level if, or a failed gate skips it`);
    assert.ok(jobIf[1].includes('!cancelled()'), `${j}: the job-level if must contain !cancelled()`);
  }
});

test('go and docker skip whole only on an explicit false', () => {
  for (const j of ['go', 'docker']) {
    const jobIf = /^    if: (.*)$/m.exec(jobs[j])[1];
    assert.ok(jobIf.includes(GUARD), `${j}: if must contain ${GUARD}`);
  }
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
