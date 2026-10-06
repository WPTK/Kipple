#!/usr/bin/env bash
# Deletes every tracked prose file (scripts/ci-prose.txt) from the working tree, so that anything that reads one of them
# fails its own test or build. ci.yml runs this before the build and test steps of every run that is not prose-only: the
# prose list is then checked by what the code does, not by a guess at what reads what.
#   ci-prune-prose.sh            lists what would be deleted
#   ci-prune-prose.sh --delete   deletes it
# Run it from inside the repository. It deletes only tracked files that the list names, never anything under .git,
# never an absolute path or one with a `..` segment, and it fails if the list is unusable or names nothing.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(git rev-parse --show-toplevel)"

mode="${1:-}"
case "$mode" in '' | --delete) ;; *) echo "usage: ci-prune-prose.sh [--delete]" >&2; exit 2 ;; esac

files="$(git -c core.quotePath=false ls-files | bash "$here/ci-changes.sh" --list)"
if [ -z "$files" ]; then
  echo "ci-prune-prose: scripts/ci-prose.txt matches no tracked file; fix the list" >&2
  exit 1
fi

n=0
while IFS= read -r f; do
  case "$f" in
    /* | .git | .git/* | ../* | */../* | */.. | ..) echo "ci-prune-prose: refusing $f" >&2; exit 1 ;;
  esac
  [ -f "$f" ] && [ ! -L "$f" ] || { echo "ci-prune-prose: not a regular tracked file: $f" >&2; exit 1; }
  n=$((n + 1))
  if [ "$mode" = --delete ]; then rm -- "$f"; else echo "$f"; fi
done <<< "$files"
echo "ci-prune-prose: $n files $([ "$mode" = --delete ] && echo deleted || echo listed)"
