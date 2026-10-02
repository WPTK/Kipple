// node --test scripts/toolchain.test.mjs
// The Go and Node versions, and the CI tool pins, are written in more than one file and Dependabot moves only some of
// them (it bumps the Dockerfile's golang image but not go.mod). This fails the first time they disagree, instead of the
// image shipping a toolchain CI never tested.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const read = (f) => readFileSync(fileURLToPath(new URL(`../${f}`, import.meta.url)), 'utf8').replace(/\r\n/g, '\n');
const first = (text, re, what) => {
  const m = re.exec(text);
  assert.ok(m, `${what}: not found`);
  return m[1];
};

test('Go: Dockerfile builder image and go.mod name the same minor', () => {
  const dockerfile = read('Dockerfile');
  const image = first(dockerfile, /^FROM\b[^\n]*\bgolang:(\d+\.\d+)(?:\.\d+)?-alpine/m, 'Dockerfile golang image');
  const mod = first(read('go.mod'), /^go (\d+\.\d+)(?:\.\d+)?$/m, 'go.mod go line');
  assert.equal(image, mod);
});

test('Go: every setup-go in the workflows reads go.mod instead of repeating the version', () => {
  for (const f of ['ci.yml', 'release.yml', 'scorecard.yml']) {
    const text = read(`.github/workflows/${f}`);
    const uses = text.split('\n').filter((l) => /uses:\s*actions\/setup-go@/.test(l)).length;
    const files = text.split('\n').filter((l) => /go-version-file:\s*go\.mod\s*$/.test(l)).length;
    assert.equal(files, uses, `${f}: each actions/setup-go needs go-version-file: go.mod`);
    assert.doesNotMatch(text, /^\s*go-version:/m, `${f}: go-version repeats go.mod`);
  }
});

test('Node: Dockerfile web stage and ci.yml name the same major', () => {
  const image = first(read('Dockerfile'), /^FROM\b[^\n]*\bnode:(\d+)(?:\.\d+)*-alpine/m, 'Dockerfile node image');
  const ci = read('.github/workflows/ci.yml');
  const versions = [...ci.matchAll(/node-version:\s*"?(\d+)(?:\.\d+)*"?/g)].map((m) => m[1]);
  assert.ok(versions.length > 0, 'ci.yml has no node-version');
  for (const v of versions) assert.equal(v, image);
});

test('CI tool pins: ci-local.ps1 matches the env block in ci.yml', () => {
  const ci = read('.github/workflows/ci.yml');
  const local = read('scripts/ci-local.ps1');
  const pairs = [
    ['GOVULNCHECK_VERSION', 'GovulncheckVersion'],
    ['STATICCHECK_VERSION', 'StaticcheckVersion'],
    ['GOSEC_VERSION', 'GosecVersion'],
    ['GITLEAKS_VERSION', 'GitleaksVersion'],
  ];
  for (const [envName, psName] of pairs) {
    const a = first(ci, new RegExp(String.raw`^\s*${envName}:\s*(\S+)`, 'm'), `ci.yml ${envName}`);
    const b = first(local, new RegExp(String.raw`^\$${psName}\s*=\s*'([^']+)'`, 'm'), `ci-local.ps1 ${psName}`);
    assert.equal(b, a, `${psName} differs from ${envName}`);
  }
});
