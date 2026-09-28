# Risk register

Living document (SQA plan candidate addition, accepted 2026-09-27). Consolidates open risks that were
previously scattered across phase "Risks" sections in the master plan (a local planning document, not in this
repository) and ad hoc notes, so they stay visible
instead of getting forgotten once a phase ends. Update at each phase; move a resolved risk to "Closed" with the
date and how it closed rather than deleting it.

| ID | Risk | Likelihood | Impact | Mitigation / status |
|---|---|---|---|---|
| R2 | Deleting a feed with roughly 600k+ items can outlast the 60s HTTP request timeout. | Low (needs an unusually large single feed) | Low — the delete still completes and resumes; the UI catches up | Open, accepted, tracked as [#31](https://github.com/WPTK/Kipple/issues/31). Not fixed because the failure mode is graceful, not data-losing. |
| R3 | `golang.org/x/crypto/openpgp` carries advisory GO-2026-5932 with no upstream fix. | N/A (advisory, not exploitable here) | None currently — Kipple's code never calls the affected package | Open, accepted. Re-check on every `govulncheck` run (already in CI); revisit if a fix ships or if a future dependency starts calling it. |
| R5 | The repository is now public; any future commit or PR could reintroduce personal data (hostnames, real name) that was scrubbed before going public. | Low, but consequence is hard to fully undo once pushed | High (privacy) | Ongoing. `.gitleaks.toml`/CI's gitleaks scan catches credential-shaped secrets but not personal-data patterns by design — review new docs/comments for names/hostnames before merging, same discipline as the pre-public scrub. |
| R6 | Running heavy tests (e.g. a full container test suite) in Docker on the development machine (Host-B, which also runs the Cloudflare tunnel) can trip its Docker self-heal task and take public services offline for 20-50s. | Medium if forgotten | Medium (brief outage of live public services, including Kipple itself) | Mitigated by policy: no heavy Docker test runs on Host-B (`scripts/ci-local.ps1` without `-Docker`); host-level test runs and CI cover it instead. Already caused one real outage on 2026-09-27 — treat as proven, not hypothetical. |
| R7 | The beta→rc and rc→1.0 soak periods (1 week, then a few days) reset on any regression found during the window. | Medium — normal software has bugs | Low-medium (delays 1.0, not a quality risk) | Accepted by design (`docs/RELEASING.md`) — the alternative (patching in place without resetting the clock) would undermine what a soak period is for. |
| R8 | CodeQL flags `internal/discover/discover.go`'s user-controlled URL reaching an HTTP client as `go/request-forgery`. | N/A (false positive) | None — SSRF is enforced elsewhere | Dismissed. `discover.Find` fetches through a caller-supplied guarded `RoundTripper` (`internal/api/feedadmin.go:213`, `s.opt.Guard`); the SSRF guard is a custom `Dialer.Control` at dial time on the resolved address, which CodeQL's static analysis can't trace through. |
| R9 | CodeQL flags `internal/greader/itemid.go:63`'s `uint64`→`int64` conversion as `go/incorrect-integer-conversion`. | N/A (false positive) | None — same bit width, not a narrowing conversion | Dismissed. Deliberate bitcast, documented in the code comment. Item ids are positive by construction; a malformed inbound hex id just fails to match any real item, not a vulnerability. |

## Closed

| ID | Risk | Closed | How |
|---|---|---|---|
| C1 | Reeder Classic's `mark-all-as-read` `ts` unit (µs/ms/s) was undocumented anywhere public. | 2026-09 (observed in production) | Digit-count parsing (`docs/design.md` §3, mark-all `ts`) has handled real Reeder traffic since the phase 1 deploy with no reported misbehavior; kept as a documented design choice rather than a live risk. |
| C2 | 88 `http://` feedburner-hosted feeds were expected to 301 to https on first fetch. | Phase 1 deploy | The redirect-migration rule handled all of them; health view showed the notices as expected, not alarming. |
| C3 | (Was R4.) The Cloudflare Access JWT + passwordless branch was developed in parallel with the phase 5 code-audit branch, and both touched `internal/api/api.go` and `internal/api/login.go`. | 2026-09-27 | The audit merged first (#26); the JWT/passwordless branch was then brought up to date with `main` and merged as #40. |
| C4 | (Was R1.) `document.hasFocus()` may report false while the installed iPhone PWA is foregrounded but the phone is locked/backgrounded, silently undercounting reading time on iOS. | 2026-09-27 | Owner confirmed all of UAT Suite 3, including TC-D4, passes on his phone (0.3.0-beta.1 feedback). Issue [#30](https://github.com/WPTK/Kipple/issues/30) closed. |
