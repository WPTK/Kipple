#!/usr/bin/env bash
# Tests for scripts/ci-changes.sh. Run: bash scripts/ci-changes.test.sh
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cc="$here/ci-changes.sh"
fails=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ok() { # ok NAME EXPECTED ACTUAL
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; fails=$((fails + 1)); fi
}
cls() { printf '%s' "$1" | bash "$cc" --classify; }

ok docs-only false "$(cls $'docs/RELEASING.md\nchanges/x.md\nCONTRIBUTING.md')"
ok nested-docs-md false "$(cls 'docs/a/b.md')"
ok issue-template false "$(cls '.github/ISSUE_TEMPLATE/bug_report.yml')"
ok mixed true "$(cls $'docs/RELEASING.md\ninternal/api/api.go')"
ok workflow true "$(cls '.github/workflows/ci.yml')"
ok scripts true "$(cls 'scripts/ci-prose.txt')"
ok test-read-doc-design true "$(cls 'docs/design.md')"
ok test-read-doc-deploy true "$(cls 'docs/deploy.md')"
ok readme true "$(cls 'README.md')"
ok changelog true "$(cls 'CHANGELOG.md')"
ok license true "$(cls 'LICENSE')"
ok notices true "$(cls 'THIRD_PARTY_NOTICES.md')"
ok new-md-outside-docs true "$(cls 'notes/new.md')"
ok non-md-under-docs true "$(cls 'docs/helper.go')"
ok non-md-under-changes true "$(cls 'changes/x.js')"
ok quoted-odd-path true "$(cls '"docs/caf\303\251.md"')"
ok path-with-space false "$(cls 'docs/my notes.md')"
ok empty-list true "$(cls '')"
ok no-trailing-newline false "$(cls 'docs/a.md')"

# Through git: a throwaway repository.
repo="$tmp/repo"
git init -q "$repo" && cd "$repo" || exit 1
git config core.autocrlf false && git config user.email t@example.com && git config user.name t
mkdir docs changes && echo a > docs/a.md && echo b > main.go && git add -A && git commit -qm base
commit() { git add -A && git commit -qm "$1"; }
run() { bash "$cc" "$@"; }

ok non-pr-event true "$(run push)"
echo x >> docs/a.md && commit docs
ok git-docs-only false "$(run pull_request HEAD~1 HEAD)"
echo x >> main.go && commit code
ok git-code true "$(run pull_request HEAD~1 HEAD)"
ok git-mixed true "$(run pull_request HEAD~2 HEAD)"
git mv docs/a.md main2.go && commit rename-docs-to-code
ok git-rename-docs-to-code true "$(run pull_request HEAD~1 HEAD)"
git mv main2.go docs/a.md && commit rename-back
git mv docs/a.md docs/b.md && commit rename-in-docs
ok git-rename-in-docs false "$(run pull_request HEAD~1 HEAD)"
git rm -q main.go && commit delete-code
ok git-delete-code true "$(run pull_request HEAD~1 HEAD)"
git rm -q docs/b.md && commit delete-doc
ok git-delete-doc false "$(run pull_request HEAD~1 HEAD)"
ok git-empty-diff true "$(run pull_request HEAD HEAD)"
ok git-diff-fails true "$(run pull_request nonexistent-ref HEAD)"
ok git-default-range false "$(run pull_request)"

# Mutation guards: each fails if the behaviour it names is reverted.
# A failing git must give true even when it printed a prose path first (a process substitution would lose the status).
mkdir "$tmp/bin"
printf '#!/bin/sh\necho docs/a.md\nexit 1\n' > "$tmp/bin/git"; chmod +x "$tmp/bin/git"
ok git-failure-after-output true "$(PATH="$tmp/bin:$PATH" bash "$cc" pull_request)"
# Non-ASCII names must be asked for literally (core.quotePath=false), or git quotes them and they count as code.
printf '#!/bin/sh\necho "$@" > "%s/args"\necho docs/a.md\n' "$tmp" > "$tmp/bin/git"
PATH="$tmp/bin:$PATH" bash "$cc" pull_request > /dev/null
ok git-asks-for-literal-names 1 "$(grep -c 'core.quotePath=false' "$tmp/args")"
rm "$tmp/bin/git"
cd "$repo" && mkdir -p docs && echo x > $'docs/caf\xc3\xa9.md' && commit unicode
ok git-unicode-doc false "$(run pull_request HEAD~1 HEAD)"

# The rules judge a pull request that edits them: scripts/ and the workflows are code whatever the list says, and
# only * is special in a list line.
alt="$tmp/alt"; mkdir "$alt"; cp "$cc" "$alt/ci-changes.sh"
altcls() { printf '%s\n' "$1" > "$alt/ci-prose.txt"; printf '%s' "$2" | bash "$alt/ci-changes.sh" --classify; }
ok list-cannot-bless-scripts true "$(altcls '*' 'scripts/ci-prose.txt')"
ok list-cannot-bless-workflows true "$(altcls '*' '.github/workflows/ci.yml')"
ok list-star-matches-others false "$(altcls '*' 'main.go')"
ok list-question-mark-is-literal true "$(altcls 'docs/x?.md' 'docs/xa.md')"
ok list-question-mark-matches-itself false "$(altcls 'docs/x?.md' 'docs/x?.md')"
ok list-bracket-is-literal true "$(altcls 'docs/[ab].md' 'docs/a.md')"

# The workflow must call the script through bash (a lost executable bit must not disable the skip) and must say so
# loudly when the script cannot run, then run everything.
wf="$here/../.github/workflows/ci.yml"
ok workflow-calls-through-bash 1 "$(grep -c 'bash scripts/ci-changes.sh' "$wf")"
ok workflow-warns-when-script-cannot-run 1 "$(grep -c '::warning::.*ci-changes' "$wf")"
bash "$cc" pull_request >/dev/null 2>&1
ok script-exits-zero-when-it-runs 0 "$?"

[ "$fails" -eq 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
