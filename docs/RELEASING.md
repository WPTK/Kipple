# Releasing Kipple

Every release, including alphas. Work top to bottom; do not deploy with a step open.

## Versioning

SemVer. Prereleases are `-alpha.N`, `-beta.N`, `-rc.N` (in that order; `N` counts up from 1). Alpha may change
anything, including the schema; beta is feature-complete; rc changes only fix bugs. Breaking changes to the
Reader API, the backup format or the settings keys need a major bump once 1.0.0 is out, a minor bump before.

## Before the tag

1. **CI is green on the exact commit** you will deploy (not on a nearby one). Push first; nothing deploys from an unpushed tree.
2. **Fuzz, by hand, not in CI:** `scripts\fuzz.ps1` (60 s per target; `-List` shows them). It must finish clean.
   A failure writes `testdata\fuzz\<Target>\<hash>` in the package: fix the bug, keep that file as a regression seed.
3. **`/code-review high`** on the diff since the last deployed tag. Fix every finding.
4. **CHANGELOG.md:** move `[Unreleased]` under `## [X.Y.Z] - date`, add a fresh empty `[Unreleased]`, update the
   compare links.
5. **THIRD_PARTY_NOTICES.md:** regenerate with `scripts/gen-notices.mjs` when present (after any dependency change at least).
   Any dependency change also needs a govulncheck run.
6. Commit `chore(release): X.Y.Z`, push, wait for CI on that commit.

## Deploy

7. **Off-box database copy first:** take the in-app backup zip (or copy the volume's `kipple.db` snapshot) and store
   it somewhere other than Host-A. Note the pre-migration snapshot name the app writes on start.
8. **Tag the deployed commit:** `git tag -a vX.Y.Z -m "Kipple X.Y.Z"` on the exact commit, then `git push origin vX.Y.Z`.
   Never move, delete or reuse a pushed tag; a bad release gets a new version.
9. **Deploy only the named service** (see CLAUDE.md, Deploy): `git pull`, `docker compose -f /home/user/stack/docker-compose.yml build kipple`,
   `up -d kipple`. Never a bare `up`/`down`.
10. **Verify:** container healthy; `docker logs kipple` shows the migrations that were expected and no errors;
    `/api/greader.php` answers with Reeder; a refresh completes; memory stays flat after a few minutes (`docker stats`).
11. **GitHub Release** from the tag, with the CHANGELOG section as the notes (`-alpha/-beta/-rc` marked pre-release).
    No Releases exist yet; they are required once the repo is public.

## Rollback

Stop the service, restore the pre-migration snapshot (`kipple restore <snapshot> --yes`, or copy it over the volume's
`kipple.db`), redeploy the previous tag's image, start it. The database may have moved forward, so a rollback across a
migration always goes through the snapshot. Record what happened in the CHANGELOG or the diary; ship the fix as the next version.
