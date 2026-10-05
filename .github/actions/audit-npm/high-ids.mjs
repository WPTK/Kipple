// Prints the advisory ids that fail the audit gate (high and critical) from a saved `npm audit --json` report.
// The weekly audit puts these in its tracking issue so it names only what fails the gate, not every moderate finding.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const GATE = new Set(['high', 'critical']);
const GHSA = /\bGHSA(?:-[a-z0-9]{4}){3}\b/;

export function highIds(json) {
  let report;
  try {
    report = JSON.parse(json);
  } catch {
    return []; // npm printed no report (registry error); the gate step fails on its own and the issue says so
  }
  const ids = new Set();
  for (const v of Object.values(report.vulnerabilities ?? {})) {
    for (const via of v.via ?? []) {
      // A string in `via` only points at another package's entry, which is listed on its own.
      if (typeof via === 'object' && GATE.has(via.severity)) {
        const id = GHSA.exec(via.url ?? '')?.[0];
        if (id) ids.add(id);
      }
    }
  }
  return [...ids].sort();
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  for (const id of highIds(readFileSync(process.argv[2], 'utf8'))) console.log(id);
}
