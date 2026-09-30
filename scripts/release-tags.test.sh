#!/usr/bin/env bash
# Tests for scripts/release-tags.sh. Run: bash scripts/release-tags.test.sh
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rt="$here/release-tags.sh"
fails=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

ok() { # ok NAME EXPECTED ACTUAL
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want [$2] got [$3]"; fails=$((fails + 1)); fi
}
tags="v0.3.0-beta.2
v1.0.0
v1.2.3
v1.2.4
v1.10.0
v2.0.0-rc.1
v2.0.0
v1.9.9
not-a-tag"
floating() { printf '%s\n' "$tags" | "$rt" floating "$1" | paste -sd' ' -; }

# Tag shape: leading zeros and junk are refused.
for t in v1.2.3 v0.0.0 v10.20.30 v1.2.3-alpha.1 v1.2.3-beta.12 v1.2.3-rc.3; do
  "$rt" check-tag "$t" && ok "accepts $t" 0 0 || ok "accepts $t" accepted rejected
done
for t in v01.2.3 v1.02.3 v1.2.03 v1.2.3-alpha.0 v1.2.3-alpha.01 v1.2.3-gamma.1 1.2.3 v1.2 v1.2.3.4 v1.2.3-rc v1.2.3-rc.1-x " v1.2.3" "v1.2.3
v9.9.9"; do
  "$rt" check-tag "$t" && ok "rejects [$t]" rejected accepted || ok "rejects [$t]" 0 0
done
"$rt" check-stable v1.2.3 && ok "stable v1.2.3" 0 0 || ok "stable v1.2.3" yes no
"$rt" check-stable v1.2.3-rc.1 && ok "prerelease is not stable" no yes || ok "prerelease is not stable" 0 0

# Floating tags: only the highest overall gets latest, the highest in a major gets X, in a minor X.Y.
ok "highest overall" "latest 2 2.0" "$(floating v2.0.0)"
ok "older patch of the highest minor-of-major-1" "" "$(floating v1.2.3)"
ok "highest 1.x is 1.10.0" "1 1.10" "$(floating v1.10.0)"
ok "1.9.9 is highest in 1.9 only" "1.9" "$(floating v1.9.9)"
ok "v1.2.4 is highest in 1.2 only" "1.2" "$(floating v1.2.4)"
ok "an old major only moves its own X.Y" "1.0" "$(floating v1.0.0)"
ok "prerelease moves nothing" "" "$(floating v2.0.0-rc.1)"
ok "tag missing from the list still counts" "latest 3 3.0" "$(printf '%s\n' "$tags" | "$rt" floating v3.0.0 | paste -sd' ' -)"
ok "first ever release" "latest 0 0.5" "$(printf 'v0.5.0\n' | "$rt" floating v0.5.0 | paste -sd' ' -)"
ok "numeric not lexical order" "latest 1 1.10" "$(printf 'v1.9.0\nv1.10.0\nv1.2.0\n' | "$rt" floating v1.10.0 | paste -sd' ' -)"
ok "prereleases in the list are ignored" "latest 1 1.0" "$(printf 'v1.0.0\nv2.0.0-rc.1\n' | "$rt" floating v1.0.0 | paste -sd' ' -)"
ok "CRLF tag list" "latest 1 1.0" "$(printf 'v1.0.0\r\n' | "$rt" floating v1.0.0 | paste -sd' ' -)"
"$rt" floating v01.0.0 </dev/null 2>/dev/null && ok "floating rejects a malformed tag" rejected accepted || ok "floating rejects a malformed tag" 0 0

# is-highest (the repoint sanity).
printf '%s\n' "$tags" | "$rt" is-highest v2.0.0 && ok "is-highest v2.0.0" 0 0 || ok "is-highest v2.0.0" yes no
printf '%s\n' "$tags" | "$rt" is-highest v1.10.0 && ok "v1.10.0 is not highest" no yes || ok "v1.10.0 is not highest" 0 0
printf '%s\n' "$tags" | "$rt" is-highest v2.0.0-rc.1 && ok "prerelease is never highest" no yes || ok "prerelease is never highest" 0 0

# base-image reads the digest of the runtime FROM, so the label cannot go stale.
d1=sha256:$(printf 'a%.0s' $(seq 64))
d2=sha256:$(printf 'b%.0s' $(seq 64))
printf 'FROM --platform=$BUILDPLATFORM node:22@%s AS web\r\nFROM gcr.io/x/static:nonroot@%s\r\n' "$d1" "$d2" >"$tmp/Dockerfile"
ok "base-image" "name=gcr.io/x/static:nonroot digest=$d2" "$("$rt" base-image "$tmp/Dockerfile" | paste -sd' ' -)"
printf 'FROM node:22 AS web\nFROM gcr.io/x/static:nonroot\n' >"$tmp/Unpinned"
"$rt" base-image "$tmp/Unpinned" 2>/dev/null && ok "unpinned base is refused" rejected accepted || ok "unpinned base is refused" 0 0
ok "the real Dockerfile is pinned" 0 "$("$rt" base-image "$here/../Dockerfile" >/dev/null 2>&1; echo $?)"

if [ "$fails" -ne 0 ]; then echo "$fails failure(s)"; exit 1; fi
echo "all passed"
