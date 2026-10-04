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

## Decisions (do not relitigate; detail is in docs/design.md and docs/ui-decisions.md)

- **Stack:** Go backend, React + TypeScript + Vite + Tailwind + shadcn frontend, SQLite in WAL mode. The frontend build
  is embedded in the Go binary. One image, one container, one port.
- **Sync API:** Google Reader API (FreshRSS/Miniflux flavor) only. **No Fever.** The web app is the preferred client
  (reading stats are web-only); Reeder Classic and NetNewsWire are supported secondary clients, test against both.
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
- **Look:** Feedly is the reference (magazine/cards, images up front). Not NewsBlur, FreshRSS or Miniflux.
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
exact commands, backup, verification and GHCR steps are in `docs/RELEASING.md`; do not copy them here.

- Kipple server: service `kipple` in its compose project, named volume for `/data`, 10m x 3 log rotation. Build from the
  tag with the three build args (`KIPPLE_VERSION`, `KIPPLE_VCS_REF`, `KIPPLE_BUILD_DATE`; `.git` is not in the build
  context). Never a bare `up`/`down`. From 0.6.0-rc.1 the server pulls the signed image by digest instead.
- Public URL `https://rss.example.com` via the owner's cloudflared tunnel. The Access bypass covers exactly the `/api/greader.php`
  prefix; root `/accounts/ClientLogin` and `/reader/api/0/*` answer but stay behind Access; the UI stays behind email
  OTP. yarr stays paused, not removed, until the owner says so.
- SemVer with `-alpha.N`/`-beta.N`/`-rc.N`. Annotated tag `vX.Y.Z[-pre.N]` on the exact deployed commit, made at deploy
  time; never move or reuse a pushed tag. One writer on the Kipple server at a time.
- `CHANGELOG.md` is Keep a Changelog 1.1.0: every behavior change adds a one-file entry under `changes/`
  (`changes/README.md`), never an edit to `CHANGELOG.md`; a release folds them in with
  `node scripts/changelog.mjs release X.Y.Z`.
- CI: govulncheck, staticcheck, gosec (high/high only), gitleaks, Trivy; the web job runs lint, Vitest, build, theme
  contrast and `npm audit --omit=dev --audit-level=high`. Any dependency change gets a govulncheck run. Suppress findings
  only with a written reason.

## Process

- Phases and the plan to 1.0 live in the history repo plan `plans/0.8-1.0-plan.md`. Release steps follow
  `docs/RELEASING.md` (gates scale with what changed); the 1.0 sign-off is `docs/release-checklist.md`.
- Verify iOS layout in the browser pane at the mobile preset before calling a UI change done.
- Save decisions and gotchas to memory. Update the history repo (`WPTK/kipple-history`, `C:\kipple-history`) at least
  daily and after every release, meeting or incident: fetch first, never force-push, and apply the scrub rules (no
  host names or labels, `rss.example.com`, account names, IPs or emails).

## Unattended work

- May do without asking: branches and worktrees, commits, PRs, merge to `main` when CI is green on the exact head (one PR
  at a time, base merged in, never force-pushed), issues and labels, subagents (never Haiku), the history repo, and
  website PRs for version text and screenshots.
- Needs the owner's word in chat: pushing a tag, creating a GitHub release, copying data off the Kipple server,
  deploying, anything touching the Kipple server's containers or compose file (including a rollback drill).
- A denied command stops that item: keep the exact command in a scratch note and carry on elsewhere; never route around it.

## Working economy (token use)

Most of the cost is context re-read on every turn, so keep each context small.

- **Models:** Sonnet for routine code, docs, release steps and checks; Opus only for review of a risky diff, root-causing,
  and design decisions. Never Haiku.
- **Subagents:** use one only when the work is independent, large, or must not fill this context. Give it the files and
  the question, a model, and a stop condition. Do not spawn a verifier for a fact one command can check. There is no
  limit on how many agents run at once.
- **Do not re-verify what CI already proved.** A release or docs-only commit needs the CI run on that commit and nothing
  more. Fuzz, UAT suites and a delta review run once, on the commit being tagged, and only if code changed since the
  last run.
- **Keep output small:** pipe test, CI and npm logs through `tail`/`grep`, read files by range, search before reading.
  Do not re-read a file just edited.
- **Sessions:** one task or release per session; start a new one after a release or when the context passes about
  300K tokens instead of carrying a week of history. Put state in memory and the history repo, not in the chat.
- **Replies:** short and plain, result first, the decision the owner must make second.
