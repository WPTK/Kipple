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

// The image build checks the builder's real version against go.mod as well (Dockerfile); this catches a tag-only bump
// without building an image.
test('Go: Dockerfile builder image and the go.mod toolchain line name the same release', () => {
  const image = first(read('Dockerfile'), /^FROM\b[^\n]*\bgolang:(\d+\.\d+\.\d+)-alpine@/m, 'Dockerfile golang image');
  const mod = first(read('go.mod'), /^toolchain go(\d+\.\d+\.\d+)$/m, 'go.mod toolchain line');
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
  const a = first(read('.github/workflows/ci.yml'), /^\s*GITLEAKS_VERSION:\s*(\S+)/m, 'ci.yml GITLEAKS_VERSION');
  const b = first(read('scripts/ci-local.ps1'), /^\$GitleaksVersion\s*=\s*'([^']+)'/m, 'ci-local.ps1 GitleaksVersion');
  assert.equal(b, a);
});

// A `go run tool@version` builds the tool with its own dependencies, which can be too old to read the export data of
// the Go release in go.mod; tools/go.mod pins the Go linters and those dependencies together.
test('Go linters run from tools/go.mod, never by go run with a version', () => {
  const mod = read('tools/go.mod');
  for (const tool of ['honnef.co/go/tools/cmd/staticcheck', 'github.com/securego/gosec/v2/cmd/gosec', 'golang.org/x/vuln/cmd/govulncheck']) {
    assert.match(mod, new RegExp(String.raw`^\s*${tool.replaceAll('.', '\\.')}$`, 'm'), `tools/go.mod: no tool line for ${tool}`);
  }
  for (const f of ['.github/workflows/ci.yml', '.github/workflows/audit.yml', '.github/actions/audit-govulncheck/action.yml', 'scripts/ci-local.ps1']) {
    assert.doesNotMatch(read(f), /\b(staticcheck|gosec|govulncheck)@/, `${f}: runs a Go linter by version instead of from tools/go.mod`);
  }
});
