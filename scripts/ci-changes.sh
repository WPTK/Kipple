#!/usr/bin/env bash
# Decides whether CI has to run the build and test jobs (ci.yml, job `changes`). Prints `true` or `false`.
#   ci-changes.sh EVENT [DIFF_ARGS...]   EVENT is github.event_name. Anything but pull_request prints true. On a pull
#                                        request, DIFF_ARGS default to `HEAD^1 HEAD` (the merge commit against the base tip).
#   ci-changes.sh --classify             reads changed paths from stdin, one per line.
# `false` means every changed path is listed in scripts/ci-prose.txt. Anything unexpected (git fails, the list is
# missing, no paths) prints `true`: running too much costs minutes, running too little skips a required check.
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# literal PATTERN: the pattern with every glob character except `*` backslash-escaped.
literal() {
  local s="$1" out= c i
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in '?' | '[' | ']' | '(' | ')' | '|' | '@' | '+' | '!' | '\') out+="\\$c" ;; *) out+="$c" ;; esac
  done
  printf '%s' "$out"
}

classify() {
  local allow=() deny=() line f p g matched
  [ -r "$here/ci-prose.txt" ] || { echo true; return; }
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      '' | '#'*) ;;
      '!'*) deny+=("${line#!}") ;;
      *) allow+=("$line") ;;
    esac
  done < "$here/ci-prose.txt"
  [ "${#allow[@]}" -gt 0 ] || { echo true; return; }
  local n=0
  while IFS= read -r f || [ -n "$f" ]; do
    [ -n "$f" ] || continue
    n=$((n + 1))
    matched=
    # Never prose, whatever the list says: the list, this script, the tests beside them, the workflows and actions. A
    # pull request that edits the rules is judged by the rules, not by its own edit of them.
    case "$f" in scripts/* | .github/workflows/* | .github/actions/*) echo true; return ;; esac
    # [[ == ]] with an unquoted pattern is a glob in which `*` also matches `/`. Only `*` is special in a list line,
    # so the other glob characters are escaped to match themselves.
    for p in "${allow[@]}"; do g="$(literal "$p")"; [[ "$f" == $g ]] && { matched=1; break; }; done
    for p in ${deny[@]+"${deny[@]}"}; do g="$(literal "$p")"; [[ "$f" == $g ]] && { matched=; break; }; done
    [ -n "$matched" ] || { echo true; return; }
  done
  # No paths at all cannot be a real pull request; run everything.
  [ "$n" -gt 0 ] && echo false || echo true
}

if [ "${1:-}" = "--classify" ]; then
  classify
  exit 0
fi

event="${1:-}"
[ "$event" = pull_request ] || { echo true; exit 0; }
shift
[ "$#" -gt 0 ] || set -- HEAD^1 HEAD
# Captured, not read through process substitution, so a failing git is seen here. core.quotePath=false keeps
# non-ASCII names literal; git still quotes names with control characters, a double quote or a backslash, and a quoted
# name matches no pattern, so it counts as code.
if ! files="$(git -c core.quotePath=false diff --no-renames --name-only "$@" 2>/dev/null)"; then
  echo true
  exit 0
fi
printf '%s\n' "$files" | classify
