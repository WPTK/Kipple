# UAT plan

Adapted from standard UAT methodology (entry/exit criteria, traceable test cases, severity-triaged defects,
formal sign-off) to a single-owner, single-user project with no separate QA team. Functional testing (`go test`,
Vitest, `/code-review`) already answers "does it work?" This plan answers "does it work for the owner, on his
actual devices, doing his actual reading?" It is a phase 5 release-readiness step, feeding the final sign-off
(`docs/maintainers/release-checklist.md`).

## Roles (mapped from the standard 5-role model)

| Standard role | Here |
|---|---|
| QA professional (orchestrates) | Claude: writes test cases, executes what can be automated or agent-driven, tracks and triages defects |
| End user | The owner (the only user), on desktop Chrome and an installed iPhone PWA, plus a Reader API client |
| Business analyst / product owner | The owner (same person) — decisions already recorded in `docs/maintainers/ui-decisions.md` and `kipple-history` are the "requirements" test cases trace to |
| Development team | Claude, via fix PRs against defects found |
| Sign-off authority | The owner, against `docs/maintainers/release-checklist.md` |

No separate defect-tracking tool: findings go in `kipple-history/audits/uat-findings-<date>.md` (same format as
the code-audit reports), severity P0-P3 (blocker / high / medium / low, borrowed from the webapp-uat skill's
triage scheme since it's a reasonable, well-known scale), each with steps to reproduce, impact and status.

## Entry criteria

- Phase 5 code audit (#26), Access JWT/passwordless (#40) and the scheduled auto-night theme (#41) merged (all
  three are, as of 2026-09-27), and deployed to the test environment below.
- CI green on the commit under test; the pending `changes/` fragments (`node scripts/changelog.mjs preview`) reflect everything in scope.
- A representative test environment: either the Kipple server deployment on a pre-release build, or the local dev stack
  (`npm run seed` / `KIPPLE_ADDR`+`KIPPLE_DATA` per CLAUDE.md) seeded with a realistic OPML set (the existing
  138-feed OPML export works, or a smaller fixture for faster runs).
- A Reader API client available on the owner's devices, already configured against the test instance.

## Exit criteria

- Every test case below has an executed result (pass, fail, or explicitly owner-waived).
- No open P0 or P1 defects.
- P2/P3 defects are either fixed or listed with the owner's explicit decision to ship anyway — no silent
  "won't fix" list (standing project rule).
- The findings doc is committed to `kipple-history/audits/`.
- The owner gives explicit sign-off, recorded in the history repository.

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
no i18n/placeholder checks Kipple doesn't need). This plan first said "MIT-licensed dependencies only"; neither tool
is MIT, so that wording is replaced here rather than quietly dropped: `@playwright/test` is Apache-2.0 (permissive)
and `axe-core` MPL-2.0 (weak, file-level copyleft: obligations attach only to modified axe-core files, and Kipple
neither modifies nor ships it). Both are dev-only and never in the image; `axe-core` was already a dev dependency
for the Vitest accessibility tests. Navigates every
screen (feed list in each of the 6 layouts, article view, search, settings (the group list and each of its six groups), stats, Wrapped) and asserts:

| ID | Check | Expected |
|---|---|---|
| S1 | Console errors | Zero uncaught console errors per screen |
| S2 | Network failures | No unexpected 4xx/5xx from `/api/*` during normal navigation |
| S3 | Accessibility | axe-core WCAG 2.2 AA scan clean (or only known/waived issues) on every screen, in at least 2 themes (a light and a dark scheme) |
| S4 | Responsive | No horizontal scroll or clipped content at iPhone width (390px), at the narrowest supported width (320px) and at a small-tablet width (768px) |
| S5 | Data integrity | No literal `undefined`, `NaN`, or `[object Object]` rendered anywhere |
| S6 | Theme contrast | Reuses the existing CI contrast check across all 20 schemes, not just the 2 spot-checked above |
| S7 | Font choice reachable | The Aa menu above every list screen (each layout, Unread, Starred, Search) and the article, and Settings > Appearance, each have one visible "Reading font" select with every font (at least 12: Default and the 11 bundled). Not waivable |

S8 (touch targets) is checked at the three touch widths: every control of Kipple's (not the feed's article HTML) has a
hit area of at least 44 x 44 px. The probe (`tapProbe` in `web/uat/probes.mjs`) hit-tests around each control with
`elementFromPoint`, so a larger label around a checkbox or a padded pseudo-element (the `.hit-pad` class) counts, and a bare
icon does not. A finding is waived only in `web/uat/waivers.json`, with the reason written there.

A manual, pre-release tool (step 2 of `docs/maintainers/RELEASING.md`): not part of CI or `scripts/ci-local.ps1`.
`@playwright/test` is a dev dependency so its version is pinned in the lockfile and audited with the rest; it has no
install scripts and downloads no browser on `npm ci`, so the image build and CI only unpack its JavaScript (about
13 MB) and never ship it. To run it:

```
cd web
npx playwright install chromium     # once per machine: the browser Playwright drives
npm run build                       # the seed embeds web/dist, so build the UI first
npm run seed                        # terminal 1: Kipple on 127.0.0.1:1919 with six sample feeds (needs Go and network)
npm run uat                         # terminal 2, once the feeds have fetched (about a minute)
```

Options (`npm run uat -- --help`): `--url` (or `KIPPLE_UAT_URL`), `--browser chromium|firefox|webkit` (default chromium) or `--browsers all` (or a list) for
one run per engine in turn, with a report directory each and the worst exit code; the default run stays Chromium only (see
the cross-engine run below),
`--user`/`--password` (default: the seed's throwaway account), `--only <screen ids>`, `--screenshots`, `--headed`,
`--out`. Every screen is checked in Paper and Midnight (the browser's light and dark preference, which the default
follow-system theme picks up) at 1280 px, 768 px, 390 px and 320 px (the last three as touch devices); S4 and S8 apply to the
three touch widths. Before the run it checks its own probes against a page built to fail them, so a clean report means
clean, not broken. A theme that does not come out as Paper and Midnight (an account defaulting to a fixed theme), an
unknown `--only` id, or no article to open (feeds not fetched yet) stops that part of the run as an error rather than
passing it.

Every theme and width is a fresh browser sharing one session and one device of the run's own (its cookie is kept in
`web/uat/results/.device-<host>-<port>.json`, so later runs reuse it instead of filling the server's device table), so
existing devices' settings are never touched. The run still changes the instance: it switches that device's layout
and opens an article (marking it read and recording reading stats). So it refuses any address that is not loopback
or credentials other than the seed's (a port forward can put the real instance on loopback) unless `--allow-remote`
is given: run it against a seeded or copied instance, never the one the owner reads on. The remembered device only
matters with `npm run seed -- --keep` or a copied database; a fresh seed starts without it.

Output: a line per screen, then `web/uat/results/<timestamp>/report.md` (findings grouped by check and rule, with the
screens and elements each one was seen on), `report.json` (everything) and a screenshot of each failing screen. Exit
code 0 clean, 1 findings, 2 a screen or the run could not be checked. Known and accepted issues go in `web/uat/waivers.json`
(`{"check": "S3", "rule"?: "<axe rule id>", "match"?: "<text of the finding>", "screen"?, "theme"?, "viewport"?,
"reason": "..."}`; a reason is required, keys and values are checked, S1/S2/S4/S5/S6 waivers need `match` since
their rules are coarse (an S3 finding is already one element; `screen` may also be `boot`, a browser's first load),
and an unused waiver is reported). axe results on the article body (the feed's own HTML), failed
non-`/api/` requests (feed images) and S5 hits that are only there because of feed-supplied text (a title that says "undefined
behaviour": the run collects feed, folder and saved-search names and item titles, excerpts, authors, sources and
search snippets from the API and takes them out before judging; Kipple text next to them, such as a time or a "min
read" line, still fails) are listed as notes, not failures, as is a scroller pushed wide only by the article HTML.
That separation is done on content rather than by serving fixed fixture feeds because the run is also meant for a
copy of the real database, whose feeds are whatever the owner reads.

Screens after the first in each browser are reached the way a reader moves: the app's own link when one is on
screen, otherwise a router history entry; each screen must show its expected heading, which proves the right screen
was checked. The list header's two menus are checked open, as screens of their own: `list-options` (its Layout and
Order choices must be there) and `list-length` (the article-length filter). These menus are modal: while one is open the
page behind it is `aria-hidden` and cannot be reached, so on these two screens S3 runs axe on the open menu only
(`[role="menu"]`, axe's own advice for a modal that hides the page). A full-page run would flag the page behind the
menu (`aria-hidden-focus`), which nobody can reach then. What makes the scoping honest is checked on the same screens:
with the menu open, focus is inside it and Tab keeps it there, and Escape closes it and puts focus back on its button;
a failure there is an error for the screen. Every other screen, the list screens with their menus closed included,
keeps the full-page axe run. The in-page probes live in `web/uat/probes.mjs` (linted with browser globals only), the runner in
`web/uat/run.mjs`. S5 also looks for `Invalid Date`
and in form field values. A screen still loading after 15 s is an error, not a pass.

Other engines (opt-in): `npx playwright install firefox webkit` once, then `npm run uat -- --browsers all` runs Chromium, Firefox and
WebKit one after the other, reports in `<out>/<engine>/`, `report.md` naming the engine and its version. A waiver may
carry `"browser": "<engine>"` to apply to one engine only. Two things differ by engine and are handled in the runner,
not waived: Firefox has no mobile emulation (the tablet and phone runs are touch devices at the right width, not
`isMobile`), and it drops an emulated color scheme when the page is sent with Cross-Origin-Opener-Policy (Kipple's is
`same-origin`), so the Firefox runs set the operating-system preference on the browser itself. The results, the
engine differences and what stays manual are in `docs/compatibility.md`. Playwright's WebKit is the WebKit engine,
not Safari: the iPhone check stays Suite 3.

Keyboard and gestures (`npm run uat:keyboard`, `web/uat/keyboard.mjs`, `--browser` as above, against the same seeded
instance): at 1280x800 with the keyboard only, K1 walks Tab through every main screen (every visible control is
reached, the focused one shows an indicator, no trap forward or back), K2 opens menus and dialogs from the keyboard and
checks that focus stays inside a modal dialog, Escape closes it and focus returns to what opened it, and K3 presses
that letter and symbol keys do nothing (there are no single-key shortcuts), arrow keys move focus between rows and the search box takes typed letters. At 390x844 as a touch
device K4 checks that each swipe or long-press action has a button or menu: Star, the row's More actions menu (mark
read or unread), Back to list, Refresh all feeds, and Show move buttons for reordering. Exit code 0 clean, 1 findings, 2
setup error. WebKit leaves links out of the Tab order unless the reader turns on Safari's "Press Tab to highlight each
item" (or uses Option+Tab), so its K1 expects buttons and fields only.

Offline reading (`npm run uat:offline`, `web/uat/offline.mjs`, against the same seeded instance): Suite 1 blocks the
service worker, so offline is checked on its own, at 1280x800 and 375x812. A fresh browser signs in, lets the worker
install and keep the first page of Unread, then goes offline with the app open (the page sees the `offline` event) and
checks: a list and a screen the worker never kept (Starred, Stats) end in their error screen, not a skeleton (O1);
opening and starring an article are queued and counted in the notice (O2); a reload offline shows the Unread list
from the worker's copy with the offline notice (O3) and opens the article from it (O4); O1 again after the reload
(O5); back online the queue is sent and Stats loads by itself (O6). It refuses non-loopback addresses and other
credentials like the main run. Exit code 0 clean, 1 findings, 2 setup error.

Feeds screen (`npm run uat:feeds`, `web/uat/feeds.mjs`, same instance, credentials, `--browser` and exit codes), at 1280x800 and
375x812, on disposable feeds that point at an address that never resolves (a stopped run's leftovers are removed first):
tapping a feed's title in select mode ticks it and never opens it (F1); deleting selected feeds shows one progress bar
with a fixed total, keeps every folder, including one the delete left with no feeds, and touches
nothing else (F2); New folder and Folder settings save on Enter (F3); Import feeds from a file (OPML) refuses a file that is not OPML, by its
look, as an RSS feed and as JSON named `.opml`, in plain words and adds nothing (F4).

Saved search and out-of-date addresses (`npm run uat:states`, `web/uat/states.mjs`, same rules): saving the same search
twice is refused in the dialog and leaves one (Q1); the list address of a deleted or unknown feed, and of a deleted folder,
says it no longer exists and offers Go to Unread (U1, U2); an article address that is gone says so (U3); a path nothing
answers says "Page not found" (U4).

In CI, the `Browser UAT` workflow (`.github/workflows/browser-uat.yml`) seeds a throwaway instance on loopback and runs
`npm run uat:all` (`web/uat/all.mjs`): every flow listed in `web/uat/flows.json` under Chromium, and the Feeds, saved-search,
downloads, images, wrapping and rotate flows under WebKit (WebKit sends fetch metadata that Chromium does not, so a download error saved as the file shows only there).
A new flow file goes in `flows.json`, as a flow or as excluded with a reason; `node uat/all.mjs --check` fails a file that is neither.
A flow that fails on one engine for a reason with an open issue goes in `knownFail` (`{"<flow>.mjs": {"<engine>": "#<issue>"}}`, empty by default). The flow still runs: a run with findings (exit 1) prints as `known (issue #N)` and does not fail the run, while a crash or exit 2 still fails it, a pass fails it until the entry is removed, and a value that is not an issue number fails `--check`.
The workflow needs no outside network: `web/scripts/fixture-feeds.mjs` serves five feeds on loopback and `KIPPLE_SEED_FEEDS_URL`
makes `npm run seed` add those instead of the public ones. It is not a required check yet, and `scripts/maintainers/pr-ready.ps1`
prints its result without blocking on it. `web/uat/wait-ready.mjs` holds the start until the seed's feeds have articles.
`npm run seed` builds `web/dist` first when it is missing or older than its sources; `KIPPLE_SEED_NO_BUILD=1` skips that.

Nested folders (`npm run uat:folders`, `web/uat/folders.mjs`, against the same seeded instance), at 1280x800 and
375x812, all through the Feeds screen: New folder, then New subfolder twice, makes three levels (N1); Select and Move to
folder, picked by its path, puts a feed in the deepest one (N2); each of the three folders counts the feed's unread in
the bootstrap and on screen, the sidebar (desktop) is a tree with three levels, the phone has no sideways scroll, the
folder list's title names its path, and axe-core finds nothing on Feeds or the folder list (N3); Move to… takes the
deepest folder to the top level and back (N4); deleting the top folder says its subfolders go and its feed moves, and
afterwards the subtree is gone and the feed is in the default folder (N5). The feed is put back where it was, and
folders a stopped run left behind are removed first. Same address and credential rules and exit codes as above.

Downloads (`npm run uat:downloads`, `web/uat/downloads.mjs`, same instance, credentials and exit codes), at 1280x800 and
375x812: Feeds, Feed actions, Export feeds to a file (OPML) saves an OPML document, not a JSON error (D1); Settings, Stats, Export…,
Download saves a CSV with a header row (D2); Settings, Account & Devices, Export backup, the web password, then the
download link saves a zip (D3).

Images (`npm run uat:images`, `web/uat/images.mjs`, same instance, credentials and exit codes), at 1280x800 and 375x812,
against a feed the flow serves itself on a loopback port: an SVG named as one and an SVG that is not each show "Image
not shown" with their alt text, no image is left in their place and the cue keeps the box the image declared (I1); the
cue's Open original link goes to the article in a new tab (I2); a GIF in the same article is shown, a 1x1 tracking
pixel gets no cue, and an image whose source answers 500 stays an image (I3); back to the list and the same article
again, from the app's cache, shows the cues again (I4); a second article of 30 lazy images below the fold sends no
image request and no check until it is scrolled, then they load (I5).

Row link names (`npm run uat:row-name`, `web/uat/row-name.mjs`, same instance, credentials and exit codes), at 375, 768 and
1280 px wide, in every list layout, against a feed the flow serves itself: two articles with titles far longer than a
phone's row and the search word past the point a one-line layout cuts them, one unread and one read. In the search
results axe's label-content-name-mismatch finds nothing (N1); the browser's own accessible name for the unread row's link
is the title, then ", unread", for the read one the title alone, and the link's text is the title alone (N2); the page
does not scroll sideways (N3).

**Label guard (no browser).** The screens above find controls by accessible name, so renaming an `aria-label` in
`web/src` breaks every screen that looks for the old one without failing any other check. `node scripts/uat-labels.mjs
[<git range>]` (default `origin/main...HEAD`) reads the diff of `web/src`, takes the static text of each removed or
changed `aria-label`, and fails when that text no longer appears in any label in `web/src` but still appears in
`web/uat/*.mjs`, naming the file and line. A UAT line that matches only by coincidence can carry the comment
`uat-labels: ignore`. It runs in `scripts/ci-local.ps1` and in its own CI job on pull requests, takes a second, and
does not replace running Suite 1.

## Suite 2 — Agent-driven scenario walkthroughs

Test case format (per the standard guide): ID, title, precondition, steps, expected result.

**Feed management**
- TC-F1: Add a feed by URL → appears in feed list within one scheduler tick (30s), items populate.
- TC-F2: Import the reference OPML → all feeds and folders present, matching count and structure.
- TC-F3: Export OPML → re-importing it is lossless (round-trip, per phase 1's original gate).
- TC-F4: Per-feed override (poll interval, retention, layout) → takes effect and survives a restart.
- TC-F5: Unsubscribe a feed with starred items → starred items remain reachable in the hidden archive.

**Reading UI**
- TC-R1: Each of the 6 layouts (Editorial, Cards, Compact, Inbox, Email - Compact, Gazette) renders correctly with
  real content, images load through the proxy.
- TC-R2: Switch spacing (Dense/Snug/Standard/Relaxed/Airy) → list and reading text both change, live preview
  matches the applied result.
- TC-R3: Letter and symbol keys do nothing (there are no single-key shortcuts). Tab, the arrow keys,
  Enter and Escape behave as `docs/maintainers/ui-decisions.md` specifies. One case that file does not settle is
  under "Open questions for the owner" below: Escape after opening an article on a wide screen.
- TC-R4: Theme picker — default short list plus "More themes"; each of the 20 schemes is legible (spot-check
  Signal's danger color and the Carbon/Fountain distinction called out in the UI decisions).
- TC-R5: Follow device switches to the configured day and night picks when the OS switches between light and
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
- TC-T1: A day of real reading produces correct numbers in the Stats screen (Week/Month/Year/All/Monthly ranges, tap a tile to compare it with the previous period,
  Items/Minutes toggle, folder rollup, never-opened list, streaks).
- TC-T2: Export (CSV/JSON/JSONL raw, JSON summary) produces valid, complete files; the "leave out titles/links"
  toggle actually omits them.
- TC-T3: Delete a range, then delete all (typed confirmation) — data is gone, screen reflects it, stats
  on/off toggle stops/resumes recording without a reload (the bug fixed in the phase 5 audit — re-verify).
- TC-T4: Wrapped renders a plausible yearly summary; the opt-in share sheet works; an error state shows "Try
  again" instead of hanging (also just fixed — re-verify).

**Reader API clients**
- TC-A1: a Reader API client (Google Reader compatible account) connects, syncs reading-list/unread/starred; mark read/unread/star in
  the client and confirm it appears in the web app within 60s, and vice versa.
- TC-A2: a second Reader API client connects the same way; add a feed from it, confirm it appears after the next
  scheduler tick; `subscription/quickadd` re-list shows it immediately (ETag behavior).
- TC-A3: `mark-all-as-read` from each client behaves correctly (the `ts` unit question; `docs/design.md` §3
  — confirm the digit-count parsing picks the right cut).

**Settings and accounts**
- TC-C1: Cloudflare Access sign-in (shipped in #40; design §7.0): with Cloudflare Access set in Settings
  (Advanced, Address and access), Settings shows the Access email and offers Remove web password (asking for the
  current password); afterwards sign-in with an empty password succeeds only through Access with a verified
  token, and a LAN request that bypasses Access is refused. Negative cases: with Access off a password is
  always required, Settings refuses to change or turn off Access while the account has no password (`access_in_use`), and an account that still has a password always needs it. Setting a password again (Settings,
  or `kipple password`) restores normal sign-in.
- TC-C2: API password generate-and-copy button works; the Reader API accepts the generated password.
- TC-C3: Backup export → `kipple restore` on a copy of the volume restores identically (this is also Suite 4's
  backup/restore drill — one execution can satisfy both).

**PWA**
- TC-P1: Manifest, icons, `apple-touch-icon`, safe-area handling look correct in the browser pane's mobile
  preset (desktop proxy for what Suite 3 confirms on the real device).
- TC-P2: Service worker precaches hashed assets; a reload after a deploy picks up the new version
  (`update()` on `visibilitychange`).

Results of the past runs of this suite are kept in the history repo.

## Suite 3 — Owner-only (real device required)

**Not a promotion gate (decided 2026-09-27, see `docs/maintainers/RELEASING.md`).** These stay open-ended: the owner checks
them informally on his own devices as he uses each build, rather than closing them off as a one-time checklist
before cutting a beta or rc. A real finding here becomes its own tracked fix on its own timeline; it doesn't
hold up an otherwise-ready release.

- TC-D1: Install the PWA on the iPhone from Safari; relaunch later, confirm still logged in.
- TC-D2: Swipe right (toggle read/unread) and swipe left (star / More menu) match iOS Mail conventions; full
  swipe commits with the 15s undo toast.
- TC-D3: Web Share sheet opens correctly from an article; clipboard fallback works where Share is unavailable.
- TC-D4: Confirm whether `document.hasFocus()` reports true while the installed PWA is foregrounded but the
  phone is locked/backgrounded — resolves the open reading-time-on-iOS question from the phase 4 audit.

## Suite 4 — Migration rehearsal and backup/restore drill

Standing checklist items (previously done ad hoc for past releases, now made explicit):

- **Migration rehearsal:** before every deploy that changes the schema, run the migration against a *copy* of
  the live the Kipple server database (not the live one) and confirm it applies cleanly, timed, with `PRAGMA integrity_check`
  passing after.
- **Restore drill:** actually execute `kipple restore` against a real snapshot at least once per release cycle
  (not just read the steps in `docs/deploy.md`) — this phase 5 cycle is when it gets its first real end-to-end
  run, satisfying TC-C3 above.

Results of the past drills are kept in the history repo.

## Suite 5 — Fresh-machine Docker walkthrough as literal UAT

The "first-time Docker setup walkthrough" release step doubles as UAT when it is followed literally rather than
paraphrased: on a machine with nothing Kipple-related installed, do exactly what `README.md` says, from nothing to a
claimed, working instance, and note every place a real newcomer would get stuck. That walkthrough is the
**setup wizard**, not a `.env` edit: there is no `.env` and no password in any file. Findings go in the same
`uat-findings` doc as the other suites.

**Where and with what.**

- **A Linux host with Docker Engine. Not the Windows dev box (the dev machine).** Running containers there can take Docker Desktop
  down and with it services other people use; this suite must never run on it. Use a separate Linux machine or VM (a
  spare host, or a fresh cloud VM deleted afterwards). Simulate a **single-host self-hoster**, not the owner's own setup:
  everything (Docker, the terminal, the browser or a browser on another machine that reaches it) with no `ssh <kipple-server>`
  wrapper anywhere. If `docs/deploy.md`'s two-host framing trips up a one-host reader, that is a real finding, not a
  suite mismatch.
- **The image under test** is the pushed prerelease image `ghcr.io/wptk/kipple:<version>`, pulled
  anonymously with no registry login, so the run also proves the package is public. Before the first image exists, and for
  run E, use a source build of the tag under test. Say which one each run used.
- **Isolation.** Use a container name, compose project name (`-p`), volume and port that cannot collide with anything else
  on the host (the examples publish `127.0.0.1:1919`; pick another left-hand port if 1919 is taken). Use throwaway
  passwords. Tear everything down afterwards (`docker compose -p <p> rm -s -f kipple`, `docker volume rm <p>_kipple_data`, `docker network rm <p>_default`, then remove the image; never a bare `down`). The owner's live instance
  never takes part.

**Run A: pull-and-run on amd64, following the README literally.** Copy `docker-compose.pull.example.yml` (or the README's one-line
`docker run`) exactly as printed, with only the isolation changes above.

| # | Do | Expected | Log |
|---|---|---|---|
| A1 | Start it as the README and deploy guide say (`docker compose up -d kipple` on the compose file, or the one-liner). | The image pulls anonymously; the container reaches `(healthy)` within about a minute even though nothing is set up. | Pull time, `docker ps` health, image digest (`docker image inspect --format '{{index .RepoDigests 0}}' "$(docker inspect -f '{{.Image}}' <container>)"`). |
| A2 | `docker logs <container>`. | One `no account yet: open Kipple in a browser to create it` line (the only one), and no setup code, banner or link anywhere. | Whether it was clear what to do next. |
| A3 | Open `http://127.0.0.1:<port>` in a browser. | The wizard's first screen is **Create your account** (Step 1 of 2). No setup code is asked for. | Anything confusing in the wording. |
| A4 | Before creating the account, from a second browser or `curl`: `GET /api/bootstrap`, then `POST /api/auth/login` with any body (same-origin headers). | `401` and `409 setup_required`. `docker logs` shows no feed fetch: nothing runs until an account exists. | |
| A5 | Restart the container and open the address again. | The same account form: there is nothing to look up. Again exactly one `no account yet` line in `docker logs`. | |
| A6 | Send two account requests at the same moment (two browsers, or two `curl` calls with `Sec-Fetch-Site: same-origin` and `X-Kipple-Client: web`). | Exactly one `201` with a session cookie; the other `409 already_set_up` and no cookie. | |
| A7 | Step 1: create the account with a password (try one that is too short first). | The short one is refused with a reason; a valid one signs you in and moves to the feeds step. | |
| A8 | Step 2, feeds. | Nothing is ticked and the one button says Continue; ticking feeds changes it to Add N feeds. There is no Skip or Back, and no time zone question: Settings > About shows the browser's zone afterwards (not UTC unless the browser is on UTC). `docker exec <container> /kipple version -v` and Settings > About agree with the tag. | The zone shown. |
| A9 | In step 2, use **Import a feeds file** with a small OPML file, then tick a few recommended feeds and press Add N feeds. | The import dialog opens over the step and closes back to it; imported and subscribed feeds start fetching and you land in the reader. | Feeds added, time to the first article. |
| A11 | Sign out and in again, Settings, Account & Devices, **Export backup**, enter the web password and save the zip, then `docker exec <container> /kipple healthcheck; echo $?`. | Lands on the feed list; the password works; the backup downloads; the health check exits 0. | Backup size, and the version and schema in its manifest. |
| A12 | On a second throwaway volume, reload the page between steps 1 and 2 (or use Settings, Account & Devices, Run setup again). | The wizard resumes where it was; what was saved is kept. | |
| A13 | After completion `POST /api/setup/account` again (a browser tab or `curl`). | `404`: the setup route is gone. | |
| A14 | Stop and start the container. | It comes back in normal mode with its data, and no setup screen. | |

**Run B: open mode.** On a third throwaway volume: A1 to A3, then in step 1 choose **No password at all**.

| # | Do | Expected |
|---|---|---|
| B1 | Read the notice; try to continue without ticking the acknowledgement. | The notice says anyone who can reach the address can read and change everything, and to use it only when Kipple is reachable from this computer, your local network or Tailscale and the Docker bind rule (publish only on a local or Tailscale address, never a public one); it cannot be skipped without ticking the box. |
| B2 | Note that there is no extra checkbox (this run is in Docker). | Only the acknowledgement is asked: nothing about a local-network switch, because open mode has no such setting. Tick it and continue; the account is created. |
| B3 | Finish the wizard, close the browser, reopen the address. | It opens straight into the app with no sign-in screen (a session is minted silently). Settings has no "Sign out". |
| B4 | Send a request with an unexpected `Host`, for example `curl -H 'Host: evil.example' http://127.0.0.1:<port>/`; put a reverse proxy (or any request carrying `X-Forwarded-For`) in front and open the app through it. | The unexpected `Host` gets `421 Misdirected Request` naming Allowed host names in Settings. The proxied request is refused as `forwarded`. With the default `127.0.0.1:` mapping another machine cannot reach the port at all. |
| B5 | Settings, Account & Devices, Set web password. | Gives the account a password and signs every other session out; a reload shows the sign-in screen. |

**Run C: verify.** `cosign verify ghcr.io/wptk/kipple:<version> ...` exactly as the deploy guide prints it succeeds and names the
release workflow of this repository; the digest equals the one in the GitHub Release's notes and in A1.

**Run D: arm64.** Repeat Run A (A1 to A11, briefly) on an arm64 machine or VM with the same published tag. Expected: the
same tag pulls a `linux/arm64` variant and `kipple version -v` reports `linux/arm64`.

**Run E: build from source (the shorter "For developers" check).** On a Linux host with Git and Docker only, follow the
deploy guide's "Build from source" (`docs/deploy.md`): clone, `cp docker-compose.example.yml docker-compose.yml`, `docker compose build kipple`, `docker compose up -d kipple`, then A2, A3, A7 and
A11, and create no `.env` at any point. Expected: the build succeeds with no Go or Node installed; the port is 1919;
the version reads `dev` unless `KIPPLE_VERSION` was passed, which the deploy guide says.

**Also check, on any run:** the container runs as uid 65532 (`docker inspect`), and read-only with capabilities dropped and a memory cap on the compose-file and hardened `docker run` paths (the README's one-line command does not set those); a named volume (as in the
README and deploy guide) works without a `chown`, while a bind mount without one fails with a permissions error that the deploy guide's
`chown 65532:65532` line fixes.

Results of the past runs are kept in the history repo.

## Suite 6 — Phone-size shakedown

A visual and structural pass over every screen at the iPhone sizes people actually own, run in WebKit against a seeded
instance. It finds what the scripted flows do not assert: text that does not fit, a menu that runs off the screen, a
control too small to hit. It reads what is on the screen; it does not replace Suite 3 (a real device, a real thumb).

**Sizes** (CSS pixels, portrait unless noted): iPhone 17 and 17 Pro 402 x 874, 17 Pro Max 440 x 956, 16e 393 x 852,
Air 420 x 912, an SE-size phone 375 x 667, and iPhone 17 in landscape 874 x 402. Use a touch device profile with the
WebKit engine, not a narrowed desktop window. Run every size in portrait and landscape.

**Per size, on every route** (Unread, Starred, a feed's list, a folder's list, an article, Search and a saved search,
Feeds in normal and Select mode, Health, Stats, Wrapped, each Settings group, the sign-in screen), with every menu,
sheet and dialog the screen offers opened in turn:

- Clipped or truncated text and icons: a title, label, button or count cut off by its box, an icon half shown or
  overlapping text. A long title may wrap or end in an ellipsis; it may not be cut mid-glyph or push a control away.
- Menus and sheets: nothing runs off an edge, nothing covers the tab bar unless it is a modal that can be dismissed,
  and the whole menu can be reached by scrolling.
- Tap targets: every button, link, switch and row action is at least 44 x 44 px, or has 44 px of hit area around it.
- Duplicates and dead ends: the same action offered twice on one screen under one label, and any screen with no way
  back or onward (no back button, no tab, no close).
- Tab bar: the highlighted tab matches where you are, including on a feed's list, Health and a screen opened from a
  menu.
- Sidebar and menu organisation: the order of items makes sense, related items sit together, nothing that exists on
  desktop is unreachable on a phone, and nothing is listed twice.
- Horizontal scroll: the document never scrolls sideways in either orientation. Inner scrollers (a table, a code block)
  may.
- Loading, empty and error states: with no unread items, with a feed that has no items, with the network cut. Each
  says what is going on and offers a next step; none shows a blank page, a spinner that never ends, or text such as
  `undefined`, `NaN` or `[object Object]`.
- Landscape: the same, plus that the article's title and body share a left edge and the tab bar does not eat the page.

**How to run it.** Start the seeded instance (`npm run seed` in `web/`, then `node uat/wait-ready.mjs`), then drive each
size with a throwaway script that reads the instance URL and sign-in from environment variables (`KIPPLE_UAT_URL` and
the seed's throwaway account), writes screenshots and a findings list to a scratch directory outside the repository,
and runs the in-page checks above (overflow, target size, text clipped by its box, duplicate labels, reachability). The
assertable part already lives in the flows `web/uat/scroll-restore.mjs` (`npm run uat:scroll-restore`: a reload comes back to the
same place in the list), `web/uat/rotate.mjs` (`npm run uat:rotate`: turning the phone keeps the list on the same article
and marks nothing read) and `web/uat/phone-shell.mjs` (`npm run uat:phone-shell`: Stats stays inside the
window, the Feeds title is whole, the list header does not grow with a long feed title, the article's edges line up in
landscape, the tab highlight), and `web/uat/wrapping.mjs` (`npm run uat:wrapping`: a wide table scrolls sideways with its
words whole, a long unbroken word in a title wraps in the list and the reader), and `web/uat/phone-chrome.mjs`
(`npm run uat:phone-chrome`: tabs and headers at the largest text size on a 320 px phone, no save error while offline, the
undo toast, the list menu, the reader's read state); extend these flows when a shakedown finding can be stated as an assertion. A sweep script
is a reading aid that produces screenshots and candidates; a person or agent looks at each one. The sweep opens an
article (marking it read) and may tick feeds in Select mode; it must never confirm Move or Delete. Lookups of menus and
buttons go by accessible name, so a miss is logged as skipped, not as a pass.

**How to triage.** Look at the screenshot for each candidate before calling it a finding: a measurement can be wrong
(an element hidden by design, a pane that has not been drawn yet). Group findings by screen and give each a severity
from the table below (a menu that cannot be dismissed or a screen that is a dead end is P1; clipped text that is still
readable is P3). Fix by root cause, so one shared component fixed once clears every size. Re-run the whole sweep after
fixes; a fix at one size often moves another. Anything that can be asserted becomes a check in `phone-shell.mjs` before
the fix is merged.

## Harness pitfalls

Things that have produced false failures or false passes in browser runs. Check these before calling something a bug.

- **WebKit does not load lazy images that are off-screen.** Count only images whose `complete` is true; an unloaded
  below-the-fold image is not a broken proxy.
- **The layout's image element can itself carry the aspect-ratio class.** When checking that an image keeps its shape,
  inspect the wrapper `div`s only, never the `img` as well, or every layout reads as wrong.
- **External navigation strands a phone run.** Opening "Read the original" or any outside link leaves the app, and on a
  phone profile there is no second tab to close. Return to the app address after any step that can leave it, and do
  not follow outside links when the step does not need to.
- **Wait for the app to be ready before checking.** The page loads before the library does: wait for the primary
  navigation and the heading of the screen under test, and on a seeded instance run `node uat/wait-ready.mjs` first
  (the sample feeds arrive over the first minute).
- **Heavy browser runs must not share the machine with other heavy work.** A WebKit or Chromium sweep running beside a
  Go test run, a fuzz run or a second browser run is slow enough to trip timeouts and timing checks. Those failures
  disappear when the run is alone. See "Running suites in parallel" for what may overlap.
- **A failing flow is not a bug until it fails alone.** Re-run it by itself on a freshly seeded instance (new data
  directory, nothing else running). Only a failure that survives that is a finding.
- **A feed on a loopback or private address is refused** when added by address (quick add and add by URL), as a
  private-network protection. A test that serves its own fixture feeds must use the add-feed option that allows a
  private network.
- **Each run gets its own data directory and port**, taken from variables or arguments, never from a fixed number
  shared with another run: two runs on one port silently test each other.

## Agent hygiene

- Stop a process you started by its PID, kept when you started it. Never stop processes by port number, port range,
  name pattern or "everything listening" in a range: another run's instance may be listening there, and stopping it
  destroys that run's work.
- Record each PID at launch (a file in your scratch directory is enough) and stop exactly those at the end, even when
  the run failed. Remove the scratch data directory last.
- Keep scratch scripts, logs, screenshots and data directories outside the repository, in a directory named for the run.
- Report what was started and stopped. Do not leave an instance running for the next person to find.

## Checks that cannot run from the development machine

These need a real client, a real network edge or a person, so an agent on the development machine cannot do them. Each is
an owner item: the agent lists it as "blocked, owner" with the reason, and does not mark it passed.

- **Real Reader API sync clients** (Suite 2 A1 to A3, and the client row of the release checklist): only the replay of
  recorded request sequences runs here, not a client's own behavior.
- **Cloudflare Access, the positive case** (TC-C1 with a valid identity): the negative case (a request without a valid
  token is refused) runs locally; a request carrying a token the real edge issued does not.
- **A live service-worker update**: that a deployed new build replaces the old one in an open, installed app needs two
  real builds served over HTTPS and a browser that kept the first.
- **Real-device Safari and Firefox**: engine emulation catches layout and script differences; installing the PWA,
  swipe gestures, the share sheet and focus when backgrounded are Suite 3.
- **Screen readers**: scripted axe scans find missing names and roles but not whether the reading order and announcements
  make sense. A pass with a real screen reader is a person's job.
- **Feeds that need the public internet or a real hosted account** (the starter-feed liveness check, a large imported
  list, an import that reports "N new articles"): run only where that access exists.

## Running suites in parallel

Run independently what does not touch the same instance or the same machine resource, and serially what depends on the
previous step. A long serial wait costs time; overload costs false failures (see "Harness pitfalls").

**Independent, so they may run at the same time** (each on its own instance, data directory and port, started and
stopped by PID):

- Documentation checks, the website build, the history and changelog checks, and the restore drill (Suite 4).
- Suite 2 (agent-driven scenarios), which drives an API and an interface lightly.
- Static gates: lint, type checks, `govulncheck`, the changelog check.

**Heavy, so alone or nearly so:** the browser UAT (Suite 1 and the phone-size shakedown). Run it with nothing else heavy
running; run the Chromium and WebKit passes one after the other, and never next to the Go test runs or fuzz.

**Strictly serial** (each step needs the one before it):

1. The tag, on the commit whose CI is green.
2. The release workflow that builds and publishes from the tag.
3. The deploy of that published image.
4. Suite 5 (fresh-machine walkthrough) and the post-deploy checks, which test what was deployed.

Before the tag, spread the work over agents as the "Working economy" section of `CLAUDE.md` describes: independent
read-only work fans out, one writer per instance, an author stops at "pushed", and the main session owns the CI wait.

## Open questions for the owner

- **Escape after opening an article on a wide screen.** Pressing Escape moves focus back to the article's row in the
  list; it does not change the address, which stays on the article's page. On a narrow screen, where the article is the
  whole screen, Escape goes back to the list. `docs/maintainers/ui-decisions.md` says only "Escape to close and return
  focus", so whether the address should also return to the list on a wide screen is undecided. Until answered, the
  current behavior is the expectation and TC-R3 does not fail on it.

## Defect severity (borrowed scale)

Every bug-type issue (`bug`, `security`, `accessibility`, `performance`, `ux`, `test`) carries exactly one priority
label, `P0` to `P4`, in any milestone. No bug is filed or left without one. Enhancements and `roadmap` items are not
bugs and get none.

| Severity | Meaning | Exit criteria impact |
|---|---|---|
| P0 Blocker | Data loss, security bypass, can't complete a core flow | Must fix before sign-off |
| P1 High | Wrong behavior a normal session would hit | Must fix before sign-off |
| P2 Medium | Edge case or cosmetic-but-real bug | Fix or owner explicitly waives |
| P3 Low | Polish | Fix or owner explicitly waives |
| P4 Lowest | Trivial polish, nits, wording, docs mismatches | Fix when convenient; may stay open |

- Performance and UX problems get a priority by user impact, plus the `performance` or `ux` label.
- The bug report form asks for a severity; the maintainer applies the matching label when the issue is filed.
- Nothing is silently dropped: a deferral is a `roadmap` or `wontfix` label with a one-line reason.
- Check for gaps: `gh issue list --state open --label bug --search "-label:P0 -label:P1 -label:P2 -label:P3 -label:P4"`
  returns nothing.

## Reader API regression replay

Separate from the client suites above, but part of the same release-readiness gap: replay the recorded Reader API client
request sequences from the client research (kept outside this repository;
`stream/items/ids` paging, `edit-tag`, `subscription/quickadd`, `mark-all-as-read`) as a contract test against
the build under test, not just the unit-level contract tests already in CI (`internal/greader/contract_test.go`)
— this is the end-to-end version, run once per release against the actual deployed instance.
