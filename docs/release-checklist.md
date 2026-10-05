# 1.0 readiness checklist

The written replacement for a second soak and a go/no-go meeting (`docs/RELEASING.md`, "Version path to 1.0.0"). It is
filled in against the last release candidate build. Each item is checked only with evidence beside it: a link, a run
number, a note in the findings doc. Items marked **owner** can only be done by the owner; the rest the agent does and
records. `docs/uat-plan.md` describes the suites.

| # | Item | Who | Evidence |
|---|---|---|---|
| 1 | Release-candidate soak: a few days of real use, zero new P0/P1 defects | owner | |
| 2 | UAT Suites 1, 2, 4 and 5 run on the rc build, no open P0/P1 | agent | |
| 3 | Restore drill on the rc image (backup, restore, start) | agent | |
| 4 | Rollback by digest drill (previous image digest back in place) | owner's go | |
| 5 | GitHub private vulnerability reporting is on (`SECURITY.md` depends on it) | agent | |
| 6 | Documentation run: every command in the README and `docs/deploy.md` works as written | agent | |
| 7 | First-time Docker walkthrough by a person who is not the author (a cold reader) | owner | |
| 8 | Reeder and NetNewsWire work against the rc | owner | |
| 9 | Firefox and Safari pass | owner | |
| 10 | Screen-reader pass | owner | |
| 11 | SBOM and its `.sigstore.json` signature are attached to the release; the `cosign verify` and `gh attestation verify` lines in the docs run and pass | agent | |
| 12 | Compatibility and deprecation document present (what is public API, "downgrade means restore") | agent | |
| 13 | Known-issues list is in the release notes | agent | |
| 14 | 1.0.0 release commit: README and `docs/deploy.md` say that the `latest` image tag works | agent | |

Owner's sign-off, one yes after reading the table above: **Kipple 1.0.0 may be released.**

Signed (owner, date): ____________________
