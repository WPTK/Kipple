# Kipple

Self-hosted RSS reader for the owner. Replaces yarr on the owner's Kipple server. Single user. The global
CLAUDE.md of the dev machine (where the owner and Claude work) also loads here and its rules apply (never Haiku,
docker via PowerShell, 127.0.0.1 not localhost, name compose services explicitly).

The dev machine and the Kipple server are two different machines, and neither is how other people will run Kipple: they
pull the published image and run it on their own. Write code, docs and examples for a stranger ("your server"), and say
"the dev machine" and "the Kipple server" in conversation, never the owner's host labels. The owner's own deploy steps
live in one place, `docs/RELEASING.md`.

## Design rules (no band-aids)

Before adding a flag, fallback, warning, retry, cache or special case, name the root cause and look for the change that
makes the case impossible. For anything non-trivial, design it twice (two radically different designs) and say why
one won. Prefer deleting to adding; a docs caveat usually means the behaviour is wrong. One source of truth: no state
copied in several places, no flag pairs that allow invalid combinations. Removing or renaming something is expand,
migrate, contract, and the contract must finish. Before calling code a band-aid, find out what it was for (git blame,
the issue, the tests). Do not over-decompose either: prefer deeper modules with simple interfaces. With one user a
removal is announced in the changelog, not in runtime code. Every PR description states the root cause, what the change
removes and adds (`git diff --shortstat`), and, if it adds a workaround, why no root fix was possible.

- Write for a stranger. User-facing text (UI, docs, errors, release notes' top paragraph) describes the product as it
  is, with no earlier versions, old ports or past decisions; history lives in CHANGELOG.md and the decision records.

## Decisions (do not relitigate)

- **Stack:** Go backend, React + TypeScript + Vite + Tailwind + shadcn frontend, SQLite in WAL
  mode. Frontend build is embedded in the Go binary. One image, one container, one port.
- **Sync API:** Google Reader API (FreshRSS/Miniflux flavor) only. **No Fever.** The web app is the
  intended and preferred client (reading stats are web-only). Reeder Classic and NetNewsWire are supported
  secondary clients: test against both.
- **Refresh:** background poll every 30 min (global + per-feed override), ETag/Last-Modified
  conditional requests, exponential backoff on failing feeds, manual refresh fetches all now.
  API clients never trigger fetches of existing feeds (the Reader API has no refresh-all call; if a client ever sends one, it is ignored); a feed added from a client is fetched on the next scheduler tick, which the add brings forward (the one opt-in exception is the setting `greader.subscribe_fetch_now`, default off: a bounded 8 s wait for the first fetch).
- **Retention:** newest N per feed (50/100/250/500/1000/unlimited), global + per-feed. Starred
  never trimmed. Trimmed IDs and read state kept for API consistency. Trim after each fetch and when retention changes (a settings change or Apply retention now).
- **Stats:** bulk mark-as-read and mark-read-on-scroll are not reads. Active reading time =
  tab visible and focused. Stats events are never trimmed (kept forever, separate from the id ledger).
- **Fonts:** bundled through @fontsource and self-hosted, no CDN: Literata, Vollkorn, Gentium
  Book Plus, Source Serif 4, Arvo, Inter, Manrope, Source Sans 3, JetBrains Mono, Source Code
  Pro, Atkinson Hyperlegible Next. System when present: New York, Charter, SF Pro, SF Mono,
  Georgia, Menlo. Default body: New York on Apple, Literata elsewhere. (2.0.0 idea, if cheap:
  let users pick their own Google Font. Not before 2.0.0.)
- **Themes:** 20 color schemes (`web/src/theme/schemes.json` is the source of truth) plus
  follow-system with separate day and night picks (default Paper and Midnight). The original
  seven names are aliases: white=Paper, off-white=Linen, sepia=Parchment, soft green=Directory,
  brown=Cocoa Kraft, dark=Graphite, OLED=Midnight.
- **Look:** Feedly is the reference (magazine/cards with images up front). Not NewsBlur,
  FreshRSS or Miniflux.
- **Non-goals:** no AI features, no notifications, no social (no other people's data, no comparisons; an opt-in share of the reader's own yearly summary, Wrapped, is allowed), no monitoring, no multi-user. Per-device appearance profiles (one account, many browsers) are not multi-user.

## Layout

- `cmd/kipple/` main. `internal/` Go packages (fetch, sched, store, greader, api, extract, imgproxy, imgcache, filter, backup, stats, ...).
- `web/` Vite app. `web/dist` is embedded via `go:embed` at build time.
- `Dockerfile` is multi-stage (node build → go build → distroless static nonroot, uid 65532). The Kipple server has Docker
  but no Go or Node, so the image must build with Docker alone.
- No secrets or hostnames committed. `.env.example` documents every variable.

## Commands

- Dev: `cd web && npm run seed` (Kipple on 127.0.0.1:1919 with sample feeds in `%TEMP%\kipple-dev`), or set
  `KIPPLE_ADDR=127.0.0.1:1919` and `KIPPLE_DATA=%TEMP%\kipple-dev` and run `go run ./cmd/kipple serve`; then
  `cd web && npm run dev` (Vite on 127.0.0.1:5173 proxies to 1919).
- Test: `go test ./...` and `cd web && npm test`.
- Local CI: `pwsh scripts/ci-local.ps1` (add `-Docker` for the image build and Trivy). It mirrors the CI workflow with the same pinned tools; GitHub Actions is on (the repository is public) and its run on the exact commit is what "CI green" means; the local run is the fast check before pushing. Fuzz targets: `scripts/fuzz.ps1` before each release (see `docs/RELEASING.md`).
- Build image locally: `docker build -t kipple:dev .`
- Run `/code-review high` before every deploy.

## Deploy

GitHub is the source of truth (repo `WPTK/Kipple`), and the pushed release tag is what deploys: nothing deploys from
an unpushed tree or from `main`. On the Kipple server the exact commands are in `docs/RELEASING.md` (step 9: build the tag, never a bare `up`/`down`,
return the checkout to `main`). `.git` is not in the build context, so the version reaches the binary only through
`KIPPLE_VERSION` and the service's `build.args`. From 0.6.0-rc.1 the server pulls the signed image by digest instead.
Service `kipple` in the server's compose project, named volume for `/data`, 10m x 3 log rotation.
Public URL `https://rss.example.com` via the owner's cloudflared tunnel; the Access bypass covers exactly the
`/api/greader.php` prefix (Reader API and its `/icon/` URLs); root `/accounts/ClientLogin` and
`/reader/api/0/*` answer too but stay behind Access, the UI stays behind email OTP. yarr stays paused, not removed, until
the owner says so. OPML source: `/home/user/newsblur-export.opml`.

## Process

Phases: 1 fetch/store/retention/Reader API; 2 reading UI (including themes and fonts); 3 PWA
(manifest, service worker, install, offline); 4 stats. Then release steps 8+, in order: full code
audit and review; changelog review; documentation run; first-time Docker setup (fresh-machine
walkthrough); how to retain and back up settings and Kipple itself; a final go/no-go meeting.
Before going public there is one more meeting, then the hostname/IP scrub of committed docs. the owner
uses each phase for a day before the next starts. Sonnet for routine code, Opus as advisor and
reviewer. One writer on the Kipple server at a time. Verify iOS layout in the browser pane at the mobile
preset before calling a UI phase done. Save decisions and gotchas to memory.
**Update the history repository (`WPTK/kipple-history`, working copy `C:\kipple-history`) as part of the normal
workflow, at the very least daily** and after every release, meeting or incident: diary, timeline, meetings,
decisions, challenges, milestones, audits, human feedback, plan snapshots. `git fetch` first and never force-push;
apply the same scrub rules as this repo (no host names or labels, `rss.example.com`, no account names, IPs or emails).

## Releases and CI

- SemVer, with `-alpha.N`/`-beta.N`/`-rc.N` prereleases. Annotated tag `vX.Y.Z[-pre.N]` on the
  exact commit deployed to the Kipple server, made at deploy time; never move or reuse a pushed tag.
- `CHANGELOG.md` is Keep a Changelog 1.1.0: every behavior change adds a one-file entry under
  `changes/` (`changes/README.md`), never an edit to `CHANGELOG.md`, so branches don't conflict; a release folds them in
  with `node scripts/changelog.mjs release X.Y.Z`.
- CI has govulncheck, staticcheck, gosec (fails on high/high only), gitleaks and Trivy. Any
  dependency change gets a govulncheck run. Suppress findings only with a written reason.
- The web job runs lint, Vitest, the build, the theme contrast check and
  `npm audit --omit=dev --audit-level=high`.
