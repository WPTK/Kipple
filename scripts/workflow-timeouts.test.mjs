// node --test scripts/workflow-timeouts.test.mjs
// A job without timeout-minutes that hangs runs to GitHub's six-hour default. Every job that starts a runner sets its
// own limit; a job that calls a reusable workflow (`uses:` at job level) has no runner, and the called jobs set theirs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const dir = fileURLToPath(new URL('../.github/workflows/', import.meta.url));

// The jobs of one workflow file and the keys written directly under each (four spaces in).
const jobs = (text) => {
  const found = new Map();
  let inJobs = false;
  let job = null;
  for (const line of text.replace(/\r\n/g, '\n').split('\n')) {
    if (/^\S/.test(line)) inJobs = /^jobs:\s*$/.test(line);
    if (!inJobs) continue;
    const name = /^ {2}([A-Za-z0-9_-]+):\s*$/.exec(line);
    if (name) found.set((job = name[1]), new Set());
    const key = /^ {4}([A-Za-z0-9_-]+):/.exec(line);
    if (job && key) found.get(job).add(key[1]);
  }
  return found;
};

test('every job that starts a runner sets timeout-minutes', () => {
  const missing = [];
  for (const f of readdirSync(dir).filter((n) => /\.ya?ml$/.test(n))) {
    const found = jobs(readFileSync(dir + f, 'utf8'));
    assert.ok(found.size > 0, `${f}: no jobs found (did the layout change?)`);
    for (const [name, keys] of found) {
      if (!keys.has('uses') && !keys.has('timeout-minutes')) missing.push(`${f}: ${name}`);
    }
  }
  assert.deepEqual(missing, []);
});

test('the parser sees a job without a limit, a job with one, and a reusable-workflow call', () => {
  const found = jobs('on: push\njobs:\n  a:\n    runs-on: x\n  b:\n    runs-on: x\n    timeout-minutes: 5\n  c:\n    uses: ./w.yml\n');
  assert.deepEqual([...found.keys()], ['a', 'b', 'c']);
  assert.ok(!found.get('a').has('timeout-minutes'));
  assert.ok(found.get('b').has('timeout-minutes'));
  assert.ok(found.get('c').has('uses'));
});
