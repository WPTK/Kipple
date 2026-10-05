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
median of the runs. Other commands: `gen -schema 11` (or `10`, `6`) writes the library at an older schema, `rewind -db F
-schema N` does the same to a copy, and `feeds -dir scale-data` serves the feeds on its own. `gen` takes
`-feeds`, `-items`, `-seed` and `-stats-days`.

The data is deterministic for a seed: the same counts, sizes, text and read, starred and retention shape. Time stamps
are relative to the moment of generation, so two databases are not byte-identical.

## The library

| | |
|---|---|
| Feeds and folders | 500 feeds spread over 541 folders (25 at the top level, nested up to 5 levels deep) and the default folder |
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
- `kipple` built natively from main after nested folders (migration 0012) with `go build` (Go 1.27). The web app was not embedded, so the
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
| Cold start (process start to `/healthz` 200) | 0.2 to 0.3 s | 286 ms in the quiet run; 1.8 and 2.0 s in two runs that followed heavy file copying |
| Open the database at the current schema | 151 ms | 47, 164, 151 |
| Upgrade from the previous schema (11 to 12, nested folders: rebuilds the `folders` table) | 7.4 s | 7.2, 7.4, 17.8 |
| Upgrade from an older schema (6 to 12: 0007 rewrites every item, 0009 builds three statistics indexes) | 11.7 s | 11.7, 21.9, 9.7 |
| Backup, in-app export (build the zip) | 33 s | 33, 31, 33 |
| Backup download (loopback) | 6.5 s | 5.9, 6.5, 15.8 |
| Backup zip size | 655 MB | the 1,637 MB database compresses to 40 percent |
| `kipple restore` of that zip | 26.5 s | 26.5, 25.1, 46.3 |
| First start after the restore | 223 ms | 223, 197, 236 |

The upgrade is dominated by the pre-migration snapshot (`VACUUM INTO`, a 1.6 GB copy), not by the migration itself:
a typical figure is 6 to 7 s, and the slow runs were disturbed by the file copying around them. The snapshot costs
about 3.5 s per GB. A restored backup reported the same unread count as the original in every run.

An **old-format backup** restores. The library was copied to schema 10, a 0.5.0-beta.1 build took its own in-app
backup of it (686 MB, 500 feeds, 150,000 items), and the then-current build restored it in 26 s with checksums and the
integrity check passing, migrated it on the first start and found the planted rare term by search. A 0.3.0-beta.3 backup
(schema 9, empty library) restores and migrates as well. The repository holds no backup zip in its test fixtures (the
tests build their archives in code), so these two were made by building the old tags. They predate nested folders;
the 11 to 12 row above is the migration on the large library.

### Interactive calls (warm, median of 3 runs)

| Call | ms |
|---|---:|
| `GET /api/status` | 2.1 |
| `GET /api/items`, unread, page of 50 | 1.5 |
| same, 100 pages deep (5,000 items in) | 2.0 |
| all items / oldest first / one feed | 1.7 / 1.6 / 1.6 |
| starred (2,215 items) | 43 |
| `GET /api/items/{id}` | 0.5 |
| `GET /api/stats/summary` (year / all time) | 50 / 51 |
| `GET /api/health/feeds` | 4.7 |
| `GET /api/opml` | 4.7 |
| **`GET /api/bootstrap`** (what the web app loads when it opens: 541 folders and 500 feeds with unread counts, totals, settings) | **706** |

Every call except the bootstrap is a few milliseconds. The bootstrap counts unread items per feed over the whole items
table, so it is the call that grows with the library: about 0.7 s at 150,000 items, and expect it to scale roughly
linearly with the item count. Nothing here is over 2 s.

### Folders (a tree of 541 folders, 5 levels deep)

| Call | ms |
|---|---:|
| Unread list of a folder in the middle of the tree | 0.6 |
| Unread list of the deepest folder | 0.5 |
| Unread list of a top-level folder, with its whole subtree | 28.8 |
| All-items list of a top-level folder, with its whole subtree | 123 |
| Reader API `stream/contents` for a top-level label / the deepest label | 4.9 / 2.1 |
| Reader API `tag/list` / `unread-count` | 2.6 / 8.0 |

A folder's list covers its subfolders, so a top-level folder with a large share of the library costs more than a leaf
(123 ms for the all-items view of a top-level folder is the slowest list in this table, still far under 2 s).

**OPML import of a large folder tree.** One OPML file that creates nested folders, each holding one feed, imported into
the library above:

| Folders in the file | Result | Time |
|---|---|---:|
| 250 | imported | 1.0 s |
| 500 | imported | 3.1 s |
| 1,000, 2,000, 3,000 | HTTP 500 | 10 s each (the writer's deadline) |

The cost grows faster than linearly with the number of folders (doubling the folders from 250 to 500 tripled the time),
and an import of somewhere between 500 and 1,000 folders cannot finish in one write. This is known and PR #233 changes the import;
re-run the bench after it merges. The bootstrap after the failed imports (about 1,000 folders and feeds more) answers
in about 1.0 s.

### Full-text search (warm)

A search that matches more than 75,000 items is answered with `422 search_too_broad` after a bounded walk of
them (date order: every page, counting the matches past its cursor; relevance order: the first page), and so is one
that outruns the 500 ms budget. The planted common words match
more than 75,000 of the 150,000 items, so they are refused; a search for two of them matches about 52,000 and is
answered.

| Search | Date order (ms) | Relevance order (ms) | Answer |
|---|---:|---:|---|
| Common word (in most items) | 141 | 115 | refused (20 of 20) |
| Mid-frequency word (over 75,000 items) | 141 | 94 | refused (20 of 20) |
| Prefix while typing (`typing=1`) of a common word | 139 | 96 | refused (20 of 20) |
| Two common words (about 52,000 items) | 216 | 375 | answered (1 of 20 refused by relevance) |
| Rare word (5 items) | 1.1 | | answered |
| Word in about 450 items | 6.0 | | answered |
| Word that is not in the library | 0.6 | | answered |

The first five rows are the median of 20 searches after 4 warm-up searches on one run; the last three rows are the
median of 3 runs. Date order walks the matches once on every page; relevance order counts them first on the first page without
ranking them, so a refused search does not pay for ranking, and later pages are bounded by the budget alone. The dev machine was shared with other test runs while this was measured:
the same searches over three runs varied by 30 percent in median and the two-common-word search by relevance came
within the budget in only two of them. Search is the call that varies most from run to run, and a cold or contended
file cache (the first searches after a restart read the index from disk) moves a search of tens of thousands of
matches past the 500 ms budget. See [#229](https://github.com/WPTK/Kipple/issues/229).

### Reader API (warm, median of 3 runs)

| Call | ms | Body | Body, gzip |
|---|---:|---:|---:|
| `subscription/list` | 4.7 | 123 KB | 14 KB |
| `tag/list` | 3.7 | 33 KB | 3 KB |
| `unread-count` | 9.9 | 69 KB | 12 KB |
| `stream/contents`, 50 unread items | 6.9 | 307 KB | 75 KB |
| `stream/contents`, 250 unread items | 15.9 | 1,671 KB | 405 KB |
| `stream/items/ids`, 10,000 unread | 5.8 | 254 KB | 63 KB |
| `stream/contents`, starred, 50 | 6.3 | 319 KB | 77 KB |
| `stream/items/contents`, 50 ids (POST) | 5.3 | 307 KB | 75 KB |

All of them are fast; a sync client syncing a library of this size is limited by the body size, not the server.
Items carry the article twice (`summary.content` and `content.content`), and a client that accepts gzip receives about
a quarter of the bytes for articles of this size (long full-text articles compress less); see "Response compression" below, which these times come from (uncompressed requests).

### Response compression

What `httpx.Compress` (gzip, `BestSpeed`) does to the largest responses of the library above, measured from a client
on the same machine over loopback: three runs, each call made six times per run, the first discarded, the median of
the rest, then the median of the three runs. "Level 1" and "level 6" compress the same uncompressed body offline with
`compress/gzip` at `BestSpeed` and at the default level, to compare the cost.

| Call | Uncompressed | gzip | Share | ms uncompressed | ms gzip | Level 1: KB / ms | Level 6: KB / ms |
|---|---:|---:|---:|---:|---:|---:|---:|
| `GET /api/bootstrap` | 190 KB | 22 KB | 12% | 29.0 | 30.2 | 22 / 0.5 | 18 / 1.0 |
| `GET /api/items`, unread, 50 | 36 KB | 13 KB | 36% | 2.6 | 2.7 | 13 / 0.5 | 12 / 0.5 |
| `subscription/list` | 123 KB | 14 KB | 11% | 4.7 | 4.7 | 14 / 0.5 | 12 / 0.5 |
| `tag/list` | 33 KB | 3 KB | 8% | 3.7 | 3.6 | 3 / 0.0 | 2 / 0.0 |
| `unread-count` | 69 KB | 12 KB | 17% | 9.9 | 10.2 | 12 / 0.5 | 11 / 0.5 |
| `stream/contents`, 50 unread items | 307 KB | 75 KB | 24% | 6.9 | 6.8 | 75 / 1.6 | 70 / 2.6 |
| `stream/contents`, 250 unread items | 1,671 KB | 405 KB | 24% | 15.9 | 18.9 | 405 / 6.8 | 378 / 12.8 |
| `stream/items/ids`, 10,000 | 254 KB | 63 KB | 25% | 5.8 | 5.8 | 63 / 1.0 | 66 / 2.1 |
| `stream/contents`, starred, 50 | 319 KB | 77 KB | 24% | 6.3 | 7.8 | 77 / 1.5 | 72 / 2.6 |
| `stream/items/contents`, 50 ids (POST) | 307 KB | 75 KB | 24% | 5.3 | 6.3 | 75 / 1.3 | 70 / 2.2 |

- Every large response shrinks to between a tenth and a third. An item page, which carries each article twice, is
  a quarter of its uncompressed size here: smaller than the same page with one copy and no compression, because this
  library's articles (3.4 KB of HTML on average) are short.
- The second copy of an article is nearly free only while the encoded article is shorter than gzip's 32 KB window:
  gzip then finds the second copy as a repeat of the first. A longer article, such as a full-text extraction, about
  doubles on the wire. One item's `summary` and `content`, compressed at `BestSpeed` (synthetic prose, so the
  compressed sizes are a plausible shape, not an exact one):

  | Article | One copy, gzip | Both copies, gzip |
  |---|---:|---:|
  | 4 KB | 2.4 KB | 2.5 KB |
  | 16 KB | 8.9 KB | 9.1 KB |
  | 32 KB (34 KB once escaped as JSON) | 16.9 KB | 33.0 KB |
  | 100 KB | 50.3 KB | 99.8 KB |
  | 500 KB (the cap) | 247 KB | 493 KB |

- Memory: an item's article is encoded once, into a buffer the response reuses, and written twice, so the second copy
  costs no memory. Writing one item with a 500 KB article (`BenchmarkWriteItem500K` and `…First` in
  `internal/greader`) allocates about 0.6 MB for the first item of a response (the buffer grows to the encoded
  article) and about 10 KB for each later one, in 0.45 ms. Encoding the whole item as one value allocated 1.3 MB per
  item, in 1.3 ms.
- Over loopback, where bandwidth is free, compression costs at most about 3 ms on the largest call (250 full items)
  and nothing measurable on the others. Over a real network the time saved sending three quarters fewer bytes is far
  larger: at 20 Mbit/s the 250-item page takes about 0.7 s uncompressed and 0.17 s compressed.
- `BestSpeed` was chosen over the default level: the default saves 7 percent more bytes on the largest body for twice
  the CPU time (12.8 ms against 6.8 ms), and on the smaller ones the two are within a kilobyte.

### Refresh and retention

| | |
|---|---|
| Refresh of all 500 feeds (local feed server, each feed returns 5 new items) | **20.0 s** (19.2, 20.0, 21.9); 500 fetched, 0 errors, 2,500 new items. On the same library with 25 flat folders it took 3.3 s; see [#237](https://github.com/WPTK/Kipple/issues/237) |
| Trim after that refresh (feeds at their cap lose the surplus) | 334 items, inside the refresh time |
| A reader during the refresh | median 2.3 ms, worst 199 ms |
| A write (star toggle) during the refresh | median 69 ms, worst 135 ms |
| Lower the default retention from 250 to 100 (bulk trim) | 60 s for 37,063 items (60, 60, 115), about 1.6 ms per item |
| A reader during the bulk trim | median 2.1 ms, worst 7 ms (2.6 s in the disturbed run) |
| A write during the bulk trim | median 139 ms, worst 0.3 s (6.8 s in the disturbed run) |

The bulk trim is the slowest background job and the one that waits longest on the single writer; see
[#228](https://github.com/WPTK/Kipple/issues/228).

### Memory (resident size)

| | MB |
|---|---:|
| Idle after start (10 s) | 62 |
| During the interactive calls above | 85 |
| During searches | 77 |
| During the in-app backup | 60 |
| During the refresh | 115 |
| During the bulk trim (the highest) | 129 |
| After the refresh, settled | 115 |

A soak of 5 rounds of 60 mixed list, search and bootstrap calls kept the resident size flat (about 53 MB after each
round, 77 right after the searches), so nothing grew with the number of requests. The peak is during the bulk trim and
the refresh, where it stays under 130 MB, within the 256 MB of the compose example.

With the compose example's `GOMEMLIMIT=64MiB` (three runs, on the library before nested folders) nothing moved: idle
62 MB, peaks 128 MB (bulk trim) and 103 MB (refresh), common-word search 203 ms, refresh 2.9 s, bulk trim 65 s. The soft
limit is below the working set during a refresh or trim, and the Go runtime keeps going past it instead of stalling.

### Database file and WAL

| | MB |
|---|---:|
| Database after generation | 1,637 |
| After a refresh and a bulk trim of 37,063 items | 1,753 (trimmed content is kept for `retention.restore_days`) |
| WAL while idle | 0 |
| WAL peak during a refresh | 4.5 |
| WAL peak during the bulk trim | 14.5 (171 in one run of an earlier series; the file is cut back to 64 MB at the next checkpoint) |

The database file does not shrink after a trim: SQLite reuses the freed pages for new items, so the size settles
instead of growing.

## Findings

Anything that failed or was slow enough to matter is an issue:

- [#228](https://github.com/WPTK/Kipple/issues/228): the bulk retention trim runs about 20 times slower than the
  budget in the code and holds the writer for about 4 s a batch against a 10 s deadline.
- [#229](https://github.com/WPTK/Kipple/issues/229): a common-word search at this size costs 100 to 200 ms and, matching
  more than 75,000 items, is refused as too broad (see Full-text search above).
- [#237](https://github.com/WPTK/Kipple/issues/237): a refresh of 500 feeds takes 20 s with the nested folder tree, 6
  times the flat-tree figure.
- OPML import of a folder tree fails somewhere between 500 and 1,000 folders (the writer's deadline); known, PR #233.

Nothing else failed: every backup restored with the same counts, every upgrade completed and passed the integrity
check, and no interactive call needed more than 2 s.

## Sizing

Rules of thumb from the numbers above. They scale with item count and content size, not with feed count.

- **Disk.** Plan about 10 KB per item for a typical mix of summaries and articles (this library: 11 KB). A feed
  at the default retention keeps 250 items, so 500 feeds is at most 125,000 items, about 1.4 GB. Image
  thumbnails are cached separately and are not in the database. Keep free space of at least 3.1 times the
  database plus 64 MB: an upgrade that migrates needs 3.1 times the database plus 64 MB when it rebuilds a table (2.1
  times plus 64 MB otherwise), with the pre-migration snapshot included, the in-app backup needs 2.2 times plus 16 MB, and the
  database needs room to grow.
- **Memory.** Idle use is about 60 MB and the peak at 500 feeds and 150,000 items was 130 MB. The 256 MB limit in the
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
- **Search.** Search cost grows with the number of matching items. A search that matches more than 75,000 items is
  refused as too broad whatever the load, so in a library over about 75,000 items a common word is always refused
  and the user adds a more specific word; a search that matches fewer is answered unless it runs past the 500 ms
  budget, which a slow disk or CPU can cause for tens of thousands of matches.

## What is not measured

A cold file cache (a start after a reboot), a network between client and server, the web app's own rendering, many
simultaneous clients, feeds that return changed content for items the library already has, a graceful shutdown, and
full-text extraction. The refresh numbers assume the feeds answer at once; a real refresh is bounded by the feeds'
own servers and by the per-host limit.
