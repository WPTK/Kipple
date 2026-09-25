# Changelog

All notable changes to Kipple are documented here. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Phase 2 (reading UI backend) so far.

### Added

- Security headers on every response (`internal/httpx`): a strict Content-Security-Policy for pages (no
  inline script; `img-src` follows `imgproxy.mode`), `default-src 'none'` for API responses, plus
  `Referrer-Policy: no-referrer`, `Permissions-Policy`, `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy`,
  `nosniff`, and HSTS with `upgrade-insecure-requests` only when the effective scheme is https.
- UI API: items list (card lists), item detail, open, star, mark-read by scope, bootstrap read
  and live unread counts over SSE.
- Stats API and recorder: validated reading events; star/unstar from the Reader API edit-tag
  are recorded too.
- FTS5 item search with safe query building, rank keyset cursors and snippets; `kipple
  fts-rebuild`.
- Full-text extraction (readability) through the guarded client: `POST /api/items/{id}/fulltext`
  with stored results and errors.
- Full-text extraction at ingest: for feeds whose full-text mode is on, the new items are committed
  first and then extracted in a bounded background pool (newest first, at most 20 per fetch, 4 at
  once overall, 2 per article host, 10 s each, 500 queued), through the guarded client with the
  feed's network flags. Each result is stored with one small write, only while the item still
  exists with the same URL, and announced with a `fulltext.ready` SSE event. A failure never fails
  the fetch and polling does not retry it. Items past the caps are left for the on-demand endpoint
  and counted in the fetch log note (`fulltext_picked: n`, `fulltext_deferred: n`). The queue is in
  memory: items queued at a restart are extracted on demand instead.
- Migration 0003: `item_fulltext.error_class` (`transient` or `permanent`); errors stored before it
  read as permanent.
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
- Reader API full-text hold: a new item of a full-text feed is left out of `stream/items/ids`,
  `stream/contents`, `stream/items/contents`, `unread-count` and the default `mark-all-as-read`
  until its extraction has finished (a result or a stored error) or 30 s have passed since its
  crawl time (capped at 60 s, inside the `ot` slack, so a client that synced meanwhile still gets
  it next time). Decided in SQL, so paging is exact. The web UI is not held. No setting.
- Cards and item details carry `origin_title` (the feed an archived starred item came from) and `source` (`origin_title`, else the feed title), so unsubscribed starred items show where they came from.
- Startup self-check: the store probes JSON1 and FTS5 (unicode61, `snippet`, `bm25`) in the temp schema before migrating and refuses to start, naming every missing feature, so a driver swap cannot silently lose search.

### Changed

- Article HTML served to the web UI goes through one serve-time pass (`sanitize.ServeHTML`): links open in a
  new tab with `rel="noopener noreferrer"` and lose tracking parameters (new setting `links.strip_tracking`,
  default on; card and detail `url` too), ids and in-page anchors get a `kp-` prefix, YouTube and Vimeo iframes become
  click-to-load placeholders with a proxied thumbnail, and audio and video get controls, `preload="none"` and no autoplay.
  Stored HTML and Reader API output are unchanged.
- At ingest, an iframe that is not YouTube or Vimeo becomes a link ("Embedded content from host") instead of
  vanishing; Vimeo player iframes are now kept. New items only.
- The status page script moved to `/_status.js` so the page runs under the strict CSP.
- Full-text extraction runs through one shared runner (`internal/ftrun`) for the ingest pool and
  `POST /api/items/{id}/fulltext`: opening an item the pool is extracting joins that run instead of
  fetching twice, and the per-article-host limit of 2 covers both. The outcome is saved inside the
  run, before joined requests are released, so they no longer each save. The endpoint's fetch now
  uses the same outgoing User-Agent as ingest.
- A background full-text save also requires the effective mode to still be on, so switching it off
  mid-extraction skips the save and the `fulltext.ready` announcement.
- The fetch log note `fulltext_queued` is now `fulltext_picked` (it is chosen before the commit);
  items picked but not queued are logged with the real counts. A closed queue at shutdown is logged
  as shut, not as full.
- The post-commit lookup of new item ids has its own 5 s deadline and, if it fails, leaves the items
  to on-demand with a warning.

- Full-text extraction now classes transport failures like feed fetches: a blocked address, an
  unverifiable certificate, an unknown host and a redirect loop are permanent (no hourly retry);
  timeouts, resets, refused connections, 5xx, 429 and HTTP 408 stay transient.

- `fetch.user_agent` is now an optional custom User-Agent that replaces the built-in browser
  string; it no longer applies when the mode is `default`.
- Account passwords may be 5 to 256 characters (was 12 to 256).
- CI runs `go test -race -shuffle=on` with a 15 minute timeout and on `phase-2` pushes; the
  Docker job loads the image so it can be scanned.
- Cleanups from staticcheck: removed dead code and a dead test field, named HTTP status constants.
- Feed health statuses are one shared rule (`store.FeedStatus`) used by `/api/bootstrap` and `/api/health/feeds`: `archive`, `dead` (was `gone`), `disabled` (was `user`, and any disabled feed), `failing` (14 or more consecutive failures), `erroring` (1 to 13), `throttled` (the host is held by a Retry-After), `redirecting` (a permanent redirect is pending; a temporary redirect is only a notice), `silent` (healthy but no new items for 90 days) and `ok`. A single blip is now `erroring`, not `failing`.
- `GET /api/health/feeds` adds `snapshot` (`last_at`, `last_error`), `clock` (`ahead_s`), `db` (`db_bytes`, `wal_bytes`, `backup_bytes`, `imgcache_bytes`) and per feed `host_throttled_until`; `migrated` is renamed `redirect_pending`.
- The nightly maintenance job (purge, optimize, snapshot, Sunday FTS check) now runs at 04:10 in the `tz` setting instead of the container `TZ`, and a changed `tz` takes effect at the next minute check. The `TZ` environment variable now only affects log timestamps. The `tz` help text says so.
- The `retention.default` help text now says that the newest N articles are kept whether read or unread (starred articles are always kept).
- `GET /api/opml` (and the future stats CSV) now needs only the same-origin rule (`Sec-Fetch-Site`, or `Origin` when that is absent), not `X-Kipple-Client`, so a plain download link works. Every other route keeps the header rule.
- A `Retry-After` given as an HTTP date is measured against the response's own `Date` header (falling back to our clock), so a publisher whose clock is off still gets the wait it meant.
- One unusable feed entry no longer costs the whole fetch: an empty entry, or one whose conversion fails, is dropped and counted in the fetch log note `skipped_malformed_items: n/total`, and the rest of the document commits.
- An item's enclosures are deduplicated by resolved URL at ingest.

### Removed

- `ui.line_height` and `ui.content_width`, replaced by `ui.reading_density`. Stored rows are
  deleted by migration 0002.

### Fixed

- A panic while parsing a hostile article page no longer crashes the process: it is stored as a
  permanent extraction error and logged with its stack.

- `POST /api/feeds/{id}/refresh` on a full-text feed no longer waits for article extraction (it used
  to run inside the fetch worker for up to 60 s, so the refresh often answered 202 pending and slow
  article hosts could tie up the worker pool).
- `POST /api/items/{id}/fulltext` retries a stored transient failure (timeout, connection error,
  5xx, 429) by itself once the last attempt is over an hour old; permanent failures (404, 410, 403,
  not readable) stay sticky and `?refresh=1` always retries.
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
