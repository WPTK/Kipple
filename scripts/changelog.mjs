#!/usr/bin/env node
// Changelog fragments: each change adds its own small file under changes/ instead of editing CHANGELOG.md, so two
// branches never conflict on the changelog. At release, the fragments are folded into CHANGELOG.md and deleted.
//
//   node scripts/changelog.mjs check                       validate fragments and that CHANGELOG.md's [Unreleased] is untouched
//   node scripts/changelog.mjs preview                     print the section the pending fragments would make
//   node scripts/changelog.mjs release X.Y.Z[-pre.N] [--date YYYY-MM-DD] [--dry-run]
//                                                          fold fragments into CHANGELOG.md, delete them, update compare links
//   node scripts/changelog.mjs notes X.Y.Z[-pre.N]         print one version's CHANGELOG section (GitHub Release notes)
//
// Run from anywhere; paths resolve against the repository root. See changes/README.md for the fragment format.
import { readFileSync, readdirSync, writeFileSync, unlinkSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const KINDS = ['added', 'changed', 'deprecated', 'removed', 'fixed', 'security'];
const HEADING = Object.fromEntries(KINDS.map((k) => [k, k[0].toUpperCase() + k.slice(1)]));
const FRAGMENT_NAME = new RegExp(`^([a-z0-9][a-z0-9._-]*)\\.(${KINDS.join('|')})\\.md$`);
const IGNORED = new Set(['README.md', '_intro.md', '.gitkeep']);
const VERSION = /^\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?$/;
const REPO = 'https://github.com/WPTK/Kipple';

// The only thing that may sit under "## [Unreleased]": the pending entries live in changes/, not here.
export const POINTER =
  'Changes not yet in a release are one file each in [`changes/`](changes/); they are folded into this file when a release is cut.';

const naturalCompare = (a, b) => a.localeCompare(b, 'en', { numeric: true });

/** Reads and validates every fragment in dir. Returns { fragments, errors }. */
export function readFragments(dir) {
  const fragments = [];
  const errors = [];
  if (!existsSync(dir)) return { fragments, errors: [`${dir} does not exist`] };
  for (const file of readdirSync(dir).sort()) {
    if (IGNORED.has(file)) continue;
    const m = FRAGMENT_NAME.exec(file);
    if (!m) {
      errors.push(`${file}: name must be <slug>.<${KINDS.join('|')}>.md (slug: lowercase letters, digits, . _ -)`);
      continue;
    }
    const text = readFileSync(join(dir, file), 'utf8').replace(/\r\n/g, '\n').trim();
    const problem = validateText(text);
    if (problem) {
      errors.push(`${file}: ${problem}`);
      continue;
    }
    fragments.push({ id: m[1], kind: m[2], text, file });
  }
  return { fragments, errors };
}

function validateText(text) {
  if (!text) return 'is empty';
  if (/\n[ \t]*\n/.test(text)) return 'has a blank line; an entry is one paragraph';
  if (/^([-*+]|#+|\d+\.)\s/.test(text)) return 'starts with a list marker or heading; write just the sentence, the bullet is added for you';
  return '';
}

/** One "### Kind" block per kind that has entries, in Keep a Changelog order, entries sorted by id. */
export function renderSections(fragments) {
  const out = [];
  for (const kind of KINDS) {
    const entries = fragments.filter((f) => f.kind === kind).sort((a, b) => naturalCompare(a.id, b.id));
    if (!entries.length) continue;
    out.push(`### ${HEADING[kind]}\n\n${entries.map((e) => `- ${e.text.replace(/\n/g, '\n  ')}`).join('\n')}\n`);
  }
  return out.join('\n');
}

function unreleasedBounds(lines) {
  const start = lines.findIndex((l) => l.startsWith('## [Unreleased]'));
  if (start < 0) throw new Error('CHANGELOG.md has no "## [Unreleased]" heading');
  let end = lines.findIndex((l, i) => i > start && l.startsWith('## ['));
  if (end < 0) end = lines.length;
  return { start, end };
}

/** Problems with CHANGELOG.md's [Unreleased] block: anything but the pointer line means someone edited it by hand. */
export function checkUnreleased(changelog) {
  const lines = changelog.replace(/\r\n/g, '\n').split('\n');
  const { start, end } = unreleasedBounds(lines);
  const body = lines.slice(start + 1, end).join('\n').trim();
  if (body === POINTER) return [];
  return [
    'CHANGELOG.md [Unreleased] must contain only the pointer line; add the entry as a file in changes/ instead (see changes/README.md)',
  ];
}

/** Returns the new CHANGELOG.md text with the fragments folded in as a version section. */
export function release(changelog, { version, date, intro = '', fragments }) {
  if (!VERSION.test(version)) throw new Error(`"${version}" is not X.Y.Z or X.Y.Z-(alpha|beta|rc).N`);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date)) throw new Error(`"${date}" is not YYYY-MM-DD`);
  if (!fragments.length) throw new Error('no fragments in changes/, nothing to release');
  const problems = checkUnreleased(changelog);
  if (problems.length) throw new Error(problems.join('\n'));
  const text = changelog.replace(/\r\n/g, '\n');

  const lines = text.split('\n');
  if (lines.some((l) => l.startsWith(`## [${version}]`))) throw new Error(`CHANGELOG.md already has ${version}`);
  const { start, end } = unreleasedBounds(lines);
  const introBlock = intro.trim() ? `${intro.trim()}

` : '';
  const section = `## [${version}] - ${date}

${introBlock}${renderSections(fragments)}`;
  const head = lines.slice(0, start + 1).join('\n');
  const tail = lines.slice(end).join('\n');
  let out = `${head}\n\n${POINTER}\n\n${section}\n${tail}`;

  // Compare links: point [Unreleased] at the new tag and add the new version's range from the previous one.
  const link = /^\[Unreleased\]: (\S+)\/compare\/(\S+?)\.\.\.HEAD$/m.exec(out);
  if (link) {
    const [line, base, prev] = link;
    out = out.replace(line, `[Unreleased]: ${base}/compare/v${version}...HEAD\n[${version}]: ${base}/compare/${prev}...v${version}`);
  }
  return out;
}

/** One version's section (heading through the line before the next release heading), for release notes. */
export function notes(changelog, version) {
  const lines = changelog.replace(/\r\n/g, '\n').split('\n');
  const start = lines.findIndex((l) => l.startsWith(`## [${version}]`));
  if (start < 0) throw new Error(`CHANGELOG.md has no section for ${version}`);
  let end = lines.findIndex((l, i) => i > start && (l.startsWith('## [') || /^\[[^\]]+\]: /.test(l)));
  if (end < 0) end = lines.length;
  return `${lines.slice(start + 1, end).join('\n').trim()}\n`;
}

const pad = (n) => String(n).padStart(2, '0');

/** Today in the machine's local time (toISOString would be UTC, a day ahead on an Eastern evening). */
export function localDate(d = new Date()) {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** `release` arguments in any order: one version, optional `--date YYYY-MM-DD`, optional `--dry-run`. */
export function parseReleaseArgs(args) {
  let version;
  let date;
  let dryRun = false;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--dry-run') dryRun = true;
    else if (args[i] === '--date') date = args[++i];
    else if (args[i].startsWith('--')) throw new Error(`unknown option ${args[i]}`);
    else if (version === undefined) version = args[i];
    else throw new Error(`unexpected argument ${args[i]}`);
  }
  if (!version) throw new Error('release needs a version, e.g. 0.3.0-beta.2');
  return { version, date: date ?? localDate(), dryRun };
}

function main(argv) {
  const root = fileURLToPath(new URL('..', import.meta.url));
  const dir = join(root, 'changes');
  const changelogPath = join(root, 'CHANGELOG.md');
  const [cmd, ...rest] = argv;

  if (cmd === 'check') {
    const { errors } = readFragments(dir);
    errors.push(...checkUnreleased(readFileSync(changelogPath, 'utf8')));
    if (errors.length) {
      console.error(errors.map((e) => `changelog: ${e}`).join('\n'));
      return 1;
    }
    return 0;
  }
  if (cmd === 'preview') {
    const { fragments, errors } = readFragments(dir);
    if (errors.length) {
      console.error(errors.join('\n'));
      return 1;
    }
    process.stdout.write(renderSections(fragments) || '(no pending changes)\n');
    return 0;
  }
  if (cmd === 'release') {
    const { version, date, dryRun } = parseReleaseArgs(rest);
    const { fragments, errors } = readFragments(dir);
    if (errors.length) {
      console.error(errors.join('\n'));
      return 1;
    }
    const introPath = join(dir, '_intro.md');
    const intro = existsSync(introPath) ? readFileSync(introPath, 'utf8').replace(/\r\n/g, '\n') : '';
    const next = release(readFileSync(changelogPath, 'utf8'), { version, date, intro, fragments });
    if (dryRun) {
      process.stdout.write(notes(next, version));
      return 0;
    }
    writeFileSync(changelogPath, next);
    for (const f of fragments) unlinkSync(join(dir, f.file));
    if (existsSync(introPath)) unlinkSync(introPath);
    console.log(`CHANGELOG.md: ${fragments.length} entries folded into ${version}; fragments deleted. Review the diff, then commit.`);
    return 0;
  }
  if (cmd === 'notes') {
    process.stdout.write(notes(readFileSync(changelogPath, 'utf8'), rest[0]));
    return 0;
  }
  console.error('usage: changelog.mjs check | preview | release <version> [--date YYYY-MM-DD] [--dry-run] | notes <version>');
  return 2;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    process.exitCode = main(process.argv.slice(2));
  } catch (e) {
    console.error(`changelog: ${e.message}`);
    process.exitCode = 1;
  }
}
