#!/usr/bin/env node
// UAT label guard: fails when an aria-label string that web/uat/*.mjs still looks for was removed or changed in web/src.
//
//   node scripts/uat-labels.mjs [<git range>]      default origin/main...HEAD
//
// Why: the UAT screens find controls by their accessible name, so renaming an aria-label in the app silently breaks
// every screen that looks for the old name (the folder-layout rename broke about 30 screens that way). This is a
// pure text check: no browser, no server, runs in a second. It is not a replacement for running Suite 1.
//
// How: `git diff -U0 <range> -- web/src` gives the removed and added lines. From each, the static text of every
// `aria-label=` / `ariaLabel=` value is taken ("Select ${title}" gives the fragment "Select "). A removed fragment that
// no longer appears in any aria-label in web/src now is "gone"; if a gone fragment (3 or more characters once trimmed)
// still appears in a web/uat/*.mjs file, the run fails and names the file and line. Put `uat-labels: ignore` in a
// comment on a UAT line to exempt it (for a word that only happens to match). Limits: only values written on the same
// line as the attribute are seen; labels built elsewhere (a helper that returns the string) are not.
//
// Exit codes: 0 clean, 1 a gone label is still used by the UAT scripts, 2 usage or git error.
// Tests: node --test scripts/uat-labels.test.mjs
import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const DEFAULT_RANGE = 'origin/main...HEAD';
export const MIN_LENGTH = 3;
export const IGNORE_MARK = 'uat-labels: ignore';

// aria-label="x", aria-label='x', aria-label={...} and the ariaLabel prop spelling.
const ATTRIBUTE = /(?:aria-label|ariaLabel)=(?:"([^"]*)"|'([^']*)'|\{)/g;
const STRING_IN_EXPRESSION = /"([^"]*)"|'([^']*)'|`([^`]*)`/g;

/** Splits a template literal's text on its ${...} holes: "Select ${x}" gives ["Select "]. */
export function staticParts(text) {
  return text.split(/\$\{[^}]*\}/).filter((p) => p.length > 0);
}

/**
 * The static label fragments written on one source line.
 * @param {string} line
 * @returns {string[]}
 */
export function extractFragments(line) {
  const out = [];
  ATTRIBUTE.lastIndex = 0;
  let m;
  while ((m = ATTRIBUTE.exec(line)) !== null) {
    if (m[1] !== undefined) out.push(...staticParts(m[1]));
    else if (m[2] !== undefined) out.push(...staticParts(m[2]));
    else {
      // aria-label={ ... }: take the string literals up to the end of the line.
      const rest = line.slice(m.index + m[0].length);
      STRING_IN_EXPRESSION.lastIndex = 0;
      let s;
      while ((s = STRING_IN_EXPRESSION.exec(rest)) !== null) {
        out.push(...staticParts(s[1] ?? s[2] ?? s[3] ?? ''));
      }
    }
  }
  return out;
}

/**
 * Reads a unified diff (-U0) and returns the fragments on removed and added lines, with the file they came from.
 * @param {string} diffText
 * @returns {{file: string, removed: string[], added: string[]}[]}
 */
export function parseDiff(diffText) {
  const files = [];
  let current = null;
  for (const line of diffText.replace(/\r\n/g, '\n').split('\n')) {
    const header = /^\+\+\+ b\/(.+)$/.exec(line);
    if (header) {
      current = { file: header[1], removed: [], added: [] };
      files.push(current);
      continue;
    }
    if (!current || line.startsWith('---') || line.startsWith('@@')) continue;
    if (line.startsWith('-')) current.removed.push(...extractFragments(line.slice(1)));
    else if (line.startsWith('+')) current.added.push(...extractFragments(line.slice(1)));
  }
  return files;
}

/** True when a fragment is long enough to be a meaningful search string. */
export const isSearchable = (fragment) => fragment.trim().length >= MIN_LENGTH;

/**
 * Removed fragments that no aria-label in the current source has any more.
 * @param {{file: string, removed: string[]}[]} diffFiles
 * @param {Set<string>} currentFragments every fragment in web/src now
 * @returns {{fragment: string, file: string}[]}
 */
export function goneFragments(diffFiles, currentFragments) {
  const gone = [];
  const seen = new Set();
  for (const f of diffFiles) {
    for (const fragment of f.removed) {
      if (!isSearchable(fragment) || currentFragments.has(fragment)) continue;
      const key = `${f.file}\u0000${fragment}`;
      if (seen.has(key)) continue;
      seen.add(key);
      gone.push({ fragment, file: f.file });
    }
  }
  return gone;
}

/**
 * Where a fragment still appears in the UAT scripts, skipping lines marked with the ignore comment.
 * @param {string} fragment
 * @param {{name: string, text: string}[]} uatFiles
 * @returns {{name: string, line: number, text: string}[]}
 */
export function findUsages(fragment, uatFiles) {
  const hits = [];
  for (const f of uatFiles) {
    f.text.replace(/\r\n/g, '\n').split('\n').forEach((text, i) => {
      if (text.includes(fragment) && !text.includes(IGNORE_MARK)) hits.push({ name: f.name, line: i + 1, text: text.trim() });
    });
  }
  return hits;
}

/**
 * The whole check on already-read inputs.
 * @returns {{fragment: string, file: string, usages: {name: string, line: number, text: string}[]}[]} the problems
 */
export function check(diffText, currentFragments, uatFiles) {
  const problems = [];
  for (const g of goneFragments(parseDiff(diffText), currentFragments)) {
    const usages = findUsages(g.fragment, uatFiles);
    if (usages.length) problems.push({ ...g, usages });
  }
  return problems;
}

function listSourceFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listSourceFiles(path));
    else if (/\.(tsx?|jsx?)$/.test(entry.name)) out.push(path);
  }
  return out;
}

/** Every aria-label fragment in the working tree's web/src. */
export function currentFragments(root) {
  const set = new Set();
  for (const file of listSourceFiles(join(root, 'web', 'src'))) {
    for (const line of readFileSync(file, 'utf8').split('\n')) for (const f of extractFragments(line)) set.add(f);
  }
  return set;
}

function readUatFiles(root) {
  const dir = join(root, 'web', 'uat');
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((n) => n.endsWith('.mjs'))
    .map((n) => ({ name: `web/uat/${n}`, text: readFileSync(join(dir, n), 'utf8') }));
}

function main(argv) {
  const range = argv[0] ?? DEFAULT_RANGE;
  if (argv.length > 1 || range.startsWith('-')) {
    console.error('usage: uat-labels.mjs [<git range>]   (default origin/main...HEAD)');
    return 2;
  }
  const root = join(dirname(fileURLToPath(import.meta.url)), '..');
  let diffText;
  try {
    diffText = execFileSync('git', ['-C', root, 'diff', '-U0', '--no-color', range, '--', 'web/src'], { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 });
  } catch (e) {
    console.error(`git diff ${range} failed: ${e.message}\nLikely fix: git fetch origin, and check the range exists.`);
    return 2;
  }
  const problems = check(diffText, currentFragments(root), readUatFiles(root));
  for (const p of problems) {
    console.error(`aria-label "${p.fragment}" was removed or changed in ${p.file}, but the UAT scripts still use it:`);
    for (const u of p.usages) console.error(`  ${u.name}:${u.line}: ${u.text}`);
  }
  if (problems.length) {
    console.error(`\n${problems.length} label(s) to update in web/uat (or mark a coincidental match with "${IGNORE_MARK}" in a comment).`);
    return 1;
  }
  console.log(`uat-labels: no removed aria-label is still used by web/uat (${range})`);
  return 0;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
