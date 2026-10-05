# Releasing Kipple

Every release, including alphas. Work top to bottom; do not deploy with a step open. The steps that protect production
(the exact green commit, the off-box copy, the migration rehearsal, the digest deploy, the rollback) apply to every
release. The test gates scale with what changed (see "Gates scale with what changed" below).

## Versioning

SemVer. Prereleases are `-alpha.N`, `-beta.N`, `-rc.N` (in that order; `N` counts up from 1). Alpha may change
anything, including the schema; beta is feature-complete; rc changes only fix bugs. Breaking changes to the
Reader API, the backup format or the settings keys need a major bump once 1.0.0 is out, a minor bump before.

### Gates scale with what changed

Pick the tier from the diff since the last gated commit (the last commit that went through the tier's gates), not from
the kind of release.

| What changed | Gates |
|---|---|
| Docs, test-only, release-commit, dependency or log-line changes | CI green on the exact head; govulncheck for any dependency change. No fuzz, no Suite 1, no review. |
| Code that parses, authenticates, migrates or renders UI | The full set, once, on the commit being tagged: two Go test runs, fuzz, Suite 1 and an Opus whole-diff review. Not repeated on every rc patch. |
| Anything that changes the schema | The above plus the migration rehearsal on a copy of the live snapshot (Suite 4). |

Kept for every release because they paid for themselves: tag only on the exact green commit, the off-box copy before a
deploy, the migration rehearsal (Suite 4), the Opus review of large diffs, and deterministic tests (no wall-clock
waits).

### Version path to 1.0.0 (decided 2026-10-03)

`0.8.0-beta.1`, `0.8.0-beta.2`, then `1.0.0-rc.N`, then `1.0.0`. A candidate for 1.0.0 is named `1.0.0-rc.N`, never `0.8.0-rc.N`. A
release candidate adds no features, so a feature lands in a beta. `0.8.0` never ships as a stable release. A change that
needs no schema or Reader API change and is small may go into `1.0.0-rc.1` instead if the PR says why; the rc rule (bug
fixes only) is the test.

- **Alpha → beta.1:** only once phase 5 is fully closed — code audit fixes merged (#26), Cloudflare Access JWT +
  passwordless shipped (#40), auto-night theme shipped (#41), the documentation run done (#42), and
  `docs/uat-plan.md` Suites 1, 2 and 4 executed clean of open P0/P1 defects. Feature-complete means verified,
  not declared: beta itself adds no new features, only fixes.
- **Beta → rc.1:** Suites 1, 2 and 4 re-verified on the beta build, plus the beta's own soak (the owner's real daily
  use, about three days, zero new P0/P1 defects). RC then means "only fixing what the soak or UAT found," not
  starting a fresh test cycle.
- **Suite 3 (owner-only device checks) is not a promotion gate at any step.** The owner runs it informally on his own
  devices as he uses each build. If it surfaces something real, that becomes its own tracked fix and does not block a
  release that is otherwise ready.
- **RC → 1.0.0:** one soak at the release candidate (a few days of the owner's real use, zero new P0/P1), then the
  written 1.0 readiness checklist, `docs/release-checklist.md`. The owner signs it with one yes; that yes cuts 1.0.0.
  There is no second soak and no go/no-go meeting. The checklist includes GitHub private vulnerability reporting still
  on (`SECURITY.md` depends on it), the documentation run and the first-time Docker walkthrough.
- A regression found during a soak resets that soak's clock (a new rc.N or a return to beta.N+1, whichever the
  defect's severity warrants) rather than being patched in place while the clock keeps running.

### Exception: 0.5.0-beta.1 adds features (owner-approved 2026-09-29)

The rule that a beta adds no new features is waived once, for 0.5.0-beta.1: the setup wizard (roadmap issue #33),
the pull-and-run image on GHCR and the build-info screens land in the first beta of the 0.5 line, because they
change how a newcomer meets Kipple and are only worth having verified together. The cost is that **the soak clock
restarts**: the 1-week soak toward rc.1 starts when 0.5.0-beta.1 is deployed, not at the 0.3.0-beta.2 soak (rc.1 was
not before 2026-10-06; it is now not before a week after the 0.5.0-beta.1 deploy), and Suites 1, 2 and 4 are re-verified
on that build. The exception is not a precedent: beta.2 onward adds no features again.

**2026-10-02 (owner):** the soak toward 0.5.0-rc.1 was abandoned. 0.6.0-beta.1 carries breaking cleanup (the removed
7080 fallback), which is a minor bump under Versioning, and the soak restarts at its own deploy. 0.5.0 never ships as
a stable release.

### 0.5.0-beta.1: merge order and pre-deploy checklist

Merged 2026-09-29 and 2026-09-30 on the owner's instruction, in this order, each with green CI on the exact commit and
the next PR brought up to date first (the PRs were stacked or overlapped; D and C went into B's branch, then B into
`main`). The review and fix PRs (#137, #150, #151), the real starter feeds (#152) and the font restore (#153) followed.
The record, kept for reference:

1. **#91**, the design document.
2. **A**, the release workflow (#111): the Dockerfile cross-compile and `.github/workflows/release.yml`.
3. **E**, build info (#114), rebased on A, because both edit the Dockerfile's build stages.
4. **B and C together**, the setup backend (#118) and the wizard UI (#119, stacked on it). Not B alone: the backend
   without its UI leaves a fresh install with nothing in the browser to claim it with.
5. **D**, the documentation (this PR), last, so every example it shows exists.

Then cut 0.5.0-beta.1 through the normal steps above, plus:

- **Changelog:** the `changes/` fragments of A, B, C and E are all there (`node scripts/changelog.mjs preview`); D adds
  none (docs only). The `changed` entries (port 1919, new installs in UTC and `TZ` now governing statistics, Host gate)
  are the ones an upgrader needs to read.
- **Release commit:** `README.md`, `docker-compose.pull.example.yml` and the "published image" text in `docs/deploy.md`
  name the image tag `0.5.0-beta.1` as an example of the version to pull: update them to the version being released
  (`grep -rn "0\.5\.0-beta\.1" README.md docker-compose.pull.example.yml docs/deploy.md`), and once a stable release
  exists, say `latest` works.
- **One-time, owner, after the first image is pushed:** make the GHCR package `kipple` public and confirm it is linked to
  `WPTK/Kipple` (step 11); until then anonymous pulls, and the README quickstart, fail.
- **On Host-A, before the upgrade** (docs/deploy.md, "Roll back an upgrade that migrated the schema"): confirm the `kipple`
  service has `KIPPLE_ADDR=:7080` set (from 0.6.0 an unset value is 1919, so a `7080:7080` mapping needs it); take
  the off-box backup (step 7); rehearse migration 0010 on a copy of the latest snapshot (Suite 4).
- **Deploy source.** 0.5.0-beta.1 is built from the tag on Host-A exactly as step 9 says (that step is unchanged; add
  `KIPPLE_BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)` to it if you want the build date on the About screen). From 0.5.0
  Host-A pulls the signed image by digest instead: after `cosign verify` (step 11), set the service's `image:` to
  `ghcr.io/wptk/kipple@sha256:<digest from the Release notes>` in place of its `build:` and `docker compose ... pull kipple`
  then `up -d kipple` (named service); build-from-tag stays as the fallback.
- **Verify after the deploy** (step 10, plus): `docker exec kipple /kipple version -v` shows the tag, commit and schema 15 (13 in 0.8.0-beta.2, 12 in 0.8.0-beta.1, 11 before 0.8);
  the log shows the port line (listening on the port `KIPPLE_ADDR` names, 1919 if unset) and no setup banner;
  Settings > About matches; the existing account signs in with no wizard.
- **UAT Suite 5** (`docs/uat-plan.md`, rewritten for the wizard) on a Linux host with Docker, not Host-B, against the pushed
  prerelease image, and once on arm64. Findings go in the `uat-findings` doc; P0 and P1 block the promotion to rc.
- **`kipple-history`, then the website (step 12):** record the exception and the decisions there; the site's quickstart text
  and "Where it stands" follow the README.

## Before the tag

1. **CI is green on the exact commit** you will deploy (not on a nearby one). Push first; nothing deploys from an unpushed tree.
2. **Fuzz and Suite 1, only for the second tier above** (code that parses, authenticates, migrates or renders UI, once,
   on the commit being tagged; skip both for docs, test-only, release-commit, dependency or log-line changes).
   **Fuzz, by hand:** `scripts\fuzz.ps1` (60 s per target; `-List` shows them). It must finish clean. The weekly
   `Fuzz` workflow runs the same script on the default branch, but until it has run green for several weeks the
   manual run stays the gate.
   A failure writes `testdata\fuzz\<Target>\<hash>` in the package: fix the bug, keep that file as a regression seed.
   **UAT Suite 1, also by hand:** in `web/`, `npm run build`, then `npm run seed` (it stays in the foreground), then
   in a second terminal, once the feeds have fetched (about a minute), `npm run uat` against that seeded local instance (never the live one; see `docs/uat-plan.md`, Suite 1). It must finish with exit code 0, or every
   remaining finding must be in `web/uat/waivers.json` with the owner's reason.
3. **`/code-review high`** (an Opus whole-diff review) on the diff since the last gated commit, for the second tier
   above. Fix every finding. Not needed for the first tier.
4. **CHANGELOG.md:** `node scripts/changelog.mjs preview` shows what is pending; add a one-paragraph
   `changes/_intro.md` if the release needs an intro. `node scripts/changelog.mjs release X.Y.Z` (`--dry-run` first) folds
   the `changes/` fragments into a new `## [X.Y.Z] - date` section, updates the compare links, deletes the
   fragments and sets the example image tag in `README.md`, `docker-compose.pull.example.yml` and `docs/deploy.md` to
   X.Y.Z (`changelog.mjs check`, run by CI, fails when one differs from the top CHANGELOG version). Review the diff
   (`changes/README.md`).
5. **THIRD_PARTY_NOTICES.md:** regenerate with `node scripts/gen-notices.mjs` (after `cd web && npm ci`; after any dependency change at least).
   Any dependency change also needs a govulncheck run.
6. Commit `chore(release): X.Y.Z`, push, wait for CI on that commit.

## Deploy

7. **Off-box database copy first:** take the in-app backup zip (or `docker cp` the nightly
   `/data/backup/kipple-snapshot.db`, never the live `kipple.db`; see docs/deploy.md) and store it somewhere other than
   Host-A. Note the pre-migration snapshot name the app writes on start. From the dev machine:

       ssh host-a 'docker cp kipple:/data/backup/kipple-snapshot.db /tmp/k.db' && scp host-a:/tmp/k.db '<backup-dir>\kipple\' && ssh host-a 'rm /tmp/k.db'
8. **Tag the deployed commit:** `git tag -a vX.Y.Z -m "Kipple X.Y.Z"` on the exact commit, then `git push origin vX.Y.Z`.
   Never move, delete or reuse a pushed tag; a bad release gets a new version.
   The tag push also starts `.github/workflows/release.yml`, which tests the tagged commit again, builds a multi-arch
   (amd64 and arm64) image, scans and smoke-tests it, then tags and signs it on `ghcr.io/wptk/kipple` (below).
9. **Deploy the tag, only the named service** (see CLAUDE.md, Deploy and releases). The tag, not `main`, is what gets built:

       ssh host-a 'cd /home/user/kipple && git fetch --tags --force && git checkout vX.Y.Z && KIPPLE_VERSION=vX.Y.Z KIPPLE_VCS_REF=$(git rev-parse HEAD) docker compose -f /home/user/stack/docker-compose.yml build kipple && docker compose -f /home/user/stack/docker-compose.yml up -d kipple'
       ssh host-a 'cd /home/user/kipple && git checkout main'

   `git checkout vX.Y.Z` leaves the checkout on a detached HEAD; the second line puts it back on `main` once the image
   is built (the running container is not affected). `KIPPLE_VERSION` and `KIPPLE_VCS_REF` reach the build through the
   service's `build.args` (as in `docker-compose.example.yml`); `.git` is not in the build context, so without them
   the binary reports version `dev`. Never a bare `up`/`down`.

   Host-A's own compose file must pass all three build args, not only the version, or the About screen shows the
   commit and build date as "unknown" (this happened on the 0.5.0-beta.1 deploy and was fixed by hand after a backup
   copy of the file). Under the `kipple` service's `build.args`, next to `VERSION`, add:

       VCS_REF: ${KIPPLE_VCS_REF:-unknown}
       BUILD_DATE: ${KIPPLE_BUILD_DATE:-unknown}

   and pass `KIPPLE_BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)` on the build command line next to the other two.
10. **Verify:** `ssh host-a 'docker exec kipple /kipple version'` prints `vX.Y.Z`; container healthy; `docker logs kipple` shows the migrations that were expected and no errors;
    `/api/greader.php` answers to a Reader API client; a refresh completes; memory stays flat after a few minutes (`docker stats`).
11. **GitHub Release** from the tag, with the CHANGELOG section as the notes (`node scripts/changelog.mjs notes X.Y.Z > notes.md`, then `gh release create vX.Y.Z --notes-file notes.md`). `-alpha`, `-beta` and `-rc` releases are marked pre-release
    (`--prerelease`), unless the owner says otherwise for that release; that choice is per release and is not a default.
    Every pushed tag has one; keep it that way. For 1.0.0 the notes carry a known-issues list.

    **Container image:** the Release workflow must be green first (Actions > Release). It publishes `X.Y.Z` (a stable
    release also `latest`, `X` and `X.Y` **only when this tag is the highest stable tag overall, in its major, or in
    its minor respectively**, so an older patch or a re-run never moves them backwards; a prerelease never moves `latest`
    or a floating tag), extracts the SBOM BuildKit embedded in the image (`kipple-X.Y.Z.sbom.json`, SPDX per platform),
    signs the digest and the SBOM file with cosign (keyless; `kipple-X.Y.Z.sbom.json.sigstore.json`) and writes an "image-notes" block (the digest, the verify command and the
    SBOM's sha256) to its job summary and to the `image-notes` artifact, which also holds the SBOM file and its signature bundle. The Release is
    normally created after the run, so download the artifact, append `image-notes.md` to the notes and attach the SBOM
    file and the bundle, so each version maps to exactly one digest and one SBOM:

        gh run download <run-id> -n image-notes
        cat image-notes.md >> notes.md
        gh release create vX.Y.Z --notes-file notes.md kipple-X.Y.Z.sbom.json kipple-X.Y.Z.sbom.json.sigstore.json

    (If the Release already exists when the run finishes, the workflow appends the block and uploads the SBOM and its bundle itself.)
    Check the signature by hand:

        cosign verify ghcr.io/wptk/kipple:X.Y.Z \
          --certificate-identity-regexp '^https://github\.com/WPTK/Kipple/\.github/workflows/release\.yml@refs/tags/v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.[1-9][0-9]*)?$' \
          --certificate-oidc-issuer https://token.actions.githubusercontent.com

    The image's `org.opencontainers.image.version` is `X.Y.Z` (the tag without its `v`, the string you pull), `created`
    is the commit time and `kipple version` prints `vX.Y.Z`. The `X.Y.Z` image tag is immutable: the workflow refuses
    to publish if it already exists with another digest. So **if a run fails part-way, use "Re-run failed jobs", never
    "Re-run all jobs"**: a full re-run rebuilds, gets a new digest and is refused at the Tag step (the Tag step comes after signing and attesting, so a pullable `X.Y.Z` always has its signature). "Re-run failed jobs" re-runs the whole failed job from its first step: the SBOM is read again, signing and attesting are repeated (harmless) and Tag finds the tag already on the same digest and does nothing. A tag whose image was
    never published and whose commit is wrong gets a new version, as ever.

    **Who may create `v*` tags is not something the workflow can enforce**: it signs whatever tag reaches it, and the
    signature identity only says "this workflow at some `v*` tag". The repository therefore has a **ruleset,
    "Protect Release Tags", active on `refs/tags/v*`**, with rules for creation, update, deletion and non-fast-forward
    (Settings > Rules). The workflow itself checks that the tag is annotated, well formed (no leading zeros) and on
    `main`.

    The floating-tag job and the rollback share one lock, so two releases cannot interleave their tag moves. GitHub
    keeps only one pending run per lock: if you push several tags in quick succession, a run of the floating-tag job that
    shows as cancelled must be re-run by hand (it recomputes from the tags as they are then).

    **First image only (one time, owner):** the GHCR package starts private. In the repository's Packages, open
    `kipple`, confirm it is linked to `WPTK/Kipple` (the `org.opencontainers.image.source` label does that) and change
    its visibility to public; until then anonymous pulls fail.
12. **Website** (`WPTK/kipple-website`, kipple.cc, GitHub Pages from `main`), once the Release is published (the version text on every release, a one-line PR):
    - **Version text:** update "Where it stands" in `index.html` to the new tag (`grep -n "v0\." index.html README.md` finds
      every mention) and anything else on the site that says what is current.
    - **Screenshots, only when the UI visibly changed** (a release with no visible UI change keeps the old ones; the
      version text is never skipped): in the Kipple repo, `cd web`, then `KIPPLE_SEED_SET=site npm run seed` (foreground; wait about a
      minute for the feeds), and in a second terminal
      `node scripts/site-shots.mjs --out <site>/screenshots --site <site>`. It writes the four WebP files at the sizes the
      site's design system names and re-renders `og.png`. Look at all five before committing. Update the capture note
      in the site's `design-system/DESIGN-SYSTEM.md` (section 10, `screenshots/`) with the new commit and the article shown.
    - Open a PR in the site repo and merge it; the merge is what publishes.

## Rolling back `latest`

Tags are never moved, so a bad stable image is superseded by the next patch version. Until that exists, run the
Release workflow by hand (Actions > Release > Run workflow) with `repoint_latest` set to the last good stable tag
(`vX.Y.Z`) and `allow_older` ticked (a rollback is by definition not the highest tag; without the box the run refuses,
which catches a typo). It verifies that version's signature and re-points `latest`, `X.Y` and `X` at its digest without a
rebuild. Prereleases are refused. Signed digests are never deleted. Until a higher patch of the rolled-back line
ships, later releases of that line do not move `latest` past the bad tag, because the bad tag is still the highest.

## Rollback

Follow docs/deploy.md, "Roll back an upgrade that migrated the schema": stop the service, restore the pre-migration
snapshot with the still-built new image, then start the previous version. On Host-A that is (while the build-from-tag
deploy of step 9 is in use):

       ssh host-a 'cd /home/user/stack && docker compose stop kipple'
       ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-<old>-<new>-<ns>.db --yes'
       ssh host-a 'cd /home/user/kipple && git fetch --tags --force && git checkout <previous tag>'
       ssh host-a 'cd /home/user/stack && KIPPLE_VERSION=<previous tag> KIPPLE_VCS_REF=$(git -C /home/user/kipple rev-parse HEAD) docker compose build kipple && docker compose up -d kipple'
       ssh host-a 'cd /home/user/kipple && git checkout main'

With the pull-by-digest deploy, put the previous image's digest back in the `image:` line and `pull kipple` then `up -d kipple`
instead of the three git/build lines.
Never copy a snapshot over the volume's `kipple.db` by hand: the `-wal` and `-shm` files left beside it would be
replayed onto the copy and corrupt it; `kipple restore` handles them. The database may have moved forward, so a rollback
across a migration always goes through the snapshot. Record what happened in the CHANGELOG or the diary; ship the fix as the next version.

## Badges and supply-chain checks

The README badges are all live except two that need a one-time setup by the owner:

- **Test coverage (Codecov).** Sign in to codecov.io with GitHub, add `WPTK/Kipple`, and put the upload token in the
  repository secret `CODECOV_TOKEN` (Settings > Secrets and variables > Actions). CI uploads the Go profile and the web
  lcov report (flags `go` and `web`) only when the secret exists, so fork PRs and the state before setup do not fail.
  The badge is empty until the first upload on main. Coverage is visibility only (`codecov.yml`, `docs/sqa-plan.md`):
  no status check gates a merge.
- **OpenSSF Scorecard.** `.github/workflows/scorecard.yml` needs no secret. It publishes after its first run on main
  (Actions > Scorecard > Run workflow to trigger it by hand); until then the badge says "invalid repo path". It then
  reruns weekly and on branch protection changes.
- **Views** is a hits.sh counter and is approximate.
- The GHCR tags badge (ghcr-badge) lists the newest three non-`sha*` tags. Do not switch it to the `latest_tag` or
  `size` endpoints: the first shows a signature tag and the second fails on a multi-arch image.
