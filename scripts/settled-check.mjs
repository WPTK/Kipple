// node scripts/settled-check.mjs [--repo OWNER/NAME] [--out <file>] [--cosign-tag <tag> --cosign-outcome <success|failure|...>]
//
// Is the project "settled"? Eight currency checks, each reported as pass, fail or unknown with one line of evidence.
// Run by the weekly audit (.github/workflows/audit.yml), which puts the result in the tracking issue's "Currency" section
// and in the run summary. Nothing here writes to GitHub; it reads with `gh` and endoflife.date and prints markdown.
//
// A check that cannot get its data (the API is down, a file is missing) is `unknown`, never `fail`: a flaky lookup must not
// look like a project problem. Exit code is 0 unless the script itself breaks.
import { execFileSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { TITLE as AUDIT_TITLE } from './audit-report.mjs';
import { dockerfileAlpineCycle, dockerfileDebianMajor, dockerfileNodeMajor, goModCycle, workflowNodeMajors } from './toolchain.mjs';

export const SUPPORT_DAYS = 180; // a pinned runtime must stay supported this long from today
export const STALE_PR_DAYS = 30; // a Dependabot PR open longer than this is stale
export const SCHEDULE_DAYS = 14; // a scheduled workflow must have run within this window
export const RESTORE_DRILL_DAYS = 180; // the dated restore drill line must be newer than this
const DAY = 86_400_000;

const pass = (name, evidence) => ({ name, status: 'pass', evidence });
const fail = (name, evidence) => ({ name, status: 'fail', evidence });
const unknown = (name, evidence) => ({ name, status: 'unknown', evidence });
// Whole calendar days (UTC) from one instant to another, so "exactly 180 days away" is 180 whatever the time of day.
const days = (from, to) => Math.floor(to / DAY) - Math.floor(from / DAY);
const date = (ms) => new Date(ms).toISOString().slice(0, 10);

// ---- 1. CI on main's head, and the latest release -------------------------------------------------------------------

const parseTag = (t) => {
  const m = /^v(\d+)\.(\d+)\.(\d+)(-.+)?$/.exec(t);
  return m ? { tag: t, n: [Number(m[1]), Number(m[2]), Number(m[3])], pre: Boolean(m[4]) } : null;
};
const cmpTags = (a, b) => a.n[0] - b.n[0] || a.n[1] - b.n[1] || a.n[2] - b.n[2];

// runs: workflow runs for main's head commit. This workflow's own run is excluded by name, since it is in progress.
export function checkCi({ sha, runs, selfName = 'Weekly audit' }) {
  const name = 'CI is green on main';
  const mine = runs.filter((r) => r.name !== selfName);
  const head = sha.slice(0, 7);
  if (mine.length === 0) return unknown(name, `no workflow run found for ${head}`);
  const bad = mine.filter((r) => r.status === 'completed' && !['success', 'skipped', 'neutral'].includes(r.conclusion));
  if (bad.length > 0) return fail(name, `${head}: ${bad.map((r) => `${r.name} ${r.conclusion}`).join(', ')}`);
  const pending = mine.filter((r) => r.status !== 'completed');
  if (pending.length > 0) return unknown(name, `${head}: ${pending.map((r) => r.name).join(', ')} still running`);
  return pass(name, `${head}: ${mine.length} workflow runs, all green`);
}

// latestRelease: tag of GitHub's latest release (stable only). tagNames: every tag name, in any order. cosign: null when
// the caller did not run the verify at all (a local run), otherwise { tag, outcome } from the workflow's steps. The
// outcome is what the step ended as: success and failure are answers; anything else (skipped, cancelled, no tag to
// verify) means the signature was not checked, which is unknown, never green.
export function checkRelease({ latestRelease, tagNames, cosign }) {
  const name = 'Latest release is the newest tag';
  const stable = tagNames.map(parseTag).filter((t) => t && !t.pre).sort(cmpTags);
  const newest = stable.at(-1)?.tag;
  if (!latestRelease || !newest) return unknown(name, 'no release or no version tag found');
  const tagText = latestRelease === newest ? `${newest} is the latest release and the newest stable tag` : `latest release ${latestRelease}, newest stable tag ${newest}`;
  if (latestRelease !== newest) return fail(name, tagText);
  if (!cosign) return pass(name, `${tagText}; cosign not checked`);
  if (cosign.outcome === 'failure') return fail(name, `${tagText}; cosign verify failed for ${cosign.tag}`);
  if (cosign.outcome !== 'success') return unknown(name, `${tagText}; cosign not checked (step ${cosign.outcome || 'did not run'})`);
  if (cosign.tag !== newest) return unknown(name, `${tagText}; cosign verified ${cosign.tag}, not ${newest}`);
  return pass(name, `${tagText}; cosign verify passed for ${cosign.tag}`);
}

// ---- 2. The audit issue ---------------------------------------------------------------------------------------------

// openIssues: open issues labelled `security` (number, title). The tracking issue this run is maintaining (found by its
// exact title, as audit-report.mjs does) is the audit's own state, not a second advisory, so it is left out: counting it
// would make a failing audit always fail this check as well. A person's duplicate with the same title is not the run's
// and still counts.
export function checkAuditIssue({ openIssues }) {
  const name = 'No other open security advisory';
  const tracked = openIssues.filter((i) => i.title === AUDIT_TITLE).sort((a, b) => a.number - b.number)[0];
  const other = openIssues.filter((i) => i !== tracked);
  return other.length === 0 ? pass(name, 'none open besides the audit issue') : fail(name, `open: ${other.map((i) => `#${i.number}`).join(', ')}`);
}

// ---- 3. P0, P1 and the freeze list ----------------------------------------------------------------------------------

// byLabel: { P0: [issue], P1: [issue], freeze: [issue] }
export function checkBlockers({ byLabel }) {
  const name = 'No open P0 or P1, freeze list resolved';
  const parts = ['P0', 'P1', 'freeze'].filter((l) => byLabel[l].length > 0).map((l) => `${l}: ${byLabel[l].map((i) => `#${i.number}`).join(', ')}`);
  return parts.length === 0 ? pass(name, 'none open') : fail(name, parts.join('; '));
}

// ---- 4. Dependabot PRs ----------------------------------------------------------------------------------------------

export function checkDependabot({ prs, now }) {
  const name = `No Dependabot PR older than ${STALE_PR_DAYS} days`;
  const stale = prs.filter((p) => days(Date.parse(p.createdAt), now) > STALE_PR_DAYS);
  if (stale.length === 0) return pass(name, `${prs.length} open, none stale`);
  const list = stale.map((p) => `#${p.number} (${days(Date.parse(p.createdAt), now)}d)`).join(', ');
  return fail(name, `${stale.length} of ${prs.length} stale: ${list}`);
}

// ---- 5. Runtime support windows -------------------------------------------------------------------------------------

// One runtime: `rows` are endoflife.date rows (cycle, eol), or null when the lookup failed. eol is a date string, or
// false while the cycle has no end date.
export function checkSupport({ label, cycle, rows, now }) {
  const name = `${label} supported for ${SUPPORT_DAYS}+ days`;
  if (!cycle) return unknown(name, 'version not found in the repository files');
  if (!rows) return unknown(name, 'endoflife.date lookup failed');
  const row = rows.find((r) => String(r.cycle) === String(cycle));
  if (!row) return unknown(name, `cycle ${cycle} not listed by endoflife.date`);
  if (row.eol === false) return pass(name, `${cycle}: no end-of-life date`);
  if (row.eol === true) return fail(name, `${cycle}: end of life`);
  const end = typeof row.eol === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(row.eol) ? Date.parse(row.eol) : NaN;
  if (Number.isNaN(end)) return unknown(name, `${cycle}: unreadable end-of-life value`);
  const left = days(now, end);
  const text = `${cycle}: end of life ${row.eol} (${left} days left)`;
  return left >= SUPPORT_DAYS ? pass(name, text) : fail(name, text);
}

export function checkRuntimes({ dockerfile, goMod, eol, now }) {
  const alpine = dockerfileAlpineCycle(dockerfile);
  return [
    checkSupport({ label: 'Node (web build stage)', cycle: dockerfileNodeMajor(dockerfile), rows: eol.nodejs, now }),
    checkSupport({ label: 'Go (go.mod)', cycle: goModCycle(goMod), rows: eol.go, now }),
    checkSupport({ label: 'Debian (runtime image)', cycle: dockerfileDebianMajor(dockerfile), rows: eol.debian, now }),
    alpine
      ? checkSupport({ label: 'Alpine (build image)', cycle: alpine, rows: eol['alpine-linux'], now })
      : unknown(`Alpine (build image) supported for ${SUPPORT_DAYS}+ days`, 'the build image follows the floating -alpine tag, so no release is pinned'),
  ];
}

// ---- 6. One Node major ----------------------------------------------------------------------------------------------

// workflows: { 'ci.yml': text, ... }
export function checkNodeMajor({ dockerfile, workflows }) {
  const name = 'Node major is the same in the Dockerfile and every workflow';
  const image = dockerfileNodeMajor(dockerfile);
  if (!image) return unknown(name, 'no node image in the Dockerfile');
  const seen = Object.entries(workflows).flatMap(([f, text]) => workflowNodeMajors(text).map((v) => [f, v]));
  const odd = seen.filter(([, v]) => v !== image).map(([f, v]) => `${f}: ${v}`);
  if (odd.length > 0) return fail(name, `Dockerfile ${image}; differs in ${[...new Set(odd)].join(', ')}`);
  const files = new Set(seen.map(([f]) => f)).size;
  return pass(name, `Dockerfile and ${seen.length} node-version settings in ${files} of ${Object.keys(workflows).length} workflows all use ${image}`);
}

// ---- 7. Scheduled workflows still run -------------------------------------------------------------------------------

// A workflow with a `schedule:` trigger under `on:` (a commented-out one does not count).
export const isScheduled = (text) => /^ {2}schedule:\s*$/m.test(text);

// runsByFile: { 'audit.yml': [run] } for the scheduled workflows; a run counts when it was started by the schedule and
// has finished, whatever its result. GitHub switches a schedule off after 60 days without repository activity, which
// leaves no error anywhere; this is the only place that shows it.
export function checkSchedules({ runsByFile, now }) {
  const name = `Every scheduled workflow ran in the last ${SCHEDULE_DAYS} days`;
  const files = Object.keys(runsByFile);
  if (files.length === 0) return unknown(name, 'no scheduled workflow found');
  const quiet = files.filter((f) => !runsByFile[f].some((r) => r.event === 'schedule' && r.status === 'completed' && days(Date.parse(r.created_at), now) <= SCHEDULE_DAYS));
  if (quiet.length > 0) return fail(name, `no scheduled run: ${quiet.join(', ')}`);
  return pass(name, `${files.length} scheduled workflows: ${files.join(', ')}`);
}

// ---- 8. Restore drill -----------------------------------------------------------------------------------------------

// handover: text of docs/maintainers/handover.md, or null when the file does not exist. Looks for a line that mentions
// the restore drill and carries a YYYY-MM-DD date.
export function checkRestoreDrill({ handover, now }) {
  const name = 'Restore drill is recent';
  if (handover === null || handover === undefined) return unknown(name, 'handover page not present');
  const line = handover.split('\n').find((l) => /restore\s+drill/i.test(l) && /\d{4}-\d{2}-\d{2}/.test(l));
  if (!line) return unknown(name, 'no dated restore-drill line in the handover page');
  const when = Date.parse(/\d{4}-\d{2}-\d{2}/.exec(line)[0]);
  if (Number.isNaN(when)) return unknown(name, 'no dated restore-drill line in the handover page');
  const age = days(when, now);
  if (age < 0) return unknown(name, `the drill date ${date(when)} is in the future`);
  const text = `last drill ${date(when)} (${age} days ago)`;
  return age <= RESTORE_DRILL_DAYS ? pass(name, text) : fail(name, `${text}; older than ${RESTORE_DRILL_DAYS} days`);
}

// ---- Rendering ------------------------------------------------------------------------------------------------------

const MARKS = { pass: 'PASS', fail: 'FAIL', unknown: 'UNKNOWN' };

export function render(checks, { now }) {
  const count = (s) => checks.filter((c) => c.status === s).length;
  const lines = [
    `Checked ${date(now)}: ${count('pass')} pass, ${count('fail')} fail, ${count('unknown')} unknown.`,
    '',
    ...checks.map((c) => `- ${MARKS[c.status]}: ${c.name}. ${c.evidence}`),
  ];
  return lines.join('\n');
}

// ---- Collecting -----------------------------------------------------------------------------------------------------

const defaultGh = (args) => execFileSync('gh', args, { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
const defaultFetchJson = async (url) => {
  const res = await fetch(url, { signal: AbortSignal.timeout(15_000) });
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return res.json();
};

// Reads everything, then runs the pure checks. `gh` and `fetchJson` are injected so the whole collection is testable.
export async function settledChecks({ repo, root, now, cosign = null, gh = defaultGh, fetchJson = defaultFetchJson }) {
  const read = (f) => readFileSync(join(root, f), 'utf8').replace(/\r\n/g, '\n');
  const api = (path) => JSON.parse(gh(['api', `repos/${repo}/${path}`]));
  // A lookup that throws makes its check unknown, with the reason as the evidence.
  const guarded = async (name, fn) => {
    try {
      return await fn();
    } catch (e) {
      return unknown(name, `lookup failed: ${String(e.message ?? e).split('\n')[0].slice(0, 160)}`);
    }
  };
  const issues = (label) => JSON.parse(gh(['issue', 'list', '--repo', repo, '--state', 'open', '--label', label, '--json', 'number,title', '--limit', '100']));

  const dockerfile = read('Dockerfile');
  const out = [];

  out.push(
    await guarded('CI is green on main', () => {
      const sha = api('commits/main').sha;
      return checkCi({ sha, runs: api(`actions/runs?head_sha=${sha}&per_page=100`).workflow_runs });
    }),
  );
  out.push(
    await guarded('Latest release is the newest tag', () =>
      // Every page of tags: the API lists them in no version order, so a newest tag past the first 100 must not be missed.
      checkRelease({ latestRelease: api('releases/latest').tag_name, tagNames: gh(['api', '--paginate', '--jq', '.[].name', `repos/${repo}/tags?per_page=100`]).split('\n').filter(Boolean), cosign }),
    ),
  );
  out.push(await guarded('No other open security advisory', () => checkAuditIssue({ openIssues: issues('security') })));
  out.push(await guarded('No open P0 or P1, freeze list resolved', () => checkBlockers({ byLabel: { P0: issues('P0'), P1: issues('P1'), freeze: issues('freeze') } })));
  out.push(
    await guarded(`No Dependabot PR older than ${STALE_PR_DAYS} days`, () =>
      checkDependabot({ prs: JSON.parse(gh(['pr', 'list', '--repo', repo, '--state', 'open', '--author', 'app/dependabot', '--json', 'number,createdAt', '--limit', '100'])), now }),
    ),
  );

  const eol = {};
  for (const product of ['nodejs', 'go', 'debian', 'alpine-linux']) {
    try {
      const body = await fetchJson(`https://endoflife.date/api/${product}.json`);
      eol[product] = Array.isArray(body) ? body : null; // an error object or a redirect notice is a failed lookup
    } catch {
      eol[product] = null;
    }
  }
  const runtimes = await guarded('Runtimes supported', () => checkRuntimes({ dockerfile, goMod: read('go.mod'), eol, now }));
  out.push(...[runtimes].flat());

  const wfDir = join(root, '.github', 'workflows');
  const workflows = Object.fromEntries(readdirSync(wfDir).filter((f) => /\.ya?ml$/.test(f)).map((f) => [f, read(`.github/workflows/${f}`)]));
  out.push(checkNodeMajor({ dockerfile, workflows }));

  out.push(
    await guarded(`Every scheduled workflow ran in the last ${SCHEDULE_DAYS} days`, () => {
      const since = date(now - SCHEDULE_DAYS * DAY);
      const runsByFile = {};
      for (const f of Object.keys(workflows).filter((n) => isScheduled(workflows[n]))) {
        runsByFile[f] = api(`actions/workflows/${f}/runs?event=schedule&created=%3E%3D${since}&per_page=30`).workflow_runs;
      }
      return checkSchedules({ runsByFile, now });
    }),
  );

  const handoverPath = join(root, 'docs', 'maintainers', 'handover.md');
  out.push(checkRestoreDrill({ handover: existsSync(handoverPath) ? read('docs/maintainers/handover.md') : null, now }));
  return out;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const arg = (name) => {
    const i = process.argv.indexOf(name);
    return i > 0 ? process.argv[i + 1] : undefined;
  };
  const repo = arg('--repo') ?? JSON.parse(defaultGh(['repo', 'view', '--json', 'nameWithOwner'])).nameWithOwner;
  const tag = arg('--cosign-tag');
  const outcome = arg('--cosign-outcome');
  // No outcome flag means the caller did not try to verify (a local run). Any outcome, even with an empty tag, is passed
  // on, so a step that failed or was skipped can never read as "not checked, so fine".
  const cosign = outcome === undefined ? null : { tag: tag ?? '', outcome };
  const now = Date.now();
  const root = fileURLToPath(new URL('..', import.meta.url));
  const text = render(await settledChecks({ repo, root, now, cosign }), { now });
  console.log(text);
  if (arg('--out')) writeFileSync(arg('--out'), `${text}\n`);
}
