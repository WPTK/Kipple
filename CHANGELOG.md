# Changelog

All notable changes to Kipple are documented here. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Phase 2 (reading UI backend) so far.

### Added

- UI API: items list (card lists), item detail, open, star, mark-read by scope, bootstrap read
  and live unread counts over SSE.
- Stats API and recorder: validated reading events; star/unstar from the Reader API edit-tag
  are recorded too.
- FTS5 item search with safe query building, rank keyset cursors and snippets; `kipple
  fts-rebuild`.
- Full-text extraction (readability) through the guarded client: `POST /api/items/{id}/fulltext`
  with stored results and errors.
- Inline full-text extraction at ingest: for feeds whose full-text mode is on, the worker extracts
  the article pages of new items (newest first, at most 20 per fetch, 3 at a time, 2 per article
  host, 10 s each, 60 s per fetch) before the commit, through the guarded client with the feed's
  network flags. Results and failures are stored in `item_fulltext`; a failure never fails the
  fetch and is not retried by polling. Items past the cap or budget are left for the on-demand
  endpoint and counted in the fetch log note (`fulltext: ok/tried`, `fulltext_deferred: n`).
- Signed streaming image proxy at `/img` with SSRF-guarded transports; card, detail and open
  images are rewritten to it at serve time.
- Feed icons at `/api/feeds/{id}/icon`.
- Feed and folder CRUD: `POST`/`PATCH /api/feeds` with guarded feed discovery and URL editing,
  delete, purge, refresh, mark-fetch-read, fetch log and folder endpoints.
- CI security tooling: govulncheck, staticcheck, gosec (gated on high severity and confidence),
  gitleaks and a Trivy image scan; gofmt check; Dependabot for Go, npm, Actions and Docker.
- `CHANGELOG.md`, pull request template and `SECURITY.md`.
- Settings API: `GET`/`PATCH /api/settings` with per-key validation (unknown and `sys.*` keys
  answer 400 naming the key; `null` resets to the default). A `retention.default` change starts
  a retention run; a changed refresh interval pulls due times in and wakes the scheduler.
  `POST /api/retention/apply` ("Apply retention now").
- Account API: `POST /api/account/password` (signs out other sessions, keeps the caller's) and
  `POST /api/account/api-password` (generate or set; revokes the Reader token at once and clears
  the login memo). Both verify the current password under the login lockout.
- Settings `greader.ot_includes_user_changes`, `greader.subscribe_fetch_now` and `ui.*` defaults.
- `GET`/`PATCH /api/settings` now describe every user-visible key for the UI: label, one-sentence
  help, group, kind, options, range, step, unit and surface (`reader_menu`, `settings` or
  `hidden`), plus the current value and default. The response is `{settings: [...], values: {...}}`.
- `ui.reading_density` (`compact`, `comfortable`, `relaxed`) with its CSS mapping (line height and
  column width) served in the option metadata.
- `oled` theme (true black, for battery savings on OLED screens).
- `fetch.user_agent_mode`: `default`, `browser_on_failure` (the default) or `browser_always`.
  With `browser_on_failure` a feed that answers 403/406 (or a Cloudflare 503 challenge) is retried
  once with a browser User-Agent; if that works the feed remembers it (`feeds.ua_fallback`,
  migration 0002) and uses the browser UA from then on. A per-feed User-Agent still wins.

### Changed

- `fetch.user_agent` is now an optional custom User-Agent that replaces the built-in browser
  string; it no longer applies when the mode is `default`.
- Account passwords may be 5 to 256 characters (was 12 to 256).
- CI runs `go test -race -shuffle=on` with a 15 minute timeout and on `phase-2` pushes; the
  Docker job loads the image so it can be scanned.
- Cleanups from staticcheck: removed dead code and a dead test field, named HTTP status constants.

### Removed

- `ui.line_height` and `ui.content_width`, replaced by `ui.reading_density`. Stored rows are
  deleted by migration 0002.

### Fixed

- A per-feed refresh request keeps its intent when the feed is already in flight.
- A fetch commit for a feed whose URL was edited mid-flight is dropped.
- `feed_id` in SSE events is a string; `trim_only` runs on disabled feeds.
- Reader API: a password change no longer leaves the old token valid for a few seconds when an
  account lookup was in flight during the invalidation.
- The custom user agent setting rejects control characters (other than tab) and DEL, which would
  otherwise make every fetch fail.
- A fetch made stale by a URL edit no longer teaches the feed the browser user agent; editing a
  feed's URL or its own user agent resets the learned browser-UA flag.
- Lowering the refresh interval no longer pulls a feed ahead of its publisher refresh hint when
  following publisher hints is on.
- Inline full-text extraction is capped at 4 articles at once across all workers (`Options.FulltextGlobal`),
  so refreshing many full-text feeds cannot spike memory.
- Discovery reads up to the 10 MiB fetch limit and sniffs the body instead of trusting
  `text/html`.
- Full-text flights are panic-safe, counts publishes are ordered and bulk-star stats are batched.
- The time zone setting is stored under `tz` as the design says (it was read as `settings.tz`).

## [0.1.0] - 2026-09-25

Phase 1: fetch, store and Reader API.

### Added

- Feed fetching with a guarded HTTP client, conditional requests, backoff and a scheduler.
- SQLite (WAL) store with migrations and single-writer commits.
- Retention (newest N per feed, starred never trimmed) with tombstones and a maintenance job.
- Google Reader API for Reeder Classic and NetNewsWire.
- OPML import and export.
- One-file status page at `/_status` with login, feed health, refresh and live events.
- Multi-stage Docker image and CI.

[Unreleased]: https://github.com/WPTK/Kipple/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/WPTK/Kipple/releases/tag/v0.1.0
