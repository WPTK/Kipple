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
4. **CHANGELOG.md:** `node scripts/changelog.mjs preview` shows what is pending; add a one-paragraph
   `changes/_intro.md` if the release needs an intro. `node scripts/changelog.mjs release X.Y.Z` (`--dry-run` first) folds
   the `changes/` fragments into a new `## [X.Y.Z] - date` section, updates the compare links and deletes the
   fragments. Review the diff (`changes/README.md`).
5. **THIRD_PARTY_NOTICES.md:** regenerate with `node scripts/gen-notices.mjs` (after `cd web && npm ci`; after any dependency change at least).
   Any dependency change also needs a govulncheck run.
6. Commit `chore(release): X.Y.Z`, push, wait for CI on that commit.

## Deploy

7. **Off-box database copy first:** take the in-app backup zip (or `docker cp` the nightly
   `/data/backup/kipple-snapshot.db`, never the live `kipple.db`; see docs/deploy.md) and store it somewhere other than
   Host-A. Note the pre-migration snapshot name the app writes on start.
8. **Tag the deployed commit:** `git tag -a vX.Y.Z -m "Kipple X.Y.Z"` on the exact commit, then `git push origin vX.Y.Z`.
   Never move, delete or reuse a pushed tag; a bad release gets a new version.
   The tag push also starts `.github/workflows/release.yml`, which tests the tagged commit again, builds a multi-arch
   (amd64 and arm64) image, scans and smoke-tests it, then tags and signs it on `ghcr.io/wptk/kipple` (below).
9. **Deploy the tag, only the named service** (see CLAUDE.md, Deploy). The tag, not `main`, is what gets built:

       ssh host-a 'cd /home/user/kipple && git fetch --tags --force && git checkout vX.Y.Z && KIPPLE_VERSION=vX.Y.Z KIPPLE_VCS_REF=$(git rev-parse HEAD) docker compose -f /home/user/stack/docker-compose.yml build kipple && docker compose -f /home/user/stack/docker-compose.yml up -d kipple'
       ssh host-a 'cd /home/user/kipple && git checkout main'

   `git checkout vX.Y.Z` leaves the checkout on a detached HEAD; the second line puts it back on `main` once the image
   is built (the running container is not affected). `KIPPLE_VERSION` and `KIPPLE_VCS_REF` reach the build through the
   service's `build.args` (as in `docker-compose.example.yml`); `.git` is not in the build context, so without them
   the binary reports version `dev`. Never a bare `up`/`down`.
10. **Verify:** `ssh host-a 'docker exec kipple /kipple version'` prints `vX.Y.Z`; container healthy; `docker logs kipple` shows the migrations that were expected and no errors;
    `/api/greader.php` answers with Reeder; a refresh completes; memory stays flat after a few minutes (`docker stats`).
11. **GitHub Release** from the tag, with the CHANGELOG section as the notes (`node scripts/changelog.mjs notes X.Y.Z > notes.md`, then `gh release create vX.Y.Z --notes-file notes.md`; `-alpha/-beta/-rc` marked pre-release).
    Every pushed tag has one; keep it that way.

    **Container image:** the Release workflow must be green first (Actions > Release). It publishes `X.Y.Z` (a stable
    release also `X.Y`, `X` and `latest`; a prerelease never moves `latest` or a floating tag), signs the digest with
    cosign (keyless) and writes an "image-notes" block (the digest and the verify command) to its job summary and an
    artifact. If the Release already exists the workflow appends the block itself; otherwise append `image-notes.md`
    to the notes before `gh release create`, so each version maps to exactly one digest. Check it by hand:

        cosign verify ghcr.io/wptk/kipple:X.Y.Z \
          --certificate-identity-regexp '^https://github.com/WPTK/Kipple/\.github/workflows/release\.yml@refs/tags/v' \
          --certificate-oidc-issuer https://token.actions.githubusercontent.com

    **First image only (one time, owner):** the GHCR package starts private. In the repository's Packages, open
    `kipple`, confirm it is linked to `WPTK/Kipple` (the `org.opencontainers.image.source` label does that) and change
    its visibility to public; until then anonymous pulls fail.
12. **Website** (`WPTK/kipple-website`, kipple.cc, GitHub Pages from `main`), for every release, once the Release is published:
    - **Version text:** update "Where it stands" in `index.html` to the new tag (`grep -n "v0\." index.html README.md` finds
      every mention) and anything else on the site that says what is current.
    - **Screenshots:** in the Kipple repo, `cd web`, then `KIPPLE_SEED_SET=site npm run seed` (foreground; wait about a
      minute for the feeds), and in a second terminal
      `node scripts/site-shots.mjs --out <site>/screenshots --site <site>`. It writes the four WebP files at the sizes the
      site's design system names and re-renders `og.png`. Look at all five before committing. Update the capture note
      in the site's `design-system/DESIGN-SYSTEM.md` (section 10, `screenshots/`) with the new commit and the article shown.
      A release with no visible UI change may keep the old screenshots; the version text is never skipped.
    - Open a PR in the site repo and merge it; the merge is what publishes.

## Rolling back `latest`

Tags are never moved, so a bad stable image is superseded by the next patch version. Until that exists, run the
Release workflow by hand (Actions > Release > Run workflow) with `repoint_latest` set to the last good stable tag
(`vX.Y.Z`). It verifies that version's signature and re-points `latest`, `X.Y` and `X` at its digest without a
rebuild. Prereleases are refused. Signed digests are never deleted.

## Rollback

Follow docs/deploy.md, "Roll back an upgrade that migrated the schema": stop the service, restore the pre-migration
snapshot with the still-built new image (`docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-<old>-<new>-<ns>.db --yes`),
then check out the previous tag, rebuild with `KIPPLE_VERSION=<previous tag>`, start it, and `git checkout main` again.
Never copy a snapshot over the volume's `kipple.db` by hand: the `-wal` and `-shm` files left beside it would be
replayed onto the copy and corrupt it; `kipple restore` handles them. The database may have moved forward, so a rollback
across a migration always goes through the snapshot. Record what happened in the CHANGELOG or the diary; ship the fix as the next version.
