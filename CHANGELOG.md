# Changelog

All notable changes to Kipple are documented here. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The project is now licensed under the Blue Oak Model License 1.0.0 (`LICENSE`), with a generated `THIRD_PARTY_NOTICES.md` (bundled fonts, Go and npm dependencies; `scripts/gen-notices.mjs`). Both files ship in the image under `/licenses/`.

## [0.2.0] - 2026-09-26

Everything since 0.2.0-alpha.2, including the alpha.3 and alpha.4 builds (both deployed to Host-A).

### Fixed

- The article list no longer overlaps its rows or clips their text after you open an article and go back, or after the first load: rows are re-measured once the layout settles, instead of keeping their estimated height.
- Live updates: a stream that opens and immediately drops (a proxy that accepts then resets) now reaches the polling fallback after two failures instead of resetting the count on every open.
- Live updates: no misleading "No new articles" toast when a refresh finishes whose start event was missed.
- Sync: the first-run migration of old device settings only sends values that differ from the defaults, so untouched settings no longer override a different server default.
- Favorites: pinning a 501st item now shows the limit message instead of silently dropping it.
- Filters: an inverted highlight rule is now rejected with a clear message instead of being saved and never showing.
- Sanitizer: a self-closing `<video src="http://...">` or `<audio>` no longer keeps its insecure source; it becomes an "Open video" link like the non-self-closing form.
- Thumbnails: a lossy WebP is priced only when its VP8 key frame is exactly the size `DecodeConfig` reported (the VP8X canvas); a frame of another size, or no key frame, is refused and the original served, instead of relying on the decoder's own check.
- Thumbnails: a lossless WebP's prefix code groups are priced at what `golang.org/x/image/vp8l` really allocates: 24 bytes per symbol instead of 16 (the code lengths and canonical codes were left out, about 20% short on group-heavy files), and groups that no tile uses but the decoder still reads are now counted (a 64x64 file naming group 2,599 allocated 33 MiB against an estimate of 1 MiB). The entropy image is decoded during the header walk (up to 65,536 tiles) so the group count is exact rather than bounded by the tile count, which also prices few-group files lower.
- Image proxy: a failure to record a revalidated original or thumbnail in the cache index is now logged at debug level instead of dropped silently.
- Web UI, lists: a relevance search with no hits shows the "No results" message instead of a lone "Best matches first" header; row height estimates follow a change of the text-size setting; remembered scroll positions are capped at 100 lists.
- Web UI, accessibility: turning off "Listen to articles" while an article is being read aloud now stops the speech.
- Web UI, settings: turning off "Titles only in lists" returns to the layout you had before, even after a reload (kept in this device's prefs).
- Web UI, OPML import: the result now lists feeds that were skipped (bad addresses), settings in the file that were not applied (private-network and certificate-check flags are never taken from an import) and settings with unusable values; merged folder names were shown as "[object Object]" and now read "news into News".
- Web UI, search: typing a character and deleting it again no longer turns an Entered search back into a wider "still typing" search.
- Web UI, themes: if the server narrows the offered themes and this device's stored theme is no longer among them, the picker shows the default theme selected (with a note) and the Day and Night lists show what is really in force.
- Web UI: the feed editor is loaded on demand from the Health screen too, and error codes are read through `ApiError`.

Phase 2 (reading UI: backend and web app) so far. Schema 4 and 5 (migrations 0004, 0005) are here, not in the alpha tags.

### Added

- API: optional `expect_total` in `POST /api/library/auto-read/run`: when the total recounted at run time exceeds it by more than `max(100, 10%)` the run answers `409 total_changed` `{total, expect_total, message}` and marks nothing (the frontend will send the total its preview showed, so running feed by feed cannot slip a moved library past the confirm rule).
- API: device-profile key `client.search_order` (`relevance|newest|oldest`, default `relevance`) so the search order syncs per device; an unusable items cursor (unparseable, the old relevance form, or from another ordering) is now `400 {"error":"bad_cursor","message"}` instead of `bad_request` so the client can restart the list; new SSE event `folder.changed {folder_id?}` after every folder mutation (web create, rename, reposition, delete, reorder, feed moves, OPML import, and the Reader API rename-tag, disable-tag, subscription edit and import).
- Web UI, search: results mark the words you searched for (in fallback mode, the words it fell back to) and show "No exact matches: showing partial matches" when only partial matches exist; sort by relevance, newest or oldest (synced per device through `client.search_order`); a search that matches too much shows the server's message; "Mark all results as read" for a submitted search; a syntax help popover.
- Web UI, saved searches: save the current search from the Search screen; they appear in the sidebar with unread counts, and Settings > Saved searches edits, deletes and reorders them.
- Web UI, auto-read: Settings > Library has "Mark old articles as read after…" with presets and each feed can set its own; a preview and a confirmed "Mark N older articles as read now" handle what is already older. Changing a setting never marks anything.
- Web UI, images: Settings > Images has a cache size preset row, a stats card and Clear image cache; feed health shows the image cache size. A browser the server could not register keeps its settings locally and no longer retries failing saves.
- Web UI: appearance settings sync per device to the server profile (with Retry on failure and a one-time migration of old local values); Settings > Devices (name, list, copy, make default, forget, reset).
- Web UI: Settings > Filters (create and edit with a live preview, apply to existing articles with progress, delete with a restore choice), the Muted view with Restore and Edit rule, "Mute similar…" from rows and articles, and keyword highlighting in lists and articles with a reading-menu toggle.
- Schema 4 (`0004_filters_devices.sql`), schema only (filters, muted items, devices and auto-read all use it now, see below): table `filters` (keyword rules: scope, kind, action, terms, fields, options, hit counters), `items.muted_by` with the partial index `idx_items_muted`, `items.muted_was_read` (the read state before a mute), `categories_json` on `item_content` and `trimmed_content`, `feeds.auto_read_days`, and table `devices` (per-device appearance profiles). Additive and O(1); the runner takes its usual pre-migration snapshot first, and an older binary refuses a schema-4 database. Rehearsed on a copy of the real phase 1 database: schema 1 to 4 in 0.3 s (5,600 items, 138 feeds, 52.9 MB snapshot), integrity and foreign-key checks clean, row counts unchanged. Trim and restore now copy `categories_json` through the retention stub.
- `internal/filter`, the keyword rules engine (wired into ingest and the filters API below): text rules (single words, phrases with any whitespace between the words, whole-word boundaries, case and diacritic folding, CJK as substrings) and RE2 regex rules over title, author, content, URL, categories and feed title; global, folder and feed scope; invert; deterministic precedence (star beats mute, mute implies read, lowest mute id wins). Bounded work: at most 200 rules, 25 enabled regex rules, 2,000 enabled text terms, regexes rejected when empty-matching or over 5,000 instructions, and scanned text truncated (content 32 KiB for text and 8 KiB for regex, other fields 4 KiB). A required-literal prefilter keeps case-insensitive regexes fast. Measured: 10,000 items x 50 rules in about 1.7 s; 25 regex rules on a full scan in about 8 ms per item. Table, fuzz (four targets, seeds committed) and benchmark tests.
- Keyword filters at ingest. New items are evaluated against the enabled rules in the fetch commit (no network, a cheap in-memory evaluation, the compiled set cached per filter-write generation, so an unchanged rule set costs one atomic load): `mute` marks the item read and sets `items.muted_by` to the lowest matching mute rule, `mark_read` marks it read, `star` stars it (and beats a mute), `highlight` is drawn by the client. Reeder and NetNewsWire need nothing: a muted item is an ordinary read item, so unread counts and `xt=read` agree with the web. Muted items are skipped by full-text extraction (never queued, never held by the Reader hold), left out of `fetch.done.new_item_ids` (which gains `muted_items`), and counted against the retention cap but kept under their own allowance (the newest `min(muted, max(N/5, N - real))` muted items; real items keep the rest). Each fetch_log row notes `filters: muted N, marked_read N, starred N`; rule hit counts count only matches whose action took effect. Item categories are now stored at ingest (`item_content.categories_json`, at most 20 of 100 runes) so `category` rules work for new items. With no rules nothing changes.
- Marking an item unread, or starring it (web or Reader API), clears its mute; a plain mark-unread is the restore.
- Filters API: `GET/POST /api/filters`, `PATCH/DELETE /api/filters/{id}` (validation errors are `400 {"error":"bad_filter","field","message"}`), `POST /api/filters/preview` (a bounded dry run with counts and up to 20 sample cards, no writes), `POST /api/filters/{id}/apply` (explicit retroactive apply as a run with `run.start`, `run.progress` and `run.done` events, writes in batches of 500 behind the commit gate) and `DELETE /api/filters/{id}?unmute=keep|read|unread` (restores what the rule muted, in batches). `POST /api/filters` accepts `apply_existing`. Filter changes publish `filters.changed`.
- `GET /api/items?view=muted` (the Muted view, cards carry `muted_by` and `muted_by_name`); `view=all` and search now exclude muted items; mark-read `scope.view:"muted"` is accepted. Bootstrap gains `counts.muted` and `highlights`; the `counts` event gains `muted`; `items.state` may carry `muted`.
- Per-device appearance profiles (backend plan step 12). The HttpOnly `kipple_device` cookie (128-bit random id, `SameSite=Lax`, `Secure` when https, 400-day `Max-Age`, never accepted by the Reader API and never required) is issued by the first bootstrap or device request and selects a row of the `devices` table; its `settings` object holds overrides only (at most 8 KB). The effective value of a key is the device override, else the account default (the `ui.*` settings row), else the built-in default. New endpoints (session and same-origin like the rest): `GET`/`PATCH /api/device`, `PUT /api/device/name`, `GET /api/devices`, `POST /api/device/copy-from/{id|defaults}`, `POST /api/device/make-default` and `DELETE /api/devices/{id}` (not the current one). `PATCH /api/device` validates device-scoped settings with the same validators as `PATCH /api/settings`, and the web client's former localStorage keys (layout, per-feed and per-folder layout overrides, order, article and sidebar widths, link target, unread badge, text size, spacing, motion, shortcuts, and so on, named `client.*`) with whitelisted validators; unknown keys, wrong types and out-of-range values are `400 invalid_settings`, and an over-size profile is `413`. Bootstrap gains `device` (`id`, `name`, `profile`, `merged`). `GET /api/settings` metadata gains `scope` (`global`, `device` or `both`). At most 50 devices (only devices unseen for 30 days are evicted, see Changed), and the nightly job purges devices unseen for 400 days. Layout ids stay `magazine` and `headlines` (labels Editorial and Email - Compact).
- Settings: `ui.theme` now takes the twenty round-2 scheme ids (plus `system`); the ids of the first draft (`white`, `off-white`, `sepia`, `soft-green`, `brown`, `dark`, `oled`) still validate and are stored and read as `paper`, `linen`, `parchment`, `directory`, `cocoa-kraft`, `graphite` and `midnight` (and the retired `fern` and `cocoa` names, read as `directory` and `cocoa-kraft`), with no data migration. New `ui.theme_day` and `ui.theme_night` (defaults `paper`, `midnight`), `ui.list_density`, the spacing steps `dense`/`snug`/`standard`/`relaxed`/`airy` for `ui.reading_density`, the font Atkinson Hyperlegible Next, and the hidden `ui.device_defaults` (the account defaults of the `client.*` keys). A test keeps the Go scheme list in step with `web/src/theme/schemes.json`.
- Auto-read after N days (backend plan step 17, F5). New setting `library.auto_read_days` ("Mark old articles as read after…", group Library, Settings screen, days, 0 to 365, default 0 = off) and a per-feed `auto_read_days` (`PATCH /api/feeds/{id}`: null inherits, 0 is off for the feed, 1 to 365; reported in bootstrap feeds and the feed detail; this is the first use of the column from schema 4). A new `auto_read` step in the nightly job marks read the unread, unstarred, unmuted, unheld articles whose crawl time crossed the threshold since the last run (the window `(lastRun - N days, now - N days]`, unix arithmetic, so DST changes nothing), plus the matching trimmed-ledger rows the Reader API still lists as unread. Each item crosses once, so a manual mark-unread sticks; a server that was down resumes from the last completed run; and switching the feature on never marks history by itself. Batches of 500 behind the commit gate, no stats, `items.state` (`source:"auto_read"`) and `counts` events. No schema change (the recorded run is the system key `sys.auto_read_last_run`).
- `POST /api/library/auto-read/preview` (`{feed_id?, days?}` gives per-feed and total counts of what a catch-up would mark, "what if" without saving) and `POST /api/library/auto-read/run` (an explicit catch-up of everything already older: `202` run with `run.start`/`run.progress`/`run.done` of kind `auto_read`, one at a time, `409 confirm_required` above 100 articles unless `confirm:true`, `409 busy`); the active run is in bootstrap and `/api/status` `runs`.
- Saved searches (backend plan step 17, F4). The hidden setting `library.saved_searches` (group Library, default `[]`, at most 100 `{id, name, q, scope?, order?}` with a feed, folder or view scope, strictly validated) and the routes `GET/POST /api/saved-searches`, `PATCH/DELETE /api/saved-searches/{id}` and `POST /api/saved-searches/reorder`, which edit the list atomically (server-made ids, no lost updates between tabs) and answer with a live unread count from the same search as `GET /api/items?q=`, computed lazily, capped at 999 (`unread_capped`), budgeted at 200 ms per search (`unread:null` when it ran out). `saved_searches.changed` SSE event; bootstrap gains `saved_searches`. Deleting a feed or folder drops the scope of any saved search that named it (the search stays, over the whole library).
- Device profile key `client.highlight_keywords` (bool, default true): a device can turn off the drawing of keyword highlights.
- Image cache (backend plan steps 13 and 14). Images now go through Kipple and are cached on disk, bounded: the new package `internal/imgcache` keeps one file per image under `<data>/imgcache/` with its own SQLite index (`index.db`, never the main database), a size cap with least-recently-used eviction down to 90% (setting `imgproxy.cache_mb`, default 1024, 0 turns it off, range 64 to 20480, a lower value evicts at once), expiry of images not seen for 60 days, remembered failures (404, 410, 403, unsupported type and oversize for 24 h; 5xx, 429 and timeouts for 10 minutes doubling to 24 h), a free-disk floor (nothing is written below max(2 GiB, 5% of the volume)), atomic writes, startup repair of orphan, missing and half-written files, and a rebuild of a corrupt index. The proxy tees a miss to the cache while streaming it (a partial or oversize body is never cached), serves hits from disk with `ETag`, `Last-Modified`, `Range` and `Cache-Control: private, max-age=2592000, immutable`, revalidates stale entries (a slow or failing source serves the stale copy), makes one upstream call per image however many clients ask (single flight), and limits fetches to 4 per host on top of the global 8. The nightly job sweeps the cache and vacuums its index (`imgcache_sweep`). Backups and snapshots never include it.
- Card thumbnails (backend plan step 15). List cards now load an 800 px wide version of their lead image: the proxy URL gains a signed flag bit (`FlagThumb`, value 4, so every URL signed before it still verifies) and the thumbnail is generated on demand from the cached original and stored as a separate cache entry that counts toward `imgproxy.cache_mb` and is evicted by the same least-recently-used rule. JPEG, PNG and static WebP over 800 px wide and 150 KB become a JPEG at quality 80, or a PNG when they have transparency (there is no pure-Go WebP encoder, so WebP is decoded, never produced); EXIF orientation is applied and all metadata dropped; nothing is upscaled. Animated GIF and WebP, AVIF, small images and anything that cannot be decoded are served as the original and remembered for a day, so they are not retried. One transcoder (2 workers, queue of 16) with a 24 megapixel header check before decoding, a per-decode memory ceiling and a shared decode budget; when the queue is full or a thumbnail takes over 4 s the original is served and the worker finishes for next time. Measured: a 12 MP photo of 3.4 MB becomes 54 KB in 0.2 s. Article bodies and the open article's lead image keep the original. `GET /api/imgcache` gains `thumbnails`. New dependency `golang.org/x/image` (`draw`, `webp`).
- Hotlink retries in the image proxy: a 401, 403 or 429 (with no `Retry-After` or Cloudflare challenge) is retried once with a plain browser `User-Agent` and no `Referer`, then once with the image's own origin as the `Referer`; Kipple's address, the article URL and cookies are never sent. The shape that worked is remembered per host.
- `GET /api/imgcache` (`enabled, mode, cache_mb, max_bytes, used_bytes, entries, neg_entries, hits, misses, evictions, failures, since, oldest_access_at, disk_free_bytes, disk_floor_bytes, low_disk`) and `POST /api/imgcache/clear` (`{cleared}`). Settings `imgproxy.cache_mb` ("Image cache size", MB) and `imgproxy.mode` ("Load images through Kipple") join the Settings screen (group `images`). `GET /api/health/feeds` now fills `db.imgcache_bytes`.
- Search with stemming (backend plan step 16). Schema 5 (`0005_fts_porter.sql`) rebuilds the FTS5 index with `tokenize = 'porter unicode61 remove_diacritics 2'` and a persistent `bm25(4.0, 2.0, 1.0)` rank (title 4x, author 2x, body 1x): `running` finds `run` and `runs`, accents fold both ways, and a title hit outranks repeats in the body. The five FTS triggers and the `item_search` view are unchanged. It runs once in `BEGIN IMMEDIATE` behind the pre-migration snapshot (a failure rolls back and the old index survives). Rehearsed on a copy of the real phase 1 database (5,600 items): the rebuild took about 0.6 s (linear in the text, so roughly 10 s at 100,000 items), the whole schema 1 to 5 open 0.95 s, integrity checks clean, every hit of the old index is still a hit. Porter also merges words that share a stem (for example `news` now also finds `new`).
- Search query syntax (`q` on `GET /api/items` and `scope.q` of mark-read, one shared builder): `"exact phrase"`, `-word` or `NOT word` to exclude (needs at least one positive term), `title:word` and `author:"jane doe"` column filters (those two columns only; any other `x:y` stays literal text), `word*` prefix. With `typing=1` (search-as-you-type) the last word (at least 3 runes, 2 for CJK) is a prefix while the text does not end in a space. User text still never reaches FTS5 outside quotes, so it cannot cause a syntax error or an unlisted column filter (fuzz test with seeds).
- Search fallback: a search with no exact match in its scope is retried once as an OR of prefix matches, and the response says so with `fallback: true` (every page of that result; `false` otherwise), so the UI can show "No exact matches: showing partial matches". Mark-read by search picks the same mode the list showed.
- Web UI: Settings ask before a lower image cache size (which purges) or a lower retention (which trims); segmented settings apply on Enter, Space or click, not on arrow focus.
- A basic `README.md` and the `docs/HF` human-feedback folder.

### Changed

- Web UI, Settings: a choice in a segmented control applies on Enter, Space or click, not on each arrow key. Lowering the image cache, turning it off, lowering the days restore stubs are kept (they are removed that night for good) and keeping fewer articles per feed each ask first, and Reset asks too when it lowers.
- Web UI, auto-read: the catch-up sends the total its preview showed as `expect_total` (Settings and the feed editor); a `409 total_changed` refreshes the number and asks again, on top of the existing client-side recount of a preview older than a minute.
- Web UI, search: highlighting follows the server's parser exactly; opening a saved search never uses the search-as-you-type match; "Mark all" while typing a search explains why it waits.
- Web UI, saved searches: counts no longer show a dash while loading; a failed count retries, then offers Try again.
- Search prefixes: the unfinished last word is a prefix only when the request says `typing=1` (`GET /api/items`, sent by the search box while the user types), or as an explicit `word*`. FTS5's porter tokenizer stems a prefix query too (measured on the real library: `"running"*` and `"run"*` both return 988 rows), so the old rendering `("w" OR "w"*)` meant `stem*` and widened every search (`apple` 518 items became 851, `police` 36 became 173). Saved-search unread counts and mark-read `scope.q` were inflated by it; both, and any request without `typing`, now use the plain stemmed words. Prefixes need at least 3 runes (2 for CJK), at most 3 per query, at most 12 terms; the partial-match fallback drops words under 3 runes.
- Search cost: `snippet()` and bm25 are computed for the returned page only (ids first, then the page is decorated), and a search has a 500 ms budget; past it `GET /api/items` answers `422 {"error":"search_too_broad"}`. Pathological queries (`a b c … p`, a typo plus one-letter words, `a* b* … p*`) took 1.6 to 3.3 s per page on the 5,600-item real library and now take about 1 ms.
- Mark-read `scope` accepts `fallback` (bool): the list's own `fallback` flag echoed back, so "mark all results read" marks the set the list showed even when an exact match arrived after the list was fetched. Without it the server probes again, as before.
- Device registration is guarded: at most 5 new devices per login session per day (then the session keeps its last device), concurrent first loads share one device, the 50-device cap only evicts devices unseen for 30 days and never the session's own, and when it cannot make room the client is served defaults as an unsaved device (empty `id`, no cookie) instead of evicting recent profiles. `TouchDevice` decides on the reader pool and takes the writer only when the daily update is due.
- `store.GlobalAutoReadDays` and `store.AutoReadLastRun` return an error (`AutoReadLastRun` gains a third result).
- Relevance cursors (`order=rank`) changed form with the new rank basis: cursors issued before this version are a 400 (start the search again). Date cursors are unchanged and every cursor now also records the fallback mode. `GET /api/items` always returns `fallback`.
- Search text is no longer trimmed of trailing spaces before it is parsed (a trailing space means "finished word": no prefix on the last word).
- `imgproxy.mode` now defaults to `all`: every image goes through Kipple, so the page policy needs no `https:` in `img-src`. A stored `imgproxy.mode` value (an existing deployment that chose `http_only`) is kept. With the cache on it no longer forwards a browser's conditional request headers to the source.
- Store internals (no behavior change): the gate + write transaction boilerplate of `CommitFetchError`, `CommitSkip` and `TrimOnly` is now the shared `gated`/`batch` helper; every JSON column and `json_each` argument is encoded by one helper (`jsonText`) that returns the marshal error; the two uid lookups of a fetch chunk (live items and ledger tombstones) are one `UNION ALL` query; feed URL normalization and key come from one parse (`feedurl.KeyAndNormalize`) in `AddFeed`, `ValidateFeedURL` and the redirect decision. Benchmarks added (`BenchmarkApplyItemsRefetch`, `BenchmarkUIDLookup`).

### Fixed

- Live updates: a stream that opens and immediately drops (a proxy that accepts then resets) now reaches the polling fallback after two failures instead of resetting the count on every open.
- Live updates: no misleading "No new articles" toast when a refresh finishes whose start event was missed.
- Sync: the first-run migration of old device settings only sends values that differ from the defaults, so untouched settings no longer override a different server default.
- Favorites: pinning a 501st item now shows the limit message instead of silently dropping it.
- Image proxy: every image response (cold miss, first view, cache disabled) now carries `Cross-Origin-Resource-Policy: same-origin`, not only cache hits.
- Image proxy: rotating the account secret closes the old image handler and its thumbnail workers instead of leaking them; the stale-revalidation bound now covers the whole hotlink retry ladder; learned host hints are evicted one at a time instead of all at once; card image flags are looked up once per feed.
- Image cache: evictions are cancelled by shutdown, and long stored URLs are cut on a character boundary so they stay valid UTF-8.
- `kipple restore -` (read the backup from standard input, as RESTORE.txt and the docs say) no longer fails with "unknown option". A restore whose swap fails no longer leaves an empty `pre-restore-*` directory, and empty ones no longer count toward the three kept, so they cannot push out a real safety copy.
- Status page: the event log names each run by its kind (a filter apply or auto-read run no longer shows as "refresh", and reports articles changed), and logs `folder.changed` and `saved_searches.changed`.
- Reordering saved searches quickly no longer loses moves.
- The search box no longer undoes a scope or sort change made while you type.
- The auto-read catch-up reports the true number marked, shows a run immediately, recounts a stale preview and refuses to mark more than the preview showed; the what-if number can no longer be used to mark articles.
- Settings changed while this browser is unregistered are kept and sent once it registers.
- Device settings changed in another tab or browser are no longer overwritten by a stale value on the next save; the Unread badge (Dot/Off) and "Highlight keywords" now sync with the device profile and survive a reload; the first-run migration no longer turns the account's "mark read on scroll" into a per-device override; settings the server refused clear once you change them and can be discarded.
- Filter names are limited by bytes (CJK), "Mute similar…" splits spaceless titles, delete and apply dialogs handle stale counts and a busy apply, and keyword highlights match the rule engine (accent folding, retry after a whole-word miss, scan window).
- The article toolbar fits 375 px: Open original and Mute similar… moved into a More menu (the `o` shortcut still opens the original); auto-read runs show a quiet status line.
- Card thumbnails could crash-loop the container: the decode-memory estimate (3 bytes per pixel for baseline JPEG, 8 for progressive and PNG) was far below what the decoders allocate (a 24 MP CMYK JPEG was admitted at 72 MiB and needs about 214 MiB; progressive 4:4:4 JPEG costs 15 bytes per pixel, progressive CMYK 24, 16-bit interlaced PNG 16), so an OOM kill recorded nothing and every list render retried it. The estimate is now read from the file's own headers (JPEG components, sampling, Adobe transform and progressive mode; PNG bit depth, color type, tRNS and interlace; WebP chunk types) and covers every allocation including the halving and encode buffers, with a 5/4 margin; tests build 24 MP worst cases and check the real allocation against it (1.27x to 1.46x headroom for JPEG and PNG). The per-transcode ceiling drops from 96 to 80 MiB and the shared budget from 128 to 96 MiB, so the transient heap stays under 100 MiB. Before a decode starts, an `in progress` marker is written on the thumbnail key (10 minutes, doubling per repeat, not counted as a failure); a process that dies mid-decode leaves it behind and the next start serves the original instead of retrying. Scaling no longer allocates a boxed color per pixel for transparent, CMYK, 16-bit, paletted or 4:1:1 sources (everything is scaled into premultiplied RGBA).
- The image cache's eviction and idle-expiry loops ignored a failing index (a full disk, an I/O error) and re-selected the same rows forever while holding the cache lock, freezing every image request. They now stop at the first error or a batch that frees nothing, honor cancellation, and warn at most once per back-off (1 minute, doubling to 1 hour).
- A slow client could permanently break an image: the 15 s upstream timeout also ran while the proxy waited on the client, so a phone on a slow link reading a large image cut the upstream mid-body on every attempt. The body is now read into the cache file at the source's speed and the client is served from the file as it grows (fixed memory; the entry is committed and the fetch slot and flight released as soon as the source is done), and the 15 s only counts time spent waiting on the source, also when the cache is off.
- A thumbnail request whose original could not be cached (disk guard, failed commit) fetched the original a second time to answer, and with no free fetch slot queued twice (20 s) before its 503. The original is now streamed to the client from the one fetch, and a failure is answered once.
- A stale image whose revalidation got a 304 after its file had been evicted answered 502; it now fetches the image once more, unconditionally.
- Revalidating a stale image replaced the good cached copy with a failure entry when the source answered a 4xx (including a hotlink ladder ending in 403) or a page that is not an image (a CDN soft 404). The stale copy is now served and kept, and the next revalidation is put off (10 minutes, doubling to 24 h, also after a 5xx or timeout).
- The first response for an image carried the source's `ETag` while every later cached response carried the cache's checksum tag, so browser revalidation never matched after the first fill. A response that fills the cache now sends only `Last-Modified` (which the cache also serves); hits keep the checksum tag.
- A panic in the thumbnail leader leaked its single-flight entry, so later requests for that image waited 4 s and never got a thumbnail; the flight is now released on every path.
- `GET /api/health/feeds` walked and stat-ed every image-cache file on each call; `db.imgcache_bytes` now comes from the cache's own byte counter plus its index files.
- Thumbnails, decode-memory model (adversarial re-review): the JPEG header walk stopped at the first scan and read a stray `FF 00` as a segment, so a file could show it a fake grayscale frame inside a comment while `image/jpeg` decoded the real progressive CMYK frame (a 4000x2666 file estimated at 37.7 MiB needs about 245 MiB to decode), and an Adobe marker after the first scan (which still turns on the RGB conversion) was not priced. JPEGs are now walked strictly from SOI to EOI and anything that is not a well-formed stream (stray bytes or `FF 00` between segments, a second frame header, unsupported markers or sampling, no scan, no EOI) is not thumbnailed; the size must also match `image.DecodeConfig`. Lossless WebP (the image or a compressed alpha plane) is walked through its transforms, and a file with meta prefix codes (up to 2,600 Huffman groups, about 200 MiB for an 801x1000 picture) or a prefix code the walk cannot follow exactly is not thumbnailed (the meta prefix code refusal was later relaxed: such files are now priced with an upper bound and refused only over the ceiling, see below); the transform sub-images, the color cache and the trees are now priced. Such images are served as the original and the refusal remembered for a day.
- An original served at a thumbnail URL (queue full, a slow transcode, a crashed one, low disk, a refusal) carried the thumbnail's `Cache-Control: private, max-age=2592000, immutable`, pinning the full original under the thumbnail URL for 30 days. It now carries `no-cache`, or for a refusal `private, max-age=` the time left on the refusal; only real thumbnails are immutable.
- A revalidation that got a chunked body over 15 MiB replaced the good cached copy with a failure entry; it now keeps the copy and puts the next revalidation off, like every other failed revalidation.
- A thumbnail whose `in progress` marker could not be written (a full disk, a failing index) was decoded anyway, without its crash protection; now it is not decoded and the original is served.
- Past 20,000 failure rows, pruning removed the earliest-expiring rows first, which are the `in progress` markers, silently resetting the crash protection. Markers now have their own cap (5,000, the oldest go first), and failures go expired first, then least recently replayed.
- With the image body streaming straight through (cache off, low disk, a failed cache write), a slow client held its fetch slots for as long as the server's write timeout allowed, so eight slow phones could starve every other image. An exchange now gives its slots back after the upstream timeout plus 30 s (`SlotHold`), cutting a client that has not taken the body by then; a client reading from the cache file is unaffected.
- The nightly auto-read step read a failed settings lookup as "off" or "never ran", so a transient read error produced an empty window that the recorded run then closed for good. It now fails the step and records nothing; the next night repeats the window.
- `feedurl.KeyAndNormalize` did not equal `Key(Normalize(x))` for a query ending in whitespace before a fragment (`http://h/p?a=b #x`) or an IPv6 zone that does not survive a re-parse; the key is now taken from the normalized string, and a differential fuzz test (seeds committed, 60 s run) keeps them equal.
- `TestRestoreRefusesBadInput` failed about one run in five: it flipped a raw byte of the zip, which can land in a compressed stream and leave the decompressed bytes unchanged. It now rewrites the `kipple.db` entry with one changed byte and the manifest's checksum stale (200 runs clean).
- `DB.HoldPending` encoded the pending full-text ids in map order, so its JSON array was not deterministic (the CI flake `TestHoldPendingEncoding` saw `[42,7]`). The ids are sorted before encoding.
- A failed settings read (cancelled context, disk I/O error) was taken for "not set". Inside a write transaction it now fails the transaction (`LoadFetchSettingsErr`): retention trimming, restore of trimmed items, the stub and ledger purges, the full-text guard in saves and mark-all-as-read, and `PullInSchedule` no longer act on a compiled-in default (a wrong retention cap or restore window) and the batch retries. Outside a transaction the loaders log a warning and use defaults for that pass only; nothing caches them. A JSON value that cannot be encoded now fails with its own error instead of tripping the `json_valid` CHECK.
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
- The hidden archive feed keeps its starred items: deleting it while it holds any (`DELETE /api/feeds/{id}`) answers 409 `archive_has_starred` (pass `?delete_starred=1` to really delete; `DeleteFeed` returns `ErrArchiveHasStarred`), and a Reader `unsubscribe` of it is skipped, logged and still answers OK, so the Reader API keeps the archive silently. Unsubscribing a batch that included it used to delete it (and cascade away every archived starred item, including ones just moved into it); it is now processed last and skipped while it holds starred items (`UnsubscribeSkipped` reports it). Starred items are only ever destroyed by `delete_starred=1`. Tests cover both batch orders, the archive alone and the `delete_starred` path.
- The nightly ledger purge used a fixed 180 days regardless of `retention.restore_days`, so a stub could vanish before its restore window ended if the setting grew. The horizon is now `max(180, restore_days + 7)` days, and `MaxRestoreDays <= LedgerDays` is a compile-time assertion.
- Sanitizer (review of phase 1): in-page links whose fragment held `&`, `'`, `(`, `)`, `*`, `+`, `,`, `;`, `=`, `@`, `/`, `?` (for example `#footnote's-1`) lost their href, because only a narrow set of fragments was shielded from the no-relative-URL policy. Every `#...` href is now kept, restored exactly. The shield placeholder carries a per-call random nonce, so a literal `https://fragment.kipple.invalid/#...` link in feed HTML is no longer rewritten to a bare fragment.
- Status page (`/_status.js`): when the event stream closes for good (a 401 after the session expires) it now says "events: signed out" and shows the sign-in form, or retries with backoff, instead of "reconnecting" forever; it shows liveness from the `heartbeat` event ("events: stalled" after 45 s of silence) and describes `counts`, `fulltext.ready`, `feed.changed` and `filters.changed` events.
- OPML import parsed every outline URL twice; `feedurl.KeyAndNormalize` parses once (`Key` and `Normalize` stay as wrappers).
- Reader API: NetNewsWire sends a folder id raw (`&` and `+` unencoded) only for `disable-tag`, so the repair now runs on the POST body of `disable-tag` alone; `subscription/edit`, subscribe and `rename-tag` parse exactly as in phase 1 (NNW encodes `&` and `+` there), so a stray `&` after a label can no longer create and file into a garbage folder. In `disable-tag` the glued tail stops at real parameter keys: identifiers, percent-encoded identifiers (`%54=`) and vendor keys with dashes or dots (`x-client=`, `client.id=`) are never swallowed; a name may start with `&` (`&Co`); merged names decode leniently (`My%20News&100%`); and it deletes only the folder that matches the fullest raw name (`AT&T` beats `AT`, a missing `AT&T` never deletes `AT`). Linear time, 64-part cap. Every other endpoint and every query string parses exactly as in phase 1. Internal: one `parseUserPath` replaces the duplicate label/state id parsers.
- Fetch pipeline (review of phase 1): a large feed committed in several chunks no longer shares one 10 s window across the chunks (each chunk gets its own budget); the backoff after a failed commit now escalates (in-memory count, capped like any failure backoff) instead of retrying at the first step forever; a UTF-32LE BOM is no longer mistaken for UTF-16LE (all UTF-32 and UTF-16 BOMs are decoded); the "response too large" message names the configured limit rather than always 10 MiB; `Cache-Control: private` now contributes no publisher refresh hint, as documented; the charset fixture generator no longer carries two no-op string replacements (fixtures unchanged).
- Thumbnails: the decode-memory model refused most libwebp lossless files and most lossy+alpha files (the compressed alpha plane is itself a lossless stream that libwebp writes with meta prefix codes): 16 of 179 real WebP files were wrongly refused. Meta prefix codes are now priced with an upper bound (the entropy image walked, at most one group per tile of it, capped at 2,600, times the per-group tree cost) and refused only over the decode ceiling; the 2,600-group 801x1000 crafted file is still refused, a few groups are admitted, and the memory tests gained multi-group cases proving estimate >= real allocation. Now 0 of the same 179 are refused.
- Auto-read: a nightly run that failed or was cut short at feed k left `sys.auto_read_last_run` alone although feeds 1 to k-1 had committed, so the next night repeated their window and marked read again an article the reader had marked unread in between. Each feed now keeps its own high-water mark (`sys.auto_read_feed_marks`, no schema change), cleared when a run completes.
- Auto-read: the nightly step took two commit-gate write transactions per enabled feed even when nothing qualified (about 276 for 138 feeds). A feed with nothing to mark now costs two reader-pool probes and no gate.
- `PATCH /api/settings` replacing or resetting `library.saved_searches` published no `saved_searches.changed`, and stored a scope naming a feed or folder that does not exist; it now publishes and answers `400 invalid_settings` for such a scope.
- Saved searches: a feed or folder deleted between the endpoint's scope check and the commit could leave a dead scope; new or changed scopes are now checked inside the write transaction.
- Saved searches: exactly 999 unread showed as "999+"; the cap now applies only when there is more.
- Saved searches: names and search text now also reject U+0085 and the other C1 control characters, U+2028 and U+2029 (they were the only line-break characters let through).
- Image cache: pruning ran only when failures plus markers passed 25,000, so `in progress` markers could grow past their 5,000 cap while failures were few, and failures past 20,000 while markers were few; each count is now checked against its own cap.

## [0.2.0-alpha.2] - 2026-09-25

Phase 2 alpha 2: real-device testing fixes and the second review round. Schema 3 (migrations 0001-0003). Deployed to Host-A.

### Added

- Web UI, from real-device testing: one font choice in the Aa menu that applies everywhere except Settings and menus; pressed-style segmented controls that hold their place; "Text spacing" (was "Reading spacing"); a smooth new-articles pill; themed, longer-lasting toasts (info 8 s, undo 15 s, errors stay).
- Web UI: layouts renamed Editorial and Email - Compact (saved choices keep working), a star to set the device default layout, and an open article always keeps its list beside it on wide screens.
- Web UI: collapsible folders, a Favorites section (folders and feeds), a resizable sidebar and list column, an Article width setting, a Settings gear next to the list controls.
- Web UI, Manage Feeds: drag to reorder (with a Saved note), favorites, multi-select with bulk move and delete.
- Web UI: rows marked read leave the Unread list after 1.5 s (undo cancels), a Share button, and device settings for Open links in (same tab on iPhone, so app handoff leaves no blank page), the Unread badge (count, dot or off, capped at 99+), and single-key shortcuts (off by default on touch-first devices).
- Setting `fetch.fulltext_all` ("Fetch the full article for every feed", group Library, Settings screen, default off): every new article of every feed is extracted, whatever the feed's own full-text flag says. It needs no schema change: an item's effective full-text mode is now `COALESCE(items.fulltext_mode, CASE WHEN fetch.fulltext_all THEN 1 ELSE feeds.fulltext END)`, computed in one place (`store.EffectiveFulltext` / `store.FulltextModeSQL`) for the ingest pick, the guarded save, `POST /api/items/{id}/fulltext`, item detail, the Reader API hold and content, and bootstrap. Flipping it takes effect at once, without a restart, and never backfills old items (they extract on demand when opened). A per-article mode of on or off still wins, and a feed's own flag being off does not opt it out. Bootstrap feeds gain `fulltext_effective` (the feed flag, or true while the switch is on); `fulltext` stays the feed's own flag.
- Setting `library.favorites` (hidden, group Library, default `[]`): the sidebar favorites, stored server-side as an array of at most 500 `{"t":"folder"|"feed","id":"<digits>"}` objects, strictly validated (shape, kind, digit ids, no duplicates). Returned by `GET /api/settings` and bootstrap; a deleted folder or feed is dropped from it in the same transaction.

### Changed

- `greader.icon_urls` ("Send feed icons to sync apps") now defaults to ON. Existing databases have no stored row for it, so they pick the new default up at once (sync apps receive `iconUrl` once `KIPPLE_PUBLIC_URL` is set, and the unauthenticated icon endpoint is served); an explicitly stored `false` stays off. No migration.

### Fixed

- Live updates: a stream that opens and immediately drops (a proxy that accepts then resets) now reaches the polling fallback after two failures instead of resetting the count on every open.
- Live updates: no misleading "No new articles" toast when a refresh finishes whose start event was missed.
- Sync: the first-run migration of old device settings only sends values that differ from the defaults, so untouched settings no longer override a different server default.
- Favorites: pinning a 501st item now shows the limit message instead of silently dropping it.
- Undo of a mark-read after opening an article on a phone now brings the row back; a saved "shortcuts off" from a touch device no longer blocks the hardware-keyboard auto-enable.
- Press-and-hold drag on feed rows works on touch (needs real-device confirmation); keyboard and button reorder keep focus; a focused Settings switch stays in view.
- Favorites keep syncing after a rejected save (latest save wins; a failure reverts only favorites); column resizing is smooth, saves on release, and can't squeeze the article below 320 px; Editorial's row-height estimate follows the list width.
- Review fixes for the full-text and favorites backend: `fetch.fulltext_all` (and the image mode) can no longer be pinned to the wrong value by one cancelled request (a failed load is never cached and loads on a detached 5 s context); merging a label into another and deleting a label from a Reader client now drop its favorite like every other delete path; favorite ids must be positive int64s and are stored canonically (`"007"` is `"7"`, `"0"` is refused, repeats in either spelling are refused), and stored lists are normalised on read; an article whose fetch snapshotted the mode before the switch or feed flag changed is now queued (and left alone when turned off) according to the current mode; the Reader API now holds only articles that are really pending in the extraction pool, so deferred ones are served at once instead of waiting 30 s for text that never comes; with the switch on the per-fetch cap is 50 and the queue 2000 (still 4 at a time, 2 per host); `stream/items/contents` reads the switch once per request; the pool concurrency test no longer depends on sleep timing.
- A right swipe from an open trailing panel now closes it and commits Read/Unread instead of snapping back.

## [0.2.0-alpha.1] - 2026-09-25

Phase 2 alpha 1: web UI, review fixes, backup export and migrations 0002-0003 (schema 3). An intermediate deploy to Host-A.

### Added

- Web UI: five list layouts (Editorial, Cards, Compact, Inbox, Email - Compact; ids `magazine` and `headlines` unchanged) with per-device, per-feed and per-folder choice; iOS Mail-style row swipes, long-press menu, swipe back, pull to refresh, a 15-second merging undo toast, and a full keymap with a shortcuts overlay.
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
- Security headers (`internal/httpx`): a strict Content-Security-Policy for pages (no
  inline script; `img-src` follows `imgproxy.mode`), `default-src 'none'` for API responses, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` and HSTS (https only) on every response, `Permissions-Policy` and
  `Cross-Origin-Opener-Policy` on pages, `Cross-Origin-Resource-Policy: same-origin` on `/api/` and `/img/`, and `upgrade-insecure-requests` only when the effective scheme is https.
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
- FTS5 item search with safe query building, rank keyset cursors and snippets; `POST /api/maintenance/fts-rebuild` (a manual
  index rebuild).
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
  delete, `POST /api/archive/purge-unstarred`, refresh, mark-fetch-read, `POST /api/feeds/{id}/trimmed-unread/reset`, fetch log and folder endpoints.
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
- Settings `greader.ot_includes_user_changes`, `greader.subscribe_fetch_now` (both stored and validated, not yet read by any code: reserved) and `ui.*` defaults.
- `GET`/`PATCH /api/settings` now describe every user-visible key for the UI: label, one-sentence
  help, group, kind, options, range, step, unit and surface (`reader_menu`, `settings` or
  `hidden`), plus the current value and default. The response is `{settings: [...], values: {...}}`.
- `ui.reading_density` (`compact`, `comfortable`, `relaxed`; later extended, see `ui.theme` under Unreleased) with its CSS mapping (line height and
  column width) served in the option metadata.
- `oled` theme (true black, for battery savings on OLED screens; now an alias of `midnight`).
- `fetch.user_agent_mode`: `default`, `browser_on_failure` (the default) or `browser_always`.
  With `browser_on_failure` a feed that answers 403/406 (or a Cloudflare 503 challenge) is retried
  once with a browser User-Agent; if that works the feed remembers it (`feeds.ua_fallback`,
  migration 0002) and uses the browser UA from then on. A per-feed User-Agent still wins.
- Reader API full-text hold: a new item still pending in the full-text extraction pool (effective mode on) is left out of `stream/items/ids`,
  `stream/contents`, `stream/items/contents`, `unread-count` and the default `mark-all-as-read`
  until its extraction finishes (a result or a stored error) or 30 s have passed since its
  crawl time (capped at 60 s, inside the `ot` slack, so a client that synced meanwhile still gets
  it next time). Decided in SQL, so paging is exact. The web UI is not held. No setting.
- Cards and item details carry `origin_title` (the feed an archived starred item came from) and `source` (`origin_title`, else the feed title), so unsubscribed starred items show where they came from.
- Startup self-check: the store probes JSON1 and FTS5 (porter over unicode61 with diacritic folding, `snippet`, `bm25`, `rank`) in the temp schema before migrating and refuses to start, naming every missing feature, so a driver swap cannot silently lose search.
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
- Web UI: mark read on scroll (setting `ui.mark_read_on_scroll`), a warnings banner for the bootstrap `warnings` (clock skew, failing or stale snapshot, over 10,000 unread), and a first-run screen with Add feed and Import OPML.

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
- Known one-time effect after deploying phase 2: the ingest iframe pass (and the stored iframe sandbox) changes the stored HTML of existing items that contain a YouTube/Vimeo/other iframe the next time their feed is fetched, so their content and text hashes change once and Reader clients (Reeder, NetNewsWire) receive those items again as updated. Nothing is lost and it does not repeat; it is not suppressed on purpose, since the stored content really did change.
- The image-proxy signing secret is cached for one second instead of being read for every image in a list; a rotation still takes effect within that second.
- Internal: one outgoing User-Agent string, one control-character rule for titles, folder names and header values (tab allowed, as HTTP header values allow), the scheduler reuses the full-text runner's extractor type and host key, and `RecordStars` is part of the stats recorder interface.
- Web UI: Settings, Feeds, Health and the feed dialogs load on demand (main bundle 483 kB, 154 kB gzip, was 672/209).
- CI: the web job runs lint, tests, the build, a theme contrast check and `npm audit --omit=dev --audit-level=high`.
- `POST /api/backup` is now an asynchronous job, and `GET /api/backup/jobs/{id}` is new: it answers 200 with the token as before when the build
  finishes within about 5 s, otherwise `202 {job_id, status:"building"}`, and `GET /api/backup/jobs/{id}`
  reports `building`, `ready` (the token payload) or `failed`. The build no longer dies with the client
  connection or a Cloudflare 524 on a big database. Still one export at a time (409 busy).

### Removed

- `ui.line_height` and `ui.content_width`, replaced by `ui.reading_density`. Stored rows are
  deleted by migration 0002.

### Fixed

- Live updates: a stream that opens and immediately drops (a proxy that accepts then resets) now reaches the polling fallback after two failures instead of resetting the count on every open.
- Live updates: no misleading "No new articles" toast when a refresh finishes whose start event was missed.
- Sync: the first-run migration of old device settings only sends values that differ from the defaults, so untouched settings no longer override a different server default.
- Favorites: pinning a 501st item now shows the limit message instead of silently dropping it.
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
- Full-text extraction is capped at 4 articles at once across all workers (`Options.FulltextGlobal`, the extraction pool size),
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

[Unreleased]: https://github.com/WPTK/Kipple/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/WPTK/Kipple/compare/v0.2.0-alpha.2...v0.2.0
[0.2.0-alpha.2]: https://github.com/WPTK/Kipple/compare/v0.2.0-alpha.1...v0.2.0-alpha.2
[0.2.0-alpha.1]: https://github.com/WPTK/Kipple/compare/v0.1.0...v0.2.0-alpha.1
[0.1.0]: https://github.com/WPTK/Kipple/releases/tag/v0.1.0
