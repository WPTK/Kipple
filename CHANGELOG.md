# Changelog

All notable changes to Kipple are documented here. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Phase 2 (reading UI backend) so far.

### Added

- Schema 4 (`0004_filters_devices.sql`), schema only (filters and muted items now use it, see below; devices and auto-read do not yet): table `filters` (keyword rules: scope, kind, action, terms, fields, options, hit counters), `items.muted_by` with the partial index `idx_items_muted`, `items.muted_was_read` (the read state before a mute), `categories_json` on `item_content` and `trimmed_content`, `feeds.auto_read_days`, and table `devices` (per-device appearance profiles). Additive and O(1); the runner takes its usual pre-migration snapshot first, and an older binary refuses a schema-4 database. Rehearsed on a copy of the real phase 1 database: schema 1 to 4 in 0.3 s (5,600 items, 138 feeds, 52.9 MB snapshot), integrity and foreign-key checks clean, row counts unchanged. Trim and restore now copy `categories_json` through the retention stub.
- `internal/filter`, the keyword rules engine (wired into ingest and the filters API below): text rules (single words, phrases with any whitespace between the words, whole-word boundaries, case and diacritic folding, CJK as substrings) and RE2 regex rules over title, author, content, URL, categories and feed title; global, folder and feed scope; invert; deterministic precedence (star beats mute, mute implies read, lowest mute id wins). Bounded work: at most 200 rules, 25 enabled regex rules, 2,000 enabled text terms, regexes rejected when empty-matching or over 5,000 instructions, and scanned text truncated (content 32 KiB for text and 8 KiB for regex, other fields 4 KiB). A required-literal prefilter keeps case-insensitive regexes fast. Measured: 10,000 items x 50 rules in about 1.7 s; 25 regex rules on a full scan in about 8 ms per item. Table, fuzz (four targets, seeds committed) and benchmark tests.

- Keyword filters at ingest. New items are evaluated against the enabled rules in the fetch commit (no network, a cheap in-memory evaluation, the compiled set cached per filter-write generation, so an unchanged rule set costs one atomic load): `mute` marks the item read and sets `items.muted_by` to the lowest matching mute rule, `mark_read` marks it read, `star` stars it (and beats a mute), `highlight` is drawn by the client. Reeder and NetNewsWire need nothing: a muted item is an ordinary read item, so unread counts and `xt=read` agree with the web. Muted items are skipped by full-text extraction (never queued, never held by the Reader hold), left out of `fetch.done.new_item_ids` (which gains `muted_items`), and trimmed before real items (`ORDER BY (muted_by IS NULL) DESC, sort_at DESC, id DESC`). Each fetch_log row notes `filters: muted N, marked_read N, starred N`; rule hit counts count only matches whose action took effect. Item categories are now stored at ingest (`item_content.categories_json`, at most 20 of 100 runes) so `category` rules work for new items. With no rules nothing changes.
- Marking an item unread, or starring it (web or Reader API), clears its mute; a plain mark-unread is the restore.
- Filters API: `GET/POST /api/filters`, `PATCH/DELETE /api/filters/{id}` (validation errors are `400 {"error":"bad_filter","field","message"}`), `POST /api/filters/preview` (a bounded dry run with counts and up to 20 sample cards, no writes), `POST /api/filters/{id}/apply` (explicit retroactive apply as a run with `run.start`, `run.progress` and `run.done` events, writes in batches of 500 behind the commit gate) and `DELETE /api/filters/{id}?unmute=keep|read|unread` (restores what the rule muted, in batches). `POST /api/filters` accepts `apply_existing`. Filter changes publish `filters.changed`.
- `GET /api/items?view=muted` (the Muted view, cards carry `muted_by` and `muted_by_name`); `view=all` and search now exclude muted items; mark-read `scope.view:"muted"` is accepted. Bootstrap gains `counts.muted` and `highlights`; the `counts` event gains `muted`; `items.state` may carry `muted`.

- Per-device appearance profiles (backend plan step 12). The HttpOnly `kipple_device` cookie (128-bit random id, `SameSite=Lax`, `Secure` when https, 400-day `Max-Age`, never accepted by the Reader API and never required) is issued by the first bootstrap or device request and selects a row of the `devices` table; its `settings` object holds overrides only (at most 8 KB). The effective value of a key is the device override, else the account default (the `ui.*` settings row), else the built-in default. New endpoints (session and same-origin like the rest): `GET`/`PATCH /api/device`, `PUT /api/device/name`, `GET /api/devices`, `POST /api/device/copy-from/{id|defaults}`, `POST /api/device/make-default` and `DELETE /api/devices/{id}` (not the current one). `PATCH /api/device` validates device-scoped settings with the same validators as `PATCH /api/settings`, and the web client's former localStorage keys (layout, per-feed and per-folder layout overrides, order, article and sidebar widths, link target, unread badge, text size, spacing, motion, shortcuts, and so on, named `client.*`) with whitelisted validators; unknown keys, wrong types and out-of-range values are `400 invalid_settings`, and an over-size profile is `413`. Bootstrap gains `device` (`id`, `name`, `profile`, `merged`). `GET /api/settings` metadata gains `scope` (`global`, `device` or `both`). At most 50 devices (the least recently seen is evicted), and the nightly job purges devices unseen for 400 days. Layout ids stay `magazine` and `headlines` (labels Editorial and Email - Compact).
- Settings: `ui.theme` now takes the twenty round-2 scheme ids (plus `system`); the ids of the first draft (`white`, `off-white`, `sepia`, `soft-green`, `brown`, `dark`, `oled`) still validate and are stored and read as `paper`, `linen`, `parchment`, `directory`, `cocoa-kraft`, `graphite` and `midnight`, with no data migration. New `ui.theme_day` and `ui.theme_night` (defaults `paper`, `midnight`), `ui.list_density`, the spacing steps `dense`/`snug`/`standard`/`relaxed`/`airy` for `ui.reading_density`, the font Atkinson Hyperlegible Next, and the hidden `ui.device_defaults` (the account defaults of the `client.*` keys). A test keeps the Go scheme list in step with `web/src/theme/schemes.json`.

- Web UI, from real-device testing: one font choice in the Aa menu that applies everywhere except Settings and menus; pressed-style segmented controls that hold their place; "Text spacing" (was "Reading spacing"); a smooth new-articles pill; themed, longer-lasting toasts (info 8 s, undo 15 s, errors stay).
- Web UI: layouts renamed Editorial and Email - Compact (saved choices keep working), a star to set the device default layout, and an open article always keeps its list beside it on wide screens.
- Web UI: collapsible folders, a Favorites section (folders and feeds), a resizable sidebar and list column, an Article width setting, a Settings gear next to the list controls.
- Web UI, Manage Feeds: drag to reorder (with a Saved note), favorites, multi-select with bulk move and delete.
- Web UI: rows marked read leave the Unread list after 1.5 s (undo cancels), a Share button, and device settings for Open links in (same tab on iPhone, so app handoff leaves no blank page), the Unread badge (count, dot or off, capped at 99+), and single-key shortcuts (off by default on touch-first devices).

- Setting `fetch.fulltext_all` ("Fetch the full article for every feed", group Library, Settings screen, default off): every new article of every feed is extracted, whatever the feed's own full-text flag says. It needs no schema change: an item's effective full-text mode is now `COALESCE(items.fulltext_mode, CASE WHEN fetch.fulltext_all THEN 1 ELSE feeds.fulltext END)`, computed in one place (`store.EffectiveFulltext` / `store.FulltextModeSQL`) for the ingest pick, the guarded save, `POST /api/items/{id}/fulltext`, item detail, the Reader API hold and content, and bootstrap. Flipping it takes effect at once, without a restart, and never backfills old items (they extract on demand when opened). A per-article mode of on or off still wins, and a feed's own flag being off does not opt it out. Bootstrap feeds gain `fulltext_effective` (the feed flag, or true while the switch is on); `fulltext` stays the feed's own flag.
- Setting `library.favorites` (hidden, group Library, default `[]`): the sidebar favorites, stored server-side as an array of at most 500 `{"t":"folder"|"feed","id":"<digits>"}` objects, strictly validated (shape, kind, digit ids, no duplicates). Returned by `GET /api/settings` and bootstrap; a deleted folder or feed is dropped from it in the same transaction.

- Web UI: five list layouts (Magazine, Cards, Compact, Inbox, Headlines) with per-device, per-feed and per-folder choice; iOS Mail-style row swipes, long-press menu, swipe back, pull to refresh, a 15-second merging undo toast, and a full keymap with a shortcuts overlay.
- Web UI: click-to-load YouTube and Vimeo embeds, in-article footnote scrolling, newest/oldest order toggle, previous/next feed buttons.
- Web UI: settings screen rendered from the API metadata, the "Aa" reading menu (theme, font, size, density), an accessibility section (text size, easy-to-read font, reading spacing, reduce motion, large targets, read aloud, prefers-contrast and forced-colors support), and all bundled fonts.
- Web UI: feed add, edit (including changing a feed's URL), delete, folders, OPML import and export, a feed health view, password and API-password screens, and Export backup.
- Live updates: the event stream reconnects with backoff, polls while disconnected, resyncs on return, and a heartbeat watchdog detects a hung stream.

- `GET /api/feeds/{id}` returns the full feed detail (what PATCH returns; never the HTTP credentials),
  so the feed editor no longer sends a no-op PATCH to load it.
- `POST /api/reorder` sets folder and feed positions (and feed folder moves) for a whole list in one
  transaction; any bad id aborts it with nothing written. Replaces one PATCH per item when renumbering.
- SSE `heartbeat` event (`{t}`, no id) every 15 s next to the `: ping` comment, so `EventSource` clients
  can detect a hung stream.
- Web UI foundation: React shell with a phone tab bar and desktop panes, 20 reading themes with a
  follow-system day/night pair, the Magazine list, the article view, live updates over SSE. `/` now serves
  the app; the status page stays at `/_status`.
- Security headers on every response (`internal/httpx`): a strict Content-Security-Policy for pages (no
  inline script; `img-src` follows `imgproxy.mode`), `default-src 'none'` for API responses, plus
  `Referrer-Policy: no-referrer`, `Permissions-Policy`, `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy`,
  `nosniff`, and HSTS with `upgrade-insecure-requests` only when the effective scheme is https.
- UI API: items list (card lists), item detail, open, star, mark-read by scope, bootstrap read
  and live unread counts over SSE.
- `GET /api/items?order=oldest` lists oldest first (search too) with an order-tagged keyset cursor;
  a cursor from another ordering is rejected with 400, and old untagged cursors keep meaning newest first.
- `min_minutes` / `max_minutes` on `GET /api/items`: reading-time filters ("quick reads").
- Mark above/below: `POST /api/items/mark-read` scopes take `bound` (side, the list's order, an anchor
  `sort_at`/`id`, optional `inclusive`), `q` (search results) and reading-time limits; the `max_id`
  guard still keeps later arrivals out. The response adds `count` and `undoable`; above 10,000 changed
  ids the list is withheld and there is no undo. Bounded and filtered scopes leave the trimmed ledger alone.
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

- Backup export: `POST /api/backup` builds `kipple-backup-YYYYMMDD-HHMMSS.zip` (a consistent
  snapshot of the database, `feeds.opml`, `settings.json`, `manifest.json` with SHA-256 checksums,
  `RESTORE.txt`) and returns a single-use, 5-minute token; `GET /api/backup/{token}` streams it
  as an attachment (session and same-origin rule too), then deletes it. One export at a time and
  never alongside the nightly snapshot (`409 busy` with a retry hint); refused with `507` when the
  volume has under 2.2 times the database size free and with `413` above 4 GiB; temporary files
  are removed on completion, failure, expiry and startup. The response carries a `warning` about
  what the file contains (password hashes, the account secret, hashed session ids, feed logins).
  Fetch commits are not blocked: the snapshot only holds a read view.
- `kipple restore <backup.zip|kipple.db|-> [--yes]`: verifies the checksums, integrity and schema
  version (a newer schema is refused), keeps the current database under
  `backup/pre-restore-<timestamp>/` (newest 3), swaps in the backup, and signs every web session
  out. Without `--yes` it only verifies. It refuses while the server runs.
- `kipple password [--stdin]`: resets the web password (no-echo prompt, or one line on standard
  input; 5 to 256 characters), signs out every web session and revokes every Reader API token.
  Safe while the server runs.
- `kipple serve` holds an exclusive lock on `<data>/kipple.lock`; a second server on the same data
  directory refuses to start. The lock goes with the process, so there is no stale lock file.
- `docs/deploy.md`: where backups live, the export, password reset and restore runbooks, the
  extra steps for the phase 2 deploy (an off-box copy first) and how to roll back to phase 1.
- `GET /api/events` accepts `?last_event_id=` as well as the `Last-Event-ID` header, so a client that recreates its `EventSource` can still replay what it missed.

### Changed

- `greader.icon_urls` ("Send feed icons to sync apps") now defaults to ON. Existing databases have no stored row for it, so they pick the new default up at once (sync apps receive `iconUrl` once `KIPPLE_PUBLIC_URL` is set, and the unauthenticated icon endpoint is served); an explicitly stored `false` stays off. No migration.
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
- Known one-time effect after deploying phase 2: the ingest iframe pass (and the stored iframe sandbox) changes the stored HTML of existing items that contain a YouTube/Vimeo/other iframe the next time their feed is fetched, so their content and text hashes change once and Reader clients (Reeder, NetNewsWire) receive those items again as updated. Nothing is lost and it does not repeat; it is not suppressed on purpose, since the stored content really did change.
- The image-proxy signing secret is cached for one second instead of being read for every image in a list; a rotation still takes effect within that second.
- Internal: one outgoing User-Agent string, one control-character rule for titles, folder names and header values (tab allowed, as HTTP header values allow), the scheduler reuses the full-text runner's extractor type and host key, and `RecordStars` is part of the stats recorder interface.

### Removed

- `ui.line_height` and `ui.content_width`, replaced by `ui.reading_density`. Stored rows are
  deleted by migration 0002.

### Fixed

- Review of keyword filters (migration 0004, `internal/filter`, ingest, filters API):
  - A feed at its retention cap no longer trims the muted items its own fetch just added. Muted items now count against the cap but are kept under their own allowance (the newest `max(N/5, N - real)` of them, real items keep the rest), so the Muted view and delete-with-unmute keep working on busy feeds.
  - Deleting a filter un-mutes first and removes the row last (the rule is disabled meanwhile), the restore no longer depends on the request context, and a repeated `DELETE` finishes a cut-off one, including for an id whose row is already gone but that left muted orphans.
  - `?unmute=unread` restores only the items that were unread when they were muted (new `items.muted_was_read`, set at ingest and by a retroactive apply); items read earlier keep their read state and `read_at`. The aliases `?unmute=1` and `?unmute=true` now mean `read`, the safe mode.
  - A retroactive apply stops writing when its rule is deleted, disabled or edited: delete and patch cancel and wait for a running apply of that filter, and each write batch checks the rule is still stored, enabled and unchanged.
  - A rule that scans the `category` field never fires on an item without categories, so an inverted category rule no longer mutes every article fetched before categories were stored (ingest, preview and apply); the `category_new_items_only` warning says so.
  - The filter cache generation is bumped inside the write transaction, so a fetch commit taking the writer right after a filter write cannot use the stale rule set.
  - The full-text pre-pick predicts mutes with the same feed title fallback as the commit (the fetched document's title for a brand-new subscription).
  - Reader `disable-tag` repair: a correctly form-encoded label followed by a stray `&` is no longer glued into the folder name.
  - `GET /api/status` now carries the filter apply run in `runs` and the `muted` count.

- Deleting the archive feed while it holds starred items (`DELETE /api/feeds/{id}`) now answers 409 `archive_has_starred` (pass `?delete_starred=1` to really delete); a Reader `unsubscribe` of it is skipped, logged and still answers OK. Starred items are only ever destroyed by `delete_starred=1`.

- Unsubscribing a batch that included the hidden archive feed could delete it (and cascade away every archived starred item, including ones just moved into it). The archive feed is now processed last and skipped while it holds starred items (`UnsubscribeSkipped` reports it); `DeleteFeed` on it without `delete_starred` returns `ErrArchiveHasStarred`. Tests cover both batch orders, the archive alone and the `delete_starred` path.
- The nightly ledger purge used a fixed 180 days regardless of `retention.restore_days`, so a stub could vanish before its restore window ended if the setting grew. The horizon is now `max(180, restore_days + 7)` days, and `MaxRestoreDays <= LedgerDays` is a compile-time assertion.

- Sanitizer (review of phase 1): in-page links whose fragment held `&`, `'`, `(`, `)`, `*`, `+`, `,`, `;`, `=`, `@`, `/`, `?` (for example `#footnote's-1`) lost their href, because only a narrow set of fragments was shielded from the no-relative-URL policy. Every `#...` href is now kept, restored exactly. The shield placeholder carries a per-call random nonce, so a literal `https://fragment.kipple.invalid/#...` link in feed HTML is no longer rewritten to a bare fragment.
- Status page (`/_status.js`): when the event stream closes for good (a 401 after the session expires) it now says "events: signed out" and shows the sign-in form, or retries with backoff, instead of "reconnecting" forever; it shows liveness from the `heartbeat` event ("events: stalled" after 45 s of silence) and describes `counts`, `fulltext.ready`, `feed.changed` and `filters.changed` events.
- OPML import parsed every outline URL twice; `feedurl.KeyAndNormalize` parses once (`Key` and `Normalize` stay as wrappers).
- Reader API: NetNewsWire sends a folder id raw (`&` and `+` unencoded) only for `disable-tag`, so the repair now runs on the POST body of `disable-tag` alone; `subscription/edit`, subscribe and `rename-tag` parse exactly as in phase 1 (NNW encodes `&` and `+` there), so a stray `&` after a label can no longer create and file into a garbage folder. In `disable-tag` the glued tail stops at real parameter keys: identifiers, percent-encoded identifiers (`%54=`) and vendor keys with dashes or dots (`x-client=`, `client.id=`) are never swallowed; a name may start with `&` (`&Co`); merged names decode leniently (`My%20News&100%`); and it deletes only the folder that matches the fullest raw name (`AT&T` beats `AT`, a missing `AT&T` never deletes `AT`). Linear time, 64-part cap. Every other endpoint and every query string parses exactly as in phase 1. Internal: one `parseUserPath` replaces the duplicate label/state id parsers.
- Fetch pipeline (review of phase 1): a large feed committed in several chunks no longer shares one 10 s window across the chunks (each chunk gets its own budget); the backoff after a failed commit now escalates (in-memory count, capped like any failure backoff) instead of retrying at the first step forever; a UTF-32LE BOM is no longer mistaken for UTF-16LE (all UTF-32 and UTF-16 BOMs are decoded); the "response too large" message names the configured limit rather than always 10 MiB; `Cache-Control: private` now contributes no publisher refresh hint, as documented; the charset fixture generator no longer carries two no-op string replacements (fixtures unchanged).
- Undo of a mark-read after opening an article on a phone now brings the row back; a saved "shortcuts off" from a touch device no longer blocks the hardware-keyboard auto-enable.
- Press-and-hold drag on feed rows works on touch (needs real-device confirmation); keyboard and button reorder keep focus; a focused Settings switch stays in view.
- Favorites keep syncing after a rejected save (latest save wins; a failure reverts only favorites); column resizing is smooth, saves on release, and can't squeeze the article below 320 px; Editorial's row-height estimate follows the list width.

- Review fixes for the full-text and favorites backend: `fetch.fulltext_all` (and the image mode) can no longer be pinned to the wrong value by one cancelled request (a failed load is never cached and loads on a detached 5 s context); merging a label into another and deleting a label from a Reader client now drop its favorite like every other delete path; favorite ids must be positive int64s and are stored canonically (`"007"` is `"7"`, `"0"` is refused, repeats in either spelling are refused), and stored lists are normalised on read; an article whose fetch snapshotted the mode before the switch or feed flag changed is now queued (and left alone when turned off) according to the current mode; the Reader API now holds only articles that are really pending in the extraction pool, so deferred ones are served at once instead of waiting 30 s for text that never comes; with the switch on the per-fetch cap is 50 and the queue 2000 (still 4 at a time, 2 per host); `stream/items/contents` reads the switch once per request; the pool concurrency test no longer depends on sleep timing.
- A right swipe from an open trailing panel now closes it and commits Read/Unread instead of snapping back.

- Bulk mark (mark all, above/below) now uses the server's `as_of`, so it covers every unread item in oldest-first lists and with backdated new items; undo only touches what the server changed and restores ledger rows.
- Undo of merged swipe-stars reverts all of them; a failed undo no longer says "Undone".
- Unread badge no longer drops twice when opening an article; failed next-page loads keep the list; the skip link is visible on focus; toasts are announced to screen readers; pinch-zoom works from a list row.

- Nightly maintenance read the `tz` setting in its goroutine while the start time was taken in `Start`; a tz change right after start could pick a different baseline (flaky `TestTzChangeBeforeTheRunDoesNotSkipTheDate` in CI). The zone is now read in `Start` too.
- Nightly job after a time zone change: the last run is recorded as an absolute instant and read in the
  new zone's calendar, so moving `tz` to a zone further behind no longer skips about a day (the old zone's
  date string compared as "tomorrow"). A night missed while the server was down now runs 5 minutes after
  startup instead of on the first tick, so it no longer overlaps the startup fetch burst.
- `kipple restore` from a bare `.db` now applies a `-wal` beside it, so undoing a restore from a
  `pre-restore-*` copy no longer drops the transactions that were only in the WAL after an unclean stop.
- Two `kipple restore` runs within one second no longer share a `pre-restore-*` directory (`-2`, `-3`
  suffixes). Running restore as root warns and hands the database to the data directory's owner; the
  deploy guide says to run it as Kipple's own user.
- Image proxy links follow a rotated account secret: `kipple password` changes the secret from another
  process, and the running server used to keep signing and verifying image URLs with the old one.
- `POST /api/backup` is now an asynchronous job: it answers 200 with the token as before when the build
  finishes within about 5 s, otherwise `202 {job_id, status:"building"}`, and `GET /api/backup/jobs/{id}`
  reports `building`, `ready` (the token payload) or `failed`. The build no longer dies with the client
  connection or a Cloudflare 524 on a big database. Still one export at a time (409 busy).
- Backup exports: the download token now lives 5 minutes from when the export is ready; a build near
  the 10-minute allowance used to hand out a token that had already expired.
- `HEAD /api/backup/{token}` no longer spends the single-use token (405) and HEAD on the download
  routes gets the same-origin rule.
- Export snapshot files are owner-only: the `export/` directory is 0700 and the database copy 0600 from
  the moment it is created.
- `GET /api/items` now returns `as_of` (the committed max id, a string) on every response; clients send it as
  mark-read `max_id`. The highest id on the first page was wrong for oldest-first lists and for
  backdated new-feed items, so mark-all could miss items or sweep ones the list never showed.
- Bulk mark-read undo now restores the trimmed-ledger rows it read: the response carries `ledger_ids`, and
  `{ids, ledger_ids, read:false}` flips them back (flag only, no stub resurrected), so the Reader API
  no longer keeps seeing them read after an undo.
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
- A changed `imgproxy.mode` no longer leaves cached pages with the old CSP `img-src`: the mode is folded into the ETag of `/` and `/_status`, so the next load revalidates with a full 200 and the new policy (a 304 cannot carry headers).
- The `/_status` page uses the real feed status vocabulary (`dead`, `failing`, `erroring`, `throttled`, `redirecting`, `silent`, `archive`, `disabled`, `ok`) and the `redirect_pending` field, instead of the retired `migrated`/`gone`/`user` names; it also shows the disabled reason and a held host.
- Stored YouTube and Vimeo iframes carry `sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"` instead of bluemonday's empty `sandbox=""`, which showed a blank player in Reader clients (Reeder, NetNewsWire). A feed cannot widen the tokens (design 4.4 step 2a, 7.8).
- Serve-time HTML: an unclosed `<video src="http://...">` or `<audio>` keeps its "Open video"/"Open audio" fallback link (written at the end of the input); the id-reference attributes `headers`, `for`, `aria-describedby`, `aria-labelledby`, `aria-controls` and `usemap` are prefixed `kp-` along with the ids they point at, so table headers, labels and image maps keep working.
- Enclosure-only entries (podcast or photo feeds with no text) are no longer dropped as empty: they are kept, titled after the media file name (or the feed title), with the first media URL as their guid. Truly empty entries are still skipped and counted.
- Nightly maintenance follows the `tz` setting without doubling or skipping a day: the local date of the last run is kept (`sys.last_nightly_date`, survives restarts) and the job runs once per local date once 04:10 has passed. An unknown `tz` name keeps the previous zone and logs a warning instead of being treated as a change to UTC.
- A full refresh or trim requested for a feed whose fetch was still waiting for a worker could be lost when its host became rate-limited: the waiting fetch was dropped with the request's follow-up on it, and the API call timed out. A waiting fetch is now upgraded in place to a full refetch, and a fetch that anything is waiting on is never dropped.
- Refresh-all and import no longer count a feed as fetched when the job that was in flight for it was only a trim or a skip: the run now fetches it after that job.
- Adding a feed from the web works for sites that refuse Kipple's User-Agent (403, 406, Cloudflare challenge): discovery follows `fetch.user_agent_mode` and retries once as a browser, as the fetcher does. Full-text extraction does the same, and honors the per-feed User-Agent, the remembered browser fallback and `browser_always`, so a feed that only loads with a browser User-Agent no longer gets a permanent 403 on its article pages.
- A folder deleted while a feed is being added now answers `folder_not_found` (400) instead of a 500.
- The health view's `backup_bytes` counts the whole backup tree (`pre-restore-*` copies and the `export/` scratch files), not just the top-level files.
- A large fetch (over 500 items) cut short by a URL edit now reports exactly the items that committed and still queues full text for them; the remembered browser-User-Agent flag is bound to the URL that was fetched.
- SSE ids are strings (`run_id`, `run_ids`, `new_item_ids`), as everywhere else in the API.
- Serve-time HTML: inline event-handler attributes are recognized explicitly (`on` plus letters), so lookalike attributes such as `<details open>` are never touched.

### Security

- Open event streams end when their session is gone: "sign out other sessions" (password change, `kipple password`) and session expiry now close a stream at the next heartbeat instead of leaving it open.
- Changing a feed's URL to a different host clears its stored HTTP credentials unless the same edit sets new ones, so a Basic-auth password is never sent to a host it was not entered for.
- CI: the gitleaks download is verified against its published SHA-256, and every GitHub Action (including the Trivy image scan) is pinned to a commit SHA with the version in a comment; Dependabot keeps them current.

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
