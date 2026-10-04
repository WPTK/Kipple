# Kipple design (2026-09-24, revision 2: after red-team review)

This is the source of truth for how Kipple works today; it was first written as a design and has been kept up to date as the code changed. Files it cites by name without a path in this repository (`greader-freshrss.md`, `netnewswire.md`, `go-libraries.md`, the round-2 research) are research notes kept outside the public repository.

This design starts from **fetch-first**, which two of the three judges picked. It adds the grafts the judges named and fixes every defect they found. Revision 2 applies the red-team review; §12 gives the disposition of every issue. Where the judges disagreed, the resolution is stated in §1. Three points also correct the brief and all three input designs, based on primary sources in the research files:

- **`ot` semantics.** FreshRSS's `ot` filter is `id >= ot·1e6 OR lastModified >= ot`, where `lastModified` means the server changed the content. User read/star changes are a separate column (`lastUserModified`) that `ot` never consults (greader-freshrss.md §8, verified in `EntryDAO.php`). Kipple follows the verified behaviour (§3, §6).
- **`application_id`.** 'KIPL' is `0x4B49504C` = **1263095884**. All three designs had a wrong constant.
- **NetNewsWire batches status changes.** NNW queues read/star changes locally and sends them in batches of up to 1000 on each sync, roughly every 120 s while the app is active (netnewswire.md, edit-tag). No id-count threshold separates reads from bulk marks. Revision 2 therefore turns API read inference **off by default** (§8).

**What was validated, and on what.**

- *Revision 1:* the DDL and the query plans in §2.3 ran on SQLite 3.50.4 with FTS5, using 140 feeds and 35,000 items for the planner checks and 150,000 items for the `ot` timing. That was the wide `items` table.
- *Revision 2:* the revised DDL ran on an in-memory SQLite 3.50.4 database. The checks were:
  - FTS5 external content over a view: `MATCH`, `snippet()` and `integrity-check`.
  - The split FTS triggers. A markup-only rewrite of 200 items left `items_fts_data` unchanged at 21 rows.
  - The retention trim with restore stubs. 300 items, 5 starred, N=50 gave 245 trimmed, 55 kept, 245 ledger rows, 245 stubs and a trimmed-unread counter of 245.
  - Restoring a starred ledger id. The item and its FTS row came back, and the stub was removed.
  - Archive-on-unsubscribe. Starred items survived; ledger rows and stubs cascaded away.
  - Deleting the default folder is refused, and a second archive feed is refused.
  - Both `ot` legs with an absent `c` bound to MaxInt64, with plans checked before and after `ANALYZE`.
  - `PRAGMA foreign_key_check` returned nothing.
- *Production differs.* The pinned `modernc.org/sqlite` v1.59.x ships **SQLite 3.53.4** compiled with STAT4 (go-libraries.md §2). The plan-regression suite must therefore run on the pinned driver, with and without `ANALYZE` (§10).
- *Not re-measured:* the 150k-row `ot` timings. The split table can only make them faster.

---

## 1. Decisions and rationale

Each item gives the decision, the reason, and the alternative that was **rejected**.

1. **Spine: fetch-first's scheduler.** One dispatcher goroutine owns all scheduler state (in-flight set, per-host counts, pending FIFO, host deadlines, runs). Workers talk to it only through channels. This is race-free by construction.
   - Every reply channel is `make(chan T, 1)`.
   - Every send the dispatcher makes is non-blocking, so a handler that has given up can never wedge scheduling.
   - The dispatcher keeps draining `doneCh` until the last worker has exited (§4.10).
   - *Rejected:* api-first's per-host map and simplicity-first's in-flight set, which workers mutated with no stated synchronization (Judge 2 flagged both as plausible data races).

2. **Three `*sql.DB` pools on one file, one write helper, and a one-slot commit gate.**
   - **Writer pool.** `SetMaxOpenConns(1)` and `_txlock=immediate`. Only `store.WithWrite(ctx, fn func(ctx context.Context, tx *sql.Tx) error)` can reach it. Every store write method takes a `*sql.Tx` (or a `Querier`) and never the `*sql.DB`, so nothing inside a write transaction can ask the pool for a second connection. Each write transaction (acquisition, `fn` and commit) has a 10 s context deadline. On timeout it logs an ERROR naming the current holder (the helper records its caller's file:line and function).
   - **Reader pool.** `SetMaxOpenConns(4)`, `SetMaxIdleConns(4)`, `SetConnMaxIdleTime(0)`, `query_only`.
   - **Snapshot pool.** `SetMaxOpenConns(1)`, not `query_only`. It is opened only for `VACUUM INTO` (the nightly snapshot, the pre-migration snapshot and the backup export), because `query_only` rejects that statement.
   - **Commit gate.** `store.DB` owns a `chan struct{}` of capacity 1, taken inside the store before the `BEGIN` of every fetch-path write (each commit chunk, the error and skip rows, `trim_only`) and of every maintenance batch; the workers never touch it. API writers skip the gate and queue on the pool. `database/sql` does not hand a freed connection to waiters in FIFO order, so without the gate 8 queued fetch commits could keep winning over a Reeder `edit-tag`. With the gate, an API write waits for at most one feed commit.
   - *Rejected:* "pool alone" (no ordering guarantee), and a dedicated writer goroutine fed with closures (more code, same effect).

3. **Driver: `modernc.org/sqlite` v1.59.x (SQLite 3.53.4, STAT4), with `modernc.org/libc` pinned to exactly the version in its go.mod** (v1.75.7 today).
   - FTS5 and JSON1 are compiled in, it is CGO-free, and it builds a static binary for distroless.
   - Every connection sets `analysis_limit=400`. The writer runs `PRAGMA optimize=0x10002` once when it opens, as SQLite recommends for long-lived connections.
   - *Rejected:* `ncruces/go-sqlite3`, the documented fallback (FTS5 needs per-connection registration, and wasm2go builds are heavy). *Rejected:* `mattn/go-sqlite3` (CGO).

4. **Item id = crawl time in microseconds**, from a process-monotonic allocator called *inside* the commit transaction, oldest item first. One integer is then the rowid, `timestampUsec`, the `crawlTimeMsec` source, the continuation cursor, the `ot`/`nt` key and the mark-all `ts` cutoff.
   - **Seeding.** The allocator seeds from a high-water mark persisted in every fetch commit, together with `MAX(items.id)` and `MAX(trimmed_items.id)`. It no longer scans `stats_events`.
   - **Clock check.** At startup it logs an ERROR and shows a health-view banner when the seed is more than 1 h ahead of the wall clock (§3).
   - *Rejected:* AUTOINCREMENT plus a separate `crawled_at` (two keys, composite cursors). *Rejected:* FreshRSS's temp-table renumbering.

5. **Inbound id parsing: a value containing `/` is always hex.** Otherwise: a leading `-` means decimal; all digits with no leading `0` means decimal; anything else is hex. This is correct whatever the id's magnitude. *Rejected:* the decimal-unless-leading-zero rule applied to the long-form suffix. *Rejected:* Miniflux's `len==16` heuristic.

6. **`ot` means `id >= (ot − 120 s)·1e6 OR content_changed_at >= ot − 120 s`.** It covers arrival time and server-side content edits, which is FreshRSS ≥ 1.29 semantics.
   - Both legs get the 120 s visibility slack (BazQux uses 180 s). The legs stay disjoint because leg 2 is bounded by `id < ot_us`.
   - An absent `c` binds to MaxInt64 when descending and to 0 when ascending, never to NULL.
   - Read/star changes are **not** included. Both clients pull the full unread and starred lists without `ot`. Including them would make Reeder's `s=read&ot=now-30d` list grow with every bulk mark. The verified FreshRSS source does not include them either.
   - The `greader.ot_includes_user_changes` setting (default `false`) adds user state changes to leg 2: with it on, the `ot` query also returns items whose read or starred state changed since `ot − 120 s`, in either direction (read, unread, star, unstar). The time comes from `items.state_changed_at` (migration 0007), not `read_at`/`starred_at`, which an unread or unstar clears. Every state change after ingest stamps it with the server's time (§6.7); ingest's initial state never does. With the setting off the query, its plan and its `INDEXED BY` are exactly as before.
   - *Rejected:* `user_modified_at` in `ot`. *Rejected:* published-date `ot` (Miniflux), which causes NNW's documented sync gap.

7. **Continuation uses an n+1 lookahead.** `continuation` is emitted only when `n+1` rows come back, so the last page never carries one. *Rejected:* `LIMIT n` emitting on every full page.

8. **The `subscription/list` and `tag/list` ETag is `sha256` of the rendered body**, truncated to 16 hex. It cannot go stale. *Rejected:* trigger-driven version counters and call-site bumps.

9. **Every feed is in exactly one folder.** `feeds.folder_id` is `NOT NULL DEFAULT 1`, and folder 1 is "Uncategorized", undeletable and unique. NNW's FreshRSS profile disallows root-level feeds and feeds in several folders. OPML imports that put a feed in several folders keep the first and **report** the rest (decision 34). *Rejected:* a nullable `folder_id`.

10. **Reader API auth: a stateless HMAC token over a separate API password hash.** `token = username + "/" + hex(HMAC-SHA256(secret, "greader-token-v1|" + api_password_hash))`: 64 hex, never contains `=`.
    - Password verification is argon2id with **m=19 MiB, t=2, p=1** (the OWASP minimum), for both the web and the API password.
    - A **global semaphore of 1** serializes every verification. The wait is bounded at 5 s, after which the request gets a 401.
    - A successful verification is remembered in memory, per kind (`api` or `web`), as `HMAC(secret, "login|" + kind + "|" + hash + "|" + password)` and compared in constant time, so the owner's clients re-running ClientLogin cost no hashing. The stored hash is part of the MAC, so a changed password never matches an old memo; a changed account secret drops every memo.
    - The password is **always** verified: ClientLogin never answers 401 for a password it did not check, except when a bounded wait runs out. Only failures are counted, and over budget attempts are paced (they wait), not refused (§6.3).
    - The UI generates a 24-character random API password by default.
    - *Rejected:* API session rows. *Rejected:* reusing the web password on the public path. *Rejected:* a global failure lockout on ClientLogin, because an attacker could then lock the owner out; the semaphore already caps the global hash rate and memory. The web login is paced the same way (§7): a wrong password is slowed per client, never refused.

11. **Retry-After host deadlines live in memory**, owned by the dispatcher. *Rejected:* a `host_backoff` table.

12. **No `fetch_runs` or `client_seen` tables.** Runs are in-memory dispatcher state. `fetch_log.trigger` records why each fetch happened. Last-seen per client family lives in a mutex-guarded map inside `internal/greader`, updated at most once a minute per family and read by the health handler. It no longer goes through a channel to the dispatcher, so a busy dispatcher can never stall request handlers.

13. **Redirects.** A same-host `http→https` upgrade with an identical path migrates immediately. Any other all-permanent (301/308) chain migrates after 3 consecutive identical observations.
    - The migration is refused when it would downgrade to http, or when `store.FindFeedByURL` finds another feed at the target.
    - `feeds.url_key` and `feeds.url_original_key` hold the scheme-less normalized form, so every subscribe path (quickadd, `ac=subscribe`, OPML import, `POST /api/feeds`) matches an existing feed whether it was given the old http URL or the migrated https URL.
    - *Rejected:* Feedbin's 6-day wait, and never migrating.

14. **Dedup key: guid, then raw link, then content hash, with a per-feed manual override** (`link`, `link_title`). An override change re-keys the feed through an explicit, user-confirmed "rekey" fetch. GUID-churn and duplicate-guid detection only flag the problem; nothing is automatic. *Rejected:* automatic GUID relink, the auto-mark-read flood guard, and "accept the wall of unread".

15. **Content updates are detected by `content_hash`; text changes by `text_hash`.**
    - When `content_hash` changes, the row is updated in place and read/starred state is kept.
    - `content_changed_at` is set only when `text_hash` (title plus plain text) changes. `text_hash` is a narrow column, so the comparison never reads article bodies.
    - The UPDATE's SET list names only the columns that changed.
    - The FTS triggers carry `WHEN old.x IS NOT new.x` guards, so markup-only churn never touches the index (validated).
    - *Rejected:* insert-only.

16. **Retention ranks by `sort_at = min(published_at, crawl_second + 86400)`**, then `id`: the UI order. The trim is a window query, `row_number() OVER (PARTITION BY muted_by IS NOT NULL ORDER BY sort_at DESC, id DESC)`, over the feed's unstarred, unretained items, so muted and real items are kept against separate allowances (the muted one is at least N/5). Items with `retain_until > now` are exempt; they come from a mark-unread restore (§5). *Rejected:* pure arrival id.

17. **Trimmed ledger plus restore stubs.**
    - A trim writes the ledger row (id, feed, uid, read) and, for `retention.restore_days` (default **90**, matching NNW's article window), a `trimmed_content` stub that holds everything needed to rebuild the item.
    - `a=starred` on a ledger id that still has a stub, a web star on a trimmed card, or `r=read` / mark-unread on one **restores** the row into `items` with the same id and the new state, and deletes the ledger row. The id then appears in the next starred or unread list, so the client's reconcile keeps the change instead of undoing it.
    - A mark-unread restore sets `retain_until = now + 7 d`, so the item is not trimmed straight away.
    - After the stub window a ledger id is a tombstone only: a star is ignored and OK is returned (§11).
    - The ledger insert uses `ON CONFLICT (feed_id, uid) DO UPDATE`. Rows are deleted with the feed. A ledger row is purged after its uid was **last seen** in the feed document. The purge horizon is `max(180, restore_days + 7)` days, so a restore stub (it cascades from its ledger row) can never be purged inside its `restore_days` window; `MaxRestoreDays <= LedgerDays` is asserted at compile time and in a test.
    - *Rejected:* "ignore stars on ledger ids" (revision 1; the red team showed both clients silently undo it). *Rejected:* a crawl-age grace window (7 days untrimmed). It breaks "newest N" on busy feeds and still misses NNW's 90-day window. *Rejected:* soft-deleting inside `items`, which would add `AND trimmed_at IS NULL` to every partial index and query.

18. **Trim runs in every successful fetch commit** (`ok`, `unchanged`, `not_modified`). Changing a feed's `retention`, or `retention.default`, also enqueues a `trim_only` job for the affected feeds. So does "Apply retention now". Trim never runs on page load or on API traffic. On a quiet feed it costs one indexed statement over ≤ N+starred narrow rows. *Rejected:* "only when the fetch inserted items" (revision 1). Lowering N on a quiet feed then did nothing for days.

19. **Full text lives in its own `item_fulltext` table** and can be toggled per feed (`feeds.fulltext`) and per article (`items.fulltext_mode`). For full-text feeds, the new items are committed first and then extracted **after the commit in a bounded background pool** (§4.3): up to 20 newest new items per fetch, 10 s each, 4 at once overall, 2 per article host, through a 500-item queue (while `fetch.fulltext_all` is on: 50 per fetch and a 2000-item queue). Items not queued are extracted on demand from the UI. *Rejected:* extracting inline in the worker before the commit (a slow article host held a fetch worker for up to 60 s and made `POST /api/feeds/{id}/refresh` answer 202 pending). *Rejected:* a column on `items`.

20. **The image proxy is UI-only.**
    - Route `/img/{sig}/{flags}/{b64url}`, HMAC-signed over flags and URL, and session-gated. `flags` carries `allow_private_net` from the item's feed.
    - It streams the image and never buffers it whole. A declared `Content-Length` over the cap returns 502 before any bytes are sent. An undeclared overflow aborts the connection.
    - AVIF is sniffed by its ISO-BMFF `ftyp` brand.
    - The rewrite happens at serve time. Stored content and Reader API content keep the original absolute URLs.
    - *Rejected:* rewriting stored HTML, and any proxy under `/api/greader.php`.

21. **Stats.**
    - There is one `open` kind for every client. `client` carries web, pwa, reeder, netnewswire, unread or api, and `inferred=1` marks API-derived opens.
    - **API inference is off, and not built.** The setting once reserved for it, `stats.api_single_read_is_open`, was removed in 0.6.0 (no code read it), and nothing writes an API-derived `open` row (`inferred=1`). The only stats the Reader API writes are stars (`RecordStars`, from `edit-tag`). The design would turn it on only if the day-1 capture showed that Reeder's scroll marks can be told apart from article opens.
    - Every phase-4 view defaults to `inferred = 0`. Streaks and feeds-never-opened exclude inferred rows unless the toggle is on.
    - `Recorder.Record(tx, …)` takes the caller's write transaction, so a stats row commits atomically with the state change and can never deadlock the one-connection pool.
    - *Rejected:* a separate `read` kind. *Rejected:* the ≤ 10-id threshold. *Rejected:* inference on by default (the red-team blocker).

22. **UI streaming: SSE with a polling fallback** (`GET /api/status`). *Rejected:* WebSockets.

23. **Web auth: a local argon2id password plus an HttpOnly, persistent `kipple_session` cookie**, and a same-origin check on every state-changing request.
    - Every state-changing cookie request must carry `Sec-Fetch-Site: same-origin`, or, if that header is absent, an `Origin` that matches the request host.
    - It must also carry `X-Kipple-Client`, which forces a CORS preflight. The `sendBeacon` stats path is exempt from the header only.
    - `SameSite=Lax` alone is not enough, because every other service on Host-A, and every other `*.example.com` host, is same-site.
    - Accepting the Access JWT is optional and off by default. *Rejected:* relying on Access alone.

24. **Unsubscribe deletes the feed but not its starred items.**
    - The starred items are re-parented to a single hidden **archive feed** ("Unsubscribed (starred)", `disabled_reason='archive'`, never fetched, retention unlimited). It is created lazily and **never listed** (the owner's rule, 2026-09-29: an unsubscribed feed shows up nowhere): not in the web sidebar or any web feed list or picker, not in `subscription/list` or `unread-count`, not in the OPML export, and no folder counts or lists its items (it sits in the default folder only because every feed row needs one). One SQL condition, `store.listedFeedSQL` (not being deleted and not the archive feed), decides which feeds are listed; the web applies the same rule to a cached bootstrap with `visibleFeeds`. Its starred items stay reachable: the Starred view, the All and Unread lists, search, and the Reader API starred and reading-list streams, always under their original feed's name (`origin_title`). A Reader API client sees those items with an `origin.streamId` that is not one of its subscriptions and no `label/` category; a client that only shows starred items of its subscriptions may not show them.
    - Everything else cascades.
    - The web UI dialog shows the starred count and offers "also delete N starred items".
    - Unsubscribing the archive feed is a real delete only when it holds no starred items. In a batch it is processed last (after the other feeds have moved their starred items in) and skipped while it still holds starred items; the web `DELETE /api/feeds/{id}` on it is refused with `ErrArchiveHasStarred` unless `delete_starred=1`, which is the only way starred items are ever destroyed.
    - Stats rows keep their snapshots either way.
    - *Rejected:* a pure hard delete (revision 1: one swipe destroyed saved articles). *Rejected:* soft-deleting every feed.

25. **Migrations are hand-rolled.**
    - SQL files are embedded, `PRAGMA user_version` is the cursor, and `PRAGMA application_id` guards against a foreign file.
    - A `VACUUM INTO` snapshot is taken before migrating an existing database. It goes to a **timestamped** target after `os.MkdirAll`, and the newest 3 are kept.
    - `PRAGMA foreign_keys=ON` is restored in a `defer` on every path and verified before the pool is handed out.
    - *Rejected:* goose and golang-migrate.

26. **Routing.**
    - A plain front `http.Handler` claims every Reader API path (`/api/greader.php…`, `/reader/api/0/…`, `/accounts/ClientLogin`). It collapses repeated slashes, strips repeated `/api/greader.php` prefixes, and dispatches with its own switch. **No ServeMux ever sees a Reader path**, so ServeMux's automatic 301 for `//` paths cannot drop a POST body.
    - Everything else goes to a stdlib `net/http` ServeMux (Go ≥ 1.22 patterns).
    - *Rejected:* chi, echo, and an inner ServeMux for the Reader API.

27. **Reader API error policy.**
    - Write endpoints always return 2xx `OK`, even for unknown ids.
    - Auth failures return 401 with both bad-token headers.
    - Kipple never answers 429 and never returns 5xx for client data. A genuine database failure, such as a write-acquisition timeout, returns 500.
    - An unknown endpoint gets `200 []` and a WARN log. An unknown stream gets an empty 200.
    - A write that parsed zero ids from a non-empty body logs a WARN with the UA and Content-Type.

28. **Form parsing is custom.**
    - The Content-Type is parsed with `mime.ParseMediaType`, ignoring parameters. Any non-multipart POST body is treated as urlencoded; `subscription/import` reads OPML instead.
    - The parser splits on `&` only, then on the **first** `=`, and keeps repeated keys **and the raw undecoded text**.
    - `disable-tag` name tails are glued back in the parser (§6.2): NNW sends that folder id unencoded (netnewswire.md §4).
    - *Rejected:* `r.ParseForm`.

29. **Schema style.** Tables are STRICT, booleans are INTEGER with CHECKs, `*_at` columns are unix seconds, and `items.id`/`trimmed_items.id` are unix microseconds. Text fields served to clients are `NOT NULL DEFAULT ''` where the wire format needs a string.

30. **`items` is split into a narrow row and `item_content`.**
    - `items` holds ids, state, dates, hashes, url, title, author and image. It is about 200–400 bytes and never overflows.
    - `item_content` holds `content_html`, `content_text` and `enclosures_json`.
    - FTS5 indexes a view that joins the two.
    - A read, star or `read_at` change (NULL → integer changes the record size, which rewrites the whole record) now rewrites one small leaf cell, not an article's overflow chain.
    - The trim and Reeder's `s=read&ot=now-30d` scans stay in narrow pages.
    - *Rejected:* only reordering columns plus an `idx_items_read` index. That still rewrites the whole record on every read change.

31. **All content URLs are absolute at ingest.**
    - Before sanitizing, every `href`, `src`, `srcset` candidate, `poster`, and audio/video `src` is resolved. The base is `xml:base` (gofeed), then the item link, then the feed `site_url`, then the final feed URL. The item link itself resolves against the feed's final URL.
    - Readability output is resolved against the article URL.
    - bluemonday runs with `RequireParseableURLs(true)` and `AllowRelativeURLs(false)`, so anything still relative is dropped.
    - Both FreshRSS (SimplePie) and Miniflux do this, so clients have never had to resolve relative URLs from those servers. Resolving against the account origin would hit Access.

32. **No JSON `null` in any Reader API response** except `LSID` in JSON ClientLogin. Every field that may be unknown is emitted as `""`, like Miniflux and FreshRSS: `iconUrl`, `htmlUrl`, `origin.htmlUrl`, `alternate[].href`, `canonical[].href` and `author`. Reeder is closed source and has broken on field types before (FreshRSS #3247, #2620). A golden-test rule enforces this.

33. **API subscribe paths and outbound HTTP** (default unchanged, opt-in added in phase 3). The brief says API clients never trigger fetches.
    - **Behaviour** (`greader.subscribe_fetch_now` off, the default): `quickadd`, `subscription/edit ac=subscribe` and `subscription/import` do **no** outbound HTTP and no discovery. They insert the feed with `next_fetch_at = now` and return at once. The scheduler's next tick (≤ 30 s) fetches the feed like any other due feed.
    - The first NNW or Reeder sync after that shows the feed title as its host and no items. The next sync fills both in.
    - The revision-1 behaviour (synchronous discovery plus a priority first fetch that waits up to 8 s) is **not built**. `greader.subscribe_fetch_now` (default off) turns on a bounded synchronous first fetch for a feed a client just added, in place of the scheduler wake: 8 s in total per request, however many feeds it subscribes, and it also ends on shutdown. With it off the default above applies.
    - The web UI's add-feed and OPML import are not API clients and keep synchronous discovery and runs.

34. **OPML fidelity.**
    - **Titles.** On import, a non-empty OPML title goes to `custom_title`. After the first successful fetch it is cleared if it equals the document title, so later title changes in the feed still flow through.
    - **Order.** Folder and feed `position` come from document order.
    - **Tree.** Nested outlines are nested folders, both ways: import keeps every named outline as a folder at its level (at most 8 levels), and export writes the folder tree as nested outlines, each named by its own name. OPML 2.0 nests natively, so no `kipple:` attribute carries the tree.
    - **Export.** `COALESCE(custom_title, title)` goes out as both `text` and `title`. Folders are nested outlines only; the comma-separated `category` attribute is never used. Disabled and gone feeds are included; the archive feed is not.
    - **Overrides.** Per-feed overrides are written as `kipple:` namespaced attributes and read back on import. `http_auth` is never exported.
    - **Import report.** The response lists every URL matched to an existing feed, every dropped extra folder membership, every folder merged with a sibling by case-insensitive name, and every folder that could not be created (its feeds go into the nearest folder above it that was).
    - **Existing feeds.** An import leaves a feed that already exists in its folder, unless the user asks for "move feeds that already exist into the file's folders" (`move_existing`). That option is how a library imported flat gets its tree back from the original file; folders it empties are reported, not deleted, since deleting a folder deletes its filters.
    - **Initial read state.** The UI import optionally marks items published more than N days ago as read on each new feed's first fetch. This is an explicit user choice made at import time.

35. **Memory budget: Go heap plus SQLite heap plus runtime, under 100 MB.**
    - `GOMEMLIMIT=64MiB`. modernc allocates SQLite's heap outside the Go heap, where GOMEMLIMIT does not reach it.
    - SQLite page caches: 16 + 4×4 + 2 MB = 34 MB worst case for the main database. The image-cache index (`imgcache/index.db`, when the cache is enabled) has its own writer and 4 readers at 2 MB each, up to 10 MB more, which this total excludes.
    - About 10 MB of runtime overhead.
    - A compose `mem_limit: 256m` backstop.
    - `stream/items/contents` and `stream/contents` stream their envelope row by row and never build the whole `[]item`. `i=` is capped at 1000 per contents request.
    - Exports page by keyset and never hold a read transaction across network writes.

---

## 2. SQLite schema

### 2.1 Connection setup (DSN, not DDL)

**Writer pool** (`SetMaxOpenConns(1)`, `SetMaxIdleConns(1)`, `SetConnMaxLifetime(0)`), reachable only through `store.WithWrite`:

```
file:/data/kipple.db?_txlock=immediate
  &_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)
  &_pragma=foreign_keys(ON)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-16000)
  &_pragma=journal_size_limit(67108864)&_pragma=mmap_size(0)&_pragma=analysis_limit(400)
```

After opening, the writer runs `PRAGMA optimize=0x10002` once, then checks that `PRAGMA foreign_keys` returns 1.

**Reader pool** (`SetMaxOpenConns(4)`, `SetMaxIdleConns(4)`, `SetConnMaxIdleTime(0)`, `SetConnMaxLifetime(0)`): the same pragmas, plus `_pragma=query_only(1)` and `cache_size(-4000)`.

**Snapshot pool** (`SetMaxOpenConns(1)`): the same pragmas except `query_only`, with `cache_size(-2000)`. It is opened for the nightly snapshot, the pre-migration snapshot and the backup export, used for `VACUUM INTO`, and closed after each. `VACUUM INTO` holds only a read snapshot on the source, so it blocks no writers.

**Page caches.** The worst case is 16 + 16 + 2 = 34 MB for the main database, plus up to 10 MB for `imgcache/index.db` when the image cache is on (its own writer and 4 readers at 2 MB each). This memory lives outside the Go heap (decision 35).

**`store.WithWrite(ctx, fn)`:**

1. Derives a context with a 10 s deadline, calls `db.BeginTx`, and runs `fn(ctx, tx)`. The deadline covers begin, `fn` and commit.
2. Commits when `fn` returns nil, and rolls back otherwise.
3. Records the caller (one frame from `runtime.Caller(1)`: file, line and function, plus the start time) in an atomic "holder" slot. A timed-out acquisition logs that slot at ERROR.
4. Every `*sql.Rows` opened inside `fn` is closed by `defer` in the store method that opened it. `UPDATE … RETURNING` goes through a helper that scans all rows and closes before returning.
5. While a maintenance job that holds the writer past the 10 s deadline runs (only the FTS rebuild, up to 45 s), it returns `store.ErrMaintenance` at once instead of waiting; a wait that times out because such a job started meanwhile ends with it too. The API answers it `503 {error:"maintenance"}` and the Reader API `503`, both with `Retry-After: 15`. Writers that take the commit gate first never see it: they wait on the gate, which the rebuild holds.

**Other connection details:**

- Call `sqlite.OFDLocking()` before the first open.
- `wal_autocheckpoint` stays at 1000 pages.
- The database lives on the named volume `/data`, which is local disk (never NFS or SMB).
- The temp table `temp.trim_set` is created with `CREATE TEMP TABLE IF NOT EXISTS` at the start of each trim, so a replaced writer connection is harmless.
- **Startup self-check** (`internal/store/selfcheck.go`, before migrations): on the writer connection, in the temp schema only, the store probes JSON1 (`json_valid`, `json_each`) and FTS5 (a `porter unicode61 remove_diacritics 2` table, `snippet`, `bm25`, `rank`). Any failure aborts startup with one error naming every missing feature, so a driver swap can never silently lose search. Tested against a stub driver that has neither.

### 2.2 DDL — `internal/store/migrations/0001_init.sql`

This block is exactly `0001_init.sql`, the schema as first shipped. Later migrations are listed in §2.2a; do not edit this block to match them. Three comments inside it are stale: sessions slide over 90 days (`sessionTTL`), not 30; the `ui.line_height` and `ui.content_width` settings it names were replaced by `ui.reading_density` (migration 0002); and its settings key list is incomplete (see the paragraph after the block). Two further comments are out of date. The `trimmed_items` comment says `last_seen_at` gates the 180-day purge; the horizon is `max(180, restore_days + 7)` days (§5). The `fetch_log.note` list names a `fulltext: <ok>/<tried>` note that no longer exists; the notes written today are `redirect_migrated: <old> -> <new>`, `redirect_target_owned_by_feed <id>`, `retry_after=<s>s`, `guid_churn_suspected`, `guid_duplicates: <k>/<n>`, `rekeyed: <k>`, `initial_read: <k>`, `filters: muted <k>, marked_read <k>, starred <k>`, `skipped: host retry-after until <RFC3339 UTC>`, `skipped_malformed_items: <k>/<n>`, `fulltext_picked: <n>`, `fulltext_deferred: <m>`, `fulltext: skipped (re-key pending)` and `fulltext: skipped (could not check existing items)` (joined with `; `).

```sql
-- Kipple schema v1. Applied by the migration runner inside BEGIN IMMEDIATE; the runner then sets
-- PRAGMA user_version = 1. Conventions: *_at = unix SECONDS; items.id / trimmed_items.id = unix
-- MICROSECONDS (crawl time). Booleans are INTEGER 0/1 with CHECKs. Strings served on the wire are
-- NOT NULL DEFAULT '' so no JSON null can leak (decision 32).
PRAGMA application_id = 1263095884;   -- 'KIPL' = 0x4B49504C

-- Single-user key/value settings. Values are JSON. Defaults live in Go; a row exists only for
-- overridden keys. User keys (whitelisted for PATCH): refresh.interval_minutes, retention.default,
-- retention.restore_days, fetch.user_agent, fetch.honor_publisher_ttl, greader.icon_urls,
-- greader.ot_includes_user_changes, greader.subscribe_fetch_now, stats.api_single_read_is_open,
-- imgproxy.mode, tz, ui.* (theme, font_body, font_ui, font_size, line_height, content_width,
-- layouts, mark_read_on_scroll, ...). System keys (never PATCHable): sys.id_high_water (JSON
-- integer, allocator high-water mark), sys.last_snapshot_at, sys.last_snapshot_error.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL CHECK (json_valid(value)),
  updated_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT, WITHOUT ROWID;

-- Exactly one row. password_hash = web login; api_password_hash = Reader API password (NULL =
-- Reader API disabled). Both argon2id PHC strings with m=19456 KiB, t=2, p=1. secret = 32 random
-- bytes hex: keys the Reader token HMAC, the login memo and image-proxy signatures. Written by Go
-- on first start from KIPPLE_USERNAME / KIPPLE_PASSWORD (/ KIPPLE_API_PASSWORD), or by the setup
-- wizard (0.5; 0.7 asks for no setup code). Migration 0010 rebuilds this table to add auth_mode ('standard' | 'open') and
-- created_via ('env' | 'wizard'); see internal/store/migrations/0010_setup.sql.
CREATE TABLE account (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  username          TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64
                                         AND username NOT GLOB '*[^A-Za-z0-9._-]*'),
  password_hash     TEXT NOT NULL,
  api_password_hash TEXT,
  secret            TEXT NOT NULL CHECK (length(secret) = 64),
  created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at        INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- Web UI sessions only (Reader tokens are stateless). id = hex sha256 of the cookie value; the
-- cookie itself is never stored. 30-day sliding expiry, purged nightly.
CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  user_agent   TEXT,
  remote_ip    TEXT
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- Nested folders (see 2.2a; the migration is 0012). parent_id NULL = top level; names are unique among siblings
-- (idx_folders_sibling_name); deleting a folder deletes its subfolders. Reader labels are flat full paths such as
-- "Tech/Apple". Row 1 is the undeletable default and stays at the top level. AUTOINCREMENT: ids are never reused.
-- position = OPML document order.
CREATE TABLE folders (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  parent_id  INTEGER REFERENCES folders(id) ON DELETE CASCADE,
  name       TEXT NOT NULL COLLATE NOCASE CHECK (length(trim(name)) > 0),
  position   INTEGER NOT NULL DEFAULT 0,
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  created_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;
CREATE UNIQUE INDEX idx_folders_one_default ON folders(is_default) WHERE is_default = 1;
INSERT INTO folders (id, name, is_default) VALUES (1, 'Uncategorized', 1);
CREATE TRIGGER folders_keep_default BEFORE DELETE ON folders WHEN old.is_default = 1
BEGIN SELECT RAISE(ABORT, 'the default folder cannot be deleted'); END;

-- One row per subscription; also all fetch state and health. AUTOINCREMENT: clients cache feed/<id>.
-- url = current fetch URL (rewritten on confirmed permanent redirect; url_original = first URL).
-- url_key / url_original_key = scheme-less normalized form (lowercase host, default port and
-- trailing '#fragment' stripped, path and query kept) used by store.FindFeedByURL so http/https
-- variants and pre-migration URLs match. host = lowercase hostname of url (per-host limiter key).
-- title = from the feed document; custom_title = user rename or OPML title (display =
-- COALESCE(custom_title, title)). disabled_reason 'archive' marks the one hidden archive feed that
-- holds starred items of unsubscribed feeds (url 'kipple:archive', never fetched, retention 0).
-- initial_read_before: set by a UI OPML import option; new items published before it are inserted
-- read on the first successful fetch, then it is cleared. trimmed_unread_count: unread items
-- trimmed since trimmed_unread_since, excluding items trimmed by the transaction that inserted them.
CREATE TABLE feeds (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  folder_id            INTEGER NOT NULL DEFAULT 1 REFERENCES folders(id) ON DELETE SET DEFAULT,
  url                  TEXT NOT NULL UNIQUE,
  url_original         TEXT,
  url_key              TEXT NOT NULL,
  url_original_key     TEXT,
  host                 TEXT NOT NULL,
  title                TEXT NOT NULL DEFAULT '',
  custom_title         TEXT,
  site_url             TEXT NOT NULL DEFAULT '',
  description          TEXT NOT NULL DEFAULT '',
  position             INTEGER NOT NULL DEFAULT 0,
  enabled              INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  disabled_reason      TEXT CHECK (disabled_reason IN ('user','gone','archive')),
  interval_minutes     INTEGER CHECK (interval_minutes IS NULL OR interval_minutes BETWEEN 5 AND 10080),
  retention            INTEGER CHECK (retention IS NULL OR retention IN (0,50,100,250,500,1000)), -- NULL inherit, 0 unlimited
  fulltext             INTEGER NOT NULL DEFAULT 0 CHECK (fulltext IN (0,1)),
  dedup_mode           TEXT NOT NULL DEFAULT 'auto' CHECK (dedup_mode IN ('auto','link','link_title')),
  rekey_pending        INTEGER NOT NULL DEFAULT 0 CHECK (rekey_pending IN (0,1)),
  initial_read_before  INTEGER,
  user_agent           TEXT,
  http_auth            TEXT,                 -- 'user:pass' for Basic auth; single-user box, stored plain, never exported
  ignore_http_cache    INTEGER NOT NULL DEFAULT 0 CHECK (ignore_http_cache IN (0,1)),
  disable_http2        INTEGER NOT NULL DEFAULT 0 CHECK (disable_http2 IN (0,1)),
  allow_insecure_tls   INTEGER NOT NULL DEFAULT 0 CHECK (allow_insecure_tls IN (0,1)),
  allow_private_net    INTEGER NOT NULL DEFAULT 0 CHECK (allow_private_net IN (0,1)),
  etag                 TEXT,
  last_modified        TEXT,
  body_hash            TEXT,
  next_fetch_at        INTEGER NOT NULL DEFAULT 0,
  last_fetch_at        INTEGER,
  last_success_at      INTEGER,
  last_new_items_at    INTEGER,
  last_error_at        INTEGER,              -- kept after a success; resolved when < last_success_at
  last_error_class     TEXT,
  last_error           TEXT,
  last_status          INTEGER,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  current_delay_s      INTEGER NOT NULL DEFAULT 0,
  ttl_hint_s           INTEGER,
  redirect_to          TEXT,
  redirect_kind        TEXT CHECK (redirect_kind IN ('permanent','temporary')),
  redirect_count       INTEGER NOT NULL DEFAULT 0,
  trimmed_unread_count INTEGER NOT NULL DEFAULT 0,
  trimmed_unread_since INTEGER,
  created_at           INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at           INTEGER NOT NULL DEFAULT (unixepoch()),
  CHECK (disabled_reason IS NULL OR enabled = 0),
  CHECK (disabled_reason IS NOT 'archive' OR retention = 0)
) STRICT;
CREATE INDEX idx_feeds_due          ON feeds(next_fetch_at) WHERE enabled = 1;
CREATE INDEX idx_feeds_folder       ON feeds(folder_id, position);
CREATE UNIQUE INDEX idx_feeds_url_key ON feeds(url_key);
CREATE INDEX idx_feeds_url_orig_key ON feeds(url_original_key) WHERE url_original_key IS NOT NULL;
CREATE UNIQUE INDEX idx_feeds_one_archive ON feeds(disabled_reason) WHERE disabled_reason = 'archive';

-- Favicon bytes, served at /api/feeds/{id}/icon (and /api/greader.php/icon/... when enabled).
-- Filled by the favicon finder (§4.11); its bookkeeping is feed_icon_checks (migration 0006).
CREATE TABLE feed_icons (
  feed_id      INTEGER PRIMARY KEY REFERENCES feeds(id) ON DELETE CASCADE,
  data         BLOB NOT NULL,
  content_type TEXT NOT NULL,
  source_url   TEXT,
  hash         TEXT NOT NULL,
  fetched_at   INTEGER NOT NULL
) STRICT;

-- Items, narrow row (decision 30). id = crawl time in unix microseconds (allocator). uid = per-feed
-- dedup key ('g:'|'l:'|'h:' + 32 hex of sha256). content_hash = h(title|url|author|html);
-- text_hash = h(title|plain text): a change sets content_changed_at (Reader API ot leg 2).
-- sort_at = min(published_at, id/1e6 + 86400): UI order and retention rank. url is absolute ('' if
-- none). fulltext_mode: NULL = follow feed, 0 = force feed content, 1 = force extracted text.
-- retain_until: retention exemption set by a mark-unread restore. origin_title: original feed title
-- for items re-parented to the archive feed. Small mutable columns come first.
CREATE TABLE items (
  id                 INTEGER PRIMARY KEY,
  feed_id            INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  read               INTEGER NOT NULL DEFAULT 0 CHECK (read IN (0,1)),
  starred            INTEGER NOT NULL DEFAULT 0 CHECK (starred IN (0,1)),
  read_at            INTEGER,
  starred_at         INTEGER,
  retain_until       INTEGER,
  fulltext_mode      INTEGER CHECK (fulltext_mode IN (0,1)),
  published_at       INTEGER NOT NULL,
  updated_at         INTEGER,
  sort_at            INTEGER NOT NULL,
  content_changed_at INTEGER,
  word_count         INTEGER NOT NULL DEFAULT 0,
  uid                TEXT NOT NULL,
  content_hash       TEXT NOT NULL,
  text_hash          TEXT NOT NULL,
  url                TEXT NOT NULL DEFAULT '',
  title              TEXT NOT NULL DEFAULT '',
  author             TEXT NOT NULL DEFAULT '',
  image_url          TEXT,
  origin_title       TEXT,
  UNIQUE (feed_id, uid)
) STRICT;
CREATE INDEX idx_items_unread      ON items(id)                 WHERE read = 0;
CREATE INDEX idx_items_unread_feed ON items(feed_id, id)        WHERE read = 0;
CREATE INDEX idx_items_starred     ON items(id)                 WHERE starred = 1;
CREATE INDEX idx_items_feed_sort   ON items(feed_id, sort_at, id);
CREATE INDEX idx_items_sort        ON items(sort_at, id);
CREATE INDEX idx_items_unread_sort ON items(sort_at, id)        WHERE read = 0;
CREATE INDEX idx_items_changed     ON items(content_changed_at) WHERE content_changed_at IS NOT NULL;

-- Article bodies (decision 30). content_html = absolutized, bluemonday-sanitized feed HTML with
-- ORIGINAL (absolute) image URLs. content_text = plain text (FTS source, excerpt, word_count).
CREATE TABLE item_content (
  item_id         INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  content_html    TEXT NOT NULL DEFAULT '',
  content_text    TEXT NOT NULL DEFAULT '',
  enclosures_json TEXT CHECK (enclosures_json IS NULL OR json_valid(enclosures_json))
) STRICT;

-- Full-text search: external-content FTS5 over a view joining the narrow row and the body
-- (validated: MATCH, snippet(), integrity-check, rebuild source). rowid = items.id. Triggers keep it
-- in sync; every update trigger is guarded so unchanged values never touch the index. Order of
-- operations in Go: INSERT items then INSERT item_content (FTS insert fires on the content row);
-- the items BEFORE DELETE trigger removes the FTS row while item_content still exists.
CREATE VIEW item_search (id, title, author, content_text) AS
  SELECT i.id, i.title, i.author, c.content_text FROM items i JOIN item_content c ON c.item_id = i.id;
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, author, content_text,
  content='item_search', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER item_content_fts_ai AFTER INSERT ON item_content BEGIN
  INSERT INTO items_fts(rowid, title, author, content_text)
    SELECT new.item_id, i.title, i.author, new.content_text FROM items i WHERE i.id = new.item_id;
END;
CREATE TRIGGER items_fts_bd BEFORE DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.id, old.title, old.author, c.content_text FROM item_content c WHERE c.item_id = old.id;
END;
CREATE TRIGGER item_content_fts_bd BEFORE DELETE ON item_content
WHEN EXISTS (SELECT 1 FROM items WHERE id = old.item_id) BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.item_id, i.title, i.author, old.content_text FROM items i WHERE i.id = old.item_id;
END;
CREATE TRIGGER items_fts_au AFTER UPDATE OF title, author ON items
WHEN old.title IS NOT new.title OR old.author IS NOT new.author BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.id, old.title, old.author, c.content_text FROM item_content c WHERE c.item_id = old.id;
  INSERT INTO items_fts(rowid, title, author, content_text)
    SELECT new.id, new.title, new.author, c.content_text FROM item_content c WHERE c.item_id = new.id;
END;
CREATE TRIGGER item_content_fts_au AFTER UPDATE OF content_text ON item_content
WHEN old.content_text IS NOT new.content_text BEGIN
  INSERT INTO items_fts(items_fts, rowid, title, author, content_text)
    SELECT 'delete', old.item_id, i.title, i.author, old.content_text FROM items i WHERE i.id = old.item_id;
  INSERT INTO items_fts(rowid, title, author, content_text)
    SELECT new.item_id, i.title, i.author, new.content_text FROM items i WHERE i.id = new.item_id;
END;

-- Readability extraction, one row per attempted item. error set and content_html NULL = failed.
-- content_html is absolutized against the article URL and sanitized with the feed policy.
CREATE TABLE item_fulltext (
  item_id      INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  content_html TEXT,
  content_text TEXT,
  word_count   INTEGER NOT NULL DEFAULT 0,
  image_url    TEXT,
  source_url   TEXT,
  extracted_at INTEGER NOT NULL,
  error        TEXT,
  CHECK (content_html IS NOT NULL OR error IS NOT NULL)
) STRICT;

-- Retention ledger and tombstone. id = the former items.id; (feed_id, uid) blocks re-ingestion;
-- read is kept truthful by edit-tag / mark-all-as-read; last_seen_at is bumped while the feed
-- document still lists the uid and gates the 180-day purge.
CREATE TABLE trimmed_items (
  id           INTEGER PRIMARY KEY,
  feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  uid          TEXT NOT NULL,
  read         INTEGER NOT NULL CHECK (read IN (0,1)),
  trimmed_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  UNIQUE (feed_id, uid)
) STRICT;
CREATE INDEX idx_trimmed_seen    ON trimmed_items(last_seen_at);
CREATE INDEX idx_trimmed_trimmed ON trimmed_items(trimmed_at);

-- Restore stubs (decision 17): everything needed to put a trimmed item back into items +
-- item_content with its original id. Kept retention.restore_days (default 90) after trimmed_at,
-- then deleted nightly (the ledger row stays as a tombstone). Extracted full text is not kept.
CREATE TABLE trimmed_content (
  id              INTEGER PRIMARY KEY REFERENCES trimmed_items(id) ON DELETE CASCADE ON UPDATE CASCADE,
  published_at    INTEGER NOT NULL,
  updated_at      INTEGER,
  sort_at         INTEGER NOT NULL,
  word_count      INTEGER NOT NULL,
  content_hash    TEXT NOT NULL,
  text_hash       TEXT NOT NULL,
  url             TEXT NOT NULL,
  title           TEXT NOT NULL,
  author          TEXT NOT NULL,
  image_url       TEXT,
  origin_title    TEXT,
  fulltext_mode   INTEGER,
  content_html    TEXT NOT NULL,
  content_text    TEXT NOT NULL,
  enclosures_json TEXT
) STRICT;

-- Per-feed fetch history (health view). Kept 14 days with a 50-row floor per feed; the last 10
-- error rows and every keep = 1 row (redirect_migrated, guid_churn_suspected, rekeyed) survive.
-- first_item_id/last_item_id bound the ids this fetch inserted ("mark this fetch read").
-- error_class: timeout|dns|connect|tls|http|cloudflare|too_large|empty|parse|ssrf|redirect_loop|gone
-- note: 'redirect_migrated: <old> -> <new>', 'redirect_held_new_site: <new> is on another site; ...',
--       'redirect_target_owned_by_feed <id>',
--       'retry_after=<s>s', 'guid_churn_suspected', 'guid_duplicates: <k>/<n>', 'rekeyed: <k>',
--       'skipped: host retry-after until <ts>', 'fulltext: <ok>/<tried>', 'initial_read: <k>'.
CREATE TABLE fetch_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  feed_id       INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL CHECK (trigger IN ('scheduled','manual','feed_manual','subscribe','import','retention')),
  started_at    INTEGER NOT NULL,
  duration_ms   INTEGER NOT NULL,
  outcome       TEXT NOT NULL CHECK (outcome IN ('ok','not_modified','unchanged','error','skipped','trim_only')),
  http_status   INTEGER,
  error_class   TEXT,
  error         TEXT,
  new_items     INTEGER NOT NULL DEFAULT 0,
  updated_items INTEGER NOT NULL DEFAULT 0,
  trimmed_items INTEGER NOT NULL DEFAULT 0,
  first_item_id INTEGER,
  last_item_id  INTEGER,
  bytes         INTEGER,
  final_url     TEXT,
  note          TEXT,
  keep          INTEGER NOT NULL DEFAULT 0 CHECK (keep IN (0,1))
) STRICT;
CREATE INDEX idx_fetch_log_feed ON fetch_log(feed_id, id);
CREATE INDEX idx_fetch_log_err  ON fetch_log(feed_id, id) WHERE outcome = 'error';

-- Reading stats: append-only, never trimmed, no FKs (survive trims and unsubscribes). Feed,
-- folder and item identity are snapshotted at write time. local_* computed in settings.tz at
-- write time (DST baked in). value: read_time = seconds, scroll = percent 0-100.
-- inferred = 1 for opens derived from Reader API edit-tag (approximate; off by default).
CREATE TABLE stats_events (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  ts            INTEGER NOT NULL,
  local_date    TEXT NOT NULL,                  -- 'YYYY-MM-DD' local, for streaks
  local_hour    INTEGER NOT NULL CHECK (local_hour BETWEEN 0 AND 23),
  local_weekday INTEGER NOT NULL CHECK (local_weekday BETWEEN 0 AND 6),   -- 0 = Sunday
  kind          TEXT NOT NULL CHECK (kind IN ('open','read_time','scroll','star','unstar','open_original','share')),
  client        TEXT NOT NULL CHECK (client IN ('web','pwa','reeder','netnewswire','unread','api')),
  inferred      INTEGER NOT NULL DEFAULT 0 CHECK (inferred IN (0,1)),
  item_id       INTEGER NOT NULL,
  feed_id       INTEGER NOT NULL,
  feed_title    TEXT NOT NULL,
  folder_id     INTEGER,
  folder_name   TEXT,
  item_title    TEXT,
  item_url      TEXT,
  value         INTEGER,
  session_key   TEXT
) STRICT;
CREATE INDEX idx_stats_kind_ts ON stats_events(kind, ts);
CREATE INDEX idx_stats_feed    ON stats_events(feed_id, kind, ts);
CREATE INDEX idx_stats_session ON stats_events(session_key, kind) WHERE session_key IS NOT NULL;
```

**Settings keys added after 0001.** The comment above lists the keys at v1. Since then: user keys `fetch.user_agent_mode` (0002; `browser_on_failure` uses the `ua_fallback` column), `fetch.fulltext_all` (default false), `library.favorites` (default `[]`, §7.1a), `library.auto_read_days` and `library.saved_searches` (§7.1d), and `imgproxy.cache_mb` (§7.4); and system keys `sys.last_nightly_date`, `sys.last_nightly_at`, `sys.auto_read_last_run`, `sys.auto_read_feed_marks`, `sys.filter_disabled_reasons` (why a stored filter over the regex limits was switched off, §7.1b) and `sys.stats_timed_since` (when reading time was first recorded, §8). `greader.icon_urls` defaults to true (a database with no stored row picks the default up; only an explicit false turns icons off). `greader.ot_includes_user_changes` (items whose read or starred state changed since `ot`, by `items.state_changed_at`, join the second leg) and `greader.subscribe_fetch_now` (a client subscribe waits up to 8 s for the first fetch) are read by the Reader API, both default off; the reserved `stats.api_single_read_is_open` was removed in 0.6.0 (nothing read it), as were `ui.font_size`, `ui.font_ui` and `ui.layouts`. Migration 0008 adds `stats_events.event_id` (§2.2a, §8). The stats settings `stats.enabled` (bool, default true), `stats.week_start` (`sunday` or `monday`, default `sunday`) and `stats.wrapped_enabled` (bool, default true) are described in §8.

### 2.2a Migrations after 0001

Built (applied in order by the runner; each one is a line here so the block above stays the v1 schema):

| Migration | Change | Why |
|---|---|---|
| `0002_ua_fallback.sql` | `feeds.ua_fallback INTEGER NOT NULL DEFAULT 0 CHECK (ua_fallback IN (0,1))`; deletes the `ui.line_height` and `ui.content_width` settings rows | A feed that only loads with a browser User-Agent remembers it (§4.4; used when `fetch.user_agent_mode` is `browser_on_failure`); the two reading settings became the single `ui.reading_density` preset |
| `0003_fulltext_error_class.sql` | `item_fulltext.error_class TEXT CHECK (error_class IN ('transient','permanent'))`; NULL means an error stored before the column existed, treated as permanent | `POST /api/items/{id}/fulltext` retries a transient failure after an hour (§7.5) |
| `0004_filters_devices.sql` | Schema only, spec §1.8, §1.13 and §4 of the round-2 backend additions (a research note not kept in this repository): table `filters` (scope, kind and action CHECKs, `terms` and `fields` JSON arrays, the scope-to-id CHECK, highlight only for text, cascade from folders and feeds, partial indexes on `folder_id` and `feed_id`); `items.muted_by INTEGER` (no FK) with the partial index `idx_items_muted(sort_at, id)`, and `items.muted_was_read` (0 or 1, NULL while not muted: whether the item was already read when the rule muted it, so `?unmute=unread` only restores the ones that were unread); `categories_json` on `item_content` (JSON-checked) and `trimmed_content`; `feeds.auto_read_days` (NULL inherits, 0 is off, at most 365); table `devices` (`id` 16 to 32 chars, `name` of at most 64, `user_agent`, `client` default `'web'`, `settings` a JSON object of at most 8 KB, `created_at`, `last_seen_at`, `idx_devices_seen`); `filters` also has `invert`, `position`, `hits` and `last_hit_at`, and defaults `fields` to `'["title"]'`, `whole_word` to 1 and `fold_diacritics` to 1. The SQL file is the authority. The trim (`retention.go`) and restore (`itemstate.go`) SQL copy `categories_json` both ways | Keyword filters, the mute marker, item categories, auto-mark-read and per-device appearance profiles. Steps 10 and 11 now read and write `filters`, `items.muted_by`, `items.muted_was_read` and `categories_json` (§4.8, §5, §7.1b); `devices` backs per-device appearance profiles (§7.1c) and is purged nightly after 400 days unseen; `feeds.auto_read_days` feeds the nightly auto-read step (§2.6, §7.1d). Additive and O(1): no table is rebuilt and the indexes start empty. Rehearsed on a copy of the real phase 1 database (schema 1 to 4, 5,600 items, about 0.3 s) |
| `0005_fts_porter.sql` | Drops and recreates `items_fts` with `tokenize = 'porter unicode61 remove_diacritics 2'`, stores the rank config `bm25(4.0, 2.0, 1.0)` (title 4x, author 2x, body 1x) with `INSERT INTO items_fts(items_fts, rank) VALUES ('rank', ...)`, then `'rebuild'`s from `item_search` | Stemmed, title-weighted search (§2.4). The five FTS triggers resolve `items_fts` by name when they fire, so they survive the drop untouched, and the view is unchanged. One `BEGIN IMMEDIATE` transaction behind the pre-migration snapshot; a failure rolls the whole file back and leaves the old index. Cost is linear in the indexed text: rehearsed on a copy of the real phase 1 database (5,600 items) the rebuild took 0.58 s and the full schema 1 to 5 open 0.95 s; expect about 10 s per 100,000 items, with no serving meanwhile. Every hit of the old index remains a hit; the index size stays about the same |
| `0006_feed_icon_checks.sql` | Table `feed_icon_checks` (`feed_id` PK with cascade from feeds, `site_url` and `feed_url` as the lookup saw them, `checked_at`, `next_check_at`, `failures >= 0`, `last_error`), STRICT, no index (one full scan of feeds per poll) | The favicon finder (§4.11): when each feed was last looked up and when it is due again, kept apart from the feed's own health columns so a failing icon never shows as a failing feed. Additive, one empty table |
| `0007_item_state_changed.sql` | `items.state_changed_at INTEGER` (unix seconds, NULL = no change recorded) and the partial index `idx_items_state_changed(state_changed_at) WHERE state_changed_at IS NOT NULL`; existing rows are backfilled with the later of `read_at` and `starred_at` | `greader.ot_includes_user_changes` (decision 6, §6.5): an unread or unstar clears `read_at`/`starred_at`, so only a separate column can report it. Every state change after ingest sets it in the same `UPDATE` that flips the flag (no trigger): `SetRead` both ways (edit-tag, the UI, mark-scope, filter mark-read), `SetStarred`/`SetStarredAt` both ways (always the server's time, even for a replayed offline star), `MarkAllRead`, the nightly auto-read, a restore from the ledger, a retro mute (only the items it turns read) and an un-mute to unread (only the items it turns unread). Ingest's initial state (initial-read window, rekey leftovers, star/mark-read/mute rules at ingest) never sets it. The ALTER is O(1); the backfill is one `UPDATE` over the rows that have either timestamp, so the ot answer is the same across the upgrade. The FTS triggers fire only on `title`/`author`/`content_text` and are untouched |
| `0008_stats_event_id.sql` | `stats_events.event_id TEXT` (nullable) and the partial unique index `idx_stats_event(event_id) WHERE event_id IS NOT NULL` | Duplicate suppression for stats events sent by the web (§8 rule 4): the sender tags every event with a random id, so a retried flush or a repeated beacon is dropped instead of counted twice, whatever the kind. Existing rows keep `event_id` NULL, so the index is empty, but the build scans `stats_events` once inside the migration transaction (not O(1); fast at expected sizes). Events without an id (older clients, and every event the server records itself) behave as before. A schema change: an older binary refuses the database |
| `0009_stats_summary_indexes.sql` | Three partial covering indexes on `stats_events`: `idx_stats_open_cov(local_date, local_hour, feed_id, item_id, session_key, ts, inferred) WHERE kind='open'`, `idx_stats_rt_cov(local_date, local_hour, feed_id, session_key, value) WHERE kind='read_time'` and `idx_stats_scroll_cov(local_date, session_key, value) WHERE kind='scroll'` | The stats summary (§8) reads opens, read time and scroll by local date range and never needs the wide table rows (snapshotted titles and URLs). Measured on 1,000,000 seeded events, the summary took 2.1 to 3.0 s for a week or a month and 5.6 to 10 s for a year or all time before the indexes; after them a month takes about 120 ms (session keys of production length: 32 hex characters). The queries name the indexes with `INDEXED BY`, because without statistics the planner prefers `idx_stats_kind_ts`. Not O(1): each index is built by one scan of `stats_events` inside the migration transaction (about 2 s per million rows for the three, measured). Costs about 60 MB per million events on disk (a 232 MB database grew to 300 MB at one million events) and a little on each insert. The `INDEXED BY` hints make a missing index a hard error, so **any future migration that rebuilds `stats_events` (a table recreate for a new column or CHECK) must recreate these three indexes**; `TestStatsSummaryPlans` runs `EXPLAIN QUERY PLAN` over every hinted query and fails when one stops using its index. The upgrade also needs transient disk room (docs/deploy.md): a pre-migration snapshot the size of the database plus the whole build in the WAL, and the start is refused up front unless there is enough extra free space (the database size plus 64 MB for the migration, plus 1.1x the database size for the snapshot, added together when both are on one volume). A schema change: an older binary refuses the database |
| `0012_nested_folders.sql` | Rebuilds `folders` with `parent_id INTEGER REFERENCES folders(id) ON DELETE CASCADE` (NULL = top level) in place of the column-level `UNIQUE` on `name`: `idx_folders_sibling_name(ifnull(parent_id, 0), name COLLATE NOCASE)` makes names unique among siblings, CHECKs keep the default folder at the top level and a folder from being its own parent, and `idx_folders_one_default`, the trigger `folders_keep_default` and the `sqlite_sequence` high-water mark carry over. Adds the view `folder_paths(id, parent_id, name, path, depth, sort_key)` (a recursive CTE: the full path joined with `/`, the depth, and a pre-order key with siblings by position, then name). Rules a CHECK cannot express (unique full paths ignoring ASCII case, no cycles, at most 8 levels, no subfolders under the default folder) are kept by the single writer, `internal/store/folders.go` | Nested folders (#209). No row changes: every folder becomes top level with its id, name and position, so every Reader API label is the same string and a flat library's API output is byte for byte what it was. Deleting a folder deletes its subtree through the cascade (its feeds fall to folder 1 by `ON DELETE SET DEFAULT`, its filters cascade). Runs with foreign keys off (the rebuild), then `foreign_key_check`. O(folders) |
| `0013_starred_and_ledger_unread_indexes.sql` | Two partial indexes: `idx_items_starred_sort(sort_at, id) WHERE starred = 1` and `idx_trimmed_unread(id) WHERE read = 0` | The Starred view's keyset pages (§7.1) walk only starred rows, as Unread walks `idx_items_unread_sort`, instead of every item older than the cursor; the ledger half of a mark-all-as-read (`UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= ?`, Reader `mark-all-as-read` and the web app's mark scope) reads only the unread ledger rows instead of the whole ledger. Additive: no row changes; each index is one scan of its table inside the migration |

Nothing is pending: migration 0013 is the last one designed.

Theme, density and similar UI changes need no migration: `ui.*` values are settings rows validated in Go.


Validated on SQLite 3.50.4 (revision 1 or 2 as marked):

- The username CHECK rejects `bi ll` and accepts `owner.k-9_x` (r1).
- Deleting folder 1 aborts. Deleting another folder moves its feeds to folder 1 (r1, r2).
- A feed delete cascades items, the FTS rows (via `items_fts_bd`), `item_content`, the ledger, stubs (a nested cascade) and fetch_log. FTS `integrity-check` is clean after a trim, a restore and a cascade (r2).
- A second `disabled_reason='archive'` row is refused (r2).
- `PRAGMA foreign_key_check` returns nothing (r2).

Still to do on the pinned driver: the whole file under modernc/SQLite 3.53.4, as the first migration test (§10).

### 2.3 Every index and the query it serves

The plans are from revision 1 on the wide table. Revision 2 re-checked the `ot` legs, the FTS join and the trim selection. The regression suite asserts every row twice on the pinned driver: once on a fresh database, and once after `ANALYZE` (§10).

| Index | Query | Plan |
|---|---|---|
| `idx_items_unread (id) WHERE read=0` | Reader `reading-list&xt=read` (NNW every ~120 s, Reeder `n=10000`); global mark-all `UPDATE … WHERE read=0 AND id<=?` | `SCAN items USING COVERING INDEX idx_items_unread`; with `c`: `SEARCH … (id<?)`; mark-all `SEARCH … idx_items_unread (id<?)` |
| `idx_items_unread_feed (feed_id,id) WHERE read=0` | Unread counts `GROUP BY feed_id` (UI sidebar, `unread-count`); `feed/N&xt=read`; label + unread; mark-all on a feed | `SCAN … COVERING INDEX idx_items_unread_feed`; `SEARCH … (feed_id=? AND id<?)` |
| `idx_items_starred (id) WHERE starred=1` | Reader `s=starred` (both clients, full list) | `SCAN … COVERING INDEX idx_items_starred` |
| `idx_items_feed_sort (feed_id,sort_at,id)` | UI feed view (keyset); the trim-set selection, a `row_number() OVER (PARTITION BY muted_by IS NOT NULL ORDER BY sort_at DESC, id DESC)` window over the feed's unstarred, unretained items with separate allowances for muted and real items (narrow-row lookups for `starred`/`retain_until`); Reader `feed/N` streams | `SEARCH items USING INDEX idx_items_feed_sort (feed_id=?)` |
| `idx_items_sort (sort_at,id)` | UI "All" view keyset | `SEARCH … (sort_at<?)` |
| `idx_items_unread_sort (sort_at,id) WHERE read=0` | UI default "Unread" view keyset | `SCAN/SEARCH … COVERING INDEX idx_items_unread_sort` |
| `idx_items_changed (content_changed_at) WHERE NOT NULL` | Leg 2 of the `ot` filter (§6.5), forced with `INDEXED BY` (always satisfiable: the leg has a range on the leading column) | `SEARCH items USING COVERING INDEX idx_items_changed (content_changed_at>?)`, the same after `ANALYZE` |
| `idx_items_state_changed (state_changed_at) WHERE NOT NULL` | Only with `greader.ot_includes_user_changes` on: the state-change branch of leg 2 (§6.5), forced with `INDEXED BY` the same way | `SEARCH items USING COVERING INDEX idx_items_state_changed (state_changed_at>?)` (not covering when a stream predicate needs the row), the same after `ANALYZE` |
| Integer primary key | Leg 1 of `ot`; Reeder `s=read&ot=now-30d` (narrow pages now, ~5 MB for 34k rows) | `SEARCH items USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)` |
| `UNIQUE (feed_id, uid)` (autoindex) | Dedup lookup in CommitFetch: the first arm of one `WITH u(uid) AS MATERIALIZED (SELECT value FROM json_each(?))` `UNION ALL` query, `uid IN u` | `SEARCH … sqlite_autoindex_items_1 (feed_id=? AND uid=?)` |
| `item_content` PK | Contents and item views join; the FTS view lookup by rowid | `SEARCH c USING INTEGER PRIMARY KEY (rowid=?)` |
| FTS5 | Search | `SCAN items_fts VIRTUAL TABLE INDEX 32:M3` then `SEARCH i USING INTEGER PRIMARY KEY (rowid=?)` |
| `trimmed_items UNIQUE (feed_id, uid)` | Tombstone check at ingest (the second arm of that same `UNION ALL` query); ledger upsert | `SEARCH … COVERING INDEX sqlite_autoindex_trimmed_items_1` |
| `idx_trimmed_seen (last_seen_at)` | Nightly purge `DELETE … WHERE last_seen_at < ?` | `SEARCH … COVERING INDEX idx_trimmed_seen` |
| `idx_trimmed_trimmed (trimmed_at)` | Nightly stub purge `DELETE FROM trimmed_content WHERE id IN (SELECT c.id FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id WHERE t.trimmed_at < ? LIMIT ?)` | range |
| `idx_feeds_due (next_fetch_at) WHERE enabled=1` | Scheduler tick selection | `SEARCH feeds USING COVERING INDEX idx_feeds_due (next_fetch_at<?)` |
| `idx_feeds_folder (folder_id,position)` | Label streams; subscription/list ordering; folder view | `SEARCH feeds USING COVERING INDEX idx_feeds_folder (folder_id=?)` |
| `idx_feeds_url_key` (unique), `idx_feeds_url_orig_key` | `FindFeedByURL`: `WHERE url_key = :k OR url_original_key = :k` | `MULTI-INDEX OR` |
| `idx_feeds_one_archive` | Enforces a single archive feed; archive lookup | — |
| `idx_fetch_log_feed (feed_id,id)` | Health view history; the retention floor `… OFFSET 49` | `SEARCH fetch_log USING COVERING INDEX idx_fetch_log_feed` |
| `idx_fetch_log_err (feed_id,id) WHERE outcome='error'` | "Keep the last 10 error rows" in the cap | `SEARCH … COVERING INDEX idx_fetch_log_err (feed_id=?)` |
| `idx_sessions_expires` | Nightly session purge | range |
| `idx_folders_one_default` | Enforces a single default folder | — |
| `idx_items_muted (sort_at,id) WHERE muted_by IS NOT NULL` (0004) | The muted view keyset | range |
| `idx_items_starred_sort (sort_at,id) WHERE starred=1` (0013) | UI "Starred" view keyset (the page's id pick) | `SCAN/SEARCH … COVERING INDEX idx_items_starred_sort` |
| `idx_trimmed_unread (id) WHERE read=0` (0013) | Ledger half of mark-all-as-read and the web app's mark scope: `UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= ?` | `SEARCH trimmed_items USING INDEX idx_trimmed_unread (id<?)` |
| `idx_filters_folder`, `idx_filters_feed` (0004, partial) | Cascade lookup when a folder or feed is deleted | range |
| `idx_devices_seen` (0004) | LRU eviction above 50 devices and the 400-day purge | range |
| `idx_stats_kind_ts (kind,ts)` | Phase-4 windows | `SEARCH … (kind=? AND ts>?)` |
| `idx_stats_feed (feed_id,kind,ts)` | Per-source views | range |
| `idx_stats_open_cov`, `idx_stats_rt_cov`, `idx_stats_scroll_cov` (0009, partial, covering; see §2.2a) | The stats summary (§8), by local date range, index-only | `SEARCH … USING COVERING INDEX idx_stats_rt_cov (local_date>? AND local_date<?)` |
| `idx_stats_session (session_key,kind)` | Ingest validation (open exists, cumulative read_time), inside the ingest write transaction | `SEARCH … idx_stats_session (session_key=? AND kind=?)` |

`stream/items/contents` uses `WHERE i.id IN (SELECT value FROM json_each(?))`, which plans as `SEARCH i USING INTEGER PRIMARY KEY` over the list: one statement for 100, 150 or 1000 ids, with no placeholder limit.

### 2.4 FTS5

- **Setup.** External content over the view `item_search`, with `porter unicode61 remove_diacritics 2` from schema 5 (stemming on both sides, diacritics folded; `unicode61` alone before) and the persistent rank `bm25(4.0, 2.0, 1.0)`, so `items_fts.rank` is already title-weighted. Only plain text is indexed (`content_text`, produced by bluemonday `StrictPolicy` at ingest), so a retention trim deletes cheap rows. Both update triggers (`items_fts_au`, `item_content_fts_au`) are value-guarded, and `item_content_fts_bd` fires only while the item exists (five FTS triggers in all). Validated: 200 markup-only updates left `items_fts_data` unchanged at 21 rows.
- **Query builder v2** (`internal/store/searchquery.go`: `ParseSearch`, `SearchQuery.Match`, `SearchQuery.FallbackMatch`; `BuildFTSQuery` is the exact form). User text is parsed left to right into terms, and the MATCH expression is built only from double-quoted phrases (embedded quotes doubled), parentheses, `AND`, `OR`, `NOT` and two whitelisted column filters, so user text can never cause an FTS5 syntax error (a fuzz test runs every parse against a live index) or filter on an unlisted column. FTS5 needs an explicit `AND` next to a parenthesized group, so terms are joined with ` AND `.

  | Input | Expression |
  |---|---|
  | `word` | `"word"` (porter stems query and text) |
  | `"exact phrase"` | `"exact phrase"` (an unterminated quote runs to the end) |
  | `-word`, `-"a b"`, `NOT word` | `(positives) NOT (word OR ...)`; exclusions alone match nothing |
  | `title:word`, `author:"jane doe"`, also `-title:x` | `title : "word"`; only `title` and `author`, case-insensitive; any other `x:y` is one literal word |
  | `word*`, word of at least 3 runes (2 for Han, Kana and Hangul) | `"word"*` (an explicit prefix; a shorter `ab*` is the literal word `"ab"`) |
  | last bare word with the same minimum length, text does not end in a space, **and the request says `typing=1`** | `"word"*` (search-as-you-type; §7.1) |

  **A prefix is a stem prefix.** FTS5's porter tokenizer also stems a prefix query, so `"running"*` and `"run"*` are the same query. Measured on the real library (5,600 items): both return 988 rows. Consequently `"apple"*` means `appl*` and reaches application and apply: `apple` finds 518 items, `apple*` 851; `police` 36, `police*` 173 (polic* pulls in policy and policies). A prefix therefore widens the match, and an earlier draft, which rendered the unfinished last word as `("w" OR "w"*)` on the belief that a prefix is not stemmed, made every saved search and every "mark all results read" match that wider set. Now the exact query is always the plain stemmed words, and a prefix exists only where the user asked for one: an explicit `word*`, the last word while the UI reports typing (`typing=1`), and the fallback below. Saved searches, mark-read `scope.q` and unread counts never send `typing` and never see an implicit prefix. A mid-word prefix of an inflected form (`runni`) finds nothing until the word is complete, because the prefix is matched against stems. Limits: 12 terms, at most 3 prefix terms (a fourth `w*` is a literal word), 64 runes per word, 512 bytes of input (the API refuses `q` over 1000 bytes). Prefixes shorter than 3 runes (2 for CJK) are never used: a one- or two-letter prefix matches most of the library. Uppercase `NOT` is the only word operator; `OR` and `AND` are ordinary words. Search text is not trimmed of trailing spaces (with `typing=1` that space means "word finished").
  ```sql
  -- pass 1: the page's ids (rank only when order=rank; date order never computes bm25)
  SELECT i.id, items_fts.rank FROM items_fts JOIN items i ON i.id = items_fts.rowid
  WHERE items_fts MATCH :q [AND i.feed_id = :f | AND i.feed_id IN (SELECT id FROM feeds WHERE folder_id = :fo)]
  ORDER BY rank LIMIT :n+1;        -- or ORDER BY i.sort_at DESC, i.id DESC when order=date (ASC when order=oldest)
  -- pass 2: only those rows are decorated
  SELECT i.*, snippet(items_fts, 2, '<mark>', '</mark>', '…', 24) FROM items_fts JOIN items i ON i.id = items_fts.rowid
  WHERE items_fts MATCH :q AND i.id IN (:page_ids);
  ```
  **Cost.** Before the two passes, `snippet()` and `bm25` ran for every matching row before `LIMIT`, so a query matching most of the library cost 1.6 to 3.3 s per page (measured: `a b c … p`, `zzqx a e i o u s t`, `a* b* … p*` on the 5,600-item copy). Now snippets are built for the page only, and the whole search (probe, id pass, decoration) runs under a 500 ms budget: past it the store returns `ErrSearchTooBroad` and the API answers `422 {"error":"search_too_broad"}`, never holding a reader connection. The rehearsal test asserts under 300 ms per page for the pathological queries.
- **Fallback.** On the first page (no cursor), if the exact expression finds no row in the request's scope (view, feed, folder, reading time; a one-row probe with the same WHERE), the search runs once as `FallbackMatch`: the positive words (phrases split into words, column filters kept) ORed, with the exclusions kept: words under 3 runes (2 for CJK) are dropped, the first three remaining words become prefixes (`"w"*`) and later ones stay plain words, at most 12 in all; with no word left there is no fallback. The response then carries `fallback: true`; it stays false when the fallback finds nothing or would equal the exact expression. The cursor records the mode (an `f` tag) so later pages stay in it. Mark-read `scope.q` marks by the list's own choice: the client echoes the response's `fallback` as `scope.fallback` and the server uses that expression without deciding again (the list and mark-read run in different transactions, and an exact match arriving in between would make a fresh probe pick the exact expression and mark little or nothing of what the fallback list showed). Without `scope.fallback` the server probes over the scope (`view:"unread"` counts unread rows only, as the unread list does), as before.
- **Cursors.** A relevance cursor is `s2[f]|<rank>|<id>` (base64url); the `s2` tag versions the rank basis: cursors of the old `r<rank>|<id>` form (before schema 5) are refused with `400 bad_cursor` because their numbers are on another scale. Date cursors are `[a][f]<sort_at>.<id>`.
- **CJK.** `unicode61` does not segment Han, Kana or Hangul: a run of such characters is a single token, so a query matches the whole run or (as a prefix) its start, never a substring from the middle. Documented limit; there is no trigram index (a second index would roughly double the FTS size for a single-user reader).
- **Known limit.** Extracted full text is not indexed. Trimmed stubs are not searchable.
- **Maintenance.** There is no weekly `optimize`: FTS5's default automerge keeps segment counts bounded without one large write. Each week the nightly snapshot job runs `'integrity-check'` against the freshly written **snapshot file**, never the live database. A failure logs an ERROR, is recorded in `sys.last_snapshot_error`, and is shown in the health view. `'rebuild'` is a manual repair action in the UI and runs on the writer, in one `WithWrite` (10 s deadline), so on a very large library it can time out and roll back; the offline path is a restore or migration.

### 2.5 Migration mechanism

1. `//go:embed migrations/*.sql`. Files are named `NNNN_name.sql`, numbered densely from 0001.
2. On open, the runner reads `PRAGMA application_id` (first on a throwaway read-only connection with no pragmas, so a foreign file is never switched to WAL; `migrate` checks again):
   - `0` with an empty schema is a fresh file.
   - `0` with a non-empty schema, or any value other than 1263095884, means refuse to start: "not a Kipple database".
3. It reads `PRAGMA user_version`. If that is higher than the highest embedded migration, it refuses to start (downgrade guard).
4. If migrations are pending and the database is not fresh:
   - **Free-space precheck.** Before anything is written it refuses to start (a clear error, nothing changed) unless enough *extra* free space is available: on the database's volume 1x the database file (without the WAL) plus 64 MB, as headroom for the migration's WAL and file growth; on the backup directory's volume 1.1x the database size for the snapshot (VACUUM INTO, about 1x). When the backup directory is on the same volume as the database the two requirements add up. This is extra free space needed, not peak total usage. If the free space cannot be read the check is skipped. `TestMigrationRefusesWhenDiskTooFull` covers it.
   - `os.MkdirAll("/data/backup")`.
   - `VACUUM INTO '/data/backup/pre-migration-<from>-<to>-<unix-ns>.db'` on the snapshot pool. The target name is unique, so a retried migration can never hit "output file already exists".
   - The newest 3 `pre-migration-*` files are kept.
5. It applies each pending file N on the writer connection, inside a function that has `defer`: `PRAGMA foreign_keys=ON`, followed by a check that `PRAGMA foreign_keys` returns 1. That check runs on success, on error and on panic, and startup aborts if it fails.
   - If the file's first line is `-- kipple:foreign-keys-off`, the runner executes `PRAGMA foreign_keys=OFF` before `BEGIN`. This is required for the 12-step table-rebuild procedure.
   - Then `BEGIN IMMEDIATE; <file>; PRAGMA foreign_key_check;`, which must return no rows or the migration rolls back. Then `PRAGMA user_version = N; COMMIT;`.
   - **Idempotence.** The runner is the guard: a file runs once, gated by `user_version`, inside one transaction, so a failure rolls the whole file back (a test appends a failing statement to 0004 and checks nothing is left behind). Plain `ALTER TABLE ... ADD COLUMN` is therefore fine; `CREATE ... IF NOT EXISTS` is used only where a file needs to tolerate a table that may exist.
   - **Tests.** `migrate0004_test.go` and `migrate0005_test.go` (fresh schema, populated schema 4, rollback on failure, the search plan) migrate a fresh database, a populated older database and (when present) a copy of a real export (`TestRehearsalOnRealDatabase`, path from `KIPPLE_REHEARSAL_DB`, skipped in CI), runs `integrity_check` and `foreign_key_check` after each, and checks that a binary with only three (or four) migrations refuses a schema-4 (or schema-5) file.
6. After all migrations, it runs `PRAGMA optimize` (`analysis_limit=400` is already set).
7. There are no down migrations. Rollback means restoring the pre-migration snapshot: `kipple restore /data/backup/pre-migration-<from>-<to>-<ns>.db --yes` (§2.6; the runbook is `docs/deploy.md`).

### 2.6 Maintenance (one goroutine, one cancellable context)

- **Implementation (as built).** `internal/maint` owns the schedule (a 1-minute `clock.Clock` ticker; hourly and nightly are due-checks against the injected clock, so a coalesced tick never loses a job) and `internal/store/maint.go` owns the SQL. Each job logs one summary line (rows, batches, duration) and calls the optional `OnJob` hook.
- **Batching.** Every purge below is a loop of bounded batches: 1000 rows per `WithWrite`, each behind the commit gate, with a 25 ms pause between batches (cancellable) so fetch commits and edit-tags interleave. A batch shorter than 1000 ends the loop.
- **Hourly:** `PRAGMA wal_checkpoint(PASSIVE)` on the writer's single connection. It cannot run inside the `WithWrite` transaction (SQLite answers "database table is locked"), so it takes the writer connection directly, which is the same exclusion `WithWrite` uses, behind the commit gate and bounded by the 10 s write timeout.
- **Nightly at 04:10 in `settings.tz`.** The nightly job and the statistics take their zone from the one resolver, `store.Zone` (`store.ZoneName` for the name): the `tz` setting, else `UTC` (`store.DefaultTZ`; installs from before 0.5 keep `America/New_York` through the row migration 0010 writes). `TZ` only seeds the setting on the first start that finds no `tz` row (`store.SeedZone`); the setting owns the zone after that. The maintenance loop reads it at every minute check. It keeps the last nightly run as an absolute instant (`sys.last_nightly_at`, RFC 3339 UTC; `sys.last_nightly_date` is still written, and read alone by a database from before the instant existed), and runs when the current local date is later than the date of that instant **read in the zone now in use**, and 04:10 has passed: once per local date, across restarts, and a `tz` change can neither repeat a date nor skip one (zone moved ahead: the same date is not run again; zone moved behind: the recorded run is read in the new calendar, so the new zone's own next date runs at its 04:10, which can be a few hours after the last run rather than a day later; zone moved before tonight's run so that 04:10 has already passed there: it runs at the next minute check). A fresh install treats the date it starts on as covered when 04:10 has passed. A night missed while the server was down runs on the first check at least 5 minutes after startup (`CatchUpDelay`), so it does not overlap the startup fetch burst; only that catch-up waits, not an on-schedule run or one that a zone change makes due. An unknown `tz` name keeps the previous zone, is not a zone change, and logs one warning per distinct bad value (the Sunday FTS check follows the same zone). `time.Local`, which sets log timestamps, follows the setting as of each start; the UI shows times in the device zone. The steps, in code order (`internal/maint/maint.go`):
  1. Purges, through `WithWrite`, batched as above (a restore also refuses ledger rows older than `restore_days`, so it never depends on when this ran):
     - `DELETE FROM trimmed_content WHERE id IN (SELECT c.id FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id WHERE t.trimmed_at < now − restore_days·86400 LIMIT :batch)`
     - `DELETE FROM trimmed_items WHERE id IN (SELECT id FROM trimmed_items WHERE last_seen_at < now − max(180, restore_days + 7) d LIMIT :batch)`
     - `DELETE FROM sessions WHERE expires_at < now` (batched)
     - `DELETE FROM devices WHERE last_seen_at < now − 400 d` (batched)
  2. Auto-read (`auto_read`, §7.1d): marks read what crossed each feed's auto-read threshold since `sys.auto_read_last_run`. It is a no-op while `library.auto_read_days` and every feed's `auto_read_days` are off.
  3. The image cache sweep (`imgcache.Cache.Sweep`, job `imgcache_sweep`, only when the cache is open): idle expiry, expired failure records, eviction to the cap, old host hints, then a WAL checkpoint and `VACUUM` of `imgcache/index.db` (its own file, never the main database). A failure is logged and does not stop the snapshot.
  4. `PRAGMA optimize`, through `WithWrite`.
  5. The snapshot, on the snapshot pool:
     - `os.MkdirAll("/data/backup")` and `os.Remove("/data/backup/kipple-snapshot.tmp")`, ignoring not-exist.
     - `VACUUM INTO '/data/backup/kipple-snapshot.tmp'`, using the maintenance context so shutdown interrupts it.
     - On Sundays, the FTS `integrity-check` against the tmp file, through a throwaway connection.
     - `fsync`, then an atomic rename to `kipple-snapshot.db`.
     - Record `sys.last_snapshot_at` (on success `sys.last_snapshot_error` is cleared), or `sys.last_snapshot_error` on failure; a cancelled run records nothing and removes the tmp file with its `-wal`, `-shm` and `-journal`. The health view shows the age of the last good snapshot and turns it red after 48 h.
  6. Off-box copies use only `kipple-snapshot.db`, never the live db/wal pair. As built, nothing on Host-A copies it: Host-B's backup job pulls it with `ssh host-a docker cp kipple:/data/backup/kipple-snapshot.db …` (`docker cp` needs no shell in the image). The in-app backup export below complements the pull.
- **One snapshot at a time.** The nightly snapshot and the backup export share one slot in `store` (`TrySnapshot`). The nightly job waits for an export in progress (it is short); an export that finds the slot taken answers `409 busy` and never queues. Both use `VACUUM INTO` on the snapshot pool, which holds only a read snapshot, so neither blocks the writer or the commit gate.
- **Backup export** (`internal/backup`, `POST /api/backup`, §7.1). Steps: take the slot; refuse a database over 4 GiB or a volume with less than 2.2x the database (plus 16 MB) free; discard any unclaimed earlier export; `VACUUM INTO backup/export/export-<ns>.db`; open that copy (journal mode `DELETE`, so it leaves no `-wal`), run `PRAGMA quick_check`, count feeds, items and starred, and read the OPML (`opml.ExportFrom`) and the non-`sys.` settings from the copy, so all files describe the same instant; close it; write the zip (Deflate) with `kipple.db`, `feeds.opml`, `settings.json`, `RESTORE.txt` and last `manifest.json` (`format, app, kipple_version, schema_version, application_id, created_at, db_sha256, db_bytes, feeds, items, starred, files:[{name, bytes, sha256}]`; the manifest lists every file but itself); rename it into place and delete the copy. The build is bounded to 10 minutes and runs as a background job (§7.1), so its context ends with the timeout or the shutdown, not the request. The `export/` directory is created 0700 and the snapshot copy 0600 before it is filled. The result is held under a random 128-bit token for 5 minutes; a new export replaces an unclaimed one; the download spends the token and deletes the file; an expiry timer deletes it otherwise; a failed or cancelled build removes its files; `backup.New` empties `backup/export/` at startup. What the DB holds that is sensitive (and the export dialog says, as the `warning` field): the web and Reader API password hashes, `account.secret` (keys Reader tokens and image links), hashed session ids, and feeds' `http_auth` in plain text. There is no redaction option: a restore needs the secret.
- **Data lock.** `serve` holds an exclusive OS lock (`flock`, or `LockFileEx` on Windows dev) on `<data>/kipple.lock` for its whole life, taken before the database opens; a second `serve` on the same directory refuses to start. The lock belongs to the process, so a crash leaves no stale lock. `import`, `api-password` and `password` do not take it (they are safe next to a running server); `restore` takes it and so refuses while `serve` runs.
- **`kipple restore <backup.zip|kipple.db|-> [--yes]`.** `-` spools standard input into `<data>/restore-upload.tmp` (the distroless user cannot read a bind-mounted directory, and a zip needs random access). Order: take the data lock; write the candidate to `<data>/restore-tmp.db` (a zip is verified while extracting: manifest format and app, every listed file's size and SHA-256, `db_sha256`; a bare `.db` has only its own checks); run `PRAGMA integrity_check`, `foreign_key_check`, the FTS `integrity-check`, and require `application_id` and `user_version` ≤ the binary's (a newer schema is refused); print the summary; **without `--yes` stop here, changing nothing**; delete the candidate's sessions; move `kipple.db`, `-wal` and `-shm` into `backup/pre-restore-<YYYYMMDD-HHMMSS>Z[-N]/` (UTC; newest 3 kept, ordered by time, where a name without the `Z` from an older version is read as local time; a name taken in the same second gets `-2`, `-3`, never overwritten); a bare `.db` source with a non-empty `-wal` beside it (an undo from a pre-restore copy after an unclean stop) has that WAL copied next to the candidate, so opening it replays and checkpoints the transactions, and the output says so; run as root on Unix, restore warns and chowns the candidate and the pre-restore directory to the data directory's owner (serve runs nonroot and cannot open a root-owned 0600 file); rename the candidate to `kipple.db` (any failure moves the old files back); print the next steps. The next `serve` migrates an older schema after its own pre-migration snapshot. Restore signs every session out, so a stolen old backup's sessions never come back to life.
- **`kipple password [--stdin]`.** Reads the new web password without echo (twice) on a terminal, or one line from standard input; 5 to 256 bytes. In one transaction (`store.ResetPassword`) it stores the argon2id hash, rotates `account.secret` (so every Reader token, an HMAC keyed by it, stops verifying within the Reader API's 5 s account cache) and deletes every session. The Reader API password is unchanged. It opens the store with `NoMigrate` and `NoCheckpoint`, so it is safe while `serve` runs. The web login pacing is in memory and clears on the next restart or when its window ends.
- **`kipple import [-mark-read-older-than-days N] [-move-existing] <file.opml|->`.** Imports an OPML file (or standard input) through `opml.Import`; N is 0 or 1 to 365 and marks items older than N days read on each new feed's first fetch, and `-move-existing` moves feeds that already exist into the file's folders (§7.6). It opens the store with `NoMigrate` and `NoCheckpoint`, so it is safe next to a running server, which fetches the new feeds on its next tick. The JSON report goes to standard output and a one-line summary to standard error.
- **`kipple api-password`.** Takes no arguments. Generates a fresh Reader API password, prints it once and signs the Reader clients out (a running server notices within its 5 s account cache). Same store options as `import`.
- **`kipple version`** prints the version. **No argument runs `serve`.** `restore` accepts `--yes` or `-yes`.

---

## 3. Item id scheme

**Generation** (`internal/store/idalloc.go`, mutex-protected):

```go
func (a *IDAlloc) Next() int64 {
    a.mu.Lock(); defer a.mu.Unlock()
    now := a.clock.Now().UnixMicro()
    if now <= a.last { now = a.last + 1 }
    a.last = now
    return now
}
func (a *IDAlloc) Last() int64 { /* a.last */ }
```

- **Seeding.** At startup: `a.last = max(settings['sys.id_high_water'], MAX(items.id), MAX(trimmed_items.id))`. Both MAX calls are rowid lookups.
  - Each `CommitFetch` that allocated ids ends with `INSERT INTO settings(key,value) VALUES('sys.id_high_water', :last) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = unixepoch() WHERE CAST(excluded.value AS INTEGER) > CAST(settings.value AS INTEGER)`.
  - Ids therefore never go backwards across restarts, unsubscribe cascades or an NTP step back. `stats_events` is no longer scanned.
- **Clock sanity.** If the seed is more than 3600·1e6 µs ahead of `time.Now()`, startup logs an ERROR with the offset, and the health view shows a banner until real time passes the seed: "Item ids are ahead of the clock by X. `ot` windows and mark-all-as-read cutoffs are unreliable until then." Kipple still starts.
- **Where it is called.** Only inside `CommitFetch` (one transaction per chunk of 250 when a fetch has more than 500 items, counting every parsed item, not only the new ones, and never while `rekey_pending` is set; see §4.3, §4.8), after `BEGIN IMMEDIATE` has acquired the write lock. New items in a batch are sorted oldest-first: `published_at` ascending, ties broken by reverse document order; items without a date sort last, stamped with the crawl time. Ids are allocated in that order. Consequences:
  - ids are strictly increasing in commit order;
  - within one fetch, id order equals publication order;
  - a batch of 1000 items runs the counter at most 1 ms ahead of the wall clock.
- **Gaps are harmless.** Ids are never reused: trimmed ids live on in the ledger, and a restore puts the same id back.

**Range.** Today ids are about 1.7587e15: positive, 51 bits, 16 decimal digits. There are 13 significant hex digits, so `%016x` always begins with `000`. For example, 1758700000123456 is `00063f8740c61a40`. The sign bit is never set. The UI JSON carries ids as strings.

**Wire forms** (all derived from the one column):

| Field | Value |
|---|---|
| `itemRefs[].id` | `strconv.FormatInt(id,10)`, a JSON string |
| `items[].id` | `"tag:google.com,2005:reader/item/" + fmt.Sprintf("%016x", uint64(id))` |
| `timestampUsec` | `FormatInt(id,10)`, a string |
| `crawlTimeMsec` | `FormatInt(id/1000,10)`, a string |
| `published` | `published_at`, an int (seconds) |
| `updated` (item) | `COALESCE(updated_at, published_at)`, an int. The envelope's `updated` is `time.Now().Unix()` |
| `continuation` | decimal string of the last id on the page; key omitted on the last page |
| `newestItemTimestampUsec` | `max(id)` as a string |

**Inbound parsing** (`greader/itemid.go`; used for `i=` on contents and edit-tag, GET or POST, repeated):

1. Trim whitespace. If the value is empty, skip it.
2. If it contains `/`, take the text after the last `/`. It must be 1–16 hex digits. Parse with `ParseUint(s,16,64)` and bitcast to `int64`.
3. Else, if it starts with `-`, use `ParseInt(s,10,64)`.
4. Else, if every byte is a digit and the first byte is not `0`, use `ParseInt(s,10,64)`.
5. Else, strip an optional `0x` and require 1–16 hex digits; `ParseUint(s,16,64)` then bitcast. Reeder's bare `0006…` lands here.
6. Anything else is skipped. It is logged at debug level and never produces a 4xx.

The remaining ambiguity is an *unpadded* bare-hex value made only of digits. No target client sends that (§11).

**Cursor and filters** (`ot`, `nt` and `c` are ANDed together):

| Parameter | Meaning |
|---|---|
| `c` | Descending: `id < :c`. With `r=o`: `id > :c`. `c` must be all digits, otherwise it is ignored. **Absent → bound to `math.MaxInt64` (descending) or `0` (ascending), never NULL** |
| `ot` (seconds) | `id >= (:ot − 120)·1e6 OR content_changed_at >= :ot − 120`. Executed as two disjoint ordered legs (§6.5) |
| `nt` (seconds) | `id < (:nt + 1)·1e6` |
| `ot`, `nt` input | All digits, else ignored. A value beyond 4102444800 (year 2100; e.g. milliseconds sent by mistake) is clamped to it and logged at WARN |
| mark-all `ts` | Normalized to µs by digit count: 1–12 → seconds × 1e6; 13–15 → milliseconds × 1e3; 16 → µs; ≥ 17 → ns ÷ 1e3. **Absent, 0, non-digit or overflowing → the larger of `max(items.id)` and `max(trimmed_items.id)` on a reader snapshot** (`MaxCommittedID`: committed ids only, never the allocator, which can be ahead of visible rows). Applied as `id <= ts_us` |

---

## 4. Fetch scheduler

### 4.1 Components

`internal/sched.Scheduler` owns one **dispatcher goroutine** and 8 **worker goroutines** (`KIPPLE_FETCH_WORKERS`). The dispatcher owns these, touched only by itself:

```
flights     map[feedID]*flight     // every queued or running job; flight.started marks the running ones; flight.runs: runs it counts toward
perHost     map[host]int           // cap 2 running per host (KIPPLE_FETCH_PER_HOST)
hostUntil   map[host]time.Time     // Retry-After deadlines (in memory, decision 11)
notBefore   map[feedID]time.Time   // feeds whose last commit failed: not redispatched before this (in memory, §4.6)
commitFails map[feedID]int         // consecutive commit failures per feed (in memory, §4.6)
pending     []*flight              // jobs waiting for a host slot or a worker; FIFO except that a priority job is put at the head
replays     []*flight              // dropped flights whose follow-ups are replayed after drainPending
runs        map[int64]*Run         // every active run by id: manual, import, retention
activeRuns  atomic.Int32           // len(runs), read by Busy() without the dispatcher
running     int                    // jobs handed to workers
live        int                    // workers not yet exited
stopping    bool                   // set once Stop has been called
lastRunID   int64                  // run ids are unix ms, bumped so two never share one
```

A `flight` also carries the reply channels waiting on it, the priority requests it cannot satisfy (`followups`) and the runs' jobs it cannot stand in for (`runFollows`; §4.2).

`Run` holds `{id, kind, total, done, new_items, errors, outstanding}`. `outstanding` counts the jobs queued or in flight that belong to the run. `runs` holds every active run. A second refresh-all joins the active manual run. A new import or retention run starts alongside an older one of the same kind as its own entry: both finish and publish `run.done`, and `GET /api/status` lists every active run (ordered by id), so neither hides the other's progress, and `Busy()` (which the favicon finder yields to) stays true until the last one ends.

Channels (all reply channels are `make(chan T, 1)`, and every dispatcher send uses `select { case ch <- v: default: }`):

- `jobs`: capacity = number of workers
- `doneCh`: capacity = number of workers; carries results and `workerExit`
- `manualCh`: refresh-all, import and retention run requests with reply channels (capacity 8)
- `priorityCh`: per-feed refresh, rekey, trim-only, UI subscribe; each job carries a reply channel (capacity 32)
- `wake`: capacity 1
- `stopCh`: closed by `Stop` to end the dispatcher
- `shutdownCh`: closed by `Stop` so every handler waiting on a reply returns at once (§4.10)
- `stopped`: closed when the dispatcher has returned (`Stopped()`)
- `syncCh`: a barrier used by tests and `Status()`/`HostHolds()`: the dispatcher runs the function it receives

The one-slot commit gate is not a scheduler object. It belongs to `store.DB` (`AcquireGate`) and is taken inside the store: see §4.3.

### 4.2 Tick loop

A `time.Ticker` fires every 30 s (`KIPPLE_SCHED_TICK`). A `wake` signal also triggers a tick. Priority jobs bypass the tick: `handlePriority` takes a fresh snapshot of the feed and puts the job at the head of `pending`. The fetch settings are loaded on every tick, and the tick query runs on the reader pool with a 5 s context:

```sql
SELECT id, url, host, enabled, etag, last_modified, body_hash, user_agent, http_auth, ignore_http_cache,
       disable_http2, allow_insecure_tls, allow_private_net, dedup_mode, rekey_pending,
       interval_minutes, retention, fulltext, redirect_to, redirect_kind, redirect_count,
       consecutive_failures, initial_read_before, last_success_at, ua_fallback
FROM feeds WHERE enabled = 1 AND next_fetch_at <= :now
  AND host NOT IN (SELECT value FROM json_each(:held_hosts))   -- hosts under a Retry-After hold
  AND id NOT IN (SELECT value FROM json_each(:skip_ids))       -- in flight, or backed off after a failed commit
ORDER BY next_fetch_at LIMIT 500;            -- idx_feeds_due; store.DueFeedsExcept
```

Selection rules:

- Feeds already in flight are skipped.
- A feed whose last commit failed is skipped until its in-memory `notBefore` (§4.6).
- A feed whose `hostUntil[host]` is in the future is skipped and stays due.
- These are filtered in the query, not after the `LIMIT`, so 500 or more due feeds on one held host (a 429 with a long Retry-After on a site with hundreds of channels) cannot fill every page and starve the other feeds. The two extra predicates are only added when something is held or skipped.
- Nothing is written at selection time. If the process dies mid-fetch, the feed is simply due again.

`enqueue(job)` registers the flight (`flights[feed]`). If `perHost[job.host] >= 2` or every worker is busy (`running` equals the worker count, so `jobs` cannot be full), the flight is pushed to `pending`. Otherwise the dispatcher marks it started, increments `running` and `perHost`, and sends it to `jobs`. That send is non-blocking; the dispatcher never blocks. A job whose feed has `rekey_pending` set is forced `Full` here (a `not_modified` would never re-key). A job that had to wait in `pending` reloads its feed's snapshot just before it starts (keeping its trigger, `Full` and host deadline), so a URL or settings change made while it waited applies; a fetch or skip whose feed has meanwhile been disabled, or any job whose feed was deleted, is dropped instead: its callers get `ErrDisabled` or `ErrNotFound`, each run it belongs to counts the feed as done with an error, and its follow-ups are replayed.

On a result from `doneCh`, the dispatcher:

1. clears the in-flight mark and decrements `perHost`;
2. applies any Retry-After result to `hostUntil`;
3. for every run in `flight.runs`, increments `done`/`new_items`/`errors` and decrements `outstanding`. A run whose `outstanding` reaches 0 is finished: `run.done` is published and the run is removed;
4. replies to the job's reply channel, if any (non-blocking);
5. publishes SSE events;
6. drains `pending`.

A priority request for a feed that already has a flight is handled by what that flight can do. A flight that has not started (still in `pending`) is upgraded in place for a full refresh (`Full` set, a skip turned back into a fetch) and answers the request itself. Otherwise, when the flight cannot honor the request (a trim, or a full refetch behind a running job), the request is kept whole as a follow-up and replayed on a fresh snapshot once the flight finishes; `drainPending` never drops a flight that has follow-ups, replies or runs waiting on it. A refresh-all or import run attaches to an in-flight feed only when that flight is a real fetch (same rule as a priority refresh); a trim or skip in flight is followed by the run's own job for that feed, and the run stays outstanding meanwhile.

### 4.3 Worker body

```
for job := range jobs:                            // exits when jobs is closed
  res := fetcher.Fetch(fetchCtx, snap)            // HTTP, redirects, charset, hash, parse, uid/content_hash/text_hash,
                                                  // absolutize, sanitize, text, word count, lead image; no DB held
  if res.Cancelled: send result{cancelled}; continue          // nothing written
  cand := pickFulltext(res)                       // outcome ok only; reader pool (items ∪ trimmed_items), no network
  res.Schedule(now)                               // next_fetch_at, backoff or success schedule (§4.6)
  if res.Success():
      ci, err := store.CommitFetch(WithoutCancel(fetchCtx), res)  // each chunk: 10 s budget, gate taken inside the store
  else:
      err := store.CommitFetchError(cctx, res)                     // cctx: WithoutCancel, 10 s
  if err == nil: queueFulltext(cand, ci.Held)     // Held: picked items this commit really inserted, unmuted (empty for a stale fetch)
  doneCh <- result{feed, host, outcome, newIDs, retryAfter}
doneCh <- workerExit
```

- No transaction is held across a network call, a channel send or an SSE write.
- **A job that panics does not crash the process.** Each job runs under `recover()` (stack logged). Before its commit it is recorded as a `parse` error through `CommitFetchError` and backs off; during the commit it is a failed commit (§4.6 in-memory backoff); after a successful commit its committed result stands (no backoff); a trim or skip job becomes an error result and never backs off the feed's fetches.
- **The commit gate lives in `store.DB`, not in the worker.** `store.DB` owns a one-slot commit gate (`AcquireGate`). Every fetch-path write takes it inside the store: each commit chunk (`commitChunk`), the error and skip rows (`CommitFetchError`, `CommitSkip`) and `TrimOnly`. So does every maintenance batch (the nightly purges, auto-read, the filter retro-apply, `CheckpointPassive`). Workers never touch it. `SetFeedUAFallback` and `SaveFulltextIfURL` use a plain `WithWrite` without the gate.
- **Full-text extraction runs after the commit, off the worker.** The worker only picks candidates before the commit (`pickFulltext`) and queues the inserted ones after it (`queueFulltext`), so a slow article host never ties up a fetch worker and `POST /api/feeds/{id}/refresh` returns when the fetch is committed. Why: extracting inline meant up to 60 s of extra worker time per feed behind a 15 s refresh wait, so refreshes of full-text feeds usually answered 202 pending, and a few hanging hosts could starve the whole pool.
  - **Pool.** `FulltextGlobal` (4) goroutines drain one bounded queue (500 items, `FulltextQueue`). A job is handed to a goroutine only when its article host has fewer than 2 running (`FulltextPerHost`), so one slow site cannot block the others. Each extraction has a 10 s timeout, the guarded client and the feed's network flags, read fresh from the database when the job starts. `allow_private_net` and `allow_insecure_tls` apply only to requests for the feed's own host (`feeds.host`) or a variant of it (a subdomain, or the bare/`www.` twin: `fetch.FeedHostVariant`), each redirect hop checked on its own; an article on any other host, or a redirect to one, uses the guarded default transport. When that refuses a request the feed's exception would have allowed (a blocked address, a bad certificate), a WARN names the feed and both hosts and the stored extraction error says the exception does not cover that host (an IP address and a name for the same server cannot be matched without DNS). The on-demand endpoint (§7.5) runs through the same code.
  - **One runner for the process (`internal/ftrun`).** The pool and the on-demand endpoint (§7.5) both extract through one shared `ftrun.Runner`. It is a single-flight keyed by item id: opening an item while the pool is extracting it joins that run instead of fetching the page twice (and the reverse). It also holds the per-article-host limit of 2, so the two paths together never exceed it. The runner saves the outcome inside the run, before it is published to joiners, so nobody is told about a result that is not stored yet, and it is written once. A joined caller whose run was refused by the pool's guard, or cut off by shutdown, runs its own.
  - **A hostile page cannot crash the process.** Each extraction runs under `recover()`: a panic in the parser or sanitizer becomes a stored **permanent** error ("the page could not be processed") and an error log with the panic value and stack.
  - **Duplicate uids in a document.** `AssignUIDs` runs before both the pick and the commit, so the URL the pool extracts is the URL that is stored. In auto mode it keys every occurrence of a repeated guid by its link (or its position when there is no link) and never drops an item; in `link` and `link_title` modes a later item whose uid repeats an earlier one is dropped, because `(feed_id, uid)` is unique.
  - **Bounds.** At most 20 (`FulltextMaxItems`) newest new items per fetch are queued, and no more than the queue has room for. While `fetch.fulltext_all` is on every feed feeds the pool, so a refresh-all would overflow those limits; then the per-fetch cap is 50 (`FulltextMaxItemsAll`) and the queue bound 2000 (`FulltextQueueAll`), never below the plain ones. Concurrency does not scale: still 4 overall and 2 per host, so a big refresh just takes longer to extract (the setting's help text says so), and what is beyond the limits is left to on-demand. The fetch_log note records `fulltext_picked: n` (chosen before the commit, so an upper bound on what was queued) and `fulltext_deferred: m`. Anything picked but not queued because the queue filled or shutdown began is logged with the real counts at warn or info level and left to on-demand. A stale commit (nothing committed, so no new ids) and a full-text mode switched off between the pick and the queueing drop the candidates silently: no log line and no note. The ids come from the commit itself (`CommitInfo.Held`); the post-commit recheck of the mode and the queue limits has its own 5 s deadline, so a slow commit cannot starve it. Deferred items are extracted on demand when opened. Outcomes are logged as they finish (failures at info level). There is no per-run `ok/tried` note any more, since the fetch is already committed and logged by then.
  - **Picking and queueing details.** `pickFulltext` rereads the feed's current mode (`FeedFulltextNow`) rather than the snapshot's, skips items without a URL and items already known (`KnownUIDs`), and leaves out the items a mute rule is predicted to mute. With a re-key pending it picks nothing and adds the note `fulltext: skipped (re-key pending)`, because unmatched items are inserted read; when the known-uid lookup fails it picks nothing and adds `fulltext: skipped (could not check existing items)`. The picked uids go to the commit as `Result.HoldUIDs`: as it inserts each of them that no mute rule muted, the commit adds it to the Reader pending set (`MarkFulltextPending`) inside the chunk's transaction, so the item is held from the moment it becomes visible (a rolled-back chunk clears its marks), and reports them as `CommitInfo.Held`. `queueFulltext` rechecks the mode and queues those items; every mark it does not hand to a queued job (mode now off, queue full or shut, commit error) is cleared at once, and a queued job clears its own when it ends (`ClearFulltextPending`); that set drives the Reader hold (§6.5). The queue dedupes by item id. `fulltext.ready` events carry `"source":"ingest"`, are coalesced over 300 ms and hold at most 500 ids each.
  - **Stored with a small write.** Each result is one `SaveFulltextIfURL` write (one `WithWrite`, not the bulk commit), applied only if the item still exists with the queued URL and its effective full-text mode (§7.5: `COALESCE(items.fulltext_mode, CASE WHEN fetch.fulltext_all THEN 1 ELSE feeds.fulltext END)`) is still 1, checked inside the write transaction. A result for an item whose full text was switched off meanwhile is dropped and not announced. Work for an item that was trimmed or deleted, whose URL changed, that already has a row, or whose full-text setting was turned off is skipped, so editing or deleting the feed meanwhile is safe. Finished items (text or error) are announced with one coalesced `fulltext.ready` event (§7.3). Until then the Reader API and the UI serve the feed's own content, because there is no `item_fulltext` row.
  - **Shutdown and restarts.** Stop cancels the fetch context: running extractions end, queued ones are dropped (logged), the pool goroutines are joined before the scheduler reports stopped, and a cut-off extraction is not stored as a failure. The queue is in memory only: items queued at a crash or restart get no extraction at ingest and are extracted on demand when opened (§7.5).
- A fetch that completed before shutdown still commits, under `WithoutCancel`, each commit chunk with its own 10 s budget, so the fetch_log row is not lost.
- A fetch whose document has more than 500 items (only realistic on a first fetch) is committed in chunks of 250, counting every parsed item, not only the new ones. It is never chunked while `rekey_pending` is set: the re-key runs in a single transaction. The gate is taken and released for each chunk. The trim and the feed bookkeeping run in the last chunk. A crash between chunks is safe. The stale-URL check (a URL PATCH that landed while the fetch was in flight) runs per chunk: if it fires after earlier chunks committed, the remaining chunks, the trim, the bookkeeping and the log row are dropped, the earlier chunks' items stay, and `CommitInfo` reports `Stale` with exactly the items that did commit, so the scheduler still queues full text for them. The remembered browser-UA flag (`SetFeedUAFallback`) is bound to the URL the fetch used (or the permanent-redirect target that same commit migrated to), so a URL PATCH cannot mark a URL that was never tried.
- The stale-URL check covers the error path too: `CommitFetchError` updates `WHERE id = ? AND url = ?`, and when the feed's URL was edited while the fetch ran it writes nothing (no failure count, no fetch_log row).
- Job kinds that skip HTTP are `skip` (writes a `skipped` fetch_log row) and `trim_only` (the retention transaction only). Both take the same gate, inside the store.

### 4.4 HTTP client (`internal/fetch/client.go`)

- **Transport.** One shared `http.Transport`:
  - `DialContext` is a `net.Dialer{Timeout: 10s, Control: ssrfGuard}`. `ssrfGuard` runs after DNS on the concrete IP. It rejects loopback, private (including unique-local), link-local, multicast and unspecified addresses (via `netip`), plus these prefixes (`internal/fetch/ssrf.go`): `0.0.0.0/8`, CGNAT `100.64.0.0/10`, `192.0.0.0/24`, the 6to4 relay `192.88.99.0/24`, `198.18.0.0/15`, `240.0.0.0/4`, IPv4-compatible `::/96`, site-local `fec0::/10`, NAT64 `64:ff9b::/96` and `64:ff9b:1::/48`, 6to4 `2002::/16`, Teredo `2001::/32` and `100::/64`. `allow_private_net=1` turns the guard off only for requests to the feed's own site (see Variants); a hop to any other host is still guarded.
  - `TLSHandshakeTimeout` 10 s, `ResponseHeaderTimeout` 15 s, `MaxResponseHeaderBytes` 64 KiB.
  - `ForceAttemptHTTP2` on, `DisableKeepAlives` on.
  - No `Proxy`: an environment proxy would bypass the dial guard.
- **Variants.** Built lazily and cached: `disable_http2`, `allow_insecure_tls`, and their private-net versions. The image proxy uses the same variants, keyed by its signed flags. When `allow_private_net` or `allow_insecure_tls` is set, the feed fetch wraps its variant so a redirect hop off the feed's site (`fetch.SameSite` or `FeedHostVariant`) goes through the guarded default transport: the exceptions never reach another host.
- **Client.** `http.Client{Timeout: 20s, CheckRedirect: recordHop}`. `recordHop` records every hop's status and allows at most 5 hops.
- **Request headers:**
  - `User-Agent`: `feeds.user_agent` wins and never retries. Otherwise `fetch.user_agent_mode` decides: `default` sends `Mozilla/5.0 (compatible; Kipple/<ver>; +<KIPPLE_PUBLIC_URL>)` (the `+url` part only when configured) and never retries; `browser_always` sends a browser string; `browser_on_failure` (the default) sends Kipple's UA and, on a 403, 406 or a Cloudflare 503 challenge, retries once with the browser string through the same guarded transport, then sets `feeds.ua_fallback = 1` so later fetches use the browser string directly. The browser string is a common Chrome UA unless the `fetch.user_agent` setting supplies a custom one. The same resolution (`store.ResolveUserAgent`) applies to the two other outgoing requests that reach a publisher's site: web add-feed discovery (§4.9; no per-feed override or remembered flag exists yet, so `browser_on_failure` means Kipple's UA and one browser-UA retry on a 403, 406 or Cloudflare 503) and article extraction (§7.5; the per-feed override, the remembered `ua_fallback` and the mode all apply, and `browser_on_failure` retries once as a browser on the same refusals).
  - `Accept: application/rss+xml, application/atom+xml, application/feed+json, application/json;q=0.9, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8`.
  - Basic auth from `http_auth`. `CheckRedirect` drops it from every hop that is not the feed's own host or a subdomain of it (net/http's rule, so `example.com` to `www.example.com` keeps it), over https (plain http is allowed only when the feed URL itself is http): net/http would also keep it for an https to http move.
  - `If-None-Match` and `If-Modified-Since` (verbatim), sent only when stored non-empty, the job is not `full`, and `ignore_http_cache=0`.
  - `Accept-Encoding` is **not** set, so Go negotiates gzip transparently.
- **Body.** `http.MaxBytesReader(nil, body, 10 MiB)` caps the decoded size.
- **Charset** (gofeed only honours the XML declaration):
  1. A BOM wins: UTF-8, UTF-32 (LE `FF FE 00 00`, BE `00 00 FE FF`, checked before UTF-16 because the LE one begins with the UTF-16LE BOM), UTF-16.
  2. Else the XML declaration's encoding, else the HTTP charset, else UTF-8. A UTF-16 or UTF-32 label without a BOM is ignored (the declaration itself would be unreadable), as is an unknown label.
  3. If UTF-8 was chosen but the bytes are not valid UTF-8, retry with the HTTP charset, then `windows-1252`. Conversely, a single-byte label (`windows-125x`, `ISO-8859-x`, `koi8-`, and so on) on a body that is valid multi-byte UTF-8 is decoded as UTF-8: the "declares latin1, sends UTF-8" case.
  4. Look the label up with `charset.Lookup` and decode with `transform.Bytes`, rewrite the `encoding=` attribute to `utf-8`, and compute `sha256` of the decoded bytes: this is `body_hash`.
  5. `gofeed.Parser.Parse`.
- **Content pipeline per item** (`internal/sanitize`):
  1. Resolve the item link against the final feed URL.
  2. Parse the content HTML with `x/net/html` and resolve every URL attribute (`a[href]`, `img[src]`, `img[srcset]` and `source[srcset]` candidate by candidate, `video[poster]`, `video[src]`, `audio[src]`, `source[src]`) against the base chain `xml:base` → item link → `site_url` → final feed URL.
  2a. **Iframe pre-pass** (`IframesToLinks`, before bluemonday). A YouTube or Vimeo (`player.vimeo.com/video/ID`) iframe is kept for the policy. Any other iframe with an absolute http(s) source becomes `<p><a href="src">Embedded content from host</a></p>` (its content is dropped); an iframe without a usable source is dropped. Markup with no iframe is unchanged byte for byte, so only new items with a foreign iframe differ from before. **Stored sandbox.** A kept iframe is stored with `sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"` (set by the pre-pass; bluemonday allows the `sandbox` attribute and filters any value to those four tokens, so a feed cannot widen it). Without this bluemonday writes `sandbox=""`, which blocks the player's script and renders a blank box in Reader clients (Reeder, NetNewsWire) that show the stored HTML. The tokens are the ones the tap-to-load iframe uses (§7.8); they are safe because only the two allowlisted hosts ever get here and neither is a same-origin parent of the app. **Deploy note:** the pass rewrites existing items that contain an iframe on their next fetch, so their hashes change once and Reader clients receive them again as updated (`content_changed_at`); this is expected and deliberately not suppressed (plan, Deployment C6a). A plain link inside the stored iframe was rejected: bluemonday drops iframe content, and a link beside it would show twice in the UI.
  3. bluemonday feed policy with `RequireParseableURLs(true)` and `AllowRelativeURLs(false)`. Iframes survive only for YouTube and Vimeo sources. **In-page anchors.** Every `a[href]` that starts with `#` (any characters after it: `#footnote's-1`, `#a&b`) is swapped for a per-call placeholder URL `https://fragment.kipple.invalid/<random nonce>/<n>` before bluemonday runs and restored to the exact original afterwards, so footnote links survive the no-relative-URL policy. The nonce is fresh per item and unguessable, so a literal `fragment.kipple.invalid` link in feed HTML is left alone. Stored HTML keeps the bare `#frag`; the `kp-` prefixing happens only at serve time (§7.8).
  4. Plain text via `StrictPolicy`, word count, and the lead image (absolute).
  5. `content_hash` and `text_hash`.

### 4.5 Per-outcome handling

"Success bookkeeping" means:

- `consecutive_failures = 0`
- `last_success_at = last_fetch_at = now`, `last_status = <status>`
- `next_fetch_at` from the success schedule (§4.6)
- **`last_error*` is kept**. The UI shows it as resolved when `last_error_at < last_success_at`.
- The retention trim (§5) runs.
- On the feed's first `ok` success (`last_success_at IS NULL` before the commit), `custom_title` is cleared if it equals the document title. Every successful commit clears `initial_read_before`.

"Error bookkeeping" means:

- `consecutive_failures += 1`
- `last_error`, `last_error_class`, `last_error_at`, `last_status` and `last_fetch_at` set
- `next_fetch_at` from the backoff (§4.6)

Every attempt appends a fetch_log row and applies the fetch_log cap (§4.8), except when the feed's URL was edited while the fetch ran: then neither the success nor the error path writes bookkeeping or a log row (§4.3).

| Outcome | Handling |
|---|---|
| 200, parsed | Commit items (§4.8). Store validators from the response: empty when absent, and both dropped when `Expires` is present but not a valid HTTP date (such as `0`; a parseable date in the past does not drop them). Store `body_hash`. Success bookkeeping. Outcome `ok` |
| 200, `body_hash` equals the stored hash | No parse. Update validators. Success bookkeeping. Outcome `unchanged` |
| 304 | Keep the ETag. Overwrite `last_modified` if the 304 carries one. Success bookkeeping. Outcome `not_modified` |
| 301/308 chain | Followed. The final response is handled by its own status. Redirect policy in §4.7 |
| 302/303/307 in the chain | Followed and never persisted. `redirect_to` is set with `redirect_kind='temporary'` (health notice only) |
| 200 with an empty or whitespace-only body | `empty`, backoff. Validators are **not** stored |
| Body over 10 MiB | `too_large`, backoff |
| Unparseable / not a feed | `parse`, backoff. Validators not stored |
| 404, other 4xx, 500/502/504 | `http`, backoff |
| 410 | `gone`: `enabled=0`, `disabled_reason='gone'`. The health view lists it as dead. Re-enabling resets failures and sets `next_fetch_at = now` |
| 401/403 | `http`, backoff. If 403 with header `cf-mitigated: challenge` and an HTML `Content-Type`: `cloudflare`, message "blocked by a Cloudflare challenge (TLS fingerprint); try 'disable HTTP/2' or a browser User-Agent" |
| 429 or 503 | Counted as a failure. `retry_after` comes from `Retry-After` (seconds, or an HTTP-date measured against the response's own `Date` header, falling back to our clock when that is missing, so a publisher clock that is off still gets the wait it meant); if absent it is 1500 s; it is clamped to [60 s, 24 h]. `next_fetch_at = max(now + backoff, now + retry_after)`. The dispatcher sets `hostUntil[host]`. Note `retry_after=<s>s` |
| Timeout | `timeout`, backoff |
| `*net.DNSError` | `dns`, backoff |
| Other `*net.OpError`, `io.ErrUnexpectedEOF`, `*url.Error` | `connect`, backoff |
| x509 unknown authority, hostname error, `tls.RecordHeaderError` | `tls`, backoff. The health view offers `allow_insecure_tls` |
| Guard rejection | `ssrf`. The message includes the resolved IP. The health view offers `allow_private_net` |
| More than 5 hops | `redirect_loop`, backoff |
| Host deadline active | A scheduled job is left due (or dropped from `pending`). A manual, import or plain per-feed refresh writes fetch_log `skipped` (note `skipped: host retry-after until <RFC3339 UTC>`), and the schedule is untouched. A job that becomes held while it waits in `pending` is dropped silently when it is a plain scheduled fetch with nobody waiting, else it becomes a skip. A `full=1` refresh and the subscribe fetch ignore the deadline |
| Fetch cancelled by shutdown before completion | Nothing is written. The feed is due again at the next start |

### 4.6 Backoff and success schedule (exact)

`interval_s = 60 × COALESCE(feeds.interval_minutes, settings.refresh.interval_minutes /*30*/)`.

On a failure, with `n` = `consecutive_failures` **after** incrementing:

```
delay_s         = min(interval_s × 2^(n−1), max(86400, interval_s))
d               = round(delay_s × U(0.85, 1.15))
next_fetch_at   = max(now + d, hostUntil[host] if set)
current_delay_s = d
```

- At the default of 30 min: n=1 → 30 m, then 1 h, 2 h, 4 h, 8 h, 16 h, and 24 h from n=7 on, each ×0.85–1.15.
- At a 7-day interval: every failure retries after 7 days (the cap is `max(86400, interval_s)`), so a failing feed never polls more often than a healthy one.
- A success resets the count. A feed is never auto-disabled except on 410.
- When the commit itself fails (the database write errors), nothing is persisted, so `consecutive_failures` does not move. The scheduler keeps an in-memory count of consecutive commit failures per feed and backs the feed off with the same schedule using `n = consecutive_failures + commit_failures` (same cap, so a permanently failing database costs at most one retry per feed per 24 h or per interval). A commit that succeeds clears the count.

On success:

```
hint_s        = fetch.honor_publisher_ttl ? max(RSS <ttl>×60, Cache-Control s-maxage (else max-age) − Age, Expires − Date, 0) : 0
              (Date is the response's own Date header, so a publisher clock that is off cancels out;
               without a valid Date it is now. An unparseable Expires contributes nothing)
              (a response with Cache-Control no-cache, no-store or private contributes only the RSS <ttl>)
d             = round(max(interval_s, min(hint_s, 86400)) × U(0.95, 1.05))
next_fetch_at = now + d;  ttl_hint_s = hint_s;  current_delay_s = d
```

Changing `refresh.interval_minutes` runs `store.PullInSchedule`, followed by a scheduler `Wake`:

```sql
UPDATE feeds SET next_fetch_at = last_fetch_at + max(:new_interval_s, CASE WHEN :honor_ttl THEN min(coalesce(ttl_hint_s, 0), 86400) ELSE 0 END)
WHERE enabled = 1 AND interval_minutes IS NULL AND consecutive_failures = 0
  AND last_fetch_at IS NOT NULL
  AND next_fetch_at > last_fetch_at + max(:new_interval_s, CASE WHEN :honor_ttl THEN min(coalesce(ttl_hint_s, 0), 86400) ELSE 0 END)
```

`:honor_ttl` is `fetch.honor_publisher_ttl`. The statement never postpones a feed (a raised interval applies from each feed's next fetch) and leaves never-fetched feeds alone. A per-feed interval change takes effect from the feed's next fetch (no reschedule, no wake).

Health statuses, computed at read time by one function, `store.FeedStatus(row, hostUntil, now)`, used by `/api/bootstrap` and `/api/health/feeds`. The first matching row wins:

| Status | Condition |
|---|---|
| `archive` | `disabled_reason='archive'` (shown separately, never as an error) |
| `dead` | `disabled_reason='gone'` |
| `disabled` | any other `disabled_reason` (`user`), or `enabled=0` without a reason |
| `failing` | `consecutive_failures ≥ 14` |
| `erroring` | 1–13 consecutive failures |
| `throttled` | the scheduler's host deadline (`hostUntil`) for the feed's `host` is in the future |
| `redirecting` | a **permanent** redirect is pending (`redirect_kind='permanent'` and `redirect_to` set). A temporary redirect is only a notice on a feed whose status stays `ok` |
| `silent` | healthy, but `COALESCE(last_new_items_at, created_at) < now − 90 d` |
| `ok` | none of the above. A resolved `last_error` is shown greyed with its time |

The scheduler exposes its live host deadlines through `Scheduler.HostHolds()`, answered on the dispatcher goroutine like `Status()` (bounded wait, empty at shutdown).

### 4.7 Redirect policy

`final` is `resp.Request.URL`, normalized: lowercase scheme and host, default port stripped. The parse base URL is always `final`.

- If `final == feeds.url`, clear `redirect_to`.
- Else, if every hop was 301/308 and the chain is not an https→http downgrade:
  - If only the scheme changed from http to https on the same host, with identical path and query, **migrate now**.
  - Else, if `redirect_to == final`, increment `redirect_count`. When it reaches 3, migrate.
  - Else set `redirect_to = final`, `redirect_kind = 'permanent'`, `redirect_count = 1`.
- Else (any temporary hop, or a downgrade): set `redirect_to = final`, `redirect_kind = 'temporary'`, `redirect_count = 0`. The URL is never rewritten.

To **migrate**, first run `store.FindFeedByURL(tx, final)`. If it finds a different feed, do not migrate: keep `redirect_to` and add the note `redirect_target_owned_by_feed <id>`; the health view suggests a merge. Otherwise:

```sql
UPDATE feeds SET url_original = COALESCE(url_original, url),
                 url_original_key = COALESCE(url_original_key, url_key),
                 url = :final, url_key = :final_key, host = :host,
                 redirect_to = NULL, redirect_kind = NULL, redirect_count = 0,
                 -- :moved = the new host is on another site (fetch.SameSite is false)
                 http_auth = CASE WHEN :moved THEN NULL ELSE http_auth END,
                 allow_insecure_tls = CASE WHEN :moved THEN 0 ELSE allow_insecure_tls END,
                 allow_private_net = CASE WHEN :moved THEN 0 ELSE allow_private_net END
WHERE id = :f
```

Then add the fetch_log note `redirect_migrated: <old> -> <new>` with `keep = 1`. `http_auth`, `allow_insecure_tls` and `allow_private_net` were granted for the old host. A move inside the same site (`fetch.SameSite`: the same registrable domain, such as `example.com` to `www.example.com`, or a single-label LAN name gaining a local suffix no public registrant can hold (`.lan`, `.local`, `.home.arpa`, `.internal`, `.localdomain`, `.home` or `.corp`), `nas` to `nas.lan`; never an arbitrary longer name such as `nas.attacker.example`) keeps them. A move to another site while any of them is set is **not migrated**: `redirect_to` stays pending (`redirect_count` held at 2) with the note `redirect_held_new_site: <new> is on another site; ...`, and the user accepts the move by editing the URL, which clears them unless the edit sets them, as a URL edit to another site does (§4.9; in the web editor, "Keep for the new address" sets them). A move to another site with none set migrates (the `:moved` clearing is then a no-op kept as a guard). Validators are kept. The `feed/<url>` stream lookup uses `FindFeedByURL`.

**`FindFeedByURL(u)`** (`internal/feedurl.Key` computes the key):

- Parse `u`, lowercase the scheme and host, strip the default port and the fragment, and keep the path and query verbatim.
- `key = host[:port] + path + ("?" + query if any)`. The scheme is dropped, so http and https variants collide by design.
- `SELECT … FROM feeds WHERE url_key = :k OR url_original_key = :k LIMIT 1`.

### 4.8 Dedup and CommitFetch (`internal/fetch/dedup.go` computes, `internal/store/fetchcommit.go` applies)

**uid.** `h(x)` is sha256 truncated to 32 hex. The rules are frozen and versioned by prefix.

- `auto`:
  - `'g:'+h(trimmed guid)` when the RSS `guid`, Atom `id` or JSON `id` is non-empty.
  - Else `'l:'+h(raw link)`, using the link as written in the document, before absolutization or tracking-param cleanup.
  - Else `'h:'+h(title+"\x1f"+text)`.
  - A guid repeated within one document is keyed on **every** occurrence, first included, as `'g:'+h(guid + "|" + rawLink)`, so a uid never depends on the item's position. An occurrence with no link is keyed `'g:'+h(guid + "|" + n)` with `n` the occurrence index. If the same guid and the same link repeat, the key becomes `'g:'+h(guid + "|" + rawLink + "|" + k)` with `k` bumped until it is free.
- `link`: `'l:'+h(raw link)`, falling back to the `h:` rule when there is no link.
- `link_title`: `'l:'+h(raw link + "\x1f" + title)`, falling back to the `h:` rule when there is no link.
- In `link` and `link_title` modes a later item whose uid repeats an earlier one in the same document is dropped, because `(feed_id, uid)` is unique.
- If more than 5% of the items that carry a non-empty guid repeat an earlier guid in the same document, the fetch adds the note `guid_duplicates: k/n` (k repeats, n items with a guid). Empty guids are not counted. Nothing switches automatically.

**Hashes.** `content_hash` = `h(title | url | author | sanitized content_html)`. `text_hash` = `h(title | content_text)`. Dates are excluded from both.

**Parse-time behavior** (`internal/fetch/parse.go`). An entry that cannot be converted (malformed, or with nothing usable) is dropped and the fetch adds the note `skipped_malformed_items: k/n`. An enclosure-only entry (no guid, link, title or text) is kept: its first media URL (else its image) becomes the guid and its title is the file name. `feedburner:origLink`, when present, wins over the link for the item's `url` (the raw link still feeds the uid). When the initial-read window marks new items read, the fetch adds the note `initial_read: k`.

**The transaction** (inside `WithWrite`; `first_new_id` = the first id allocated in this transaction, or MaxInt64 when none):

```
BEGIN IMMEDIATE
existing := SELECT id, uid, content_hash, text_hash, title, author FROM items
            WHERE feed_id = :f AND uid IN (SELECT value FROM json_each(:uids))        -- autoindex, narrow rows
tomb     := SELECT uid FROM trimmed_items WHERE feed_id = :f AND uid IN (SELECT value FROM json_each(:uids))
for each parsed item, oldest-first:
  uid ∈ tomb     → collect for last_seen bump (never re-inserted)
  uid ∈ existing → if content_hash differs:
                     UPDATE items SET <only changed among title, author, url, image_url, word_count>,
                        content_hash, text_hash, updated_at = COALESCE(:feed_updated, updated_at),
                        content_changed_at = CASE WHEN text_hash changed THEN :now ELSE content_changed_at END
                     WHERE id = :id;                                  -- read/starred/published_at/sort_at untouched
                     UPDATE item_content SET content_html = :h, enclosures_json = :e
                        [, content_text = :t  -- only when text_hash changed]
                     WHERE item_id = :id;
                     updated++
  else           → new
if rekey_pending and outcome ok and this is a single-chunk commit:
                  for each new item whose absolutized url uniquely matches an existing row of this
                  feed not in the document by uid, and no other new item in the document has that URL:
                  UPDATE items SET uid = :new_uid (state kept); rekeyed++.
                  Remaining new items are inserted with read = 1. rekey_pending = 0. Note 'rekeyed: k' (keep = 1).
for each new item (oldest-first):
  id = alloc.Next()
  read = 1 if rekey leftover, or if initial_read_before IS NOT NULL AND published_at < initial_read_before
  verdict = filters.Evaluate(item)        -- Go, on the parsed item; the compiled set is cached (below); no I/O
  read = 1 if verdict.read; starred = verdict.star; muted_by = verdict.muted_by (the lowest matching mute rule id, else NULL)
  INSERT INTO items(id, feed_id, uid, url, title, author, image_url, word_count, content_hash, text_hash,
                    published_at, updated_at, sort_at, read, read_at, starred, starred_at, muted_by, muted_was_read)
    -- published_at = PublishedParsed ?? UpdatedParsed ?? id/1e6 (the feed's value is kept even when bogus)
    -- sort_at = min(published_at, id/1e6 + 86400)
  INSERT INTO item_content(item_id, content_html, content_text, enclosures_json, categories_json)     -- FTS insert fires here
UPDATE filters SET hits = hits + :n, last_hit_at = :now WHERE id = :rule     -- per matched rule whose action took effect, per chunk
UPDATE trimmed_items SET last_seen_at = :now WHERE feed_id = :f AND uid IN (SELECT value FROM json_each(:seen_tomb))
retention trim (§5), with :first_new_id
churn check: IF new ≥ 10 AND new ≥ 0.8 × document size AND the feed had ≥ 20 items before
             → note 'guid_churn_suspected' (keep = 1; items stay unread; the health view offers "mark this fetch read")
apply the §4.7 redirect decision (its own UPDATE of redirect_*, or the migration UPDATE)
UPDATE feeds SET title = CASE WHEN :doc_title != '' THEN :doc_title ELSE title END,     -- outcome ok only
       site_url = CASE WHEN :site != '' THEN :site ELSE site_url END,
       description = :desc,
       custom_title = CASE WHEN last_success_at IS NULL AND custom_title = :doc_title THEN NULL ELSE custom_title END,
       rekey_pending = 0 WHERE id = :f
UPDATE feeds SET etag = CASE WHEN :set_validators THEN NULLIF(:etag,'') ELSE etag END,  -- every success outcome
       last_modified = CASE WHEN :set_validators THEN NULLIF(:lm,'') ELSE last_modified END,
       body_hash = CASE WHEN :hash != '' THEN :hash ELSE body_hash END,
       initial_read_before = NULL,
       last_fetch_at, last_success_at, last_status, consecutive_failures = 0,
       next_fetch_at, current_delay_s, ttl_hint_s,
       last_new_items_at = CASE WHEN :new > 0 THEN :now ELSE last_new_items_at END,
       updated_at = :now WHERE id = :f
INSERT INTO settings('sys.id_high_water', :alloc_last) ON CONFLICT … (monotonic, §3)   -- when ids were allocated
INSERT INTO fetch_log(... first_item_id, last_item_id, keep ...);
DELETE FROM fetch_log WHERE feed_id = :f AND keep = 0
  AND started_at < :now - 14*86400
  AND id < (SELECT id FROM fetch_log WHERE feed_id = :f ORDER BY id DESC LIMIT 1 OFFSET 49)   -- NULL (< 50 rows) deletes nothing
  AND id NOT IN (SELECT id FROM fetch_log WHERE feed_id = :f AND outcome = 'error' ORDER BY id DESC LIMIT 10);
COMMIT
```

The `unchanged` and `not_modified` outcomes run the same transaction without the item section and without the `ok`-only feed UPDATE (title, site URL, description, `custom_title`, `rekey_pending`): the trim, the every-success feed UPDATE (validators, `body_hash` when non-empty, the schedule), and the fetch_log row and cap. The title and site URL keep their old values when the document's own is empty.

**Filters at ingest** (`internal/store/filters.go`, engine in `internal/filter`). Only *new* items are evaluated: an item that already exists is never re-evaluated, so a later edit cannot flip its state, and a tombstoned uid is never re-inserted. `DB.filters` returns the compiled rule set of the current generation: `DB.filterGen` is bumped by every filter write (create, patch, delete, and the cascade of a feed or folder delete) inside that write's own transaction, before it returns, so a fetch commit that takes the writer right after it cannot read a stale set, and the set is rebuilt only when the generation moved, so an ingest with unchanged rules does one atomic load and no query. The set is loaded only inside a write transaction (the ingest path), and write transactions are serialized, so it can never be rebuilt from stale rows; the full-text pre-pick (`DB.MutedUIDs`) compiles its own set from the reader pool and never touches the cache; it takes the fetched document's title and resolves a rule's `feed` field exactly as the commit does (custom title, else stored title, else the document title), so prediction and commit cannot disagree on a new subscription. A stored rule that no longer validates is logged and skipped, and a load failure ingests without rules: a filter problem never fails a fetch. With no rules the hook is skipped entirely, so ingest behaves exactly as before.

- A `mute` match sets `read = 1, read_at = now, muted_by = <lowest matching mute rule id>` and records `muted_was_read` (the read state before any rule: 1 for an item read by the initial-read window or a rekey leftover, else 0); `mark_read` sets `read = 1` alone; `star` sets `starred = 1, starred_at = now` (and cancels a mute: the item is not muted, though a `mark_read` rule may still read it). `highlight` stores nothing.
- A match counts as a hit only when its action took effect: the mute a star cancels is not a hit, and a `mark_read` on an item that was already read (initial-read window, rekey leftover) is not one. The rules' `hits` and `last_hit_at` are written in the chunk's own transaction.
- The fetch_log note is `filters: muted k, marked_read k, starred k` (`keep = 0`; an item that is both muted and matched by `mark_read` counts as muted). `CommitInfo` carries `MutedIDs`, `Muted`, `MarkedRead`, `Starred`; `NewIDs` still lists every new item.
- Muted items are never queued for full-text extraction: the worker drops `MutedIDs` from the ids it hands `queueFulltext`, and `pickFulltext` already leaves out the items it predicts will be muted (`MutedUIDs`), so they do not use the per-fetch cap. A muted item is therefore never held back by the Reader hold either (§6.5).
- `fetch.done` (§7.3) leaves muted ids out of `new_item_ids` and adds `muted_items`.
- A rule that scans the `category` field never fires on an item without categories, inverted or not: no stored categories means unknown, not "does not match" (an inverted category rule would otherwise mute every item fetched before migration 0004). The same holds in the preview and the retroactive apply.
- `item_content.categories_json` is filled for every new item, filters or not: the feed's categories, trimmed, blank and case-insensitive repeats dropped, at most 20 of 100 runes each. `content_hash` does not include them and an update never rewrites them.

On the **first fetch** of a new subscription, every item is inserted unread (or read, per `initial_read_before`), and the trim then applies N. Items trimmed in this same transaction are not added to `trimmed_unread_count`, because their ids are ≥ `first_new_id`.

A dedup-mode change through the UI or API sets `rekey_pending = 1` and nothing else. The re-key runs on the feed's next fetch (scheduled, refresh-all or per-feed), which is forced `Full` while `rekey_pending` is set. The editor shows "Duplicate detection is being re-applied to this feed." while it is pending. There is no confirm dialog.

`CommitFetchError` is one short transaction: error bookkeeping, then the fetch_log row and cap.

### 4.9 Manual refresh, priority jobs, runs

- **Refresh all** (button, pull-to-refresh, `r` key → `POST /api/refresh`) sends to `manualCh`.
  - If a **manual** run is active, the reply is `202 {run_id, total, joined: true}`. An active import or retention run is never joined: a manual run starts alongside it.
  - Otherwise the dispatcher creates a run (`id` = unix ms, `kind='manual'`). For every enabled feed:
    - if the feed is in flight, the run is **attached** to that flight (`outstanding++`), because a fetch happening now counts as "fetched now";
    - else, if its host has an active `hostUntil`, a `skip` job is enqueued;
    - else a `trigger='manual'` job is enqueued, **ignoring `next_fetch_at` and `consecutive_failures`**.
  - Each attached or enqueued feed adds 1 to `outstanding`, and `total = outstanding` at creation. If `total = 0`, `run.done` is published at once.
  - The run ends when `outstanding` reaches 0, which is guaranteed because every attached flight and every job returns through `doneCh`. The next press then starts a new run.
  - Validators are still sent. A failure increments the failure count and reschedules normally.
- **Per-feed refresh** (health view, `POST /api/feeds/{id}/refresh[?full=1]`) is a priority job. `full=1` drops validators and the body-hash short-circuit for that attempt. The handler waits on its buffered reply channel, a 15 s timer, and the scheduler's `shutdownCh`, whichever fires first. After the timer it returns `202 {pending:true}`, and the late reply is dropped harmlessly. A disabled feed (including `gone`) answers `409 disabled` and must be re-enabled first.
- **Add feed in the web UI** (`POST /api/feeds`): synchronous discovery through the guarded client (10 s timeout), insert with `next_fetch_at = now`, then a `Full` priority job (`trigger='subscribe'`) that ignores a host Retry-After deadline. The handler waits up to 8 s the same way; if the submit fails it falls back to a `wake`.
- **Add feed through the Reader API** (`quickadd`, `subscription/edit ac=subscribe`): see decision 33. By default there is no outbound HTTP. The feed is inserted with `next_fetch_at = now`, the dispatcher gets a non-blocking `wake`, and the call returns at once. `greader.subscribe_fetch_now` (default off) opts a client subscribe into a bounded 8 s first fetch like the web UI path; off, the Reader API never fetches inline.
- **OPML import in the web UI** (`POST /api/opml`) creates an `import` run over the new feeds. **`subscription/import` through the Reader API** inserts the feeds with `next_fetch_at = now` and only sends `wake`; the scheduler fetches them as ordinary due feeds. Existing feeds are not fetched in either case (the web option `move_existing` may move them to another folder).
- **Retention changes.** `PATCH /api/feeds/{id}` with a new `retention` enqueues a priority `trim_only` job for that feed, which trims it even when the feed is disabled (a trim touches no network). `PATCH /api/settings` with a new `retention.default` creates a `retention` run of `trim_only` jobs over the enabled feeds that inherit the default. "Apply retention now" (`POST /api/retention/apply`) does the same over every enabled feed. These are user actions, never page loads.
- **Other `PATCH /api/feeds/{id}` effects.** A URL change or a re-enable submits a `Full` priority fetch (falling back to a `wake` when the submit fails). A `dedup_mode` change only sets `rekey_pending` (§4.8). A change of `user_agent`, or of the URL, resets `ua_fallback`. Moving the feed to another host clears `http_auth`, and moving it to another site (`fetch.SameSite` false) also resets `allow_insecure_tls` and `allow_private_net` (the new URL is validated without the old private-network grant), each unless the same request sets it (the web feed editor sends both as shown only when the user picks "Keep for the new address", which it offers next to a changed URL while either is on, and says so when a save turned them off). A per-feed `interval_minutes` change only writes the column (§4.6).
- **API clients never reach any of this**, except for the inserts with `next_fetch_at = now` described above (decision 33). No Reader endpoint touches `next_fetch_at` of an existing feed or the queues.

### 4.10 SSE streaming and shutdown

- **`GET /api/events`** (session-authenticated):
  - Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`, `X-Accel-Buffering: no`, no `Content-Length`. The handler calls `http.Flusher` after every event.
  - Every 15 s a `: ping` comment goes out, well under Cloudflare's 125 s proxy read timeout, followed by a named `heartbeat` event (`data: {"t":<unix>}`, no `id`), which `EventSource` can observe (§7.3).
  - Each subscriber has a buffered channel of 64. When it overflows, the hub drops the channel and sends one `resync` event.
  - The stream opens with `retry: 3000` and a `: connected` comment. Each write has its own 10 s deadline (the server-wide `WriteTimeout` would kill the stream).
  - Every heartbeat re-checks the client's session and closes the stream when it was revoked (sign out other sessions).
  - The handler holds no database connection while waiting. Event ids are monotonic across restarts (the hub seeds its counter from the clock in microseconds). `Last-Event-ID` replays from a ring buffer capped at 500 events and 1 MiB of payload. The cursor is also accepted as `?last_event_id=` (the header wins). The subscriber gets a single `resync` event instead of a replay when the ring no longer reaches back to the cursor, when 64 or more events would be replayed, or when the cursor is ahead of the hub.
  - Event list in §7.3.
- **Graceful shutdown.** `signal.NotifyContext(SIGINT, SIGTERM)`, then:
  1. `sched.Stop()`:
     - stop the ticker and close `shutdownCh`, so every handler waiting on a reply returns 503 (UI) at once;
     - refuse new runs and priority jobs;
     - cancel the fetch context so in-flight HTTP aborts;
     - close `jobs`.

     Workers finish the job in hand: a completed fetch commits under `WithoutCancel` (the context carries no deadline; each chunk of a large feed gets its own 10 s budget), and an aborted one writes nothing. Each worker then sends `workerExit`.

     Right after it, `favicon.Finder.Cancel()` cancels an icon lookup in progress (which writes nothing) without waiting (§4.11).
  2. `hub.Close()` closes every subscriber channel, so SSE handlers return immediately.
  3. `http.Server.Shutdown` with a context of at most 10 s; when it errors, `srv.Close()` cuts the requests that outlived the grace period. No handler waits on the scheduler any more.
  4. `<-sched.Stopped()`, bounded at 15 s (it logs "scheduler did not drain in time" and goes on). The dispatcher keeps receiving on `doneCh` until `live == 0`, so no worker can block on its final send.
  5. `favicon.Finder.Stop()` joins the finder's goroutine, then `maint.Stop()` cancels the maintenance context. An in-progress `VACUUM INTO` is interrupted and its tmp file is removed at once; a leftover is removed again at the start of the next run.
  6. The deferred closes run, last in first out: the API server's `Close`, then the image cache's `Close`, then `db.Close()`. `db.Close()` closes the reader pool, runs `PRAGMA wal_checkpoint(TRUNCATE)` on the writer with a 5 s timeout, and closes the writer.

  All of it shares one 25 s budget (`cmd/kipple/shutdown.go`), started by the signal: each stage is capped by its own maximum and by what is left, steps 3 to 5 stop 5 s before the deadline so the closes of step 6 keep that reserve. Of it, the last 3 s belong to the store's close (the checkpoint) alone: stopping maintenance (step 5) still gets at least 1 s when the earlier steps used their whole share, but never past that 3 s mark, and the UI API and image cache closes (which run before the store's) are abandoned at it. The store's close is abandoned (logged) at the deadline. The scheduler, maintenance and the favicon finder start only after every handler is built, so a setup error leaves nothing running; the finder's `Stop` is also deferred (before the store's close, inside the same reserve), so no return path leaves it running while the store closes. Compose sets `stop_grace_period: 30s`. The shutdown test asserts exit in under 15 s during a 138-feed run.
- **Startup.** Feeds that are already past due are simply due on the first tick. A full catch-up of 138 feeds takes about 30 s.

### 4.11 Feed icons (`internal/favicon`)

`feed_icons` is filled by one background goroutine, the favicon finder. It is not part of the scheduler and never runs on the fetch path: a feed fetch neither waits for it nor learns of its outcome.

- **When.** Every minute (`Poll`) the finder asks `store.NextIconJob` for the most overdue feed and runs lookups one at a time, at least 2 s apart when they use the network, at most 30 per pass. A feed is due when it is enabled, has had at least one successful fetch (`last_success_at` set), and either has no `feed_icon_checks` row (first lookup, so a new feed gets its icon within a minute or two of its first fetch), or its `next_check_at` has passed, or its site changed since the last lookup. The site's identity is `store.IconSiteKey`: the host (and a non-default port) of `site_url` when it is an absolute http(s) URL, else of the feed URL; the scheme, path and query are not part of it, so a link that changes on every fetch (a session id, a tracking parameter, http/https flapping) is not a change and keeps its backoff. The feed's own host is part of the identity too (the network exceptions are scoped to it), so a feed that moves to another host is looked up again even when `site_url` stays. A changed site is due at once and its failure count starts over. The pass ends early while `sched.Scheduler.Busy()` is true: a scheduler run (refresh-all, import, retention) is active or the scheduler is stopping. Busy reads an atomic count kept in step with the runs map, so it never waits on the dispatcher (`Status`, which gives up after 2 s and then reports no runs, would read a loaded dispatcher as idle); the finder then catches up one feed at a time.
- **Pacing per site.** One site (`IconSiteKey`) is looked up at most once every 10 minutes. The finder remembers each lookup's outcome for that long, with its network scope (the feed host and exceptions, or the guarded default): a feed of the same site under the same scope takes that outcome (icon or failure) without a request, and one under another scope is passed over by `NextIconJob` until the 10 minutes are up. So 30 feeds of one site (subreddits, channels) cost one home-page fetch.
- **Schedule.** After a success the next lookup is 7 days later (at most weekly; the icon is replaced when it changed). After failure *n* it is 6 h × 2^(n−1), capped at 7 days: 6 h, 12 h, 1 d, 2 d, 4 d, then weekly. A failure keeps any icon already stored. Failures are recorded only in `feed_icon_checks` (`failures`, `last_error`, logged at debug level): the feed's `consecutive_failures`, `last_error` and the health view are never touched.
- **Lookup.** The page is `site_url` when it is an absolute http(s) URL, else the origin of the feed URL. The finder fetches the page (at most 512 KiB are read; a failing page is not fatal), collects the head's `<link rel>` icons with `icon` or `apple-touch-icon(-precomposed)` among the rel tokens (so `shortcut icon` counts, `mask-icon` and `fluid-icon` do not), resolved against the final URL and the first `<base href>`, at most 32. SVG is dropped by `type` or `.svg` path, as are `data:` and any other non-http(s) URL. Candidates are ranked by the largest side in `sizes`: 32 to 180 px first (closest to 64), then unknown sizes (an unsized apple-touch-icon counts as 180), then the rest (closest to the band), document order breaking ties. `/favicon.ico` at the final page's origin is always the last candidate. At most 4 icon fetches are made per lookup (the top 3 links plus the fallback).
- **Limits.** Every request goes through `fetch.Client.Transport(allow_private_net, allow_insecure_tls, disable_http2)` with the feed's own flags: the dial-time SSRF guard applies to every hop. The feed's `allow_private_net` and `allow_insecure_tls` cover only requests to the feed's own host (`feeds.host`), a subdomain of it, or its bare/www. twin (`fetch.FeedHostVariant`, the rule of full-text extraction and the image proxy): `favicon.ScopedTransport` picks the transport per request, so per redirect hop, and a `site_url`, icon link or redirect on any other host goes through the guarded default. The User-Agent is the one the feed's fetch resolves to, with the same one-time browser retry on 403/406. HTTP credentials are never sent: userinfo in the page URL, an icon link or a redirect `Location` is dropped before the request (Go's client would otherwise send it as Basic auth) and never stored in `source_url`. Each request has a 10 s timeout (body included), a lookup 45 s overall, at most 5 redirects (http(s) only). An icon is at most 256 KiB (by `Content-Length` and by the bytes read).
- **Accepted bytes.** The type is sniffed from the bytes; the response's `Content-Type` is ignored. Only PNG, JPEG, GIF, WebP (their headers must decode, sides 8 to 2048 px) and ICO (1 to 64 directory entries, each inside the file; the first entry at least 8 px, and its payload a PNG whose header decodes within the same side limits or a DIB starting with a 40-byte `BITMAPINFOHEADER`) pass, stored as `image/png`, `image/jpeg`, `image/gif`, `image/webp` or `image/x-icon`. SVG, HTML and anything else are rejected, which matches what the Reader `/icon/` route serves (§6.9).
- **Write.** `store.SaveIconCheck` upserts `feed_icons` (`data`, `content_type`, `source_url` = the final icon URL, `hash` = the first 16 hex characters of its SHA-256, `fetched_at`) and the check row in one transaction, only if the feed still exists with the same site identity and feed host the lookup used; otherwise it writes nothing and the changed feed is simply due again. The icon's bytes are rewritten only when its `hash` changed; a recheck that finds the same icon updates `fetched_at` and `source_url` alone. The `hash` changes with the bytes, so Reader `iconUrl` (`/icon/<id>-<hash>`) and the UI's `/api/feeds/{id}/icon?h=<hash>` bust caches by themselves.
- **Shutdown.** `Cancel` (step 1 of §4.10) cancels the lookup in hand, which writes nothing, so the feed stays due; `Stop` joins the goroutine before maintenance stops, and again, deferred, before the store closes.

---

## 5. Retention, the trimmed ledger and restore

**Setting.**

- `settings.retention.default` is one of {50, 100, 250, 500, 1000, 0}, where 0 means unlimited. The default is **250**.
- `feeds.retention` uses the same values; NULL means inherit. The archive feed is fixed at 0.
- `N = COALESCE(feeds.retention, default)`. When `N = 0` the trim is skipped.
- Muted items (`muted_by IS NOT NULL`) count against N like any item but are ranked in their own list, newest first, so noise cannot displace real items and a fetch cannot trim the muted items it just added. With `real` and `muted` the counts of non-starred, non-held items, the feed keeps the newest `min(muted, max(N/5, N - real))` muted items and the newest `N - that` real ones: muted items are always allowed a fifth of the cap, and more while real items leave room. With a cap of 50, 40 real and 30 muted items keep all 40 real and the 10 newest muted ones; with 50 real and a fetch that brings 2 muted and 1 real, the 3 oldest real items go and both muted ones stay (the Muted view and delete-with-unmute keep working on a busy feed). The per-feed sort is cheap; the batch bound below keeps each trim transaction short however large the backlog. A restore brings an item back with `muted_by = NULL`.
- N counts **non-starred, non-held** items only. "Keep newest N" means the N newest unstarred items, plus every starred item, plus items whose `retain_until` is still in the future.
- `settings.retention.restore_days` (0–180, default **90**; `MaxRestoreDays` caps it at 180, the base of the nightly ledger purge horizon `max(180, restore_days + 7)`, so a stub never goes before its window ends) is how long a restore stub is kept.
- **Settings reads never guess.** A missing row or an unparseable value is the default; a *failed* read (cancelled context, I/O error) is an error, not "not set". Inside a write transaction (retention trim, restore, the stub and ledger purges, the full-text guard) it fails the transaction so the batch retries; outside one (`store.LoadFetchSettings`) it is logged at warn and the defaults apply to that pass only. Nothing caches a value that came from a failed read.

**When.**

- In every successful fetch commit: `ok`, `unchanged` and `not_modified`.
- In `trim_only` jobs: a per-feed retention PATCH, a `retention.default` change, or "Apply retention now".
- Never on page load, never on Reader API traffic, never on a timer (the only delayed trim is the continuation of an unfinished one, below).

**Exact SQL** (inside the fetch or `trim_only` transaction; validated):

```sql
CREATE TEMP TABLE IF NOT EXISTS trim_set (id INTEGER PRIMARY KEY) STRICT;
DELETE FROM temp.trim_set;
-- :keep_muted = min(muted, max(N/5, N - real)); real and muted are counted first, over the same WHERE
INSERT INTO temp.trim_set(id)
  SELECT id FROM (
    SELECT id, sort_at, muted_by IS NOT NULL AS m,
           row_number() OVER (PARTITION BY muted_by IS NOT NULL ORDER BY sort_at DESC, id DESC) AS rn
    FROM items
    WHERE feed_id = :feed AND starred = 0 AND (retain_until IS NULL OR retain_until <= :now))
  WHERE rn > CASE WHEN m THEN :keep_muted ELSE :N - :keep_muted END    -- real and muted items are ranked apart
  ORDER BY sort_at ASC, id ASC LIMIT :batch;                            -- oldest first, bounded (see below)

INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
  SELECT i.id, i.feed_id, i.uid, i.read, :now, :now
  FROM temp.trim_set t CROSS JOIN items i ON i.id = t.id   -- CROSS: walk trim_set (see below)
  WHERE true                                   -- required before ON CONFLICT with INSERT … SELECT … JOIN
ON CONFLICT (feed_id, uid) DO UPDATE SET
  id = excluded.id, read = excluded.read, trimmed_at = excluded.trimmed_at, last_seen_at = excluded.last_seen_at;

INSERT INTO trimmed_content (id, published_at, updated_at, sort_at, word_count, content_hash, text_hash,
                             url, title, author, image_url, origin_title, fulltext_mode,
                             content_html, content_text, enclosures_json, categories_json)
  SELECT i.id, i.published_at, i.updated_at, i.sort_at, i.word_count, i.content_hash, i.text_hash,
         i.url, i.title, i.author, i.image_url, i.origin_title, i.fulltext_mode,
         c.content_html, c.content_text, c.enclosures_json, c.categories_json
  FROM temp.trim_set t CROSS JOIN items i ON i.id = t.id CROSS JOIN item_content c ON c.item_id = i.id
  WHERE :restore_days > 0
ON CONFLICT (id) DO NOTHING;

UPDATE feeds SET trimmed_unread_count = trimmed_unread_count +
  (SELECT count(*) FROM temp.trim_set t CROSS JOIN items i ON i.id = t.id WHERE i.read = 0 AND i.id < :first_new_id),
  trimmed_unread_since = COALESCE(trimmed_unread_since, :now)
WHERE id = :feed
  AND EXISTS (SELECT 1 FROM temp.trim_set t CROSS JOIN items i ON i.id = t.id WHERE i.read = 0 AND i.id < :first_new_id);

DELETE FROM items WHERE id IN (SELECT id FROM temp.trim_set);
```

- **Bounded per transaction.** The `trim_set` INSERT ends with `ORDER BY sort_at ASC, id ASC LIMIT :batch` (2000): one transaction trims at most that many items, the oldest first, so a feed with a huge backlog under a newly lowered cap never pushes a write past the writer's 10 s deadline (about 0.6 ms per item on a fast desktop with a local SSD, mostly the FTS delete trigger and the WAL writes of the commit, so a little over 1 s a batch). Repeating it converges on exactly the unbounded result, since kept items never enter the set and the muted allowance recomputed from the smaller counts is unchanged. A fetch commit trims one batch; when that batch was full the scheduler queues a `trim_only` job for the feed 1 s later. A `trim_only` job loops in batches, each its own gated transaction, for at most 10 s (the first batch always runs), and when it stops with work left it is requeued the same way; one `fetch_log` row is written by the first batch and later ones add to it. Deleting or unsubscribing a feed likewise deletes its items and then its ledger rows in gated batches of the same size before the final transaction removes the feed row; an interrupted delete resumes on retry.
- Every statement that reads the trim set's items joins `temp.trim_set t CROSS JOIN items i`, so SQLite walks the trim set and looks each item up by id. With a plain JOIN the planner, which has no statistics for the temp table, may scan `items` instead and probe the trim set, so a batch would cost what the library holds rather than what it trims.
- The trim returns early, before any INSERT, when `trim_set` is empty.
- `changes()` from the DELETE goes to `fetch_log.trimmed_items`.
- The FTS rows go via `items_fts_bd`. `item_content` and `item_fulltext` go by cascade.
- `stats_events` is never touched.

Validated with 300 items in one feed, 5 of them starred, and N=50: 245 trimmed, 55 kept (50 unstarred plus 5 starred), 245 ledger rows and 245 stubs. FTS `integrity-check` is clean.

**Starred exemption.** It is enforced by `starred = 0` in the selection. Unstarring makes an item eligible at the next trim.

**Ranking.** `sort_at` DESC, then `id` DESC: the same order as the UI.

- A backfilled or republished old-dated post can be trimmed in the same transaction that inserted it. It goes to the ledger (and a stub) but is not counted in `trimmed_unread_count`.
- A bogus future date is clamped to crawl + 24 h in `sort_at` (the ranking key). `published_at` keeps the feed's value.

**Restore** (`store.restoreTrimmed(tx, ids, mode)`, where `mode` is `star` or `unread`; validated). It first selects the restorable ids: those trimmed within `restore_days` that also still have a stub. A stub the nightly purge has not yet removed but that is older than the window cannot be restored, so a restore never depends on when the purge last ran. The statements below then run over `:rids`, that selection:

```sql
SELECT t.id FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id
  WHERE t.id IN (SELECT value FROM json_each(:ids)) AND t.trimmed_at >= :now - :restore_days*86400;   -- :rids

INSERT INTO items (id, feed_id, uid, read, starred, read_at, starred_at, retain_until, fulltext_mode,
                   published_at, updated_at, sort_at, word_count, content_hash, text_hash,
                   url, title, author, image_url, origin_title)
  SELECT t.id, t.feed_id, t.uid,
         CASE WHEN :mode = 'unread' THEN 0 ELSE t.read END,
         CASE WHEN :mode = 'star' THEN 1 ELSE 0 END,
         CASE WHEN :mode = 'unread' THEN NULL WHEN t.read = 1 THEN :now END,
         CASE WHEN :mode = 'star' THEN :now END,
         CASE WHEN :mode = 'unread' THEN :now + 7*86400 END,
         c.fulltext_mode, c.published_at, c.updated_at, c.sort_at, c.word_count, c.content_hash, c.text_hash,
         c.url, c.title, c.author, c.image_url, c.origin_title
  FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id
  WHERE t.id IN (SELECT value FROM json_each(:rids))
ON CONFLICT DO NOTHING RETURNING id;                           -- the ids actually restored
INSERT INTO item_content (item_id, content_html, content_text, enclosures_json, categories_json)
  SELECT c.id, c.content_html, c.content_text, c.enclosures_json, c.categories_json FROM trimmed_content c
  WHERE c.id IN (SELECT value FROM json_each(:rids)) AND EXISTS (SELECT 1 FROM items i WHERE i.id = c.id)
ON CONFLICT DO NOTHING;                                        -- FTS row comes back via the trigger
DELETE FROM trimmed_items WHERE id IN (SELECT value FROM json_each(:rids)) AND id IN (SELECT id FROM items);
                                                               -- cascades the stub
```

- A restored item keeps its original id, uid and `sort_at`. It re-enters every list it qualifies for.
- A restored starred item is exempt from trimming as long as it stays starred. A mark-unread restore is held for 7 days.
- Restores publish `items.state` over SSE.
- Ledger ids trimmed more than `restore_days` ago (with or without a stub still on disk) cannot be restored: a star is ignored, and a mark-unread falls through to the plain ledger `read = 0` flip (§11).

**Ledger writes elsewhere.**

- **Ingest.** A uid found in `trimmed_items` for the feed is never re-inserted. Its `last_seen_at` is bumped.
- **`edit-tag`, and web `mark-read` with `read:true`.** `UPDATE trimmed_items SET read = 1 WHERE id IN (…) AND read = 0` (silent).
- **`edit-tag r=read`, and web mark-unread.** Restore with `mode='unread'` where a stub exists. Otherwise `UPDATE trimmed_items SET read = 0 …`.
- **`edit-tag a=starred`, and web star.** Restore with `mode='star'` where a stub exists. Otherwise ignored.
- **`mark-all-as-read`.** `UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts_us [AND feed_id = :f | AND feed_id IN (folder feeds)]`. It is **skipped for the starred and muted scopes and for any filtered or search scope** (web); the Reader API skips it only for starred. Starred items are never in the ledger.
- **Nightly auto-read** (`RunAutoRead`, §7.1d) marks matching ledger rows read, in gated batches, along with the items.
- **"Mark this fetch read"** (the guid-churn action) flips the unread ledger rows in that fetch's id range along with the items.
- **Unsubscribe.** FK cascade (the ledger and stubs of the deleted feed go with it; starred items were moved to the archive first, §6.9).
- **Nightly purge.** Stubs after `restore_days` (measured from `trimmed_at`). Ledger rows (and, by cascade, any stub) `max(180, restore_days + 7)` days after their uid was last seen in the feed document.

**Size.** About 60 bytes per ledger row. A stub is about the article size. At roughly 500 trims a day × ~6 KB × 90 days, stubs take about 270 MB, which is acceptable on Host-A. `restore_days` can be lowered.

**Reader API treatment of trimmed ids.**

| Endpoint | Behaviour |
|---|---|
| `stream/items/ids` (every stream) | Never lists ledger ids. An unread item that gets trimmed disappears from the unread list; NNW marks it read locally and Reeder drops it. Starred lists are unaffected |
| `stream/items/contents`, `stream/contents` | Ledger ids are silently omitted from `items[]` |
| `edit-tag` | Always `200 OK`. Read flags update the ledger. Star and unread **restore** within the stub window |
| `mark-all-as-read` | Updates in-scope ledger rows (not for starred). `OK` |
| `unread-count` | Never counts ledger rows |
| Health view | Shows `trimmed_unread_count` since `trimmed_unread_since`, with a reset button. This counter is immutable at trim time: mark-all never zeroes it, and first-fetch trims never inflate it |

**Since migration 0004** the stub and the restore also carry `categories_json` (both statements above list it; the restore SQL in the last block does too). `items.muted_by` is not carried: a restore always brings an item back with `muted_by = NULL`. Muted items are trimmed under their own allowance (the rules above), never ahead of real items beyond it.

---

## 6. Google Reader API mapping

### 6.1 Routing

- **Front handler** (`internal/greader.Front`), installed in `main` ahead of the root ServeMux:
  1. Take `r.URL.Path` and collapse runs of `/` into one.
  2. Strip any number of leading `/api/greader.php` prefixes; remember whether one was present.
  3. If the remainder is `""`, `/`, `/accounts/ClientLogin`, `/check/compatibility`, `/icon/…`, or starts with `/reader/api/0/`, **and** either a prefix was present or the remainder is one of the root forms (`/accounts/ClientLogin`, `/reader/api/0/…`), dispatch to the Reader handler with the cleaned path.
  4. Otherwise pass the request to the root ServeMux.

  The probes, `/icon/…` and ClientLogin are switched on the cleaned path first. Every other route is looked up in a route table (`map[string]route`) whose entries carry flags: `post` (writes: POST only, T checked), `raw` (the body is not a form: `subscription/import`), `repair` (the label-name repair, §6.2) and `prefix` (matches a name plus anything after it, as `stream/contents/…` does). `lookup` tries an exact entry, then the prefix entries. **No ServeMux ever sees a Reader path**, so ServeMux's automatic 301 for `//` paths, which URLSession would follow as a bodiless GET, cannot occur. Contract tests cover `/api/greader.php//reader/api/0/edit-tag`, a trailing-slash base, and a doubled prefix.
- **Mount scope.** `/api/greader.php…` is the Access-bypassed public path. The root forms (`/accounts/ClientLogin`, `/reader/api/0/…`) exist for Reeder's generic "Reader" type on the LAN. They sit behind Access on the tunnel and must never get their own bypass.
- **Probes.** `GET ""` or `/` under the prefix returns `200 text/plain "OK"`. `/check/compatibility` returns `PASS`.
- **Unknown paths.** An unknown `/reader/api/0/*` after successful auth returns `200 application/json []` with a WARN log. Any other path under the `/api/greader.php` prefix (one the Reader handler does not own) answers `404` with the plain `http.NotFound` body and never reaches the web mux, so it carries no Reader `Cache-Control`.
- **Wrong method.** A write route (`edit-tag`, `mark-all-as-read`, `subscription/quickadd`, `subscription/edit`, `subscription/import`, `rename-tag`, `disable-tag`) answers `405 text/plain` with `Allow: POST` to any other method, after authentication. `subscription/import` is `raw` (see §6.9), so it authenticates by header only.
- **Response headers.** Every response except `/icon/…` carries `Cache-Control: private, no-cache` (the icon endpoint sets its own, §6.9). JSON is `application/json; charset=utf-8`, arrays are `[]`, and **no value is ever JSON `null`** except `LSID` in JSON ClientLogin (decision 32). OK, token and ClientLogin bodies are `text/plain; charset=utf-8`. Go emits the `Date` header, which NNW uses for its next `ot`. No cookies are ever set.
- **Status policy.** Never 429, never a redirect, never 5xx for client data. A 500 only happens on a genuine database failure; NNW re-queues and retries. The only other non-2xx answers besides the 401s (§6.3) are `400` for more than 20000 `key=value` pairs in the body or the query, `413` for a body over its cap (§6.2), `404` for a non-Reader path under the prefix, `405` for the wrong method on a write route, `400 "Bad OPML"` for an unparseable `subscription/import` body, `404` from the icon endpoint, and `503` with `Retry-After: 15` while a search-index rebuild holds the writer (§2.1). A refused folder name (too long, control characters) answers `OK` with a WARN.
- **Logging.** The request log is at DEBUG level. It always records method, path, UA, client family, Content-Type, status, duration and the query and form **keys** (never query values, never the `Authorization` header). With `KIPPLE_LOG_GREADER_FORMS=1` it also records, per key, the value count and the first 3 values (each truncated to 200 bytes), with `Passwd`, `T`, `auth`, `SID`, `LSID` and `Authorization` replaced by `[redacted]`. `edit-tag` and `stream/items/contents` log a WARN with UA and Content-Type when they parsed zero ids from a non-empty body.

### 6.2 Form parsing (`greader/form.go`)

Reader routes never call `r.ParseForm`.

1. Parse `Content-Type` with `mime.ParseMediaType`, ignoring parameters (`; charset=utf-8`) and treating an unparseable header as absent.
   - `multipart/form-data` (Readrops ClientLogin) goes through `r.MultipartReader`.
   - `subscription/import` reads the raw body as OPML.
   - For every other POST, whatever the media type (including absent, `text/plain`, `application/octet-stream`), the body is parsed as urlencoded. The cap is 4 MiB (`maxBody`) when the `Authorization` header already authenticated the request. ClientLogin and any POST that has no valid header (so must authenticate with T in the body) are capped at 64 KiB (`maxLoginBody`). `subscription/import` reads its raw OPML body with the same 4 MiB cap. A body over its cap answers `413` and is never parsed truncated. More than 20000 pairs (`maxPairs`) in the body or the query answers `400`. A 1000-id NNW edit-tag is about 60 KB.
2. Split on `&` only. Split each pair on the **first** `=`: NNW leaves `=`, `;`, `,` and `$` unencoded in values. A pair with no `=` becomes `key → ""`.
3. `url.QueryUnescape` both sides, so `+` becomes a space. On an unescape error, keep the raw text.
4. Keep repeated keys in order in a `map[string][]string`, **and** keep the raw body string and each pair's raw text.
5. Parse the query string the same way.
6. `p.Get(k)` checks the body first, then the query. `p.All(k)` returns body values followed by query values. `p.RawBody()` returns the undecoded body.

**Label lookup** (`subscription/edit a=`, `rename-tag s=` and `dest=`, `disable-tag s=`, label streams; `subscription/edit r=` is only checked for being a label, never looked up, §6.9). Take the text after `user/<x>/label/`. Match it against each folder's full path (`folder_paths.path`, the names from the top joined with `/`, so `Tech/Apple`) `COLLATE NOCASE`, trying in order:

1. the decoded value;
2. the raw value (no unescaping);
3. the raw value through `url.PathUnescape` (which keeps `+`).

**Unencoded folder names (the repair, `splitPairsLimit(s, repair)` in `form.go`).** NNW encodes `&` and `+` in `a=`/`r=`/`t=` (subscription/edit, subscribe) and in rename-tag names, so those arrive whole and are parsed by the exact phase 1 parser (`repair` false). Only `disable-tag` sends the folder id raw (`T=<token>&s=user/-/label/News & Politics+`), so the `&` splits the name. The repair therefore runs **only on the POST body of `disable-tag`** (route flag `repair`; it never creates a folder, it only looks one up and deletes it). Every other endpoint, and every query string, is parsed exactly as in phase 1, including `subscription/edit` and `rename-tag` with a stray `&` after a label (no garbage folder is created). After a body pair whose key is `s`, `a`, `r` or `dest` and whose value starts `user/<x>/label/` (the prefix alone; the name may still be empty or blank, as in `&Co`), the following `&`-parts are glued back onto the value, in their original positions (empty parts included), for as long as each is a *name tail*: a part with **no `=`** (the `T` of `AT&T`, the empty parts of `R&` and `A&&B`), or a part whose key is **not a parameter key**. A key containing `+` is never a parameter key. A parameter key is an identifier (`^[A-Za-z_][A-Za-z0-9_]*$`), a key whose percent-decoded form is an identifier (`%54` is `T`, `%61` is `a`), or a vendor key with a dash or dot and no space (`x-client`, `client.id`). So ` Politics+` and `a b=c` are tails, while `T=`, `i=`, `x-client=` are never swallowed. The run is joined once and decoded once with a lenient unescape (every valid `%XX` decoded, an invalid escape left literal, `+` left literal; a client that leaves `&` unencoded is not form-encoding), so `My%20News&100%` becomes `My News&100%`. One exception: when the label value as sent contains a valid `%XX` escape (its client form-encoded it), an empty part is a stray `&` and ends the run, so `s=user%2F-%2Flabel%2FMy+Folder&&T=tok` keeps `My Folder` and `T` survives; raw names (`R&`, `A&&B`) still glue their empty parts. Values that were not merged keep the ordinary `QueryUnescape`. Cost is linear, and one name absorbs at most 64 parts. **disable-tag lookup** tries the merged name decoded (lenient unescape), then the raw merged name, then the raw name through `url.PathUnescape`, and, only when the raw name ends in stray `&` characters, the trimmed name (decoded, raw and path-unescaped); the first existing folder wins, so `AT&T` beats `AT` when both exist, and a missing `AT&T` never deletes `AT`. Limits: a name containing `&<parameter key>=` (`R&D=5`) is not recoverable in disable-tag. A differential test against a vendored copy of the phase 1 parser (`refparser_test.go`) proves every other endpoint, and `subscription/edit` and `rename-tag` with `&`-tails, parse identically to phase 1; a fuzz target guards non-label input.

### 6.3 Auth and tokens

- **ClientLogin (POST, or GET for FeedMe).**
  - `Email` and `Passwd` come from the body or the query. `Email` must match `account.username` case-insensitively. A NULL `api_password_hash` means the API is disabled and every attempt fails. An empty `Passwd` fails at once without hashing.
  - **Verification** (`internal/auth.Verifier`, shared with the web login):
    1. If `hmac.Equal(HMAC(secret, "login|"+kind+"|"+hash+"|"+Passwd), memo[kind])` (`kind` is `api` or `web`, `hash` the stored PHC string), succeed without hashing.
    2. Otherwise acquire the global semaphore of 1, waiting at most 5 s (else 401). Run argon2id (m=19 MiB, t=2, p=1; about 50 ms and 19 MiB on Host-A), then release. On success, set the memo for that kind.
    3. The stored hash is part of the MAC, so a changed password never matches an old memo and nothing is cleared. A changed account secret (`SetSecret`) drops every memo.
    4. A **busy** verifier (the 5 s wait expired, or the request context was cancelled) answers 401 but records no failure and adds no delay: it says nothing about the password.
  - **Per-client attempt budget** (`auth.FailureTracker`). The client IP is decided once, by `auth.ClientIP`: `RemoteAddr` unless it is in `KIPPLE_TRUSTED_PROXY_IPS` (addresses or CIDR ranges), when it is the rightmost `X-Forwarded-For` hop not itself listed, else `CF-Connecting-IP`; the budget (the Reader API's own tracker; the web login builds its own) keys an IPv6 client by its /64, so rotating addresses inside one subscriber prefix buys nothing.
    - A password that matches the success memo succeeds without hashing and is not paced, so a signed-in device keeps working while its address is over budget. It leaves the failure count alone.
    - Otherwise the attempt is **admitted before hashing**. Only **failures** (a wrong password or email) are counted; the count fades only after an hour with no failure, and a verified success clears the client.
      - At most one attempt per client hashes at a time. A second concurrent attempt (a client retrying a slow login) **waits** for the first instead of failing.
      - The first 5 failures are free. After that an attempt starts only after a delay since the client's previous attempt: 2 s, doubling with each further failure to a 60 s cap. It **waits** for that (up to the bound below) instead of being refused.
      - Waiting is bounded: at most 4 attempts per client wait, each for at most 10 s or until the request is cancelled. An attempt that cannot start is the ordinary failure 401 below, without hashing, and counts nothing.
      - Every admitted attempt verifies the password, even when the email is wrong. A busy verifier counts nothing.
    - So an attempt that gets its turn is always verified, and a delay never refuses it unchecked except that a wait longer than 10 s (a client already past 7 failures) gives up and answers as below, so a guesser at one address is held to about one guess per minute. The hashing cost stays bounded: one client never runs two hashes at once, holds at most 5 requests open, and all clients share the global semaphore.
    - Never 429.
  - A chosen Reader API password (`POST /api/account/api-password` with `new`, or `KIPPLE_API_PASSWORD`) must be 16 to 256 characters; the UI and `kipple api-password` generate 24.
  - **Failure:** `401 text/plain "Error=BadAuthentication\n"` with both `Google-Bad-Token: true` and `X-Reader-Google-Bad-Token: true`.
  - **Success:** `text/plain "SID=<tok>\nLSID=null\nAuth=<tok>\n"`. With `output=json`: `{"SID":…,"LSID":null,"Auth":…}`.
  - `tok = username + "/" + hex(HMAC-SHA256(secret, "greader-token-v1|" + api_password_hash))`. It is 64 hex after the slash and never contains `=`. It is part of an account snapshot cached in an `atomic.Pointer` with a 5 s TTL (`acctTTL`). `InvalidateAccount` (wired to `OnAPIPasswordChange` in `main`) drops the snapshot at once when the API password changes in the UI, and a change made through the CLI takes effect within 5 s. Either way every client is revoked.
- **Every other route.**
  - `Authorization: GoogleLogin auth=<tok>`: split with `strings.Fields` into exactly 2 fields; the second must start with `auth=`; compare in constant time (`subtle.ConstantTimeCompare`).
  - On POST (not `subscription/import`), a `T=<tok>` in the body or query is an equally valid credential. A GET authenticates only by header.
  - Cookies (`kipple_session`, `kipple_device`) are never credentials on the Reader API.
  - With no API password set (the API is disabled), every non-probe route answers 401.
  - **Failure:** `401 text/plain "Unauthorized!"` with **both** `X-Reader-Google-Bad-Token: true` and `Google-Bad-Token: true`.
- **`GET /reader/api/0/token`** returns `text/plain tok + "\n"`.
- **The T check on write endpoints:**
  - If the Authorization header authenticated the request, T must be `tok`, `""` or `x`. Any other T returns 401.
  - If there is no header, T must equal `tok`, else 401. Never 400.
- **Client family.** The `User-Agent` prefix classifies the client: `Reeder` → reeder, `NetNewsWire` → netnewswire, `Unread` → unread, anything else → api. The family is kept on the per-request `call` and passed to the stats recorder and as the SSE `source`; it also goes into greader's mutex-guarded last-seen map, updated at most once a minute per family.

### 6.4 Stream grammar (`greader/stream.go`)

| Stream id | Predicate |
|---|---|
| `user/<x>/state/com.google/reading-list` | none |
| `…/starred` | `starred = 1` |
| `…/read` | `read = 1` |
| `…/unread` | `read = 0` |
| `…/kept-unread` | `read = 0` |
| `…/broadcast`, `…/like` | always empty |
| `user/<x>/label/<name>` | `feed_id IN (SELECT id FROM feeds WHERE folder_id = <label lookup, §6.2> AND disabled_reason IS NOT 'archive')` (the archive feed is in no folder a reader sees). Only the folder's own feeds, never its subfolders' (see "Folders and the Reader API" in §6.9); the web folder list, search and mark-all-read scope a folder with its subfolders |
| `feed/<digits>` | `feed_id = :f` |
| `feed/<url>` | `feed_id = FindFeedByURL(:u)` |
| `''` | reading-list |
| anything else | empty result, 200, WARN |

`<x>` is `-` or anything else. Kipple always emits the `user/-/` forms and `feed/<id>`.

`it` ANDs `read=1`, `read=0` or `starred=1`. `xt` (repeatable) maps `read` → `read = 0`, `unread` → `read = 1`, `starred` → `starred = 0`. Contradictions simply produce an empty page.

### 6.5 stream/items/ids (reader pool)

Parameters:

- `n`: default 20, capped at 100000.
- `r`: `o` means ascending; anything else descending.
- `c`, `ot`, `nt` as in §3. **An absent `c` binds to MaxInt64 when descending and to 0 when ascending.**

Without `ot`:

```sql
SELECT id FROM items WHERE <scope> AND <it/xt> [AND id < (:nt+1)*1000000] AND id < :c
ORDER BY id DESC LIMIT :n + 1;
```

With `ot`, the query has two **disjoint** ordered legs, merged. Leg 2 only returns ids below the leg-1 range, so the combined order stays exactly `id DESC`, and continuation stays valid across both legs:

```sql
SELECT id FROM (
  SELECT id FROM (SELECT id FROM items
                  WHERE id >= :ot_us AND id < :c AND <preds>
                  ORDER BY id DESC LIMIT :n + 1)
  UNION ALL
  SELECT id FROM (SELECT id FROM items INDEXED BY idx_items_changed
                  WHERE content_changed_at >= :ot - 120 AND id < min(:ot_us, :c) AND <preds>
                  ORDER BY id DESC LIMIT :n + 1)
) ORDER BY id DESC LIMIT :n + 1;       -- :ot_us = (:ot - 120) * 1000000; :c never NULL
```

With `r=o`, both legs and the final order are ascending, leg 2 comes first, `c` becomes `id > :c`, and leg 2's bound becomes `id < :ot_us AND id > :c`.

With `greader.ot_includes_user_changes` on (decision 6), leg 2 gains a second ordered branch with the same bound and predicates, `SELECT id FROM items INDEXED BY idx_items_state_changed WHERE state_changed_at >= :ot - 120 AND ...`, and the compound becomes `UNION` (distinct), since one older item can match both branches. Leg 1 and leg 2 stay disjoint by id, so the merge and the continuation work exactly as above. With the setting off the query is the one shown, unchanged.

Validated (revision 2): with no `c` (bound to MaxInt64), leg 2 returns the content-changed older ids on the first page. The plans are a PK range for leg 1 and `COVERING INDEX idx_items_changed` for leg 2, unchanged after `ANALYZE`.

Revision 1 measured this form at 150,000 rows. It returns results identical to the naive `(id >= ? OR content_changed_at >= ?)` query:

| `ot` window | Two-leg form | Naive OR form |
|---|---|---|
| 60 s | 0.24 ms | 39.7 ms (full `SCAN items`) |
| 1 h | 2 ms | — |
| 30 d | 6.4 ms | — |

Response:

```json
{"itemRefs":[{"id":"1758700000123456"},…],"continuation":"1758699000000001"}
```

`continuation` is present only when `n+1` rows came back; the page is then exactly `n` rows, and `continuation` is the id of the n-th row. `itemRefs` carries no `timestampUsec` or `directStreamIds`.

**Full-text hold.** A new item of a full-text feed is *held back* from every Reader API listing until its extraction has finished or a hold window has passed, whichever comes first, so a client that syncs right after a fetch gets the extracted text and not the feed stub (open question 55, decided). An item is held when all of these are true at request time: its effective mode (§7.5, which includes `fetch.fulltext_all`) is 1; it has no `item_fulltext` row (a stored result **or** a stored error releases it); it is **pending in the ingest pool** (see below); and `id > :hold_cut`, where `:hold_cut = (now − window)·1e6`. The id is the crawl time in microseconds, so the window is measured from insert time. The window is `greader.Options.FulltextHold`: 0 means 30 s, negative disables it, and it is capped at 60 s. It is an Options field, not a user setting. Non-full-text feeds and items, and items older than the window, are never held. **Pending set.** The fetch commit records each item picked for the pool in `store.DB` (`MarkFulltextPending`, inside the transaction that inserts it, undone if the queue then refuses it) and forgets it when its job ends, after any result or error row is written (`ClearFulltextPending`). `HeldSQL` holds only items in that set (bound as `:pending`, a JSON array, next to `:hold_cut`; `MarkScope.HoldPending` carries it for mark-all-as-read). So an item the pool never accepted (beyond the per-fetch cap, queue full, shutdown, or full-text turned on for the feed after its fetch was picked; muted items are never queued, so never held) is served at once with the feed's own content instead of waiting out the window for text that is not coming. The set is process memory: after a restart nothing is pending, which matches the queue being gone. The window still ends the hold for an item that waits in a long queue (a big refresh with the switch on), which then shows the feed content to Reader clients; the web UI swaps in the text when it arrives.

- **SQL, not Go.** The predicate (`store.HeldSQL`) is ANDed into the stream filter, so `LIMIT`, continuation and both `ot` legs see only visible rows and a page is never short.
- **Everything the Reader API reads.** It applies to `stream/items/ids`, `stream/contents`, `stream/items/contents` (a held id is absent even if a client asks for it), `unread-count` and the default `mark-all-as-read`. The web UI (`/api/items`, bootstrap counts, SSE `counts`) is **not** held; the UI already swaps in text through `fulltext.ready`.
- **Counts.** `unread-count` leaves held items out (total, per folder, per feed and `newestItemTimestampUsec`), so a client whose count is the length of its unread list agrees with the listing. The UI count includes them, so for at most the window the UI and a Reader client can differ by the held items.
- **mark-all-as-read** skips held items, with or without `ts`: a client cannot have seen them, and marking them read would silently swallow them.
- **Monotonic ids and `ot`.** Ids are unchanged; only visibility moves, and it moves from hidden to visible (the one exception is turning full-text on for a feed or item younger than the window, which can hide an already listed item; that is ignored). A held item H has crawl time t0. A client that synced while H was held holds an `ot` (its sync time, or the newest crawl time it saw) of at most t0 + window. Leg 1 of its next sync lists ids at or after `ot − 120 s`, and 120 s exceeds the window (capped at 60 s, which leaves 60 s for client clock skew), so H is returned then. That is why the cap exists: a longer window could let `ot` pass H by more than the slack and lose it. Clients that list without `ot` (Reeder's unread and starred lists, NNW) get H from the full list. Paging: if H is released between two pages of one continuation pass and its id is above `c`, that pass misses it, exactly as it would miss any item committed mid-pass, and the next sync gets it.
- **NNW.** A held id was never listed, so nothing is removed on the client; a missing unread id is only marked read there when it had been listed before.

### 6.6 stream/items/contents (POST; GET tolerated) and stream/contents

- **Id cap.** `i=` values beyond the first 1000 are ignored with a WARN. NNW sends 150 and Reeder 100.
- **Streaming.** The handler writes the envelope prefix, then scans rows one at a time and encodes each item straight into a 32 KB `bufio.Writer`, then writes `]}`. The full `[]item` is never built. The read transaction is short, because the rows are local and the writer only blocks on a full socket buffer; a stalled client is cut off by the server `WriteTimeout` of 60 s.

```sql
SELECT i.id, i.feed_id, i.url, i.title, i.author, c.content_html, i.published_at, i.updated_at,
       i.read, i.starred, c.enclosures_json, i.origin_title,
       COALESCE(f.custom_title, f.title) AS feed_title, f.site_url, fo.name AS folder_name,
       <effective full-text mode>, ft.content_html AS fulltext_html
FROM items i JOIN item_content c ON c.item_id = i.id
JOIN feeds f ON f.id = i.feed_id JOIN folders fo ON fo.id = f.folder_id
LEFT JOIN item_fulltext ft ON ft.item_id = i.id
WHERE i.id IN (SELECT value FROM json_each(:ids_json)) [AND NOT <HeldSQL(i.)>]
ORDER BY i.id DESC;                                   -- ASC when r=o
```

`<effective full-text mode>` is `store.FulltextModeSQL("i.fulltext_mode", "f.fulltext", fetch.fulltext_all)`: `COALESCE(i.fulltext_mode, 1)` when the `fetch.fulltext_all` switch is on, else `COALESCE(i.fulltext_mode, f.fulltext, 0)`. The switch is read once per request, so the hold and the content mode agree. The `AND NOT <HeldSQL>` predicate is appended only when the hold is active (§6.5).

**Envelope:**

```json
{"id":"user/-/state/com.google/reading-list","updated":<now>,"items":[…]}
```

There is never a `continuation` here. With zero matches it is still 200 with `items: []`.

**Item:**

```json
{"id":"tag:google.com,2005:reader/item/00063f8740c61a40",
 "crawlTimeMsec":"1758700000123","timestampUsec":"1758700000123456",
 "published":1758690000,"updated":1758690000,
 "title":"…","author":"…",
 "canonical":[{"href":"<url or ''>"}],"alternate":[{"href":"<url or ''>","type":"text/html"}],
 "summary":{"direction":"ltr","content":"<html>"},
 "categories":["user/-/state/com.google/reading-list","user/-/label/<folder>" (not for an archived item),
               "user/-/state/com.google/read"?,"user/-/state/com.google/starred"?],
 "origin":{"streamId":"feed/<feed_id>","title":"<COALESCE(origin_title, feed title)>","htmlUrl":"<site_url or ''>"},
 "enclosure":[{"href":u,"url":u,"type":t,"length":n}]}
```

Rules for the item fields:

- **Content.** `content` is `fulltext_html` when the effective full-text mode (§7.5) is 1 and an extraction exists; otherwise `content_html`. Both carry **absolute original** URLs (decision 31). The body is cut UTF-8-safely at 500,000 bytes.
- **Strings, never null.** `author`, `title`, `alternate[].href`, `canonical[].href` and `origin.htmlUrl` are always strings, possibly `""`. `enclosure` is omitted when there are none.
- **Titles are plain JSON strings.** There is no fullwidth `＆＜＞` escaping.
- **Required fields.** `summary`, `categories` and `origin` are always present. `origin.streamId` is the item's feed id: the subscription id, or for an archived item the archive feed's id, which is not in `subscription/list` (§6.9, decision 24). An archived item carries no `user/-/label/` category.
- **Ids.** Trimmed and unknown ids are absent, and so are ids held back by the full-text hold (§6.5).

**stream/contents.** `GET /reader/api/0/stream/contents[/<stream>]` (or `?s=`) runs the §6.5 filter with `n` capped at 1000, then streams contents the same way. The envelope `id` is the requested stream, with a continuation as in §6.5.

### 6.7 edit-tag (POST)

`ids = p.All("i")` parsed per §3, and only the first 10000 values (`maxEditIDs`) are processed: the rest are ignored and the reply is still `200`. `a = p.All("a")`; `r = p.All("r")`. When no id parsed, or no `a=`/`r=` value is a recognized read/starred op, it answers `OK` without opening a transaction. Otherwise it runs as one `WithWrite` transaction, the `a=` values first, then the `r=` values:

| Tag | SQL (then ledger handling, §5) |
|---|---|
| `a=read`, `r=kept-unread` | `UPDATE items SET read=1, read_at=:now, state_changed_at=:now WHERE id IN (SELECT value FROM json_each(:ids)) AND read=0 RETURNING id, feed_id`; ledger `read=1` for the rest |
| `r=read`, `a=kept-unread` | `UPDATE items SET read=0, read_at=NULL, muted_by=NULL, muted_was_read=NULL, state_changed_at=:now WHERE id IN (…) AND (read=1 OR muted_by IS NOT NULL) RETURNING id, feed_id`; ids not in `items` → restore `mode='unread'` or ledger `read=0` |
| `a=starred` | `UPDATE items SET starred=1, starred_at=:now, state_changed_at=:now, muted_by=NULL, muted_was_read=NULL WHERE id IN (…) AND (starred=0 OR muted_by IS NOT NULL) RETURNING id, feed_id`; ids not in `items` → restore `mode='star'` (the item stays read) |
| `r=starred` | `UPDATE items SET starred=0, starred_at=NULL, state_changed_at=:now WHERE id IN (…) AND starred=1 RETURNING id, feed_id` |
| labels, `broadcast`, `like`, `tracking-*`, unknown | ignored |

- The Reader API has no concept of filters: a muted item is an ordinary read item. `r=read`/`a=kept-unread` and `a=starred` are the un-mute (star beats mute), which is why their guards also match `muted_by IS NOT NULL`.
- The `WHERE` guards make replays and NNW's four-pass retries no-ops (so a replay does not move `state_changed_at` either).
- `state_changed_at` (migration 0007) is what `greader.ot_includes_user_changes` reads; a restore from the ledger sets it too.
- `RETURNING` rows are fully scanned and closed inside the store helper.
- The changed ids feed the SSE `items.state` event after commit, with `source` = the client family and `restored` listing the ids brought back from the ledger. When one op changes more than `events.MaxStateIDs` ids, it publishes `resync` instead of listing them.
- Star and unstar changes record one stats row per changed id (restores included) through `Recorder.RecordStars(tx, …)` in the same transaction. Read changes record nothing: single-read inference (§8) is not implemented.
- The response is **always** `200 text/plain "OK"`: for zero ids, unknown ids, ledger ids, and an empty `i`.

### 6.8 mark-all-as-read (POST)

Scope from `s`:

| `s` | Scope |
|---|---|
| reading-list | none |
| `feed/N` | `feed_id = N` |
| `feed/<url>` | `FindFeedByURL` |
| label | `feed_id IN (folder feeds)` |
| starred | `starred = 1` (**no ledger statement**) |
| read, `kept-unread`, unread, unknown | no-op, `OK` |
| empty `s` | reading-list |

`ts` is normalized per §3. When it is absent, 0, non-digit or overflowing, the cutoff is the committed maximum id over `items` and `trimmed_items` (`max(max(items.id), max(trimmed_items.id))`) from a reader snapshot. `it` and `xt` are ignored. When any item row changed, it publishes an SSE `resync`.

```sql
UPDATE items SET read = 1, read_at = :now, state_changed_at = :now WHERE read = 0 AND id <= :ts_us [AND scope];
-- plans: idx_items_unread (id<?) or idx_items_unread_feed (feed_id=? AND id<?)
UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts_us [AND feed scope];   -- not for starred
```

On the narrow table each changed row rewrites one small cell. This never produces stats. The response is `OK`.

### 6.9 Subscriptions, folders, misc

**`GET subscription/list`.** `output` is ignored; JSON is always returned.

```sql
SELECT f.id, COALESCE(f.custom_title, f.title), f.url, f.site_url, fp.path, fi.hash
FROM feeds f JOIN folder_paths fp ON fp.id = f.folder_id
LEFT JOIN feed_icons fi ON fi.feed_id = f.id
WHERE f.disabled_reason IS NOT 'archive'          -- listedFeedSQL
  AND f.url NOT LIKE 'kipple:deleting:%'
ORDER BY fp.sort_key, f.position, COALESCE(f.custom_title, f.title);
```

It returns `{"subscriptions":[{"id":"feed/12","title":…,"categories":[{"id":"user/-/label/Comics","label":"Comics"}],"url":…,"htmlUrl":"<site_url or ''>","iconUrl":"<icon url or ''>"}]}`.

- There is always exactly one category per feed: its own folder's full path.
- **Folders and the Reader API.** Folders nest (migration 0012, at most 8 levels), but Reader labels are flat and clients (NetNewsWire, Reeder) build one folder level from them. A nested folder is therefore a flat label named by its full path (`user/-/label/Tech/Apple`), which NetNewsWire and Reeder show as one folder named `Tech/Apple`. A label's stream, mark-all-as-read and `unread-count` row cover that folder's own feeds only: if they rolled up the subfolders, the counts would disagree with the folder each client builds from `subscription/list`. `tag/list` leaves out pure containers (a folder with subfolders and no listed feed of its own), which a client would show as an empty folder; an empty folder without subfolders stays listed. So a parent becomes a visible empty folder again when its last subfolder goes: a `disable-tag` of its only child, or a `rename-tag` that moves the child to the top level, leaves a parent with no feeds of its own listed in `tag/list` (NetNewsWire shows it as an empty folder, which the client can delete). Full paths are unique ignoring ASCII case, so a label always names one folder: a top-level folder literally named `AC/DC` and a folder `DC` inside `AC` cannot both exist. A library with no nesting gets exactly the bytes it got before folders could nest. The tree itself is a web app feature.
- Disabled and gone feeds are listed; a feed being deleted (`kipple:deleting:` URL, see `ac=unsubscribe`) is not, and neither is the archive feed, ever (decision 24).
- `iconUrl` is **always present**. It is `<KIPPLE_PUBLIC_URL>/api/greader.php/icon/<id>-<hash>` (a trailing `/` on the public URL is trimmed) only when the `greader.icon_urls` setting is on (the default), `KIPPLE_PUBLIC_URL` is set and the feed has an icon (the favicon finder fills `feed_icons`, §4.11), otherwise `""`.
- **ETag:** `"` + the first 16 hex of `sha256(body)` + `"`. An `If-None-Match` match (after stripping `W/`) returns `304` with an empty body.

**`GET tag/list`.** Returns `{"tags":[{"id":"user/-/state/com.google/starred"},{"id":"user/-/state/com.google/reading-list"},{"id":"user/-/label/<name>","type":"folder"},…]}` from every folder's full path in display order (`folder_paths.sort_key`: the tree in pre-order, siblings by position, then name), leaving out pure containers (§6.9, "Folders and the Reader API"), with the same body-hash ETag. `types=1` is ignored.

**`POST subscription/quickadd`.** T is optional when the header is valid.

1. The `quickadd=` value has a leading `feed/` stripped.
2. **Idempotent.** If `FindFeedByURL(url)` matches (http/https and pre-migration URLs included), reply `200 {"numResults":1,"query":url,"streamId":"feed/<id>","streamName":<display title>}` with the **existing** feed. NNW's follow-up re-list then finds it.
3. Otherwise (decision 33): validate the URL (`ValidateFeedURL(raw, false)`: absolute http/https, a host, not a literal blocked IP; the Reader API always refuses one, and per-feed `allow_private_net` is set only from the web UI), then insert the feed. The fields are `folder_id=1`, `title` = host, `url`, `url_key`, `host`, and `next_fetch_at = now`. Send a non-blocking `wake`.
4. Reply `{"numResults":1,"query":url,"streamId":"feed/<id>","streamName":title}`. The subscription/list ETag changes automatically.
5. `greader.subscribe_fetch_now` (default off): when on, a quickadd or subscribe of a new feed waits up to 8 s for its first fetch; otherwise every Reader subscribe path behaves as the default above, with no outbound HTTP.

On an invalid URL the reply is `200 {"numResults":0,"query":url,"error":"<msg>"}`, where the message is `not an absolute http(s) URL`, `address not allowed`, or `the URL contains a user name or password; use the feed's HTTP authentication instead`.

**`POST subscription/edit`.**

- `ac=subscribe`: `s=feed/<url>` is repeatable; an `s` that is not `feed/<url>` or whose URL is invalid is skipped (the reply is still `OK`). `t` values are index-aligned with `s`. `a=user/-/label/<name>` puts the feed in that folder, creating the folder if missing (the label is a full path, resolved as for `ac=edit a=` below). A label the folder rules refuse never costs the subscription: it is logged (WARN), a new feed goes to folder 1 and an existing one stays where it is, and the remaining `s` values are still subscribed. Each `s` goes through the idempotent quickadd logic; an existing feed is moved or renamed only if `a`/`t` were given.
- `ac=unsubscribe` (**archive, decision 24**), in one transaction:
  1. The archive feed, if named (by its `feed/<id>`: it is never listed), is processed **last**, after the other `s` feeds have moved their starred items into it. It is deleted only when it holds no starred items; otherwise it is kept, an INFO line is logged, and the reply is still `OK` (the Reader API has no `delete_starred`; the web UI answers `409 archive_has_starred`, §7.1).
  2. Any other feed with starred items:
     - create the archive feed if missing: `url='kipple:archive'`, `url_key='kipple:archive'`, `host='kipple.invalid'`, title "Unsubscribed (starred)", `enabled=0`, `disabled_reason='archive'`, `retention=0`, folder 1;
     - `UPDATE items SET feed_id = :archive, uid = 'a' || :f || ':' || uid, origin_title = COALESCE(origin_title, :feed_title) WHERE feed_id = :f AND starred = 1`.
  3. `DELETE FROM feeds WHERE id = :f`. The cascade covers the remaining items (and FTS via trigger), ledger, stubs, log, icons and full text. Stats survive.

  Steps 2 and 3 run after two others, so a large feed never needs one long transaction and an interruption never leaves a subscribed feed with its history gone (the same holds for the web `DELETE /api/feeds/{id}`): first one short transaction **marks** every listed feed (except the archive feed): `enabled=0`, `disabled_reason='user'`, `url`/`url_key` = `kipple:deleting:<id>`, `url_original(_key)`, the validators and the redirect bookkeeping cleared. A marked feed is left out of the web feed list, Reader `subscription/list` and `unread-count` while its items are purged. The scheduler only picks enabled feeds and a fetch already in flight finds its URL changed and drops its commit as stale, so nothing is fetched or stored for the feed again; its `feed/<id>` stays the same and its old URL is free at once. Then each feed's unstarred items and ledger rows are deleted in bounded batches behind the commit gate. The request runs detached from the client's cancellation and bounded at 5 min; cut off anywhere (client timeout, writer deadline, restart) the feed stays marked and unfetched, a PATCH of it answers 404, and unsubscribing or deleting it again (or `store.ResumeFeedDeletes`, which archives starred items) finishes the job.
- `ac=edit`:
  - a non-empty `t` → `custom_title`: with one `s`, `t[0]` renames it; with several `s` and exactly as many `t`, `t[i]` applies to feed `i`; any other count renames none of them (a WARN is logged), and `a=`/`r=` still move them;
  - an `a=` label → move to that folder, created if missing: the label is a full path, matched as a whole first; otherwise the longest prefix (cut at a `/`) that is an existing folder's path becomes the parent and the remaining segments are found or created below it (`Tech/Apple` with no Tech creates both; a literal top-level `A/B` takes `A/B/C` as its child). Only the outer ends of the whole label are trimmed; each level keeps its name as written (`Tech / Mac` is a top-level `Tech ` holding ` Mac`, so the label a client sent is the one it gets back). A path the folder rules refuse (an empty level from a leading, trailing or doubled `/`, a level too long or with control characters, more than 8 levels, below the default folder) is logged and answered `OK` with nothing changed;
  - `r=` without `a=` → move to folder 1 (`r=` is only checked for being a `user/<x>/label/<name>` value, never looked up: any label moves the feed).
- An unknown `ac`, and unknown feeds, still get `OK`.

**`POST rename-tag`** (`s=user/-/label/<old>`, `dest=user/-/label/<new>`): the label lookup (§6.2) finds the old folder. `dest` is a full path: its last segment becomes the folder's name and the rest names its parent, resolved as for `ac=edit a=` (so a rename may also move the folder to another level, and its subfolders follow; renaming `Tech/Apple` to `Apple Stuff` puts it at the top level). A change the folder rules refuse (a cycle, more than 8 levels, a path another folder has, moving the default folder) is logged and answered `OK` with nothing changed. If the new path already exists, the folders are merged, unless the old folder has subfolders (merging two trees is refused the same way): the feeds move to the existing folder, the old folder's filters become one feed filter per feed that was in it, the archive feed aside (the rule itself, keeping its id and match count, for the first feed and a copy for each other; so they keep matching exactly those feeds, not the target folder's own; a merge whose copies would not all run as the rule runs now, past the 200-filter limit or another set-wide filter limit, is refused and answered `OK` with nothing changed), and the old folder is deleted (the default folder is never deleted: its feeds move, its folder filters stay, and the moved feeds get feed-filter copies). The default folder may be renamed. Response `OK`.

**`POST disable-tag`** (`s` repeatable): the label lookup with the merged-name candidates (§6.2), then the folder is deleted with all its subfolders, every feed in them moving to folder 1 and their folder filters going with them: the web app's folder delete (`DeleteFolder`). The default folder is never deleted. Response `OK`.

**`POST subscription/import`**:

1. Requires the `Authorization` header (the route is `raw`: T is not read, the body is not a form, so a T-only POST is 401). Parse the raw OPML body (4 MiB cap). An unparseable body answers `400 text/plain "Bad OPML"` and logs a WARN.
2. Create the file's folder tree (§7.6). A folder that cannot be created (too deep, a refused name) is logged as a WARN; its feeds go into the nearest folder above it.
3. Match each outline through `FindFeedByURL`. Matches keep their state and folder (`move_existing` is a web and CLI option only). New feeds get their OPML title in `custom_title` and their OPML position, with `next_fetch_at = now`.
4. Send `wake` when any feed was added (no run, no synchronous HTTP; decision 33).
5. Reply `200 OK` (NNW requires exactly 200).

**`GET subscription/export`** returns the OPML attachment from the same exporter as the UI (§7.6).

**`GET user-info`** returns `{"userId":"1","userName":u,"userProfileId":"1","userEmail":u}`. Reeder calls it right after ClientLogin.

**`GET unread-count`** runs `SELECT feed_id, count(*), max(id) FROM items WHERE read = 0 GROUP BY feed_id` (covering on `idx_items_unread_feed`) and rolls the results up in Go. Held items (§6.5) are left out (`AND NOT <HeldSQL>`), as are feeds being deleted (`kipple:deleting:` URL); the archive feed's unread items count toward the reading-list total (they are in that stream) but get no folder or feed entry, and the rows are joined to their folder and ordered by folder position and name, feed position, then feed id. The response is `{"max":<total>,"unreadcounts":[…]}`, where `unreadcounts` holds three kinds of entry in this order: one `user/-/state/com.google/reading-list` entry (the total and the newest id), then one `user/-/label/<folder>` entry per folder with unread items, then one `feed/<id>` entry per feed with unread items:

```json
{"max":5,"unreadcounts":[
  {"id":"user/-/state/com.google/reading-list","count":5,"newestItemTimestampUsec":"1758700000123456"},
  {"id":"user/-/label/Comics","count":5,"newestItemTimestampUsec":"1758700000123456"},
  {"id":"feed/12","count":3,"newestItemTimestampUsec":"1758700000123456"},
  {"id":"feed/14","count":2,"newestItemTimestampUsec":"1758690000000001"}]}
```

**`GET /api/greader.php/icon/<id>-<hash>`** is unauthenticated and exists only while `greader.icon_urls` is on (the default). It serves the stored `feed_icons` bytes only for raster image types (JPEG, PNG, GIF, WebP, AVIF, ICO); SVG, HTML and anything else answer `404`, as do a malformed path, a hash that does not match, and the setting being off. The reply carries `Cache-Control: public, max-age=86400`, `X-Content-Type-Options: nosniff` and `Content-Security-Policy: default-src 'none'; sandbox`.

**Parameters ignored everywhere:** `output` (except on ClientLogin), `ck`, `client`, `includeAllDirectStreamIds`, `merge`, `likes`, `comments`, `mediaRss`, `types`.

---

## 7. Web UI API surface

Everything under `/api/` except the Reader paths and `POST /api/auth/login` requires the `kipple_session` cookie, and so does `GET /img/{sig}/{flags}/{u}` (§7.4), though it is not under `/api/`. `GET /healthz` (plain text `ok`, `Cache-Control: private, no-store`) is unauthenticated. Any other `/api/*` path answers `401 {"error":"auth"}` without a session and `404 {"error":"not_found"}` with one, never the SPA; `/api/greader.php*` reaching a UI route is a plain 404 (the Reader API has its own handler, §6). Details of the cookie:

- It is HttpOnly, `SameSite=Lax`, `Path=/`, persistent with `Max-Age` 90 days and sliding (`last_seen_at` updated at most hourly).
- **Web login pacing.** `POST /api/auth/login` and the two account endpoints that verify `current` share one `auth.FailureTracker` per client (Reader API ClientLogin uses the same type with its own instance): the first five failures are free, then each attempt waits 2 s, doubling per further failure to a 60 s cap; a verified success clears the client and the count fades only after an hour with no failure. One hash in flight per client, at most four attempts waiting up to ten seconds. An attempt that cannot get a turn answers `503 busy` + `Retry-After: 5` and counts nothing (never 429). The promise is exactly this: with `KIPPLE_TRUSTED_PROXY_IPS` correct each visitor is its own key, so a stranger cannot slow the owner's key's pacing; all keys share one password-hashing slot (5 s wait, also used by the public ClientLogin), so enough distinct addresses (an IPv6 /48 is 65,536 keys) can keep it full and give the owner `503 busy` even on their own key; people who truly share one key (an unlisted proxy, Docker Desktop's gateway, carrier NAT) share a budget, and even one persistent guesser there can keep the waiting room full so the owner's attempt answers `503 busy` until it stops. An attempt whose own wait is over the 10 s bound is answered at once with `Retry-After` set to the remaining wait. There is no lockout and no per-device exception. A busy verifier, a malformed body, an empty password, and on a passwordless account no usable token or `access_unavailable` (§7.0) are not failures; a wrong password, a refused token and a password sent to a passwordless account are. Client IP rules are those of §6.3.
- It is `Secure` when the effective scheme is https: `X-Forwarded-Proto: https` from a trusted proxy IP, or real TLS.
- It is persistent, not a session cookie, because WebKit bug 272325 drops session cookies in Home Screen apps.

**Same-origin enforcement** (`internal/api/api.go`: `authed`, `needsOriginCheck`, `sameOrigin`; every cookie-authenticated request whose method is not GET/HEAD, **and `POST /api/auth/login`** (which has no cookie yet; all three rules, so the login form must send `X-Kipple-Client`), plus the GET downloads `GET /api/opml`, `GET /api/stats/export` (the stats export, section 8) and `GET /api/backup/{token}`, which get rules 1 and 2 only; the job poll `GET /api/backup/jobs/{id}` gets all three because it returns the download token):

1. If `Sec-Fetch-Site` is present, it must be `same-origin`.
2. Else `Origin` must be present and equal `scheme://host` of the request, using the effective scheme.
3. The request must also carry `X-Kipple-Client: web|pwa`, except on the GET downloads (a plain link cannot set a header, and rules 1 and 2 already refuse cross-site requests), which makes a cross-origin `fetch` non-simple and forces a CORS preflight that Kipple never answers. `POST /api/stats/events` is exempt from this header only, because `sendBeacon` cannot set headers; it still needs rule 1 or 2.
4. Any failure returns `403 {"error":"origin"}`.

Other conventions:

- Unauthenticated calls get `401 {"error":"auth"}`. A session lookup that fails (a busy database) is `500 internal` (`503 maintenance` + `Retry-After: 15` during a search-index rebuild), never 401, so the app is not signed out.
- The UI JSON API never accepts a Reader token, and the Reader API never accepts a cookie (neither `kipple_session` nor the `kipple_device` profile cookie of §7.1c).
- `X-Kipple-Client` doubles as the stats attribution (`pwa` when `display-mode: standalone` matches).
- **Ids are strings** in every JSON body.
- Time values are unix seconds.

### 7.0 Cloudflare Access tokens and the optional web password

Optional, and off unless both `KIPPLE_ACCESS_TEAM_DOMAIN` and `KIPPLE_ACCESS_AUD` are set (one without the other stops startup). Kipple never needs Cloudflare; the rules below only add a trust signal for deployments that put Cloudflare Access in front.

- **Token check** (`internal/access`). The token is the `Cf-Access-Jwt-Assertion` request header (never the `CF_Authorization` cookie). It must be a three-part JWS with header `alg` exactly `RS256` (never `none`, never HMAC) and a `kid` found in the team key set at `https://<team domain>/cdn-cgi/access/certs`; the RSASSA-PKCS1-v1_5 SHA-256 signature must verify before the payload is parsed. Then `iss` must equal `https://<team domain>`, `aud` (a string or an array) must contain the configured AUD, `exp` is required and `nbf` is checked when present, both with 30 s of clock skew. Tokens over 8 KiB are refused unparsed.
- **Key set.** Fetched with a 10 s timeout and a 1 MiB cap. Only RSA keys of at least 2048 bits, with `alg` RS256 or absent and `use` sig or absent, each with a `kid` and an odd exponent of at least 3, are kept; a set with no usable key counts as a failed fetch. The set is refreshed after 1 h, in the background: a cached key answers at once (stale-while-revalidate), so a slow Cloudflare never stalls a request. A token naming an unknown `kid`, or no usable set, needs a fetch: a sign-in or account change waits for it (until its request ends), while the display-only check behind `access_email` never waits (it starts a background fetch and shows null). There is at most one fetch in flight and at most one attempt per minute (a background refresh is not even started inside that minute), so a flood of made-up key ids cannot hammer Cloudflare. When refreshes fail the last good set stays usable for 24 h after its last successful fetch, then nothing verifies until a fetch succeeds. The set is loaded once at startup so a wrong team domain is logged at once.
- **What a verified token means.** Only that Cloudflare Access admitted this request to this application. It never creates or replaces a session: every UI route still needs the `kipple_session` cookie, and the same-origin rules above apply unchanged. A token with no `email` (a service token) is not a person and never counts. A refused token is logged at debug level; a wrong issuer, wrong audience or a key id the configured team does not have even after a fetch (the sign of a wrong team domain) warn instead, at most once an hour. A key missing from the cache on the display-only path (startup, a rotation) is never a warning, and a key set that cannot be fetched is warned about by the fetch.
- **Optional web password.** An empty `password_hash` means the account has no web password; the password verifier never accepts an empty hash. Such an account signs in (`POST /api/auth/login`, the user name still has to match) only when **this request** carries a verified token, and proves itself on the account endpoints the same way. With Access validation not configured nothing verifies, so a passwordless account cannot sign in at all and startup logs a WARN: the port may be reachable on the LAN without Access in front, and a passwordless login without the token check would be an open door. An account that has a password always needs it; a token never stands in for a set password. One rule (`accessProof` in `internal/api/access.go`) decides what stands in for the missing password, and the pacing follows it: a refused token (bad signature, expired, wrong issuer or audience, a service token) or a valid token with the wrong user name counts like a wrong password; no token, or one naming a key id the set does not have even after a fresh fetch, is nothing presented and is not counted (it cannot succeed); a key set that cannot be loaded, or a key id missing from the cached set while a refetch is not due yet (a key rotation, most likely), is `503 access_unavailable` + `Retry-After: 5`, not counted, like a busy verifier, so the owner's own token right after a rotation never counts against them. A password sent to a passwordless account (with any token or none, unless the token signs the owner in) is checked against a constant decoy argon2id hash with the current parameters and refused and counted exactly like a wrong password, and an empty password on an account with one is refused uncounted: a request that carries a password cannot tell the two kinds of account apart by timing, answer or pacing. A request with a token and no password can (it is judged on the token); that only reveals that a valid Access token is the one way in. The account endpoints use the same rule after admission (`403 access_required`, `503 access_unavailable`), and with Access validation off they answer `403 access_not_configured` pointing to `kipple password` (`400 access_not_configured` for `remove:true`).
- **Getting a passwordless account.** Settings > Remove web password (`POST /api/account/password` with `remove:true`, offered only when the live `GET /api/auth/me` shows a verified Access email) needs Access configured, a verified token on that request (so the caller has just proved passwordless sign-in works and cannot lock themselves out or do it from the LAN) and the current password; on an account that already has none it is a no-op `204` that signs nobody out. The environment path never makes one: a new account from `KIPPLE_USERNAME`/`KIPPLE_PASSWORD` always gets a password, because an empty value is the example file's default and must never quietly mean "no password". The setup wizard (0.5) offers the same account, "no password, Cloudflare Access", but only on a request that carries a verified token (`accessProof`). Setting a password again needs only the token. `kipple password` on the host always sets one.

**Open mode (0.5).** A separate kind of passwordless account, `account.auth_mode = 'open'` with an empty hash, chosen only in the setup wizard or by `POST /api/account/password {current, open:true}`. It never involves Access. `POST /api/auth/open` mints a session only when the request passes the open gate (`internal/setup/gate.go`): an allowed `Host` (DNS-rebinding fence, also enforced in setup mode), not forwarded (no proxy or tunnel headers, peer not in `KIPPLE_TRUSTED_PROXY_IPS`; Tailscale Serve excepted), a local peer (loopback, link-local, RFC 1918, ULA, or a Tailscale address that arrived on this machine's own Tailscale address or reached a private one), and an `Origin` naming the requested host; every signed-in request keeps passing the network part. There is one rule and no setting (0.7: `security.open_lan` was removed): inside a container the peer is Docker's gateway, a private address, so the published port's bind address is the protection. Operator-facing description: docs/deploy.md, "Open mode"; design record: docs/setup-wizard-design.md sections 3 to 5.
- **Who can sign in.** Anyone the Access policy of that application admits (the AUD pins the application, the issuer the team). Keep that policy to the owner's own identity. The Reader API is unaffected: it keeps its own API password, generated on demand.

### 7.1 Endpoints

| Method and path | Request | Response |
|---|---|---|
| `POST /api/auth/login` | `{username, password}` (same Verifier as ClientLogin; the per-client pacing keys IPv6 by /64; `username` is compared trimmed and case-insensitively). An account without a web password signs in with an empty or ignored `password` only when the request carries a verified Cloudflare Access token (§7.0) | `204` + cookie and `Cache-Control: private, no-store`. Errors: `401 auth` (bad user or password; an empty password on an account that has one is not counted), `503 busy` + `Retry-After: 5` (verifier saturated, or the client's turn did not come in time; not counted as a failure), `503 access_unavailable` + `Retry-After: 5` (a passwordless account, no password sent, whose Access token cannot be checked right now, §7.0; not counted), `503 maintenance` + `Retry-After: 15` (search-index rebuild), `500 internal`, `400 bad_request` (malformed body), `403 origin` (the same-origin rule above) |
| `POST /api/auth/logout` | — | `204` |
| `GET /api/auth/me` | — | `{username, api_enabled, password_set, access_enabled, access_email}`: `password_set` is false for an account without a web password, `access_enabled` says Access token validation is configured, `access_email` is the email of this request's verified Access token or null (§7.0; checked against the cached key set only, never waiting). The bootstrap `user` object carries the same fields except `access_email`, which stays out of the offline-cached bootstrap |
| `GET /api/bootstrap` | — | `{user, settings:{…merged defaults…}, device:{id,name,profile,merged}, folders:[{id,parent_id,name,position,is_default,unread}], feeds:[{id,folder_id,title,site_url,icon:"/api/feeds/<id>/icon?h=<hash>",unread,status(§4.6),fulltext,retention,interval_minutes,auto_read_days,is_archive,starred_count,fulltext_effective}], saved_searches:[{id,name,q,scope?,order?}] (§7.1d, no counts), counts:{unread,starred,muted}, highlights:[{id,scope,folder_id,feed_id,terms,fields,case_sensitive,whole_word,fold_diacritics}], runs:[{…}], warnings:[…], server_time, version}`. `counts.muted` is the number of muted items (the partial index answers it). `feeds` and the folders' `unread` never include the archive feed (§6.9, decision 24), so `is_archive` is always false there; `counts` still includes its items. `highlights` lists the enabled, non-inverted `highlight` filters (text kind only) for the client to draw in titles and articles (§7.1b). `runs` holds the scheduler's runs and, while one is active, the filter apply run (`{id, kind:"filter_apply", filter_id, done, total, changed, new_items, errors}`) and the auto-read catch-up run (`{id, kind:"auto_read", ...}`, §7.1d). `warnings` carries the clock banner, a stale snapshot, and "unread total above 10,000: Reeder only syncs the newest 10,000 unread ids" |
| `GET /api/items` | `view=unread\|all\|starred\|muted`, `feed=<id>` \| `folder=<id>` (the folder with its subfolders), `q=<search>` (syntax in §2.4: phrases, `-word`, `title:`/`author:`, prefix; send it untrimmed), `typing=1` (search-as-you-type only: the unfinished last word also matches as a prefix; omit it for a submitted search, a saved search or any count), `order=date\|oldest\|rank`, `min_minutes=<n>`, `max_minutes=<n>`, `cursor=<opaque>`, `limit≤100`, or `ids=a,b,c` (SSE catch-up; ignores order; more than `maxIDsQuery` (the card limit) ids is `400`; the web client does not call it) | `{items:[Card], next_cursor\|null, as_of, fallback}`. With no `view` the list is `unread`, or `all` when `q` is set. `q` over 1000 bytes, `order=rank` with a blank `q`, `feed` and `folder` together and a negative `limit` are `400 bad_request`. A search that runs past its 500 ms budget is `422 {"error":"search_too_broad"}`. `fallback` is true only for a text search that had no exact match and shows partial (prefix/OR) matches instead (§2.4); false otherwise. `as_of` is the store's MaxCommittedID (highest committed item or ledger id, a string) read just before the query, on every response. A client sends it back as mark-read `max_id`, never the highest id on the page: ids follow arrival, not sort order, so an oldest-first page or a backdated new-feed item makes the page maximum wrong in both directions. Card = `{id, feed_id, title, url, author, excerpt(≤280 chars of content_text), image(proxied or null), published_at, sort_at, read, starred, word_count, reading_minutes(ceil(words/230), null under 100 words), origin_title(the feed an archived starred item came from, else null), source(`COALESCE(origin_title, feed title)`), muted_by(the muting filter's id, or null), muted_by_name(its name; null when the filter was deleted), snippet?}`. `view=all` and search leave muted items out (`muted_by IS NULL`); `view=muted` lists only them (`muted_by IS NOT NULL`, served by `idx_items_muted`; feed, folder, `q`, order and paging work as elsewhere); `unread` and `starred` need no clause (a muted item is read and never starred); an `ids=` lookup finds muted items too. The cursor is base64 of `sort_at.id` (newest first, keyset `(sort_at,id) < (?,?)`), `a<sort_at>.<id>` for `order=oldest` (`ASC`, keyset `>`) or `s2[f]\|<rank>\|<id>` for rank (an `f` records the fallback mode; cursors of the pre-schema-5 form `r<rank>\|<id>` are refused). A cursor that does not parse, is of the pre-schema-5 form or belongs to another ordering is `400 {"error":"bad_cursor","message":"…"}` (the status stays 400; the code lets the client restart the list from the first page instead of showing an error). Other bad parameters stay `400 {"error":"bad_request"}`. `min_minutes`/`max_minutes` (0 or absent = off, ≥0, min ≤ max) keep `word_count` in `((min-1)*230, max*230]`; items with `word_count` 0 (no extracted text) match neither filter, and items under 100 words count as quick reads (they carry `reading_minutes: null`). Search supports date and oldest order as well as rank |
| `GET /api/items/{id}` | — (prefetch-safe, never a stat) | `{…Card, content_html(image-proxied), fulltext:{mode, effective, available, error\|null}, enclosures:[…], feed:{id,title,site_url}}`. For a ledger id with a stub: the stub content with `trimmed:true` (star and mark-unread restore it). Otherwise 404 |
| `POST /api/items/{id}/open` | `{via?:"tap"\|"key"\|"nav"}` (the body may be empty; any other `via` is `400`; an unknown id is `404`) | In one `WithWrite`: `store.SetRead` (no stats side effect), then `Recorder.Record(tx, open)`. Returns `{session_key, item:<as GET>}` |
| `PUT /api/items/{id}/star` | `{starred: bool}` | `{starred, restored}`. A star on a ledger id restores it (§5). Records `star`/`unstar` in the same transaction only when `RETURNING` shows a change |
| `POST /api/items/mark-read` | `{ids:[…]}` or `{scope:{feed_id\|folder_id\|all:true, view:"unread"\|"all"\|"starred"\|"muted", q?, fallback?, min_minutes?, max_minutes?}, bound?:{order:"date"\|"oldest", side:"above"\|"below", anchor:{sort_at, id}, inclusive?:false}, max_id}`, plus `read: bool`, `reason: "swipe"\|"key"\|"scroll"\|"bulk"` | `{changed:[ids], restored:[ids], ledger_ids:[ids], count, undoable}`. Scope marks are `read:true` only (`read:false` with a scope is `400`; mark-unread is by id) and exactly one of `feed_id`, `folder_id` (the folder with its subfolders), `all` must be given. `max_id` is optional (default: the current MaxCommittedID, which a client should not rely on); it bounds the scope and sweeps every matching unread item with an id at or below it, whatever the list order. `scope.q` is at most 1000 bytes. A scope mark also marks matching trimmed-ledger rows read; `ledger_ids` lists them (withheld with `changed` above the caps, then `undoable:false`). Undo is `{ids: changed, ledger_ids, read:false}`: the ledger ids are flipped back by flag only, with no stub restored (up to 20,000; `ledger_ids` with `read:true` is a 400), and the reply lists the ledger ids that changed. `read:false` on other ledger ids restores them (§5). `reason` is only logged. **`bound`** (scope only, `read:true` only) marks the rows on one side of the anchor in the list's own `(sort_at,id)` order: `above` is the rows shown before the anchor, so `>` in a newest-first list and `<` in an oldest-first one; `below` is the opposite; the anchor is excluded unless `inclusive`. The client sends the order it shows and the anchor card's `sort_at` and `id`; no lookup happens, so a trimmed anchor works. `rank` is not accepted (relevance order has no above or below). `scope.q` adds the FTS filter, so "mark all results read" works (never as-you-type; `scope.fallback` is the list's `fallback` flag echoed back), and `min_minutes`/`max_minutes` mirror the list filter, so rows hidden from the list are never touched. A scope with `q`, reading-time limits or a `bound` never updates the ledger (it has no sort_at, text or words); an unfiltered scope still does. `count` is the number changed; `changed` carries at most 10,000 ids, above that it is `[]` and `undoable` is false (SSE is `resync` above 500 either way). Undo is the by-id `{ids: changed, read: false}` call. SSE: `items.state` or `resync`, then `counts`. **No stats code path** |
| `POST /api/items/{id}/fulltext` | `{mode: 1\|0\|null}` (omit to keep the mode), `?refresh=1` | Sets `items.fulltext_mode`. If the effective mode is 1 and no successful extraction exists (or `refresh=1`), it extracts synchronously (15 s budget, guarded client, go-readabilityV2, absolutized against the article URL, the same sanitize policy) and stores `item_fulltext`; a stored transient failure older than 1 hour is retried the same way (§7.5). Returns `{mode, effective, status:"ok"\|"error"\|"skipped", content_html(proxied), word_count, error}` |
| `GET /api/feeds/{id}/icon` | — | Icon bytes. `Cache-Control: private, max-age=604800` |
| `GET /api/imgcache` | — | Image cache stats, cap and free disk (see 7.4) |
| `POST /api/imgcache/clear` | — | `{cleared}`: deletes every cached image, failure record and host hint |
| `POST /api/feeds` | `{url, folder_id?, title?}` | `FindFeedByURL` first: an existing match returns `{status:"exists", feed}`. Else `{status:"choose", candidates:[{url,title,type}]}` when discovery finds several; else creates the feed, waits ≤ 8 s for the first fetch, and returns `{status:"ok", feed, fetch:{pending?\|outcome, new_items, error_class?, error?}}` (`pending:true` when the first fetch is not done yet). Errors: `400 invalid_url`, `400 folder_not_found`, `400 bad_request` (`title` over 200 characters or with control characters, or a bad `folder_id`), `400 unknown_field`, `422 no_feed`, `422 discovery_failed` (discovery has a 10 s budget) |
| `GET /api/feeds/{id}` | — | `feed`: the same FeedDetail object PATCH returns (`custom_title`, `dedup_mode`, `user_agent`, the network flags, `has_http_auth` (never `http_auth`), `retention`, `fulltext`, `interval_minutes`, `url`, `url_original`, `position`, `enabled`, `next_fetch_at`, `starred_count`, …). `404` for an unknown id, `409 archive_feed` for the archive feed (like PATCH and refresh). The editor loads it instead of sending a no-op PATCH |
| `POST /api/reorder` | `{folders?:[folder ids in order], feeds?:[{folder_id, ids:[feed ids in order]}]}`; ids are strings or numbers, at least one key | `{changed_feeds:[ids], changed_folders:[ids]}`. One transaction: folder positions become 0..n-1 in the order given (the web sends every folder in tree order, parents before children, so sibling order follows; moving a folder to another parent is a `PATCH /api/folders/{id}` first), and each listed feed gets position 0..n-1 in its `folder_id` (a feed listed under another folder moves there). Only rows whose position or folder changes are written. Any unknown or repeated id, a folder with two lists or the archive feed aborts everything: `400 bad_request` (message names the id), or `409 archive_feed`. Publishes `feed.changed` per changed feed and, when anything changed, one `folder.changed {}` (§7.3) |
| `PATCH /api/feeds/{id}` | Any of `custom_title, folder_id, position, interval_minutes, retention, auto_read_days, fulltext, dedup_mode, user_agent, http_auth, ignore_http_cache, disable_http2, allow_insecure_tls, allow_private_net, enabled, url`; any other key is `400 {"error":"unknown_field"}`, and the archive feed is `409 archive_feed` | `feed`. A `dedup_mode` change sets `rekey_pending`. `enabled:true` resets failures and sets `next_fetch_at = now`. A `retention` change enqueues a `trim_only` job. A URL change to a different host clears the stored `http_auth` unless the same patch sets it: credentials belong to the host they were entered for and are never sent to a new one. A URL change to another site (`fetch.SameSite`, the redirect-migration rule) also resets `allow_insecure_tls` and `allow_private_net` unless the same patch sets them, and the new URL is validated without the kept private-network grant |
| `DELETE /api/feeds/{id}` | `?delete_starred=1` | `204`. By default starred items move to the archive feed (§6.9). Deleting the archive feed itself while it holds starred items answers `409` `{"error":"archive_has_starred","message":...}`; pass `?delete_starred=1` to really delete. The confirm dialog shows `starred_count` from the feed object. Any `delete_starred` value other than `0` or `1` is `400 bad_request` |
| `POST /api/feeds/{id}/refresh` | `?full=1` | Waits ≤ 15 s: `{outcome, new_items, error_class, error}`, or `202 {pending:true}`. `error_class` and `error` are `null` when empty. Errors: `400 bad_request` (`full` other than 0 or 1), `404`, `409 archive_feed`, `409 disabled`, `503 shutting_down` |
| `POST /api/feeds/{id}/mark-fetch-read` | `{fetch_log_id}` | Marks read the ids in that row's `[first_item_id,last_item_id]` range for this feed. Bulk, no stats. `{changed}` |
| `POST /api/feeds/{id}/trimmed-unread/reset` | — | Sets `trimmed_unread_count = 0`, `trimmed_unread_since = now`. `204` |
| `POST /api/archive/purge-unstarred` | — | Deletes unstarred items of the archive feed. `{deleted}` |
| `POST/PATCH/DELETE /api/folders[/{id}]` | `{name, position, parent_id}` (`name` trimmed, 1 to 100 characters, no control characters; `position` 0 to 1,000,000; `parent_id` a folder id string, or `null` for the top level, the default when absent on POST; a PATCH with `parent_id` moves the folder and its subfolders) | `201` folder (POST), `200` folder (PATCH), `204` (DELETE). A folder is `{id, parent_id, name, position, is_default, unread}`, its `unread` counting its whole subtree. Deleting a folder deletes its subfolders too, moves all their feeds to the default and publishes `feed.changed` for each moved feed, then `folder.changed {folder_id}`. Errors: `409 folder_exists` (create, rename or move onto a name taken among the new siblings, or onto a full path another folder has, such as a top-level `AC/DC` against `DC` inside `AC`), `409 folder_cycle` (a move inside itself or one of its subfolders), `409 folder_too_deep` (more than 8 levels), `409 default_folder` (deleting or moving the default folder, or a subfolder of it), `400 folder_not_found` (no such parent), `400 unknown_field`, `400 bad_request`, `404` |
| `POST /api/refresh` | — | `202 {run_id, total, joined}`; `503 shutting_down` |
| `GET /api/status` | — | `{runs:[{id,kind,done,total,new_items,errors}], inflight, unread_total, muted}`. The SSE fallback. `runs` is the scheduler's runs plus, while active, the filter apply run and the auto-read catch-up run (as in bootstrap); `muted` is the muted-item count (as `counts.muted`) |
| `GET /api/events` | `Last-Event-ID` header, or `?last_event_id=<seq>` (the header wins when both are sent) | SSE (§7.3) |
| `GET /api/health/feeds` | — | `{feeds:[{id,title,url,url_original,status(§4.6),redirect_pending,notices,enabled,disabled_reason,last_success_at,last_fetch_at,last_error_at,last_error_class,last_error,last_status,consecutive_failures,current_delay_s,next_fetch_at,redirect_to,redirect_kind,redirect_count,last_new_items_at,trimmed_unread_count,trimmed_unread_since,host_throttled_until}], clients:[{family,last_seen_at}], snapshot:{last_at,last_error}, clock:{ahead_s}, db:{db_bytes,wal_bytes,backup_bytes,imgcache_bytes}, unread_total}`. `redirect_pending` is true while a permanent redirect is seen but not yet migrated (the migration itself is a kept `fetch_log` note). `host_throttled_until` is the host deadline in unix seconds, or null. `db` sizes the database, its WAL and the backup directory tree, recursively (snapshots, pre-migration and `pre-restore-*` copies, and the `export/` scratch files) `imgcache_bytes` sizes the image cache directory (files, temp files and index) |
| `GET /api/health/feeds/{id}/log` | — | The feed's fetch_log rows (14 days, plus kept rows) |
| `GET /api/settings`, `PATCH /api/settings` | `{key: value, …}` (whitelisted user keys, validated) | `{settings: [{key, value, default, label, description, group, kind, options, min, max, step, unit, surface, scope}, …], values: {key: value, …}}`. `scope` is `global` or `device` (each device may override the row, see §7.1c; a `both` value is declared but no setting uses it); `surface` is `reader_menu`, `settings` or `hidden`. A `retention.default` change starts a `retention` run. A `400` carries `{error:"invalid_settings", message, keys:[…], issues:[{key, message}]}`: `sys.*` keys give "read-only setting", unknown keys "unknown setting", and a saved-search list naming a missing feed or folder is rejected the same way from inside the write; an empty body is `400 bad_request`; `null` resets a key to its default. Side effects: lowering `refresh.interval_minutes` pulls due times in and wakes the scheduler; `imgproxy.mode` refreshes the CSP `img-src`; `imgproxy.cache_mb` applies the cap at once; replacing or resetting `saved_searches` publishes `saved_searches.changed` |
| `GET /api/device`, `PATCH /api/device`, `PUT /api/device/name`, `GET /api/devices`, `POST /api/device/copy-from/{id}`, `POST /api/device/make-default`, `DELETE /api/devices/{id}` | §7.1c | §7.1c |
| `POST /api/account/password`, `POST /api/account/api-password` | `{current, new}` or `{current, generate:true}`; `POST /api/account/password` also takes `{current, remove:true}` (§7.0) | `204`, or `{api_password}` once when generated (24 characters). `POST /api/account/password` signs out every other session and keeps the caller's. Both routes verify `current` under the same per-client pacing as sign-in (a wrong one counts as a failure): `403 bad_password`, `503 busy`, `400 bad_new_password` (web password `auth.MinPasswordLen` to `auth.MaxPasswordLen`; a chosen API password `auth.MinAPIPasswordLen` to `auth.MaxPasswordLen`; checked before admission, not counted), `400 bad_request` (`api-password` needs exactly one of `new` and `generate`). Changing the API password revokes the Reader token and clears the login memo. A forgotten web password is reset from the host with `kipple password` (§2.6). For an account without a web password, a verified Access token on the request stands in for `current` (else `403 access_required`, `503 access_unavailable` + `Retry-After: 5` when it cannot be checked right now, or `403 access_not_configured` when Access validation is off: set a password with `kipple password`). `remove:true` removes the web password and signs out every other session; it needs Access validation configured (else `400 access_not_configured`), a verified Access token on the request (else `403 access_required`, or `503 access_unavailable` when it cannot be checked right now; checked after admission, so a refused token counts) and the correct `current`; on an account that already has no web password it is a `204` that changes nothing; `remove` with `new` is `400 bad_request` |
| `GET /api/filters`, `POST /api/filters`, `PATCH /api/filters/{id}`, `DELETE /api/filters/{id}` | §7.1b | §7.1b |
| `POST /api/filters/preview`, `POST /api/filters/{id}/apply` | §7.1b | §7.1b |
| `POST /api/opml` | Raw OPML or multipart; `?mark_read_older_than_days=N` (1–365, optional); `?move_existing=true` (optional, default `false`: move feeds that already exist into the file's folders) | `{folders_created, feeds_added, feeds_existing:[{url, feed_id}], feeds_moved:[{url, feed_id}], folders_emptied:[path], folders_merged_case:[{kept, merged}], folders_merged_path:[{kept:[names], merged:[names]}], folders_refused:[{path, reason}], memberships_dropped:[{url, kept, dropped:[…]}], skipped, invalid_attrs, ignored_attrs, run_id}`; folders are named by path (`Tech/Apple`). Errors: `400 bad_days` (`N` not an integer from 1 to 365), `400 bad_move_existing` (`move_existing` not `true` or `false`), `400 bad_request` (a multipart body with no file part), `400 bad_opml` (unparseable OPML), `413 too_large` (body over 8 MiB) |
| `GET /api/opml` | — | OPML attachment (§7.6) |
| `POST /api/backup` | — (needs `X-Kipple-Client`) | `200 {status:"ready", token, url:"/api/backup/<token>", filename:"kipple-backup-YYYYMMDD-HHMMSS.zip", bytes, expires_at (unix), expires_in (s), warning, contents:{kipple_version, schema_version, created_at, feeds, items, starred, db_bytes}}` when the build finishes within about 5 s, else `202 {job_id, status:"building"}` at once. The export is a background job on the manager's own context: a client disconnect or a proxy's origin timeout (Cloudflare answers 524 after about 100 s) does not cancel it, and the 10 min build limit still applies. Poll `GET /api/backup/jobs/{id}` (session, same-origin and `X-Kipple-Client`, as it returns the token): `{status:"building"}`, the ready payload above, or `{status:"failed", error:"no_space"\|"too_large"\|"internal", message}`; `404 gone` for an unknown id or once the export was downloaded, replaced or expired. The token TTL (5 min) counts from the moment the export is ready. Builds the export (§2.6). `409 {error:"busy", retry_after, message}` + `Retry-After` while a snapshot or export runs; `507 {error:"no_space", message}` below 2.2x the database size free; `413 {error:"too_large"}` over 4 GiB (a failure inside the sync window answers with the same statuses; later it shows in the job). One export at a time, and the temporary files are removed on failure and at startup. The download route answers HEAD with 405 (it would otherwise spend the single-use token) and HEAD gets the same same-origin rule as GET |
| `GET /api/backup/{token}` | Session cookie, the download origin rule (§7), and the token | The zip as `Content-Disposition: attachment; filename="kipple-backup-….zip"`, with `Content-Length`, no Range. The token is 128-bit, valid 5 min and **spent by the first request**, complete or not; then `404 {error:"gone"}`. The file is deleted after the transfer. Each 256 KiB chunk has its own 60 s write deadline and the whole download is capped at 30 min |
| `POST /api/retention/apply` | — | `202 {run_id, total}`; `503 shutting_down` |
| `GET /api/stats/summary` | `?range=week\|month\|year\|all` (default `month`) or `?from=YYYY-MM-DD&to=YYYY-MM-DD` (local dates, inclusive, at most 3660 days, both ends included; anything else is `400`); `include_inferred=1` (default 0) | The summary of §8: `{enabled, tz, week_start, range, first_event_date, totals, daily, streaks, heatmap, behavior, sources, sources_truncated, never_opened}`. Reader pool only. With `stats.enabled` off: `{enabled:false, tz, week_start}` and empty collections, `200` |
| `POST /api/stats/events` | `{events:[{kind:"read_time"\|"scroll"\|"open_original"\|"share", item_id, session_key?, event_id?, value?}], client?:"web"\|"pwa"}`. Accepted via `fetch` or `navigator.sendBeacon` (a JSON Blob) | `204`. Invalid events are dropped silently (§8) |
| `GET /api/stats/export` | `?format=csv\|json\|jsonl` (default `csv`; `json` when `content=summary`), `content=raw\|summary` (default `raw`), the range of the summary (`range=week\|month\|year\|all` or `from=&to=`; default `all`), `titles=1\|0` (default 1), `include_inferred=1` (default 0), `bom=1` (csv only, default 0). Rules 1 and 2 only (a plain download, no `X-Kipple-Client`) | An attachment (`kipple-stats-YYYYMMDD.csv\|json\|jsonl`, `kipple-stats-summary-YYYYMMDD.json`; `-no-titles` before the extension when `titles=0`), `Cache-Control: no-store`, streamed; headers `X-Kipple-Titles-Included`, `X-Kipple-TZ`, `X-Kipple-Include-Inferred` and, on raw, `X-Kipple-Rows`. `400 bad_request` with a `message` for a bad format, content, titles or range, or `content=summary` with `csv` or `jsonl`. Works with recording off (§8) |
| `GET /api/stats/dictionary` | `?format=json\|md` (default `json`) | The data dictionary of §8: every export column with type, unit and description, the kinds, the definitions and the summary fields. `md` is Markdown text, an attachment `kipple-stats-dictionary.md`. `400` for another format |
| `POST /api/stats/delete` | `{from, to, dry_run?}` (local dates, inclusive) or `{all:true, confirm:"DELETE ALL", dry_run?}` | `{count, deleted}`; a dry run counts and returns `deleted` 0. Deletes only `stats_events` rows, in bounded batches (§8). All three same-origin rules. `400 bad_request` for a bad or missing date, `to` before `from`, an unknown field, `all` with dates, or `all` without the exact confirm phrase (a dry run needs none). A delete that stops after earlier batches committed answers `500` (`503` with `Retry-After` during maintenance) with `{error, message, deleted, complete:false}`; running it again finishes |
| `POST /api/maintenance/fts-rebuild` | — | Runs `'rebuild'` on the writer behind the commit gate, bounded at 45 s. `204`. While it runs every write that does not queue on the commit gate (read and star changes, edit-tag, logins, settings, filter edits) is refused at once with `503 {error:"maintenance"}` + `Retry-After: 15` here and `503` + `Retry-After: 15` on the Reader API, instead of waiting out the 10 s write deadline and failing with a 500; clients retry (the web app's `api()` retries after `Retry-After`, 1 to 15 s, for up to 60 s (`MAINTENANCE_WAIT_MS`, `web/src/api/client.ts`), then says Kipple is busy rebuilding its search index). Fetch commits and maintenance batches wait on the gate and are not refused |
| `GET /api/backup/jobs/{id}` | Session, all three same-origin rules (it returns the download token) | `{status:"building"}`, the ready payload of `POST /api/backup`, or `{status:"failed", error, message}`; `404 gone` (see `POST /api/backup`) |
| `GET /healthz` | — (no session) | Plain text `ok` |
| `GET /_status`, `GET /_status.js` | — (no session) | The one-file status page and its script (`internal/web`), `Cache-Control: no-cache` and an ETag; the page is also served at `/` when `web/dist` has no build |
| `GET /img/{sig}/{flags}/{b64url}` | — (session cookie) | Proxied image (§7.4) |
| `GET /` and SPA paths | — | Embedded `index.html` (`no-cache`, ETag). `/assets/*` is immutable. MIME types are registered for woff2, woff, ttf and webmanifest |
| `GET /sw.js`, `/manifest.webmanifest`, `/favicon.ico`, `/favicon.svg`, `/apple-touch-icon.png`, `/icon-*.png` | — | The files at the top of the web build (copied from `web/public`, plus the generated `sw.js`), each served at the root with `Cache-Control: no-cache` and an ETag; `sw.js` is `text/javascript` with `Service-Worker-Allowed: /`, the manifest `application/manifest+json`. Dotfiles, names starting with an underscore, names with characters outside letters, digits, dot, dash and underscore, and `index.html` are never served this way (§7.9) |

### 7.1e Setup wizard routes (0.5, `internal/api/setup.go`, `internal/setup`)

| Route | Auth | What |
|---|---|---|
| `GET /api/instance` | none | `{setup, auth: null\|"password"\|"access"\|"open"}`, the one fact the signed-out app needs to choose its first screen. While `setup` is true it also carries `access` (`enabled`, `verified`) and `open` (`reason`, null when open mode works from here): what the account form can offer from here. |
| `POST /api/setup/account` | setup mode only (registered only when the process starts with no account: the process that did the claim answers `404` afterwards, and after a restart the route does not exist, so a signed-out request gets the `/api/` catch-all's `401`); Host gate, same-origin, `X-Kipple-Client` | Create the account if none exists and sign the browser in with the normal session cookie. There is no setup code (removed in 0.7): an unclaimed instance is "no account row". One claim is hashed and inserted at a time; exactly one racing caller gets `201` (`INSERT ... ON CONFLICT DO NOTHING`), the rest `409 already_set_up` and no session (a claim that arrives after the winner finished gets `404`: the route is gone). While unclaimed, only this route, `GET /api/instance`, `/healthz` and the app shell answer: other `/api` routes `401`, `POST /api/auth/login` and `/api/auth/open` `409 setup_required`, the Reader API `401` (its static probe paths `/api/greader.php` and `/check/compatibility` answer `200` with no data, `/icon/...` `404`), and the scheduler, maintenance and favicon finder are not started until the claim. |
| `POST /api/auth/open` | open gate | Open mode only: a session for a request that passes the open gate (`403 open_refused` with a reason otherwise). |
| `GET/POST /api/starter-feeds` | session | The embedded recommended-feeds list; subscribe by id only. |
| `POST /api/onboarding/complete`, `POST /api/onboarding/restart` | session | Set or clear `sys.setup_completed_at` ("Finish", "Skip", "Run setup again"). |

Host gate (`421`) in setup and open mode, the `TZ` seeding of the `tz` setting and the port rules are described in
`docs/setup-wizard-design.md` (sections 5.2, 7a, 8.3) and, for operators, `docs/deploy.md`.

### 7.1a Library settings added in phase 2 (no schema change)

- **`fetch.fulltext_all`**: bool, default `false`, group `library`, surface `settings`, label "Fetch the full article for every feed". Semantics in §7.5. Read paths never need a restart or a backfill.
- **`library.favorites`**: json, default `[]`, group `library`, surface `hidden`. The sidebar's server-side favorites, in display order: an array of at most 500 objects `{"t":"folder"|"feed","id":"<digits string>"}`. `PATCH /api/settings` validates strictly and replaces the whole list: each element must be an object with exactly the keys `t` and `id`, `t` one of the two kinds, `id` a decimal string that parses as a positive int64 (so not `"0"` and not above 9223372036854775807; ids are strings on the wire, as everywhere) and is stored in canonical form (`"007"` becomes `"7"`, so `"7"` and `"007"` are one entry and a repeat is rejected). The PATCH rejects any other shape, and a repeated `(t, id)`, with `400 invalid_settings` naming the key. Stored lists are normalised when read (`MergedSettings`) and when a deleted folder or feed is dropped from them: ids are canonicalised, and malformed or repeated entries are dropped silently. It is returned in `GET /api/settings` (`values` and the setting's `value`/`default`) and in `bootstrap.settings`. Deleting a folder (`DELETE /api/folders/{id}`) or a feed (`DELETE /api/feeds/{id}`, or an unsubscribe from a sync app, which is the same removal) drops its entry in the same transaction, so the list never names a dead id; a stale id sent by a client is accepted as written (the client should filter against the folder and feed lists it has).
- **`greader.icon_urls`** now defaults to `true`. A database that has no stored row for it picks the new default up at once (nothing is written or migrated); a stored `false` stays `false`.

### 7.1a2 Phase 3 additions to the item routes

- `GET /api/items?include=content` returns each item as `GET /api/items/{id}` does (`content_html`, `fulltext`, `enclosures`, `feed`, `trimmed`) instead of a card, so a client can keep a page for offline reading in one request. `limit` (default and maximum) is 50 with it, and `ids=` may name at most 50; more is `400 bad_request`. Any other `include` value is `400`. An id gone between the list and the detail read is left out. The serve-time options (image proxy, link stripping) are looked up once per feed, not per item.
- `PUT /api/items/{id}/star` accepts `at` (unix seconds): when the star happened, for a change a client queued offline. Absent means now, a time ahead of the server clock counts as now, one more than 30 days old is stamped 30 days back (a queue that waited that long still lands), and a value that is not positive is `400 bad_request`. It sets `starred_at`. The stats event is stamped at replay time.
- Every `/api/*` response (not the Reader API) carries `X-Kipple-API: <n>`, the version of the web API contract (`httpx.APIVersion`). It is raised only for a change that would break the web app built for the previous version; the app compares it with its own and offers a reload when the server's is higher.

### 7.1b Filters (`internal/api/filters.go`, engine `internal/filter`)

A `folder` filter covers the feeds of that folder and of every folder below it: the engine gets each item's folder followed by the folders above it and matches when the rule's folder is among them. Moving a feed or a folder changes what a folder filter covers from the next evaluation (ingest, preview, apply).

All routes need the session and the same-origin rules of §7. Ids are strings in every body (numbers are accepted in requests). A `Filter` is `{id, name, enabled, scope("global"|"folder"|"feed"), folder_id|null, feed_id|null, kind("text"|"regex"), terms:[…], fields:["title"|"author"|"content"|"url"|"category"|"feed"], case_sensitive, whole_word, fold_diacritics, invert, action("mute"|"mark_read"|"star"|"highlight"), position, hits, last_hit_at|null, created_at, updated_at, disabled_reason|null, muted_items}`. `muted_items` is how many items the filter currently mutes (orphans of deleted filters are counted under their old id). `disabled_reason` is set on a disabled filter that Kipple switched off because it no longer meets the current limits (a rule saved by an older version, or one that pushed the enabled regex rules past the set cap), e.g. `"terms[0]: repeats something 400 times … (edit the filter to re-enable it)"`; an edit that validates clears it. Validation (`400 bad_filter`) judges only the filter being created, edited, previewed or applied, plus the set-wide sums over the enabled filters, so such a stored filter never blocks another one. Matching semantics, limits and precedence are the package comment of `internal/filter`.

| Route | Request | Response |
|---|---|---|
| `GET /api/filters` | — | `{filters:[Filter]}` in `position, id` order |
| `POST /api/filters` | Any `Filter` field except the read-only ones. Defaults: `enabled` true, `scope` global, `kind` text, `fields` ["title"], `whole_word` true, `fold_diacritics` true; `action` and `terms` are required. Optional `apply_existing:{include_read:bool}` | `201 {filter, applied}`; `applied` is null, or the started apply run (below), or `{error:"busy"}` when another apply is running (the filter is created either way) |
| `PATCH /api/filters/{id}` | Any subset of the fields. Changing `scope` clears the folder or feed that no longer fits unless the request names it. Never retroactive | `{filter}` |
| `DELETE /api/filters/{id}` | `?unmute=keep\|read\|unread` (default `read`; `1` and `true` are aliases of `read`, the safe mode: `unread` must be spelled out) | `{changed, made_unread, done}`: items restored by this call, how many of them went back to unread, and whether the restore finished (`200`; `202` with `done:false` when the request's 40 s restore budget ran out: repeat the `DELETE` to resume). See below |
| `POST /api/filters/preview` | `{filter:{…}}` (an unsaved rule; `action` required) or `{id}` (a saved one), or both (the `filter` fields overlay the saved rule, for an edit in progress); `include_read:false` | `{matches, scanned, truncated, sample:[Card ≤ 20], warnings:[{code,message}]}` |
| `POST /api/filters/{id}/apply` | `{include_read:false}` (body optional) | `202` with the run `{id, kind:"filter_apply", filter_id, done, total, changed, new_items, errors}` |

**Errors.** A validation failure is `400 {"error":"bad_filter", "field":"terms[2]", "message":"…"}` where `field` is the engine's vocabulary (`scope`, `kind`, `action`, `terms` (no terms, more than 50 text terms, more than 5 regex patterns), `terms[i]`, `fields[i]`, `name` (over 200 bytes)), plus `folder_id` or `feed_id` for an unknown target (a feed scope naming the archive feed is `feed_id: no such feed`), `id` (a bad or duplicate id; in a preview, an `id` that is not an id), `filter` (a preview with neither `filter` nor `id`), `rules` for the 200-filter cap, `kind` or `terms` for the regex and term caps of the whole set, `unmute`, `apply_existing`, and `enabled` or `action` for an apply that cannot run. A malformed body is `400 {"error":"bad_request"}`; an unknown id is `404 {"error":"not_found"}`. Validation compiles the whole set, so the set-wide limits apply to a create and a patch (every stored rule counts toward the 200) and to a preview (evaluated against the saved enabled rules plus itself; disabled rules do not count toward its cap).

**Preview** evaluates the rule together with the saved enabled rules, so precedence holds (a mute that a star rule would cancel is not counted). Candidates are the items of every non-archive feed in the rule's scope that are unread and unstarred, or every item with `include_read`; a match is counted only if the action would take effect (a mute on a starred or already muted item, a `mark_read` on a read one and a `star` on a starred one do nothing; a `highlight` counts every match). It reads the reader pool in keyset pages of 1000 (`id DESC`), holds no read transaction across pages and stops at a 5 s budget, checked on every item (`truncated`; `scanned` is the items actually evaluated). The scan loads every field that the rule and the enabled star rules read (the only rules that change its effect), with the largest content limit among them, and the `feed` field is the same title as at ingest (custom title, else stored title, else the feed URL). It writes nothing. `warnings` carries `category_new_items_only` for a rule that scans categories: articles without stored categories (everything fetched before categories were stored, and feeds that carry none) are skipped by category rules, inverted ones included.

**Apply** is a run on a background goroutine (one at a time; a second start is `409 {"error":"busy"}`). It rejects a disabled or `highlight` rule (`400 bad_filter`). It pages candidates like the preview and writes matches in batches of 500 ids, each its own `WithWrite` behind the commit gate: `mute` sets `read = 1, muted_by = <this rule>` (the applied rule's id; at ingest it is the lowest matching mute rule), `mark_read` reads, `star` stars and clears a mute. Every UPDATE re-checks the state it expects, so a change made between the scan and the write is respected, and the rule's `hits` grow by what changed. Events: `run.start {run_id, kind:"filter_apply", total, filter_id}`, `run.progress {run_id, done, total, new_items:0, errors:0, changed}` (at most every 500 ms), one `items.state` per batch (`read`, `muted` or `starred` set as appropriate; at most 500 ids), `run.done {run_id, kind, filter_id, new_items:0, errors, changed, scanned}` (`error` when it did not finish: `cancelled` (an edit or delete of the rule), `filter_changed` (the per-batch check below) or `shutdown`, with `errors:0`; `timed_out` (a 30 minute budget) or `apply_failed`, with `errors:1`), then `filters.changed` and `counts`. Every write batch first checks, in its own transaction, that the rule is still stored, enabled and unchanged; otherwise it writes nothing and the run ends (`run.done` with `error`). Deleting a filter, or a patch that changes how it matches or acts (anything but `name` and `position`), cancels a running apply of that id and waits for it before it proceeds; the run checks for a cancel on every item. The candidate count at the start runs outside the apply lock (the run is listed with `total:0` meanwhile); a cancel during it answers `409 {"error":"cancelled"}`. A mute records `muted_was_read` (the read state before it) beside `muted_by`. There is no undo token: the reverse of a mute is un-muting (mark unread, star, or delete the filter with `unmute`), and of a `mark_read` a mark-unread.

**Delete** is resumable and un-mutes first: it cancels a running apply of the rule, then disables the row (one transaction, generation bumped, so ingest and any apply stop muting with it), restores in batches of 500 by `id`, each behind the commit gate, and deletes the row last (generation bumped again). The restore runs detached from the request, so a client that gives up mid-batch does not roll it back. One request restores for at most 40 s (under the server's 60 s write timeout) and then answers `202 {done:false}`; the rule is already disabled, and the client repeats the `DELETE` until `done` (the web client does). A run that is cut off any other way also leaves a disabled rule plus some still-muted items: the same `DELETE` finishes it. `read` clears `muted_by` (the items stay read and appear in All); `unread` clears it and marks unread **only the items that were unread when the rule muted them** (`muted_was_read = 0`), so items the user had read before the mute, or that the initial-read window read at ingest, keep their read state and `read_at`; `keep` just deletes the row and leaves the items orphaned, shown as "muted by a deleted filter" (`muted_by_name` null). For an id whose row is already gone, `read` and `unread` still restore orphans that carry that `muted_by` (`200` with their count; `404` only when there is neither a row nor an orphan). Each batch is one `items.state {ids, muted:false}`, followed for `unread` by one `items.state {ids, read:false}` for the items that went back to unread; `filters.changed` and `counts` follow.

**Un-muting elsewhere.** `SetRead(false)` (web mark-unread, Reader `r=read`) clears `muted_by` in the same UPDATE; `SetStarred(true)` clears it too and leaves the item read. Both also accept an item that violates the invariant (`muted_by` set, unread) as a repair. Nothing else changes for the Reader API: a muted item is an ordinary read item, so `unread-count` and `stream/items/ids?xt=read` already agree with the web unread counts, before and after a retroactive apply or un-mute. The Reader hold (§6.5) never sees a muted-at-ingest item, because it is never queued for extraction; an item muted retroactively while its extraction is pending stays held until the extraction ends, then shows as read.

**Client drawing.** `highlight` filters store nothing per item. Bootstrap `highlights` gives the client each enabled, non-inverted text rule (`id, scope, folder_id, feed_id, terms, fields, case_sensitive, whole_word, fold_diacritics`); the client matches them in titles and article text with the same word-boundary and diacritic rules. A regex highlight is refused at validation because JS regex is not RE2.

### 7.1c Device profiles (`internal/api/devices.go`, `internal/store/devices.go`)

A device is one browser or installed app (an iOS Home Screen app has its own cookie jar, so it is its own device). The server-set `kipple_device` cookie (128-bit random base64url, HttpOnly, `SameSite=Lax`, `Path=/`, `Secure` when the effective scheme is https, `Max-Age` 400 days) only selects a row of `devices`; it grants nothing, is never accepted by the Reader API, and no route requires it. It is issued on the first bootstrap or `/api/device*` request that lacks a valid one, and slides (is re-issued) when `last_seen_at` is bumped, at most daily. A missing, malformed or unregistered value (evicted, deleted, made up) registers a new device under a server-made id; a client never chooses its own id. Registration is guarded per login session: concurrent first loads share one device (one registration at a time, and a request without a valid registered cookie within 30 s of the session's last registration gets that device back), a session registers at most 5 devices a day (beyond that it keeps getting its last device, or the unsaved default device if that device is gone); the stored user agent is truncated to 300 characters, and when the table is full of recently seen devices the client is served defaults as an unsaved device (`id` empty, no cookie; the writes to the device itself, `PATCH /api/device`, `PUT /api/device/name` and `copy-from`, answer 404, and `make-default` is a no-op `200`). The per-session record is in memory; a restart forgets it. A rejected (401 or 403) request registers nothing.

**Profile keys.** The profile is a flat JSON object of overrides. Allowed keys are the settings whose `scope` is `device` (`ui.theme`, `ui.theme_day`, `ui.theme_night`, `ui.theme_schedule`, `ui.theme_night_start`, `ui.theme_day_start`, `ui.font_body`, `ui.reading_density`, `ui.list_density`, `ui.mark_read_on_scroll`), validated by the very `settingDef.check` of `PATCH /api/settings`, and the client-only `client.*` keys: `client.layout` (`magazine|cards|compact|inbox|headlines`), `client.layout_overrides` (`{feed:{id:layout},folder:{id:layout}}`, numeric-string ids, at most 200 each), `client.order` (`newest|oldest`), `client.search_order` (`relevance|newest|oldest`, default `relevance`: the order of a text search, kept apart from the list order so it syncs per device), `client.inbox_thumbs` (`auto|off`), `client.peek_seen`, `client.article_width` (`narrow|medium|wide|full`), `client.list_width` (260 to 720), `client.sidebar_width` (200 to 420), `client.collapsed_folders` (at most 200 numeric-string ids), `client.link_target` (`new|same`), `client.unread_badge` (`count|dot|off`), `client.text_size` (0.875, 1, 1.125, 1.25, 1.5), `client.adjust_separately`, `client.shortcuts`, `client.spacing` (`snug|normal|roomy`), `client.motion` (`system|on|off`), `client.large_targets`, `client.highlight_keywords` (default true: draw the keyword highlights of §7.1b in lists and articles; false hides them on this device), `client.listen`, `client.voice` (at most 200 characters) and `client.rate` (0.8, 1, 1.2, 1.5). A key without a built-in default (`client.list_width`, `client.link_target`, `client.shortcuts`) is absent from `defaults` and `merged` until set: the client decides (from the layout, the platform, touch-first). Any other key, and any `global` setting, is `400 invalid_settings`. The stored profile is at most 8 KB (`413 too_large` above). Theme ids are the round-2 schemes; the first-draft ids are aliases (`store.ThemeAliases`) applied when a value is written or read. The same holds for the first-draft spacing names (`compact` reads as `snug`, `comfortable` as `standard`) and for a font's display name, which reads as its id (`store.Fonts`); `ui.font_body` stores the id.

**Theme modes.** A device shows a fixed theme (`ui.theme` a scheme id) or its day and night pair (`ui.theme` `system`, with `ui.theme_day` and `ui.theme_night`). The pair follows the OS's `prefers-color-scheme` or, with `ui.theme_schedule` true (issue #32), two fixed times of day whatever the OS reports: "On a schedule" next to "Match my device". The schedule is a separate flag rather than a third `ui.theme` value so an older client passes it through unchanged: it reads the device as follow-system and never writes the flag. Picking a fixed theme turns the flag off; `PATCH /api/device` does that itself when a patch sets `ui.theme` to a scheme id (or clears it, leaving a fixed account default) without naming `ui.theme_schedule`, so an older client that picks a theme and later "Match my device" gets plain follow-system, not a schedule it cannot show. The times are `ui.theme_night_start` and `ui.theme_day_start`, 24-hour `"HH:MM"` strings (`00:00` to `23:59`, defaults `21:00` and `07:00`); all three keys are device-scoped with surface `hidden`, because the Settings theme picker draws them. They are read on the **device's own clock**, not the server's `tz` setting: the device is where the screen is. The night window runs from the night start (inclusive) to the day start (exclusive) and wraps past midnight when the night start is the later time (`21:00` to `07:00` is night from 21:00 until 06:59); equal times mean no night, so the day theme stays. The rule is `isNightAt()` in `web/src/theme/settings.ts`, repeated in the theme boot script so the first paint is already right. On daylight-saving nights it follows the clock as it reads: a time the clock skips switches when the gap ends, and a time it shows twice switches both times. While the schedule is on, `initTheme()` arms one timer for the next switch, found by stepping real minutes until `isNightAt()` changes and capped at 15 minutes so a clock change is caught up; the timer is re-armed, and the theme corrected, whenever the tab becomes visible or focused. The theme therefore changes live, without a reload. A time field saves only a committed time (Enter, leaving the field, Settings closing, the page going to the background or being left), because browsers report unintended complete times on the way to the one wanted. The reading menu's theme select offers "On a schedule" after "Match my device"; the times are set in Settings.

**Resolution.** effective = device override, else the account default, else the built-in default. The account default of a `ui.*` device key is its settings row; the account defaults of the `client.*` keys are the hidden setting `ui.device_defaults` (an object of `client.*` keys).

**Endpoints** (session cookie, then the same-origin rule with `X-Kipple-Client`, like every other UI route):

| Method and path | Request | Response |
|---|---|---|
| `GET /api/device` | none | `{id, name, created_at, last_seen_at, profile:{overrides}, defaults:{...}, merged:{...}}` (times are unix seconds) |
| `PATCH /api/device` | `{key: value or null, ...}`, all or nothing, `null` clears an override; an empty body is `400`. Stored keys this build no longer knows are ignored on read | the same object |
| `PUT /api/device/name` | `{name}` (required; trimmed, at most 64 characters, no control characters, empty clears; otherwise `400`) | the same object |
| `GET /api/devices` | none | `{devices:[{id, name, current, user_agent, client, created_at, last_seen_at, overrides}]}`, most recently seen first; `overrides` is the number of stored override keys (an integer, not an object) |
| `POST /api/device/copy-from/{id}` | none; `id` is another device's id, or `defaults` to clear this device's overrides | the same object (`400` for itself, `404` unknown) |
| `POST /api/device/make-default` | none | the same object. This device's `ui.*` overrides are written to their settings rows and its `client.*` overrides merged into `ui.device_defaults`; the device keeps its overrides. `ui.device_defaults` accepts only known `client.*` keys, drops nulls and is capped at 8 KB; a merge that would pass the cap answers `413 too_large` and writes nothing |
| `DELETE /api/devices/{id}` | none | `204`; `409 current_device` for the caller's own, `404` unknown |

At most 50 devices: registering another evicts the least recently seen, but only one unseen for 30 days and never the registering session's own last device; if none qualifies the registration is refused (`ErrDeviceLimit`, the client gets defaults). The nightly job (`purge_devices`) deletes devices unseen for 400 days. `last_seen_at`, the user agent and the client are refreshed at most daily; the "is a day old" check runs on the reader pool and the writer is taken only when an update is due. The web client mirrors `merged` to `localStorage` as a paint cache, plus a queue of unsent profile changes (replayed after a reload) and a one-time migration flag (legacy `localStorage` preferences are uploaded once).

### 7.1d Auto-read and saved searches (`internal/store/autoread.go`, `internal/maint`, `internal/api/autoread.go`, `savedsearches.go`)

**Auto-read (F5).** Keeps the unread list, and the 10,000 ids Reeder loads, from growing without end.

- **Settings.** `library.auto_read_days` (int, 0 to 365, default 0 = off; group `library`, surface `settings`, unit `days`, label "Mark old articles as read after…"). `feeds.auto_read_days` overrides it per feed: `NULL` inherits, `0` is off for that feed, 1 to 365 is its own threshold. `PATCH /api/feeds/{id}` accepts `auto_read_days` (null, or an integer 0 to 365; anything else is `400`), and `auto_read_days` is in bootstrap feed objects and the feed detail. The archive feed is never a target; disabled feeds are (there is no `enabled` filter).
- **What is marked.** An item that is unread, not starred, not muted and not held (`retain_until` unset or in the past) and whose crawl time (its id, unix microseconds) is more than N days before the run. It is an ordinary read mark (`read = 1`, `read_at = now`): Reeder and NetNewsWire see the item as read at their next sync, the counts and the Reader unread ids follow, and the stats recorder is not involved (a bulk read is not a read). The matching rows of the trimmed ledger (`trimmed_items`) are marked read too, because the Reader API still lists ledger ids as unread; they are not items, so they never appear in events and are not in the preview count.
- **Window semantics (the nightly step).** Each item crosses its threshold once. The nightly job (`internal/maint`, step `auto_read`, after the purges and before `optimize`) marks, per feed with threshold N, the items with `id` in `(lastRun - N days, now - N days]`, where `lastRun` is `sys.auto_read_last_run`, recorded (unix seconds) at the end of every completed step, even when nothing is enabled. Consequences, all pinned by tests: (1) **a manual mark-unread sticks**: the next night's window has moved past the item, and only the explicit catch-up below can mark it again; (2) **downtime is covered**: a server that missed nights resumes from the last completed run, so everything that crossed meanwhile is marked and nothing older; (3) **enabling never reaches back**: a database with no recorded run gets an empty first window and only records the instant, and a newly enabled or shortened threshold has no effect on items that crossed before the last run; (4) a step that fails or is interrupted does not advance `lastRun`, but each feed keeps its own **high-water mark** in `sys.auto_read_feed_marks` (`{"<feed id>": unix time}`, the end of that feed's last completed window; no schema change): a feed's window starts at the later of `lastRun` and its own mark, the mark moves to `now` as the feed completes (stored right after any feed that marked something, and, best effort, when the run ends early), and a run that completes advances `lastRun` and clears the marks. So a night that fails at feed k repeats only the windows of the feeds that did not finish, and a manual mark-unread in a feed that did finish still sticks (a hard kill can lose only the marks of feeds that had nothing to mark). Windows are plain unix arithmetic (`now - N * 86400` seconds), so a DST change (a 25-hour local day) moves nothing, and no zone is involved.
- **Batches.** A feed with nothing to mark costs two reader-pool `EXISTS` probes (items, ledger) and no gate acquisition, so a night with nothing to do takes the commit gate zero times; only a feed (and its ledger) with candidates writes. 500 ids per write transaction behind the commit gate, each one a single `UPDATE ... WHERE id IN (SELECT ... LIMIT 500) RETURNING` that selects its own rows inside the transaction, so an item starred, muted or read between batches is simply not selected; a 25 ms pause between batches. After each batch the server publishes `items.state` `{ids, read:true, source:"auto_read"}` (`resync` above 500 ids) and the coalesced `counts`. The nightly log has a `maint: job done job=auto_read rows=…` line and, when anything changed, `maint: auto-read items=… ledger=… feeds=… since=…`. There is no separate dry run in the log: the preview endpoint below is the dry run.
- **Catch-up (explicit, previewed).** The nightly window never touches what crossed earlier. "Clear what is already older" is a separate, user-started action with no lower bound (`id <= now - N days`), and a dry run counts exactly what it would change:

| Method and path | Request | Response |
|---|---|---|
| `POST /api/library/auto-read/preview` | `{feed_id?, days?}`: `feed_id` limits it to one feed; `days` (0 to 365) previews "what if" without saving: it replaces the global default for feeds that inherit it (feeds with their own value keep it), or with `feed_id` that feed's value; the body may be empty | `200 {total, feeds:[{feed_id, title, days, count}] (only feeds with something to mark, biggest first), global_days, confirm_above:100}`; `404` unknown feed (the archive feed counts as unknown); `400 unknown_field` for a body key other than `feed_id`, `days` (and, for run, `confirm`, `expect_total`); `400 bad_request` for bad values (`days` outside 0 to 365, `expect_total` outside 0 to 2^31, a non-boolean `confirm`) |
| `POST /api/library/auto-read/run` | the same, plus `confirm` and `expect_total` (the total the client's preview showed; null or absent = no check) | `202 {id, kind:"auto_read", done, total, changed, new_items, errors}` and a run; `409 {error:"confirm_required", total, confirm_above:100, message}` when the preview total exceeds 100 and `confirm` is not true (nothing is marked); `409 {error:"total_changed", total, expect_total, message}` when the total recounted at run time exceeds `expect_total` by more than `max(100, 10%)` (nothing is marked; checked before `confirm`, so a client that runs feed by feed cannot slip a moved library past the user); `409 {error:"busy"}` while another catch-up runs |

  The run is one at a time and reports as `run.start` `{run_id, kind:"auto_read", total}`, `run.progress` (at most every 500 ms, with `changed`), `run.done` `{run_id, kind:"auto_read", new_items:0, errors, changed, scanned, ledger, error?}` (`error:"auto_read_failed"` when it failed), `items.state` per batch and `counts`; while active it is in bootstrap `runs` and `/api/status` `runs` as `{id, kind:"auto_read", done, total, changed, new_items, errors}`. Changing `library.auto_read_days` or a feed's value never marks anything by itself. The UI flow for a first enable: save the value, call preview, and offer "Mark N older articles read now" (with the confirm dialog above 100).

**Saved searches (F4).**

- **Setting.** `library.saved_searches`: json, default `[]`, group `library`, surface `hidden`. An array of at most 100 objects `{id, name, q, scope?, order?}` and no other keys: `id` (1 to 32 of `A-Za-z0-9_-`, unique), `name` (trimmed, 1 to 60 characters), `q` (the search text, trimmed, 1 to 300 bytes; the syntax of `GET /api/items?q=`); neither may contain a control or line-break character (any Unicode `Cc`, so a tab and U+0085 too, or U+2028 and U+2029; ordinary spaces are fine), `scope` (omitted = the whole library; else exactly one of `{feed_id:"<digits>"}`, `{folder_id:"<digits>"}` or `{view:"all"|"unread"|"starred"}`, ids canonicalized) and `order` (`date`, `oldest` or `rank`, the `GET /api/items` orders). `PATCH /api/settings` may replace the whole list (validated like the endpoints below, but a bad entry is reported as `400 invalid_settings`; a scope naming a feed or folder that does not exist is `400 invalid_settings` with the issue `scope: no such feed or folder`; `null` resets it; last write wins). A scope is checked inside the write transaction that stores it, only when it is new or changed, so a feed or folder deleted at the same moment (whose own transaction drops the scopes naming it) can never leave a dead scope behind, and an old dangling one never blocks an unrelated edit.
- **Ids are server-generated** (`s` plus 12 hex digits) by `POST /api/saved-searches`; the client never chooses one. The dedicated routes edit the list with read-modify-write inside one write transaction, so two tabs cannot lose each other's edit (the settings PATCH replaces the list and can).

| Method and path | Request | Response |
|---|---|---|
| `GET /api/saved-searches` | `?counts=0` skips the counts | `{saved_searches:[{id, name, q, scope?, order?, unread, unread_capped}]}` in display order |
| `POST /api/saved-searches` | `{name, q, scope?, order?}`, no `id`, no other keys | `201` the saved search (with its count); `400 {error:"bad_saved_search", field, message}` (a `scope` naming a feed or folder that does not exist is a `400` on `scope`); `409 too_many` at 100. `field` is `name`, `q`, `order`, `scope.feed_id`, `scope.folder_id`, `scope.view` or `scope` (not exactly one of the three keys). An unknown key in the body is `400 bad_request` |
| `PATCH /api/saved-searches/{id}` | any of `name, q, scope (null clears), order (null clears)` | `200` the saved search; `404`; `400 bad_saved_search` (fields as above; a `scope` object with unknown keys is `bad_saved_search` on `scope`); an unknown top-level key is `400 unknown_field` |
| `DELETE /api/saved-searches/{id}` | none | `204`; `404` |
| `POST /api/saved-searches/reorder` | `{ids:[...]}`, every id exactly once | `200 {saved_searches}` without counts; `400 bad_saved_search` on `ids` when it does not name every saved search exactly once; an unknown body key is `400 bad_request` |

  Every change made through the endpoints publishes `saved_searches.changed` `{}` so other tabs refetch, and so does a `PATCH /api/settings` that replaces or resets `library.saved_searches`. A feed or folder deletion that drops scopes publishes only its `feed.changed` / `folder.changed`; clients refetch saved searches on those events too. On the settings path the reported keys are `saved_searches` (the list) and `id`. Bootstrap carries `saved_searches` (the same list, no counts) and the setting value.
- **Live unread count**, computed lazily on `GET /api/saved-searches` (and on create and patch), never stored. It runs the same card search as `GET /api/items` (`store.ListCardsFB`, `view=unread`, or `view=starred` counting the unread cards, with the feed or folder scope), page by page, so it cannot disagree with the list the user opens. It stops when it has counted more than 999 (`unread:999, unread_capped:true`, shown as "999+"; exactly 999 is `unread:999, unread_capped:false`); a search whose only results are the prefix/OR fallback counts 0; each search has a 200 ms budget and the whole list 2 s; a count that ran out of budget or failed is `unread:null` (show nothing).
- **Deleting a feed or folder** (a folder merge included) removes the `scope` of every saved search that names it, in the deleting transaction; the search stays, over the whole library, rather than silently vanishing or pointing at a dead id.

### 7.2 Read/star write rules in the UI API

**Read later is starred.** Reader clients know only starred, so "read later" is a star; there is no separate state. Highlights and annotations are a non-goal.

Only two entry points can produce stats: `POST /api/items/{id}/open` and `PUT /api/items/{id}/star`. Swipe, keyboard `m`, mark-read-on-scroll, "mark above", mark-all, and folder or feed mark-all all go through `POST /api/items/mark-read`. That handler has no reference to the Recorder.

### 7.3 SSE events

The stream opens with `retry: 3000` and a `: connected` comment. Each event is `event: <type>`, `id: <seq>`, `data: <json>`. As everywhere in the API, ids inside `data` (`run_id`, `run_ids`, `feed_id`, `ids`, `new_item_ids`) are strings.

A client that recreates its `EventSource` (the watchdog reconnect) cannot set `Last-Event-ID`, so `GET /api/events?last_event_id=<seq>` is the same cursor; the header wins when both are present. The stream checks its session on every heartbeat tick and ends when the session is gone (`Sign out other sessions`, a password change, expiry), so an open stream never outlives its login.

| Event | Data |
|---|---|
| `run.start` | `{run_id, kind, total}` (kinds include `filter_apply` and `auto_read`): `run_id` is a string. A filter apply run has `kind: "filter_apply"` and also `filter_id` (§7.1b). A scheduler run with nothing to do sends `run.done` without a `run.start` |
| `fetch.done` | `{feed_id, run_ids (strings), trigger, outcome, new_items, new_item_ids (first 50, strings; muted items are left out), muted_items, updated_items, trimmed_items, error_class, error, next_fetch_at?}`: `error_class` and `error` are `""` when there was no error, and `next_fetch_at` (unix seconds) is omitted when the feed is unscheduled. `trigger` is what the web app keys its "N new articles" pill and announcement on: a scheduled fetch that a person's own refresh or a "Refresh all" run joined while it was already running reports `feed_manual` or `manual` here instead of `scheduled`, although its `fetch_log` row keeps the trigger it started with; an import run or a subscribe-time fetch that joins one changes nothing |
| `run.progress` | `{run_id (string), done, total, new_items, errors}`, at most every 500 ms (a filter apply or auto-read run adds `changed`) |
| `run.done` | `{run_id (string), kind, new_items, errors}`: `kind` is the one `run.start` sent (`manual`, `import`, `retention`, ...), so a client that missed `run.start` still knows the run. The web app announces the result of a `manual` run only ("N new articles", "No new articles", or the feeds it couldn't refresh); an `import` run's first fetches bring their items in silently, like a subscribe-time fetch, and a retention run is housekeeping (a filter apply adds `filter_id, changed, scanned`; an auto-read run adds `changed, scanned, ledger`; both add `error` when they did not finish: `auto_read_failed`, or for an apply `cancelled`, `filter_changed`, `shutdown`, `timed_out` or `apply_failed`, see §7.1b) |
| `items.state` | `{ids, read?, starred?, muted?, restored?, source: "web"\|"reeder"\|"auto_read"\|…}` (`auto_read`: the nightly step or the catch-up of §7.1d marked them read). `muted: true` (a mute was applied) tells a client to drop the items from the All view; `muted: false` (a filter was deleted with un-muting) tells it they are visible again |
| `filters.changed` | `{}`, after any filter create, patch, delete or apply, so other tabs refetch `GET /api/filters` (and `highlights` from bootstrap) |
| `saved_searches.changed` | `{}`, after any saved-search create, patch, delete or reorder, or a `PATCH /api/settings` that replaces or resets the list, so other tabs refetch `GET /api/saved-searches` (§7.1d). Not sent when a feed or folder deletion drops scopes (only `feed.changed` / `folder.changed` go out) |
| `fulltext.ready` | `{ids, source: "ingest"}`: ids (strings) whose background extraction finished, with text or an error; at most 500 per event, coalesced over about 300 ms. The UI refetches those items if it shows them |
| `counts` | `{unread_total, muted, feeds: {id: unread}}`, coalesced to at most 1 per second. The web client recomputes each folder's `unread` from these feed counts over the folder's subtree, the same number the bootstrap gives |
| `feed.changed` | `{feed_id?}`: a feed was created, edited (any notifying PATCH, including position or folder), reordered, moved by a folder delete, migrated, found gone by the scheduler, archived or purged, or deleted, from the web or the Reader API. `feed_id` (string) is absent (`{}`) when the Reader API renamed or disabled a label (`rename-tag`, `disable-tag`), where several feeds may have changed |
| `folder.changed` | `{folder_id?}`: any folder mutation, so other tabs refetch folders (the bootstrap). `folder_id` (a string) is present for a single-folder web change (create, rename or reposition, delete); it is absent when several folders or memberships changed or the folder is unknown (`{}`): `POST /api/reorder` with any change, a feed moved to another folder by `PATCH /api/feeds/{id}`, an OPML import (web or Reader API) that created folders or moved existing feeds (each moved feed also gets `feed.changed`), and the Reader API paths `rename-tag`, `disable-tag`, `subscription/edit` subscribe or edit that names a folder or moves to Uncategorized. A refused or no-op request publishes nothing |
| `heartbeat` | `{t: <unix seconds>}`, every 15 s, sent with **no `id`** so it never advances `Last-Event-ID`. It exists only so `EventSource` can see the stream is alive (a `: ping` comment is invisible to it) |
| `resync` | `{}`, on buffer overflow or failed replay |

Each tick sends a `: ping` comment and then the `heartbeat` event. A client watchdog reconnects when no event of any kind (heartbeat included) has arrived for 45 s, which catches a stream hung after a backend crash; it arms only after the first heartbeat, so older servers without one are not cut off.

The React app does not refetch lists on events, except on `resync`. For `fetch.done` with new items it keeps a per-feed pending count and the `new_item_ids` (capped) and shows an "n new" pill for the current scope, not counting ids already loaded. `GET /api/items?ids=…` stays available for catch-up but the web client does not call it. The client closes its `EventSource` on every error and reconnects itself with backoff (1 s doubling to 30 s, ±25% jitter, reset once a stream stayed up 10 s), passing the last id as `?last_event_id=`. If `EventSource` errors twice without a message, it polls `/api/status` every 2 s while a run is active, and every 60 s otherwise. After a gap it reconciles runs and counts from `/api/status` and refetches bootstrap, and it reconciles again on `visibilitychange` while runs are active.

### 7.4 Image proxy (`internal/imgproxy`)

- **Signing secret.** The account secret is cached in the API process for 1 s (one read per second instead of one per image in a list). `kipple password` rotates it from another process, and the rotation takes effect within that second.

- **URL.** `/img/{sig}/{flags}/{u}`:
  - `u = base64url(originalURL)` without padding;
  - `flags` is a decimal bitset (bit 0 = `allow_private_net`, bit 1 = `allow_insecure_tls`, bit 2 = `FlagThumb`; bits 0 and 1 are taken from the item's feed at rewrite time, only for an image on the feed's own host (`fetch.FeedHostVariant`: the host of its URL in any case or port, its subdomains and its bare/www twin), so a third-party image in its items, cards or extracted pages never gets the private-network or insecure-TLS grant; the proxy re-checks every redirect hop against the image's host the same way and sends any other hop through the guarded transport; bit 2 is set for list-card images, see Thumbnails);
  - `sig = base64url(HMAC-SHA256(account.secret, "img-v1|" + flags + "|" + originalURL))[:22]`.

  The proxy checks `sig` with `hmac.Equal` **and** requires a web session. It uses the transport variant that `flags` selects. It answers 400 to an encoded URL over 4096 bytes, to non-canonical flags (such as `04`) and to a URL with userinfo, and 503 while the account has no secret.
- **Which images.** With `imgproxy.mode`: `all` (the default: every image goes through Kipple, like other RSS readers, so the sites that host images never see the reader, hotlink rules do not matter, and the page CSP needs no `https:` in `img-src`) or `http_only` (`http://` sources only). A stored `imgproxy.mode` row always wins over the default, so a deployment that chose `http_only` keeps it. Stored URLs are always absolute (decision 31), so every image can be signed. Kipple's own `/img/` route can no longer collide with a relative `/img/foo.png`.
- **Rewrite.** At serve time only (`GET /api/items/{id}`, the open response (`POST /api/items/{id}/open`), cards, the fulltext response). Article HTML is rewritten in `internal/sanitize` (`ServeHTML`, §7.8); the card `image` and `url` are rewritten in `api.proxyCards` through `imgproxy.Rewriter` (`internal/api/image.go`). It covers `img[src]`, every `srcset` candidate, `video[poster]`, the card `image`, and `picture > source[srcset]`. Stored HTML and Reader API output are never rewritten. The rewriter leaves a URL over 4096 bytes unproxied (in `all` mode the page CSP then blocks such an image).
- **Fetch.** Through the SSRF-guarded client variant, with no cookies, at most 5 redirects and a 15 s timeout on waiting for the source: the headers get a deadline, and each body read spends from what is left of the 15 s (the time is counted only inside reads from the source, so a slow client never times out a fast source, while a source that trickles is still cut at 15 s). The first attempt sends `Accept: image/*`, Kipple's own `User-Agent` and no `Referer`. See the hotlink ladder below.
- **Size.** If the upstream `Content-Length` is over 15 MiB, the proxy returns 502 before writing anything. Otherwise it streams through a `MaxBytesReader` of 15 MiB. On overflow mid-stream it aborts the response (`panic(http.ErrAbortHandler)`), so the browser sees a broken image and nothing is buffered. Only the first 512 bytes are held for sniffing.
- **Type check.** Allowed types are `image/jpeg`, `png`, `gif`, `webp` and `avif`. SVG is refused.
  - `http.DetectContentType` covers the first four.
  - AVIF is detected by an ISO-BMFF `ftyp` box with major or compatible brand `avif`/`avis`.
  - When sniffing is inconclusive, the upstream `Content-Type` is accepted only if it is on the allowed list.
- **Response headers.** With the cache off, upstream `ETag` and `Last-Modified` are passed through and `If-None-Match`/`If-Modified-Since` are forwarded. On a cache miss only `Last-Modified` is passed through: the source's `ETag` is stored for revalidation but never sent, because every later hit answers with the cache's own checksum tag and a browser holding the source's tag would never match it (the client's conditional headers are *not* forwarded either: the body is needed to fill the cache). A cache hit answers with `ETag: "<sha256[:16]>"` and the stored `Last-Modified` and honors `Range`, `If-None-Match` and `If-Modified-Since` itself (`http.ServeContent`). Every image response has `Cache-Control: private, max-age=2592000, immutable`, `X-Content-Type-Options: nosniff` and `Content-Security-Policy: default-src 'none'`; every `/img/` response also carries `Cross-Origin-Resource-Policy: same-origin` (set by `httpx.Secure`, §7.7; a hit sets it itself as well). The one exception is an original served at a thumbnail URL (see Thumbnails): it is never immutable.
- **Concurrency.** At most 8 upstream image fetches run at once (a semaphore) and at most 4 to one host (a per-host limiter, taken first so a crowded host never holds global slots while it waits); the rest wait up to 10 s together, then get 503 `Retry-After: 5`. Cache hits take no slot. An exchange gives its slots back at the latest `SlotHold` (the 15 s timeout plus 30 s) after its body started: where the body streams straight through to the client (cache off, the disk guard, a failed cache write), a client that has not taken it by then has its response cut (the source is closed, so the next read fails) instead of holding a slot that every other image needs; a client reading a committed cache file is not affected.
- **Hotlink ladder.** When the source answers 401, 403 or 429 (never when it sends `Retry-After` or `Cf-Mitigated`), the proxy retries in this order, each once: (1) a plain desktop Chrome `User-Agent` with `Accept: image/avif,image/webp,image/apng,image/*,*/*;q=0.8` and no `Referer`; (2) the same with `Referer` set to the image's own origin (`https://host/`). Kipple's address, the article URL and any cookie are never sent, and a redirect strips `Referer`. The shape that worked is remembered per host (`hosts` table of the cache index, or in memory with the cache off) and tried first next time; if the default shape works again the hint is dropped.
- **Disk cache** (`internal/imgcache`, `imgproxy.cache_mb`, default 1024, 0 = off, range 64 to 20480). One file per image under `<data>/imgcache/v1/<k[0:2]>/<k>` with `k` = the first 32 hex of `sha256("img-v1|orig|<flags>|<url>")` (the variant is `orig`, or `t800` for a card thumbnail), half-written downloads in `tmp/`, and the index in `<data>/imgcache/index.db`: its own SQLite file (WAL, `synchronous=NORMAL`, one writer connection, `cache_size` 2 MB), never the main database, so a per-hit LRU update cannot touch the single writer that fetch commits and edit-tags queue on. It is outside `VACUUM INTO`, the nightly snapshot, the export and the migrations: **backups never include it** (the export builds its zip from the snapshot, and the snapshot and the pre-migration copies live in `<data>/backup`; a host-level volume backup should exclude `imgcache/`). A corrupt or old-schema index is renamed to `index.db.bad-<unix>` (one kept) and rebuilt empty.
  - **Miss.** One leader per key fetches through the guarded transport. A fill goroutine reads the body into a temp file at the source's speed, and the client is served from that file (a second read handle, opened with `FILE_SHARE_DELETE` on Windows so the rename still works) as it grows: a slow client (a phone on 2 Mbit/s reading a 5 MB image) never holds up the upstream read, so it can never time it out, and memory stays at two 32 KiB buffers. At EOF, if the length matches any declared `Content-Length` and the type check passed, the file is renamed into place and the row inserted (no fsync: it is a cache), and the flight and fetch slot are released at once, while the slow client is still reading. An overflow past 15 MiB, an upstream error or a truncated body aborts the temp file, cuts the client's response and is remembered as a failure (see Failures; a body that stalls until the read-time budget runs out is "timed out"); a client that goes away cancels the fill, and a body Kipple closes itself (the client left, `SlotHold`) records nothing: a partial body is never cached. If a write to the temp file fails mid-body, the rest of the body streams to the client uncached. When the cache refuses the write up front (the disk guard) the body streams straight through under the same read-time budget. Followers wait for the leader (up to `Wait` + `Timeout`), then look the key up again; a failed leader has written a failure entry, so followers get 502 and there is no stampede.
  - **Hit.** Served from disk, no upstream contact, no fetch slot.
  - **Freshness.** `fresh_until = fetched_at + clamp(Cache-Control max-age, 1 d, 30 d)`, 7 days when absent; `no-store` and `no-cache` count as 1 d (the entry is still cached and revalidated daily), and a max-age over 30 d is capped. A stale entry is revalidated with `If-None-Match`/`If-Modified-Since` under a 3 s header budget: 304 renews it and a good 200 replaces it. Anything else (a 5xx, 429, timeout, transport error, a 404/403/other 4xx including a hotlink ladder that ends in 403, a 200 that is not an image, such as a CDN's soft-404 page, or a 200 whose body runs past 15 MiB, stalls or is cut, whose response is cut) serves the stale copy, or leaves it, and keeps it; the failed revalidation is remembered on the entry itself (it counts as fresh for 10 minutes, doubling with each consecutive failure up to 24 h, reset by the next 304 or new copy), so a good copy is never replaced by a failure entry and a failing source is not asked on every request. A 304 whose stale file was evicted meanwhile (stale entries are the LRU's first victims) is followed by one unconditional fetch instead of a 502.
  - **Failures** are remembered without contacting the source again: for 24 h: any non-200 status other than 5xx, 429 and 408 (so 404, 410, 403, 400, 401, 451 and the rest), an unsupported type and too large (type refusals replay as 415, the rest as 502, all `Cache-Control: no-store`); for 10 minutes, doubling per repeat up to 24 h: 5xx, 429, 408, any transport error (timeouts, DNS, connection, too many redirects, a blocked address), an empty or unreadable body, and a body that stalls or is cut after its first bytes. At most 20,000 failure rows: past that the expired ones go first, then the least recently replayed (a replay counts as a use). In-progress markers (Thumbnails) are counted apart and never pushed out by failures: at most 5,000, the oldest go first. Each set is cut to 90% of its cap.
  - **Bound.** Each cached file counts against the cap as its size in whole 4 KiB blocks plus its URL (the index row), so a cache of tiny files cannot take many times the cap on disk, and the bytes of downloads still in `tmp/` count too until they are committed or aborted (at most one image per fetch slot). Over the cap, least-recently-used entries are evicted down to 90% of it, less the downloads in progress (`last_access_at`, buffered in memory and flushed every 60 s and before any eviction). Entries not accessed for 60 days expire. Host hints are at most 10,000 rows: past that the least recently updated go. `used_bytes` stays the files' own bytes; the health view's `db.imgcache_bytes` is the charged total plus the index. A background pass every 10 minutes and the nightly job (`imgcache_sweep`: idle expiry, expired failures, eviction, old host hints, WAL checkpoint and `VACUUM` of the index) enforce both. A lower `imgproxy.cache_mb` evicts at once (asynchronously); 0 disables the cache and purges it, and the proxy then streams every image as before. Eviction and idle expiry stop at the first index error (a full disk, an I/O error) and on a batch that frees nothing, and honor cancellation, so they can never spin holding the cache lock; the failure is logged at WARN at most once per back-off (1 minute, doubling to 1 hour, reset by a success).
  - **Disk guard.** Before each write the free space of the data volume is checked: below max(2 GiB, 5% of the volume) plus the incoming size, the image is streamed uncached, a WARN is logged at most hourly, and the cache trims itself to 50% of its cap (at most once a minute).
  - **Startup.** `tmp/` is emptied; rows whose file is missing or the wrong size are deleted; files without a row are deleted; the byte and file counters load from the index. A hit whose file has vanished drops its row and refetches. Files are 0600 and directories 0700.
  - **API** (session; the POST is also same-origin checked): `GET /api/imgcache` returns `{enabled, mode, cache_mb, max_bytes, used_bytes, entries, neg_entries, hits, misses, evictions, failures, since, oldest_access_at, disk_free_bytes, disk_floor_bytes, low_disk}` (counters are since `since`, the process start; `oldest_access_at` is null when empty), and `POST /api/imgcache/clear` deletes every image, failure record and host hint and returns `{cleared}`. The health view's `db.imgcache_bytes` is the cache's charged total (the files in whole 4 KiB blocks plus their URLs, and the downloads in progress in `tmp/`; see Bound) plus the index file, its WAL and shared-memory file: no directory walk per health call.

- **Thumbnails** (`internal/imgproxy/thumb.go`, `thumbserve.go`; backend plan step 15).
  - **URL.** `FlagThumb` (bit 2, value 4) is part of the signed flags, so the variant cannot be added to or removed from a URL without the secret. `sig` is computed exactly as before over the decimal flags, so every URL signed before the bit existed (flags 0 to 3) verifies unchanged; flags 8 and up are refused with 400. Only the card `image` (every list layout: Editorial, Magazine, Headlines and the rest) is signed with the bit, and only when the cache is on and the image is proxied. The open article's lead image, article bodies and the fulltext response keep the original.
  - **Keys.** The thumbnail is the entry `sha256("img-v1|t800|<fetch flags>|<url>")`, where the fetch flags are the URL's flags **without** the thumb bit, so the original and its thumbnail are two rows of the same index (`variant` `orig` and `t800`), share one upstream fetch, and both count toward `imgproxy.cache_mb` and are evicted by the same LRU (serving a thumbnail also refreshes its original's LRU position, so the two age together; either one may still go first, and a thumbnail whose original was evicted is still served, stale or not: a stale one whose original cannot be fetched again is served and its revalidation put off, 10 minutes doubling, as a stale original would be). `GET /api/imgcache` gains `thumbnails` (how many of `entries`).
  - **Request path.** A fresh thumbnail is served from disk like any hit (`ETag` = its own checksum, Range and conditional requests). Otherwise one leader per image (single flight on the thumbnail key): (1) makes sure the original is cached, through the ordinary fetch path run into a probe that holds its answer back from the client (single flight, host limits, hotlink ladder, negative cache all apply). When that path cannot cache the original (the disk guard refuses, or the commit fails) the probe forwards the original it is fetching to the client, so the source is contacted once, not twice; a failure it met (502, 415, or 503 when no fetch slot came free) is replayed as it is, so a request never queues twice for a slot; (2) decides from the cached type and size, with no decoding; (3) queues the transcode and waits up to 4 s. Followers wait on the leader. The worker goes on when the wait ends, so the next request finds the thumbnail. The leader releases its flight on every path, a panic included (a guarded defer), unless the job went to a worker, which then releases it.
  - **Browser caching of the answer.** Only a real thumbnail is sent `immutable`. The original served at a thumbnail URL for a passing reason (a full queue, a wait that ran out, a transcode running or left by a crash, the disk guard forwarding the original, a lookup error) is sent `Cache-Control: no-cache`, so the browser asks again and gets the thumbnail once there is one; for a refusal it is `private, max-age=` the time left on the refusal (at most 24 h). Errors keep `no-store`.
  - **In-progress marker.** Before a decode starts, the worker writes a transient failure entry on the thumbnail key with the reason `in progress` (10 minutes, doubling each time a marker was never replaced, up to 24 h; not counted in `failures`), and every outcome replaces it (the thumbnail, a refusal, a failed store). If the marker cannot be written (a full disk, a failing index), the worker does not decode at all and the waiting request serves the original (a WARN at most once a minute). An out-of-memory kill runs no defer and records nothing else, so after a crash the marker is what the next process finds: a request that sees a marker and can lead the flight (so no decode of it is running in this process) serves the original until the marker expires, instead of crash-looping on every list render. While a decode is running in this process, requests that see the marker wait on its flight as usual. Expired markers are kept two more days by the sweep so a repeat crash backs off further.
  - **Transcoder.** One pool for the whole process: 2 goroutines and a queue of 16. A job is refused at once when the queue is full. The decode-memory budget is shared (see Limits), so two jobs never decode side by side unless both fit.
  - **What is made.** Only JPEG, PNG and static WebP whose (EXIF-oriented) width is over 800 px **and** whose file is at least 150 KiB (153,600 bytes). Decoded with the standard library and `golang.org/x/image/webp` (decode only). Scaled to 800 px wide keeping the aspect ratio, never upscaled. The output is **JPEG at quality 80**, or **PNG** (`BestSpeed`) when the scaled picture has any transparency; WebP output is not offered because there is no pure-Go encoder, and AVIF cannot be decoded. Metadata (EXIF, ICC, XMP) is dropped; the EXIF orientation is applied to the pixels first so a phone photo is not turned sideways, and colors are treated as sRGB. A thumbnail that is not smaller than the original is discarded and the original served.
  - **Scaling.** Halving passes by `ApproxBiLinear` (at exactly 2x this is a box average, so no aliasing) until less than 2x remains, then one bilinear step. The kernel scalers of x/image/draw were rejected because their float64 intermediate (`dstW x srcH x 4 x 8` bytes) costs about 100 MB for a 20 MP photo against a 64 MiB `GOMEMLIMIT`. Every intermediate and the result are premultiplied `*image.RGBA` (transparency is kept; the PNG encoder takes RGBA directly), and a source without an x/image/draw fast path (CMYK, 16-bit, paletted, NYCbCrA, 4:1:1 YCbCr) is read through `RGBA64At`: any other destination or source type goes through `Set`/`At`, which allocates a boxed color per pixel (measured: 8 bytes of garbage per destination pixel).
  - **Served as the original** (and remembered): GIF (animated or not), animated WebP (`VP8X` animation flag), AVIF, sources under 150 KiB or not wider than 800 px, decode errors, images over the limits below, files whose structure the memory model cannot account for exactly (see Limits), and any job the decoder panics on (a decode that kills the process leaves its in-progress marker instead). The refusal is a negative entry on the thumbnail key for 24 h, so it costs one lookup, not another decode. A full queue and a timed-out wait are *not* remembered (they are load, not the image); a failed cache write is remembered as a transient failure (10 min, doubling). SVG is refused as before.
  - **Limits.** Pixel count is read from the header (`image.DecodeConfig`) before any decode and refused over 24 megapixels (the decompression-bomb guard). Each transcode has an estimated cost (`thumbCost`, `thumbmem.go`): an upper bound of **every byte it allocates, garbage included**, so no GC timing can push the heap past it. It is read from the file itself the way the decoders size their buffers, and a file the model cannot account for exactly is **not decoded** (never a guess, never a worst-case estimate: a model that reads a file differently from the decoder can be shown one frame while the decoder decodes another):
    - **JPEG** is walked strictly from SOI to EOI (`thumbjpeg.go`). Only a well-formed stream is accepted: every segment starts with `FF` and a known marker (legal `FF` fill bytes before a marker allowed) and its length fits the file; exactly one frame header, SOF0, SOF1 or SOF2, with 8-bit samples, a non-zero size, 1, 3 or 4 components with distinct ids, valid table selectors and sampling factors `image/jpeg` supports; at least one scan; entropy-coded data after each SOS is skipped byte by byte (the only place `FF 00` stuffing and RSTn markers may appear); DHT, DQT, DRI, APPn and COM are skipped by length; the JFIF APP0 and Adobe APP14 are read wherever they are, the last one of each winning, as in the decoder (an Adobe marker after the first scan still turns on the RGB conversion). Stray bytes or `FF 00` between segments (which `image/jpeg` skips as junk), a second frame header, SOF3/5..15, DAC, DNL, DHP, EXP, JPGn, TEM, a restart marker outside scan data, a missing scan or EOI: refused. Bytes after EOI are never read by the decoder and are ignored. The frame's size must also match `image.DecodeConfig`. In an accepted stream every byte is where the decoder looks for it, so the model prices the frame the decoder decodes. The cost: the MCU-padded Gray or YCbCr picture (4:4:4 for any non-standard sampling), plus the black plane of a 4-component file, plus 256 bytes per 8x8 block of every component for a progressive file's coefficients (4 bytes per sample), plus a 4 bytes per pixel RGBA/CMYK copy for CMYK, YCCK and RGB files. So baseline 4:2:0 is about 1.5 bytes per pixel, CMYK 8, progressive 4:4:4 15, progressive CMYK 24. The reviewer's file (SOI, `FF 00 00 06`, a comment holding a fake one-component frame and scan, then a progressive CMYK file) was priced at 37.7 MiB for 4000x2666 by the old first-scan walk while its decode allocates about 245 MiB; it is now refused, as is the variant whose comment also holds a fake EOI. A property test mutates well-formed headers (junk, `FF 00`, duplicate frames, comments holding fake frames, truncation, trailing garbage, flipped bytes, Adobe and JFIF markers anywhere) and checks that the walk either refuses or agrees with `DecodeConfig` and prices at least the real allocation; a fuzz target checks the agreement.
    - **PNG** (IHDR, tRNS): the output picture (1 byte per pixel gray or paletted, 2 for 16-bit gray, 4 for 8-bit color or 8-bit with transparency, 8 for 16-bit color or transparency), twice for Adam7 (each pass is decoded into its own picture before the merge).
    - **WebP** (`thumbwebp.go`): lossy (VP8) is 4:2:0 YCbCr padded to macroblocks plus per-macroblock state, plus the frame's size (its partitions are read whole), and the key frame header must name exactly the size `DecodeConfig` reported (for a VP8X file, the canvas) or the file is refused; a raw alpha plane is 1 byte per pixel. A lossless stream (the image, or a compressed ALPH plane, which is one too, plus the 1 byte per pixel plane copied out of it) is walked through its transforms: each predictor or cross-color sub-image and the color-indexing palette are decoded symbol by symbol (values not kept), then the main image's color cache and meta prefix codes are read. `golang.org/x/image/vp8l` keeps five Huffman trees per prefix code group and a file with meta prefix codes may have up to 2,600 groups (with an 11-bit color cache about 50 KB of trees and 25 KB of garbage each, whatever the pixel count: about 200 MiB for an 801x1000 picture with 600 KB of tree data). The group count itself is only known by decoding the entropy image, so meta prefix codes are **priced, not refused**: the entropy image is walked like the other sub-images (its own cost is counted) and, up to 65,536 tiles, decoded (literals, back-references and color cache, as `decodePix` does), so the groups are counted the way the decoder counts them: it reads every group up to the largest index (refusing an index of 2,600 or more), keeps all of them when that index is under 1,000 and under the tile count, and otherwise keeps only the used ones and drops the rest (a dropped group still leaves its code lengths as garbage, about 12 KB with an 11-bit cache). A larger entropy image is priced as 2,600 kept groups. The file is refused only when the total is over the 80 MiB ceiling below. Many groups over a large image are refused (the 801x1000 file with 2,600 groups is over the ceiling); a few groups, as libwebp writes in the alpha plane of most lossy+alpha files, are admitted. Measured on 179 real WebP files (the `x/image` test data and 162 theme images): before, 16 were refused (all libwebp lossless or lossy+alpha files with meta prefix codes); after, none. Anything the walk cannot follow exactly is still refused (a prefix code that is not complete, which is the only kind whose every bit means one thing, a repeated transform, an invalid back-reference, a short stream). The cost: 4 bytes per pixel of the (bundled) main image, the unpacked copy of a color-indexing transform that bundles pixels, every sub-image (the entropy image too), the color caches, and per kept group the worst trees for its alphabets (24 bytes per symbol: 4 for the code lengths and 4 for the canonical codes, both garbage, and up to 16 for the nodes), per dropped group 4 bytes per symbol plus about 5 KiB for its code-length code. The 2,600-group bound for a very large entropy image is deliberately loose (it may refuse a rare large file with a few real groups; the original is then served), never tight enough to under-price one; the decode-memory tests prove estimate >= real allocation with multi-group and dropped-group cases, and the walk's decoded entropy values are checked pixel for pixel against the decoder.
    - **Lossless WebP: why it is bounded before decoding, and why not from the header alone.** Everything the VP8L price uses is read from the bytes before `golang.org/x/image/vp8l` allocates anything: the size (at most 24 MP, from `DecodeConfig`), each transform (at most once) and its sub-image size (from its tile bits), the main image's color cache bits (1 to 11, anything else refused), the meta prefix code flag and tile bits, and the entropy image. The fixed header fields alone are not enough: the number of prefix code groups lives in the entropy image's pixels, and a price from the header alone would have to assume the decoder's maximum of 2,600 groups, about 84 MiB of trees and garbage without a color cache and 226 MiB with an 11-bit one (decode alone, before the 5/4 and the 3 MiB), both over the 80 MiB ceiling. That rule would refuse every file with meta prefix codes, which is nearly every libwebp lossless file and the alpha plane of most lossy+alpha files. So the walk reads one step further and decodes the entropy image, which is small by construction: libwebp raises its tile bits until the entropy image has at most 2,600 tiles (`MAX_HUFF_IMAGE_SIZE`, the same 2,600 the decoder's group limit comes from), so the exact path (up to 65,536 tiles) covers every libwebp file, and the 2,600-group price applies only to a larger entropy image from another encoder (refused, since that price is over the ceiling). The walk's own memory is bounded and small: a 4 KiB reader, at most a few KiB per prefix code, 256 KiB of entropy values and an 8 KiB color cache, inside the fixed 3 MiB; its time is bounded by the sub-images' tile counts (at most `w*h/16`), not by the file. Checked against the lossless and lossy+alpha files libwebp wrote for `golang.org/x/image`'s own test data (read from the module cache by the tests, not committed): all 11 are accepted and priced at 1.04x to 19x their real decode (the 19x one is a 75x100 file whose price is 1.3 MiB), every truncation inside the image chunk is refused, and random byte changes never panic the walk and, when the changed file is still accepted and decodes, stay within the price. Headers that promise more than the file holds (16384x16384, 24 MP with a palette, 24 MP with 4x4 meta tiles, a truncated sub-image, a 12-bit cache) are refused having allocated under 2 MiB. The largest admitted lossless pictures (3:2) are 3760x2506 with every transform, an 11-bit cache and full trees (estimate 79.5 MiB, 58.3 MiB allocated) and 3984x2656 with 40 groups and an 11-bit cache (79.9 MiB, 59.3 MiB).
    - Plus the halving pictures (4 bytes per pixel of each half), the scaled picture, its oriented copy and the encoder's buffer, all times 5/4, plus 3 MiB of decoder state.

    The estimate must fit an **80 MiB per-transcode ceiling** and is taken from a shared **96 MiB budget** (so two large transcodes never run side by side and the transient heap stays under 100 MiB with `GOMEMLIMIT=64MiB` in the 256 MB container); over the ceiling the original is served and the refusal remembered. The tests build worst-case files of every shape (flat-block JPEGs up to 24 MP in each sampling and color model, with restart intervals and with the Adobe marker after the scan; 16-bit interlaced PNGs; lossless WebPs with every transform, an 11-bit color cache and full trees; lossy WebP alone and with a raw or a lossless alpha plane) and check the real allocation (`runtime.MemStats.TotalAlloc`) against the estimate: it is 1.27x to 1.46x the allocation for every shape (a 4000x3000 baseline 4:4:4 file with the Adobe marker after its scan allocates 96.4 MiB against an estimate of 127.9 MiB; the old first-scan walk estimated 70.7). Measured examples: a 24 MP baseline CMYK JPEG allocates 214 MiB (estimate 274, refused; the old 3 bytes per pixel estimate admitted it at 72 MiB, and the OOM kill then crash-looped); the largest admitted progressive 4:4:4 JPEG is 2352x1568 (3.7 MP, 58 MiB allocated), progressive CMYK 1872x1248 (2.3 MP, 58 MiB), baseline CMYK 3136x2090 (6.6 MP, 58 MiB), 16-bit RGBA Adam7 PNG 2272x1514 (3.4 MP, 59 MiB); a 20 MP baseline 4:2:0 JPEG is admitted (estimate 75 MiB, 55 MiB allocated). Measured on a laptop (photographic 4:2:0 JPEG at quality 88): 1280x720 (291 KB) to 60 KB in 24 ms; 2048x1365 (954 KB) to 51 KB in 55 ms; 4000x3000 (3.4 MB) to 54 KB in 0.2 s and 33 MiB; 6000x4000 (6.7 MB) to 62 KB in 0.37 s and 65 MiB.
  - **Freshness.** A thumbnail is fresh while its original is (at least an hour). When it goes stale, the original is revalidated first; if its checksum is still the one recorded in the thumbnail's `etag` column (`src=<sha256[:16]>`), the thumbnail is renewed without a new transcode.
  - **Without the cache** (`imgproxy.cache_mb` 0) a thumbnail URL streams the original. There is no `srcset`: one width serves all cards. There is no separate setting: thumbnails follow the cache, and the size cap is the lever.

### 7.5 Full-text rules

- **Effective mode** = `COALESCE(items.fulltext_mode, CASE WHEN <fetch.fulltext_all> THEN 1 ELSE feeds.fulltext END)`. The setting `fetch.fulltext_all` ("Fetch the full article for every feed", bool, default false, group library, Settings screen) needs no schema: while it is on, a feed's own `fulltext = 0` does not opt out (the label says "every feed"), but an item-level `fulltext_mode` of 0 or 1 still wins. Flipping it rewrites nothing and backfills nothing: it applies to new fetches and to on-demand extraction, and to every read path, from the next request, with no restart (the value is cached in `store.DB` and dropped when `PATCH /api/settings` writes it; the load runs under a detached 5 s context and a failed load is never cached, so one cancelled request cannot pin the default; `StreamItems` reads it once per request so its hold and its content mode agree). Old items with no extraction simply extract on demand when opened. ONE helper computes it: `store.EffectiveFulltext(itemMode, feed, all)` in Go and `store.FulltextModeSQL(itemCol, feedExpr, all)` in SQL. Its call sites: the ingest pick and queueing (`store.FeedFulltextNow`: the CURRENT switch and feed flag, evaluated after the download and again after the commit, because `Snapshot.Fulltext` from `feedSnapshots` can be stale by then; the worker also re-checks the item before extracting), the guarded save (`SaveFulltextIfURL`), `GetFulltextItem` (endpoint `effective`), `GetItem` (`fulltext.effective`, content choice, stub `effective`), `store.HeldSQL` (Reader API hold, unread counts, mark-all-as-read) and `StreamItems` (Reader API content), and `UIFeeds` (bootstrap `fulltext_effective`). `bootstrap.feeds[].fulltext` stays the feed's own stored flag, which the feed dialog edits.
- **Ingest.** For full-text feeds, new items are extracted after the commit in a bounded background pool, as in §4.3. Until an item's extraction finishes it has no row and the UI shows the feed content; `fulltext.ready` tells it to swap in the text. Reader API clients are not shown the item at all until its extraction has finished (result or stored error) or 30 s have passed since its crawl time (the full-text hold, §6.5; the window is configurable up to 60 s), and only while the item is in the pool's in-memory pending set: an item the pool did not accept is shown at once with the feed content. So they normally fetch the text on first sight; open question 55 keeps a deploy-time check with Reeder.
- **Shared runs.** The endpoint extracts through the same runner as the pool (§4.3): it joins a run already in flight for the item, honors the per-host limit of 2, and the save happens inside the run, so every joined request reads the stored result. The endpoint's run is detached from the request and has a 15 s budget from when the host slot is free.
- **UI.** When the effective mode is 1 and `available=false`, the UI calls `POST /api/items/{id}/fulltext` with an empty body, which triggers extraction without changing the mode.
- **Per-article toggle.** In the article view the toggle sets `mode` to 1 or 0; "reset" sets `null`.
- **Failures.** A failed extraction is stored with `error`, its class (`error_class`) and the attempt time (`extracted_at`). The UI shows the feed content with a retry action, and the Reader API falls back to the feed content.
- **Retry rule.** A stored failure is reported as is and not refetched when the article is opened, with two exceptions. (1) If the error is **transient** (timeout, reset or refused connection, DNS failure other than not-found, HTTP 5xx, 429 or 408) and the last attempt was **more than 1 hour ago**, the endpoint tries again itself; a new failure restarts the hour, a success replaces the error. (2) `?refresh=1` always retries. **Permanent** errors (HTTP 404, 410, 403 and other 4xx except 408 and 429, not HTML, no readable content, too large, no article URL, a blocked address (SSRF guard), a certificate that does not verify, a host that does not exist, a redirect loop; transport errors are classed like `fetch.Classify`) and errors stored without a class (before migration 0003) stay sticky until `refresh=1`. Ingest and the endpoint classify the same way. The hour bounds the load a flaky host sees: at most one attempt per item per hour, and only when someone opens it.
- **"Open original"** is a menu action that opens the article URL through `openExternal`: the same tab on iPhone and iPad (the default there), a new tab with `noopener,noreferrer` elsewhere, overridable by the device setting "Open links in". It records an `open_original` stats event (§8; `openOriginalAndRecord` in `web/src/lib/statsSender.ts`).

### 7.6 OPML import and export (`internal/opml`)

**Import** (the UI, `subscription/import` and the `kipple import [-mark-read-older-than-days N] [-move-existing] <file|->` command share the parser):

1. Every named outline without an `xmlUrl` is a folder, pure containers and empty folders included, nested as in the file: `opml.Parse` returns each folder as its chain of names from the top (`Doc.Folders`, parents before children, document order) and each feed with its folder's chain. An unnamed wrapper outline is transparent. A `/` inside a name is a character, not a level. Outlines nested deeper than 8 levels (`store.MaxFolderDepth`) are not folders: what they hold goes into their level-8 ancestor, and the topmost such outline of each branch is reported in `folders_refused` with its path (what is below it is covered by it, as for a refused folder).
2. Each chain goes through the folder writer (`store.EnsureFolderChainFrom`), which walks down from the nearest level already resolved: matched level by level ignoring ASCII case on the sibling index (`idx_folders_sibling_name`, never the `folder_paths` view, so each lookup reads a few index rows and 5,000 folders import in about two seconds), the missing levels created at the end of the folder order. A folder with subfolders in the file is resolved through its first subfolder, top-down, so a run of levels can match one existing folder: a file's `Music` > `AC` > `DC` lands in an existing `Music` > `AC/DC` (full paths are unique), with no empty `AC` created, and is reported in `folders_merged_path` with both chains of names. A folder the writer refuses (a name over 100 characters or with a control character, a subfolder of Uncategorized) is not created and is reported once, at its top, in `folders_refused` with the reason; its feeds and its subfolders' feeds go into the nearest folder above it that was created, or Uncategorized. No feed is lost to a folder.
3. Assign `folders.position` and `feeds.position` from document order.
4. Match every `xmlUrl` through `FindFeedByURL`. Matches are reported as `feeds_existing` and left in their folder. With the option `move_existing` (`POST /api/opml ?move_existing=true`, `kipple import -move-existing`; default off, never on through `subscription/import`) a match in another folder moves into the file's folder and is reported in `feeds_moved`, unless that folder was not kept (`folders_refused`): moving it to an ancestor is not what the file says, so it stays; a folder the moves leave with no feed and no subfolder is listed in `folders_emptied` and kept (deleting it would delete its filters).
5. A feed listed in several folders keeps the first. The others are reported in `memberships_dropped`, by path.
6. Sibling folder names that differ only by ASCII case (SQLite's `NOCASE`, so `É` and `é` are two folders) are one folder (the first spelling), reported by path in `folders_merged_case`. The same name under different parents is a different folder: `Tech/News` and `Sports/News` stay apart.
7. For a new feed, a non-empty `title`/`text` goes into `custom_title`, and `kipple:*` attributes are applied (validated like PATCH; `kipple:enabled=0` imports as `disabled_reason='user'`). Only the real `kipple` namespace counts. `kipple:allow_private_net` and `kipple:allow_insecure_tls` are never applied on import; they are listed in the result as `ignored_attrs`. A non-http(s) `htmlUrl` is dropped. An `xmlUrl` must pass the URL syntax check (absolute http(s), no user name or password; otherwise `skipped`), but a literal private or loopback address is imported, with `allow_private_net` off like every imported feed, and listed in `ignored_attrs` as a private address to enable: the dial-time guard blocks it until the user turns the exception on, and a self-export or migration does not lose LAN feeds.
8. The UI option `mark_read_older_than_days=N` (1 to 365, otherwise 400 `bad_days`) sets `initial_read_before = now − N·86400` on every new feed.
9. The result also carries `folders_created`, `feeds_added`, `skipped` (outlines that could not be imported, with the reason: a URL that fails `store.ValidateFeedURL`, the check every other path uses) and `invalid_attrs`; `POST /api/opml` adds a `run_id`.
10. Values are never split on commas, and the `category` attribute is ignored.

**Export:**

- `<opml version="2.0" xmlns:kipple="https://kipple.invalid/opml/1">`, with the folder tree as nested `<outline text title>` folder outlines, each named by its own name (not its path), in `folder_paths.sort_key` order (pre-order, siblings by position, then name). Inside a folder its feeds come first, then its subfolders. The default folder is written only while it has feeds. One query reads folders and feeds, so the document is one snapshot.
- Each feed is `<outline type="rss" text title xmlUrl htmlUrl>`, where `text` and `title` = `COALESCE(custom_title, title)`.
- Optional attributes, written only when not at the default: `kipple:interval`, `kipple:retention`, `kipple:fulltext`, `kipple:dedup`, `kipple:user_agent`, `kipple:ignore_http_cache`, `kipple:disable_http2`, `kipple:allow_insecure_tls`, `kipple:allow_private_net`, `kipple:enabled`.
- Disabled and gone feeds are included; the archive feed is excluded. `http_auth` is never exported.
- `xml.EscapeText` escapes `& < > " '` and tab, LF and CR as character references; `;` and `,` are left as is.

A golden test checks that import → export → import is a fixed point for the tree, names, titles, order and overrides (`testdata/synthetic.opml` and the nested `testdata/nested.opml`, with a literal `AC/DC` folder and `News` under two parents). `FuzzParse` checks the parser's invariants and `FuzzImportExport` that no feed with a valid URL is lost, the stored tree stays within 8 levels and the export is a fixed point.

### 7.7 Security headers (`internal/httpx`)

One middleware, `httpx.Secure`, wraps the root handler in `cmd/kipple` (outside `greader.Front`), so every response
carries the headers, the Reader API's included. It replaces the two `noFraming` copies. The content-dependent headers
are chosen when the response starts, from its `Content-Type`, so there is no route list to keep in step:

| Response | `Content-Security-Policy` |
|---|---|
| `text/html` (SPA, `/_status`) | `default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:[ https:]; font-src 'self'; connect-src 'self'; media-src 'self' https:; frame-src https://www.youtube-nocookie.com https://player.vimeo.com; manifest-src 'self'; worker-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'[; upgrade-insecure-requests]` |
| JSON, other text, XML, SVG, no type | `default-src 'none'; frame-ancestors 'none'` |
| scripts, styles, fonts, images, other | `frame-ancestors 'none'` (a stricter `default-src` would only bind a worker that inherits it) |
| a handler that sets its own (`/img/`, feed icons) | kept as the handler set it |
| 304 | no policy header, so a revalidation cannot replace the stored page policy |

- `https:` is added to `img-src` only while `imgproxy.mode` is `http_only` (https images then load from their host). With the default `all`, `img-src` is `'self' data: blob:`.
  The mode is read from an atomic cache (`api.Server.ImgMode`) that `PATCH /api/settings` refreshes.
- `upgrade-insecure-requests` and `Strict-Transport-Security: max-age=31536000` are sent only when the effective scheme
  is https (TLS, or a trusted proxy's `X-Forwarded-Proto`). Over plain http (a LAN visit) the upgrade would break the page.
- All responses: `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`. HTML also
  gets `Permissions-Policy` (camera, microphone, geolocation and others off; `autoplay`, `fullscreen` and
  `picture-in-picture` allowed only for self and the two embed origins, `https://www.youtube-nocookie.com` and
  `https://player.vimeo.com`) and `Cross-Origin-Opener-Policy: same-origin`. `/api/*` and `/img/*` responses (every one, not only cache hits) get `Cross-Origin-Resource-Policy: same-origin`.
- `script-src 'self'` is the control that matters: no inline script anywhere. `/_status` loads its script from
  `/_status.js`; the SPA's theme boot script is an emitted file under `/assets/`. `style-src 'unsafe-inline'` stays for
  Radix, shadcn and `react-remove-scroll`, which inject styles.
- The frontend must therefore: ship no inline `<script>`, no `eval`/`new Function`, no inline event handlers (`onclick=`);
  keep fonts as files (no `data:` font URLs); use `fetch`/`EventSource` against its own origin only; create
  embed iframes only for the two hosts above; set `referrerpolicy="strict-origin-when-cross-origin"` on those iframes.

### 7.8 Serve-time HTML transform (`sanitize.ServeHTML`)

One tokenizer pass over stored article HTML for the detail, open and fulltext responses. Stored HTML, the Reader API and
item ids never see it; it is idempotent (its output passes through unchanged). Untouched markup is emitted byte for byte.

| Element | Transform |
|---|---|
| `a[href]`, `area[href]` http(s) | `target="_blank" rel="noopener noreferrer"`; with `links.strip_tracking` (default on) the parameters `utm_*`, `fbclid`, `gclid`, `dclid`, `yclid`, `mc_cid`, `mc_eid`, `igshid`, `_hsenc`, `_hsmi`, `mkt_tok`, `oly_anon_id`, `oly_enc_id`, `vero_id`, `ref_src` are removed, the rest keep their order and spelling. The card and detail `url` are stripped the same way. `items.url` and uids are never touched. The client's DOMPurify pass drops `target` on devices set to open links in the same tab (the default on Apple touch devices) and keeps `rel` |
| `a[href]`, `area[href]` other | `mailto:` and `tel:` stay; any other scheme (`javascript:`, `data:`, relative) loses its `href` |
| `[id]`, `[name]`, `a[href="#x"]`, id references | prefixed `kp-` / `#kp-x` (no DOM clobbering; footnotes keep working). The references to ids follow: `headers`, `for`, `aria-describedby`, `aria-labelledby` and `aria-controls` (space-separated, each token prefixed) and `usemap="#name"`. The client handles clicks on `a[href^="#kp-"]` by scrolling inside the article container |
| YouTube `iframe` | `<figure class="kp-embed" data-provider="youtube" data-id="ID"><img src="/img/… of i.ytimg.com/vi/ID/hqdefault.jpg" alt=""><a href="https://www.youtube.com/watch?v=ID" target="_blank" rel="noopener noreferrer">Watch on YouTube</a></figure>`. The thumbnail is always proxied, even in `http_only` mode, so nothing contacts YouTube before the tap. Two player parameters survive, only when they match `[A-Za-z0-9_-]{1,64}` and `[0-9]{1,6}`: `list` as `data-list` (the watch link gains `&list=`) and `start` as `data-start` (the link gains `&t=Ns`). A playlist player (`/embed/videoseries?list=L`) keeps `data-id="videoseries"`, gets no thumbnail and links to `https://www.youtube.com/playlist?list=L`; without a valid `list` it is removed. On tap the client re-checks every value and inserts an iframe at `https://www.youtube-nocookie.com/embed/ID?autoplay=1` (plus `&list=`, `&start=`) with `sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"`, `allow="autoplay; fullscreen; picture-in-picture"` (the page's `Permissions-Policy` grants `autoplay` to the page and the two embed origins, §7.7, so `?autoplay=1` can start playback after the tap) and `referrerpolicy="strict-origin-when-cross-origin"` (YouTube refuses embeds without a Referer, player error 153; this sends the origin only, only on the tap) |
| Vimeo `iframe` | the same figure with `data-provider="vimeo"`, no thumbnail, link `https://vimeo.com/ID`; an unlisted video's `h` hash (`[0-9a-f]{1,32}`) is kept as `data-h` and the link becomes `https://vimeo.com/ID/H`; on tap `https://player.vimeo.com/video/ID?dnt=1&autoplay=1` (with `h=H&` first when present) with the same sandbox and referrer policy |
| stored iframe `sandbox` | the stored iframe (Reader clients) carries the four-token sandbox of §4.4 step 2a; ServeHTML replaces the whole iframe with the placeholder, so the app never renders it. The tap-to-load iframe uses the same tokens |
| other `iframe` | removed with its content (new items never have one: the ingest pre-pass turned it into a link) |
| `video`, `audio` | `preload="none"`, `controls` added, `autoplay` removed. An `http://` `src` replaces the element with `<p><a>Open video</a></p>` (also when the element is never closed: the link is written at the end of the input); `http://` `<source>` children are dropped, and if none is left the same link follows the element (mixed content otherwise) |
| images | as §7.4 (`img[src]`, `srcset`, `video[poster]`, `picture > source`) |
| defense in depth | `script`, `style`, `template`, `object`, `applet`, `frameset` (with content), `embed`, `base`, `meta`, `link`, `frame`, `param`, and the attributes `on*`, `style`, `srcdoc`, `action`, `formaction` are dropped even though bluemonday already removed them at ingest |

`links.strip_tracking` is an account setting (group reading, Settings screen): "Remove tracking from links".

### 7.9 Installable app and offline reading (phase 3)

- **Manifest and icons.** `web/public` holds `manifest.webmanifest` (standalone, scope `/`), a maskable icon and the favicons, generated by `web/scripts/gen-icons.mjs` and committed. `index.html` links the manifest with `crossorigin="use-credentials"`, so it is fetched with the session cookie when the site sits behind an access proxy.
- **Service worker** (`web/sw/sw.js`, filled in and written to `dist/sw.js` by the `kipple-sw` Vite plugin): precaches the shell (index, hashed JS and CSS, icons, all or nothing; fonts and images are kept as they are used); navigations go network first with the cached shell as the fallback (network down, 5 s of silence or a 5xx), preferring this build's own shell; `/assets/*` are cache first; the manifest and icons are network first (their names never change); a failed install deletes its half-filled shell; `bootstrap`, `items` and `items/{id}` GETs are network first and, when the network fails or is slower than 5 s, answered from the last good copy with `X-Kipple-Cache: 1` (a slow answer still refreshes the copy, and with no copy the worker waits for it; searches being typed are never kept); a list fetched with `include=content` is also stored as one entry per item, so each article opens offline; `/img/*` and feed icons are cache first (400 entries); writes and every other path go to the network. The cache keeps the previous build's shell so a page from the old build can still load its lazy chunks. The worker skips waiting and claims its clients; the page shows a "newer version" banner rather than reloading by itself.
- **Queue** (`web/src/lib/offline.ts`): with no network, star or unstar and mark-read by id keep their optimistic state and go into an IndexedDB queue (memory when it is unavailable). A later star of the same article replaces an earlier one. The queue is sent in order at launch, when the browser comes online, when a request gets through after a failure, when the tab becomes visible and every minute while it is not empty; a star carries `at`. A change made with the network up first removes the queued changes to the same articles, so a stale one is never replayed over it. A change the server refuses for good (any 4xx except 401 and 429) is dropped, the lists reload and a toast says so; 401, 429, 5xx and network failures keep it. Opening an article offline queues a read (it records no `open` statistic). The backend is chosen once (IndexedDB if a probe of it works, else memory) and never swapped mid-session. Only an explicit sign-out clears the queue and the worker's API and image copies (the page deletes the caches itself); an expired session keeps the queue for the next sign-in. Nothing else works offline: subscribing, settings, filters, scope marks, refresh.
- **Requests while offline** (`makeQueryClient` in `web/src/App.tsx`): every TanStack Query read and write uses network mode `always`, so it is sent even while the browser says it is offline, and the worker answers what it kept; the default mode (`online`) would hold it paused before it reached the worker, a screen with no end and a change never queued. A read nothing can answer fails at once (no retries while the browser is offline) and shows its screen's error with Try again; failed screens load again by themselves when the browser comes back online (`refetchOnReconnect` stays on, and Query's online state starts from `navigator.onLine`). Writes keep the rules above: star and read are queued, the rest fail with their usual error.
- **Prefetch:** once every 15 minutes while online (never with data-saver on) the app asks for the first page of Unread, in the device's sort order, with `include=content`, which the worker keeps. It is a background request: a copy the worker substitutes for a slow answer does not make the app say it is offline.
- **Notice:** a line above the app says when it is offline, how many changes wait, and when a newer build is ready.

---

## 8. Stats model and write-path rules

**Table.** `stats_events` (§2.2).

- Append-only and never trimmed. Retention, cascades, archive moves and unsubscribe never touch it, and it has no FKs.
- Feed title (custom title, else stored title, else the feed URL, as in the rest of the app), folder, item title and URL are snapshotted.
- `local_date`, `local_hour` and `local_weekday` are computed at write time in the zone of the `tz` setting, `store.Zone` (default `UTC` for a new install, `America/New_York` kept for installs from before 0.5; an unknown zone falls back to UTC; `time/tzdata` is embedded).

**Kinds.** The CHECK constraint is the whitelist. There is no `read`, `mark_read` or `unread` kind.

| Kind | Written by | Value |
|---|---|---|
| `open` | Web `POST /api/items/{id}/open`. API: none yet; the single-id inference of rule 3 is designed but not implemented | — |
| `read_time` | Web only (sent since 0.3.0-alpha.4), via `/api/stats/events` with the `session_key` of an open and a per-event `event_id` (§8 rule 4) | seconds |
| `scroll` | Web only (sent since 0.3.0-alpha.4), one per open (views take `MAX` per `session_key`) | 0–100 |
| `star`, `unstar` | Web star endpoint; API `edit-tag a/r=starred`, one row per changed id (restores included) | — |
| `open_original` | Web only (sent since 0.3.0-alpha.4) | — |
| `share` | Web only (sent since 0.3.0-alpha.4), when `navigator.share` resolves or the clipboard fallback succeeds; not on `AbortError` | — |

**Switch.** `stats.enabled` (bool, default `true`; Settings screen, Statistics group (`stats`), "Reading statistics") turns recording off: `Recorder.Record` returns `ErrDropped` and `RecordStars` records nothing for every kind (open, read_time, scroll, star, unstar, open_original, share), so no new row is written from the web or the Reader API. The setting is read inside the caller's write transaction through the typed settings read (a failed read fails the transaction, it is never taken for on or off). Nothing else changes: the open handler still marks the item read and returns a `session_key`, the ingest handler still answers `204`, and star edits from the web and from Reader clients still succeed. Existing rows are kept, and turning it back on resumes recording (a session opened while it was off has no `open` row, so its `read_time` and `scroll` events drop). `stats.week_start` (`sunday` or `monday`, default `sunday`; "First day of the week") changes only presentation: the summary uses it for the start of the `week` range and the stats views lay out weeks with it.

Server-side ingest and validation of `read_time`, `scroll`, `open_original` and `share` are implemented, and the web client sends all four (0.3.0-alpha.4); rule 5 is the contract it follows. The ingest handler reads `stats.enabled` and the time zone once per request (`Recorder.RecordMany`): with statistics off it reads that one setting through the read pool and answers `204` before parsing the events or opening a write transaction (`RecordMany` repeats the check inside the transaction as the backstop).

**Enforcement is by construction and tests:**

1. `store.SetRead(ctx, tx, ids, read, now)`, `store.MarkScopeRead(ctx, tx, scope, filter, maxID, now)` and `store.MarkAllRead(ctx, tx, scope, maxID, now)` take no client argument and cannot write stats (`store.UnreadLedger` is the fourth). Every read-state write, including auto-read (`store/autoread.go`), retroactive mutes (`store/filterretro.go`) and the fetch commit's initial-read and mute marking, takes no Recorder and writes no stats.
2. `stats.Recorder` is an interface with three methods: `Record(tx, ev)`, `RecordMany(tx, evs)` (the ingest handler's batch entry point; same validation as `Record`) and `RecordStars(tx, kind, client, ids)` (one star or unstar row per changed id, used by greader `edit-tag`). The Recorder is held by `api.Server` (`s.rec`) and by the greader API (`opt.Stats`). Only these handlers call it:
   - the web open handler;
   - the web star handler;
   - the stats ingest handler;
   - the greader `edit-tag` handler (stars only).

   The scheduler, maint and the store never receive it. That `api.markRead` and the greader `mark-all-as-read` handler do not call it is enforced by the §10 tests (`TestMarkReadNeverTouchesRecorder`), not by the types.
3. **API inference (not implemented).** No API read ever becomes an `open` (the setting once reserved for this was removed in 0.6.0). The design, were it built behind a setting: an `edit-tag` request produces an `open` with `client=<family>` and `inferred=1` only when all of the following hold:
   - it carries exactly **one** parseable id;
   - it has `a=user/-/state/com.google/read`;
   - `RETURNING` shows the row flipped from 0 to 1.

   The setting is turned on only after the day-1 `KIPPLE_LOG_GREADER_FORMS` capture shows Reeder's scroll marks arrive distinguishably from article opens (for example, batched versus single). Even then the rows stay `inferred=1`. Requests with two or more ids, `r=read`, and every `mark-all-as-read` never write anything.
4. **Ingest validation** (`POST /api/stats/events`) runs **inside one `WithWrite` transaction** together with the inserts, so overlapping flushes (the 15 s timer plus a `sendBeacon`) serialize on `BEGIN IMMEDIATE` and cannot both pass a cap:
   - `read_time` and `scroll` require a `session_key` that belongs to an `open` of the same item within the last 12 h.
   - Any event may carry an `event_id` (string, 8 to 64 characters of `[A-Za-z0-9_-]`, generated by the sender per event). An event whose `event_id` already exists, of any kind and from any session, is a duplicate (a retried flush, a repeated beacon, or the same id twice in one batch): it is dropped silently, before the cumulative cap is evaluated, so it never consumes the cap. The partial unique index `idx_stats_event` (migration 0008) is the backstop, and the insert skips a conflicting row instead of failing, so an `event_id` conflict never produces a 5xx (a genuine database error still does; see the last bullet). An `event_id` that is absent, not a string, too short or too long, or has other characters is treated as absent (the event is recorded as before, with `event_id` NULL); it never fails the body.
   - Each `read_time` value must be 1–60. The cumulative `SUM(value)` per `session_key`, including the events accepted earlier in this batch, must stay ≤ the seconds elapsed since that open + 5, and ≤ 3600.
   - One `scroll` per `session_key` is kept: a later one replaces the value only if it is larger. `scroll` is clamped to 0–100.
   - `open_original` and `share` require the item to exist in `items` or the ledger (`trimmed_items`).
   - Only `read_time`, `scroll`, `open_original` and `share` are accepted; values are truncated to integers; the body's `client` (`web` or `pwa`) sets the attribution, since a `sendBeacon` flush cannot send `X-Kipple-Client` (anything else falls back to that header, then `web`); `client` must be one of `web`, `pwa`, `reeder`, `netnewswire`, `unread`, `api`.
   - The body is capped at 64 KiB and a batch at 200 events (the rest are dropped).
   - The envelope is decoded first and each event is then decoded on its own, so one malformed event (a mistyped field, an out-of-range number) is dropped without affecting the others in the batch; a body that is not a JSON object is dropped whole. Invalid events and events that fail validation are dropped with `204`. A genuine database error (not an `event_id` conflict) answers `500`, which the sender treats as retryable (the retry is safe because of the `event_id`).
5. **Active reading time (client contract, implemented by the web client since 0.3.0-alpha.4).**
   - The timer runs only while `document.visibilityState === 'visible'` **and** `document.hasFocus()` **and** the article route is active.
   - A 2 minute idle cutoff applies: activity is a pointer, touch or key event inside the article pane, a scroll of the article, or a focus or visibility change. List activity never counts, and list scrolling never counts. Scroll events in the first 500 ms of a session are ignored (the programmatic scroll to the top when the article changes). No session runs unless both the article pane and its scroller are on screen (not on the error screen).
   - When the tab becomes visible again the timer re-checks focus after 1 s and 3 s, since some platforms report focus late; the behavior of `document.hasFocus()` in an installed iOS web app has to be verified on a device.
   - It flushes every 15 s; `visibilitychange` (to hidden) is the primary flush trigger and `pagehide` the backup, both via `sendBeacon`. It also flushes on route change. While the access proxy's sign-in has expired, events are queued instead of beaconed (a swallowed beacon would lose them); an opaque redirect, 429 or 5xx queues a batch, and 401 or any other refusal drops it.
   - Each `read_time` event carries at most 60 s, and every event carries its own `event_id`.
   - One `scroll` value is sent per session, measured only from real scroll events or from a settled check that the article fits on screen.
   - Events that cannot be sent are kept in an offline queue (one key per batch; entries older than 12 h are dropped for `read_time` and `scroll`, and 24 h for the other kinds) that is wiped on sign-out. While signed out (or after the wipe until the next sign-in) new events are dropped, not queued. Losses are accepted: the queue is best-effort, and nothing is retried forever.

**Not recorded:**

- any read-state change on its own (single, bulk, scroll, keyboard, mark-all, and every API read change, since inference is not built);
- restores other than a star;
- fetches, trims and content updates;
- Reader API list and contents syncs;
- search queries;
- health-page views;
- prefetch via `GET /api/items/{id}`.

**Stats summary (`GET /api/stats/summary`, `internal/store/statssummary.go`, `internal/api/statssummary.go`).** Authed, read-only, reader pool only: short queries, no write transaction, and nothing is held while the response is written. The opens, read-time, scroll, star and streak queries run concurrently, but at most three at a time (a small semaphore; the first error cancels the rest and fails the request), and the API admits one summary computation at a time process-wide (a waiting request leaves without querying when its context is cancelled), so the summary never holds more than three of the four reader connections and the UI's other reads are not starved. Every row loop checks its error, so a failure midway fails the request instead of returning partial numbers. All queries are bounded by the newest `stats_events` id captured when the summary starts (`rowid <= maxid`), so ingest landing between two of its queries cannot split a session (an open seen while its read time is not, which would show as a bounce); rows written after that instant appear in the next summary. The one exception is scroll depth: a later, larger scroll updates the session's first scroll row in place (its id is within the bound), so scroll values are read live and a read can flip from bounce to read between two summaries. Every view takes `include_inferred` (**default 0**): opens and stars with `inferred = 1` are left out unless it is `1`. Read time and scroll rows are never inferred.

- **Range.** Computed in the `tz` setting (the local date of now; date arithmetic is on dates, so a DST change never adds or drops a day). `week` runs from the current week's first day (`stats.week_start`, `sunday` or `monday`) through today; `month` is the last 30 days and `year` the last 365, both including today; `all` runs from `first_event_date` (the smallest `local_date` of any row, not the date of the lowest id, which a time zone change can leave later; today when there are none) through today: `range.to` is always the server's local today (the web reads it as "today"), never a later date. Rows dated after today (written under another time zone or by a clock that moved back) are outside this range, as the daily series and the streak clamping already assume, so `days` can never stretch; `range.last_event_date` reports the newest `local_date` of any row (null with none), which may be after `to`. `from` and `to` select a custom range (`range.key` is `custom`); a missing half, a bad date, `to` before `from` or more than 3660 days is `400`. 3660 days counts both ends, so `to` minus `from` is at most 3659 (3660 is `400`). `range.days` and `daily` cover every day of the range, zero-filled and ascending.
- **Definitions.** An *open* is an `open` row and its session is its `session_key`. An open is a **read** when its session's `SUM(read_time value)` is at least 10 seconds, **or** its `scroll` value is at least 25 **and** its read time is at least 3 seconds (`store.statsIsRead`; `StatsReadSeconds`, `StatsReadScroll`, `StatsReadScrollSeconds`). A scroll alone is never a read: a quick flick through an opened article, or next/previous paging past it, is a bounce (issue #120; before it a 25 percent scroll with no read time at all counted). The rule is applied when the summary is computed, never stored, so existing history is reclassified on the next summary. **Legacy opens** (`ts` earlier than the earliest `read_time` or `scroll` row ever recorded, or every open when there is none) count as reads, because the sender did not exist yet: their engagement is unknowable, so totals that include them can overstate real reading, and the dictionary and the Stats screen say so. `items_read` is `COUNT(DISTINCT item_id)` over read opens; `opens` counts all opens; `active_seconds` is `SUM(read_time value)`; `days_active` is the number of local dates with a read open; `legacy_opens` is the number of legacy opens in the range (counted as reads with nothing measured, so the Stats screen can say how many of its reads are unverified). A **bounce** is a non-legacy open that is not a read: `bounce_rate` of a feed is bounces over its non-legacy opens (null when it has none). `open_original_rate` is the distinct items with an `open_original` event over the distinct items opened (null when nothing was opened). `stars` counts `star` rows. Each source row also carries the raw counts behind the rates so the web can roll feeds up into folders exactly: `tracked_opens` (non-legacy opens, the `bounce_rate` denominator), `bounces`, `items_opened` (distinct items opened, the `open_original_rate` denominator) and `items_original` (distinct items with an `open_original` event); the rates are null when their denominator is 0 (`open_original_rate` is capped at 1). `avg_read_seconds` is the mean read time of read opens that have any, and each source row carries its two parts, `timed_seconds` (the sum of read time over its read opens that have any read time in the range) and `timed_items` (the count of those opens), so `avg_read_seconds` is `timed_seconds / timed_items` (null when `timed_items` is 0) and the web can roll feeds up into folders exactly. A session's read time and scroll are counted within the requested range (a session that crosses the range edge loses the part outside it).
- **Streaks.** All time, not range-limited: a day counts when it has at least one read open (the same rule, in SQL, over the session's whole read time and scroll rather than the range's part). `current` counts the run ending today or yesterday (else 0); `longest` is the longest run, `longest_end` its last day (the latest run wins a tie). One probe per date, stopping at the first read open of that date. A date later than today (rows written under a different time zone, or a clock that moved back) counts as today, so a future-dated row can never zero the current streak.
- **Heatmap and busiest.** `heatmap` lists only the non-zero (`weekday`, `hour`) cells over the range with `active_seconds` and `opens`; weekday is `0` Sunday to `6` Saturday. `busiest_weekday` and `busiest_hour` rank by `active_seconds`, ties (and no read time at all) by `opens`; both are null with no activity. `longest_read` is the read open with the most read time.
- **Sources.** Feeds with an open, read time or star in the range, ordered by `items_read` then `active_seconds` (then id), at most 300; the top-level `sources_truncated` is true when more feeds than that had activity and the list was cut. `feed_title`, `folder_id` and `folder_name` come from the newest open or star snapshot row in the range (so a renamed feed shows its latest name); a feed that only has read time in the range (a session that spanned its start) takes them from its newest `read_time` row in the range, whose `feed_title` snapshot is never null, so a source never shows a blank name. `subscribed` is true while the feed exists and is not the archive. Attribution follows the `feed_id` written on each row: an item moved to the archive feed (a feed kept as archive after unsubscribing) has its later events recorded under the archive feed, so an open written before the move and a read time written after it can land on two different sources, and the archive feed itself is listed with `subscribed` false.
- **Never opened.** Current feeds that are not archived (`disabled_reason IS NOT 'archive'`), subscribed no later than the end of the range (`created_at` before the end of the `to` day in the `tz` setting) and with no open in it, oldest subscription first (`subscribed_on`, the local date of `created_at`), at most 500.
- **Indexes.** Opens, read time and scroll come from the three covering partial indexes of migration 0009 (`INDEXED BY`, so the planner cannot pick `idx_stats_kind_ts`); stars and `open_original` use `idx_stats_kind_ts` (few rows). Every hinted query is listed in `statsHinted` and `TestStatsSummaryPlans` fails when one no longer plans with its index (the covering plan for the three, `idx_stats_kind_ts` for the small kinds, `idx_stats_feed` for the read-time name lookup); any future rebuild of `stats_events` must recreate the three 0009 indexes. Measured on 1,000,000 seeded events (a year, mostly `read_time`, session keys of 32 hex characters as in production), best of three, at most three queries at a time: week 130 ms, month 125 ms, year 0.51 s, all time 0.52 s (before the indexes: 2.1 s, 2.2 s, 6.4 s, 5.6 s). The 200 ms budget holds for the week and month views; a year or all time at that size is bounded by the single read-time scan, and only a rollup table would cut that further. Random 32-character keys cost more than sequential test keys did (the per-session lookups of the streak probe scatter), which is why these figures are higher than earlier measurements with short keys. Run `KIPPLE_PERF=1 go test ./internal/store -run StatsSummaryPerf -v` (it also prints the index build time and size).
- **Other views (not built).** Client split is `GROUP BY client, inferred`.

**Wrapped.** A yearly summary of reading, at `/stats/wrapped` (a "Your year" link on the Stats screen), shown in the web app and shareable as an image or text. It is derived entirely on the client from `GET /api/stats/summary` with a calendar-year custom range (`from=YYYY-01-01&to=YYYY-12-31`, 365 or 366 days, well inside the 3660-day cap) — there is no dedicated endpoint. A year picker selects the calendar year (bounded by `first_event_date` through the current year); months and the in-year streak (days with reading, and the longest streak within the year) come from the zero-filled `daily` series, top sources from `sources` (capped at 300, ordered by `items_read`), and time-of-day habits (busiest weekday, busiest hour) from `heatmap` and `behavior`, which cover the range. The seven cards are: items read, active reading time, days with reading and longest streak within the year, busiest weekday and hour, busiest month, top sources, and the longest read. Notes for the client: a custom range is not clamped to today, so `daily` runs to `to` and days after today are present with zeros (drop them, or use `to` = today for the current year); `range.days` counts the full requested range; `streaks` are all time, not in-year; `first_event_date` says whether the year is fully covered by recorded data. The web client currently derives "today" and the selectable year range from the **browser's** local clock, not the server's `tz` setting; near a day boundary (especially when the browser's zone differs a lot from the stats `tz`) a read from the last hour of the year or day may not yet appear in Wrapped even though it already appears on the main Stats screen. This is a known limitation, not fixed in this round.

A share dialog offers native share, downloading an image, or copying as text. Two toggles, both off by default, control whether a share includes the top sources' feed names or the longest read's title; left off (the default), a shared Wrapped includes only aggregate numbers (counts, durations, weekday/hour/month labels) and no feed names or article titles. The setting `stats.wrapped_enabled` (bool, default `true`; Settings screen, Statistics group, "Yearly Wrapped") hides Wrapped when off, and so does `stats.enabled=false` (turning off statistics altogether hides Wrapped too, not only the dedicated Wrapped toggle); statistics are kept and recording is unaffected either way. The server has no Wrapped endpoint and stores nothing about it; sharing happens only in the browser, on the user's action.

**Stats export, data dictionary and delete (`internal/api/statsexport.go`, `statsdict.go`, `internal/store/statsexport.go`).** All three are available with recording off: `stats.enabled` only stops new rows, and the data controls act on what is already stored. With recording off, `content=summary` still summarises the stored rows (the envelope says `recording_enabled: false`), unlike `GET /api/stats/summary`, which answers its empty off shape.

- **The legacy cutoff outlives a delete.** The summary's legacy-open cutoff (an open earlier than the first timed event counts as a read) is the earliest `read_time` or `scroll` `ts` ever recorded, kept in the hidden setting `sys.stats_timed_since` (like the other `sys.*` keys: read-only, not in the settings UI, not in `settings.json`; it travels in the database and its snapshots). `InsertStat` stores it in the same write transaction as the first timed row, only when absent, and computed from the rows so a database that predates it gets the right value from its next timed row; `StatsDelete` stores it, also only when absent, in the transaction of each window before removing anything, so the first delete on an older database fixes it before any row is gone. The summary takes the smaller of the stored value and the earliest row. Deleting the earliest timed rows, or every row, therefore never turns a kept bounce into a legacy read, and a short session recorded after a delete-all is still a bounce. No migration; the schema stays 9.
- **`GET /api/stats/export`, raw.** Rows of `stats_events` whose `local_date` is in the range (default `all`; `range` and `from`/`to` are parsed and validated as in the summary, and `include_inferred` filters as elsewhere), in `id` order, paged by keyset: `SELECT ... WHERE id > :after AND id <= :max AND local_date BETWEEN ... ORDER BY id LIMIT 5000`, `:max` being the newest id when the export started. Each page is fully read into memory and its rows closed **before** it is written, so no read is ever held across a network write and checkpoints are never pinned; the first page is read before any header is sent, so a database error at the start is a clean `500`. The response is flushed per page, the write deadline is extended per page (the server's 60 s `WriteTimeout` would otherwise cut a long export off), and the export stops when the request context is cancelled. A database error after the headers are sent aborts the connection rather than ending the body cleanly. It does not take the summary's gate, and `HEAD` answers the headers only.
- **Detecting a cut-off download.** Every raw export carries `X-Kipple-Rows`, the number of **records** (not lines: a CSV title can contain line breaks, so count CSV records with a CSV parser; JSONL has one record per line) the range holds at the start, counted with the same `:max` bound and filters as the pages. Fewer records than that means the download was cut off, or that a delete ran while it did (the rows it removed are gone from the later pages), so a complete file can be shorter than the header. As a best-effort end-of-stream check every raw export also declares the trailer `X-Kipple-Rows-Sent` up front and sets it after the last page: the number of records actually written; it is absent when the export stopped early, and a proxy may drop trailers. The dry-run count of the same range is the other yardstick. A JSON export ends with `"event_count": N` (authoritative for JSON) after the events array, and a cut-off one is not valid JSON at all. CSV and JSONL have no envelope, so `X-Kipple-Titles-Included` (`0` or `1`), `X-Kipple-TZ` and `X-Kipple-Include-Inferred` carry the rest of the metadata, and a `titles=0` filename ends in `-no-titles` (for example `kipple-stats-20260927-no-titles.csv`, summary too). **The start count is index-only**: one count per big kind from the covering indexes of migration 0009 (`INDEXED BY`, checked by `TestStatsSummaryPlans`) plus the few star, unstar, open_original and share rows through `idx_stats_kind_ts`, with the same `id <= :max`, `local_date` and inferred filters; a `HEAD` skips it.
- **Columns**, in this order: `id, ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id, feed_title, folder_id, folder_name, item_title, item_url, value, session_key`. `ts` is unix seconds, `local_weekday` is 0 for Sunday, `inferred` is 0 or 1. `event_id` is internal and not exported. With `titles=0`, `item_title` and `item_url` are blank (CSV) or `null` (JSON, JSONL) in raw rows and the `longest_read` title is blank in a summary; feed and folder names stay.
- **Formats.** `csv` is RFC 4180 (`encoding/csv`, CRLF, a header row, `text/csv`); `bom=1` puts a UTF-8 byte-order mark first (default off; Excel needs it to read UTF-8, most other tools do not want it; ignored for the other formats). A line break inside a text cell is one CRLF: CRLF, LF and a lone CR in the source all become one break (`encoding/csv` would drop a lone CR without a word, so the cell is normalised first). **Formula-injection guard:** a CSV text cell (`kind`, `client`, `feed_title`, `folder_name`, `item_title`, `item_url`, `session_key`) that starts with `=`, `+`, `-`, `@`, a tab or a carriage return gets a single quote in front, so a spreadsheet shows it as text instead of running it; numeric columns are never touched, and the dictionary says so. `json` is `{"format":"kipple-stats-export","version":1,"content":"raw","exported_at":<RFC 3339>,"tz","range":{key,from,to},"titles_included","include_inferred","recording_enabled","dictionary":{...},"events":[...],"event_count":N}`, written as the envelope then one event at a time, so a large export is never built in memory; ids are strings and empty columns are `null`. `jsonl` is one such event object per line with no envelope. Filenames are `kipple-stats-YYYYMMDD[-no-titles].<format>` (the date in the `tz` zone).
- **The `all` range.** In the envelope, `range.from` is the first `local_date` present (today with none) and `range.to` is today in the `tz` zone, exactly as in the summary, plus `range.last_event_date` (the newest `local_date` of any row, possibly after today; null with none). Coverage is explicit and differs on purpose: the `all` **summary** covers `from` through `to` only (rows dated after today are excluded), while the **raw** `all` export has no end date and includes every row, future-dated ones too, which `last_event_date` and `event_count` make visible.
- **`content=summary`** is JSON only (`csv` or `jsonl` answers `400`; the default format is `json` for it) and is the summary of that range, exactly the object of `GET /api/stats/summary` computed from the stored rows even with recording off, plus the envelope fields (`format`, `version`, `content: "summary"`, `exported_at`, `titles_included`, `include_inferred`, `recording_enabled`) and the full dictionary; it has no per-event rows. It takes the summary's process-wide gate and is named `kipple-stats-summary-YYYYMMDD[-no-titles].json`.
- **Data dictionary.** One Go source (`statsdict.go`) feeds both the full `dictionary` (columns, kinds, definitions, summary fields) in every JSON export, raw and summary, and `GET /api/stats/dictionary`, so a person or an AI tool can read an export cold: every column with type, unit and description, the kinds, what counts as a read (10 s of read time, or 25 percent scroll with at least 3 s of read time; legacy opens count too and the dictionary says they may overstate reading, and their cutoff is remembered even if the rows are deleted; the numbers in the text come from the store constants), local-time semantics (`local_*` are fixed at write time in the zone then in force; `ts` is always UTC), the response headers, and how each summary field is computed. `?format=md` returns the same as Markdown.
- **`POST /api/stats/delete`.** `{from, to}` deletes the rows whose `local_date` is in the range, inclusive; `{all:true, confirm:"DELETE ALL"}` deletes every row (`400` unless `confirm` is exactly that string); `dry_run:true` only counts (reader pool) and answers `{count, deleted: 0}`; otherwise `{count: n, deleted: n}`. It needs `from` and `to` valid with `from` <= `to` and caps nothing else. It deletes in id windows of 10,000, one `WithWrite` transaction each, so the single writer is never held long and a window with no matches is an index-bounded probe; it stops between windows when the request context is cancelled; rows recorded after it starts are not considered. **Partial results:** if a window fails after earlier windows committed, the answer is `500` (`503` with `Retry-After` for maintenance) with `{"error":..., "message":..., "deleted": n, "complete": false}` (`n` is what really went) and the failure is logged; running the same delete again finishes it. **Deadline:** each window extends the response write deadline, so the server's 60 s `WriteTimeout` does not cut a long delete. A proxy in front does give up (Cloudflare answers `524` after about 100 s) while the server keeps deleting; the client then sees an error and must re-check with a dry run, and a rerun finishes the job. It touches **only** `stats_events` (and, first, the `sys.stats_timed_since` setting above): never items, read state, starred flags, the id ledger, feeds or user settings, and nothing else needs updating afterwards (the indexes follow the rows). The autoincrement counter is kept, so an id is never reused. Deleting is permanent; the nightly snapshot and any export are the only copies.
- **Measured** on 1,000,000 seeded rows through the handler with the body discarded (`KIPPLE_PERF=1 go test ./internal/api -run StatsExportPerf -v`; the timings vary a lot with the machine and with what else is running): raw CSV about 2.7 to 3.9 s (about 260,000 to 370,000 rows a second, 180 MB), jsonl about 3.6 s (390 MB), with the Go heap staying near 15 MB; the start-of-export count of 1,000,000 rows takes about 0.22 to 0.30 s index-only against 0.34 to 0.41 s for a walk of the rows, since only one page is in memory at a time; delete of all 1,000,000 rows about 11 s (100 windows), so a delete stays inside 100 s up to roughly nine million rows. A real download is bounded by the network.

---

## 9. Go package layout and goroutines

```
cmd/kipple/main.go      subcommands (serve is the default; healthcheck, import, api-password, password, restore,
                        version).
                        serve: config → ensureDataDir → lock.Acquire(kipple.lock) → store.Open (migrations;
                        sqlite OFDLocking is switched on inside Open) → ResumeFeedDeletes → ensureAccount → SeedZone (TZ, first start only) →
                        accessVerifier (optional, §7.0) → imgcache.Open (optional) →
                        one shared auth.Verifier, fetch client, ftrun runner, sched, maint, favicon finder → greader.Front
                        ahead of the root mux, wrapped by auth.WarnUntrustedProxyHeaders and httpx.Secure →
                        serve; shutdown sequence (§4.10); import _ "time/tzdata"; mime registrations
cmd/kipple/*.go         import.go, account.go (api-password, ensureAccount), password.go, restore.go
                        (+ restore_owner_*.go), access.go (accessVerifier, startup key-set prefetch), datadir.go
                        (ensureDataDir), healthcheck.go (the healthcheck probe), shutdown.go (shutdown budget and
                        stages, §4.10); see §2.6
web/embed.go            package web: //go:embed all:dist (web/dist from the Vite build)
internal/config         KIPPLE_* env only: ADDR(:1919), DATA(/data), USERNAME, PASSWORD, API_PASSWORD, ALLOWED_HOSTS; TZ
                        (optional initial), PUBLIC_URL, TRUSTED_PROXY_IPS, SCHED_TICK,
                        FETCH_WORKERS(8), FETCH_PER_HOST(2),
                        LOG_LEVEL, LOG_GREADER_FORMS, ACCESS_TEAM_DOMAIN + ACCESS_AUD (both or neither, §7.0).
                        Documented in .env.example. Nothing else reads os.Getenv except the healthcheck
                        subcommand (KIPPLE_ADDR only).
internal/clock          the injectable time source (scheduler, id allocator, store)
internal/lock           the data-directory lock (kipple.lock): flock on Unix, LockFileEx on Windows
internal/store          db.go (three pools, DSNs, sqlite OFDLocking under a sync.Once), tx.go (WithWrite, holder
                        tracking, deadline), migrate.go + migrations/*.sql, idalloc.go (seed, high-water, clock
                        check), feeds.go, subs.go, feedadmin.go, feedstatus.go, health.go, items.go / uiitems.go /
                        itemstate.go (list/keyset/get; SetRead/SetStarred/MarkScope/MarkAllRead, restore of trimmed
                        items, all taking tx), search.go / searchquery.go, fetchcommit.go (CommitFetch/
                        CommitFetchError), retention.go (trim, ledger updates), maint.go (purges, snapshot,
                        checkpoint), settings.go (typed reads with defaults; not cached), account.go, sessions.go,
                        devices.go, favorites.go, savedsearch.go, filters.go / filterretro.go, autoread.go,
                        fulltext.go (incl. KnownUIDs) / fulltextmode.go, export.go, uibootstrap.go, selfcheck.go,
                        stats.go (stats_events insert, session lookups, StatsEnabled), statssummary.go (the
                        summary queries, §8), statsexport.go (keyset export pages, index-only counts, windowed
                        delete), icons.go, space_*.go (free-space check before a migration). Kipple-database SQL lives in this package, except
                        `internal/opml` (import/export, run inside a store transaction), `internal/backup`
                        (checks on a copied file) and one count in `internal/api/autoread.go`; `internal/imgcache`
                        has its own separate SQLite index.
internal/feedurl        Normalize and Key (scheme-less url_key), shared by store, fetch and opml
internal/fetch          client.go (transports, ssrf.go guard, UA, timeouts, hop recorder), fetch.go
                        (conditional GET, cap, charset.go, body hash), parse.go (gofeed parse, malformed-item
                        skip, enclosure dedup), dedup.go (uid, content_hash, text_hash, in-document duplicates,
                        churn/dup detection), errors.go (classification), backoff.go (pure NextOnSuccess/
                        NextOnFailure; injected clock + rand), redirect.go (policy decision)
internal/discover       feed autodiscovery for the UI add-feed path, through the guarded transport
internal/favicon        the favicon finder (§4.11): links.go (pure `<link rel=icon>` parser and ranking, fuzzed),
                        sniff.go (raster-only byte sniffing), favicon.go (Lookup through the guarded transport),
                        finder.go (the one background goroutine, NextCheck backoff); its SQL is store/icons.go
internal/sanitize       absolutize.go (URL attribute resolution, base chain), policy.go (bluemonday feed policy,
                        RequireParseableURLs, no relative URLs), iframes.go (ingest pre-pass), text.go (plain text
                        + word count), leadimage.go (lead-image pick), rewrite.go (serve-time proxy rewrite),
                        serve.go (ServeHTML, StripTracking), url.go
internal/extract        go-readability v2 (codeberg.org/readeck) pipeline: guarded fetch, charset, absolutize,
                        sanitize, text, image
internal/ftrun          the one place a stored item's full-text extraction runs; the ingest pool and the
                        on-demand endpoint share a Runner (joined extractions, per-host limit)
internal/filter         The keyword rules engine (round-2 backend additions), pure: no I/O, no DB, no clock. rule.go (Rule,
                        Item, Error, the limits and the package comment that states the matching semantics),
                        compile.go (CompileRule, NewSet: validation, regex safety, set-wide caps), text.go
                        (normalize, word runes, containsTerm), prefilter.go (required-literal prefilter for regexes),
                        eval.go (Set.Evaluate, precedence: star beats mute, mute implies read, lowest mute id
                        wins). Regexes are RE2 only, compiled with (?i) unless case-sensitive, rejected when they
                        match the empty string, exceed 500 instructions, repeat more than 50 times or cost more than
                        MaxRegexCost (20,000) on their own; the enabled regex rules of a set (at most 50) stay under
                        MaxRegexSetCost (60,000), which admits 40 typical 15-word keyword alternations on title and
                        content. These limits apply to the rule being created or edited (ValidateEdit); a stored rule
                        from an older version that no longer meets them never blocks another rule's edit, preview or
                        apply. The store disables such a rule with a reason (settings key sys.filter_disabled_reasons,
                        shown as the filter's disabled_reason) on the first filters list, filter write or fetch
                        commit that meets it, instead of ingest skipping it silently. Scanned text is truncated (content 32 KiB
                        for text, 8 KiB for regex, other fields 4 KiB). Single-word terms are answered from a
                        per-field word set. Reference numbers: 10,000 items x 50 rules in about 1.7 s (170 us per
                        item); 25 regex rules x 5 patterns on a full 8 KiB scan in about 9 ms per item; 40 keyword
                        rules with their prefilter defeated about 13 ms; the worst case the set cost cap allows,
                        about 0.45 s.
internal/sched          sched.go, dispatcher.go, worker.go, run.go (attach/outstanding), fulltext.go (the
                        extraction pool)
internal/maint          maint.go: the one maintenance goroutine (hourly/nightly/Sunday, cancellable);
                        its SQL is store/maint.go
internal/backup         the backup export (zip of a VACUUM INTO snapshot, OPML, settings, manifest) and its
                        read-back for `kipple restore`: archive.go, backup.go, check.go, job.go, sessions.go
internal/greader        api.go (New, Front: path cleaning and dispatch switch, auth, client-seen map), routes.go,
                        form.go (raw-preserving), itemid.go, stream.go, h_streams.go (ids, contents), h_edit.go
                        (edit-tag, mark-all-as-read), h_subs.go, log.go (request log with redaction)
internal/api            api.go (routes, session check, same-origin guard: authed/needsOriginCheck/sameOrigin),
                        login.go, account.go, bootstrap.go, items.go (incl. POST /api/stats/events), feeds.go,
                        feedadmin.go, filters.go, autoread.go, savedsearches.go, devices.go, settings.go /
                        settingsmeta.go, opml.go, fulltext.go, image.go, imgcache.go, backup.go,
                        maintenance.go, sse.go, access.go (accessProof: the one rule for signing in without a web
                        password, §7.0), statssummary.go, statsexport.go (export + delete), statsdict.go
internal/auth           argon2id Verifier (params, semaphore, memo, per-client attempt budget), cookie sessions,
                        effective scheme, trusted-proxy IP, web login FailureTracker (5 free failures, then doubling waits to 60 s),
                        GeneratePassword, WarnUntrustedProxyHeaders
internal/access         Cloudflare Access JWT verifier (§7.0): RS256 against the team's key set, iss/aud/exp/nbf;
                        key set cached 1 h (stale-while-revalidate, usable 24 h while refreshes fail), refetch on
                        an unknown kid at most once a minute, one fetch at a time. nil when KIPPLE_ACCESS_* are unset
internal/events         SSE hub: Subscribe/Publish/Close, per-subscriber buffer 64, replay ring bounded at 500
                        events and 1 MiB
internal/opml           encoding/xml structs, Import (folder tree, positions, report), Export (nested tree, kipple: attrs)
internal/imgproxy       signed, streaming proxy handler (flags, AVIF sniff, semaphores, hotlink ladder, cache tee),
                        upstream.go, relay.go, thumb*.go (lazily started thumbnail worker pool)
internal/imgcache       bounded on-disk image cache: index.db, LRU, idle expiry, failure cache, disk guard, repair
internal/stats          Recorder interface (Record, RecordMany, RecordStars, all taking tx) + impl (event validation,
                        event_id dedup, local-time fields)
internal/web            SPA handler (index ETag + no-cache, immutable /assets), plus the `/_status` page
internal/httpx          headers.go: Secure (security headers, CSP via PageCSP). No recover middleware and no CSRF
                        code: the same-origin guard is in internal/api/api.go and the redacting request log is
                        internal/greader/log.go
```

**Dependencies** (pinned): `modernc.org/sqlite` v1.59.x (with its exact `modernc.org/libc`), `github.com/mmcdole/gofeed` v1.4.x, `github.com/microcosm-cc/bluemonday` v1.0.27, `codeberg.org/readeck/go-readability/v2`, `golang.org/x/net` (html, charset), `golang.org/x/text`, `golang.org/x/crypto/argon2`, `golang.org/x/image` (thumbnails), `golang.org/x/sys`, `golang.org/x/term` (password prompt). Tests use `testify/require`.

**Dockerfile.** Three stages: `node:22-alpine` (`npm ci && npm run build`) → `golang:1.27-alpine` (`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`, with cache mounts) → `gcr.io/distroless/static-debian12:nonroot`.

**Compose** (`host-a` project, service `kipple`): named volume `/data`, `stop_grace_period: 30s`, `GOMEMLIMIT=64MiB`, `mem_limit: 256m`, json-file logging 10m × 3, port 1919 (7080 only where `KIPPLE_ADDR=:7080` is set). `http.Server` has `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `WriteTimeout` 60 s (SSE, the backup download, the stats export and the stats delete extend it per write or batch with `http.ResponseController.SetWriteDeadline`), and `IdleTimeout` 120 s.

**Long-lived goroutines:**

1. `http.Server`: per-connection goroutines. SSE handlers select on the subscriber channel and `r.Context()`. The `ListenAndServe` call runs in its own goroutine.
2. The sched dispatcher (1). It is the only owner of in-flight, per-host, host-deadline, pending and run state.
3. Fetch workers (8), ranging over `jobs`.
4. The full-text extraction pool (`FulltextGlobal`, default 4), running through `ftrun`.
5. Maintenance (1).
6. The favicon finder (1), one lookup at a time (§4.11).
7. The imgcache sweep loop (1), plus a short goroutine per cap change.
8. The thumbnail worker pool (started lazily on the first thumbnail).
9. Short-lived job goroutines: the backup export, a filter apply, an auto-read run, the stats summary's query group (at most three), a shutdown stage (`bounded`), and the Access key-set fetch (once at startup, then on demand in the background, one at a time; no ticker).
10. Main, in `superviseServe`, selecting on the listener error and `ctx.Done()` (the signal watcher).

**Synchronization inventory:**

- the id allocator mutex;
- the hub mutex;
- the `atomic.Pointer`s for the Reader API account snapshot (5 s TTL), the API `imgMode`, and the `WithWrite` holder (settings are read on each call, not cached);
- the Verifier semaphore, memo and per-IP failure map (mutex) (the web login has its own);
- the greader client-seen map (mutex);
- the image-proxy semaphores and per-host limiter, and the imgcache mutex;
- the full-text queue mutex and condition variable, and the `ftrun` in-flight call map;
- the commit gate;
- the stats summary gate (one computation at a time) and its three-slot query semaphore;
- the Access verifier's key-set mutex and single fetch slot;
- the writer `*sql.DB` with one connection, reachable only through `WithWrite`: the only persistence serialization point.

No goroutine holds a transaction across a channel send, an HTTP call or an SSE wait. No code inside `WithWrite` touches a `*sql.DB`. `go test -race ./...` runs in CI (the Windows dev box has no C compiler, so it cannot run there).

---

## 10. Test plan

CI runs `go test -race -shuffle=on -timeout 15m ./...` (the `race_on`/`race_off` build-tagged test files stretch timing ceilings under the race detector; the Windows dev box has no C compiler, so it runs without `-race`). Every store and API test gets a SQLite file in `t.TempDir()` on the **pinned modernc driver**. Golden files with an `-update` flag are used only by the `internal/fetch` parser tests; the Reader contract tests assert inline. Items marked **TODO** below are planned but not yet written.

**`internal/store`**

- **Migrations:**
  - A fresh database gets `user_version` = the latest migration (currently 9) and `application_id=1263095884`. Re-opening is idempotent.
  - A foreign `application_id` is refused. A `user_version` above the maximum is refused.
  - A pre-migration snapshot is created for a non-empty database. **A failed migration followed by a retry succeeds**: the unique timestamped name means no "output file already exists".
  - `PRAGMA foreign_keys` is 1 after a failed `foreign-keys-off` migration (**TODO**: the restore mechanism in `migrate.go` exists, but no test drives it; the failed-then-retry path is covered by `migrate0004_test.go`).
- **Constraints:**
  - the username CHECK, the default-folder rules and the retention CHECK (75 rejected) are tested at the CLI and API level (`cmd/kipple/account_test.go`, `internal/api/feedops_test.go`, `internal/api/settings_test.go`), not in the store package;
  - **TODO** in the store package: a second archive feed is refused; `url_key` uniqueness.
- **WithWrite:**
  - a nested writer-DB call is impossible by type (compile-time: store write methods accept only `Querier`);
  - **TODO**: a deliberately leaked `Rows` caught by a test helper that asserts the pool is free after each handler test (the helper does not exist yet);
  - an acquisition timeout logs the holder.
- **Allocator:**
  - seeding from high-water plus items plus ledger, and ids that stay increasing across a restart and a backward clock step (`TestIDAllocMonotonicAcrossRestart`);
  - **TODO**: strictly increasing under 8 goroutines × 10k calls; the high-water mark surviving an unsubscribe cascade of the newest items; a seed 2 h ahead logging ERROR and setting the health warning.
- **Query-plan regression:** `EXPLAIN QUERY PLAN` is asserted against §2.3, **twice**: fresh, and after `ANALYZE`. Built: `TestStreamIDsQueryPlans` (300 seeded items: the ids legs, unread and starred), `TestOrderQueryPlans` (900 items: the list order and keyset queries) and `TestSearchPlan`. **TODO**: plan tests for contents, mark-all, the trim-set selection, the dedup lookup, the due selection, `FindFeedByURL` and the stats session lookups, and a larger seeded database (the design target was 150k items). `INDEXED BY` never errors.
- **WAL bound (TODO, not yet written):** 1000-id edit-tag `a=read` on articles of 20 KB should write < 1 MB of WAL (measured with `wal_autocheckpoint=0` and the `-wal` file size). No test sets `wal_autocheckpoint=0` or measures the `-wal` file today.
- **FTS:**
  - insert, update, delete, trim, restore and cascade stay consistent, and `integrity-check` is clean after each;
  - **TODO**: a markup-only update leaves `SELECT count(*) FROM items_fts_data` unchanged, and `integrity-check` after trim and restore;
  - title-only and text-only changes re-index correctly;
  - the query sanitizer handles `"`, `-`, `:`, `*` and empty input.
- **Maintenance:**
  - a pre-existing `kipple-snapshot.tmp` does not stop the nightly snapshot;
  - **TODO**: a missing `/data/backup` is created (the test pre-creates it);
  - `sys.last_snapshot_at` is updated;
  - cancelling the context stops the snapshot (tested with a context that is already cancelled).

**`internal/fetch` and `internal/sanitize`**

- **`backoff`:** the exact table for intervals of 30 and 5 minutes, n = 1..10, and **10080 minutes, n = 1..3** (always 7 days). Jitter bounds use an injected random source. The success schedule with ttl, max-age and Expires hints. `hostUntil` dominates.
- **`redirect`:**
  - 301×3 then migrate;
  - the same-host http→https immediate migration (`url_key` unchanged, `url_original_key` set);
  - a changing target resets the count;
  - 302 never persists;
  - an https→http downgrade is refused;
  - a target matched by `FindFeedByURL` is refused with a note;
  - a mixed 301+302 chain counts as temporary.
- **`dedup`:**
  - the guid, link and hash chain;
  - in-document duplicate guids;
  - `link` and `link_title` modes;
  - no date in any key;
  - `text_hash` ignores markup;
  - the churn and duplicate-guid notes and their thresholds.
- **`absolutize`** (inline tests, `internal/sanitize/absolutize_test.go`):
  - relative `src`, `srcset` (several candidates), `href`, `poster` and `source[src]`;
  - `xml:base` (**not built**: nothing resolves `xml:base` and there is no test);
  - a relative item link;
  - protocol-relative `//cdn/x.png`;
  - a `data:` URI;
  - an unresolvable value is dropped;
  - readability output resolved against the article URL.

  **TODO**: an end-to-end fixture with `<img src="/wp-content/x.jpg">` that must come out absolute in `stream/items/contents` **and** in `GET /api/items/{id}`, proxied in the latter only when it is http.
- **`errors`** with httptest servers: 200, 304 (with and without Last-Modified), ETag with `W/`, an `Expires: 0` quirk, 404, 410, 403 with `cf-mitigated`, 429 with a seconds or date Retry-After, 503 with none, 500, oversize, empty, HTML instead of a feed, a windows-1252 body with a UTF-8 declaration, a BOM, gzip, a redirect loop, a timeout, and a guard rejection of `127.0.0.1` and `10.x`.

**`internal/sched`** (fake clock, injected random source, httptest feed servers)

- Due selection and in-flight de-duplication.
- The per-host cap of 2 is never exceeded.
- A pending FIFO drains when a host slot frees.
- A 429 sets `hostUntil`, sibling feeds skip until it passes, and manual runs write `skipped` rows.
- **Runs:**
  - pressing refresh while 3 scheduled fetches are in flight gives `run.done` with `total` = enabled feeds, and a second press after that starts a new run;
  - pressing refresh during an import run starts a manual run instead of joining;
  - a run with `total = 0` finishes immediately.
- **Reply channels:** a per-feed refresh whose handler timed out, followed by another priority job, both complete, and the dispatcher keeps ticking.
- **Commit-gate fairness:** during a 138-feed run against local fixtures, a loop of API `SetRead` writes has p99 latency ≤ 250 ms.
- Chunked commits over 500 items.
- **Trim on every success:** a `not_modified` fetch after lowering N trims.
- **Shutdown:**
  - `TestShutdownDuringLargeRun` covers shutdown during a large run in under 15 s, a completed fetch committing its fetch_log row, no worker blocking on `doneCh`, and waiting handlers returning (`ErrStopped`, which the API maps to 503 `shutting_down`);
  - SSE subscribers returning before `Shutdown` completes is covered in part by `internal/api/api_test.go`;
  - **TODO**: an assertion that `wal_checkpoint` runs.

**Retention**

- N ∈ {50, 250, 0}, with and without starred items and `retain_until` holds.
- Ranking by `sort_at`: a backfilled old item is trimmed first and a future-dated item is clamped.
- The ledger upsert (`ON CONFLICT` refreshes `read`), and stubs written when `restore_days > 0` but not when it is 0.
- `trimmed_unread_count` excludes same-transaction inserts and is unchanged by mark-all.
- **Restore:**
  - `edit-tag a=starred` on a trimmed id → it appears in `ids s=starred` and in contents with the same id;
  - `r=read` → it appears in `ids xt=read`, has `retain_until` set, and survives the next trim;
  - a web `PUT /star` on a trimmed card restores it;
  - a restore after the stub purge is ignored with `OK`.
- A tombstoned uid is not re-inserted and its `last_seen_at` is bumped.
- The purges respect `trimmed_at`/`restore_days` and `last_seen_at`.
- **Unsubscribe with starred items:**
  - the items move to the archive feed, which is never in `subscription/list`; `origin.streamId` = archive, `origin.title` = the original feed;
  - ledger rows and stubs cascade;
  - unsubscribing the archive feed deletes it.
- `mark-all-as-read s=starred` does not touch the ledger.
- A `trim_only` job from a retention PATCH.

**`internal/greader` unit tests**

- **`itemid`:** NNW long padded `tag:google.com,2005:reader/item/00063f8740c61a40`; NNW legacy unpadded long form; Reeder bare `00063f8740c61a40`; decimal `1758700000123456`; NNW's vectors `00058b10ce338909` ↔ `1560279178774793`, `ffffffffffffcdef` ↔ `-12817`, `0000000000000000` ↔ `0`; `0x…`; garbage and 17-digit hex skipped; the emitted formats round-trip.
- **`form`:**
  - `;` in `t=` and `a=`, raw `=` inside a value, `+` as a space, `%2B`, repeated `i`, a pair with no `=`, body plus query merge, and the 4 MiB cap;
  - `Content-Type: application/x-www-form-urlencoded; charset=utf-8`, `text/plain`, `application/octet-stream` and an absent header all parse;
  - zero ids from a non-empty body logs a WARN.
- **`front`:** `//reader/api/0/edit-tag` under the prefix, a trailing-slash base, a doubled prefix, and root forms. None produces a 3xx, and POST bodies arrive intact.
- **`ts` normalization:** 10, 13, 14, 15, 16 and 19 digits, 0, absent (committed `max(id)`, not the allocator), and non-digit.
- **No-null rule:** every golden Reader response is walked and asserted to contain no JSON `null` except `LSID`. Fixtures include a feed with no `site_url`, an item with no link, and `greader.icon_urls` off.

**`internal/greader` contract tests.** Each test replays a full request sequence against a seeded database and asserts the responses inline (`contract_test.go`; there are no golden files).

- *NetNewsWire 7.1.4 sequence* (from its source):
  1. `POST ClientLogin` (the body parses on `=` into exactly two parts, and `Auth` has no `=`)
  2. `GET token`
  3. `tag/list` (200, then 304 with its ETag)
  4. `subscription/list` (200, then 304)
  5. `stream/items/ids?s=reading-list&ot=<Date−3mo>&n=1000` following continuations to the end, with no empty trailing page. **The first page includes a content-changed older item** (leg 2 with no `c`)
  6. `ids xt=read` with no `ot`
  7. `ids s=starred`
  8. `POST contents` with 150 long-form ids, including 3 trimmed and 2 unknown (those are omitted; the envelope has an int `updated`; every item has `summary`, `categories` and `origin.streamId`)
  9. `edit-tag` with 1000 ids in four passes, including ledger and unknown ids (all `OK`; a replay is a no-op)
  10. `edit-tag a=starred` on a trimmed id, then `ids s=starred` includes it (restore)
  11. a stale `T` returns 401, then `token`, then a retry succeeds
  12. `quickadd` of a new URL, then `subscription/list` with the *old* ETag returns 200 and includes the new `feed/N`
  13. `quickadd` of the **http** form of an already-migrated https feed returns `numResults:1` with the existing `streamId`
  14. `subscription/edit ac=edit t=… a=user/-/label/New` moves the feed and creates the folder
  15. `r=` without `a=` moves the feed to Uncategorized
  16. `rename-tag` (including the merge case)
  17. `disable-tag` with the raw body `T=…&s=user/-/label/News & Politics+` deletes that folder
  18. `subscription/import` of an OPML with `;` and `&` in titles
  19. a folder name containing `;` survives the trip
  20. `ac=unsubscribe` of a feed with starred items: those ids stay in `ids s=starred`
- *Reeder Classic sequence* (reconstructed from FreshRSS and Miniflux logs):
  1. `ClientLogin`
  2. `user-info`
  3. `subscription/list` (`iconUrl` and `htmlUrl` present as strings)
  4. `ids reading-list xt=read n=10000`
  5. `ids starred n=10000`
  6. `ids s=read ot=now-30d n=10000`
  7. `token`
  8. `POST contents` with 100 bare-hex ids in two batches
  9. `edit-tag a=read` with a single id → 0 `open` rows (API `open` inference is not built)
  10. several scroll-style single-id `a=read` requests → 0 `open` rows
  11. `edit-tag r=starred`
  12. `edit-tag` with `T=x` plus a valid header (accepted)
  13. `mark-all-as-read s=feed/N ts=` with 10, 13 and 16 digits (items and ledger updated up to the cutoff; nothing newer)
  14. the same sequence mounted at the root `/reader/api/0/` and under a doubled `/api/greader.php/api/greader.php` prefix
- *Unread and FeedMe smoke tests:* `GET /api/greader.php` returns `OK`; `ids n=100000`; `tag/list?types=1`; `stream/contents?…&ot=0`; `user-info` without `output`; an unknown endpoint returns `200 []`.
- *Auth matrix:*
  - no header → 401 with both headers;
  - a wrong token → 401;
  - a GET with a cookie but no token → 401;
  - the API disabled (NULL hash) → ClientLogin 401;
  - the attempt budget never returns 429;
  - **after 10 wrong-password attempts from one IP, the correct password from the same IP is verified and succeeds after at most the 2 s pace, even with nothing remembered, and leaves the failure count alone**; a concurrent second attempt from one client waits for the first and is then verified;
  - a memoized login does not call argon2 (counted through a fake);
  - 50 concurrent wrong ClientLogins never run more than one argon2 at a time (fake with a concurrency gauge).
- *`ot` semantics:*
  - an item crawled before `ot` whose content changed after `ot` is returned, including on a first page without `c`;
  - an item crawled before `ot` and read after `ot` is **not** returned;
  - the 120 s slack boundary on both legs;
  - a property test on randomized data, **with and without `c`**, in both directions, shows the two-leg form equals the reference OR query.
- **Phase-1 gate.** On day 1, the owner's real Reeder and NNW traffic is captured with `KIPPLE_LOG_GREADER_FORMS=1` and turned into additional sequences. Any mismatch with the reconstructed sequences becomes a failing test before phase 2 starts. Status: no captured-traffic sequences exist under `internal/greader`; the only captured evidence is a Reeder log from 2026-09-26 (alpha.2), kept outside this repository. The capture was also meant to decide whether API reads could count (§8); the owner has since decided they never do.

**`internal/stats` and `internal/api`**

- **The structural rule:** a fake Recorder counts calls.
  - `POST /api/items/mark-read` with 1 id, 500 ids, a scope, and `reason=scroll` → 0 calls.
  - greader `mark-all-as-read` → 0.
  - `edit-tag` with 2 ids and `a=read` → 0.
  - `edit-tag` with 1 id and `a=read` → 0 (API `open` inference is not built).
  - `r=read` → 0.
  - `a=starred` with 5 ids → 5 `star`.
- **Atomicity (TODO):** a Recorder that returns an error rolls back the state change in the same transaction.
- **Web:** `POST /open` gives 1 `open` plus a read state; `GET /api/items/{id}` gives 0.
- **Validation:**
  - `read_time` without an open is dropped;
  - a cumulative time above elapsed is dropped;
  - more than 3600 is dropped;
  - **two concurrent batches whose sum exceeds the cap insert at most the cap** (**TODO**: no concurrent-batch test yet);
  - scroll 150 is clamped to 100;
  - a `session_key` from another item is dropped.
- **Views:** with default parameters (`include_inferred=0`), inferred rows are excluded from streaks and feeds-never-opened (built; **TODO**: no summary test seeds an inferred row).
- **Local-time fields** across the DST boundaries (2026-03-08 and 2026-11-01, America/New_York): **TODO** (`TestRecordSnapshotAndLocalTime` covers the fields, not the DST dates).
- **Stats export, dictionary and delete (built, `internal/api/statsexport_test.go`, `statsexport_more_test.go`, `internal/store/statsexport_test.go`):** CSV round-trip (commas, quotes, newlines, lone CR and CRLF in titles), the formula guard, `bom`, and every header (`X-Kipple-Rows`, `-Titles-Included`, `-TZ`, `-Include-Inferred`, the `-no-titles` filename); a 25,034-row export in csv, jsonl and json that crosses four page boundaries, with `event_count` and the row header agreeing; a cancelled context stopping after one page; every 400 path; origin and auth guards; the full dictionary embedded in raw and summary; summary export matching the summary endpoint, and working with recording off; the `all` range covering rows dated after today; delete: a range delete keeps rows outside it and leaves every other table untouched, dry run, `all` with the confirm phrase, a partial failure (a trigger aborting the second window) reporting `deleted` and `complete:false` and a rerun finishing, one progress call per window, the persisted legacy cutoff (deleting the earliest timed rows and then everything leaves kept opens' classification and `items_read` unchanged and a new short session a bounce); export and delete with recording off. `TestStatsExportPerf` (opt-in) times a million rows.
- **CSRF:**
  - a POST with `Sec-Fetch-Site: same-site` → 403;
  - no `Sec-Fetch-Site` with a mismatched `Origin` → 403;
  - no `X-Kipple-Client` → 403;
  - a beacon with `Sec-Fetch-Site: same-origin` and no `X-Kipple-Client` → accepted.
- **OPML:**
  - re-importing the original NewsBlur OPML after http→https migrations adds **0** feeds;
  - import → export → import is a fixed point (names with `&`, `;` and `,`, titles, order, overrides);
  - multi-folder memberships and case merges are reported;
  - `http_auth` is not exported;
  - `mark_read_older_than_days` marks the right first-fetch items read.

**`internal/imgproxy`:**

- a bad signature → 403;
- tampered flags → 403;
- no session → 401;
- SVG refused;
- AVIF (an `ftyp avif` fixture) accepted;
- `Content-Length` over the cap → 502 with no body;
- an undeclared overflow aborts the connection, with heap growth under 1 MB;
- a private IP is blocked unless the flag allows it;
- passthrough validators and 304;
- cache: a miss tees to disk and the second request never reaches the source; 20 concurrent requests make one upstream call; a truncated, overflowing or abandoned body is never cached; failures replay without contacting the source and expire on schedule; a stale entry revalidates (304, 200) and is served stale on a 5xx or a slow source; the hotlink ladder sends exactly the documented shapes, remembers the winner, and never sends Kipple's address; at most 4 fetches per host; a full disk streams uncached; cap 0 behaves as the uncached proxy.

**`internal/imgcache`:** LRU order and the 90% target, cap changes and 0, idle expiry, failure TTLs and backoff, revalidation bookkeeping, single flight, startup repair (temp files, missing, truncated and orphan files), a corrupt or old-schema index, the disk floor, `Clear`, host hints, concurrent use with consistent counters and no leaked goroutines, and exclusion from exports and snapshots.

**Shipped suites the plan above predates** (each lives beside its package):

- **Filters:** the pure engine (`internal/filter`: matching semantics, precedence, regex safety limits, fuzz corpora, benchmarks) and the store, API, sched and greader `filters_test.go` files (ingest, retroactive apply, unmute paths, Reader agreement with muted items).
- **Backup, restore and the CLI:** `internal/backup` (export, expiry, image-cache exclusion), `cmd/kipple/restore_test.go`, `restore_wal_test.go`, `restore_owner_unix_test.go`, `password_test.go`, `account_test.go` (`password` and `api-password`), and the data lock in `internal/lock/lock_test.go`.
- **Full text:** `internal/ftrun`, `internal/extract`, `internal/sched/fulltext_test.go`, `internal/greader/hold_test.go` (the Reader hold), and the store and API full-text tests.
- **Devices, saved searches, auto-read, search v2, discovery:** the store and API test files of those names, `internal/maint/autoread_test.go` and `internal/discover`.
- **Thumbnails:** `internal/imgproxy/thumb*_test.go`.
- **Stats summary and export:** `internal/store/statssummary_test.go` (`TestStatsSummaryPlans` and the rest), `internal/api/statssummary_test.go`, the `statsexport` tests, and the opt-in `TestStatsSummaryPerf` / `TestStatsExportPerf` (`KIPPLE_PERF`).
- **Cloudflare Access and the optional web password:** `internal/access/access_test.go`, `internal/api/access_test.go`, `cmd/kipple/access_test.go`.
- **Server lifecycle:** `cmd/kipple/shutdown_test.go`, `healthcheck_test.go`, `datadir_test.go`.
- **Web (Vitest):** the suite under `web/` (`npm test`; includes the stats sender, Stats screen, export and data controls, Wrapped, passwordless sign-in and theme schedule suites).

**Release checklist** (manual, on Host-A, before each deploy, after `/code-review high`):

- The image is under 50 MB (`docker image inspect`).
- Idle RSS stays under 100 MB after 30 minutes with the real OPML (`docker stats`).
- **RSS stays under 100 MB during a 150-id contents request of full-text items, and under 20 concurrent wrong ClientLogins.**
- From outside the LAN:
  - `curl` ClientLogin through `https://rss.example.com/api/greader.php` returns 200 (Access bypass works).
  - `https://rss.example.com/api/greader.php//reader/api/0/token` with the header returns 200, not a 3xx.
  - `https://rss.example.com/reader/api/0/token` gets the Access redirect.
  - `https://rss.example.com/` gets the OTP page.
  - `curl -N` on `/api/events` with a session cookie streams pings line by line through the named tunnel.
- Reeder and NNW each complete a full sync.
- The health view shows a snapshot younger than 24 h.

---

## 11. Known risks and mitigations

| Risk | Mitigation |
|---|---|
| **Reeder Classic is closed source.** The `mark-all-as-read` `ts` unit, continuation behaviour, `T=x` use, its Content-Type, its field-type strictness and its scroll-mark batching are inferred, not observed | Tolerant parsing (`ts` by digit count, any non-multipart POST body parsed as a form), `n` up to 100000, `T` `""`/`x` accepted, no JSON nulls anywhere. The day-1 capture was meant to turn real traffic into contract tests (§10; not yet done) |
| **Reeder does not follow continuation** (`n=10000`). If more than 10,000 items are unread, the oldest never reach Reeder and counts differ from NNW and the web UI | Warned in the bootstrap `warnings` and the health view. The UI OPML import offers "mark items older than N days read" on first fetch as an explicit choice. The default N=250 × 138 feeds can reach 34,500, so the migration import should use that option |
| **`ot` excludes user state changes** (a deliberate departure from the brief's wording) | Both target clients pull full unread and starred lists without `ot`; FreshRSS behaves this way. `greader.ot_includes_user_changes` (default off) restores it: items whose read or starred state changed since `ot`, unread and unstar included, join leg 2 through `items.state_changed_at` (migration 0007) |
| **The `ot` slack of 120 s on both legs** returns up to 2 minutes of already-known ids | Harmless: clients de-duplicate by id |
| **NNW's `Date` parser uses a full-weekday pattern** and may fall back to a 3-month `ot` every refresh | A few pages of 1000 ids at worst. Watch the access log for `ot` of about now − 90 d |
| **Newest-N retention with NNW:** a trimmed unread item vanishes and NNW marks it read locally | Default N=250, `trimmed_unread_count` in the health view, per-feed overrides. A mark-unread within 90 days restores and holds the item |
| **Stars and mark-unread on items older than `restore_days`** (stub purged) are still ignored, and the client undoes them | 90 days matches NNW's article window. The window is a setting (≤ 180) if the owner stars very old items from app caches |
| **Restore stubs cost disk** (about 270 MB at 90 days for busy feeds) | `retention.restore_days` can be lowered; the nightly snapshot size shows the cost |
| **The archive feed is hidden everywhere**, so a Reader API client sees archived starred items under a stream id it has no subscription for | The server still serves them in the starred and reading-list streams with their original feed name; a client that only shows starred items of its own subscriptions (NetNewsWire may) will not show them. The web Starred view always does. "Purge unstarred" and deleting the archive (`DELETE /api/feeds/{id}?delete_starred=1`) remain API-only |
| **API `open` inference is not built.** No API read ever becomes an `open` (a Reeder read counts only as read state) | The `inferred` column exists, but nothing writes `inferred=1` |
| **API subscribe without a synchronous fetch** (the default, decision 33) shows the host as title and no items until the next sync | Opt-in with `greader.subscribe_fetch_now` (phase 3): a bounded synchronous first fetch, 8 s per request |
| **GUID churn** produces a wall of unread | The fetch_log note (kept), a health-view flag, and one-click "mark this fetch read". No automatic judgement |
| **A dedup-mode change** needs a rekey; unmatched items are inserted read | An explicit user action; `rekey_pending` persists across restarts and the re-key runs on the feed's next fetch |
| **The ledger purge** (after max(180 days, `restore_days` + 7) since last seen) re-opens the tombstone for uids the publisher drops and later re-adds | Rare; shows as one unread item |
| **The `modernc.org/sqlite` and `libc` pin is fragile** | Pin both exactly. The plan-regression suite (fresh and `ANALYZE`d) and FTS integrity tests run on every bump. `ncruces/go-sqlite3` is the fallback |
| **FTS over a view** relies on FTS5 reading external content through a join | Validated on 3.50.4 (MATCH, snippet, integrity-check); re-validated on the pinned driver in the migration test. `'rebuild'` repairs from the view |
| **Write-lock latency** during a manual run or a huge first fetch | The commit gate plus 250-item chunking, and narrow rows for state writes. The fairness test asserts p99 ≤ 250 ms |
| **A writer-pool deadlock or leaked Rows** would silently stop ingestion (there is no monitoring, by design) | `WithWrite` is the only door, with a 10 s deadline and holder logging; store writes take `Querier`; the pool-free test helper is planned but not written (§10) |
| **The allocator can be seeded ahead of the clock** (bad RTC, manual error). Every id minted until real time catches up is "in the future": NNW's `ot` matches those items on every refresh, and Reeder's `mark-all-as-read ts=<now>` never covers them, so they come back unread | Startup ERROR plus a health banner above 1 h ahead; NTP on Host-A. Kipple does not rewrite ids |
| **Unpadded bare-hex ids made only of digits** would be parsed as decimal | No target client sends them. A test documents the limitation |
| **SSE through cloudflared and Access** is unverified on this named tunnel | Pings every 15 s, `no-transform`, the `/api/status` polling fallback, and `curl -N` on the release checklist |
| **Access path precedence** for the `/api/greader.php` Bypass app has community reports of inconsistency | The external `curl` checks are mandatory. The root Reader routes never get a bypass |
| **Password exposure on the public ClientLogin** | A separate, generated API password, argon2id (m=19 MiB) behind a global semaphore of 1, a success memo, a per-client failure budget enforced before hashing by pacing, not refusal (one hash in flight per client, over budget a wait doubling from 2 s to 60 s, bounded waiting, IPv6 per /64, never 429), and redaction in logs. A distributed attacker gets at most ~20 guesses per second against a 24-character random password |
| **The stateless Reader token** can only be revoked by changing the API password | Accepted for a single user. The settings UI says so |
| **Cloudflare-challenged feeds** fail on TLS fingerprint | `error_class='cloudflare'` with actionable text; `disable_http2` and a UA override |
| **Per-host limits key on the exact hostname** | About 15 rounds of 1–2 s in a manual run at 138 feeds |
| **The uid, content_hash, text_hash and `url_key` rules** are embedded in stored data | Frozen before the first real import, with versioned prefixes. Any change needs a re-keying migration |
| **Extracted full text is not searchable**, and restores lose extracted full text | Accepted for phase 2. Re-extraction is on demand |
| **The in-memory full-text queue** (500 items) is lost on a restart, and the Reader full-text hold can delay a new item by up to 30 s | Extraction runs after the commit in the `ftrun` pool, so it never holds a fetch worker (§4.3). Items queued at a restart are extracted on demand when opened. The hold is capped at 60 s inside the `ot` slack, and open question 55 keeps a Reeder check on the release checklist |
| **The Reader `summary.direction` is hard-coded `ltr`**, so right-to-left feeds render wrong in Reeder and NNW | Deferred in both the Reader API and the web UI (neither sets a text direction today). Deriving `rtl` from the feed or item `xml:lang` is a later fix |
| **Image cache growth and disk use** | Bounded by `imgproxy.cache_mb` (default 1 GiB, LRU to 90%, 60 day idle expiry) plus a free-space floor of max(2 GiB, 5%) below which nothing is written; the size shows in the health view and Settings; excluded from every backup. The index is a second SQLite file with its own 2 MB page cache (about 2 MB more than the §2.1 figure) |
| **Hotlink retries look like a browser** | Only after a 401/403/429 with no `Retry-After` or Cloudflare challenge, at most two extra requests per image, remembered per host; never sends Kipple's address or a cookie, and goes through the same SSRF-guarded transport |
| **Memory: SQLite heap is outside GOMEMLIMIT** | GOMEMLIMIT 64 MiB + 34 MB of caches + runtime < 100 MB, streaming contents, capped `i=`, keyset exports, a streaming image proxy, and a `mem_limit: 256m` backstop. RSS under load is on the release checklist |
| **The iOS Home Screen app** has its own cookie jar and WebKit bug 272325 | A persistent (Max-Age) cookie and a cheap re-login. A long Access session duration (about 1 month) |
| **The nightly snapshot is Kipple's only backup artifact**, and nothing watches it | `sys.last_snapshot_at` and `last_snapshot_error` shown in the health view (red after 48 h), plus a release-checklist item |
| **Image budget** (under 50 MB) is unmeasured with modernc plus 11 font families | Size check on the release checklist; fonts subset per the phase-3 plan |

---

## 12. Red-team dispositions

| # | Issue | Disposition | Reason |
|---|---|---|---|
| 1 | [major] Star or mark-unread on a trimmed item is silently undone by the next sync | **FIX** | 90-day `trimmed_content` restore stubs; `a=starred`/`r=read` restore the row with the same id (mark-unread held 7 d), so the next starred or unread list keeps the change (decision 17, §5). Validated; contract test added |
| 2 | [major] Null or missing fields where Miniflux/FreshRSS always send strings | **FIX** | `iconUrl` always present (`""` when off); `htmlUrl`/`origin.htmlUrl`/`href`/`author` are `NOT NULL DEFAULT ''` in the schema; a golden-test rule allows no JSON null except `LSID` (decision 32) |
| 3 | [minor] NNW disable-tag sends the folder id raw, so `&`/`+` names are never deleted | **FIX** | The form parser keeps raw text; label lookup tries decoded, raw and PathUnescape forms; disable-tag glues unencoded name tails in the parser (§6.2). NNW test with `News & Politics+` |
| 4 | [minor] ServeMux 301s `//` paths, dropping POST bodies | **FIX** | A front handler cleans slashes and prefixes and dispatches Reader paths with its own switch; no ServeMux sees them (decision 26, §6.1). Tests plus a release-checklist curl |
| 5 | [minor] Unrecognized Content-Type silently discards edits | **FIX** | `mime.ParseMediaType` with parameters ignored; every non-multipart POST body is parsed as a form; WARN on zero ids from a non-empty body (§6.1–6.2) |
| 6 | [minor] Item HTML never absolutized (client-compat) | **FIX** | Merged with #25: ingest-time resolution of every URL attribute, readability output included, relative leftovers dropped (decision 31) |
| 7 | [minor] quickadd on an existing URL returns 0 results; the `url_original` match is missing | **FIX** | Idempotent quickadd/`ac=subscribe` through `FindFeedByURL` (scheme-less `url_key`/`url_original_key`) returning `numResults:1` with the existing streamId (§6.9) |
| 8 | [minor] ot leg 2 empty when `c` is absent (`min(x, NULL)`) | **FIX** | Absent `c` binds to MaxInt64 descending and 0 ascending, never NULL; validated; the property test covers no-`c` (§3, §6.5) |
| 9 | [minor] Reeder never follows continuation, so unread beyond 10,000 is missing | **FIX** (residual **DEFER**) | Warning when unread > 10,000; an explicit UI import option marks old first-fetch items read. The client limit itself cannot be fixed server-side (§11) |
| 10 | [minor] Login lockout never checks the password and slides forever | **FIX** | The password is always verified; success clears the IP's count; only failures are delayed, in a fixed window (§6.3). Test: 20 bad then 1 good from the same IP succeeds |
| 11 | [minor] An allocator seeded ahead of the clock breaks ot and mark-all | **FIX** (residual **DEFER**) | Startup ERROR plus a health banner above 1 h ahead; the §11 row now names the ot/mark-all impact. Rewriting ids is not attempted |
| 12 | [major] Read/star state in the wide items row rewrites overflow pages | **FIX** | `items` split into a narrow row plus `item_content`; FTS over a join view; triggers moved and validated (decision 30). The WAL-bound test is designed but **not yet written** (§10). The alternative (column reorder plus `idx_items_read`) is rejected |
| 13 | [major] The one-connection writer deadlocks on nested acquisition or leaked Rows; maintenance can't run under `query_only` | **FIX** | `WithWrite` is the only door with a 10 s deadline and holder logging; store writes take a tx; `Recorder.Record(tx, …)`; a snapshot pool for `VACUUM INTO`; FTS integrity-check on the snapshot; weekly optimize dropped for automerge (decision 2, §2.1, §2.6) |
| 14 | [major] Reply channels and shutdown order can wedge the dispatcher or hang workers | **FIX** | Buffered reply channels, non-blocking dispatcher sends, handlers select on reply, timer and `shutdownCh`, a dispatcher draining until workers exit, commits under `WithoutCancel` with 10 s, a revised 7-step order, client-seen moved out of the dispatcher (§4.1, §4.10) |
| 15 | [major] VACUUM INTO refuses existing targets; foreign_keys may stay off | **FIX** | `MkdirAll` plus `Remove` before every VACUUM INTO; timestamped pre-migration names (newest 3 kept); foreign_keys restored in a defer and verified; last-snapshot time in the health view (§2.5, §2.6) |
| 16 | [minor] ot leg-2 NULL `c`, and no visibility slack on leg 2 | **FIX** | Same binding fix as #8; leg 2 uses `content_changed_at >= ot − 120`; the legs stay disjoint (validated) |
| 17 | [minor] FTS `AFTER UPDATE OF` fires on markup-only changes | **FIX** | `WHEN old IS NOT new` guards on every update trigger, SET lists built from changed columns, and `text_hash` gating; validated (`items_fts_data` 21 → 21) |
| 18 | [minor] GOMEMLIMIT doesn't bound SQLite; contents built unbounded | **FIX** | GOMEMLIMIT 64 MiB + 34 MB of caches budget, a streaming contents encoder, `i=` capped at 1000, `mem_limit: 256m`, an RSS-under-load checklist item (decision 35) |
| 19 | [minor] Long-lived read cursors in exports pin checkpoints | **FIX** (built in phase 4) | The stats export pages by keyset, 5000 rows per short query, fully read before writing; the OPML export is built before writing (§8) |
| 20 | [minor] Reader pool keeps only 2 idle connections | **FIX** | `SetMaxIdleConns(4)`, `SetConnMaxIdleTime(0)` (§2.1) |
| 21 | [minor] Stats ingest caps are check-then-insert across pools | **FIX** | Validation and inserts in one `WithWrite` transaction, counting earlier events in the same batch (§8). The concurrent-batch test is **not yet written** (§10) |
| 22 | [minor] Plans verified without ANALYZE on 3.50.4; production is 3.53.4/STAT4 | **FIX** | The plan suite (partly written, §10) runs on the pinned driver, fresh and after `ANALYZE`; `analysis_limit=400`; `optimize=0x10002` at open. `INDEXED BY` is kept: the leg always constrains the leading column, so it cannot become unsatisfiable (§2.3) |
| 23 | [minor] Mark-all cutoff and allocator seed include uncommitted or deleted ids | **FIX** | A missing `ts` → committed `max(id)` on a reader snapshot; `sys.id_high_water` persisted in CommitFetch; the stats scan dropped from the seed (§3) |
| 24 | [blocker] API open inference is on by default, so Reeder scroll marks count as reads | **FIX** | The inference is **not built**, so a Reeder scroll-style read never counts as an `open`; the default is effectively permanent until inference is built. The views are built with `include_inferred` default 0 (streaks and never-opened exclude inferred rows); the contract test with scroll-style marks asserts 0 `open` rows (decision 21, §8) |
| 25 | [major] Relative image/link URLs never made absolute (product-rules) | **FIX** | Same as #6, plus golden tests through both the Reader API and the web API; `/img/` collision removed |
| 26 | [major] A manual run can stay open forever; refresh joins import/retention runs | **FIX** | In-flight feeds are attached to the run; `total = outstanding` at creation; the run ends when `outstanding` hits 0; only manual runs are joined; runs are per kind (§4.9). Scheduler test added. A watchdog is unnecessary because completion is structural |
| 27 | [major] Starring an item trimmed while a client still shows it is lost | **FIX** | Same restore mechanism as #1, also applied to the web star and mark-unread endpoints. The proposed crawl-age grace window is **REJECTED**: it breaks "newest N" on busy feeds and doesn't cover NNW's 90-day window |
| 28 | [major] OPML re-import and quickadd duplicate feeds after the http→https migration | **FIX** | One `FindFeedByURL` (normalized, scheme-less, `url` or `url_original` key) used by every subscribe path; `url_key` is UNIQUE; a re-import test expects 0 new feeds |
| 29 | [major] The OPML round trip loses titles, order and per-feed settings | **FIX** (partial **REJECT**) | The OPML title goes to `custom_title` (cleared if it equals the doc title after the first fetch); positions from document order; `kipple:` override attributes; `http_auth` never exported; case merges and dropped memberships reported. Keeping multi-folder membership is rejected: decision 9, NNW disallows it (§7.6) |
| 30 | [major] Unsubscribing hard-deletes starred items | **FIX** | Starred items are re-parented to a single archive feed (listed in subscription/list while non-empty until 2026-09-29; now never listed, #100); the web dialog shows the count with an opt-in delete (decision 24, §6.9). Validated |
| 31 | [major] Unbounded argon2id on the public ClientLogin | **FIX** (partial **REJECT**) | m=19 MiB/t=2/p=1, a global semaphore of 1 with a 5 s wait, a success memo, a generated 24-character API password. The global failure lockout is rejected for ClientLogin: it would let an attacker lock the owner out, and the semaphore already bounds global rate and memory. The web login keeps its per-IP lockout (10 failures per 15 minutes) and a 90-day cookie (the owner's decision) |
| 32 | [minor] The backoff cap makes long-interval feeds poll more often when failing | **FIX** | `delay_s = min(interval_s·2^(n−1), max(86400, interval_s))`; 10080-minute rows in the backoff test (§4.6) |
| 33 | [minor] The health view's history and trim signals erode quickly | **FIX** | fetch_log kept 14 days with a 50-row floor, the last 10 errors kept, `keep=1` for redirect/churn/rekey notes; `last_error*` kept after success; an immutable `trimmed_unread_count` excluding same-transaction inserts, with a reset; no ledger update for starred-scope mark-all (§4.8, §5) |
| 34 | [minor] Lowering per-feed retention does nothing until new items arrive | **FIX** | Trim runs on every successful fetch (ok/unchanged/not_modified), and a retention PATCH or default change enqueues `trim_only` jobs (decision 18) |
| 35 | [minor] The API "never fetch" exceptions need the owner's sign-off | **FIX** (opt-in only; the default keeps the rule) | The default now has no outbound HTTP on any Reader API path: the feed is inserted as due and the scheduler fetches it within 30 s. An opt-in synchronous path (`greader.subscribe_fetch_now`, default off, 8 s per request) was built in phase 3 (decision 33) |
| 36 | [minor] Image proxy buffering, AVIF sniffing, private-net feeds | **FIX** | Content-Length pre-check then streaming with `MaxBytesReader` and an abort on overflow; AVIF by `ftyp` brand with an allow-listed upstream-type fallback; signed `flags` carry `allow_private_net`/`allow_insecure_tls`; a fetch semaphore of 8 (§7.4) |
| 37 | [minor] SameSite=Lax doesn't protect against same-site origins | **FIX** | `Sec-Fetch-Site: same-origin` (or a matching `Origin`) plus a required `X-Kipple-Client` on every state-changing cookie request; the beacon is exempt from the header only (§7) |

---

## 13. Deltas from lemon24/reader (status)

A research note on lemon24/reader (not kept in this repository) compared this design with a mature SQLite feed-reader library. Most of its ADOPT items were already here (per-connection pragmas in the DSN, `application_id`, `foreign_key_check` after migrations, `PRAGMA optimize`, a date-free `content_hash`, validators and body hash, per-feed resync, keeping stats rows without a cascade from `items`). This section was first written as "applied after the red team"; it is now a status table, one line per delta, so it never contradicts the body.

| # | Delta | Status |
|---|---|---|
| 1 | `synchronous=NORMAL` and `busy_timeout=5000` in every DSN | **Built** (`buildDSN`). WAL with `NORMAL` loses at most the last commits on power loss, which the next poll refetches |
| 2 | Startup self-check for JSON1 and FTS5 | **Built** (§2.1, `internal/store/selfcheck.go`) |
| 3 | Last-new-item time on the feed | **Built** as `feeds.last_new_items_at` (§2.2), written when a fetch inserts an item. Drives the `silent` status (§4.6) |
| 4 | Reading time with `image_count` and 265 wpm | **Dropped.** Reading time is `ceil(word_count / 230)`, null under 100 words, computed at serve time (§7.1). No `image_count` column |
| 5 | `porter` tokenizer with `bm25(items_fts, 4.0, 1.0)` | **Superseded and built** by the round-2 backend additions (a research note not kept in this repository): porter over unicode61 (diacritics folded) with a persistent `bm25(4,2,1)` rank, migration 0005 |
| 6 | 403 browser-UA fallback persisted as `feeds.ua_mode` | **Superseded** by `feeds.ua_fallback` (migration 0002) and the `fetch.user_agent_mode` setting (§4.4) |
| 7 | `Retry-After` HTTP-date relative to the response `Date` | **Built** (§4.4, `fetch.ParseRetryAfter`) |
| 8 | URL migration resets fetch state | **Dropped for redirects**: a redirect migration keeps validators and the items (§4.7). **Kept for manual URL edits**, which already clear them (`feedadmin.go`) |
| 9 | A malformed item is skipped, not fatal | **Built** (§4.3): an empty entry (no title, link, guid, text, enclosure or image) or one whose conversion fails is dropped and counted in the `skipped_malformed_items: n/total` fetch_log note. A document that does not parse at all is still a failed fetch, because the XML parser cannot resume after a syntax error. An entry whose only content is an enclosure or image (a podcast or photo feed) is kept: its title is the media file name (else the feed title) and its first media URL stands in as the guid, so each gets its own uid. |
| 10 | Enclosures deduplicated by URL at ingest | **Built** (`fetch.ParseFeed`), by resolved URL |
| 11 | Normalized links for dedup (`l:` uid and `link_hash`) | **Dropped.** The uid rules are frozen (§4.8, §11): they hash the raw link, and re-normalizing would change the uid of every live item, so the next fetch would re-insert a wall of duplicates. The common http to https migration is handled by the redirect policy (§4.7) instead |
| 12 | Slow-stage WARN (a stage of 1 s or more) | **Dropped.** It is log noise, and monitoring is a non-goal; `fetch_log` already records durations |

Rejected from reader, with reasons in the report: grid-snapped scheduling, dropping exponential
backoff, non-atomic entries-then-feed writes, `A-IM: feed` deltas, post-hoc delete-and-merge dedup
(ids already served to Reader clients cannot be retracted), a separate search database, and a
tri-state starred flag.
