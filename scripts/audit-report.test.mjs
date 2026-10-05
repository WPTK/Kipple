// node --test scripts/audit-report.test.mjs
// The open / update / close decisions of the weekly audit, run against a fake `gh` that keeps issues in memory, and the
// advisory-id extraction the issue text depends on.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ID_RE, report, TITLE } from './audit-report.mjs';
import { highIds } from '../.github/actions/audit-npm/high-ids.mjs';

const RUN = 'https://example.invalid/run/1';
const OPTS = { runUrl: RUN, ref: 'main', sha: 'abc123', assignee: 'octo' };

function results(checks) {
  const dir = mkdtempSync(join(tmpdir(), 'a263-audit-'));
  for (const name of ['govulncheck', 'npm', 'trivy']) {
    const { status = 'success', log = '' } = checks[name] ?? {};
    writeFileSync(join(dir, `${name}.status`), `${status}\n`);
    writeFileSync(join(dir, `${name}.log`), log);
  }
  return dir;
}

// Minimal in-memory GitHub: list/create/edit/comment/close, recording every write. The body arrives on stdin
// (`--body-file -`) exactly as with the real gh.
function fakeGh(initial = []) {
  const issues = initial.map((i) => ({ state: 'open', body: '', comments: [], ...i }));
  const writes = [];
  const flag = (args, name) => args[args.indexOf(name) + 1];
  const gh = (args, input) => {
    const [, verb] = args;
    if (verb === 'list') return JSON.stringify(issues.filter((i) => i.state === 'open'));
    writes.push(args);
    if (verb === 'create') {
      assert.equal(flag(args, '--body-file'), '-');
      issues.push({ number: issues.length + 1, state: 'open', title: flag(args, '--title'), body: input, comments: [], args });
    } else if (verb === 'edit') {
      assert.equal(flag(args, '--body-file'), '-');
      issues.find((i) => i.number === Number(args[2])).body = input;
    } else if (verb === 'comment') {
      issues.find((i) => i.number === Number(args[2])).comments.push(input);
    } else if (verb === 'close') {
      issues.find((i) => i.number === Number(args[2])).state = 'closed';
    }
    return '';
  };
  return { gh, issues, writes };
}

const vulnerable = {
  govulncheck: { status: 'failure', log: 'Vulnerability #1: GO-2026-0001\nVulnerability #2: GO-2026-0002\nGO-2026-0001 again' },
  npm: { status: 'failure', log: 'GHSA-abcd-2345-wxyz\n' },
};

test('a failing check opens one issue naming the advisories, with labels, assignee and audited ref', () => {
  const { gh, issues, writes } = fakeGh();
  report({ dir: results(vulnerable), ...OPTS, gh });
  assert.equal(issues.length, 1);
  assert.equal(writes.length, 1);
  assert.equal(issues[0].title, TITLE);
  assert.match(issues[0].body, /GO-2026-0001/);
  assert.match(issues[0].body, /GO-2026-0002/);
  assert.match(issues[0].body, /GHSA-abcd-2345-wxyz/);
  assert.match(issues[0].body, /Audited: main @ abc123/);
  assert.equal(issues[0].body.match(/GO-2026-0001/g).length, 1, 'ids are listed once');
  for (const want of ['--assignee', 'octo', 'security', 'area:infra']) assert.ok(issues[0].args.includes(want), want);
});

test('no assignee flag when none is given', () => {
  const { gh, issues } = fakeGh();
  report({ dir: results(vulnerable), runUrl: RUN, gh });
  assert.ok(!issues[0].args.includes('--assignee'));
});

test('a second run with the same findings changes nothing, even from another run, ref or sha', () => {
  const { gh, writes } = fakeGh();
  report({ dir: results(vulnerable), ...OPTS, gh });
  const out = report({ dir: results(vulnerable), runUrl: 'https://example.invalid/run/2', ref: 'other', sha: 'def456', gh });
  assert.equal(writes.length, 1, 'only the first run wrote');
  assert.match(out, /nothing to do/);
});

test('different findings update the same issue (body and a comment) instead of opening another', () => {
  const { gh, issues, writes } = fakeGh();
  report({ dir: results(vulnerable), ...OPTS, gh });
  const more = { ...vulnerable, trivy: { status: 'failure', log: 'libfoo CVE-2026-12345 HIGH' } };
  report({ dir: results(more), ...OPTS, runUrl: 'https://example.invalid/run/2', gh });
  assert.equal(issues.length, 1);
  assert.deepEqual(writes.slice(1).map((w) => w[1]), ['edit', 'comment']);
  assert.match(issues[0].body, /CVE-2026-12345/);
  assert.match(issues[0].body, /run\/2/);
  assert.equal(issues[0].comments.length, 1);
  assert.match(issues[0].comments[0], /run\/2/);
});

test('a clean run closes the open issue and a later clean run does nothing', () => {
  const { gh, issues, writes } = fakeGh();
  report({ dir: results(vulnerable), ...OPTS, gh });
  report({ dir: results({}), ...OPTS, gh });
  assert.equal(issues[0].state, 'closed');
  assert.equal(writes.length, 2);
  assert.match(report({ dir: results({}), ...OPTS, gh }), /nothing to do/);
  assert.equal(writes.length, 2);
});

test('a failure after the issue was closed opens a new one', () => {
  const { gh, issues } = fakeGh();
  report({ dir: results(vulnerable), ...OPTS, gh });
  report({ dir: results({}), ...OPTS, gh });
  report({ dir: results(vulnerable), ...OPTS, gh });
  assert.deepEqual(issues.map((i) => i.state), ['closed', 'open']);
});

test('a check that did not run is a failure, never a clean run', () => {
  const { gh, issues } = fakeGh([{ number: 7, title: TITLE, body: 'old' }]);
  report({ dir: results({ trivy: { status: 'skipped' } }), ...OPTS, gh });
  assert.equal(issues[0].state, 'open');
  assert.match(issues[0].body, /did not complete \(skipped\)/);
});

test('an unknown or garbled status counts as missing, not as clean', () => {
  const { gh, issues } = fakeGh([{ number: 7, title: TITLE, body: 'old' }]);
  report({ dir: results({ npm: { status: 'succes' }, govulncheck: { status: '' } }), ...OPTS, gh });
  assert.equal(issues[0].state, 'open');
  assert.match(issues[0].body, /did not complete \(missing\)/);
});

test('a build failure is named as such, not as a missing scan', () => {
  const { gh, issues } = fakeGh();
  report({ dir: results({ trivy: { status: 'build-failure' } }), ...OPTS, gh });
  assert.match(issues[0].body, /The image did not build/);
});

test('the issue is found by title even if its labels were edited', () => {
  // The fake list ignores labels entirely, as the real call no longer filters on them.
  const { gh, issues } = fakeGh([{ number: 4, title: TITLE, body: '' }]);
  report({ dir: results({}), ...OPTS, gh });
  assert.equal(issues[0].state, 'closed');
});

test('an issue with some other title is not touched', () => {
  const { gh, issues, writes } = fakeGh([{ number: 3, title: 'Something else' }]);
  report({ dir: results({}), ...OPTS, gh });
  assert.equal(writes.length, 0);
  assert.equal(issues[0].state, 'open');
});

test('dry run reads and reports but writes nothing, for open, update and close', () => {
  const empty = fakeGh();
  assert.match(report({ dir: results(vulnerable), ...OPTS, dryRun: true, gh: empty.gh }), /would: open/);
  assert.equal(empty.writes.length, 0);

  const live = fakeGh();
  report({ dir: results(vulnerable), ...OPTS, gh: live.gh });
  const before = live.writes.length;
  const more = { ...vulnerable, trivy: { status: 'failure', log: 'CVE-2026-12345' } };
  assert.match(report({ dir: results(more), ...OPTS, dryRun: true, gh: live.gh }), /would: update/);
  assert.match(report({ dir: results({}), ...OPTS, dryRun: true, gh: live.gh }), /would: close/);
  assert.equal(live.writes.length, before);
  assert.equal(live.issues[0].state, 'open');
});

test('advisory id pattern: CVE, GHSA and GO ids match, near misses do not', () => {
  const text = 'CVE-2026-12345 CVE-2026-1 GHSA-abcd-2345-wxyz GHSA-abc-2345-wxyz GO-2026-0001 GO-2026 XCVE-2026-12345 GHSA-abcd-2345-wxyz-extra';
  assert.deepEqual(text.match(ID_RE), ['CVE-2026-12345', 'GHSA-abcd-2345-wxyz', 'GO-2026-0001', 'GHSA-abcd-2345-wxyz']);
});

test('npm: only high and critical advisories are named, from the --json report', () => {
  const via = (severity, id) => ({ source: 1, name: 'x', severity, url: `https://github.com/advisories/${id}` });
  const json = JSON.stringify({
    vulnerabilities: {
      a: { via: [via('high', 'GHSA-aaaa-bbbb-cccc'), 'b'] },
      b: { via: [via('critical', 'GHSA-dddd-eeee-ffff')] },
      c: { via: [via('moderate', 'GHSA-gggg-hhhh-iiii'), via('low', 'GHSA-jjjj-kkkk-llll')] },
      d: { via: [{ severity: 'high', url: 'https://example.invalid/no-id' }] },
    },
  });
  assert.deepEqual(highIds(json), ['GHSA-aaaa-bbbb-cccc', 'GHSA-dddd-eeee-ffff']);
  assert.deepEqual(highIds('not json'), []);
  assert.deepEqual(highIds('{}'), []);
});
