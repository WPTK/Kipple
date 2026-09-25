# Kipple design (2026-09-24, revision 2: after red-team review)

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
   - **Writer pool.** `SetMaxOpenConns(1)` and `_txlock=immediate`. Only `store.WithWrite(ctx, func(tx *sql.Tx) error)` can reach it. Every store write method takes a `*sql.Tx` (or a `Querier`) and never the `*sql.DB`, so nothing inside a write transaction can ask the pool for a second connection. Each acquisition has a 10 s context deadline. On timeout it logs an ERROR naming the current holder (the helper records its caller's stack).
   - **Reader pool.** `SetMaxOpenConns(4)`, `SetMaxIdleConns(4)`, `SetConnMaxIdleTime(0)`, `query_only`.
   - **Snapshot pool.** `SetMaxOpenConns(1)`, not `query_only`. It is opened only by maintenance for `VACUUM INTO`, because `query_only` rejects that statement.
   - **Commit gate.** Fetch workers take a `chan struct{}` of capacity 1 before they `BEGIN`. API writers skip the gate and queue on the pool. `database/sql` does not hand a freed connection to waiters in FIFO order, so without the gate 8 queued fetch commits could keep winning over a Reeder `edit-tag`. With the gate, an API write waits for at most one feed commit.
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
   - The `greader.ot_includes_user_changes` setting (default `false`) restores the OR on `read_at`/`starred_at`.
   - *Rejected:* `user_modified_at` in `ot`. *Rejected:* published-date `ot` (Miniflux), which causes NNW's documented sync gap.

7. **Continuation uses an n+1 lookahead.** `continuation` is emitted only when `n+1` rows come back, so the last page never carries one. *Rejected:* `LIMIT n` emitting on every full page.

8. **The `subscription/list` and `tag/list` ETag is `sha256` of the rendered body**, truncated to 16 hex. It cannot go stale. *Rejected:* trigger-driven version counters and call-site bumps.

9. **Every feed is in exactly one folder.** `feeds.folder_id` is `NOT NULL DEFAULT 1`, and folder 1 is "Uncategorized", undeletable and unique. NNW's FreshRSS profile disallows root-level feeds and feeds in several folders. OPML imports that put a feed in several folders keep the first and **report** the rest (decision 34). *Rejected:* a nullable `folder_id`.

10. **Reader API auth: a stateless HMAC token over a separate API password hash.** `token = username + "/" + hex(HMAC-SHA256(secret, "greader-token-v1|" + api_password_hash))`: 64 hex, never contains `=`.
    - Password verification is argon2id with **m=19 MiB, t=2, p=1** (the OWASP minimum), for both the web and the API password.
    - A **global semaphore of 1** serializes every verification. The wait is bounded at 5 s, after which the request gets a 401.
    - A successful verification is remembered in memory as `HMAC(secret, "login|" + password)` and compared in constant time, so the owner's clients re-running ClientLogin cost no hashing. The memo is cleared whenever the password changes.
    - The password is **always** verified. A correct one succeeds and clears that IP's failure count. Only failures are delayed (§6.3).
    - The UI generates a 24-character random API password by default.
    - *Rejected:* API session rows. *Rejected:* reusing the web password on the public path. *Rejected:* a global failure lockout, because an attacker could then lock the owner out; the semaphore already caps the global hash rate and memory.

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

16. **Retention ranks by `sort_at = min(published_at, crawl_second + 86400)`**, then `id`: the UI order. Items with `retain_until > now` are exempt; they come from a mark-unread restore (§5). *Rejected:* pure arrival id.

17. **Trimmed ledger plus restore stubs.**
    - A trim writes the ledger row (id, feed, uid, read) and, for `retention.restore_days` (default **90**, matching NNW's article window), a `trimmed_content` stub that holds everything needed to rebuild the item.
    - `a=starred` on a ledger id that still has a stub, a web star on a trimmed card, or `r=read` / mark-unread on one **restores** the row into `items` with the same id and the new state, and deletes the ledger row. The id then appears in the next starred or unread list, so the client's reconcile keeps the change instead of undoing it.
    - A mark-unread restore sets `retain_until = now + 7 d`, so the item is not trimmed straight away.
    - After the stub window a ledger id is a tombstone only: a star is ignored and OK is returned (§11).
    - The ledger insert uses `ON CONFLICT (feed_id, uid) DO UPDATE`. Rows are deleted with the feed. A ledger row is purged 180 days after its uid was **last seen** in the feed document.
    - *Rejected:* "ignore stars on ledger ids" (revision 1; the red team showed both clients silently undo it). *Rejected:* a crawl-age grace window (7 days untrimmed). It breaks "newest N" on busy feeds and still misses NNW's 90-day window. *Rejected:* soft-deleting inside `items`, which would add `AND trimmed_at IS NULL` to every partial index and query.

18. **Trim runs in every successful fetch commit** (`ok`, `unchanged`, `not_modified`). Changing a feed's `retention`, or `retention.default`, also enqueues a `trim_only` job for the affected feeds. So does "Apply retention now". Trim never runs on page load or on API traffic. On a quiet feed it costs one indexed statement over ≤ N+starred narrow rows. *Rejected:* "only when the fetch inserted items" (revision 1). Lowering N on a quiet feed then did nothing for days.

19. **Full text lives in its own `item_fulltext` table** and can be toggled per feed (`feeds.fulltext`) and per article (`items.fulltext_mode`). For full-text feeds, extraction runs **inline in the worker, before the commit**: up to 20 newest new items, 10 s each, 60 s total. Items not extracted within the budget are extracted on demand from the UI. *Rejected:* a background queue. *Rejected:* a column on `items`.

20. **The image proxy is UI-only.**
    - Route `/img/{sig}/{flags}/{b64url}`, HMAC-signed over flags and URL, and session-gated. `flags` carries `allow_private_net` from the item's feed.
    - It streams the image and never buffers it whole. A declared `Content-Length` over the cap returns 502 before any bytes are sent. An undeclared overflow aborts the connection.
    - AVIF is sniffed by its ISO-BMFF `ftyp` brand.
    - The rewrite happens at serve time. Stored content and Reader API content keep the original absolute URLs.
    - *Rejected:* rewriting stored HTML, and any proxy under `/api/greader.php`.

21. **Stats.**
    - There is one `open` kind for every client. `client` carries web, pwa, reeder, netnewswire, unread or api, and `inferred=1` marks API-derived opens.
    - **API inference is off by default** (`stats.api_single_read_is_open = false`). It is turned on only if the day-1 capture shows that Reeder's scroll marks can be told apart from article opens.
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
    - The starred items are re-parented to a single hidden **archive feed** ("Unsubscribed (starred)", `disabled_reason='archive'`, never fetched, retention unlimited). It is created lazily and listed in `subscription/list` only while it holds items, so NNW's `origin.streamId` still resolves and the starred lists stay intact on every client.
    - Everything else cascades.
    - The web UI dialog shows the starred count and offers "also delete N starred items".
    - Unsubscribing the archive feed itself is a real delete.
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
    - `disable-tag` falls back to the raw body. NNW sends that folder id unencoded (netnewswire.md §4), so a name containing `&` or `+` would otherwise be lost.
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

33. **OPEN, needs the owner's confirmation: API subscribe paths and outbound HTTP.** The brief says API clients never trigger fetches.
    - **Default** (`greader.subscribe_fetch_now = false`): `quickadd`, `subscription/edit ac=subscribe` and `subscription/import` do **no** outbound HTTP and no discovery. They insert the feed with `next_fetch_at = now` and return at once. The scheduler's next tick (≤ 30 s) fetches the feed like any other due feed.
    - The first NNW or Reeder sync after that shows the feed title as its host and no items. The next sync fills both in.
    - The revision-1 behaviour (synchronous discovery plus a priority first fetch that waits up to 8 s) stays behind the setting, pending the owner.
    - The web UI's add-feed and OPML import are not API clients and keep synchronous discovery and runs.

34. **OPML fidelity.**
    - **Titles.** On import, a non-empty OPML title goes to `custom_title`. After the first successful fetch it is cleared if it equals the document title, so later title changes in the feed still flow through.
    - **Order.** Folder and feed `position` come from document order.
    - **Export.** `COALESCE(custom_title, title)` goes out as both `text` and `title`. Folders are nested outlines only; the comma-separated `category` attribute is never used. Disabled and gone feeds are included; the archive feed is not.
    - **Overrides.** Per-feed overrides are written as `kipple:` namespaced attributes and read back on import. `http_auth` is never exported.
    - **Import report.** The response lists every URL matched to an existing feed, every dropped extra folder membership, and every folder merged by case-insensitive name.
    - **Initial read state.** The UI import optionally marks items published more than N days ago as read on each new feed's first fetch. This is an explicit user choice made at import time.

35. **Memory budget: Go heap plus SQLite heap plus runtime, under 100 MB.**
    - `GOMEMLIMIT=64MiB`. modernc allocates SQLite's heap outside the Go heap, where GOMEMLIMIT does not reach it.
    - SQLite page caches: 16 + 4×4 + 2 MB = 34 MB worst case.
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

**Snapshot pool** (`SetMaxOpenConns(1)`): the same pragmas except `query_only`, with `cache_size(-2000)`. It is opened by maintenance, used for `VACUUM INTO`, and closed afterwards. `VACUUM INTO` holds only a read snapshot on the source, so it blocks no writers.

**Page caches.** The worst case is 16 + 16 + 2 = 34 MB. This memory lives outside the Go heap (decision 35).

**`store.WithWrite(ctx, fn)`:**

1. Derives a context with a 10 s deadline, calls `db.BeginTx`, and runs `fn(tx)`.
2. Commits when `fn` returns nil, and rolls back otherwise.
3. Records the caller (`runtime.Caller` plus the start time) in an atomic "holder" slot. A timed-out acquisition logs that slot at ERROR.
4. Every `*sql.Rows` opened inside `fn` is closed by `defer` in the store method that opened it. `UPDATE … RETURNING` goes through a helper that scans all rows and closes before returning.

**Other connection details:**

- Call `sqlite.OFDLocking()` before the first open.
- `wal_autocheckpoint` stays at 1000 pages.
- The database lives on the named volume `/data`, which is local disk (never NFS or SMB).
- The temp table `temp.trim_set` is created with `CREATE TEMP TABLE IF NOT EXISTS` at the start of each trim, so a replaced writer connection is harmless.

### 2.2 DDL — `internal/store/migrations/0001_init.sql`

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
-- on first start from KIPPLE_USERNAME / KIPPLE_PASSWORD (/ KIPPLE_API_PASSWORD).
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

-- Single-level folders (Reader labels are flat; nested OPML is flattened). Row 1 is the
-- undeletable default. AUTOINCREMENT: ids are never reused. position = OPML document order.
CREATE TABLE folders (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(trim(name)) > 0),
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
-- note: 'redirect_migrated: <old> -> <new>', 'redirect_target_owned_by_feed <id>',
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
| `idx_items_feed_sort (feed_id,sort_at,id)` | UI feed view (keyset); the trim-set selection `ORDER BY sort_at DESC, id DESC OFFSET N` (narrow-row lookups for `starred`/`retain_until`); Reader `feed/N` streams | `SEARCH items USING INDEX idx_items_feed_sort (feed_id=?)` |
| `idx_items_sort (sort_at,id)` | UI "All" view keyset | `SEARCH … (sort_at<?)` |
| `idx_items_unread_sort (sort_at,id) WHERE read=0` | UI default "Unread" view keyset | `SCAN/SEARCH … COVERING INDEX idx_items_unread_sort` |
| `idx_items_changed (content_changed_at) WHERE NOT NULL` | Leg 2 of the `ot` filter (§6.5), forced with `INDEXED BY` (always satisfiable: the leg has a range on the leading column) | `SEARCH items USING COVERING INDEX idx_items_changed (content_changed_at>?)`, the same after `ANALYZE` |
| Integer primary key | Leg 1 of `ot`; Reeder `s=read&ot=now-30d` (narrow pages now, ~5 MB for 34k rows) | `SEARCH items USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)` |
| `UNIQUE (feed_id, uid)` (autoindex) | Dedup lookup in CommitFetch (`uid IN json_each(?)`) | `SEARCH … sqlite_autoindex_items_1 (feed_id=? AND uid=?)` |
| `item_content` PK | Contents and item views join; the FTS view lookup by rowid | `SEARCH c USING INTEGER PRIMARY KEY (rowid=?)` |
| FTS5 | Search | `SCAN items_fts VIRTUAL TABLE INDEX 32:M3` then `SEARCH i USING INTEGER PRIMARY KEY (rowid=?)` |
| `trimmed_items UNIQUE (feed_id, uid)` | Tombstone check at ingest; ledger upsert | `SEARCH … COVERING INDEX sqlite_autoindex_trimmed_items_1` |
| `idx_trimmed_seen (last_seen_at)` | Nightly purge `DELETE … WHERE last_seen_at < ?` | `SEARCH … COVERING INDEX idx_trimmed_seen` |
| `idx_trimmed_trimmed (trimmed_at)` | Nightly stub purge `DELETE FROM trimmed_content WHERE id IN (SELECT id FROM trimmed_items WHERE trimmed_at < ?)` | range |
| `idx_feeds_due (next_fetch_at) WHERE enabled=1` | Scheduler tick selection | `SEARCH feeds USING COVERING INDEX idx_feeds_due (next_fetch_at<?)` |
| `idx_feeds_folder (folder_id,position)` | Label streams; subscription/list ordering; folder view | `SEARCH feeds USING COVERING INDEX idx_feeds_folder (folder_id=?)` |
| `idx_feeds_url_key` (unique), `idx_feeds_url_orig_key` | `FindFeedByURL`: `WHERE url_key = :k OR url_original_key = :k` | `MULTI-INDEX OR` |
| `idx_feeds_one_archive` | Enforces a single archive feed; archive lookup | — |
| `idx_fetch_log_feed (feed_id,id)` | Health view history; the retention floor `… OFFSET 49` | `SEARCH fetch_log USING COVERING INDEX idx_fetch_log_feed` |
| `idx_fetch_log_err (feed_id,id) WHERE outcome='error'` | "Keep the last 10 error rows" in the cap | `SEARCH … COVERING INDEX idx_fetch_log_err (feed_id=?)` |
| `idx_sessions_expires` | Nightly session purge | range |
| `idx_folders_one_default` | Enforces a single default folder | — |
| `idx_stats_kind_ts (kind,ts)` | Phase-4 windows | `SEARCH … (kind=? AND ts>?)` |
| `idx_stats_feed (feed_id,kind,ts)` | Per-source views | range |
| `idx_stats_session (session_key,kind)` | Ingest validation (open exists, cumulative read_time), inside the ingest write transaction | `SEARCH … idx_stats_session (session_key=? AND kind=?)` |

`stream/items/contents` uses `WHERE i.id IN (SELECT value FROM json_each(?))`, which plans as `SEARCH i USING INTEGER PRIMARY KEY` over the list: one statement for 100, 150 or 1000 ids, with no placeholder limit.

### 2.4 FTS5

- **Setup.** External content over the view `item_search`, with `unicode61 remove_diacritics 2`. Only plain text is indexed (`content_text`, produced by bluemonday `StrictPolicy` at ingest), so a retention trim deletes cheap rows. All three update triggers are value-guarded. Validated: 200 markup-only updates left `items_fts_data` unchanged at 21 rows.
- **Query.** User input is tokenized on whitespace. Each token is wrapped in double quotes, with embedded quotes doubled. A trailing `*` is kept outside the quotes for prefix search. Tokens are joined with spaces (implicit AND). No other FTS syntax is passed through.
  ```sql
  SELECT i.id, snippet(items_fts, 2, '<mark>', '</mark>', '…', 24) AS snip
  FROM items_fts JOIN items i ON i.id = items_fts.rowid
  WHERE items_fts MATCH :q [AND i.feed_id = :f | AND i.feed_id IN (SELECT id FROM feeds WHERE folder_id = :fo)]
  ORDER BY rank LIMIT :n OFFSET :off;          -- or ORDER BY i.sort_at DESC when order=date
  ```
- **Known limit.** Extracted full text is not indexed. Trimmed stubs are not searchable.
- **Maintenance.** There is no weekly `optimize`: FTS5's default automerge keeps segment counts bounded without one large write. Each week the nightly snapshot job runs `'integrity-check'` against the freshly written **snapshot file**, never the live database. A failure logs an ERROR, is recorded in `sys.last_snapshot_error`, and is shown in the health view. `'rebuild'` is a manual repair action in the UI and runs on the writer.

### 2.5 Migration mechanism

1. `//go:embed migrations/*.sql`. Files are named `NNNN_name.sql`, numbered densely from 0001.
2. On open, the runner reads `PRAGMA application_id`:
   - `0` with an empty schema is a fresh file.
   - `0` with a non-empty schema, or any value other than 1263095884, means refuse to start: "not a Kipple database".
3. It reads `PRAGMA user_version`. If that is higher than the highest embedded migration, it refuses to start (downgrade guard).
4. If migrations are pending and the database is not fresh:
   - `os.MkdirAll("/data/backup")`.
   - `VACUUM INTO '/data/backup/pre-migration-<from>-<to>-<unixtime>.db'` on the snapshot pool. The target name is unique, so a retried migration can never hit "output file already exists".
   - The newest 3 `pre-migration-*` files are kept.
5. It applies each pending file N on the writer connection, inside a function that has `defer`: `PRAGMA foreign_keys=ON`, followed by a check that `PRAGMA foreign_keys` returns 1. That check runs on success, on error and on panic, and startup aborts if it fails.
   - If the file's first line is `-- kipple:foreign-keys-off`, the runner executes `PRAGMA foreign_keys=OFF` before `BEGIN`. This is required for the 12-step table-rebuild procedure.
   - Then `BEGIN IMMEDIATE; <file>; PRAGMA foreign_key_check;`, which must return no rows or the migration rolls back. Then `PRAGMA user_version = N; COMMIT;`.
6. After all migrations, it runs `PRAGMA optimize` (`analysis_limit=400` is already set).
7. There are no down migrations. Rollback means restoring the pre-migration snapshot.

### 2.6 Maintenance (one goroutine, one cancellable context)

- **Hourly:** `PRAGMA wal_checkpoint(PASSIVE)` through `WithWrite`.
- **Nightly at 04:10 in `settings.tz`:**
  1. Through `WithWrite`, one short transaction per statement:
     - `DELETE FROM trimmed_content WHERE id IN (SELECT id FROM trimmed_items WHERE trimmed_at < now − restore_days·86400)`
     - `DELETE FROM trimmed_items WHERE last_seen_at < now − 180 d`
     - `DELETE FROM sessions WHERE expires_at < now`
     - `PRAGMA optimize`
  2. The snapshot, on the snapshot pool:
     - `os.MkdirAll("/data/backup")` and `os.Remove("/data/backup/kipple-snapshot.tmp")`, ignoring not-exist.
     - `VACUUM INTO '/data/backup/kipple-snapshot.tmp'`, using the maintenance context so shutdown interrupts it.
     - On Sundays, the FTS `integrity-check` against the tmp file, through a throwaway connection.
     - `fsync`, then an atomic rename to `kipple-snapshot.db`.
     - Record `sys.last_snapshot_at`, or `sys.last_snapshot_error` on failure. The health view shows the age of the last good snapshot and turns it red after 48 h.
  3. Host-A's host backup copies only `kipple-snapshot.db`, never the live db/wal pair.

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
  - Each `CommitFetch` that allocated ids ends with `INSERT INTO settings(key,value) VALUES('sys.id_high_water', :last) ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE CAST(excluded.value AS INTEGER) > CAST(settings.value AS INTEGER)`.
  - Ids therefore never go backwards across restarts, unsubscribe cascades or an NTP step back. `stats_events` is no longer scanned.
- **Clock sanity.** If the seed is more than 3600·1e6 µs ahead of `time.Now()`, startup logs an ERROR with the offset, and the health view shows a banner until real time passes the seed: "Item ids are ahead of the clock by X. `ot` windows and mark-all-as-read cutoffs are unreliable until then." Kipple still starts.
- **Where it is called.** Only inside `CommitFetch`, after `BEGIN IMMEDIATE` has acquired the write lock. New items in a batch are sorted oldest-first: `published_at` ascending, ties broken by reverse document order. Ids are allocated in that order. Consequences:
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
| mark-all `ts` | Normalized to µs by digit count: 1–12 → seconds × 1e6; 13–15 → milliseconds × 1e3; 16 → µs; ≥ 17 → ns ÷ 1e3. **Absent, 0 or non-digit → `SELECT max(id) FROM items` on a reader snapshot** (committed ids only, never the allocator, which can be ahead of visible rows). Applied as `id <= ts_us` |

---

## 4. Fetch scheduler

### 4.1 Components

`internal/sched.Scheduler` owns one **dispatcher goroutine** and 8 **worker goroutines** (`KIPPLE_FETCH_WORKERS`). The dispatcher owns these, touched only by itself:

```
inflight  map[feedID]*flight       // flight.runs: runs this attempt counts toward
perHost   map[host]int             // cap 2 in flight per host (KIPPLE_FETCH_PER_HOST)
hostUntil map[host]time.Time       // Retry-After deadlines (in memory, decision 11)
pending   FIFO of jobs waiting for a host slot or a worker
runs      map[runKind]*Run         // at most one active run per kind: manual, import, retention
live      int                      // workers not yet exited
```

`Run` holds `{id, kind, total, done, new_items, errors, outstanding}`. `outstanding` counts the jobs queued or in flight that belong to the run.

Channels (all reply channels are `make(chan T, 1)`, and every dispatcher send uses `select { case ch <- v: default: }`):

- `jobs`: capacity = number of workers
- `doneCh`: capacity = number of workers; carries results and `workerExit`
- `manualCh`: refresh-all requests with reply channels
- `priorityCh`: per-feed refresh, rekey, trim-only, UI subscribe; each job carries a reply channel
- `wake`
- `stopCh`

`commitGate chan struct{}` (capacity 1) is shared by the workers.

### 4.2 Tick loop

A `time.Ticker` fires every 30 s (`KIPPLE_SCHED_TICK`). A `wake` signal or a priority job also triggers a tick. The tick query runs on the reader pool with a 5 s context:

```sql
SELECT id, url, host, etag, last_modified, body_hash, user_agent, http_auth, ignore_http_cache,
       disable_http2, allow_insecure_tls, allow_private_net, dedup_mode, rekey_pending,
       interval_minutes, retention, fulltext, redirect_to, redirect_kind, redirect_count,
       consecutive_failures, initial_read_before, last_success_at
FROM feeds WHERE enabled = 1 AND next_fetch_at <= :now
ORDER BY next_fetch_at LIMIT 500;            -- idx_feeds_due
```

Selection rules:

- Feeds already in flight are skipped.
- A feed whose `hostUntil[host]` is in the future is skipped and stays due.
- Nothing is written at selection time. If the process dies mid-fetch, the feed is simply due again.

`enqueue(job)`: if `perHost[job.host] >= 2` or `jobs` is full, the job is pushed to `pending`. Otherwise the dispatcher marks it in flight (`inflight[feed] = &flight{runs: job.runs}`), increments `perHost`, and sends it to `jobs`. That send is non-blocking; the dispatcher never blocks.

On a result from `doneCh`, the dispatcher:

1. clears the in-flight mark and decrements `perHost`;
2. applies any Retry-After result to `hostUntil`;
3. for every run in `flight.runs`, increments `done`/`new_items`/`errors` and decrements `outstanding`. A run whose `outstanding` reaches 0 is finished: `run.done` is published and the run is removed;
4. replies to the job's reply channel, if any (non-blocking);
5. publishes SSE events;
6. drains `pending`.

### 4.3 Worker body

```
for job := range jobs:                            // exits when jobs is closed
  res := fetcher.Fetch(fetchCtx, snap)            // HTTP, redirects, charset, hash, parse, uid/content_hash/text_hash,
                                                  // absolutize, sanitize, text, word count, lead image; no DB held
  if res.OK && snap.fulltext:
      known := store.KnownUIDs(readerCtx, snap.id, res.UIDs)   // reader pool: items ∪ trimmed_items
      res.Fulltext = extractInline(fetchCtx, newest-first new items, max 20, 10 s each, 60 s total)
  if fetchCtx cancelled and res is not complete: send result{cancelled}; continue   // nothing written
  cctx := context.WithTimeout(context.WithoutCancel(fetchCtx), 10*time.Second)
  commitGate <- struct{}{}
      store.CommitFetch(cctx, res)  or  store.CommitFetchError(cctx, res)   // WithWrite, one BEGIN IMMEDIATE
  <-commitGate
  doneCh <- result{feed, host, outcome, newIDs, retryAfter}
doneCh <- workerExit
```

- No transaction is held across a network call, a channel send or an SSE write.
- A fetch that completed before shutdown still commits, under `WithoutCancel` with its own 10 s deadline, so the fetch_log row is not lost.
- Fetches of more than 500 new items (only realistic on a first fetch) are committed in chunks of 250. The gate is taken and released for each chunk. The trim and the feed bookkeeping run in the last chunk. A crash between chunks is safe.
- Job kinds that skip HTTP are `skip` (writes a `skipped` fetch_log row) and `trim_only` (the retention transaction only). Both go through the same gate.

### 4.4 HTTP client (`internal/fetch/client.go`)

- **Transport.** One shared `http.Transport`:
  - `DialContext` is a `net.Dialer{Timeout: 10s, Control: ssrfGuard}`. `ssrfGuard` runs after DNS on the concrete IP. It rejects loopback, private, link-local, multicast, unspecified, CGNAT `100.64/10`, NAT64, 6to4 and unique-local ranges (via `netip`), unless `allow_private_net=1`.
  - `TLSHandshakeTimeout` 10 s, `ResponseHeaderTimeout` 15 s, `MaxResponseHeaderBytes` 64 KiB.
  - `ForceAttemptHTTP2` on, `DisableKeepAlives` on.
- **Variants.** Built lazily and cached: `disable_http2`, `allow_insecure_tls`, and their private-net versions. The image proxy uses the same variants, keyed by its signed flags.
- **Client.** `http.Client{Timeout: 20s, CheckRedirect: recordHop}`. `recordHop` records every hop's status and allows at most 5 hops.
- **Request headers:**
  - `User-Agent`: `feeds.user_agent`, else the `fetch.user_agent` setting, else `Mozilla/5.0 (compatible; Kipple/<ver>; +<KIPPLE_PUBLIC_URL>)`. The `+url` part is included only when configured.
  - `Accept: application/rss+xml, application/atom+xml, application/feed+json, application/json;q=0.9, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8`.
  - Basic auth from `http_auth`.
  - `If-None-Match` and `If-Modified-Since` (verbatim), sent only when stored non-empty, the job is not `full`, and `ignore_http_cache=0`.
  - `Accept-Encoding` is **not** set, so Go negotiates gzip transparently.
- **Body.** `http.MaxBytesReader(nil, body, 10 MiB)` caps the decoded size.
- **Charset** (gofeed only honours the XML declaration):
  1. A BOM wins.
  2. Else the XML declaration's encoding, else the HTTP charset, else UTF-8.
  3. If UTF-8 was chosen but the bytes are not valid UTF-8, retry with the HTTP charset, then `windows-1252`.
  4. Decode with `charset.NewReaderLabel`, rewrite the `encoding=` attribute to `utf-8`, and compute `sha256` of the decoded bytes: this is `body_hash`.
  5. `gofeed.Parser.Parse`.
- **Content pipeline per item** (`internal/sanitize`):
  1. Resolve the item link against the final feed URL.
  2. Parse the content HTML with `x/net/html` and resolve every URL attribute (`a[href]`, `img[src]`, `img[srcset]` and `source[srcset]` candidate by candidate, `video[poster]`, `video[src]`, `audio[src]`, `source[src]`) against the base chain `xml:base` → item link → `site_url` → final feed URL.
  3. bluemonday feed policy with `RequireParseableURLs(true)` and `AllowRelativeURLs(false)`.
  4. Plain text via `StrictPolicy`, word count, and the lead image (absolute).
  5. `content_hash` and `text_hash`.

### 4.5 Per-outcome handling

"Success bookkeeping" means:

- `consecutive_failures = 0`
- `last_success_at = last_fetch_at = now`, `last_status = <status>`
- `next_fetch_at` from the success schedule (§4.6)
- **`last_error*` is kept**. The UI shows it as resolved when `last_error_at < last_success_at`.
- The retention trim (§5) runs.
- On the feed's first success (`last_success_at IS NULL` before the commit), `custom_title` is cleared if it equals the document title, and `initial_read_before` is cleared.

"Error bookkeeping" means:

- `consecutive_failures += 1`
- `last_error`, `last_error_class`, `last_error_at`, `last_status` and `last_fetch_at` set
- `next_fetch_at` from the backoff (§4.6)

Every attempt appends a fetch_log row and applies the fetch_log cap (§4.8).

| Outcome | Handling |
|---|---|
| 200, parsed | Commit items (§4.8). Store validators from the response: empty when absent, and both dropped when `Expires: 0`. Store `body_hash`. Success bookkeeping. Outcome `ok` |
| 200, `body_hash` equals the stored hash | No parse. Update validators. Success bookkeeping. Outcome `unchanged` |
| 304 | Keep the ETag. Overwrite `last_modified` if the 304 carries one. Success bookkeeping. Outcome `not_modified` |
| 301/308 chain | Followed. The final response is handled by its own status. Redirect policy in §4.7 |
| 302/303/307 in the chain | Followed and never persisted. `redirect_to` is set with `redirect_kind='temporary'` (health notice only) |
| 200 with empty body | `empty`, backoff. Validators are **not** stored |
| Body over 10 MiB | `too_large`, backoff |
| Unparseable / not a feed | `parse`, backoff. Validators not stored |
| 404, other 4xx, 500/502/504 | `http`, backoff |
| 410 | `gone`: `enabled=0`, `disabled_reason='gone'`. The health view lists it as dead. Re-enabling resets failures and sets `next_fetch_at = now` |
| 401/403 | `http`, backoff. If 403 with header `cf-mitigated: challenge` and an HTML body: `cloudflare`, message "blocked by a Cloudflare challenge (TLS fingerprint); try 'disable HTTP/2' or a browser User-Agent" |
| 429 or 503 | Counted as a failure. `retry_after` comes from `Retry-After` (seconds or HTTP-date); if absent it is 1500 s; it is clamped to [60 s, 24 h]. `next_fetch_at = max(now + backoff, now + retry_after)`. The dispatcher sets `hostUntil[host]`. Note `retry_after=<s>s` |
| Timeout | `timeout`, backoff |
| `*net.DNSError` | `dns`, backoff |
| Other `*net.OpError`, `io.ErrUnexpectedEOF`, `*url.Error` | `connect`, backoff |
| x509 unknown authority, hostname error, `tls.RecordHeaderError` | `tls`, backoff. The health view offers `allow_insecure_tls` |
| Guard rejection | `ssrf`. The message includes the resolved IP. The health view offers `allow_private_net` |
| More than 5 hops | `redirect_loop`, backoff |
| Host deadline active during a manual run | fetch_log `skipped`, note `skipped: host retry-after until <ts>`. The schedule is untouched |
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

On success:

```
hint_s        = fetch.honor_publisher_ttl ? max(RSS <ttl>×60, Cache-Control max-age − Age, Expires − now, 0) : 0
d             = round(max(interval_s, min(hint_s, 86400)) × U(0.95, 1.05))
next_fetch_at = now + d;  ttl_hint_s = hint_s;  current_delay_s = d
```

Changing the global interval runs:

```sql
UPDATE feeds SET next_fetch_at = min(next_fetch_at, COALESCE(last_fetch_at, :now) + :new_interval_s)
WHERE interval_minutes IS NULL AND consecutive_failures = 0
```

The same statement, keyed on the feed id, runs when a per-feed interval changes.

Health statuses, computed at read time:

| Status | Condition |
|---|---|
| `archive` | `disabled_reason='archive'` (shown separately, never as an error) |
| `dead` | `disabled_reason='gone'` |
| `disabled` | `disabled_reason='user'` |
| `failing` | `consecutive_failures ≥ 14` |
| `erroring` | 1–13 consecutive failures |
| `throttled` | `hostUntil` in the future |
| `redirecting` | `redirect_to` set |
| `silent` | healthy, but `last_new_items_at < now − 90 d` |
| `ok` | none of the above. A resolved `last_error` is shown greyed with its time |

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
                 redirect_to = NULL, redirect_kind = NULL, redirect_count = 0
WHERE id = :f
```

Then add the fetch_log note `redirect_migrated: <old> -> <new>` with `keep = 1`. Validators are kept. The `feed/<url>` stream lookup uses `FindFeedByURL`.

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
  - A guid repeated within one document follows Miniflux: the first occurrence keeps its uid; later ones get `h(guid|rawLink)`, else `h(guid|n)`.
- `link`: `'l:'+h(raw link)`, falling back to the `h:` rule.
- `link_title`: `'l:'+h(raw link + "\x1f" + title)`.
- If more than 5% of the guids in a document are empty or duplicated, the fetch adds the note `guid_duplicates: k/n`. Nothing switches automatically.

**Hashes.** `content_hash` = `h(title | url | author | sanitized content_html)`. `text_hash` = `h(title | content_text)`. Dates are excluded from both.

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
if rekey_pending: for each new item whose absolutized url uniquely matches an existing row of this
                  feed not in the document by uid: UPDATE items SET uid = :new_uid (state kept); rekeyed++.
                  Remaining new items are inserted with read = 1. rekey_pending = 0. Note 'rekeyed: k' (keep = 1).
for each new item (oldest-first):
  id = alloc.Next()
  read = 1 if rekey leftover, or if initial_read_before IS NOT NULL AND published_at < initial_read_before
  INSERT INTO items(id, feed_id, uid, url, title, author, image_url, word_count, content_hash, text_hash,
                    published_at, updated_at, sort_at, read, read_at)
    -- published_at = PublishedParsed ?? UpdatedParsed ?? id/1e6; sort_at = min(published_at, id/1e6 + 86400)
  INSERT INTO item_content(item_id, content_html, content_text, enclosures_json)     -- FTS insert fires here
INSERT INTO item_fulltext(...) for inline extractions (keyed by uid → new id)
UPDATE trimmed_items SET last_seen_at = :now WHERE feed_id = :f AND uid IN (SELECT value FROM json_each(:seen_tomb))
retention trim (§5), with :first_new_id
churn check: IF new ≥ 10 AND new ≥ 0.8 × document size AND the feed had ≥ 20 items before
             → note 'guid_churn_suspected' (keep = 1; items stay unread; the health view offers "mark this fetch read")
UPDATE feeds SET etag, last_modified, body_hash, title = :doc_title, site_url = COALESCE(:site,''),
       description = COALESCE(:desc,''),
       custom_title = CASE WHEN last_success_at IS NULL AND custom_title = :doc_title THEN NULL ELSE custom_title END,
       initial_read_before = NULL,
       last_fetch_at, last_success_at, last_status, consecutive_failures = 0,
       next_fetch_at, current_delay_s, ttl_hint_s,
       last_new_items_at = CASE WHEN :new > 0 THEN :now ELSE last_new_items_at END,
       redirect_* per §4.7, updated_at = :now WHERE id = :f
INSERT INTO settings('sys.id_high_water', :alloc_last) ON CONFLICT … (monotonic, §3)   -- when ids were allocated
INSERT INTO fetch_log(... first_item_id, last_item_id, keep ...);
DELETE FROM fetch_log WHERE feed_id = :f AND keep = 0
  AND started_at < :now - 14*86400
  AND id < (SELECT id FROM fetch_log WHERE feed_id = :f ORDER BY id DESC LIMIT 1 OFFSET 49)   -- NULL (< 50 rows) deletes nothing
  AND id NOT IN (SELECT id FROM fetch_log WHERE feed_id = :f AND outcome = 'error' ORDER BY id DESC LIMIT 10);
COMMIT
```

The `unchanged` and `not_modified` outcomes run the same transaction without the item section: the trim, the feed bookkeeping, and the fetch_log row and cap.

On the **first fetch** of a new subscription, every item is inserted unread (or read, per `initial_read_before`), and the trim then applies N. Items trimmed in this same transaction are not added to `trimmed_unread_count`, because their ids are ≥ `first_new_id`.

A dedup-mode change through the UI or API sets `rekey_pending = 1` and enqueues a priority fetch. The UI confirm dialog says: "items that cannot be matched will be marked read".

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
- **Per-feed refresh** (health view, `POST /api/feeds/{id}/refresh[?full=1]`) is a priority job. `full=1` drops validators and the body-hash short-circuit for that attempt. The handler waits on its buffered reply channel, a 15 s timer, and the scheduler's `shutdownCh`, whichever fires first. After the timer it returns `202 {pending:true}`, and the late reply is dropped harmlessly. A `gone` feed must be re-enabled first.
- **Add feed in the web UI** (`POST /api/feeds`): synchronous discovery through the guarded client, insert with `next_fetch_at = now`, then a priority job (`trigger='subscribe'`). The handler waits up to 8 s the same way.
- **Add feed through the Reader API** (`quickadd`, `subscription/edit ac=subscribe`): see decision 33. By default there is no outbound HTTP. The feed is inserted with `next_fetch_at = now`, the dispatcher gets a non-blocking `wake`, and the call returns at once. With `greader.subscribe_fetch_now = true` (pending the owner), it behaves like the web UI path.
- **OPML import in the web UI** (`POST /api/opml`) creates an `import` run over the new feeds. **`subscription/import` through the Reader API** inserts the feeds with `next_fetch_at = now` and only sends `wake`; the scheduler fetches them as ordinary due feeds. Existing feeds are untouched in both cases.
- **Retention changes.** `PATCH /api/feeds/{id}` with a new `retention` enqueues a priority `trim_only` job for that feed. `PATCH /api/settings` with a new `retention.default` creates a `retention` run of `trim_only` jobs over the inheriting feeds. "Apply retention now" (`POST /api/retention/apply`) does the same over every feed. These are user actions, never page loads.
- **API clients never reach any of this**, except for the inserts with `next_fetch_at = now` described above (decision 33). No Reader endpoint touches `next_fetch_at` of an existing feed or the queues.

### 4.10 SSE streaming and shutdown

- **`GET /api/events`** (session-authenticated):
  - Headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache, no-transform`, `X-Accel-Buffering: no`, no `Content-Length`. The handler calls `http.Flusher` after every event.
  - A `: ping` comment goes out every 15 s, well under Cloudflare's 125 s proxy read timeout.
  - Each subscriber has a buffered channel of 64. When it overflows, the hub drops the channel and sends one `resync` event.
  - The handler holds no database connection while waiting. Event ids are monotonic, and `Last-Event-ID` replays from a 500-event ring buffer.
  - Event list in §7.3.
- **Graceful shutdown.** `signal.NotifyContext(SIGINT, SIGTERM)`, then:
  1. `sched.Stop()`:
     - stop the ticker and close `shutdownCh`, so every handler waiting on a reply returns 503 (UI) at once;
     - refuse new runs and priority jobs;
     - cancel the fetch context so in-flight HTTP aborts;
     - close `jobs`.

     Workers finish the job in hand: a completed fetch commits under `WithoutCancel` with a 10 s deadline, and an aborted one writes nothing. Each worker then sends `workerExit`.
  2. `hub.Close()` closes every subscriber channel, so SSE handlers return immediately.
  3. `http.Server.Shutdown` with a 10 s context. No handler waits on the scheduler any more.
  4. `<-sched.Stopped()`. The dispatcher keeps receiving on `doneCh` until `live == 0`, so no worker can block on its final send.
  5. `maint.Stop()` cancels the maintenance context. An in-progress `VACUUM INTO` is interrupted, and its tmp file is removed on the next run.
  6. `PRAGMA wal_checkpoint(TRUNCATE)` through `WithWrite`.
  7. Close all pools.

  Compose sets `stop_grace_period: 30s`. The shutdown test asserts exit in under 15 s during a 138-feed run.
- **Startup.** Feeds that are already past due are simply due on the first tick. A full catch-up of 138 feeds takes about 30 s.

---

## 5. Retention, the trimmed ledger and restore

**Setting.**

- `settings.retention.default` is one of {50, 100, 250, 500, 1000, 0}, where 0 means unlimited. The default is **250**.
- `feeds.retention` uses the same values; NULL means inherit. The archive feed is fixed at 0.
- `N = COALESCE(feeds.retention, default)`. When `N = 0` the trim is skipped.
- N counts **non-starred, non-held** items only. "Keep newest N" means the N newest unstarred items, plus every starred item, plus items whose `retain_until` is still in the future.
- `settings.retention.restore_days` (0–365, default **90**) is how long a restore stub is kept.

**When.**

- In every successful fetch commit: `ok`, `unchanged` and `not_modified`.
- In `trim_only` jobs: a per-feed retention PATCH, a `retention.default` change, or "Apply retention now".
- Never on page load, never on Reader API traffic, never on a timer.

**Exact SQL** (inside the fetch or `trim_only` transaction; validated):

```sql
CREATE TEMP TABLE IF NOT EXISTS trim_set (id INTEGER PRIMARY KEY) STRICT;
DELETE FROM temp.trim_set;
INSERT INTO temp.trim_set(id)
  SELECT id FROM items
  WHERE feed_id = :feed AND starred = 0 AND (retain_until IS NULL OR retain_until <= :now)
  ORDER BY sort_at DESC, id DESC
  LIMIT -1 OFFSET :N;

INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
  SELECT i.id, i.feed_id, i.uid, i.read, :now, :now
  FROM temp.trim_set t JOIN items i ON i.id = t.id
  WHERE true                                   -- required before ON CONFLICT with INSERT … SELECT … JOIN
ON CONFLICT (feed_id, uid) DO UPDATE SET
  id = excluded.id, read = excluded.read, trimmed_at = excluded.trimmed_at, last_seen_at = excluded.last_seen_at;

INSERT INTO trimmed_content (id, published_at, updated_at, sort_at, word_count, content_hash, text_hash,
                             url, title, author, image_url, origin_title, fulltext_mode,
                             content_html, content_text, enclosures_json)
  SELECT i.id, i.published_at, i.updated_at, i.sort_at, i.word_count, i.content_hash, i.text_hash,
         i.url, i.title, i.author, i.image_url, i.origin_title, i.fulltext_mode,
         c.content_html, c.content_text, c.enclosures_json
  FROM temp.trim_set t JOIN items i ON i.id = t.id JOIN item_content c ON c.item_id = i.id
  WHERE :restore_days > 0
ON CONFLICT (id) DO NOTHING;

UPDATE feeds SET trimmed_unread_count = trimmed_unread_count +
  (SELECT count(*) FROM temp.trim_set t JOIN items i ON i.id = t.id WHERE i.read = 0 AND i.id < :first_new_id)
WHERE id = :feed;

DELETE FROM items WHERE id IN (SELECT id FROM temp.trim_set);
```

- `changes()` from the DELETE goes to `fetch_log.trimmed_items`.
- The FTS rows go via `items_fts_bd`. `item_content` and `item_fulltext` go by cascade.
- `stats_events` is never touched.

Validated with 300 items in one feed, 5 of them starred, and N=50: 245 trimmed, 55 kept (50 unstarred plus 5 starred), 245 ledger rows and 245 stubs. FTS `integrity-check` is clean.

**Starred exemption.** It is enforced by `starred = 0` in the selection. Unstarring makes an item eligible at the next trim.

**Ranking.** `sort_at` DESC, then `id` DESC: the same order as the UI.

- A backfilled or republished old-dated post can be trimmed in the same transaction that inserted it. It goes to the ledger (and a stub) but is not counted in `trimmed_unread_count`.
- A bogus future date is clamped to crawl + 24 h.

**Restore** (`store.restoreTrimmed(tx, ids, mode)`, where `mode` is `star` or `unread`; validated):

```sql
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
  WHERE t.id IN (SELECT value FROM json_each(:ids))
ON CONFLICT DO NOTHING;
INSERT INTO item_content (item_id, content_html, content_text, enclosures_json)
  SELECT c.id, c.content_html, c.content_text, c.enclosures_json FROM trimmed_content c
  WHERE c.id IN (SELECT value FROM json_each(:ids)) AND EXISTS (SELECT 1 FROM items i WHERE i.id = c.id)
ON CONFLICT DO NOTHING;                                        -- FTS row comes back via the trigger
DELETE FROM trimmed_items WHERE id IN (SELECT value FROM json_each(:ids)) AND id IN (SELECT id FROM items);
                                                               -- cascades the stub
```

- A restored item keeps its original id, uid and `sort_at`. It re-enters every list it qualifies for.
- A restored starred item is exempt from trimming as long as it stays starred. A mark-unread restore is held for 7 days.
- Restores publish `items.state` over SSE.
- Ledger ids without a stub (older than `restore_days`) cannot be restored: a star is ignored, and a mark-unread only updates the ledger `read` flag (§11).

**Ledger writes elsewhere.**

- **Ingest.** A uid found in `trimmed_items` for the feed is never re-inserted. Its `last_seen_at` is bumped.
- **`edit-tag`, and web `mark-read` with `read:true`.** `UPDATE trimmed_items SET read = 1 WHERE id IN (…) AND read = 0` (silent).
- **`edit-tag r=read`, and web mark-unread.** Restore with `mode='unread'` where a stub exists. Otherwise `UPDATE trimmed_items SET read = 0 …`.
- **`edit-tag a=starred`, and web star.** Restore with `mode='star'` where a stub exists. Otherwise ignored.
- **`mark-all-as-read`.** `UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts_us [AND feed_id = :f | AND feed_id IN (folder feeds)]`. It is **skipped entirely for the starred scope**, because starred items are never in the ledger.
- **Unsubscribe.** FK cascade (the ledger and stubs of the deleted feed go with it; starred items were moved to the archive first, §6.9).
- **Nightly purge.** Stubs after `restore_days`. Ledger rows 180 days after their uid was last seen in the feed document.

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

---

## 6. Google Reader API mapping

### 6.1 Routing

- **Front handler** (`internal/greader.Front`), installed in `main` ahead of the root ServeMux:
  1. Take `r.URL.Path` and collapse runs of `/` into one.
  2. Strip any number of leading `/api/greader.php` prefixes; remember whether one was present.
  3. If the remainder is `""`, `/`, `/accounts/ClientLogin`, `/check/compatibility`, `/icon/…`, or starts with `/reader/api/0/`, **and** either a prefix was present or the remainder is one of the root forms (`/accounts/ClientLogin`, `/reader/api/0/…`), dispatch to the Reader handler with the cleaned path.
  4. Otherwise pass the request to the root ServeMux.

  The Reader handler dispatches with a `switch` on method plus cleaned path, and a `strings.HasPrefix` for `/reader/api/0/stream/contents/`. **No ServeMux ever sees a Reader path**, so ServeMux's automatic 301 for `//` paths, which URLSession would follow as a bodiless GET, cannot occur. Contract tests cover `/api/greader.php//reader/api/0/edit-tag`, a trailing-slash base, and a doubled prefix.
- **Mount scope.** `/api/greader.php…` is the Access-bypassed public path. The root forms (`/accounts/ClientLogin`, `/reader/api/0/…`) exist for Reeder's generic "Reader" type on the LAN. They sit behind Access on the tunnel and must never get their own bypass.
- **Probes.** `GET ""` or `/` under the prefix returns `200 text/plain "OK"`. `/check/compatibility` returns `PASS`.
- **Unknown paths.** An unknown `/reader/api/0/*` after successful auth returns `200 application/json []` with a WARN log.
- **Response headers.** Every response carries `Cache-Control: private, no-cache`. JSON is `application/json; charset=utf-8`, arrays are `[]`, and **no value is ever JSON `null`** except `LSID` in JSON ClientLogin (decision 32). OK, token and ClientLogin bodies are `text/plain; charset=utf-8`. Go emits the `Date` header, which NNW uses for its next `ot`. No cookies are ever set.
- **Status policy.** Never 429, never a redirect, never 5xx for client data. A 500 only happens on a genuine database failure; NNW re-queues and retries.
- **Logging.** The request log records method, path, query, UA, Content-Type, status and duration, with `Passwd`, `T` and `Authorization` redacted. With `KIPPLE_LOG_GREADER_FORMS=1` it also logs form keys and value counts. A write endpoint (`edit-tag`, `mark-all-as-read`, `stream/items/contents`) that parsed zero ids from a non-empty body always logs a WARN with UA and Content-Type.

### 6.2 Form parsing (`greader/form.go`)

Reader routes never call `r.ParseForm`.

1. Parse `Content-Type` with `mime.ParseMediaType`, ignoring parameters (`; charset=utf-8`) and treating an unparseable header as absent.
   - `multipart/form-data` (Readrops ClientLogin) goes through `r.MultipartReader`.
   - `subscription/import` reads the raw body as OPML.
   - For every other POST, whatever the media type (including absent, `text/plain`, `application/octet-stream`), the body is read with a 4 MiB cap and parsed as urlencoded. A 1000-id NNW edit-tag is about 60 KB.
2. Split on `&` only. Split each pair on the **first** `=`: NNW leaves `=`, `;`, `,` and `$` unencoded in values. A pair with no `=` becomes `key → ""`.
3. `url.QueryUnescape` both sides, so `+` becomes a space. On an unescape error, keep the raw text.
4. Keep repeated keys in order in a `map[string][]string`, **and** keep the raw body string and each pair's raw text.
5. Parse the query string the same way.
6. `p.Get(k)` checks the body first, then the query. `p.All(k)` returns body values followed by query values. `p.RawBody()` returns the undecoded body.

**Label lookup** (`subscription/edit a=`/`r=`, `rename-tag s=`, `disable-tag s=`, label streams). Take the text after `user/<x>/label/`. Match it against `folders.name COLLATE NOCASE`, trying in order:

1. the decoded value;
2. the raw value (no unescaping);
3. the raw value through `url.PathUnescape` (which keeps `+`).

**`disable-tag` raw fallback.** NNW sends `T=<token>&s=<raw tag id>`, with no re-encoding and `s` last. If no candidate matches, the handler takes the raw body text from the first `s=` up to the next `&T=`, or to the end of the body, and retries the three forms. So `News & Politics+`, split by `&` into `s=user/-/label/News ` and a stray ` Politics+` key, is still found.

### 6.3 Auth and tokens

- **ClientLogin (POST, or GET for FeedMe).**
  - `Email` and `Passwd` come from the body or the query. `Email` must match `account.username` case-insensitively. A NULL `api_password_hash` means the API is disabled and every attempt fails.
  - **Verification** (`internal/auth.Verifier`, shared with the web login):
    1. If `hmac.Equal(HMAC(secret, "login|"+Passwd), memo)`, succeed without hashing.
    2. Otherwise acquire the global semaphore of 1, waiting at most 5 s (else 401). Run argon2id (m=19 MiB, t=2, p=1; about 50 ms and 19 MiB on Host-A), then release. On success, set the memo.
    3. The memo is cleared whenever either password changes.
  - **Per-IP failure delay.** The client IP is `CF-Connecting-IP` when `RemoteAddr` is in `KIPPLE_TRUSTED_PROXY_IPS`, else `RemoteAddr`. The password is **always** verified.
    - A success clears that IP's failure count and returns at once.
    - A failure is counted in a fixed 10-minute window. Once the window holds 5 or more failures, each further failing response is delayed 2 s.
    - A stale device retrying an old password therefore never blocks the correct password from the same home IP.
    - Never 429.
  - **Failure:** `401 text/plain "Error=BadAuthentication\n"`.
  - **Success:** `text/plain "SID=<tok>\nLSID=null\nAuth=<tok>\n"`. With `output=json`: `{"SID":…,"LSID":null,"Auth":…}`.
  - `tok = username + "/" + hex(HMAC-SHA256(secret, "greader-token-v1|" + api_password_hash))`. It is 64 hex after the slash and never contains `=`. It is cached in an `atomic.Pointer` and recomputed when the API password changes, which revokes every client.
- **Every other route.**
  - `Authorization: GoogleLogin auth=<tok>`: split with `strings.Fields` into exactly 2 fields; the second must start with `auth=`; compare with `hmac.Equal`.
  - On POST, a `T=<tok>` in the body or query is an equally valid credential.
  - **Failure:** `401 text/plain "Unauthorized!"` with **both** `X-Reader-Google-Bad-Token: true` and `Google-Bad-Token: true`.
- **`GET /reader/api/0/token`** returns `text/plain tok + "\n"`.
- **The T check on write endpoints:**
  - If the Authorization header authenticated the request, T must be `tok`, `""` or `x`. Any other T returns 401.
  - If there is no header, T must equal `tok`, else 401. Never 400.
- **Client family.** The `User-Agent` prefix classifies the client: `Reeder` → reeder, `NetNewsWire` → netnewswire, `Unread` → unread, anything else → api. The family goes into the request context for stats, and into greader's mutex-guarded last-seen map, updated at most once a minute per family.

### 6.4 Stream grammar (`greader/stream.go`)

| Stream id | Predicate |
|---|---|
| `user/<x>/state/com.google/reading-list` | none |
| `…/starred` | `starred = 1` |
| `…/read` | `read = 1` |
| `…/unread` | `read = 0` |
| `…/kept-unread` | `read = 0` |
| `…/broadcast`, `…/like` | always empty |
| `user/<x>/label/<name>` | `feed_id IN (SELECT id FROM feeds WHERE folder_id = <label lookup, §6.2>)` |
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

### 6.6 stream/items/contents (POST; GET tolerated) and stream/contents

- **Id cap.** `i=` values beyond the first 1000 are ignored with a WARN. NNW sends 150 and Reeder 100.
- **Streaming.** The handler writes the envelope prefix, then scans rows one at a time and encodes each item straight into a 32 KB `bufio.Writer`, then writes `]}`. The full `[]item` is never built. The read transaction is short, because the rows are local and the writer only blocks on a full socket buffer; a stalled client is cut off by the server `WriteTimeout` of 60 s.

```sql
SELECT i.id, i.feed_id, i.url, i.title, i.author, c.content_html, i.published_at, i.updated_at,
       i.read, i.starred, c.enclosures_json, i.fulltext_mode, i.origin_title,
       COALESCE(f.custom_title, f.title) AS feed_title, f.site_url, f.fulltext AS feed_fulltext,
       fo.name AS folder_name, ft.content_html AS fulltext_html
FROM items i JOIN item_content c ON c.item_id = i.id
JOIN feeds f ON f.id = i.feed_id JOIN folders fo ON fo.id = f.folder_id
LEFT JOIN item_fulltext ft ON ft.item_id = i.id
WHERE i.id IN (SELECT value FROM json_each(:ids_json))
ORDER BY i.id DESC;                                   -- ASC when r=o
```

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
 "categories":["user/-/state/com.google/reading-list","user/-/label/<folder>",
               "user/-/state/com.google/read"?,"user/-/state/com.google/starred"?],
 "origin":{"streamId":"feed/<feed_id>","title":"<COALESCE(origin_title, feed title)>","htmlUrl":"<site_url or ''>"},
 "enclosure":[{"href":u,"url":u,"type":t,"length":n}]}
```

Rules for the item fields:

- **Content.** `content` is `fulltext_html` when `COALESCE(fulltext_mode, feed_fulltext) = 1` and an extraction exists; otherwise `content_html`. Both carry **absolute original** URLs (decision 31). The body is cut UTF-8-safely at 500,000 bytes.
- **Strings, never null.** `author`, `title`, `alternate[].href`, `canonical[].href` and `origin.htmlUrl` are always strings, possibly `""`. `enclosure` is omitted when there are none.
- **Titles are plain JSON strings.** There is no fullwidth `＆＜＞` escaping.
- **Required fields.** `summary`, `categories` and `origin` are always present. `origin.streamId` always equals the subscription id (for archived items, the archive feed's id).
- **Ids.** Trimmed and unknown ids are absent.

**stream/contents.** `GET /reader/api/0/stream/contents[/<stream>]` (or `?s=`) runs the §6.5 filter with `n` capped at 1000, then streams contents the same way. The envelope `id` is the requested stream, with a continuation as in §6.5.

### 6.7 edit-tag (POST)

`ids = p.All("i")` parsed per §3; `a = p.All("a")`; `r = p.All("r")`. It runs as one `WithWrite` transaction:

| Tag | SQL (then ledger handling, §5) |
|---|---|
| `a=read`, `r=kept-unread` | `UPDATE items SET read=1, read_at=:now WHERE id IN (SELECT value FROM json_each(:ids)) AND read=0 RETURNING id, feed_id`; ledger `read=1` for the rest |
| `r=read`, `a=kept-unread` | `UPDATE items SET read=0, read_at=NULL WHERE id IN (…) AND read=1 RETURNING id, feed_id`; ids not in `items` → restore `mode='unread'` or ledger `read=0` |
| `a=starred` | `UPDATE items SET starred=1, starred_at=:now WHERE id IN (…) AND starred=0 RETURNING id, feed_id`; ids not in `items` → restore `mode='star'` |
| `r=starred` | `UPDATE items SET starred=0, starred_at=NULL WHERE id IN (…) AND starred=1 RETURNING id, feed_id` |
| labels, `broadcast`, `like`, `tracking-*`, unknown | ignored |

- The `WHERE` guards make replays and NNW's four-pass retries no-ops.
- `RETURNING` rows are fully scanned and closed inside the store helper.
- The changed ids feed the SSE `items.state` event after commit. Stats rules (§8) run inside the same transaction through `Recorder.Record(tx, …)`.
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
| read, unread, unknown | no-op, `OK` |

`ts` is normalized per §3. When it is absent, it is the committed `max(id)` from a reader snapshot.

```sql
UPDATE items SET read = 1, read_at = :now WHERE read = 0 AND id <= :ts_us [AND scope];
-- plans: idx_items_unread (id<?) or idx_items_unread_feed (feed_id=? AND id<?)
UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts_us [AND feed scope];   -- not for starred
```

On the narrow table each changed row rewrites one small cell. This never produces stats. The response is `OK`.

### 6.9 Subscriptions, folders, misc

**`GET subscription/list`.** `output` is ignored; JSON is always returned.

```sql
SELECT f.id, COALESCE(f.custom_title, f.title), f.url, f.site_url, fo.name, fi.hash
FROM feeds f JOIN folders fo ON fo.id = f.folder_id
LEFT JOIN feed_icons fi ON fi.feed_id = f.id
WHERE f.disabled_reason IS NOT 'archive' OR EXISTS (SELECT 1 FROM items WHERE feed_id = f.id)
ORDER BY fo.position, fo.name, f.position, COALESCE(f.custom_title, f.title);
```

It returns `{"subscriptions":[{"id":"feed/12","title":…,"categories":[{"id":"user/-/label/Comics","label":"Comics"}],"url":…,"htmlUrl":"<site_url or ''>","iconUrl":"<icon url or ''>"}]}`.

- There is always exactly one category per feed.
- Disabled and gone feeds are listed. The archive feed is listed while it holds items.
- `iconUrl` is **always present**. It is `<KIPPLE_PUBLIC_URL>/api/greader.php/icon/<id>-<hash>` when the `greader.icon_urls` setting is on and an icon exists, otherwise `""`.
- **ETag:** `"` + the first 16 hex of `sha256(body)` + `"`. An `If-None-Match` match (after stripping `W/`) returns `304` with an empty body.

**`GET tag/list`.** Returns `{"tags":[{"id":"user/-/state/com.google/starred"},{"id":"user/-/state/com.google/reading-list"},{"id":"user/-/label/<name>","type":"folder"},…]}` from `SELECT name FROM folders ORDER BY position, name`, with the same body-hash ETag. `types=1` is ignored.

**`POST subscription/quickadd`.** T is optional when the header is valid.

1. The `quickadd=` value has a leading `feed/` stripped.
2. **Idempotent.** If `FindFeedByURL(url)` matches (http/https and pre-migration URLs included), reply `200 {"numResults":1,"query":url,"streamId":"feed/<id>","streamName":<display title>}` with the **existing** feed. NNW's follow-up re-list then finds it.
3. Otherwise, by default (decision 33): validate the URL (http/https, a host, not an SSRF-literal IP unless allowed), then insert the feed. The fields are `folder_id=1`, `title` = host, `url`, `url_key`, `host`, and `next_fetch_at = now`. Send a non-blocking `wake`.
4. Reply `{"numResults":1,"query":url,"streamId":"feed/<id>","streamName":title}`. The subscription/list ETag changes automatically.
5. With `greader.subscribe_fetch_now = true`, step 3 runs guarded discovery and a priority first fetch, waiting up to 8 s, as in revision 1.

On an invalid URL the reply is `200 {"numResults":0,"error":"<msg>"}`.

**`POST subscription/edit`.**

- `ac=subscribe`: `s=feed/<url>` is repeatable. `t` values are index-aligned with `s`. `a=user/-/label/<name>` puts the feed in that folder, creating the folder if missing. Each `s` goes through the idempotent quickadd logic; an existing feed is moved or renamed only if `a`/`t` were given.
- `ac=unsubscribe` (**archive, decision 24**), in one transaction:
  1. If the feed is the archive feed, `DELETE` it (a real delete).
  2. Otherwise, if it has starred items:
     - create the archive feed if missing: `url='kipple:archive'`, `url_key='kipple:archive'`, `host='kipple.invalid'`, title "Unsubscribed (starred)", `enabled=0`, `disabled_reason='archive'`, `retention=0`, folder 1;
     - `UPDATE items SET feed_id = :archive, uid = 'a' || :f || ':' || uid, origin_title = COALESCE(origin_title, :feed_title) WHERE feed_id = :f AND starred = 1`.
  3. `DELETE FROM feeds WHERE id = :f`. The cascade covers the remaining items (and FTS via trigger), ledger, stubs, log, icons and full text. Stats survive.
- `ac=edit`:
  - a non-empty `t` → `custom_title`;
  - an `a=` label → move to that folder, created if missing;
  - `r=` without `a=` → move to folder 1.
- Unknown feeds still get `OK`.

**`POST rename-tag`** (`s=user/-/label/<old>`, `dest=user/-/label/<new>`): the label lookup (§6.2) finds the old folder. `UPDATE folders SET name = ?`. If the new name already exists, the folders are merged (feeds moved, old folder deleted). The default folder may be renamed. Response `OK`.

**`POST disable-tag`** (`s` repeatable): the label lookup with the raw-body fallback (§6.2), then `UPDATE feeds SET folder_id = 1 WHERE folder_id = ?` and `DELETE FROM folders WHERE id = ? AND is_default = 0`. Response `OK`.

**`POST subscription/import`**:

1. Parse the raw OPML body (no T needed; NNW requires exactly 200).
2. Flatten nested folders.
3. Match each outline through `FindFeedByURL`. Matches keep their state and folder. New feeds get their OPML title in `custom_title` and their OPML position, with `next_fetch_at = now`.
4. Send `wake` (no run, no synchronous HTTP; decision 33).
5. Reply `200 OK`.

**`GET subscription/export`** returns the OPML attachment from the same exporter as the UI (§7.6).

**`GET user-info`** returns `{"userId":"1","userName":u,"userProfileId":"1","userEmail":u}`. Reeder calls it right after ClientLogin.

**`GET unread-count`** runs `SELECT feed_id, count(*), max(id) FROM items WHERE read = 0 GROUP BY feed_id` (covering on `idx_items_unread_feed`) and rolls the results up in Go. The response is `{"max":<total>,"unreadcounts":[{"id":"feed/12","count":3,"newestItemTimestampUsec":"…"},…]}`.

**`GET /api/greader.php/icon/<id>-<hash>`** is unauthenticated and exists only when `greader.icon_urls` is on. It serves `feed_icons` bytes and nothing else.

**Parameters ignored everywhere:** `output` (except on ClientLogin), `ck`, `client`, `includeAllDirectStreamIds`, `merge`, `likes`, `comments`, `mediaRss`, `types`.

---

## 7. Web UI API surface (phase 2)

Everything under `/api/` except the Reader paths requires the `kipple_session` cookie. Details of the cookie:

- It is HttpOnly, `SameSite=Lax`, `Path=/`, persistent with `Max-Age` 30 days and sliding (`last_seen_at` updated at most hourly).
- It is `Secure` when the effective scheme is https: `X-Forwarded-Proto: https` from a trusted proxy IP, or real TLS.
- It is persistent, not a session cookie, because WebKit bug 272325 drops session cookies in Home Screen apps.

**Same-origin enforcement** (`internal/httpx/csrf.go`, every cookie-authenticated request whose method is not GET/HEAD, plus `GET /api/opml` and `GET /api/stats/export.csv`):

1. If `Sec-Fetch-Site` is present, it must be `same-origin`.
2. Else `Origin` must be present and equal `scheme://host` of the request, using the effective scheme.
3. The request must also carry `X-Kipple-Client: web|pwa`, which makes a cross-origin `fetch` non-simple and forces a CORS preflight that Kipple never answers. `POST /api/stats/events` is exempt from this header only, because `sendBeacon` cannot set headers; it still needs rule 1 or 2.
4. Any failure returns `403 {"error":"origin"}`.

Other conventions:

- Unauthenticated calls get `401 {"error":"auth"}`.
- The UI JSON API never accepts a Reader token, and the Reader API never accepts the cookie.
- `X-Kipple-Client` doubles as the stats attribution (`pwa` when `display-mode: standalone` matches).
- **Ids are strings** in every JSON body.
- Time values are unix seconds.

### 7.1 Endpoints

| Method and path | Request | Response |
|---|---|---|
| `POST /api/auth/login` | `{username, password}` (same Verifier and failure delay as ClientLogin) | `204` + cookie |
| `POST /api/auth/logout` | — | `204` |
| `GET /api/auth/me` | — | `{username, api_enabled}` |
| `GET /api/bootstrap` | — | `{user, settings:{…merged defaults…}, folders:[{id,name,position,is_default,unread}], feeds:[{id,folder_id,title,site_url,icon:"/api/feeds/<id>/icon?h=<hash>",unread,status,fulltext,retention,interval_minutes,is_archive}], counts:{unread,starred}, runs:[{…}], warnings:[…], server_time, version}`. `warnings` carries the clock banner, a stale snapshot, and "unread total above 10,000: Reeder only syncs the newest 10,000 unread ids" |
| `GET /api/items` | `view=unread\|all\|starred`, `feed=<id>` \| `folder=<id>`, `q=<search>`, `order=date\|rank`, `cursor=<opaque>`, `limit≤100`, or `ids=a,b,c` (SSE catch-up) | `{items:[Card], next_cursor\|null}`. Card = `{id, feed_id, title, url, author, excerpt(≤280 chars of content_text), image(proxied or null), published_at, sort_at, read, starred, word_count, reading_minutes(ceil(words/230), null under 100 words), snippet?}`. The cursor is base64 of `sort_at.id`; keyset `(sort_at,id) < (?,?)` |
| `GET /api/items/{id}` | — (prefetch-safe, never a stat) | `{…Card, content_html(image-proxied), fulltext:{mode, effective, available, error\|null}, enclosures:[…], feed:{id,title,site_url}}`. For a ledger id with a stub: the stub content with `trimmed:true` (star and mark-unread restore it). Otherwise 404 |
| `POST /api/items/{id}/open` | `{via:"tap"\|"key"\|"nav"}` | In one `WithWrite`: `store.SetRead` (no stats side effect), then `Recorder.Record(tx, open)`. Returns `{session_key, item:<as GET>}` |
| `PUT /api/items/{id}/star` | `{starred: bool}` | `{starred, restored}`. A star on a ledger id restores it (§5). Records `star`/`unstar` in the same transaction only when `RETURNING` shows a change |
| `POST /api/items/mark-read` | `{ids:[…]}` or `{scope:{feed_id\|folder_id\|all:true, view}, max_id}`, plus `read: bool`, `reason: "swipe"\|"key"\|"scroll"\|"bulk"` | `{changed:[ids], restored:[ids]}`. `max_id` bounds the scope. `read:false` on ledger ids restores them (§5). `reason` is only logged. **No stats code path** |
| `POST /api/items/{id}/fulltext` | `{mode: 1\|0\|null}` (omit to keep the mode), `?refresh=1` | Sets `items.fulltext_mode`. If the effective mode is 1 and no successful extraction exists (or `refresh=1`), it extracts synchronously (15 s budget, guarded client, go-readabilityV2, absolutized against the article URL, the same sanitize policy) and stores `item_fulltext`. Returns `{mode, effective, status:"ok"\|"error"\|"skipped", content_html(proxied), word_count, error}` |
| `GET /api/feeds/{id}/icon` | — | Icon bytes. `Cache-Control: private, max-age=604800` |
| `POST /api/feeds` | `{url, folder_id?, title?}` | `FindFeedByURL` first: an existing match returns `{status:"exists", feed}`. Else `{status:"choose", candidates:[{url,title,type}]}` when discovery finds several; else creates the feed, waits ≤ 8 s for the first fetch, and returns `{status:"ok", feed}` |
| `PATCH /api/feeds/{id}` | Any of `custom_title, folder_id, position, interval_minutes, retention, fulltext, dedup_mode, user_agent, http_auth, ignore_http_cache, disable_http2, allow_insecure_tls, allow_private_net, enabled` | `feed`. A `dedup_mode` change sets `rekey_pending`. `enabled:true` resets failures and sets `next_fetch_at = now`. A `retention` change enqueues a `trim_only` job |
| `DELETE /api/feeds/{id}` | `?delete_starred=1` | `204`. By default starred items move to the archive feed (§6.9). The confirm dialog shows `starred_count` from the feed object |
| `POST /api/feeds/{id}/refresh` | `?full=1` | Waits ≤ 15 s: `{outcome, new_items, error_class, error}`, or `202 {pending:true}` |
| `POST /api/feeds/{id}/mark-fetch-read` | `{fetch_log_id}` | Marks read the ids in that row's `[first_item_id,last_item_id]` range for this feed. Bulk, no stats. `{changed}` |
| `POST /api/feeds/{id}/trimmed-unread/reset` | — | Sets `trimmed_unread_count = 0`, `trimmed_unread_since = now`. `204` |
| `POST /api/archive/purge-unstarred` | — | Deletes unstarred items of the archive feed. `{deleted}` |
| `POST/PATCH/DELETE /api/folders[/{id}]` | `{name, position}` | Folder or `204`. Deleting a folder moves its feeds to the default |
| `POST /api/refresh` | — | `202 {run_id, total, joined}` |
| `GET /api/status` | — | `{runs:[{id,kind,done,total,new_items,errors}], inflight, unread_total}`. The SSE fallback |
| `GET /api/events` | `Last-Event-ID` | SSE (§7.3) |
| `GET /api/health/feeds` | — | `{feeds:[{id,title,url,url_original,status,enabled,disabled_reason,last_success_at,last_fetch_at,last_error_at,last_error_class,last_error,last_status,consecutive_failures,current_delay_s,next_fetch_at,redirect_to,redirect_kind,redirect_count,last_new_items_at,trimmed_unread_count,trimmed_unread_since,host_throttled_until}], clients:[{family,last_seen_at}], snapshot:{last_at,last_error}, clock:{ahead_s}, unread_total}` |
| `GET /api/health/feeds/{id}/log` | — | The feed's fetch_log rows (14 days, plus kept rows) |
| `GET /api/settings`, `PATCH /api/settings` | `{key: value, …}` (whitelisted user keys, validated) | Merged settings. A `retention.default` change starts a `retention` run |
| `POST /api/account/password`, `POST /api/account/api-password` | `{current, new}` or `{current, generate:true}` | `204`, or `{api_password}` once when generated. Changing the API password revokes the Reader token and clears the login memo |
| `POST /api/opml` | Raw OPML or multipart; `?mark_read_older_than_days=N` (1–365, optional) | `{folders_created, feeds_added, feeds_existing:[{url, feed_id}], folders_merged_case:[…], memberships_dropped:[{url, kept, dropped:[…]}], run_id}` |
| `GET /api/opml` | — | OPML attachment (§7.6) |
| `POST /api/retention/apply` | — | `202 {run_id, total}` |
| `POST /api/stats/events` | `{events:[{kind:"read_time"\|"scroll"\|"open_original"\|"share", item_id, session_key?, value?}]}`. Accepted via `fetch` or `navigator.sendBeacon` (a JSON Blob) | `204`. Invalid events are dropped silently (§8) |
| `GET /api/stats/export.csv` | `?from=&to=` | RFC 4180 CSV of `stats_events`, paged by keyset (§8) |
| `GET /api/stats/summary` (phase 4) | `?from=&to=&include_inferred=0\|1` (default 0) | Aggregates (§8) |
| `POST /api/maintenance/fts-rebuild` | — | Runs `'rebuild'` on the writer. `204` |
| `GET /img/{sig}/{flags}/{b64url}` | — | Proxied image (§7.4) |
| `GET /` and SPA paths | — | Embedded `index.html` (`no-cache`, ETag). `/assets/*` is immutable. MIME types are registered for woff2, woff, ttf and webmanifest |

### 7.2 Read/star write rules in the UI API

Only two entry points can produce stats: `POST /api/items/{id}/open` and `PUT /api/items/{id}/star`. Swipe, keyboard `m`, mark-read-on-scroll, "mark above", mark-all, and folder or feed mark-all all go through `POST /api/items/mark-read`. That handler has no reference to the Recorder.

### 7.3 SSE events

Each event is `event: <type>`, `id: <seq>`, `data: <json>`.

| Event | Data |
|---|---|
| `run.start` | `{run_id, kind, total}` |
| `fetch.done` | `{feed_id, run_ids, trigger, outcome, new_items, new_item_ids (first 50), updated_items, trimmed_items, error_class, error, next_fetch_at}` |
| `run.progress` | `{run_id, done, total, new_items, errors}`, at most every 500 ms |
| `run.done` | `{run_id, new_items, errors}` |
| `items.state` | `{ids, read?, starred?, restored?, source: "web"\|"reeder"\|…}` |
| `counts` | `{unread_total, feeds: {id: unread}}`, coalesced to at most 1 per second |
| `feed.changed` | `{feed_id}`, on migration, disable, rename, move, archive or delete |
| `resync` | `{}`, on buffer overflow or failed replay |

The React app reconciles by item id. For `new_item_ids` inside the current view it calls `GET /api/items?ids=…`. If `EventSource` errors twice without a message, it polls `/api/status` every 2 s while a run is active, and every 60 s otherwise.

### 7.4 Image proxy (`internal/imgproxy`)

- **URL.** `/img/{sig}/{flags}/{u}`:
  - `u = base64url(originalURL)` without padding;
  - `flags` is a decimal bitset (bit 0 = `allow_private_net`, bit 1 = `allow_insecure_tls`) taken from the item's feed at rewrite time;
  - `sig = base64url(HMAC-SHA256(account.secret, "img-v1|" + flags + "|" + originalURL))[:22]`.

  The proxy checks `sig` with `hmac.Equal` **and** requires a web session. It uses the transport variant that `flags` selects.
- **Which images.** With `imgproxy.mode`: `http_only` (the default: `http://` sources only) or `all`. Stored URLs are always absolute (decision 31), so every image can be signed. Kipple's own `/img/` route can no longer collide with a relative `/img/foo.png`.
- **Rewrite.** At serve time only (`GET /api/items/{id}`, cards, the fulltext response), in `internal/sanitize`. It covers `img[src]`, every `srcset` candidate, `video[poster]`, the card `image`, and `picture > source[srcset]`. Stored HTML and Reader API output are never rewritten.
- **Fetch.** Through the SSRF-guarded client variant, with `Accept: image/*`, no cookies, no `Referer`, and a 15 s timeout.
- **Size.** If the upstream `Content-Length` is over 15 MiB, the proxy returns 502 before writing anything. Otherwise it streams through a `MaxBytesReader` of 15 MiB. On overflow mid-stream it aborts the response (`panic(http.ErrAbortHandler)`), so the browser sees a broken image and nothing is buffered. Only the first 512 bytes are held for sniffing.
- **Type check.** Allowed types are `image/jpeg`, `png`, `gif`, `webp` and `avif`. SVG is refused.
  - `http.DetectContentType` covers the first four.
  - AVIF is detected by an ISO-BMFF `ftyp` box with major or compatible brand `avif`/`avis`.
  - When sniffing is inconclusive, the upstream `Content-Type` is accepted only if it is on the allowed list.
- **Response headers.** Upstream `ETag` and `Last-Modified` are passed through; `If-None-Match` and `If-Modified-Since` are forwarded. `Cache-Control: private, max-age=2592000, immutable`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'`.
- **Concurrency.** At most 8 upstream image fetches run at once (a semaphore); the rest wait up to 10 s.
- **No disk cache in phase 2.**

### 7.5 Full-text rules

- **Effective mode** = `COALESCE(items.fulltext_mode, feeds.fulltext)`.
- **Ingest.** Inline extraction for full-text feeds, as in §4.3.
- **UI.** When the effective mode is 1 and `available=false`, the UI calls `POST /api/items/{id}/fulltext` with an empty body, which triggers extraction without changing the mode.
- **Per-article toggle.** In the article view the toggle sets `mode` to 1 or 0; "reset" sets `null`.
- **Failures.** A failed extraction is stored with `error`. The UI shows the feed content with a retry action, and the Reader API falls back to the feed content.
- **"Open original"** is a plain link (`target=_blank`, `rel=noopener noreferrer`). It also records `open_original` through `POST /api/stats/events`.

### 7.6 OPML import and export (`internal/opml`)

**Import** (UI and `subscription/import` share the parser):

1. Flatten nested outlines into single-level folders. The folder path is the nearest ancestor outline with no `xmlUrl`.
2. Assign `folders.position` and `feeds.position` from document order.
3. Match every `xmlUrl` through `FindFeedByURL`. Matches are reported as `feeds_existing` and left untouched.
4. A feed listed in several folders keeps the first. The others are reported in `memberships_dropped`.
5. Folder names that differ only by case are merged (the `COLLATE NOCASE` unique) and reported in `folders_merged_case`.
6. For a new feed, a non-empty `title`/`text` goes into `custom_title`, and `kipple:*` attributes are applied (validated like PATCH).
7. The UI option `mark_read_older_than_days=N` sets `initial_read_before = now − N·86400` on every new feed.
8. Values are never split on commas, and the `category` attribute is ignored.

**Export:**

- `<opml version="2.0" xmlns:kipple="https://kipple.invalid/opml/1">`, with nested `<outline text title>` folder outlines in folder position order.
- Each feed is `<outline type="rss" text title xmlUrl htmlUrl>`, where `text` and `title` = `COALESCE(custom_title, title)`.
- Optional attributes: `kipple:interval`, `kipple:retention`, `kipple:fulltext`, `kipple:dedup`, `kipple:user_agent`, `kipple:ignore_http_cache`, `kipple:disable_http2`, `kipple:allow_insecure_tls`, `kipple:allow_private_net`, `kipple:enabled`.
- Disabled and gone feeds are included; the archive feed is excluded. `http_auth` is never exported.
- `encoding/xml` escapes `&`, `<` and `"`. `;` and `,` are left as is.

A golden test checks that import → export → import is a fixed point for names, titles, order and overrides.

---

## 8. Stats model and write-path rules

**Table.** `stats_events` (§2.2).

- Append-only and never trimmed. Retention, cascades, archive moves and unsubscribe never touch it, and it has no FKs.
- Feed title, folder, item title and URL are snapshotted.
- `local_date`, `local_hour` and `local_weekday` are computed at write time in `settings.tz` (default `America/New_York`; `time/tzdata` is embedded).

**Kinds.** The CHECK constraint is the whitelist. There is no `read`, `mark_read` or `unread` kind.

| Kind | Written by | Value |
|---|---|---|
| `open` | Web `POST /api/items/{id}/open`; API: the single-id `edit-tag` inference **only when `stats.api_single_read_is_open` is on (default off)**, with `inferred=1` | — |
| `read_time` | Web only, via `/api/stats/events` with the `session_key` of an open | seconds |
| `scroll` | Web only, one per open (views take `MAX` per `session_key`) | 0–100 |
| `star`, `unstar` | Web star endpoint; API `edit-tag a/r=starred`, one row per changed id (restores included) | — |
| `open_original` | Web only | — |
| `share` | Web only, when `navigator.share` resolves or the clipboard fallback succeeds; not on `AbortError` | — |

**Enforcement is structural:**

1. `store.SetRead(tx, ids, read)`, `store.MarkScope(tx, scope, maxID)` and `store.MarkAllRead(tx, scope, ts)` take no client argument and cannot write stats. Every read-state path goes through them.
2. `stats.Recorder` is an interface whose only method is `Record(tx *sql.Tx, ev Event) error`. It is injected **only** into:
   - the web open handler;
   - the web star handler;
   - the stats ingest handler;
   - the greader `edit-tag` handler.

   `api.markRead`, the greader `mark-all-as-read` handler, the scheduler and the store never receive it. The test in §10 asserts this.
3. **API inference (off by default).** When `stats.api_single_read_is_open` is on, an `edit-tag` request produces an `open` with `client=<family>` and `inferred=1` only when all of the following hold:
   - it carries exactly **one** parseable id;
   - it has `a=user/-/state/com.google/read`;
   - `RETURNING` shows the row flipped from 0 to 1.

   The setting is turned on only after the day-1 `KIPPLE_LOG_GREADER_FORMS` capture shows Reeder's scroll marks arrive distinguishably from article opens (for example, batched versus single). Even then the rows stay `inferred=1`. Requests with two or more ids, `r=read`, and every `mark-all-as-read` never write anything.
4. **Ingest validation** (`POST /api/stats/events`) runs **inside one `WithWrite` transaction** together with the inserts, so overlapping flushes (the 15 s timer plus a `sendBeacon`) serialize on `BEGIN IMMEDIATE` and cannot both pass a cap:
   - `read_time` and `scroll` require a `session_key` that belongs to an `open` of the same item within the last 12 h.
   - Each `read_time` value must be 1–60. The cumulative `SUM(value)` per `session_key`, including the events accepted earlier in this batch, must stay ≤ the seconds elapsed since that open + 5, and ≤ 3600.
   - One `scroll` per `session_key` is kept: a later one replaces the value only if it is larger. `scroll` is clamped to 0–100.
   - `open_original` and `share` require the item to exist in `items` or the ledger.
   - Failures are dropped with `204`.
5. **Active reading time (client contract).**
   - The timer runs only while `document.visibilityState === 'visible'` **and** `document.hasFocus()` **and** the article view is the active route.
   - It pauses on `visibilitychange`, `blur` and route change.
   - It flushes every 15 s, and on `pagehide` and route change via `sendBeacon`.
   - List scrolling never counts.

**Not recorded:**

- any read-state change on its own (single, bulk, scroll, keyboard, mark-all, API multi-id, and API single-id while inference is off);
- restores other than a star;
- fetches, trims and content updates;
- Reader API list and contents syncs;
- search queries;
- health-page views;
- prefetch via `GET /api/items/{id}`.

**Phase-4 queries.** Every view takes `include_inferred`, **default 0**. With the default, streaks, feeds-never-opened, most-read, hour/weekday and time-per-source all use `inferred = 0` rows only.

- **Most-read sources:** `COUNT(DISTINCT item_id) … WHERE kind='open' AND inferred <= :inc AND ts BETWEEN … GROUP BY feed_id`.
- **Reading by hour and weekday:** `GROUP BY local_hour` / `local_weekday` over `open`, plus `SUM(value)` of `read_time`.
- **Time per source:** `SUM(value) WHERE kind='read_time' GROUP BY feed_id`.
- **Feeds never opened:** `feeds f WHERE f.disabled_reason IS NOT 'archive' AND NOT EXISTS (SELECT 1 FROM stats_events s WHERE s.feed_id=f.id AND s.kind='open' AND s.inferred <= :inc)`.
- **Streaks:** consecutive `local_date` values with at least one qualifying `open`.
- **Client split:** `GROUP BY client, inferred`.

**CSV export.** `GET /api/stats/export.csv` pages by keyset: `SELECT * FROM stats_events WHERE id > :after [AND ts BETWEEN …] ORDER BY id LIMIT 5000`, one short reader query per page. Each page is fully read into memory and the rows are closed **before** that page is written out through `encoding/csv`. No read transaction is ever held across a network write, so checkpoints are never pinned. The response has a header row and `Content-Disposition: attachment; filename="kipple-stats-YYYYMMDD.csv"`. The OPML export is small and is built fully before writing.

---

## 9. Go package layout and goroutines

```
cmd/kipple/main.go      config → store.Open (migrations) → fetch/sched/hub/handlers → greader.Front ahead of
                        the root mux → serve; shutdown sequence (§4.10); import _ "time/tzdata"; mime
                        registrations; sqlite.OFDLocking()
web/embed.go            package web: //go:embed all:dist (web/dist from the Vite build)
internal/config         KIPPLE_* env only: ADDR(:7080), DATA(/data), USERNAME, PASSWORD, API_PASSWORD
                        (optional initial), PUBLIC_URL, TRUSTED_PROXY_IPS, TZ, SCHED_TICK,
                        FETCH_WORKERS(8), FETCH_PER_HOST(2), CF_ACCESS_TEAM/AUD (optional),
                        LOG_LEVEL, LOG_GREADER_FORMS. Documented in .env.example. Nothing else reads os.Getenv.
internal/store          db.go (three pools, DSNs), tx.go (WithWrite, holder tracking, deadline), migrate.go +
                        migrations/*.sql, idalloc.go (seed, high-water, clock check), feeds.go (FindFeedByURL,
                        archive), folders.go (label lookup), items.go (list/keyset/search/get; SetRead/SetStarred/
                        MarkScope/MarkAllRead, all taking tx), fetchcommit.go (CommitFetch/CommitFetchError/
                        KnownUIDs), retention.go (trim, restore, ledger updates, purge), settings.go (cached
                        snapshot in an atomic.Pointer, refreshed on PATCH), account.go, sessions.go, stats.go
                        (insert + validation in tx + aggregates + keyset CSV pages), icons.go, fulltext.go,
                        maint.go. Only this package contains SQL.
internal/feedurl        Normalize and Key (scheme-less url_key), shared by store, fetch and opml
internal/fetch          client.go (transports, ssrf.go guard, UA, timeouts, hop recorder), fetch.go
                        (conditional GET, cap, charset.go, body hash, gofeed), dedup.go (uid, content_hash,
                        text_hash, in-document duplicates, churn/dup detection), errors.go (classification),
                        backoff.go (pure NextOnSuccess/NextOnFailure; injected clock + rand), redirect.go
                        (policy decision), discover.go (autodiscovery, favicon finder; UI path only by default)
internal/sanitize       absolutize.go (URL attribute resolution, base chain), bluemonday feed policy
                        (RequireParseableURLs, no relative URLs), plain text + word count, lead-image pick,
                        serve-time proxy rewrite
internal/readability    go-readabilityV2 pipeline (guarded fetch, charset, absolutize, sanitize, text, image)
internal/sched          dispatcher.go, worker.go, run.go (attach/outstanding), maintenance.go
                        (hourly/nightly/weekly, cancellable)
internal/greader        front.go (path cleaning, dispatch switch), form.go (raw-preserving), auth.go,
                        itemid.go, stream.go, filter.go, h_ids.go, h_contents.go (streaming encoder),
                        h_edit.go, h_markall.go, h_subs.go, h_tags.go, h_misc.go, response.go (no-null
                        writers), ua.go, seen.go (client last-seen map)
internal/api            router.go, items.go, feeds.go, folders.go, refresh.go, health.go, settings.go,
                        account.go, opml.go, fulltext.go, stats.go, events.go, archive.go
internal/auth           argon2id Verifier (params, semaphore, memo, per-IP failure delay), cookie sessions,
                        effective scheme, trusted-proxy IP, optional Access JWT verifier
internal/events         SSE hub: Subscribe/Publish/Close, per-subscriber buffer 64, 500-event ring
internal/opml           encoding/xml structs, Import (flatten, positions, report), Export (kipple: attrs)
internal/imgproxy       signed, streaming proxy handler (flags, AVIF sniff, semaphore)
internal/stats          Recorder interface (Record(tx, ev)) + impl (event validation, local-time fields)
internal/web            SPA handler (index ETag + no-cache, immutable /assets)
internal/httpx          middleware: request log with redaction, recover, security headers, csrf.go
```

**Dependencies** (pinned): `modernc.org/sqlite` v1.59.x (with its exact `modernc.org/libc`), `github.com/mmcdole/gofeed` v1.4.x, `github.com/microcosm-cc/bluemonday` v1.0.27, `github.com/markusmobius/go-readabilityV2`, `golang.org/x/net` (html, charset), `golang.org/x/text`, `golang.org/x/crypto/argon2`, `golang.org/x/sync`. Tests use `testify/require`.

**Dockerfile.** Three stages: `node:22-alpine` (`npm ci && npm run build`) → `golang:1.27-alpine` (`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`, with cache mounts) → `gcr.io/distroless/static-debian12:nonroot`.

**Compose** (`host-a` project, service `kipple`): named volume `/data`, `stop_grace_period: 30s`, `GOMEMLIMIT=64MiB`, `mem_limit: 256m`, json-file logging 10m × 3, port 7080. `http.Server` has `ReadHeaderTimeout` 10 s, `WriteTimeout` 60 s on non-SSE routes (SSE clears it with `http.ResponseController.SetWriteDeadline`), and `IdleTimeout` 120 s.

**Long-lived goroutines:**

1. `http.Server`: per-connection goroutines. SSE handlers select on the subscriber channel and `r.Context()`.
2. The sched dispatcher (1). It is the only owner of in-flight, per-host, host-deadline, pending and run state.
3. Fetch workers (8), ranging over `jobs`, sharing the commit gate.
4. Maintenance (1).
5. The signal watcher (main, blocked on `ctx.Done()`).

**Synchronization inventory:**

- the id allocator mutex;
- the hub mutex;
- the cached Reader-token and settings `atomic.Pointer`s;
- the Verifier semaphore, memo and per-IP failure map (mutex);
- the greader client-seen map (mutex);
- the image-proxy semaphore;
- the commit gate;
- the writer `*sql.DB` with one connection, reachable only through `WithWrite`: the only persistence serialization point.

No goroutine holds a transaction across a channel send, an HTTP call or an SSE wait. No code inside `WithWrite` touches a `*sql.DB`. `go test -race ./...` is mandatory.

---

## 10. Test plan

All tests use `go test -race -timeout 5m ./...`. Every store and API test gets a SQLite file in `t.TempDir()` on the **pinned modernc driver**. Golden files use an `-update` flag.

**`internal/store`**

- **Migrations:**
  - A fresh database gets `user_version=1` and `application_id=1263095884`. Re-opening is idempotent.
  - A foreign `application_id` is refused. A `user_version` above the maximum is refused.
  - A pre-migration snapshot is created for a non-empty database. **A failed migration followed by a retry succeeds**: the unique timestamped name means no "output file already exists".
  - `PRAGMA foreign_keys` is 1 after a failed `foreign-keys-off` migration.
- **Constraints:**
  - the username CHECK;
  - the default folder cannot be deleted;
  - deleting a folder moves its feeds to the default;
  - the retention CHECK rejects 75;
  - a second archive feed is refused;
  - `url_key` uniqueness.
- **WithWrite:**
  - a nested writer-DB call is impossible by type (compile-time: store write methods accept only `Querier`);
  - a deliberately leaked `Rows` is caught by a test helper that asserts the pool is free after each handler test;
  - an acquisition timeout logs the holder.
- **Allocator:**
  - strictly increasing under 8 goroutines × 10k calls;
  - seeding from high-water plus items plus ledger;
  - the high-water mark survives an unsubscribe cascade of the newest items;
  - a backward clock step still gives increasing ids;
  - a seed 2 h ahead logs ERROR and sets the health warning.
- **Query-plan regression:** a seeded database of 150k items. `EXPLAIN QUERY PLAN` is asserted against §2.3 for every hot query, **twice**: fresh, and after `ANALYZE` (STAT4). The queries are ids unread/starred/feed/label, both `ot` legs with and without `c`, contents, mark-all, keyset views, the trim-set selection, the dedup lookup, the due selection, `FindFeedByURL` and the stats session lookups. `INDEXED BY` never errors.
- **WAL bound:** 1000-id edit-tag `a=read` on articles of 20 KB writes < 1 MB of WAL (measured with `wal_autocheckpoint=0` and the `-wal` file size).
- **FTS:**
  - insert, update, delete, trim, restore and cascade stay consistent, and `integrity-check` is clean after each;
  - a markup-only update leaves `SELECT count(*) FROM items_fts_data` unchanged;
  - title-only and text-only changes re-index correctly;
  - the query sanitizer handles `"`, `-`, `:`, `*` and empty input.
- **Maintenance:**
  - a pre-existing `kipple-snapshot.tmp` does not stop the nightly snapshot;
  - a missing `/data/backup` is created;
  - `sys.last_snapshot_at` is updated;
  - cancelling the context interrupts `VACUUM INTO`.

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
- **`absolutize`** (golden):
  - relative `src`, `srcset` (several candidates), `href`, `poster` and `source[src]`;
  - `xml:base`;
  - a relative item link;
  - protocol-relative `//cdn/x.png`;
  - a `data:` URI;
  - an unresolvable value is dropped;
  - readability output resolved against the article URL.

  An end-to-end fixture with `<img src="/wp-content/x.jpg">` must come out absolute in `stream/items/contents` **and** in `GET /api/items/{id}`, proxied in the latter only when it is http.
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
  - SIGTERM during a 138-feed run exits in under 15 s;
  - a completed fetch commits its fetch_log row;
  - no worker blocks on `doneCh`;
  - SSE subscribers return before `Shutdown` completes;
  - waiting handlers return 503;
  - `wal_checkpoint` runs.

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
  - the items move to the archive feed, which appears in `subscription/list` with `origin.streamId` = archive;
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

**`internal/greader` contract tests.** Each test replays a full request sequence against a seeded database and compares the responses with golden files.

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
  9. `edit-tag a=read` with a single id **under the default settings → 0 `open` rows**; with the setting on → 1 inferred `open`
  10. several scroll-style single-id `a=read` requests under the default → 0 `open` rows
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
  - the failure delay never returns 429;
  - **after 20 wrong-password attempts from one IP, the correct password from the same IP succeeds at once and clears the count**;
  - a memoized login does not call argon2 (counted through a fake);
  - 50 concurrent wrong ClientLogins never run more than one argon2 at a time (fake with a concurrency gauge).
- *`ot` semantics:*
  - an item crawled before `ot` whose content changed after `ot` is returned, including on a first page without `c`;
  - an item crawled before `ot` and read after `ot` is **not** returned (unless the setting is on);
  - the 120 s slack boundary on both legs;
  - a property test on randomized data, **with and without `c`**, in both directions, shows the two-leg form equals the reference OR query.
- **Phase-1 gate.** On day 1, the owner's real Reeder and NNW traffic is captured with `KIPPLE_LOG_GREADER_FORMS=1` and turned into additional golden sequences. Any mismatch with the reconstructed sequences becomes a failing test before phase 2 starts. The capture also decides `stats.api_single_read_is_open` (§8).

**`internal/stats` and `internal/api`**

- **The structural rule:** a fake Recorder counts calls.
  - `POST /api/items/mark-read` with 1 id, 500 ids, a scope, and `reason=scroll` → 0 calls.
  - greader `mark-all-as-read` → 0.
  - `edit-tag` with 2 ids and `a=read` → 0.
  - `edit-tag` with 1 id and `a=read`: 0 with the default settings; exactly 1 `open` (inferred, family by UA) with the setting on and an unread item; 0 on an already-read item.
  - `r=read` → 0.
  - `a=starred` with 5 ids → 5 `star`.
- **Atomicity:** a Recorder that returns an error rolls back the state change in the same transaction.
- **Web:** `POST /open` gives 1 `open` plus a read state; `GET /api/items/{id}` gives 0.
- **Validation:**
  - `read_time` without an open is dropped;
  - a cumulative time above elapsed is dropped;
  - more than 3600 is dropped;
  - **two concurrent batches whose sum exceeds the cap insert at most the cap**;
  - scroll 150 is clamped to 100;
  - a `session_key` from another item is dropped.
- **Views:** with default parameters, inferred rows are excluded from streaks and feeds-never-opened.
- **Local-time fields** across the DST boundaries (2026-03-08 and 2026-11-01, America/New_York).
- **CSV** round-trip (commas, quotes, newlines in titles), and a 20k-row export that runs as ≥ 4 separate reader queries.
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
- passthrough validators and 304.

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
| **Reeder Classic is closed source.** The `mark-all-as-read` `ts` unit, continuation behaviour, `T=x` use, its Content-Type, its field-type strictness and its scroll-mark batching are inferred, not observed | Tolerant parsing (`ts` by digit count, any non-multipart POST body parsed as a form), `n` up to 100000, `T` `""`/`x` accepted, no JSON nulls anywhere, inference off by default. The day-1 capture turns real traffic into golden tests (§10) |
| **Reeder does not follow continuation** (`n=10000`). If more than 10,000 items are unread, the oldest never reach Reeder and counts differ from NNW and the web UI | Warned in the bootstrap `warnings` and the health view. The UI OPML import offers "mark items older than N days read" on first fetch as an explicit choice. The default N=250 × 138 feeds can reach 34,500, so the migration import should use that option |
| **`ot` excludes user state changes** (a deliberate departure from the brief's wording) | Both target clients pull full unread and starred lists without `ot`; FreshRSS behaves this way. `greader.ot_includes_user_changes` restores it without a schema change |
| **The `ot` slack of 120 s on both legs** returns up to 2 minutes of already-known ids | Harmless: clients de-duplicate by id |
| **NNW's `Date` parser uses a full-weekday pattern** and may fall back to a 3-month `ot` every refresh | A few pages of 1000 ids at worst. Watch the access log for `ot` of about now − 90 d |
| **Newest-N retention with NNW:** a trimmed unread item vanishes and NNW marks it read locally | Default N=250, `trimmed_unread_count` in the health view, per-feed overrides. A mark-unread within 90 days restores and holds the item |
| **Stars and mark-unread on items older than `restore_days`** (stub purged) are still ignored, and the client undoes them | 90 days matches NNW's article window. The window is a setting (≤ 365) if the owner stars very old items from app caches |
| **Restore stubs cost disk** (about 270 MB at 90 days for busy feeds) | `retention.restore_days` can be lowered; the nightly snapshot size shows the cost |
| **The archive feed is a visible pseudo-feed** in both clients ("Unsubscribed (starred)") | It is listed only while it holds items. "Purge unstarred" and a real unsubscribe of the archive are explicit actions |
| **The API `open` inference is approximate** | Off by default. When enabled it is stored with `inferred=1` and excluded from every view unless the toggle is on |
| **API subscribe without a synchronous fetch** (the default, decision 33) shows the host as title and no items until the next sync | Pending the owner's decision; `greader.subscribe_fetch_now` restores the synchronous path |
| **GUID churn** produces a wall of unread | The fetch_log note (kept), a health-view flag, and one-click "mark this fetch read". No automatic judgement |
| **A dedup-mode change** needs a rekey; unmatched items are inserted read | An explicit user action with a confirm dialog; `rekey_pending` persists across restarts |
| **The ledger purge** (180 days after last seen) re-opens the tombstone for uids the publisher drops and later re-adds | Rare; shows as one unread item |
| **The `modernc.org/sqlite` and `libc` pin is fragile** | Pin both exactly. The plan-regression suite (fresh and `ANALYZE`d) and FTS integrity tests run on every bump. `ncruces/go-sqlite3` is the fallback |
| **FTS over a view** relies on FTS5 reading external content through a join | Validated on 3.50.4 (MATCH, snippet, integrity-check); re-validated on the pinned driver in the migration test. `'rebuild'` repairs from the view |
| **Write-lock latency** during a manual run or a huge first fetch | The commit gate plus 250-item chunking, and narrow rows for state writes. The fairness test asserts p99 ≤ 250 ms |
| **A writer-pool deadlock or leaked Rows** would silently stop ingestion (there is no monitoring, by design) | `WithWrite` is the only door, with a 10 s deadline and holder logging; store writes take `Querier`; a pool-free assertion after every handler test |
| **The allocator can be seeded ahead of the clock** (bad RTC, manual error). Every id minted until real time catches up is "in the future": NNW's `ot` matches those items on every refresh, and Reeder's `mark-all-as-read ts=<now>` never covers them, so they come back unread | Startup ERROR plus a health banner above 1 h ahead; NTP on Host-A. Kipple does not rewrite ids |
| **Unpadded bare-hex ids made only of digits** would be parsed as decimal | No target client sends them. A test documents the limitation |
| **SSE through cloudflared and Access** is unverified on this named tunnel | Pings every 15 s, `no-transform`, the `/api/status` polling fallback, and `curl -N` on the release checklist |
| **Access path precedence** for the `/api/greader.php` Bypass app has community reports of inconsistency | The external `curl` checks are mandatory. The root Reader routes never get a bypass |
| **Password exposure on the public ClientLogin** | A separate, generated API password, argon2id (m=19 MiB) behind a global semaphore of 1, a success memo, per-IP failure delays (never 429), and redaction in logs. A distributed attacker gets at most ~20 guesses per second against a 24-character random password |
| **The stateless Reader token** can only be revoked by changing the API password | Accepted for a single user. The settings UI says so |
| **Cloudflare-challenged feeds** fail on TLS fingerprint | `error_class='cloudflare'` with actionable text; `disable_http2` and a UA override |
| **Per-host limits key on the exact hostname** | About 15 rounds of 1–2 s in a manual run at 138 feeds |
| **The uid, content_hash, text_hash and `url_key` rules** are embedded in stored data | Frozen before the first real import, with versioned prefixes. Any change needs a re-keying migration |
| **Extracted full text is not searchable**, and restores lose extracted full text | Accepted for phase 2. Re-extraction is on demand |
| **Inline full-text extraction** can hold a worker up to 60 s | 8 workers and few full-text feeds. Anything over budget falls back to on-demand extraction |
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
| 3 | [minor] NNW disable-tag sends the folder id raw, so `&`/`+` names are never deleted | **FIX** | The form parser keeps raw text; label lookup tries decoded, raw and PathUnescape forms; disable-tag falls back to the raw body from `s=` to `&T=` or the end (§6.2). NNW test with `News & Politics+` |
| 4 | [minor] ServeMux 301s `//` paths, dropping POST bodies | **FIX** | A front handler cleans slashes and prefixes and dispatches Reader paths with its own switch; no ServeMux sees them (decision 26, §6.1). Tests plus a release-checklist curl |
| 5 | [minor] Unrecognized Content-Type silently discards edits | **FIX** | `mime.ParseMediaType` with parameters ignored; every non-multipart POST body is parsed as a form; WARN on zero ids from a non-empty body (§6.1–6.2) |
| 6 | [minor] Item HTML never absolutized (client-compat) | **FIX** | Merged with #25: ingest-time resolution of every URL attribute, readability output included, relative leftovers dropped (decision 31) |
| 7 | [minor] quickadd on an existing URL returns 0 results; the `url_original` match is missing | **FIX** | Idempotent quickadd/`ac=subscribe` through `FindFeedByURL` (scheme-less `url_key`/`url_original_key`) returning `numResults:1` with the existing streamId (§6.9) |
| 8 | [minor] ot leg 2 empty when `c` is absent (`min(x, NULL)`) | **FIX** | Absent `c` binds to MaxInt64 descending and 0 ascending, never NULL; validated; the property test covers no-`c` (§3, §6.5) |
| 9 | [minor] Reeder never follows continuation, so unread beyond 10,000 is missing | **FIX** (residual **DEFER**) | Warning when unread > 10,000; an explicit UI import option marks old first-fetch items read. The client limit itself cannot be fixed server-side (§11) |
| 10 | [minor] Login lockout never checks the password and slides forever | **FIX** | The password is always verified; success clears the IP's count; only failures are delayed, in a fixed window (§6.3). Test: 20 bad then 1 good from the same IP succeeds |
| 11 | [minor] An allocator seeded ahead of the clock breaks ot and mark-all | **FIX** (residual **DEFER**) | Startup ERROR plus a health banner above 1 h ahead; the §11 row now names the ot/mark-all impact. Rewriting ids is not attempted |
| 12 | [major] Read/star state in the wide items row rewrites overflow pages | **FIX** | `items` split into a narrow row plus `item_content`; FTS over a join view; triggers moved and validated (decision 30). WAL-bound test added. The alternative (column reorder plus `idx_items_read`) is rejected |
| 13 | [major] The one-connection writer deadlocks on nested acquisition or leaked Rows; maintenance can't run under `query_only` | **FIX** | `WithWrite` is the only door with a 10 s deadline and holder logging; store writes take a tx; `Recorder.Record(tx, …)`; a snapshot pool for `VACUUM INTO`; FTS integrity-check on the snapshot; weekly optimize dropped for automerge (decision 2, §2.1, §2.6) |
| 14 | [major] Reply channels and shutdown order can wedge the dispatcher or hang workers | **FIX** | Buffered reply channels, non-blocking dispatcher sends, handlers select on reply, timer and `shutdownCh`, a dispatcher draining until workers exit, commits under `WithoutCancel` with 10 s, a revised 7-step order, client-seen moved out of the dispatcher (§4.1, §4.10) |
| 15 | [major] VACUUM INTO refuses existing targets; foreign_keys may stay off | **FIX** | `MkdirAll` plus `Remove` before every VACUUM INTO; timestamped pre-migration names (newest 3 kept); foreign_keys restored in a defer and verified; last-snapshot time in the health view (§2.5, §2.6) |
| 16 | [minor] ot leg-2 NULL `c`, and no visibility slack on leg 2 | **FIX** | Same binding fix as #8; leg 2 uses `content_changed_at >= ot − 120`; the legs stay disjoint (validated) |
| 17 | [minor] FTS `AFTER UPDATE OF` fires on markup-only changes | **FIX** | `WHEN old IS NOT new` guards on every update trigger, SET lists built from changed columns, and `text_hash` gating; validated (`items_fts_data` 21 → 21) |
| 18 | [minor] GOMEMLIMIT doesn't bound SQLite; contents built unbounded | **FIX** | GOMEMLIMIT 64 MiB + 34 MB of caches budget, a streaming contents encoder, `i=` capped at 1000, `mem_limit: 256m`, an RSS-under-load checklist item (decision 35) |
| 19 | [minor] Long-lived read cursors in exports pin checkpoints | **FIX** | The CSV export pages by keyset, 5000 rows per short query, fully read before writing; the OPML export is built before writing (§8) |
| 20 | [minor] Reader pool keeps only 2 idle connections | **FIX** | `SetMaxIdleConns(4)`, `SetConnMaxIdleTime(0)` (§2.1) |
| 21 | [minor] Stats ingest caps are check-then-insert across pools | **FIX** | Validation and inserts in one `WithWrite` transaction, counting earlier events in the same batch; concurrency test (§8) |
| 22 | [minor] Plans verified without ANALYZE on 3.50.4; production is 3.53.4/STAT4 | **FIX** | The plan suite runs on the pinned driver, fresh and after `ANALYZE`; `analysis_limit=400`; `optimize=0x10002` at open. `INDEXED BY` is kept: the leg always constrains the leading column, so it cannot become unsatisfiable (§2.3) |
| 23 | [minor] Mark-all cutoff and allocator seed include uncommitted or deleted ids | **FIX** | A missing `ts` → committed `max(id)` on a reader snapshot; `sys.id_high_water` persisted in CommitFetch; the stats scan dropped from the seed (§3) |
| 24 | [blocker] API open inference is on by default, so Reeder scroll marks count as reads | **FIX** | Default off; turned on only after the day-1 capture; views default to `inferred=0`; streaks and never-opened exclude inferred rows unless toggled; contract test with scroll-style marks (decision 21, §8) |
| 25 | [major] Relative image/link URLs never made absolute (product-rules) | **FIX** | Same as #6, plus golden tests through both the Reader API and the web API; `/img/` collision removed |
| 26 | [major] A manual run can stay open forever; refresh joins import/retention runs | **FIX** | In-flight feeds are attached to the run; `total = outstanding` at creation; the run ends when `outstanding` hits 0; only manual runs are joined; runs are per kind (§4.9). Scheduler test added. A watchdog is unnecessary because completion is structural |
| 27 | [major] Starring an item trimmed while a client still shows it is lost | **FIX** | Same restore mechanism as #1, also applied to the web star and mark-unread endpoints. The proposed crawl-age grace window is **REJECTED**: it breaks "newest N" on busy feeds and doesn't cover NNW's 90-day window |
| 28 | [major] OPML re-import and quickadd duplicate feeds after the http→https migration | **FIX** | One `FindFeedByURL` (normalized, scheme-less, `url` or `url_original` key) used by every subscribe path; `url_key` is UNIQUE; a re-import test expects 0 new feeds |
| 29 | [major] The OPML round trip loses titles, order and per-feed settings | **FIX** (partial **REJECT**) | The OPML title goes to `custom_title` (cleared if it equals the doc title after the first fetch); positions from document order; `kipple:` override attributes; `http_auth` never exported; case merges and dropped memberships reported. Keeping multi-folder membership is rejected: decision 9, NNW disallows it (§7.6) |
| 30 | [major] Unsubscribing hard-deletes starred items | **FIX** | Starred items are re-parented to a single archive feed that stays in subscription/list while non-empty; the web dialog shows the count with an opt-in delete (decision 24, §6.9). Validated |
| 31 | [major] Unbounded argon2id on the public ClientLogin | **FIX** (partial **REJECT**) | m=19 MiB/t=2/p=1, a global semaphore of 1 with a 5 s wait, a success memo, a generated 24-character API password. The global failure lockout is rejected: it would let an attacker lock the owner out, and the semaphore already bounds global rate and memory |
| 32 | [minor] The backoff cap makes long-interval feeds poll more often when failing | **FIX** | `delay_s = min(interval_s·2^(n−1), max(86400, interval_s))`; 10080-minute rows in the backoff test (§4.6) |
| 33 | [minor] The health view's history and trim signals erode quickly | **FIX** | fetch_log kept 14 days with a 50-row floor, the last 10 errors kept, `keep=1` for redirect/churn/rekey notes; `last_error*` kept after success; an immutable `trimmed_unread_count` excluding same-transaction inserts, with a reset; no ledger update for starred-scope mark-all (§4.8, §5) |
| 34 | [minor] Lowering per-feed retention does nothing until new items arrive | **FIX** | Trim runs on every successful fetch (ok/unchanged/not_modified), and a retention PATCH or default change enqueues `trim_only` jobs (decision 18) |
| 35 | [minor] The API "never fetch" exceptions need the owner's sign-off | **FIX** (flagged **OPEN** for the owner) | The default now has no outbound HTTP on any Reader API path: the feed is inserted as due and the scheduler fetches it within 30 s. The synchronous path sits behind `greader.subscribe_fetch_now` pending the owner (decision 33) |
| 36 | [minor] Image proxy buffering, AVIF sniffing, private-net feeds | **FIX** | Content-Length pre-check then streaming with `MaxBytesReader` and an abort on overflow; AVIF by `ftyp` brand with an allow-listed upstream-type fallback; signed `flags` carry `allow_private_net`/`allow_insecure_tls`; a fetch semaphore of 8 (§7.4) |
| 37 | [minor] SameSite=Lax doesn't protect against same-site origins | **FIX** | `Sec-Fetch-Site: same-origin` (or a matching `Origin`) plus a required `X-Kipple-Client` on every state-changing cookie request; the beacon is exempt from the header only (§7) |

---

## 13. Deltas from lemon24/reader (applied after the red-team revision)

`docs/research/lemon24-reader.md` compared this design with a mature SQLite feed-reader library.
Most of its ADOPT items were already here (per-connection pragmas in the DSN, `application_id`,
`foreign_key_check` after migrations, `PRAGMA optimize`, a date-free `content_hash`, validators and
body hash, per-feed resync, keeping stats rows without a cascade from `items`). These are the ones
that were not, now part of the design:

1. **`synchronous=NORMAL` and `busy_timeout=5000` in every DSN**, stated explicitly. SQLite's
   default is `FULL`; WAL with `NORMAL` loses at most the last commits on power loss, which the next
   poll refetches.
2. **Startup self-check.** Probe `json_valid('1')` and `CREATE VIRTUAL TABLE temp.k USING fts5(a)`;
   fail fast naming every missing feature, so a driver swap can never silently lose search.
3. **`feeds.last_new_item_at`** beside `last_success_at`, written only when a fetch inserts an item.
   The health view shows "checked 5 min ago, nothing new for 94 days", the most useful dead-feed signal.
4. **Reading time.** `items.image_count` is stored at ingest next to `word_count`.
   `read_seconds = ceil(word_count / 265 × 60) + image bonus` (12 s for the first image, one second
   less for each next image down to a 3 s floor). It is computed at serve time for cards and
   recomputed from the full text when extraction runs. This is the "only if trivial" feature, and it is.
5. **FTS5 tokenizer** `porter unicode61 remove_diacritics 2`, with ranking `bm25(items_fts, 4.0, 1.0)`
   (title weighted 4×). HTML stripping for the index (drop script/style/noscript, fold in `alt` and
   `title` text) happens in Go before the write transaction; the split triggers in §2 stay.
6. **403 browser-UA fallback.** On a 403 without `cf-mitigated`, retry once with a browser User-Agent.
   On success, persist `feeds.ua_mode = 'browser'` so later fetches use it directly, and log the 403's
   `Server` header. A later 403 in browser mode is an ordinary failure.
7. **`Retry-After` as an HTTP-date is interpreted relative to the response `Date` header**, which
   tolerates publisher clock skew.
8. **URL migration resets fetch state.** When a redirect migration or a manual URL edit changes
   `feeds.url`, clear `etag`, `last_modified`, `body_hash`, the error counters and `next_fetch_at`,
   and keep the items.
9. **A malformed item is skipped, not fatal.** One unparseable entry is dropped with a `fetch_log`
   note; the rest of the document commits. This applies to RSS, Atom and JSON Feed.
10. **Enclosures are deduplicated by normalized URL at ingest.**
11. **Normalized links for dedup.** The `l:` uid and `link_hash` hash reader's normalized form
    (lowercase scheme and host, `http`→`https`, no trailing slash, query and fragment kept), so the
    common https migration does not look like a new item.
12. **Slow-stage warning.** A fetch, parse or commit stage taking ≥ 1 s logs a WARN with the feed id.

Rejected from reader, with reasons in the report: grid-snapped scheduling, dropping exponential
backoff, non-atomic entries-then-feed writes, `A-IM: feed` deltas, post-hoc delete-and-merge dedup
(ids already served to Reader clients cannot be retracted), a separate search database, and a
tri-state starred flag.
