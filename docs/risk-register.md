# Risk register

Living document (SQA plan candidate addition, accepted 2026-09-27). Consolidates open risks that were
previously scattered across phase "Risks" sections in `docs/plan.md` and ad hoc notes, so they stay visible
instead of getting forgotten once a phase ends. Update at each phase; move a resolved risk to "Closed" with the
date and how it closed rather than deleting it.

| ID | Risk | Likelihood | Impact | Mitigation / status |
|---|---|---|---|---|
| R1 | `document.hasFocus()` may report false while the installed iPhone PWA is foregrounded but the phone is locked/backgrounded, silently undercounting reading time on iOS. | Unknown — needs a real device to confirm | Medium (stats accuracy on the owner's primary reading device) | Open, tracked as [#30](https://github.com/WPTK/Kipple/issues/30). Resolves as part of UAT Suite 3 (TC-D4, `docs/uat-plan.md`). |
| R2 | Deleting a feed with roughly 600k+ items can outlast the 60s HTTP request timeout. | Low (needs an unusually large single feed) | Low — the delete still completes and resumes; the UI catches up | Open, accepted, tracked as [#31](https://github.com/WPTK/Kipple/issues/31). Not fixed because the failure mode is graceful, not data-losing. |
| R3 | `golang.org/x/crypto/openpgp` carries advisory GO-2026-5932 with no upstream fix. | N/A (advisory, not exploitable here) | None currently — Kipple's code never calls the affected package | Open, accepted. Re-check on every `govulncheck` run (already in CI); revisit if a fix ships or if a future dependency starts calling it. |
| R4 | The Cloudflare Access JWT + passwordless branch was developed in parallel with the phase 5 code-audit branch (merged as #26), and both touch `internal/api/api.go` and `internal/api/login.go`. | Medium (both were active in parallel by design) | Low — a rebase conflict, not a logic conflict, per the audit agent's own note | Open, expected. The JWT/passwordless branch now rebases against `main` post-merge rather than against the audit branch directly; not a reason it should have been un-parallelized. |
| R5 | The repository is now public; any future commit or PR could reintroduce personal data (hostnames, real name) that was scrubbed before going public. | Low, but consequence is hard to fully undo once pushed | High (privacy) | Ongoing. `.gitleaks.toml`/CI's gitleaks scan catches credential-shaped secrets but not personal-data patterns by design — review new docs/comments for names/hostnames before merging, same discipline as the pre-public scrub. |
| R6 | Running heavy tests (e.g. a full container test suite) in Docker on Gilead can trip `DockerCleanStart` and take public services offline for 20-50s. | Medium if forgotten | Medium (brief outage of live public services, including Kipple itself) | Mitigated by policy: no heavy Docker test runs on Gilead (memory `feedback-no-heavy-tests-in-docker-on-gilead`); host-level test runs and CI cover it instead. Already caused one real outage on 2026-09-27 — treat as proven, not hypothetical. |
| R7 | The beta→rc and rc→1.0 soak periods (1 week, then a few days) reset on any regression found during the window. | Medium — normal software has bugs | Low-medium (delays 1.0, not a quality risk) | Accepted by design (`docs/RELEASING.md`) — the alternative (patching in place without resetting the clock) would undermine what a soak period is for. |

## Closed

| ID | Risk | Closed | How |
|---|---|---|---|
| C1 | Reeder Classic's `mark-all-as-read` `ts` unit (µs/ms/s) was undocumented anywhere public. | 2026-09 (observed in production) | Digit-count parsing (`docs/plan.md`) has handled real Reeder traffic since the phase 1 deploy with no reported misbehavior; kept as a documented design choice rather than a live risk. |
| C2 | 88 `http://` feedburner-hosted feeds were expected to 301 to https on first fetch. | Phase 1 deploy | The redirect-migration rule handled all of them; health view showed the notices as expected, not alarming. |
