#!/usr/bin/env bash
# Release tag logic shared by .github/workflows/release.yml and its tests (scripts/release-tags.test.sh).
#
#   release-tags.sh check-tag TAG          exit 0 if TAG is vX.Y.Z or vX.Y.Z-(alpha|beta|rc).N (no leading zeros)
#   release-tags.sh check-stable TAG       exit 0 if TAG is a stable vX.Y.Z
#   release-tags.sh floating TAG           read all git tags on stdin; print the floating image tags TAG may move
#                                          (latest, X, X.Y), one per line. Prints nothing for a prerelease.
#   release-tags.sh is-highest TAG         read all git tags on stdin; exit 0 if TAG is the highest stable tag
#   release-tags.sh base-image DOCKERFILE  print name= and digest= lines for the last FROM (the runtime base)
set -euo pipefail

NUM='(0|[1-9][0-9]*)'
PRE='(-(alpha|beta|rc)\.[1-9][0-9]*)'
TAG_RE="^v${NUM}\.${NUM}\.${NUM}${PRE}?\$"
STABLE_RE="^v${NUM}\.${NUM}\.${NUM}\$"

is_tag() { [[ "$1" =~ $TAG_RE ]]; }
is_stable() { [[ "$1" =~ $STABLE_RE ]]; }

# stdin: any lines; stdout: the stable vX.Y.Z ones, highest first, deduplicated.
stable_sorted() {
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    if is_stable "$line"; then printf '%s\n' "${line#v}"; fi
  done | LC_ALL=C sort -t. -k1,1nr -k2,2nr -k3,3nr -u | sed 's/^/v/'
}

# stdin: sorted stable tags; arg: optional prefix ("v1." or "v1.2."). Prints the first tag with that prefix.
highest_of() {
  local p="${1:-}" t
  while IFS= read -r t; do
    if [[ "$t" == "$p"* ]]; then printf '%s\n' "$t"; return 0; fi
  done
  return 1
}

cmd="${1:-}"
case "$cmd" in
  check-tag)
    is_tag "${2:-}" ;;
  check-stable)
    is_stable "${2:-}" ;;
  is-highest)
    tag="${2:?tag}"
    is_stable "$tag" || exit 1
    top="$( { cat; printf '%s\n' "$tag"; } | stable_sorted | highest_of )"
    [ "$top" = "$tag" ] ;;
  floating)
    tag="${2:?tag}"
    is_tag "$tag" || { echo "not a release tag: $tag" >&2; exit 1; }
    is_stable "$tag" || exit 0 # a prerelease never moves a floating tag
    # The tag itself is always a candidate, even if the caller's list lacks it.
    sorted="$( { cat; printf '%s\n' "$tag"; } | stable_sorted )"
    v="${tag#v}"
    major="${v%%.*}"
    rest="${v#*.}"; minor="${rest%%.*}"
    if [ "$(printf '%s\n' "$sorted" | highest_of)" = "$tag" ]; then echo latest; fi
    if [ "$(printf '%s\n' "$sorted" | highest_of "v$major.")" = "$tag" ]; then echo "$major"; fi
    if [ "$(printf '%s\n' "$sorted" | highest_of "v$major.$minor.")" = "$tag" ]; then echo "$major.$minor"; fi
    exit 0 ;;
  base-image)
    file="${2:?Dockerfile}"
    # The last FROM is the runtime image: "FROM [--platform=x] name:tag@sha256:digest [AS x]".
    line="$(grep -E '^FROM[[:space:]]' "$file" | tail -n 1 | tr -d '\r')"
    ref="$(printf '%s\n' "$line" | tr -s ' ' '\n' | grep -E '@sha256:[0-9a-f]{64}$' | head -n 1 || true)"
    [ -n "$ref" ] || { echo "last FROM of $file is not pinned by digest: $line" >&2; exit 1; }
    echo "name=${ref%@*}"
    echo "digest=${ref#*@}" ;;
  *)
    echo "usage: $0 check-tag|check-stable|is-highest|floating|base-image ..." >&2; exit 2 ;;
esac
