# Test plan

How Kipple is tested. Kept beside the other maintainer documents; [design.md](../design.md) holds the behaviour it checks.

CI runs `go test -race -shuffle=on -timeout 15m` over every package, split across three runners (`internal/api`, `internal/store`, the rest) behind one required `go` check (the `race_on`/`race_off` build-tagged test files stretch timing ceilings under the race detector; the Windows dev box has no C compiler, so it runs without `-race`). Every store and API test gets a SQLite file in `t.TempDir()` on the **pinned modernc driver**, so tests run with `t.Parallel()`; the exceptions stay serial: a test that swaps a package variable (a hook, a batch size, a budget), reads process-wide memory statistics, or asserts a wall-time ceiling. Golden files with an `-update` flag are used only by the `internal/fetch` parser tests; the Reader contract tests assert inline. Items marked **TODO** below are planned but not yet written.

**`internal/store`**

- **Migrations:**
  - A fresh database gets `user_version` = the latest migration (currently 15) and `application_id=1263095884`. Re-opening is idempotent.
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
- `trimmed_unread_count` excludes the items the trimming transaction inserted (for a chunked commit that counts its arrivals, the last chunk's) and is unchanged by mark-all.
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

- **`itemid`:** long padded `tag:google.com,2005:reader/item/00063f8740c61a40`; the legacy unpadded long form; bare `00063f8740c61a40`; decimal `1758700000123456`; client-supplied vectors `00058b10ce338909` ↔ `1560279178774793`, `ffffffffffffcdef` ↔ `-12817`, `0000000000000000` ↔ `0`; `0x…`; garbage and 17-digit hex skipped; the emitted formats round-trip.
- **`form`:**
  - `;` in `t=` and `a=`, raw `=` inside a value, `+` as a space, `%2B`, repeated `i`, a pair with no `=`, body plus query merge, and the 4 MiB cap;
  - `Content-Type: application/x-www-form-urlencoded; charset=utf-8`, `text/plain`, `application/octet-stream` and an absent header all parse;
  - zero ids from a non-empty body logs a WARN.
- **`front`:** `//reader/api/0/edit-tag` under the prefix, a trailing-slash base, a doubled prefix, and root forms. None produces a 3xx, and POST bodies arrive intact.
- **`ts` normalization:** 10, 13, 14, 15, 16 and 19 digits, 0, absent (committed `max(id)`, not the allocator), and non-digit.
- **No-null rule:** every golden Reader response is walked and asserted to contain no JSON `null` except `LSID`. Fixtures include a feed with no `site_url`, an item with no link, and `greader.icon_urls` off.

**`internal/greader` conformance suite** (`conformance_*_test.go`, `go test ./internal/greader -run Conformance`). A generic client over a real HTTP server: it signs in with ClientLogin, keeps only the returned token, and exercises every endpoint, parameter and answer listed in [compatibility.md](compatibility.md#reader-api), including the failure paths (401 headers, revoked tokens, T rules, 405), CORS preflights, every content type and the GET/POST parameter forms. Each assertion is tagged with the reference it follows: the Google Reader API, the reference servers, or this document. A divergence from those references is listed in compatibility.md's endpoint notes, never only in a test.

**`internal/greader` contract tests.** Each test replays a full request sequence against a seeded database and asserts the responses inline (`contract_test.go`; there are no golden files).

- *Batching-client sequence* (from an open-source client's source):
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
- *Closed-source-client sequence* (reconstructed from reference server logs):
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
- *Reader API client smoke tests:* `GET /api/greader.php` returns `OK`; `ids n=100000`; `tag/list?types=1`; `stream/contents?…&ot=0`; `user-info` without `output`; an unknown endpoint returns `200 []`.
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
- **Phase-1 gate.** On day 1, the owner's real Reader API client traffic is captured with `KIPPLE_LOG_GREADER_FORMS=1` and turned into additional sequences. Any mismatch with the reconstructed sequences becomes a failing test before phase 2 starts. Status: no captured-traffic sequences exist under `internal/greader`; the only captured evidence is a Reader API client log from 2026-09-26 (alpha.2), kept outside this repository. The capture was also meant to decide whether API reads could count (§8); the owner has since decided they never do.

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
  - re-importing the original 138-feed OPML after http→https migrations adds **0** feeds;
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

**Release checklist** (manual, on the Kipple server, before each deploy, after `/code-review high`):

- The image is under 50 MB (`docker image inspect`).
- Idle RSS stays under 100 MB after 30 minutes with the real OPML (`docker stats`).
- **RSS stays under 100 MB during a 150-id contents request of full-text items, and under 20 concurrent wrong ClientLogins.**
- From outside the LAN:
  - `curl` ClientLogin through `https://rss.example.com/api/greader.php` returns 200 (Access bypass works).
  - `https://rss.example.com/api/greader.php//reader/api/0/token` with the header returns 200, not a 3xx.
  - `https://rss.example.com/reader/api/0/token` gets the Access redirect.
  - `https://rss.example.com/` gets the OTP page.
  - `curl -N` on `/api/events` with a session cookie streams pings line by line through the named tunnel.
- A Reader API client completes a full sync.
- The health view shows a snapshot younger than 24 h.
