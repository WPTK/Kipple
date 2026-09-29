# SQA plan

Documents existing practice against the standard IEEE 730 Software Quality Assurance Plan outline, rather than
inventing new process. UAT (`docs/uat-plan.md`) asks "does it work for the owner"; this asks "is the process
that built it trustworthy." Written as a phase 5 release-readiness step; a gaps section at the end lists
candidate additions for the owner to accept or decline.

## 1. Purpose and scope

Kipple is a single-maintainer, agent-assisted, self-hosted single-user RSS reader, now a public repository.
Quality here means: never loses or corrupts the owner's read/starred state or feed data, never leaks personal
information (scrubbed before the repository went public), behaves correctly against real
Reader API clients (Reeder Classic, NetNewsWire), stays within its resource budgets, and is safe and
followable for a stranger to self-host from a clean machine.

## 2. Reference documents

`CLAUDE.md` (fixed decisions), `docs/design.md` (data model, API contract, design rationale), `docs/README.md`
(the docs index), `docs/uat-plan.md`, `docs/risk-register.md`, `docs/RELEASING.md`, `docs/deploy.md`,
`CHANGELOG.md`, `SECURITY.md`, `.env.example`; outside this repository, the master plan (`docs/plan.md`, a local
planning document) and the private `kipple-history` repo (meetings, decisions, diary, audits — the project's
institutional memory).

## 3. Management

One person directs and signs off (the owner); one agent implements, reviews and tests (Claude), following a
documented model policy — Sonnet for routine implementation, Opus for review/judging/ambiguous root-causing
(`CLAUDE.md`, "Process"). Concurrency rule: one writer on the Kipple repo/Host-A
instance at a time, to avoid half-applied changes. Deploys, Cloudflare changes, and go/no-go decisions are
reserved to the owner; routine build decisions are not (current phase 5 preference — minimal owner
involvement).

## 4. Documentation

Already comprehensive and versioned in-repo: architecture (`design.md`), release process
(`RELEASING.md`, `deploy.md`), UAT (`uat-plan.md`), UI decisions (`ui-decisions.md`), risks (`risk-register.md`),
a change history (`CHANGELOG.md`, Keep a Changelog 1.1.0), and `docs/README.md`, an index tying them together
for a newcomer (gap 3 below, since closed).

## 5. Standards, practices, conventions, and metrics

- **Coding/product conventions:** CLAUDE.md's "do not relitigate" decisions (stack, sync API, retention model,
  media policy, fonts/themes, non-goals).
- **Resource budgets, enforced as pass/fail gates, not aspirations:** image < 50 MB, idle RSS < 100 MB,
  `GOMEMLIMIT=64MiB`, compose `mem_limit: 256m`, content cap 500 KB per item, image cache default 1 GiB.
- **Style/lint:** gofmt, go vet, staticcheck, ESLint (web), theme contrast check in CI.
- **Coverage:** `go test -cover` and `npm run test:coverage` print coverage in CI and in
  `scripts/ci-local.ps1`, for visibility only, not as a gate (gap 2 below, since closed).

## 6. Reviews and audits

`/code-review high` before every deploy (medium for routine bot/script-equivalent work, per CLAUDE.md's
effort tiers). A full multi-agent code audit + changelog review ran for phase 5 (PR #26,
`kipple-history/audits/phase5-code-audit-2026-09-27.md`), following the same pattern as the 2026-09-26 audit
round. Standing rule: fix everything a review finds, no silent "not fixing" list.

## 7. Test

- **Unit/property tests:** `go test -race` (CI only — no `-race` locally, no gcc on this box), Vitest.
- **Fuzz:** `scripts/fuzz.ps1`, run by hand before every release, not in CI; a failure keeps its regression
  seed in `testdata/fuzz/`.
- **Contract tests:** replay recorded Reeder Classic / NetNewsWire request sequences (unit-level, in CI).
- **End-to-end UAT:** `docs/uat-plan.md` Suites 1-5 (scripted Playwright+axe, agent-driven scenarios,
  owner-only device checks, migration rehearsal, restore drill, Reader API regression replay against a real
  deployed build, fresh-machine Docker walkthrough). Planned for phase 5 and not yet executed; the Suite 1
  script is not built yet.
- **Security scanning:** govulncheck (on every dependency change), staticcheck, gosec (fails only on
  high/high), gitleaks, Trivy (image scan) — all in CI.

## 8. Problem reporting and corrective action

GitHub Issues (now public). `SECURITY.md` for vulnerability reports, through GitHub private vulnerability
reporting (turned on for the repository). Every behavior change,
including a bug fix, gets a changelog fragment under `changes/` (folded into `CHANGELOG.md` at release). Findings from reviews/audits/UAT go into
`kipple-history/audits/*.md` with severity and resolution status; nothing is silently dropped.

## 9. Tools, techniques, and methodologies

Go 1.27 toolchain, `modernc.org/sqlite` (CGO-free), Vite/Vitest, and, planned for phase 5 (not built yet),
Playwright + axe-core (an in-repo script, not a third-party installed skill — see the phase 5 addendum in
`docs/ui-decisions.md` for why an external UAT package was declined). `scripts/ci-local.ps1` mirrors CI for a fast pre-push check.

## 10. Media/configuration control

`.gitattributes` (`* text=auto eol=lf`) avoids CRLF drift. Docker image builds are reproducible and
attributable (`KIPPLE_VERSION`, `KIPPLE_VCS_REF` build args). Git tags are immutable once pushed — a bad
release gets a new version, never a moved tag. Migrations are one-way with a pre-migration snapshot written
automatically; rollback goes through that snapshot, never a hand-copied `kipple.db`.

## 11. Supplier control (third-party dependencies)

Every dependency choice in the master plan's Go-libraries table (a local planning document) carries a
documented reason; `docs/design.md` §1 records the ones that shape the design (the SQLite driver, routing). govulncheck runs
on every dependency change; Dependabot is configured; `THIRD_PARTY_NOTICES.md` is regenerated after dependency
changes. The phase 5 decision not to install the third-party `webapp-uat` npm skill (unverified package scope,
unnecessary features) is this control working as intended, not a one-off.

## 12. Records collection, maintenance, and retention

The `kipple-history` private repo is the durable record: `MEETINGS.md`, `DECISIONS.md`, `TIMELINE.md`,
`MILESTONES.md`, `CHALLENGES.md`, a diary, and `audits/`. This is unusually thorough for a project this size —
treat it as the SQA "records" requirement already satisfied, not a gap.

## 13. Training

No team to train. The closest analogues: `CLAUDE.md` functions as onboarding for any future contributor or
agent working on the repo, and the planned first-time Docker setup walkthrough is onboarding for self-hosters.

## 14. Risk management

`docs/risk-register.md` is the living register of open and closed risks, updated at each phase (gap 1 below,
since closed). It replaced the master plan's per-phase "Risks" sections and ad hoc notes. `docs/design.md` §11
keeps the design-level risks and their mitigations.

## Gaps / candidate additions (all four accepted and built, 2026-09-27)

1. **A living risk register.** Consolidate open risks (e.g., the unverified `document.hasFocus()` behavior in
   the installed iOS PWA, the 600k+-item feed-delete timeout noted in the phase 5 audit, the `x/crypto/openpgp`
   govulncheck advisory with no fix) into one doc, updated at each phase, instead of scattered phase call-outs
   that get forgotten.
2. **Code coverage visibility.** Report a coverage percentage from `go test`/Vitest in CI output — visibility
   only, not a gate, so untested areas are at least known rather than invisible.
3. **A docs index.** One page in `docs/` linking `plan.md`, `design.md`, `uat-plan.md`, `sqa-plan.md`,
   `RELEASING.md`, `deploy.md` with a one-line description each, for a newcomer (self-hoster or future
   contributor) who doesn't already know the project's shape.
4. **A stated issue-triage expectation for the now-public repo.** Something honest and low-commitment (e.g.,
   "security reports get a fast look via SECURITY.md; other issues are triaged best-effort by a solo
   maintainer, no SLA") rather than either silence or an over-promise, since external reports are now possible
   for the first time.

Status: all four are built. 1 is `docs/risk-register.md`; 2 is `-cover` in the Go test step and
`npm run test:coverage` in the web job (CI and `scripts/ci-local.ps1`); 3 is `docs/README.md`; 4 is the "Issue
triage" section of `SECURITY.md`.
