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

In-repo script, `web/uat/run.mjs` (not the third-party `webapp-uat` npm skill: built in-repo so it's auditable and has
no i18n/placeholder checks Kipple doesn't need; its two dependencies are dev-only and permissively licensed:
`@playwright/test`, Apache-2.0, and `axe-core`, MPL-2.0, which was already a dev dependency). Navigates every
screen (feed list in each of the 5 layouts, article view, search, settings, stats, Wrapped) and asserts:

| ID | Check | Expected |
|---|---|---|
| S1 | Console errors | Zero uncaught console errors per screen |
| S2 | Network failures | No unexpected 4xx/5xx from `/api/*` during normal navigation |
| S3 | Accessibility | axe-core WCAG 2.2 AA scan clean (or only known/waived issues) on every screen, in at least 2 themes (a light and a dark scheme) |
| S4 | Responsive | No horizontal scroll or clipped content at iPhone width (390px) and at a small-tablet width (768px) |
| S5 | Data integrity | No literal `undefined`, `NaN`, or `[object Object]` rendered anywhere |
| S6 | Theme contrast | Reuses the existing CI contrast check across all 20 schemes, not just the 2 spot-checked above |

A manual, pre-release tool (step 2 of `docs/RELEASING.md`): not part of CI or `scripts/ci-local.ps1`.
`@playwright/test` is a dev dependency so its version is pinned in the lockfile and audited with the rest; it has no
install scripts and downloads no browser on `npm ci`, so the image build and CI only unpack its JavaScript (about
13 MB) and never ship it. To run it:

```
cd web
npx playwright install chromium     # once per machine: the browser Playwright drives
npm run build                       # the seed embeds web/dist, so build the UI first
npm run seed                        # terminal 1: Kipple on 127.0.0.1:7080 with six sample feeds (needs Go and network)
npm run uat                         # terminal 2, once the feeds have fetched (about a minute)
```

Options (`npm run uat -- --help`): `--url` (or `KIPPLE_UAT_URL`),
`--user`/`--password` (default: the seed's throwaway account), `--only <screen ids>`, `--screenshots`, `--headed`,
`--out`. Every screen is checked in Paper and Midnight (the browser's light and dark preference, which the default
follow-system theme picks up) at 1280 px, 768 px and 390 px (the last two as touch devices); S4 applies to the two
narrow widths. Before the run it checks its own probes against a page built to fail them, so a clean report means
clean, not broken. A theme that does not come out as Paper and Midnight (an account defaulting to a fixed theme), an
unknown `--only` id, or no article to open (feeds not fetched yet) stops that part of the run as an error rather than
passing it.

Every theme and width is a fresh browser sharing one session and one device of the run's own (its cookie is kept in
`web/uat/results/.device-<host>-<port>.json`, so later runs reuse it instead of filling the server's device table), so
existing devices' settings are never touched. The run still changes the instance: it switches that device's layout
and opens an article (marking it read and recording reading stats). So it refuses any address that is not loopback
unless `--allow-remote` is given: run it against a seeded or copied instance, never the one the owner reads on.

Output: a line per screen, then `web/uat/results/<timestamp>/report.md` (findings grouped by check and rule, with the
screens and elements each one was seen on), `report.json` (everything) and a screenshot of each failing screen. Exit
code 0 clean, 1 findings, 2 a screen or the run could not be checked. Known and accepted issues go in `web/uat/waivers.json`
(`{"check": "S3", "rule"?: "<axe rule id>", "match"?: "<text of the finding>", "screen"?, "theme"?, "viewport"?,
"reason": "..."}`; a reason is required, keys and values are checked, S1/S2/S4/S5 waivers need `match` since their
rules are coarse, and an unused waiver is reported). axe results on the article body (the feed's own HTML), failed
non-`/api/` requests (feed images) and S5 hits in feed text (a title that says "undefined behaviour"; a field that
rendered as nothing but `undefined` still fails; elsewhere, a hit that is only there because of a feed, folder or
saved-search name) are listed as notes, not failures. S5 also looks for `Invalid Date`
and in form field values. A screen still loading after 15 s is an error, not a pass.

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

**Executed 2026-09-27.** Driven by Claude in the desktop app's built-in browser pane against a throwaway local
instance (`npm run seed` on 127.0.0.1:7092, its own temp data dir, build of `main` at `ffb48cb`), plus a local
test feed served from the same temp dir so arrivals and filter matches could be controlled. The pane stayed
hidden behind other windows for the whole run, which has side effects worth knowing before re-running this: the
page reports `document.visibilityState` "hidden" and never has focus, CSS transitions, `ResizeObserver` and
media-query change events only advance when a frame is actually drawn (a screenshot forces one), react-query
pauses retries, and active reading time is never recorded. Where that mattered it is said below; none of it is a
Kipple defect. Defects found: three fixed in PR #45, two copy questions filed as issues #43 and #44 for the owner.

- TC-F1 **pass.** Adding `https://blog.rust-lang.org/` (a site, not a feed) discovered the feed and showed 10
  items within about 3 s.
- TC-F2 **pass, with a substitute fixture.** The 138-feed NewsBlur export was not available to the agent; a
  6-feed OPML with two folders, a top-level feed and one duplicate was used instead. Result: 5 added, 2 folders
  created, 1 already present and left in its original folder, top-level feed placed in Uncategorized. The
  summary's "1 feed was already in Kipple and left as they are" mixed singular and plural (fixed, PR #45).
- TC-F3 **pass.** Re-importing the export into the same instance added nothing; importing it into a second,
  empty instance and exporting again gave a byte-identical file, including `kipple:interval` and
  `kipple:retention` overrides.
- TC-F4 **pass.** Poll interval (2 h), retention (newest 50) and layout (Cards, a per-device override) set on one
  feed all survived a server restart, and the feed opened in Cards.
- TC-F5 **pass.** Deleting a feed with 2 starred items (the dialog showed the count, keep-starred default)
  moved them to "Unsubscribed (starred)"; both stayed in Starred with their original feed name, and Stats lists
  the feed as "(unsubscribed)".
- TC-R1 **pass.** All five layouts render real content with images through `/img/`, no horizontal overflow and
  no `undefined`/`NaN` text. (Overlapping Editorial rows in one screenshot were the hidden-pane
  `ResizeObserver` stall, gone once a frame was drawn.)
- TC-R2 **pass.** Each density step changes both the list variables and the article line height and measure
  (1.4 to 1.8); the Settings live preview matches the applied values exactly and the choice is saved to the
  device profile.
- TC-R3 **pass after a fix.** j/k, m (with the dim-then-leave behavior when moving off, and undo), s, o, c, r,
  z, `?` and the 15 s undo toast behave as specified. `/` from another screen opened Search with the caret on
  the heading instead of the search box (the shell's heading focus ran after Search focused its box); fixed in
  PR #45 with regression tests. Other in-app arrivals at Search keep heading focus and a page load still puts the
  caret in the box.
- TC-R4 **pass.** Short list plus More themes and Accessibility themes groups; all 20 schemes apply, body text
  contrast 7.8:1 or better on every one; Signal's danger is magenta (`#a0006a`), Carbon is neutral gray-black and
  Fountain clearly navy with cream text.
- TC-R5 **pass.** Follow system switches Paper/Midnight with the color scheme; On a schedule with custom picks
  (Linen by day, Graphite by night) switched to Graphite at the night boundary and back to Linen at the day
  boundary without a reload.
- TC-R6 **pass, auto-read partly.** Mute (created in the UI), mark read, auto-star and highlight each acted on a
  new matching item as it arrived; Muted lists the muted item with its rule and Restore brings it back unread; a
  saved search is in the sidebar with its count. Auto-read after N days is measured from crawl time by design,
  so on a fresh instance the preview correctly finds nothing (the catch-up path itself is covered by store
  tests). The highlight rule always said "Hasn't matched anything yet" because highlights are never counted;
  fixed in PR #45.
- TC-R7 **pass.** With the list loaded, an item that arrived afterwards stayed unread after Shift+A while the
  three loaded ones were marked read. The empty state then says new ones appear after the next refresh while
  the "1 new article" pill is showing (issue #44).
- TC-S1 **pass.** `runner` finds "runners", `run` finds "running"; the saved search re-runs to the same result
  list later.
- TC-T1 **pass (short session).** Opens and reads from the session show up with matching numbers in all four
  ranges, the Items/Minutes toggle and folder rollup, never-opened feeds and streaks. A full day of the owner's
  real reading is still worth a look in Suite 3.
- TC-T2 **pass.** CSV, JSON and JSON Lines raw exports and the JSON summary all parse and carry the same event
  count; with titles and links off, `item_title`/`item_url` are null and no URL or title appears anywhere; the
  data dictionary downloads as Markdown.
- TC-T3 **pass.** Delete a range showed "No events in that range" for a range without data and the right count
  for today, then a second confirmation; Delete all only enables on the exact `DELETE ALL`; Stats shows the empty
  state afterwards. Turning statistics off stopped recording and hid Stats, and on again resumed recording, both
  without a reload.
- TC-T4 **pass.** Your year gives a plausible summary, the share sheet copies the text (top sources only when
  switched on) and saves a PNG; a failing summary request shows "Try again", which recovers. When a year has
  opens but no reads, the "Only 1 day of reading" notice contradicts "No days with reading" (issue #43).
- TC-A1, TC-A2, TC-A3 **skipped.** They need Reeder Classic and NetNewsWire on the owner's devices; not
  executable by an agent.
- TC-C1 **pass (negative case only).** Without Access configured the login form requires the password: an empty
  password is refused, including with a forged `Cf-Access-Jwt-Assertion` header. The positive case needs a real
  Cloudflare Access setup and was not testable here.
- TC-C2 **pass.** Generate API password asks for the web password, shows the new one once and copies it; that
  password signs in on `ClientLogin` and lists subscriptions, and the web password does not.
- TC-C3 **pass.** The restore half is Suite 4's drill below; the web backup export here produced a valid zip
  with the expected contents summary and warning.
- TC-P1 **pass.** Manifest (standalone, scope and start `/`), 192/512/maskable icons and a 180 px
  `apple-touch-icon` all load at their declared sizes; `viewport-fit=cover` with safe-area insets in the CSS; at
  the mobile preset (375 px) no screen scrolls sideways and the bottom tab bar is in place.
- TC-P2 **blocked in the pane, checked statically.** The built-in browser refuses every service worker
  registration, even a one-line worker on another local origin, so the live update could not be exercised. The
  built `sw.js` precaches the page's hashed `index-*.js`/`.css` and the lazy chunks, and `update()` runs on
  `visibilitychange`. The live check belongs on a real browser in Suite 3.

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
