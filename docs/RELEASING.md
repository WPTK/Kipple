# Releasing Kipple

Every release, including alphas. Work top to bottom; do not deploy with a step open.

## Versioning

SemVer. Prereleases are `-alpha.N`, `-beta.N`, `-rc.N` (in that order; `N` counts up from 1). Alpha may change
anything, including the schema; beta is feature-complete; rc changes only fix bugs. Breaking changes to the
Reader API, the backup format or the settings keys need a major bump once 1.0.0 is out, a minor bump before.

### Alpha → beta → rc → 1.0.0 (decided 2026-09-27)

- **Alpha → beta.1:** only once phase 5 is fully closed — code audit fixes merged (#26), Cloudflare Access JWT +
  passwordless shipped (#40), auto-night theme shipped (#41), the documentation run done (#42), and
  `docs/uat-plan.md` Suites 1, 2 and 4 executed clean of open P0/P1 defects. Feature-complete means verified,
  not declared: no more planned phases remain once beta cuts, and beta itself adds no new features, only fixes.
- **Beta → rc.1:** Suites 1, 2 and 4 re-verified stable on the beta build, plus a **1-week soak period** of the
  owner's real daily use producing zero new P0/P1 defects. RC then means "only fixing what the soak period or
  UAT found," not starting a fresh test cycle.
- **Suite 3 (owner-only device checks) is not a promotion gate at any step** (decided 2026-09-27, superseding
  the original plan). It stays open-ended: the owner runs it informally on his own devices as he uses each
  build, not as a one-time checklist to close before cutting beta or rc. If it surfaces something real (the
  `document.hasFocus()` question, a gesture bug, an install/Share issue), that becomes its own tracked fix, on
  its own timeline — it doesn't block a release that's otherwise ready.
- **RC → 1.0.0:** a second, shorter soak (a few days) on the final rc build with zero regressions, GitHub
  private vulnerability reporting still turned on (it is on as of 2026-09-27; `SECURITY.md` depends on it), the
  documentation run and the first-time Docker setup walkthrough proven end-to-end, then the final go/no-go
  meeting — its approval is what cuts 1.0.0.
- A regression found during a soak period resets that soak's clock (a new rc.N or a return to beta.N+1,
  whichever the defect's severity warrants) rather than being patched in place while the clock keeps running.

## Before the tag

1. **CI is green on the exact commit** you will deploy (not on a nearby one). Push first; nothing deploys from an unpushed tree.
2. **Fuzz, by hand, not in CI:** `scripts\fuzz.ps1` (60 s per target; `-List` shows them). It must finish clean.
   A failure writes `testdata\fuzz\<Target>\<hash>` in the package: fix the bug, keep that file as a regression seed.
   **UAT Suite 1, also by hand:** in `web/`, `npm run build`, then `npm run seed` (it stays in the foreground), then
   in a second terminal, once the feeds have fetched (about a minute), `npm run uat` against that seeded local instance (never the live one; see `docs/uat-plan.md`, Suite 1). It must finish with exit code 0, or every
   remaining finding must be in `web/uat/waivers.json` with the owner's reason.
3. **`/code-review high`** on the diff since the last deployed tag. Fix every finding.
4. **CHANGELOG.md:** move `[Unreleased]` under `## [X.Y.Z] - date`, add a fresh empty `[Unreleased]`, update the
   compare links.
5. **THIRD_PARTY_NOTICES.md:** regenerate with `node scripts/gen-notices.mjs` (after `cd web && npm ci`; after any dependency change at least).
   Any dependency change also needs a govulncheck run.
6. Commit `chore(release): X.Y.Z`, push, wait for CI on that commit.

## Deploy

7. **Off-box database copy first:** take the in-app backup zip (or `docker cp` the nightly
   `/data/backup/kipple-snapshot.db`, never the live `kipple.db`; see docs/deploy.md) and store it somewhere other than
   Host-A. Note the pre-migration snapshot name the app writes on start.
8. **Tag the deployed commit:** `git tag -a vX.Y.Z -m "Kipple X.Y.Z"` on the exact commit, then `git push origin vX.Y.Z`.
   Never move, delete or reuse a pushed tag; a bad release gets a new version.
9. **Deploy the tag, only the named service** (see CLAUDE.md, Deploy). The tag, not `main`, is what gets built:

       ssh host-a 'cd /home/user/kipple && git fetch --tags --force && git checkout vX.Y.Z && KIPPLE_VERSION=vX.Y.Z KIPPLE_VCS_REF=$(git rev-parse HEAD) docker compose -f /home/user/stack/docker-compose.yml build kipple && docker compose -f /home/user/stack/docker-compose.yml up -d kipple'
       ssh host-a 'cd /home/user/kipple && git checkout main'

   `git checkout vX.Y.Z` leaves the checkout on a detached HEAD; the second line puts it back on `main` once the image
   is built (the running container is not affected). `KIPPLE_VERSION` and `KIPPLE_VCS_REF` reach the build through the
   service's `build.args` (as in `docker-compose.example.yml`); `.git` is not in the build context, so without them
   the binary reports version `dev`. Never a bare `up`/`down`.
10. **Verify:** `ssh host-a 'docker exec kipple /kipple version'` prints `vX.Y.Z`; container healthy; `docker logs kipple` shows the migrations that were expected and no errors;
    `/api/greader.php` answers with Reeder; a refresh completes; memory stays flat after a few minutes (`docker stats`).
11. **GitHub Release** from the tag, with the CHANGELOG section as the notes (`-alpha/-beta/-rc` marked pre-release).
    Every pushed tag has one; keep it that way.

## Rollback

Follow docs/deploy.md, "Roll back an upgrade that migrated the schema": stop the service, restore the pre-migration
snapshot with the still-built new image (`docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-<old>-<new>-<ns>.db --yes`),
then check out the previous tag, rebuild with `KIPPLE_VERSION=<previous tag>`, start it, and `git checkout main` again.
Never copy a snapshot over the volume's `kipple.db` by hand: the `-wal` and `-shm` files left beside it would be
replayed onto the copy and corrupt it; `kipple restore` handles them. The database may have moved forward, so a rollback
across a migration always goes through the snapshot. Record what happened in the CHANGELOG or the diary; ship the fix as the next version.
