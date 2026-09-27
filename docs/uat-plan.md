# UAT plan

Adapted from standard UAT methodology (entry/exit criteria, traceable test cases, severity-triaged defects,
formal sign-off) to a single-owner, single-user project with no separate QA team. Functional testing (`go test`,
Vitest, `/code-review`) already answers "does it work?" This plan answers "does it work for the owner, on his
actual devices, doing his actual reading?" It is a phase 5 release-readiness step, feeding the final go/no-go
meeting (`docs/RELEASING.md`, promotion criteria). Status: planned, not yet executed.

## Roles (mapped from the standard 5-role model)

| Standard role | Here |
|---|---|
| QA professional (orchestrates) | Claude: writes test cases, executes what can be automated or agent-driven, tracks and triages defects |
| End user | The owner — the only user, on desktop Chrome and an installed iPhone PWA, plus Reeder Classic and NetNewsWire as Reader API clients |
| Business analyst / product owner | The owner (same person) — decisions already recorded in `docs/ui-decisions.md` and `kipple-history` are the "requirements" test cases trace to |
| Development team | Claude, via fix PRs against defects found |
| Sign-off authority | The owner, at the final go/no-go meeting |

No separate defect-tracking tool: findings go in `kipple-history/audits/uat-findings-<date>.md` (same format as
the code-audit reports), severity P0-P3 (blocker / high / medium / low, borrowed from the webapp-uat skill's
triage scheme since it's a reasonable, well-known scale), each with steps to reproduce, impact and status.

## Entry criteria

- Phase 5 code audit (#26), Access JWT/passwordless (#40) and the scheduled auto-night theme (#41) merged (all
  three are, as of 2026-09-27), and deployed to the test environment below.
- CI green on the commit under test; `CHANGELOG.md` `[Unreleased]` reflects everything in scope.
- A representative test environment: either the Host-A deployment on a pre-release build, or the local dev stack
  (`npm run seed` / `KIPPLE_ADDR`+`KIPPLE_DATA` per CLAUDE.md) seeded with a realistic OPML set (the existing
  138-feed NewsBlur export works, or a smaller fixture for faster runs).
- Reeder Classic and NetNewsWire available on the owner's devices, already configured against the test instance.

## Exit criteria

- Every test case below has an executed result (pass, fail, or explicitly owner-waived).
- No open P0 or P1 defects.
- P2/P3 defects are either fixed or listed with the owner's explicit decision to ship anyway — no silent
  "won't fix" list (standing project rule).
- The findings doc is committed to `kipple-history/audits/`.
- The owner gives explicit sign-off, recorded as a `kipple-history` MEETINGS.md entry (the go/no-go meeting).

## Execution split

Three tiers, to keep the owner's involvement to what only he can actually do (per the phase 5 "minimal
involvement" decision):

1. **Scripted** — a Kipple-specific Playwright + axe-core UAT script (new, in-repo; see below), run unattended.
2. **Agent-driven** — scenario walkthroughs Claude drives itself in the built-in browser pane against the test
   instance, judging results visually/behaviorally where a script can't easily assert (layout correctness,
   theme legibility, filter behavior).
3. **Owner-only** — a short, deliberately minimal list of things that need a real device and a real body: touch
   swipe gestures, PWA install flow, the iOS share sheet, and whether `document.hasFocus()` behaves as expected
   in the installed PWA (an already-known open question from the phase 4 audit). These are the only test cases
   that require the owner to do anything.

## Suite 1 — Scripted (Playwright + axe-core)

Not built yet. A new script, `web/uat/run.mjs` (or similar; not the third-party `webapp-uat` npm skill — built in-repo, MIT-licensed
dependencies only, so it's auditable and has no i18n/placeholder checks Kipple doesn't need). Navigates every
screen (feed list in each of the 5 layouts, article view, search, settings, stats, Wrapped) and asserts:

| ID | Check | Expected |
|---|---|---|
| S1 | Console errors | Zero uncaught console errors per screen |
| S2 | Network failures | No unexpected 4xx/5xx from `/api/*` during normal navigation |
| S3 | Accessibility | axe-core WCAG 2.2 AA scan clean (or only known/waived issues) on every screen, in at least 2 themes (a light and a dark scheme) |
| S4 | Responsive | No horizontal scroll or clipped content at iPhone width (390px) and at a small-tablet width (768px) |
| S5 | Data integrity | No literal `undefined`, `NaN`, or `[object Object]` rendered anywhere |
| S6 | Theme contrast | Reuses the existing CI contrast check across all 20 schemes, not just the 2 spot-checked above |

Once built, it runs as part of `scripts/ci-local.ps1 -UAT` (a new optional flag; `ci-local.ps1` has no such flag
yet) and before every deploy, added then as a step in `docs/RELEASING.md`.

## Suite 2 — Agent-driven scenario walkthroughs

Test case format (per the standard guide): ID, title, precondition, steps, expected result.

**Feed management**
- TC-F1: Add a feed by URL → appears in feed list within one scheduler tick (30s), items populate.
- TC-F2: Import the reference OPML → all feeds and folders present, matching count and structure.
- TC-F3: Export OPML → re-importing it is lossless (round-trip, per phase 1's original gate).
- TC-F4: Per-feed override (poll interval, retention, layout) → takes effect and survives a restart.
- TC-F5: Unsubscribe a feed with starred items → starred items remain reachable in the hidden archive.

**Reading UI**
- TC-R1: Each of the 5 layouts (Magazine/Editorial, Cards, Compact, Inbox, Headlines) renders correctly with
  real content, images load through the proxy.
- TC-R2: Switch density (Dense/Snug/Standard/Relaxed/Airy) → list and reading text both change, live preview
  matches the applied result.
- TC-R3: Every keyboard shortcut (j/k/s/o/r/m/c/z/Shift+A/`/`, and the rest of the `?` overlay,
  `web/src/lib/keys.ts`) does what the overlay and `docs/ui-decisions.md` specify,
  including the Unread-view dim-then-remove behavior and the 15s undo toast.
- TC-R4: Theme picker — default short list plus "More themes"; each of the 20 schemes is legible (spot-check
  Signal's danger color and the Carbon/Fountain distinction called out in the UI decisions).
- TC-R5: Follow system switches to the configured day and night picks when the OS switches between light and
  dark, live, without a reload.
- TC-R6: Filters — mute, mark-read, auto-star, highlight, saved search, auto-read-after-N-days each behave as
  specified; a Muted view exists and is correct.
- TC-R7: Mark-all-as-read only affects items present when the list loaded (Shift+A semantics).
- TC-R8: On a schedule (Settings > Appearance, and the reading menu's theme select; shipped in #41) switches to
  the night pick at "Night starts" (default 21:00) and back at "Day starts" (default 07:00) on the device's clock,
  whatever the OS setting; a window across midnight works, equal times keep the day theme, the first paint is
  already right, and picking a fixed theme ends the schedule. Per device: another browser keeps its own choice.

**Search**
- TC-S1: FTS search returns stemmed matches; a saved search re-runs identically later.

**Stats**
- TC-T1: A day of real reading produces correct numbers in the Stats screen (Week/Month/Year/All ranges,
  Items/Minutes toggle, folder rollup, never-opened list, streaks).
- TC-T2: Export (CSV/JSON/JSONL raw, JSON summary) produces valid, complete files; the "leave out titles/links"
  toggle actually omits them.
- TC-T3: Delete a range, then delete all (typed confirmation) — data is gone, screen reflects it, stats
  on/off toggle stops/resumes recording without a reload (the bug fixed in the phase 5 audit — re-verify).
- TC-T4: Wrapped renders a plausible yearly summary; the opt-in share sheet works; an error state shows "Try
  again" instead of hanging (also just fixed — re-verify).

**Reader API clients**
- TC-A1: Reeder Classic (FreshRSS type) connects, syncs reading-list/unread/starred; mark read/unread/star in
  Reeder and confirm it appears in the web app within 60s, and vice versa.
- TC-A2: NetNewsWire connects the same way; add a feed from NetNewsWire, confirm it appears after the next
  scheduler tick; `subscription/quickadd` re-list shows it immediately (ETag behavior).
- TC-A3: `mark-all-as-read` from each client behaves correctly (the `ts` unit question; `docs/design.md` §3
  — confirm the digit-count parsing picks the right cut).

**Settings and accounts**
- TC-C1: Cloudflare Access sign-in (shipped in #40; design §7.0): with `KIPPLE_ACCESS_TEAM_DOMAIN` and
  `KIPPLE_ACCESS_AUD` set, Settings shows the Access email and offers Remove web password (asking for the
  current password); afterwards sign-in with an empty password succeeds only through Access with a verified
  token, and a LAN request that bypasses Access is refused. Negative cases: with the variables unset a password is
  always required, and an account that still has a password always needs it. Setting a password again (Settings,
  or `kipple password`) restores normal sign-in.
- TC-C2: API password generate-and-copy button works; the Reader API accepts the generated password.
- TC-C3: Backup export → `kipple restore` on a copy of the volume restores identically (this is also Suite 4's
  backup/restore drill — one execution can satisfy both).

**PWA**
- TC-P1: Manifest, icons, `apple-touch-icon`, safe-area handling look correct in the browser pane's mobile
  preset (desktop proxy for what Suite 3 confirms on the real device).
- TC-P2: Service worker precaches hashed assets; a reload after a deploy picks up the new version
  (`update()` on `visibilitychange`).

## Suite 3 — Owner-only (real device required)

- TC-D1: Install the PWA on the iPhone from Safari; relaunch later, confirm still logged in.
- TC-D2: Swipe right (toggle read/unread) and swipe left (star / More menu) match iOS Mail conventions; full
  swipe commits with the 15s undo toast.
- TC-D3: Web Share sheet opens correctly from an article; clipboard fallback works where Share is unavailable.
- TC-D4: Confirm whether `document.hasFocus()` reports true while the installed PWA is foregrounded but the
  phone is locked/backgrounded — resolves the open reading-time-on-iOS question from the phase 4 audit.

## Suite 4 — Migration rehearsal and backup/restore drill

Standing checklist items (previously done ad hoc for past releases, now made explicit):

- **Migration rehearsal:** before every deploy that changes the schema, run the migration against a *copy* of
  the live Host-A database (not the live one) and confirm it applies cleanly, timed, with `PRAGMA integrity_check`
  passing after.
- **Restore drill:** actually execute `kipple restore` against a real snapshot at least once per release cycle
  (not just read the steps in `docs/deploy.md`) — this phase 5 cycle is when it gets its first real end-to-end
  run, satisfying TC-C3 above.

**Executed 2026-09-27.** Copied the live nightly snapshot (`kipple-snapshot.db`, taken 04:10 that day, schema 8,
138 feeds, 6594 items) off the running container with `docker cp` (never touching `kipple.db` itself), restored
it onto a brand-new throwaway volume with the currently-deployed image (`kipple:local`, v0.3.0-alpha.7):
`kipple restore` reported the backup passed its integrity checks with no previous database to keep. Starting a
throwaway container against that volume also exercised a real migration rehearsal for free — the snapshot was
one migration behind the live schema, so startup applied `0009_stats_summary_indexes.sql` automatically,
confirmed by the log line, and the container came up `(healthy)` on `/healthz` immediately after. The live
`kipple` container was never stopped, restarted or otherwise touched throughout (verified via `docker ps`
before and after). All throwaway artifacts (test container, test volume, copied snapshot file) were removed
afterward. Both TC-C3 and the standing migration-rehearsal checklist item are satisfied by this one drill.

## Suite 5 — Fresh-machine Docker walkthrough as literal UAT

The planned "first-time Docker setup walkthrough" release step doubles as UAT if followed literally rather than
paraphrased: on a machine with nothing Kipple-related installed, follow `README.md`/`docs/deploy.md` verbatim
from `git clone` to a working login, noting every point where a real newcomer would get stuck. Findings go in
the same `uat-findings` doc, not a separate one.

**Simulate a single-host self-hoster, not the owner's own setup.** The owner runs Kipple across two machines
(one running the app, one for admin/backups over SSH) because that's convenient for him, but most people
following this walkthrough will have one machine with Docker on it and no SSH step at all. Run this suite as
that person: everything (clone, build, `.env`, `docker compose up`, first login, a test backup export) on a
single box, no `ssh host-a` wrapper. If anything in `docs/deploy.md`'s two-host framing trips up a one-host
walkthrough, that's a real finding, not a suite mismatch.

## Defect severity (borrowed scale)

| Severity | Meaning | Exit criteria impact |
|---|---|---|
| P0 Blocker | Data loss, security bypass, can't complete a core flow | Must fix before sign-off |
| P1 High | Wrong behavior a normal session would hit | Must fix before sign-off |
| P2 Medium | Edge case or cosmetic-but-real bug | Fix or owner explicitly waives |
| P3 Low | Polish | Fix or owner explicitly waives |

## Reader API regression replay

Separate from the client suites above, but part of the same release-readiness gap: replay the recorded Reeder
Classic and NetNewsWire request sequences from the client research (kept outside this repository;
`stream/items/ids` paging, `edit-tag`, `subscription/quickadd`, `mark-all-as-read`) as a contract test against
the build under test, not just the unit-level contract tests already in CI (`internal/greader/contract_test.go`)
— this is the end-to-end version, run once per release against the actual deployed instance.
