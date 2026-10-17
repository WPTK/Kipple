# 1.0 readiness checklist

The written replacement for a second soak and a go/no-go meeting (`docs/maintainers/RELEASING.md`, "Version path to 1.0.0"). It is
filled in against the last release candidate build. Each item is checked only with evidence beside it: a link, a run
number, a note in the findings doc. Items marked **owner** can only be done by the owner; the rest the agent does and
records. `docs/maintainers/uat-plan.md` describes the suites.

| # | Item | Who | Evidence |
|---|---|---|---|
| 1 | Release-candidate soak: a few days of real use, zero new P0/P1 defects | owner | |
| 2 | UAT Suites 1, 2, 4 and 5 run on the rc build, no open P0/P1 | agent | Partial, no open P0/P1. Suite 1 passed on Chromium and WebKit on main 6ada23b1 (a local harness failure in `images.mjs` and a WebKit focus race were fixed in test code, #430; CI `Browser UAT` green on 11cb2402). Suite 2 passed with gaps: quickadd of a loopback URL refused as a private network; real sync clients and the access-proxy positive case not run. Suite 4 restore drill passed (counts and id sets identical, corrupt zip refused). Suite 5 on a Linux host passed Runs A, B, C, E; Run D (arm64) partial: the arm64 manifest is present and the binary is an aarch64 ELF reporting v1.0.0-rc.1, but it was not executed. P2 #431 is fixed on main (#433); P3s #432 and #435 remain open (#435 fix in review, #436) |
| 3 | Restore drill on the rc image (backup, restore, start) | agent | Pass. Restore drill on image `sha256:64032d91af88fb67e76df7aab82ecf7959779591d3b97693c2110066b0c97500`, through the app (wizard restore) and through the command-line runbook |
| 4 | Rollback by digest drill (previous image digest back in place) | owner's go | |
| 5 | GitHub private vulnerability reporting is on (`SECURITY.md` depends on it) | agent | Pass. Private vulnerability reporting is enabled (GitHub API `private-vulnerability-reporting` returns `enabled: true`) |
| 6 | Documentation run: every command in the README and `docs/deploy.md` works as written | agent | Pass after fixes. Suite 5 followed the README and `docs/deploy.md` as written and found 11 documentation defects, all fixed in #434 (merged) |
| 7 | First-time Docker walkthrough by a person who is not the author (a cold reader) | owner | |
| 8 | A Reader API client works against the rc | owner | |
| 9 | Firefox and Safari pass | owner | |
| 10 | Screen-reader pass | owner | |
| 11 | SBOM and its `.sigstore.json` signature are attached to the release; the `cosign verify` and `gh attestation verify` lines in the docs run and pass | agent | Pass. The release `v1.0.0-rc.1` carries `kipple-1.0.0-rc.1.sbom.json` and `kipple-1.0.0-rc.1.sbom.json.sigstore.json`. The release-publish run verified cosign on the image digest, the image tag and the SBOM, and `gh attestation verify` named the tag workflow and commit 11cb2402 (the tag points at that commit). Not re-run for this PR |
| 12 | Compatibility and deprecation document present (what is public API, "downgrade means restore") | agent | Done. `docs/compatibility.md` exists |
| 13 | Known-issues list is in the release notes | agent | Done for the rc. The v1.0.0-rc.1 release notes have a "Known issues in this release candidate" section (#431, #435, #432) |
| 14 | 1.0.0 release commit: README and `docs/deploy.md` say that the `latest` image tag works | agent | Not for the rc; applies to 1.0.0 only (README and `docs/deploy.md` `latest` tag, and the `docker-compose.pull.example.yml` pin, now `1.0.0-rc.1`). Not done |
| 15 | Every screen and every download walked in a real browser on the rc build, at the mobile preset and on desktop (`docs/maintainers/RELEASING.md`, step 3), and the `Browser UAT` workflow green on the tagged commit | agent | Partial. Phone shakedown at 7 phone sizes including landscape on 6ada23b1 covered 48 screens; `Browser UAT` workflow green on the tagged commit 11cb2402. Found one P2 (#431, fixed in #433) and P3s (#432) |
| 16 | Phone-size shakedown (`docs/maintainers/uat-plan.md`, Suite 6) at the six iPhone sizes, portrait and landscape, no open P0/P1 | agent | Partial. Shakedown at 7 phone sizes (portrait and landscape) on 6ada23b1 covered 48 screens; one P2 (#431) found and fixed on main, P3s tracked in #432. No open P0/P1. Run against 7 sizes, not the six listed here |

Owner's sign-off, one yes after reading the table above: **Kipple 1.0.0 may be released.**

Signed (owner, date): ____________________
