# Kipple

Self-hosted RSS reader for the owner. Replaces yarr on Host-A. Single user. The global Host-B
CLAUDE.md also loads here and its rules apply (never Haiku, docker via PowerShell, 127.0.0.1
not localhost, name compose services explicitly).

## Decisions (do not relitigate)

- **Stack:** Go backend, React + TypeScript + Vite + Tailwind + shadcn frontend, SQLite in WAL
  mode. Frontend build is embedded in the Go binary. One image, one container, one port.
- **Sync API:** Google Reader API (FreshRSS/Miniflux flavor) only. **No Fever.** Primary client
  Reeder Classic, secondary NetNewsWire. Test against both.
- **Refresh:** background poll every 30 min (global + per-feed override), ETag/Last-Modified
  conditional requests, exponential backoff on failing feeds, manual refresh fetches all now.
  API clients never trigger fetches of existing feeds, except a client explicitly asking to refresh all feeds; a feed added from a client is fetched on the next scheduler tick, which the add brings forward.
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
- **Non-goals:** no AI features, no notifications, no social, no monitoring, no multi-user. Per-device appearance profiles (one account, many browsers) are not multi-user.

## Layout

- `cmd/kipple/` main. `internal/` Go packages (fetch, sched, store, greader, api, extract, imgproxy, imgcache, filter, backup, stats, ...).
- `web/` Vite app. `web/dist` is embedded via `go:embed` at build time.
- `Dockerfile` is multi-stage (node build → go build → distroless static nonroot, uid 65532). Host-A has Docker
  but no Go or Node, so the image must build with Docker alone.
- No secrets or hostnames committed. `.env.example` documents every variable.

## Commands

- Dev: `cd web && npm run seed` (Kipple on 127.0.0.1:7080 with sample feeds in `%TEMP%\kipple-dev`), or set
  `KIPPLE_ADDR=127.0.0.1:7080` and `KIPPLE_DATA=%TEMP%\kipple-dev` and run `go run ./cmd/kipple serve`; then
  `cd web && npm run dev` (Vite on 127.0.0.1:5173 proxies to 7080).
- Test: `go test ./...` and `cd web && npm test`.
- Build image locally: `docker build -t kipple:dev .`
- Run `/code-review high` before every deploy.

## Deploy

GitHub is the source of truth (private repo `Kipple`). Nothing deploys from an unpushed tree.
On Host-A: `ssh host-a 'cd /home/user/kipple && git pull && docker compose -f /home/user/stack/docker-compose.yml build kipple && docker compose -f /home/user/stack/docker-compose.yml up -d kipple'`.
Service `kipple` in compose project `host-a`, named volume for `/data`, 10m x 3 log rotation.
Public URL `https://rss.example.com` via Host-B's cloudflared; the Access bypass covers exactly the
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
reviewer. One writer on Host-A at a time. Verify iOS layout in the browser pane at the mobile
preset before calling a UI phase done. Save decisions and gotchas to memory.

## Releases and CI

- SemVer, with `-alpha.N`/`-beta.N`/`-rc.N` prereleases. Annotated tag `vX.Y.Z[-pre.N]` on the
  exact commit deployed to Host-A, made at deploy time; never move or reuse a pushed tag.
- `CHANGELOG.md` is Keep a Changelog 1.1.0: every behavior change adds an entry under
  `[Unreleased]`; a release moves it under the version heading.
- CI has govulncheck, staticcheck, gosec (fails on high/high only), gitleaks and Trivy. Any
  dependency change gets a govulncheck run. Suppress findings only with a written reason.
- The web job runs lint, Vitest, the build, the theme contrast check and
  `npm audit --omit=dev --audit-level=high`.
