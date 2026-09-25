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
  API clients never trigger fetches.
- **Retention:** newest N per feed (50/100/250/500/1000/unlimited), global + per-feed. Starred
  never trimmed. Trimmed IDs and read state kept for API consistency. Trim after fetch only.
- **Stats:** bulk mark-as-read and mark-read-on-scroll are not reads. Active reading time =
  tab visible and focused. Stats events are never trimmed.
- **Fonts:** bundled and self-hosted, no CDN: Literata, Charter, Vollkorn, Gentium Book Plus,
  Source Serif 4, Arvo, Inter, Manrope, Source Sans 3, JetBrains Mono, Source Code Pro. System
  when present: New York, SF Pro, SF Mono, Georgia, Menlo. Default body: New York on Apple,
  Literata elsewhere.
- **Themes:** white, off-white, sepia, soft green, brown, dark, follow-system.
- **Look:** Feedly is the reference (magazine/cards with images up front). Not NewsBlur,
  FreshRSS or Miniflux.
- **Non-goals:** no AI features, no notifications, no social, no monitoring, no multi-user.

## Layout

- `cmd/kipple/` main. `internal/` Go packages (fetch, store, greader, readability, stats).
- `web/` Vite app. `web/dist` is embedded via `go:embed` at build time.
- `Dockerfile` is multi-stage (node build → go build → scratch/distroless). Host-A has Docker
  but no Go or Node, so the image must build with Docker alone.
- No secrets or hostnames committed. `.env.example` documents every variable.

## Commands

- Dev: `cd web && npm run dev` plus `go run ./cmd/kipple` (API on 127.0.0.1:8080).
- Test: `go test ./...` and `cd web && npm test`.
- Build image locally: `docker build -t kipple:dev .`
- Run `/code-review high` before every deploy.

## Deploy

GitHub is the source of truth (private repo `Kipple`). Nothing deploys from an unpushed tree.
On Host-A: `ssh host-a 'cd /home/user/kipple && git pull && docker compose -f /home/user/stack/docker-compose.yml build kipple && docker compose -f /home/user/stack/docker-compose.yml up -d kipple'`.
Service `kipple` in compose project `host-a`, named volume for `/data`, 10m x 3 log rotation.
Public URL `https://rss.example.com` via Host-B's cloudflared; the Reader API path gets a
Cloudflare Access bypass, the UI stays behind email OTP. yarr stays paused, not removed, until
the owner says so. OPML source: `/home/user/newsblur-export.opml`.

## Process

Phases: 1 fetch/store/retention/Reader API; 2 reading UI; 3 themes, fonts, PWA; 4 stats. the owner
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
- TODO when the phase 2 frontend deps land: add `npm audit --omit=dev`, ESLint and Vitest to
  the web job.
