# Kipple

Self-hosted RSS reader for the owner. Replaces the reader the owner used before, on the owner's Kipple server. Single user. The global
CLAUDE.md of the dev machine (where the owner and Claude work) also loads here and its rules apply (Haiku only as a read-only helper,
docker via PowerShell, 127.0.0.1 not localhost, name compose services explicitly).

The dev machine and the Kipple server are two different machines, and neither is how other people will run Kipple: they
pull the published image and run it on their own. Write code, docs and examples for a stranger ("your server"), and say
"the dev machine" and "the Kipple server" in conversation, never the owner's host labels. The owner's own deploy steps
live in one place, `docs/maintainers/RELEASING.md`.

## Design rules (no band-aids)

The global coding principles apply (root cause first, delete over add, one source of truth, expand/migrate/contract).
Kipple adds: for anything non-trivial, design it twice (two radically different designs) and say why one won. A docs
caveat usually means the behaviour is wrong. No flag pairs that allow invalid combinations; the contract step must
finish. Before calling code a band-aid, find out what it was for (git blame, the issue, the tests). Prefer deeper modules
with simple interfaces. With one user a removal is announced in the changelog, not in runtime code.

- Write for a stranger. User-facing text (UI, docs, errors, release notes' top paragraph) describes the product as it
  is, with no earlier versions, old ports or past decisions; history lives in CHANGELOG.md and the decision records.

## Decisions (do not relitigate; detail is in docs/design.md and docs/maintainers/ui-decisions.md)

- **Stack:** Go backend, React + TypeScript + Vite + Tailwind + shadcn frontend, SQLite in WAL mode. The frontend build
  is embedded in the Go binary. One image, one container, one port.
- **Sync API:** Google Reader API (as the two most widely used open source servers implement it, called the reference servers) only. **No Fever.** The web app is the preferred client
  (reading stats are web-only); any client that speaks the Google Reader API works. Test against the protocol, not named apps; build nothing special for any one app (issues #256, #260).
- **Refresh:** background poll every 30 min (global + per-feed override), ETag/Last-Modified, exponential backoff on
  failing feeds, manual refresh fetches all now. API clients never trigger fetches of existing feeds; a feed added from
  a client is fetched on the next scheduler tick (opt-in exception: `greader.subscribe_fetch_now`, default off, see
  docs/design.md).
- **Retention:** newest N per feed (50/100/250/500/1000/unlimited), global + per-feed. Starred never trimmed. Trimmed IDs
  and read state kept for API consistency. Trim after each fetch and when retention changes.
- **Stats:** bulk mark-as-read and mark-read-on-scroll are not reads. Active reading time = tab visible and focused.
  Stats events are never trimmed.
- **Fonts:** bundled through @fontsource, self-hosted, no CDN: Literata, Vollkorn, Gentium Book Plus, Source Serif 4,
  Arvo, Inter, Manrope, Source Sans 3, JetBrains Mono, Source Code Pro, Atkinson Hyperlegible Next. System when present:
  New York, Charter, SF Pro, SF Mono, Georgia, Menlo. Default body: New York on Apple, Literata elsewhere. User-picked
  Google Fonts: not before 2.0.0.
- **Themes:** 20 color schemes (`web/src/theme/schemes.json` is the source of truth) plus follow-system with separate
  day and night picks (default Paper and Midnight). The original seven names are aliases (white=Paper, off-white=Linen,
  sepia=Parchment, soft green=Directory, brown=Cocoa Kraft, dark=Graphite, OLED=Midnight).
- **Look:** The reference is a hosted commercial reader with magazine and cards, images up front (named in the history repo's UI decisions). Not the dense text-list readers.
- **Non-goals:** no AI features, no notifications, no social (an opt-in share of the reader's own yearly summary,
  Wrapped, is allowed), no monitoring, no multi-user. Per-device appearance profiles are not multi-user. No podcasts or media players, no
  read-later or webhook integrations, no tags. Kipple is not trying to match what other readers have.

## Layout and commands

- `cmd/kipple/` main; `internal/` Go packages; `web/` Vite app (`web/dist` embedded via `go:embed`).
- `Dockerfile` is multi-stage (node, go, distroless static nonroot uid 65532). the Kipple server has Docker but no Go or Node, so
  the image must build with Docker alone. No secrets or hostnames committed; `.env.example` documents every variable.
- Dev: `cd web && npm run seed` (Kipple on 127.0.0.1:1919, sample feeds in `%TEMP%\kipple-dev`), then
  `cd web && npm run dev` (Vite on 127.0.0.1:5173 proxies to 1919).
- Test: `go test ./...` and `cd web && npm test`. Local CI: `pwsh scripts/ci-local.ps1` (`-Docker` adds the image build
  and Trivy). "CI green" means the GitHub Actions run on the exact commit; the local run is the fast pre-push check.
  Fuzz: `scripts/fuzz.ps1` once per release, not per PR.
- Before every deploy run `/code-review high`.

## Deploy and releases

GitHub (`WPTK/Kipple`) is the source of truth; the pushed tag is what deploys, never `main` or an unpushed tree. The
exact commands, backup, verification and GHCR steps are in `docs/maintainers/RELEASING.md`; do not copy them here.

- Kipple server: service `kipple` in its compose project; it pulls the signed image by digest, with build-from-tag as the
  fallback (both in the release steps). Never a bare `up`/`down`.
- Cloudflare Access: the bypass covers exactly the `/api/greader.php` prefix; root `/accounts/ClientLogin` and
  `/reader/api/0/*` answer but stay behind Access, and the UI stays behind email OTP. The previous reader stays paused,
  not removed, until the owner says so.
- SemVer with `-alpha.N`/`-beta.N`/`-rc.N`. Annotated tag `vX.Y.Z[-pre.N]` on the exact deployed commit, made at deploy
  time; never move or reuse a pushed tag. One writer on the Kipple server at a time.
- Every behavior change adds a one-file entry under `changes/` (`changes/README.md`), never an edit to `CHANGELOG.md`; a
  release folds them in with `node scripts/changelog.mjs release X.Y.Z`.
- The checks are the workflow files in `.github/workflows/`. Any dependency change gets a govulncheck run. Suppress
  findings only with a written reason.

## Process

- Phases and the plan to 1.0 live in the history repo plan `plans/0.8-1.0-plan.md`. Release steps follow
  `docs/maintainers/RELEASING.md` (gates scale with what changed); the 1.0 sign-off is `docs/maintainers/release-checklist.md`.
- A UI change is not done until it has been driven in a real browser, at the mobile preset and on desktop, against a
  seeded instance (`npm run seed`): every control it touches, and every download or export path (a saved file can be
  an error body that no test of the handler sees). Tests must send what real browsers send (fetch metadata, `Origin`,
  `Accept`); a hand-built header set proves only what the code was written to do. Add the flow to `web/uat/` and list it in
  `web/uat/flows.json` (it then runs in the `Browser UAT` workflow, which `pr-ready` shows but does not block on), and record what you drove in the PR description.
- Save decisions and gotchas to memory. Update the history repo (`WPTK/kipple-history`, `C:\kipple-history`) at least
  daily and after every release, meeting or incident: fetch first, never force-push, and apply the scrub rules (no
  host names or labels, `rss.example.com`, account names, IPs or emails).

## Work tracking (how sessions avoid each other)

- Remaining design-review work is one tracker issue per area (label `tracker`, milestone "Design review follow-ups").
  The tracker is the only list of an area's remaining work; the Decision comment on each linked issue is the spec.
- GitHub is the only record of who is working on what, not memory. Before starting, read the open trackers, the open
  pull requests and the `in-progress` label. Set **Working now** in the tracker body and add `in-progress` when you
  start; clear both when you stop. If a tracker is taken, work another or ask.
- One writer on `web/` at a time. A second session may work outside `web/` (Go, docs, CI, history repo).
- A pull request ticks the tracker lines it finishes. Review leftovers and nits go in the pull request description or stay
  as unticked tracker lines; never a new "leftovers" issue. A defect that is on no tracker gets its own issue.

## Unattended work

The global unattended-work rules apply (branches, PRs, merge on green CI, a denied command stops that item). Also without
asking: the history repo and website PRs for version text and screenshots.

- Needs the owner's word in chat: pushing a tag, creating a GitHub release, copying data off the Kipple server,
  deploying, anything touching the Kipple server's containers or compose file (including a rollback drill).

## Working economy (token use)

The global working-economy and subagent rules apply. Kipple specifics:

- **Models and agents:** the global "Which agent" rules apply. Kipple's `pr-author` and `pr-reviewer` (both Sonnet) stay
  for the PR flow; `release-gate` (Sonnet) runs `release-gates.ps1` on the tagged commit. Never an untyped
  `general-purpose` agent (a hook blocks it), and `model: opus` only for `reviewer-deep`, `diagnoser`, `planner`.
- **Review loop cap, per PR:** one `pr-reviewer` pass, `pr-triage` sorts the findings, one `pr-author` fixes blockers and
  fixes in a single push, at most one delta review of the fix commits only. A third round goes to the owner. Nits go in the
  PR description. `reviewer-deep` (Opus) runs once, on the release-gate commit or a security-sensitive diff, never on
  feature PRs. Never re-review an unchanged head.
- **CI waits:** `ci-watcher` or the main thread runs one blocking `gh pr checks <n> --watch --fail-fast`; no polling, no
  re-running a job to see if it passes. CI already skips on prose-only PRs, cancels superseded PR runs, and fuzzes weekly;
  do not re-run fuzz, Browser UAT or audits by hand except on the commit being tagged, and only if code changed since
  their last run.
- **Audits and reports:** when the owner asks for one, walk through the script plan and wait for a go before running
  anything (global rule). Scripts first, at most one `audit` agent, no fan-out.
- **Sessions:** start a fresh Sonnet session per PR or issue and carry state in memory and the history repo. Do not
  rebuild state in a long "recovery" session; read the state file, then work. The main thread is Sonnet unless the owner
  picks Opus.
- **CI already proved it:** a release or docs-only commit needs the CI run on that commit and nothing more. Fuzz, UAT
  suites and a delta review run once, on the commit being tagged, and only if code changed since the last run.
- **Batch small PRs:** `changes/` entries, docs and wording go in one PR. The auto-fix monitor (`bind_pr` +
  `set_monitor`) is for PRs that touch code, workflows or dependencies; a docs-only or `changes/`-only PR gets one
  `gh pr checks <n> --watch --fail-fast` (CI skips build and test when every changed file is in `scripts/ci-prose.txt`).
- **Short output:** prefer the one-line-verdict scripts (`release-gates`, `pr-ready`).
- **Sessions and state:** one release per session; state goes in memory and the history repo.
