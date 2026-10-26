// Where the Node, Go and base-image versions are written, read in one place. toolchain.test.mjs asserts they agree;
// settled-check.mjs reports how long they stay supported.

// "22" from `FROM node:22-alpine@sha256:...` (null when the Dockerfile has no node stage).
export const dockerfileNodeMajor = (dockerfile) => /^FROM\b[^\n]*\bnode:(\d+)(?:\.\d+)*-alpine/m.exec(dockerfile)?.[1] ?? null;

// Every Node major a workflow sets with `node-version:`.
// A commented-out line does not set anything, so it is skipped.
export const workflowNodeMajors = (workflow) =>
  workflow
    .split('\n')
    .filter((l) => !/^\s*#/.test(l))
    .flatMap((l) => [...l.matchAll(/node-version:\s*"?(\d+)(?:\.\d+)*"?/g)].map((m) => m[1]));

// "1.27" from the `go 1.27` line of go.mod.
export const goModCycle = (goMod) => /^go (\d+\.\d+)(?:\.\d+)?\s*$/m.exec(goMod)?.[1] ?? null;

// The runtime stage's Debian major, from `FROM gcr.io/distroless/static-debian13:nonroot` (the last FROM).
export const dockerfileDebianMajor = (dockerfile) => {
  const froms = [...dockerfile.matchAll(/^FROM\b[^\n]*\bstatic-debian(\d+)\b/gm)];
  return froms.at(-1)?.[1] ?? null;
};

// "3.22" from an `alpineX.Y` tag in any FROM line; null when the image follows the floating `-alpine` tag.
export const dockerfileAlpineCycle = (dockerfile) => /^FROM\b[^\n]*-alpine(\d+\.\d+)\b/m.exec(dockerfile)?.[1] ?? null;
