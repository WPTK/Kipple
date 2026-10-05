// node scripts/audit-report.mjs --dir <results> --run-url <url> --ref <ref> --sha <sha> [--assignee <login>]
//                               --default-branch <name> --event <name> [--dry-run-input true]
//
// The weekly audit (.github/workflows/audit.yml) runs govulncheck, npm audit and Trivy and leaves, per check, a
// <check>.status file (the step outcome) and a <check>.log file in <results>. This turns that into exactly one
// tracking issue, found by its exact title:
//   - something failed, no open issue    -> open one
//   - something failed, issue is open    -> edit its body and add a short comment, only when the set of findings
//                                           changed (the edit keeps the list current, the comment notifies the
//                                           assignee and keeps the history)
//   - everything passed, issue is open   -> close it with a comment
//   - everything passed, no open issue   -> do nothing
// The findings are fingerprinted without the run link, the audited ref or sha, or any timestamp, so a repeat run that
// sees the same advisories changes nothing. With --dry-run it only reads and prints what it would have done.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const TITLE = 'Weekly audit: dependency or image advisories on main';
export const LABELS = ['security', 'area:infra'];
const MARK = 'audit-fingerprint:';

// What a check's log can name: a CVE (Trivy), a GitHub advisory (npm) or a Go vulnerability (govulncheck).
export const ID_RE = /\b(?:CVE-\d{4}-\d{4,}|GHSA(?:-[a-z0-9]{4}){3}|GO-\d{4}-\d+)\b/g;

export const CHECKS = [
  { name: 'govulncheck', label: 'govulncheck (Go modules and standard library)' },
  { name: 'npm', label: 'npm audit (web production dependencies, high and critical)' },
  { name: 'trivy', label: 'Trivy (container image)' },
];

// What the workflow may write into a .status file. Anything else is treated as a check that did not report.
const STATUSES = new Set(['success', 'failure', 'cancelled', 'skipped', 'missing', 'build-failure']);

function readOr(path, fallback) {
  return existsSync(path) ? readFileSync(path, 'utf8') : fallback;
}

// One entry per check that did not pass. A check with no (or an unknown) status did not run to completion, which is a
// failure: a missing result must never read as a clean run and close the issue.
export function collect(dir) {
  const failed = [];
  for (const c of CHECKS) {
    const raw = readOr(join(dir, `${c.name}.status`), 'missing').trim();
    const status = STATUSES.has(raw) ? raw : 'missing';
    if (status === 'success') continue;
    const ids = [...new Set(readOr(join(dir, `${c.name}.log`), '').match(ID_RE) ?? [])].sort();
    failed.push({ ...c, status, ids });
  }
  return failed;
}

export function fingerprint(failed) {
  const canon = failed.map((f) => `${f.name}:${f.status}:${f.ids.join(',')}`).join('\n');
  return createHash('sha256').update(canon).digest('hex').slice(0, 16);
}

function detail(f) {
  if (f.ids.length > 0) return f.ids.map((id) => `- ${id}`);
  if (f.status === 'failure') return ['- Failed without a recognisable advisory id; see the run log.'];
  if (f.status === 'build-failure') return ['- The image did not build, so it was not scanned; see the run log.'];
  return [`- The check did not complete (${f.status}); see the run log.`];
}

export function body(failed, { runUrl, ref, sha }) {
  const lines = ['The weekly audit found the following. This issue is updated when the findings change and closed by the first clean run.', ''];
  for (const f of failed) lines.push(`### ${f.label}`, ...detail(f), '');
  lines.push(`Last changed by: ${runUrl}`, `Audited: ${ref} @ ${sha}`, '', `<!-- ${MARK} ${fingerprint(failed)} -->`);
  return lines.join('\n');
}

// Whether this run may write to the issue. Only a run on the default branch may; any other ref, or a manual run with
// dry_run set, reports what it would have done. The default branch must be known: an empty value (a failed lookup) throws
// instead of quietly turning every run into a dry run.
export function decideDryRun({ ref, defaultBranch, eventName, dryRunInput }) {
  if (!defaultBranch) throw new Error('the default branch is unknown, so it is not safe to decide whether this run may write');
  if (!ref) throw new Error('the run has no ref');
  if (eventName === 'workflow_dispatch' && dryRunInput) return true;
  return ref !== defaultBranch;
}

const defaultGh =(args, input) => execFileSync('gh', args, { encoding: 'utf8', input });

// `gh` is injected so the decision logic can be tested without a repository. Returns the actions taken, as text.
export function report({ dir, runUrl, ref = 'unknown', sha = 'unknown', assignee, dryRun, gh = defaultGh }) {
  const failed = collect(dir);
  // The title is the identity; labels can be edited by hand and must not hide the issue from the audit.
  const found = JSON.parse(
    gh(['issue', 'list', '--state', 'open', '--search', `"${TITLE}" in:title`, '--json', 'number,title,body', '--limit', '20']),
  ).filter((i) => i.title === TITLE);
  // A second open issue can only come from a human; the oldest is the tracked one and the rest are left alone.
  const open = found.sort((a, b) => a.number - b.number)[0];
  const act = (text, calls) => {
    if (!dryRun) for (const [args, input] of calls) gh(args, input);
    return dryRun ? `dry run, would: ${text}` : text;
  };

  if (failed.length === 0) {
    if (!open) return 'clean run, no open issue: nothing to do';
    return act(`close #${open.number}`, [[['issue', 'close', String(open.number), '--comment', `Clean run, closing: ${runUrl}`]]]);
  }
  const text = body(failed, { runUrl, ref, sha });
  if (!open) {
    const args = ['issue', 'create', '--title', TITLE, '--body-file', '-'];
    for (const l of LABELS) args.push('--label', l);
    if (!assignee) return act('open a new issue', [[args, text]]);
    if (dryRun) return act('open a new issue', []);
    // An organization cannot be assigned. The issue matters more than its assignee, so retry once without one and say so.
    try {
      gh([...args, '--assignee', assignee], text);
      return 'open a new issue';
    } catch {
      const note = `\n\nThe assignee \`${assignee}\` could not be set (the repository owner is probably not a user). Please assign someone.`;
      gh(args, text.replace(/\n\n<!-- /, `${note}\n\n<!-- `));
      return 'open a new issue (without an assignee)';
    }
  }
  if ((open.body ?? '').includes(`${MARK} ${fingerprint(failed)} `)) return `#${open.number} already lists these findings: nothing to do`;
  const note = `The findings changed. The description now lists them. Run: ${runUrl}`;
  return act(`update #${open.number}`, [
    [['issue', 'edit', String(open.number), '--body-file', '-'], text],
    [['issue', 'comment', String(open.number), '--body-file', '-'], note],
  ]);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const arg = (name) => {
    const i = process.argv.indexOf(name);
    return i > 0 ? process.argv[i + 1] : undefined;
  };
  const dir = arg('--dir');
  const runUrl = arg('--run-url');
  if (!dir || !runUrl) {
    console.error('usage: audit-report.mjs --dir <results> --run-url <url> [--ref <ref>] [--sha <sha>] [--assignee <login>] --default-branch <name> --event <name> [--dry-run-input true]');
    process.exit(2);
  }
  const dryRun = decideDryRun({
    ref: arg('--ref'),
    defaultBranch: arg('--default-branch'),
    eventName: arg('--event'),
    dryRunInput: arg('--dry-run-input') === 'true',
  });
  console.log(report({ dir, runUrl, ref: arg('--ref'), sha: arg('--sha'), assignee: arg('--assignee'), dryRun }));
}
