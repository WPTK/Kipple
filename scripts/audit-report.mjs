// node scripts/audit-report.mjs --dir <results> --run-url <url> [--dry-run]
//
// The weekly audit (.github/workflows/audit.yml) runs govulncheck, npm audit and Trivy and leaves, per check, a
// <check>.status file (the step outcome) and a <check>.log file in <results>. This turns that into exactly one
// tracking issue:
//   - something failed, no open issue    -> open one
//   - something failed, issue is open    -> edit its body, only when the set of findings changed
//   - everything passed, issue is open   -> close it with a comment
//   - everything passed, no open issue   -> do nothing
// The findings are fingerprinted without the run link or any timestamp, so a repeat run that sees the same
// advisories changes nothing. With --dry-run it only reads and prints what it would have done.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const TITLE = 'Weekly audit: dependency or image advisories on main';
export const LABELS = ['security', 'area:infra'];
export const ASSIGNEE = 'WPTK';
const MARK = 'audit-fingerprint:';

export const CHECKS = [
  { name: 'govulncheck', label: 'govulncheck (Go modules and standard library)', ids: /\bGO-\d{4}-\d+\b/g },
  { name: 'npm', label: 'npm audit (web production dependencies)', ids: /\bGHSA(?:-[a-z0-9]{4}){3}\b/g },
  { name: 'trivy', label: 'Trivy (container image)', ids: /\bCVE-\d{4}-\d{4,}\b/g },
];

function readOr(path, fallback) {
  return existsSync(path) ? readFileSync(path, 'utf8') : fallback;
}

// One entry per check that did not pass. A check with no status file did not run to completion, which is a failure:
// a missing result must never read as a clean run and close the issue.
export function collect(dir) {
  const failed = [];
  for (const c of CHECKS) {
    const status = readOr(join(dir, `${c.name}.status`), 'missing').trim();
    if (status === 'success') continue;
    const log = readOr(join(dir, `${c.name}.log`), '');
    const ids = [...new Set(log.match(c.ids) ?? [])].sort();
    failed.push({ ...c, status, ids });
  }
  return failed;
}

export function fingerprint(failed) {
  const canon = failed.map((f) => `${f.name}:${f.status}:${f.ids.join(',')}`).join('\n');
  return createHash('sha256').update(canon).digest('hex').slice(0, 16);
}

export function body(failed, runUrl) {
  const lines = ['The weekly audit of `main` found the following. This issue is updated when the findings change and closed by the first clean run.', ''];
  for (const f of failed) {
    lines.push(`### ${f.label}`);
    if (f.ids.length > 0) for (const id of f.ids) lines.push(`- ${id}`);
    else lines.push(f.status === 'failure' ? '- Failed without a recognisable advisory id; see the run log.' : `- The check did not complete (${f.status}); see the run log.`);
    lines.push('');
  }
  lines.push(`Run that last changed this list: ${runUrl}`, '', `<!-- ${MARK} ${fingerprint(failed)} -->`);
  return lines.join('\n');
}

const defaultGh = (args) => execFileSync('gh', args, { encoding: 'utf8' });

// `gh` is injected so the decision logic can be tested without a repository. Returns the actions taken, as text.
export function report({ dir, runUrl, dryRun, gh = defaultGh }) {
  const failed = collect(dir);
  const found = JSON.parse(
    gh(['issue', 'list', '--state', 'open', '--label', LABELS[0], '--label', LABELS[1], '--search', `"${TITLE}" in:title`, '--json', 'number,title,body', '--limit', '20']),
  ).filter((i) => i.title === TITLE);
  // A second open issue can only come from a human; the oldest is the tracked one and the rest are left alone.
  const open = found.sort((a, b) => a.number - b.number)[0];
  const act = (text, args) => {
    if (!dryRun) gh(args);
    return dryRun ? `dry run, would: ${text}` : text;
  };

  if (failed.length === 0) {
    if (!open) return 'clean run, no open issue: nothing to do';
    return act(`close #${open.number}`, ['issue', 'close', String(open.number), '--comment', `Clean run, closing: ${runUrl}`]);
  }
  const text = body(failed, runUrl);
  if (!open) {
    const args = ['issue', 'create', '--title', TITLE, '--body', text, '--assignee', ASSIGNEE];
    for (const l of LABELS) args.push('--label', l);
    return act('open a new issue', args);
  }
  if ((open.body ?? '').includes(`${MARK} ${fingerprint(failed)} `)) return `#${open.number} already lists these findings: nothing to do`;
  return act(`update #${open.number}`, ['issue', 'edit', String(open.number), '--body', text]);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const arg = (name) => {
    const i = process.argv.indexOf(name);
    return i > 0 ? process.argv[i + 1] : undefined;
  };
  const dir = arg('--dir');
  const runUrl = arg('--run-url');
  if (!dir || !runUrl) {
    console.error('usage: audit-report.mjs --dir <results> --run-url <url> [--dry-run]');
    process.exit(2);
  }
  console.log(report({ dir, runUrl, dryRun: process.argv.includes('--dry-run') }));
}
