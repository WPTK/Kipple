// node --test scripts/audit-report.test.mjs
// The open / update / close decisions of the weekly audit, run against a fake `gh` that keeps issues in memory.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { report, TITLE } from './audit-report.mjs';

const RUN = 'https://example.invalid/run/1';

function results(checks) {
  const dir = mkdtempSync(join(tmpdir(), 'a263-audit-'));
  for (const name of ['govulncheck', 'npm', 'trivy']) {
    const { status = 'success', log = '' } = checks[name] ?? {};
    writeFileSync(join(dir, `${name}.status`), `${status}\n`);
    writeFileSync(join(dir, `${name}.log`), log);
  }
  return dir;
}

// Minimal in-memory GitHub: list/create/edit/close, recording every write.
function fakeGh(initial = []) {
  const issues = initial.map((i) => ({ state: 'open', body: '', ...i }));
  const writes = [];
  const flag = (args, name) => args[args.indexOf(name) + 1];
  const gh = (args) => {
    const [, verb] = args;
    if (verb === 'list') return JSON.stringify(issues.filter((i) => i.state === 'open'));
    writes.push(args);
    if (verb === 'create') {
      issues.push({ number: issues.length + 1, state: 'open', title: flag(args, '--title'), body: flag(args, '--body') });
    } else if (verb === 'edit') {
      issues.find((i) => i.number === Number(args[2])).body = flag(args, '--body');
    } else if (verb === 'close') {
      issues.find((i) => i.number === Number(args[2])).state = 'closed';
    }
    return '';
  };
  return { gh, issues, writes };
}

const vulnerable = {
  govulncheck: { status: 'failure', log: 'Vulnerability #1: GO-2026-0001\nVulnerability #2: GO-2026-0002\nGO-2026-0001 again' },
  npm: { status: 'failure', log: 'https://github.com/advisories/GHSA-abcd-2345-wxyz  high' },
};

test('a failing check opens one issue naming the advisories', () => {
  const { gh, issues, writes } = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  assert.equal(issues.length, 1);
  assert.equal(writes.length, 1);
  const create = writes[0];
  assert.equal(issues[0].title, TITLE);
  assert.match(issues[0].body, /GO-2026-0001/);
  assert.match(issues[0].body, /GO-2026-0002/);
  assert.match(issues[0].body, /GHSA-abcd-2345-wxyz/);
  assert.equal(issues[0].body.match(/GO-2026-0001/g).length, 1, 'ids are listed once');
  for (const want of ['--assignee', 'WPTK', 'security', 'area:infra']) assert.ok(create.includes(want), want);
});

test('a second run with the same findings changes nothing, even from a different run', () => {
  const { gh, writes } = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  const out = report({ dir: results(vulnerable), runUrl: 'https://example.invalid/run/2', gh });
  assert.equal(writes.length, 1, 'only the first run wrote');
  assert.match(out, /nothing to do/);
});

test('a run with different findings updates the same issue instead of opening another', () => {
  const { gh, issues, writes } = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  const more = { ...vulnerable, trivy: { status: 'failure', log: 'libfoo CVE-2026-12345 HIGH' } };
  report({ dir: results(more), runUrl: 'https://example.invalid/run/2', gh });
  assert.equal(issues.length, 1);
  assert.equal(writes.length, 2);
  assert.equal(writes[1][1], 'edit');
  assert.match(issues[0].body, /CVE-2026-12345/);
  assert.match(issues[0].body, /run\/2/);
});

test('a clean run closes the open issue and a later clean run does nothing', () => {
  const { gh, issues, writes } = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  report({ dir: results({}), runUrl: RUN, gh });
  assert.equal(issues[0].state, 'closed');
  assert.equal(writes.length, 2);
  assert.match(report({ dir: results({}), runUrl: RUN, gh }), /nothing to do/);
  assert.equal(writes.length, 2);
});

test('a failure after the issue was closed opens a new one', () => {
  const { gh, issues } = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  report({ dir: results({}), runUrl: RUN, gh });
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  assert.deepEqual(issues.map((i) => i.state), ['closed', 'open']);
});

test('a check that did not run is a failure, never a clean run', () => {
  const { gh, issues } = fakeGh([{ number: 7, title: TITLE, body: 'old' }]);
  const dir = results({ trivy: { status: 'skipped' } });
  report({ dir, runUrl: RUN, gh });
  assert.equal(issues[0].state, 'open');
  assert.match(issues[0].body, /did not complete/);
});

test('an issue with some other title is not touched', () => {
  const { gh, issues, writes } = fakeGh([{ number: 3, title: 'Something else' }]);
  report({ dir: results({}), runUrl: RUN, gh });
  assert.equal(writes.length, 0);
  assert.equal(issues[0].state, 'open');
});

test('dry run reads and reports but writes nothing, for open, update and close', () => {
  const empty = fakeGh();
  assert.match(report({ dir: results(vulnerable), runUrl: RUN, dryRun: true, gh: empty.gh }), /would: open/);
  assert.equal(empty.writes.length, 0);

  const live = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh: live.gh });
  const before = live.writes.length;
  const more = { ...vulnerable, trivy: { status: 'failure', log: 'CVE-2026-12345' } };
  assert.match(report({ dir: results(more), runUrl: RUN, dryRun: true, gh: live.gh }), /would: update/);
  assert.match(report({ dir: results({}), runUrl: RUN, dryRun: true, gh: live.gh }), /would: close/);
  assert.equal(live.writes.length, before);
  assert.equal(live.issues[0].state, 'open');
});
