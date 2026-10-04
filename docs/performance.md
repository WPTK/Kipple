# Performance at scale

What Kipple does with a large library: 500 feeds and 150,000 items in one 1.6 GB database. This page says how the
library is built, what was measured, the numbers, how to repeat the run on your own machine, and what to size for.

The tool is `scripts/scalegen`. It writes the library straight into a SQLite database through the real `store`
package (so migrations, triggers and the full-text index behave as in production), serves a synthetic feed for the
refresh, and runs the whole measurement against a `kipple` binary.

## Reproduce

```
go build -o kipple ./cmd/kipple                       # a native build; no Docker is needed
go run ./scripts/scalegen gen   -dir scale-data       # about 2 minutes, writes scale-data/kipple.db (1.7 GB)
go run ./scripts/scalegen bench -bin ./kipple -src scale-data -work scale-work -runs 3
```

`bench` needs about 8 GB free in `-work`, takes about 30 minutes for three runs, uses ports 1931, 1932 (the feed
server) and 1933 (the restored copy), and deletes `-work` when it finishes. It prints a table with every value and the
median of the runs. Other commands: `gen -schema 10` (or `6`) writes the library at an older schema, `rewind -db F
-schema N` does the same to a copy, and `feeds -dir scale-data` serves the feeds on its own. `gen` takes
`-feeds`, `-items`, `-seed` and `-stats-days`.

The data is deterministic for a seed: the same counts, sizes, text and read, starred and retention shape. Time stamps
are relative to the moment of generation, so two databases are not byte-identical.

## The library

| | |
|---|---|
| Feeds | 500 in 25 folders (431 of them in a folder, 69 in the default one) |
| Retention | 330 feeds inherit the default of 250 items, 100 keep 500, 50 keep 1000, 20 keep everything. Most feeds sit at their cap, as a library that has been running for a while does |
| Items | 150,000 with content (title, HTML, plain text, image URL for 70 percent) |
| Item size | half are summaries of 30 to 200 words, 40 percent articles of 300 to 900 words, 10 percent long reads of 1000 to 3500 words; about 3.4 KB of HTML and 3.3 KB of text per item on average |
| State | 33,239 unread, 116,761 read, 2,215 starred |
| Other tables | 6,000 trimmed items with restorable content, 25,000 fetch log rows, 48,403 reading statistics events over 400 days |
| Text | words follow a Zipf distribution over a 30,000-word vocabulary, so a few words are in most items and most words are rare. The rare term `zyxquorn` is in exactly 5 items and `photosynthesis` in about 1 item in 330 |
| Database file | 1,637 MB after generation, about 11 KB per item including the full-text index, indexes and the other tables |

Synthetic text has no real-language structure, so the full-text index is a plausible shape, not an exact one.
Item content is larger or smaller in other libraries; see "Sizing" for how to scale the numbers.

## Machine and method

- A desktop with a 13th-generation Intel Core i5 (14 cores, 20 threads), 48 GB of RAM and a local SSD, running
  Windows 11. Other work was running on the machine during the measurements, so every figure is a typical value, not a
  best case.
- `kipple` built natively from main at 0.7.0-beta.2 with `go build` (Go 1.27). The web app was not embedded, so the
  resident size is that of the server without the static assets.
- Loopback only (no network latency). The operating system file cache is warm: each run starts from a fresh copy of
  the database that was just written. A start after a reboot reads the file from disk and can take longer.
- Each metric is taken three times and the table reports the median. An API call is made six times per run: the
  first call is "first" and the median of the other five is "warm". The server process is stopped by killing it, not
  by a graceful shutdown, so the WAL checkpoint at close is measured separately (`open.current_schema.close_ms`).
- Without `GOMEMLIMIT` unless stated. The compose example sets `GOMEMLIMIT=64MiB` with `mem_limit: 256m`; the
  last section of the memory results covers that.

## Results

### Start, upgrade and restore

| | Median | Runs |
|---|---:|---|
| Cold start (process start to `/healthz` 200) | 217 ms | 205, 217, 247 |
| Open the database at the current schema | 28 ms | 152, 28, 26 |
| Upgrade from the previous schema (10 to 11, the newest migration) | 6.6 s | 6.6, 5.4, 6.8 |
| Upgrade from an older schema (6 to 11: 0007 rewrites every item, 0009 builds three statistics indexes) | 9.0 s | 8.2, 11.1, 9.0 |
| Backup, in-app export (build the zip) | 32 s | 35, 31, 32 |
| Backup download (loopback) | 5.9 s | 6.5, 5.9, 5.9 |
| Backup zip size | 654 MB | the 1,637 MB database compresses to 40 percent |
| `kipple restore` of that zip | 26 s | 26.3, 26.6, 25.8 |
| First start after the restore | 204 ms | 279, 204, 204 |

The upgrade is dominated by the pre-migration snapshot (`VACUUM INTO`, a 1.6 GB copy), not by the migration itself:
the newest migration only deletes a few settings rows. The snapshot costs about 3.5 s per GB. A restored backup
reported the same unread count as the original in every run.

An **old-format backup** restores. The library above was copied to the previous schema, a 0.5.0-beta.1 build took its
own in-app backup of it (686 MB, schema 10, 500 feeds, 150,000 items), and the current build restored it in 26 s with
checksums and the integrity check passing, migrated it on the first start and found the planted rare term by search.
A 0.3.0-beta.3 backup (schema 9, empty library) restores and migrates as well. The repository holds no backup zip in its
test fixtures (the tests build their archives in code), so these two were made by building the old tags.

### Interactive calls (warm, median of 3 runs)

| Call | ms |
|---|---:|
| `GET /api/status` | 2.2 |
| `GET /api/items`, unread, page of 50 | 1.5 |
| same, 100 pages deep (5,000 items in) | 1.6 |
| all items / oldest first / one feed | 1.5 / 1.5 / 1.6 |
| one folder | 3.1 |
| starred (2,215 items) | 46 |
| `GET /api/items/{id}` | 0.5 |
| `GET /api/stats/summary` (year / all time) | 46 / 50 |
| `GET /api/health/feeds` | 5.2 |
| `GET /api/opml` | 2.0 |
| **`GET /api/bootstrap`** (what the web app loads when it opens: folders, 500 feeds with unread counts, totals, settings) | **706** |

Every call except the bootstrap is a few milliseconds. The bootstrap counts unread items per feed over the whole items
table, so it is the call that grows with the library: about 0.7 s at 150,000 items, and expect it to scale roughly
linearly with the item count. Nothing here is over 2 s.

### Full-text search (warm, median of 3 runs)

| Search | ms |
|---|---:|
| Common word (in most items) | 191 |
| Common word, relevance order | 269 |
| Mid-frequency word | 231 |
| Two common words | 185 |
| Prefix while typing (`typing=1`) | 220 |
| Rare word (5 items) | 1.6 |
| Word in about 450 items | 6.6 |
| Word that is not in the library | 0.5 |

Forty common-word searches in a row had a median of 198 ms and a slowest of 292 ms (450 ms in the worst run). A
search that outruns the 500 ms budget is answered with `422 search_too_broad`. See
[#229](https://github.com/WPTK/Kipple/issues/229).

Search is the call that varies most from run to run. In three of the six runs (with and without `GOMEMLIMIT`) at least
one search loop had searches refused, 12 to 33 of 40 in a loop, while the median of the other runs was 190 to 230 ms.
The slow runs followed the copying of tens of gigabytes of database files, so the file cache was competing with them:
the same thing happens after a restart, when the first searches read the index from disk.

### Reader API (warm, median of 3 runs)

| Call | ms | Body |
|---|---:|---:|
| `subscription/list` | 1.2 | 102 KB |
| `tag/list` | under 1 | 1 KB |
| `unread-count` | 6.0 | 38 KB |
| `stream/contents`, 50 unread items | 1.5 | 175 KB |
| `stream/contents`, 250 unread items | 5.2 | 901 KB |
| `stream/items/ids`, 10,000 unread / all | 3.8 / 3.1 | 254 KB |
| `stream/contents`, starred, 50 | 1.5 | 208 KB |
| `stream/items/contents`, 50 ids (POST) | 1.1 | 175 KB |

All of them are fast; a sync client syncing a library of this size is limited by the body size, not the server.

### Refresh and retention

| | |
|---|---|
| Refresh of all 500 feeds (local feed server, each feed returns 5 new items) | 3.3 s (3.9, 3.3, 3.1); 500 fetched, 0 errors, 2,500 new items |
| Trim after that refresh (feeds at their cap lose the surplus) | 142 items, inside the refresh time |
| A reader during the refresh | median 2.6 ms, worst 169 ms |
| A write (star toggle) during the refresh | median 6.3 ms, worst 117 ms |
| Lower the default retention from 250 to 100 (bulk trim) | 64 s for 32,414 items (65, 64, 63), about 2 ms per item |
| A reader during the bulk trim | median 2.6 ms, worst 12 ms |
| A write during the bulk trim | median 132 ms, worst 0.8 s (0.4 to 0.8 s) |

The bulk trim is the slowest background job and the one that waits longest on the single writer; see
[#228](https://github.com/WPTK/Kipple/issues/228).

### Memory (resident size)

| | MB |
|---|---:|
| Idle after start (10 s) | 62 |
| During the interactive calls above | 86 |
| During searches | 79 |
| During the in-app backup | 61 |
| During the refresh | 104 |
| During the bulk trim (the highest) | 125 |
| After the refresh, settled | 100 |

A soak of 5 rounds of 60 mixed list, search and bootstrap calls kept the resident size flat (79, 53, 53, 53, 52 MB after
each round), so nothing grew with the number of requests. The peak is during the bulk trim and the refresh, where it
stays under 130 MB, within the 256 MB of the compose example.

With the compose example's `GOMEMLIMIT=64MiB` (three more runs) nothing moved: idle 62 MB, peaks 128 MB (bulk
trim) and 103 MB (refresh), common-word search 203 ms, refresh 2.9 s, bulk trim 65 s. The soft limit is below the
working set during a refresh or trim, and the Go runtime keeps going past it instead of stalling.

### Database file and WAL

| | MB |
|---|---:|
| Database after generation | 1,637 |
| After a refresh and a bulk trim of 32,414 items | 1,740 (trimmed content is kept for `retention.restore_days`) |
| WAL while idle | 0 |
| WAL peak during a refresh | 5.5 |
| WAL peak during the bulk trim | 17 (171 in one of three runs; the file is cut back to 64 MB at the next checkpoint) |

The database file does not shrink after a trim: SQLite reuses the freed pages for new items, so the size settles
instead of growing.

## Findings

Anything that failed or was slow enough to matter is an issue:

- [#228](https://github.com/WPTK/Kipple/issues/228): the bulk retention trim runs about 20 times slower than the
  budget in the code and holds the writer for about 4 s a batch against a 10 s deadline.
- [#229](https://github.com/WPTK/Kipple/issues/229): a common-word search at this size is within 2 times of its 500 ms
  budget and is refused as too broad when the machine is busy.

Nothing failed outright: every backup restored with the same counts, every upgrade completed and passed the
integrity check, and no call needed more than 2 s.

## Sizing

Rules of thumb from the numbers above. They scale with item count and content size, not with feed count.

- **Disk.** Plan about 10 KB per item for a typical mix of summaries and articles (this library: 11 KB). A feed
  at the default retention keeps 250 items, so 500 feeds is at most 125,000 items, about 1.4 GB. Image
  thumbnails are cached separately and are not in the database. Keep free space of at least 2.5 times the
  database: the pre-upgrade snapshot needs 1.1 times plus 64 MB, the in-app backup needs 2.2 times plus 16 MB, and the
  database needs room to grow.
- **Memory.** Idle use is about 60 MB and the peak at 500 feeds and 150,000 items was 125 MB. The 256 MB limit in the
  compose example fits this size; memory follows the work in flight (a refresh, a trim), not the library size.
- **CPU and time.** One core is enough for the interactive calls. Backup is about 20 s per GB of database, restore
  about 16 s per GB, and the pre-upgrade snapshot about 3.5 s per GB. A restart after an upgrade is not
  reachable until the snapshot and migration finish: about 7 s at 1.6 GB, so the 40 s start period of the image's
  health check covers a database up to roughly 10 GB on a disk like this one, and less on a slow disk.
- **In-app backup limit.** The in-app export refuses a database over 4 GiB; at this item size that is about 390,000
  items. Beyond it, take the nightly snapshot from the server instead (see deploy.md).
- **Slower storage.** Everything above ran on a local SSD. On a network volume, an SD card or a spinning disk, expect
  the write-heavy jobs (trim, backup, upgrade, restore) to take several times longer, and check #228 and #229
  before running a library at this size on such a disk.
- **Search.** Common-word search cost grows with the number of matching items, and 150,000 items is the size where it
  comes within 2 times of its 500 ms budget on this machine (smaller libraries were not measured).

## What is not measured

A cold file cache (a start after a reboot), a network between client and server, the web app's own rendering, many
simultaneous clients, feeds that return changed content for items the library already has, a graceful shutdown, and
full-text extraction. The refresh numbers assume the feeds answer at once; a real refresh is bounded by the feeds'
own servers and by the per-host limit.
