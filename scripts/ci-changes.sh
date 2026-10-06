#!/usr/bin/env bash
# Decides whether CI has to run the build and test jobs (ci.yml, job `changes`) and which files are prose.
#   ci-changes.sh EVENT [DIFF_ARGS...]   Prints `true` or `false`. EVENT is github.event_name. Anything but
#                                        pull_request prints true. On a pull request, DIFF_ARGS default to
#                                        `HEAD^1 HEAD` (the merge commit against the base tip).
#   ci-changes.sh --classify             Reads changed paths from stdin, one per line; prints true or false.
#   ci-changes.sh --scripts EVENT [DIFF_ARGS...]
#                                        For the `tooling` job (PowerShell tooling tests): prints `false` only on a pull
#                                        request whose changed paths are all outside scripts/, not the CI workflow and not
#                                        .gitignore or web/.gitignore (inputs of the tooling tests); anything else,
#                                        and every failure, prints true.
#   ci-changes.sh --scripts-classify     Reads changed paths from stdin; prints true or false (same rule).
#   ci-changes.sh --list                 Reads paths from stdin and prints the ones that are prose (scripts/ci-prune-prose.sh).
# `false` means every changed path is listed in ci-prose.txt beside this script. Anything unexpected (git fails, the
# list is missing, no paths) prints `true`: running too much costs minutes, running too little skips a required check.
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

# load_rules: reads ci-prose.txt once into allow[] and deny[] (already escaped). Returns 1 if there is nothing to go on.
allow=() deny=()
load_rules() {
  local line
  [ -r "$here/ci-prose.txt" ] || return 1
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in
      '' | '#'*) ;;
      '!'*) deny+=("$(literal "${line#!}")") ;;
      *) allow+=("$(literal "$line")") ;;
    esac
  done < "$here/ci-prose.txt"
  [ "${#allow[@]}" -gt 0 ]
}

# is_prose PATH: success when the list allows it and nothing removes it.
is_prose() {
  local f="$1" p
  # Never prose, whatever the list says: the list, this script and the tests beside them, the workflows and actions.
  case "$f" in scripts/* | .github/workflows/* | .github/actions/*) return 1 ;; esac
  # [[ == ]] with an unquoted pattern is a glob in which `*` also matches `/`.
  for p in ${deny[@]+"${deny[@]}"}; do [[ "$f" == $p ]] && return 1; done
  for p in "${allow[@]}"; do [[ "$f" == $p ]] && return 0; done
  return 1
}

classify() {
  load_rules || { echo true; return; }
  local f n=0
  while IFS= read -r f || [ -n "$f" ]; do
    [ -n "$f" ] || continue
    n=$((n + 1))
    is_prose "$f" || { echo true; return; }
  done
  # No paths at all cannot be a real pull request; run everything.
  [ "$n" -gt 0 ] && echo false || echo true
}

# classify_scripts: true when any path is under scripts/, is the CI workflow, or is a .gitignore the tooling tests copy.
classify_scripts() {
  local f n=0
  while IFS= read -r f || [ -n "$f" ]; do
    [ -n "$f" ] || continue
    n=$((n + 1))
    # A name git quotes (control character, double quote, backslash) starts with ": it counts as code, as in is_prose.
    case "$f" in \"* | scripts/* | .github/workflows/ci.yml | .gitignore | web/.gitignore) echo true; return ;; esac
  done
  [ "$n" -gt 0 ] && echo false || echo true
}

case "${1:-}" in
  --classify) classify; exit 0 ;;
  --scripts-classify) classify_scripts; exit 0 ;;
  --scripts)
    shift
    [ "${1:-}" = pull_request ] || { echo true; exit 0; }
    shift
    [ "$#" -gt 0 ] || set -- HEAD^1 HEAD
    if ! files="$(git -c core.quotePath=false diff --no-renames --name-only "$@" 2>/dev/null)"; then echo true; exit 0; fi
    printf '%s\n' "$files" | classify_scripts
    exit 0
    ;;
  --list)
    load_rules || { echo "ci-changes.sh: no usable rules in $here/ci-prose.txt" >&2; exit 1; }
    while IFS= read -r f || [ -n "$f" ]; do
      [ -n "$f" ] && is_prose "$f" && printf '%s\n' "$f"
    done
    exit 0
    ;;
esac

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
