// node --test scripts/settled-check.test.mjs
// The currency checks of the weekly audit, offline: every check is a pure function of data, and the collection step
// takes its `gh` and endoflife.date lookups as arguments.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  checkAuditIssue, checkBlockers, checkCi, checkDependabot, checkNodeMajor, checkRelease, checkRestoreDrill, checkRuntimes,
  checkSchedules, checkSupport, isScheduled, render, settledChecks,
} from './settled-check.mjs';
import { TITLE } from './audit-report.mjs';

const now = Date.parse('2026-10-26T12:00:00Z');
const ago = (d) => new Date(now - d * 86_400_000).toISOString();
const inDays = (d) => new Date(now + d * 86_400_000).toISOString().slice(0, 10);

test('CI: green, red, running and absent runs', () => {
  const run = (name, status, conclusion) => ({ name, status, conclusion });
  assert.equal(checkCi({ sha: 'abcdef123', runs: [run('CI', 'completed', 'success'), run('Links', 'completed', 'skipped')] }).status, 'pass');
  const red = checkCi({ sha: 'abcdef123', runs: [run('CI', 'completed', 'failure')] });
  assert.equal(red.status, 'fail');
  assert.match(red.evidence, /CI failure/);
  assert.equal(checkCi({ sha: 'abcdef123', runs: [run('CI', 'in_progress', null)] }).status, 'unknown');
  assert.equal(checkCi({ sha: 'abcdef123', runs: [] }).status, 'unknown');
  // This workflow's own run is in progress and must not hide a green head.
  assert.equal(checkCi({ sha: 'abcdef123', runs: [run('CI', 'completed', 'success'), run('Weekly audit', 'in_progress', null)] }).status, 'pass');
});

test('Release: latest release must be the newest stable tag; cosign result is reported', () => {
  const tagNames = ['v1.5.0', 'v1.10.0', 'v1.9.2', 'v2.0.0-rc.1', 'not-a-version'];
  assert.equal(checkRelease({ latestRelease: 'v1.10.0', tagNames, cosign: null }).evidence, 'v1.10.0 is the latest release and the newest stable tag; cosign not checked');
  assert.equal(checkRelease({ latestRelease: 'v1.9.2', tagNames, cosign: null }).status, 'fail');
  assert.equal(checkRelease({ latestRelease: 'v1.10.0', tagNames, cosign: { tag: 'v1.10.0', outcome: 'success' } }).status, 'pass');
  const bad = checkRelease({ latestRelease: 'v1.10.0', tagNames, cosign: { tag: 'v1.10.0', outcome: 'failure' } });
  assert.equal(bad.status, 'fail');
  assert.match(bad.evidence, /cosign verify failed for v1.10.0/);
  // A step that was skipped, cancelled or had no tag to verify was not checked: unknown, never green.
  for (const outcome of ['skipped', 'cancelled', '']) assert.equal(checkRelease({ latestRelease: 'v1.10.0', tagNames, cosign: { tag: '', outcome } }).status, 'unknown', outcome);
  assert.equal(checkRelease({ latestRelease: 'v1.10.0', tagNames, cosign: { tag: 'v1.9.2', outcome: 'success' } }).status, 'unknown');
  assert.equal(checkRelease({ latestRelease: undefined, tagNames, cosign: null }).status, 'unknown');
});

test('Security advisories: the audit issue this run maintains is not counted against itself', () => {
  assert.equal(checkAuditIssue({ openIssues: [] }).status, 'pass');
  assert.equal(checkAuditIssue({ openIssues: [{ number: 9, title: TITLE }] }).status, 'pass');
  assert.equal(checkAuditIssue({ openIssues: [{ number: 4, title: 'something else' }, { number: 9, title: TITLE }] }).evidence, 'open: #4');
  // A person's duplicate is not the one the run maintains (that is the oldest).
  assert.equal(checkAuditIssue({ openIssues: [{ number: 9, title: TITLE }, { number: 12, title: TITLE }] }).evidence, 'open: #12');
});

test('Blockers: P0, P1 and freeze each fail the check', () => {
  const none = { P0: [], P1: [], freeze: [] };
  assert.equal(checkBlockers({ byLabel: none }).status, 'pass');
  const r = checkBlockers({ byLabel: { ...none, P1: [{ number: 3 }], freeze: [{ number: 5 }, { number: 6 }] } });
  assert.equal(r.status, 'fail');
  assert.equal(r.evidence, 'P1: #3; freeze: #5, #6');
});

test('Dependabot: only a PR older than 30 days is stale', () => {
  assert.equal(checkDependabot({ prs: [{ number: 1, createdAt: ago(30) }], now }).status, 'pass');
  const r = checkDependabot({ prs: [{ number: 1, createdAt: ago(31) }, { number: 2, createdAt: ago(2) }], now });
  assert.equal(r.status, 'fail');
  assert.match(r.evidence, /1 of 2 stale: #1 \(31d\)/);
});

test('Support: 180 days or more passes; less fails; no date passes; missing data is unknown', () => {
  const rows = [{ cycle: '22', eol: inDays(181) }, { cycle: '20', eol: inDays(10) }, { cycle: '1.27', eol: false }, { cycle: '9', eol: inDays(-5) }];
  const s = (cycle, r = rows) => checkSupport({ label: 'X', cycle, rows: r, now }).status;
  assert.equal(s('22'), 'pass');
  assert.equal(s('20'), 'fail');
  assert.equal(s('9'), 'fail');
  assert.equal(s('1.27'), 'pass');
  assert.equal(s('7'), 'unknown'); // cycle not listed
  assert.equal(s('22', null), 'unknown'); // API failure is not a fail
  assert.equal(s('22', [{ cycle: '22', eol: null }]), 'unknown'); // an unreadable date is not a fail
  assert.equal(s('22', [{ cycle: '22', eol: 'soon' }]), 'unknown');
  assert.equal(s('22', [{ cycle: '22', eol: inDays(180) }]), 'pass'); // exactly 180 days away
  assert.equal(s('22', [{ cycle: '22', eol: inDays(179) }]), 'fail');
  assert.equal(s(null), 'unknown');
});

test('Runtimes: read from the Dockerfile and go.mod; a floating alpine tag is unknown', () => {
  const dockerfile = [
    'FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:aa AS web',
    'FROM golang:1.27.3-alpine@sha256:bb AS build',
    'FROM gcr.io/distroless/static-debian13:nonroot@sha256:cc',
  ].join('\n');
  const eol = { nodejs: [{ cycle: '22', eol: inDays(400) }], go: [{ cycle: '1.27', eol: false }], debian: [{ cycle: '13', eol: inDays(100) }], 'alpine-linux': null };
  const r = checkRuntimes({ dockerfile, goMod: 'module x\n\ngo 1.27\n\ntoolchain go1.27.3\n', eol, now });
  assert.deepEqual(r.map((c) => c.status), ['pass', 'pass', 'fail', 'unknown']);
  const pinned = checkRuntimes({ dockerfile: dockerfile.replace('1.27.3-alpine', '1.27.3-alpine3.22'), goMod: 'go 1.27\n', eol: { ...eol, 'alpine-linux': [{ cycle: '3.22', eol: inDays(300) }] }, now });
  assert.equal(pinned[3].status, 'pass');
});

test('Node major: every workflow must match the Dockerfile', () => {
  const dockerfile = 'FROM node:22-alpine@sha256:aa AS web\n';
  assert.equal(checkNodeMajor({ dockerfile, workflows: { 'a.yml': 'node-version: "22"', 'b.yml': 'name: no node' } }).status, 'pass');
  const r = checkNodeMajor({ dockerfile, workflows: { 'a.yml': 'node-version: "22"', 'b.yml': "node-version: '24'\n node-version: 24" } });
  assert.equal(r.status, 'fail');
  assert.match(r.evidence, /b\.yml: 24/);
  assert.equal(checkNodeMajor({ dockerfile: 'FROM scratch', workflows: {} }).status, 'unknown');
  // A commented-out line sets nothing; the evidence counts the settings read.
  const ok = checkNodeMajor({ dockerfile, workflows: { 'a.yml': '# node-version: 20\n  node-version: "22" # now', 'b.yml': 'x: 1' } });
  assert.equal(ok.status, 'pass');
  assert.match(ok.evidence, /1 node-version settings in 1 of 2 workflows/);
});

test('Schedules: detected under on:, and each needs a finished scheduled run in the window', () => {
  assert.equal(isScheduled('on:\n  schedule:\n    - cron: "0 6 * * 1"\n'), true);
  assert.equal(isScheduled('on:\n  push:\n# schedule:\n'), false);
  const run = (event, status, days) => ({ event, status, created_at: ago(days) });
  assert.equal(checkSchedules({ runsByFile: { 'a.yml': [run('schedule', 'completed', 3)], 'b.yml': [run('schedule', 'completed', 14)] }, now }).status, 'pass');
  const r = checkSchedules({ runsByFile: { 'a.yml': [run('schedule', 'completed', 3)], 'b.yml': [run('schedule', 'completed', 15)], 'c.yml': [run('schedule', 'in_progress', 1)], 'd.yml': [] }, now });
  assert.equal(r.status, 'fail');
  assert.equal(r.evidence, 'no scheduled run: b.yml, c.yml, d.yml');
  assert.equal(checkSchedules({ runsByFile: {}, now }).status, 'unknown');
});

test('Restore drill: unknown without a dated line, otherwise by age', () => {
  assert.equal(checkRestoreDrill({ handover: null, now }).evidence, 'handover page not present');
  const undated = checkRestoreDrill({ handover: '# Handover\nNo drill yet.\n', now });
  assert.equal(undated.status, 'unknown');
  assert.equal(undated.evidence, 'no dated restore-drill line in the handover page'); // present but undated differs from absent
  assert.equal(checkRestoreDrill({ handover: 'Restore drill: 2027-01-01\n', now }).status, 'unknown'); // a future date is not a pass
  assert.equal(checkRestoreDrill({ handover: `Restore drill: ${inDays(-180)}\n`, now }).status, 'pass'); // exactly 180 days ago
  assert.equal(checkRestoreDrill({ handover: 'Restore drill: 2026-09-01\n', now }).status, 'pass');
  assert.equal(checkRestoreDrill({ handover: 'Last restore drill done 2025-01-02\n', now }).status, 'fail');
  assert.equal(checkRestoreDrill({ handover: 'Backup taken 2026-09-01\n', now }).status, 'unknown'); // a date on another line does not count
});

test('render: one line per check with a summary', () => {
  const text = render([{ name: 'A', status: 'pass', evidence: 'ok' }, { name: 'B', status: 'unknown', evidence: 'x' }], { now });
  assert.equal(text, 'Checked 2026-10-26: 1 pass, 0 fail, 1 unknown.\n\n- PASS: A. ok\n- UNKNOWN: B. x');
});

test('settledChecks: a failed lookup is unknown, never a throw or a fail', async () => {
  const root = mkdtempSync(join(tmpdir(), 'settled-'));
  mkdirSync(join(root, '.github', 'workflows'), { recursive: true });
  writeFileSync(join(root, 'Dockerfile'), 'FROM node:22-alpine AS web\nFROM gcr.io/distroless/static-debian13:nonroot\n');
  writeFileSync(join(root, 'go.mod'), 'go 1.27\n');
  writeFileSync(join(root, '.github', 'workflows', 'a.yml'), 'on:\n  schedule:\n    - cron: "1 1 * * 1"\n');
  const calls = [];
  const gh = (args) => {
    calls.push(args.join(' '));
    throw new Error('gh: not logged in\nmore');
  };
  const checks = await settledChecks({ repo: 'o/r', root, now, gh, fetchJson: async () => { throw new Error('offline'); } });
  assert.equal(checks.length, 12); // eight checks, the runtime one split in four
  // Only the Node major check reads local files alone, so it is the one that still answers.
  const answered = checks.filter((c) => c.status !== 'unknown').map((c) => c.name);
  assert.deepEqual(answered, ['Node major is the same in the Dockerfile and every workflow']);
  assert.match(checks[0].evidence, /gh: not logged in/);
  assert.ok(calls.some((c) => c.startsWith('api repos/o/r/actions/workflows/a.yml/runs?event=schedule')));
});

// A repository and a fake `gh` that answer every lookup, so the whole collection can be checked end to end.
function fixture({ tags = ['v1.5.1', 'v1.5.0'] } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'settled-'));
  mkdirSync(join(root, '.github', 'workflows'), { recursive: true });
  writeFileSync(join(root, 'Dockerfile'), 'FROM node:22-alpine AS web\nFROM gcr.io/distroless/static-debian13:nonroot\n');
  writeFileSync(join(root, 'go.mod'), 'go 1.27\n');
  writeFileSync(join(root, '.github', 'workflows', 'a.yml'), 'on:\n  schedule:\n    - cron: "1 1 * * 1"\njobs:\n  x:\n    steps:\n      - with:\n          node-version: "22"\n');
  const gh = (args) => {
    const line = args.join(' ');
    if (line.includes('--paginate')) return `${tags.join('\n')}\n`;
    if (line.includes('commits/main')) return JSON.stringify({ sha: 'abcdef1234' });
    if (line.includes('actions/runs?head_sha=abcdef1234')) return JSON.stringify({ workflow_runs: [{ name: 'CI', status: 'completed', conclusion: 'success' }] });
    if (line.includes('releases/latest')) return JSON.stringify({ tag_name: 'v1.5.1' });
    if (line.includes('workflows/a.yml/runs')) return JSON.stringify({ workflow_runs: [{ event: 'schedule', status: 'completed', created_at: ago(2) }] });
    if (line.startsWith('issue list') || line.startsWith('pr list')) return '[]';
    throw new Error(`unexpected gh call: ${line}`);
  };
  const rows = {
    nodejs: [{ cycle: '22', eol: inDays(400) }],
    go: [{ cycle: '1.27', eol: false }],
    debian: [{ cycle: '13', eol: inDays(900) }],
    'alpine-linux': [],
  };
  const fetchJson = async (url) => rows[/api\/(.+)\.json$/.exec(url)[1]];
  return { root, gh, fetchJson };
}
const byName = (checks, re) => checks.find((c) => re.test(c.name));

test('settledChecks: a healthy repository passes, with unknown only where nothing is pinned or present', async () => {
  const { root, gh, fetchJson } = fixture();
  const checks = await settledChecks({ repo: 'o/r', root, now, gh, fetchJson, cosign: { tag: 'v1.5.1', outcome: 'success' } });
  assert.deepEqual(checks.map((c) => c.status), ['pass', 'pass', 'pass', 'pass', 'pass', 'pass', 'pass', 'pass', 'unknown', 'pass', 'pass', 'unknown']);
  assert.match(byName(checks, /Latest release/).evidence, /cosign verify passed for v1.5.1/);
});

test('settledChecks: the newest tag is found past the first page and in any order', async () => {
  const many = Array.from({ length: 250 }, (_, i) => `v0.${i}.0`);
  const a = fixture({ tags: [...many, 'v1.5.1', ...many.slice(0, 5)] });
  assert.equal(byName(await settledChecks({ repo: 'o/r', root: a.root, now, gh: a.gh, fetchJson: a.fetchJson }), /Latest release/).status, 'pass');
  const b = fixture({ tags: [...many, 'v1.6.0', 'v1.5.1'] });
  assert.equal(byName(await settledChecks({ repo: 'o/r', root: b.root, now, gh: b.gh, fetchJson: b.fetchJson }), /Latest release/).status, 'fail');
});

test('settledChecks: an endoflife.date body that is not a list is a failed lookup, not a crash or a fail', async () => {
  const { root, gh } = fixture();
  for (const body of [{ message: 'rate limited' }, null, 'oops']) {
    const checks = await settledChecks({ repo: 'o/r', root, now, gh, fetchJson: async () => body });
    const runtimes = checks.filter((c) => /supported for/.test(c.name));
    assert.equal(runtimes.length, 4);
    assert.ok(runtimes.every((c) => c.status === 'unknown'), JSON.stringify(runtimes));
  }
});

test('settledChecks: a cosign step that failed or did not run never reads as green', async () => {
  const { root, gh, fetchJson } = fixture();
  const statusFor = async (cosign) => byName(await settledChecks({ repo: 'o/r', root, now, gh, fetchJson, cosign }), /Latest release/).status;
  assert.equal(await statusFor({ tag: 'v1.5.1', outcome: 'failure' }), 'fail');
  assert.equal(await statusFor({ tag: '', outcome: 'skipped' }), 'unknown'); // the release lookup failed, so nothing was verified
  assert.equal(await statusFor({ tag: 'v1.5.1', outcome: 'cancelled' }), 'unknown');
});
