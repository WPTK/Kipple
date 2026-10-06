#!/usr/bin/env bash
# Tests for scripts/ci-prune-prose.sh. Run: bash scripts/ci-prune-prose.test.sh
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fails=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ok() { # ok NAME EXPECTED ACTUAL
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; fails=$((fails + 1)); fi
}

# newrepo DIR LIST: a throwaway repository with copies of the scripts and the given prose list.
newrepo() {
  mkdir -p "$1/scripts/lib" "$1/docs/sub" "$1/.github/workflows" "$1/src"
  cp "$here/ci-changes.sh" "$here/ci-prune-prose.sh" "$1/scripts/"
  printf '%s' "$2" > "$1/scripts/ci-prose.txt"
  echo a > "$1/docs/a.md"; echo b > "$1/docs/my notes.md"; echo c > "$1/docs/sub/n.md"; echo k > "$1/docs/keep.md"
  echo m > "$1/main.go"; echo w > "$1/.github/workflows/ci.yml"; echo s > "$1/scripts/lib/x.md"; echo r > "$1/src/readme.md"
  (cd "$1" && git init -q && git config core.autocrlf false && git config user.email t@example.com && git config user.name t && git add -A && git commit -qm base)
}
prune() { (cd "$1" && GITHUB_ACTIONS=true bash scripts/ci-prune-prose.sh "${@:2}" 2>&1); }
remaining() { (cd "$1" && find . -path ./.git -prune -o -type f -print | sort | paste -sd' ' -); }

r1="$tmp/r1"; newrepo "$r1" $'docs/*.md\n!docs/keep.md\nsrc/readme.md\n'
before="$(remaining "$r1")"
prune "$r1" > /dev/null
ok dry-run-deletes-nothing "$before" "$(remaining "$r1")"
ok dry-run-lists "docs/a.md docs/my notes.md docs/sub/n.md src/readme.md" "$(cd "$r1" && bash scripts/ci-prune-prose.sh | grep -v '^ci-prune' | sort | paste -sd' ' -)"
prune "$r1" --delete > /dev/null
ok delete-removes-globs-spaces-nested "./.github/workflows/ci.yml ./docs/keep.md ./main.go ./scripts/ci-changes.sh ./scripts/ci-prose.txt ./scripts/ci-prune-prose.sh ./scripts/lib/x.md" "$(remaining "$r1")"
ok git-dir-intact 0 "$(cd "$r1" && git status > /dev/null 2>&1; echo $?)"
ok git-still-tracks-deleted "4" "$(cd "$r1" && git status --short | grep -c '^ D')"

# By hand it refuses without the explicit flag, and deletes nothing.
r0="$tmp/r0"; newrepo "$r0" $'docs/*.md
'
(cd "$r0" && env -u GITHUB_ACTIONS bash scripts/ci-prune-prose.sh --delete > /dev/null 2>&1); ok refuses-by-hand 2 "$?"
ok refusal-deletes-nothing 1 "$([ -f "$r0/docs/a.md" ] && echo 1 || echo 0)"
(cd "$r0" && env -u GITHUB_ACTIONS bash scripts/ci-prune-prose.sh --delete --discard-edits > /dev/null 2>&1); ok discard-edits-flag-allows 0 "$?"
ok flag-deleted 0 "$([ -f "$r0/docs/a.md" ] && echo 1 || echo 0)"

# A tracked symlink under a listed name is refused (only where the checkout can make symlinks).
r6="$tmp/r6"; newrepo "$r6" $'docs/*.md
'
if (cd "$r6" && ln -s ../main.go docs/link.md 2>/dev/null && [ -L docs/link.md ]); then
  (cd "$r6" && git add -A && git commit -qm link)
  prune "$r6" --delete > /dev/null; ok symlink-refused 1 "$?"
  ok symlink-target-intact 1 "$([ -f "$r6/main.go" ] && echo 1 || echo 0)"
fi

# Nothing outside the list: a pattern that tries scripts/ and the workflows deletes neither.
r2="$tmp/r2"; newrepo "$r2" $'*\n'
prune "$r2" --delete > /dev/null
ok star-list-spares-scripts-and-workflows "./.github/workflows/ci.yml ./scripts/ci-changes.sh ./scripts/ci-prose.txt ./scripts/ci-prune-prose.sh ./scripts/lib/x.md" "$(remaining "$r2")"

# Failing loudly.
r3="$tmp/r3"; newrepo "$r3" $'# only comments\n'
before3="$(remaining "$r3")"
out="$(prune "$r3" --delete)"; rc=$?
ok empty-list-fails 1 "$rc"
ok empty-list-deletes-nothing "$before3" "$(remaining "$r3")"
r4="$tmp/r4"; newrepo "$r4" $'nothing/here.md\n'
prune "$r4" --delete > /dev/null; ok list-matching-nothing-fails 1 "$?"
ok list-matching-nothing-deletes-nothing 1 "$([ -f "$r4/docs/a.md" ] && echo 1 || echo 0)"
r5="$tmp/r5"; newrepo "$r5" $'docs/*.md\n'; rm "$r5/scripts/ci-prose.txt"
prune "$r5" --delete > /dev/null; ok missing-list-fails 1 "$?"
ok missing-list-deletes-nothing 1 "$([ -f "$r5/docs/a.md" ] && echo 1 || echo 0)"
prune "$r1" --bogus > /dev/null; ok bad-argument-fails 2 "$?"

[ "$fails" -eq 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
