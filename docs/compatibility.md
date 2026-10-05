# Compatibility and deprecation promise

This page says what stays the same across Kipple 1.x, what may change, and how a removal is announced. It follows the
shape of Go's compatibility promise: a named surface is promised, everything else is explicitly not. It also lists the
Reader API that sync clients use, endpoint by endpoint, and how to run the test suite that checks it.

Kipple uses [Semantic Versioning](https://semver.org/). Within 1.x, a release changes the covered surface below only
by adding to it, unless the deprecation rule below has run its course. A change that breaks the covered surface
without that notice is a bug, and the fix is a new release that restores it.

## What is covered

If you only use Kipple through these, you can upgrade within 1.x without changing anything:

| Surface | What is promised |
|---|---|
| **Reader API** at `/api/greader.php` | The endpoints, parameters and response shapes in [Reader API](#reader-api) below (specified in detail in the Reader API section of [design.md](design.md#6-google-reader-api-mapping)), for any client that speaks the Google Reader API. Releases may add endpoints, parameters and response fields; they do not remove or reinterpret existing ones. |
| **Backup zip** | The layout of an export (`kipple.db`, `feeds.opml`, `settings.json`, `manifest.json`, `RESTORE.txt`) and the manifest `format` number. A newer 1.x restores any export written by an earlier release of the line. An older Kipple refuses a database from a newer one. |
| **Settings keys** | The keys that appear in a backup's `settings.json`, with their meaning and value format. A key may gain new accepted values; it is not renamed or repurposed without notice. The `sys.*` entries are internal and not covered. |
| **Environment variables** | The variables in [.env.example](../.env.example) (`KIPPLE_ADDR`, `KIPPLE_DATA`, `KIPPLE_PUBLIC_URL`, `KIPPLE_TRUSTED_PROXY_IPS`, and the rest) and `TZ`, with the behavior documented there. |
| **CLI subcommands** | `serve`, `healthcheck`, `import`, `restore`, `password`, `api-password` and `version`, with the flags and exit statuses documented in [deploy.md](deploy.md). The text printed by `version -v` is for people to read and may gain lines. |
| **Image tags** | `ghcr.io/wptk/kipple:X.Y.Z` is immutable once published. `X.Y` and `X` move to the newest release of that line, and `latest` moves only to the newest stable release. A prerelease is tagged only with its exact version and never moves another tag. |
| **Volume layout** | Your data is the volume mounted at `/data`: `kipple.db`, the `backup/` folder, `imgcache/` and the other paths in [deploy.md](deploy.md#where-things-live). The container runs as uid 65532 and listens on 1919 unless `KIPPLE_ADDR` says otherwise. |

## Reader API

Kipple follows the Google Reader API as FreshRSS and Miniflux implement it, the two server implementations sync clients
are built against; where they differ, the [endpoint notes](#endpoint-notes) say which way Kipple goes. Nothing depends
on which client is calling.

### Conventions

- **Base URL.** Point a client at `https://your-server/api/greader.php`. The root forms `/accounts/ClientLogin` and
  `/reader/api/0/…` (without the prefix) answer too, for clients that only take a server address. Doubled slashes and a
  repeated prefix are tolerated; nothing on these paths ever redirects.
- **Sign-in.** `ClientLogin` with the account's user name as `Email` and its Reader API password (set in Settings or
  with `kipple api-password`) as `Passwd`. The `Auth` value it returns is the token.
- **Authentication.** Send `Authorization: GoogleLogin auth=<token>` on every call. A `POST` may instead carry the token
  as `T` in the body or the query. With the header, `T` may also be absent, empty or `x`. A `GET` authenticates only by
  the header. Tokens do not expire; changing the API password revokes every token at once.
- **Parameters.** A `POST` may carry its parameters in the body, the query string or both (body first). Bodies are read
  as `application/x-www-form-urlencoded` whatever the `Content-Type` says, or as `multipart/form-data` when they are.
  Repeated keys (`i`, `s`, `a`, `r`, `t`, `it`, `xt`) are kept in order. Values may leave `=`, `;`, `,` and `$`
  unencoded.
- **Responses.** JSON is `application/json; charset=utf-8` whatever `output` says; JSON values are never `null` (except
  `LSID` in a JSON sign-in). Writes answer `200 text/plain` `OK`. Item ids in `itemRefs` are decimal strings; in
  `items[].id` they are `tag:google.com,2005:reader/item/` plus 16 hex digits.
- **Status codes.** `401` with `X-Reader-Google-Bad-Token: true` and `Google-Bad-Token: true` for a missing, wrong or
  revoked token (sign in again); `405` for a write endpoint called without `POST`; `413` for a body over 4 MiB (64 KiB
  before the client has authenticated); `400` for more than 20,000 parameters and for an unreadable OPML import; `404`
  for a path under `/api/greader.php` that is not part of the API; `503`
  with `Retry-After` while a search-index rebuild holds the database (retry); `500` only for a database failure. Data a
  client sends that Kipple cannot use (an unknown id, feed, folder or stream) is never an error: the write answers
  `OK` and changes nothing, the read returns an empty list. Never `403`, `429` or a redirect.

### Endpoints

Paths are below `/api/greader.php`. Every endpoint except the first three and `/icon/…` needs the token.

| Endpoint | Method | Parameters | Answer |
|---|---|---|---|
| `/` | any | | `OK` (a reachability probe) |
| `/check/compatibility` | any | | `PASS` |
| `/accounts/ClientLogin` | `POST` or `GET` | `Email`, `Passwd`, `output=json` | `SID=…`, `LSID=null`, `Auth=…` lines, or `{"SID","LSID","Auth"}` with `output=json`. Failure: `401` `Error=BadAuthentication` |
| `/reader/api/0/token` | `GET` | | the token, plain text, newline-terminated (the same value as `Auth`) |
| `/reader/api/0/user-info` | `GET` | | `{"userId","userName","userProfileId","userEmail"}` |
| `/reader/api/0/subscription/list` | `GET` | `output` | `{"subscriptions":[{"id":"feed/<n>","title","categories":[{"id":"user/-/label/<folder>","label"}],"url","htmlUrl","iconUrl"}]}`, with an `ETag` (`If-None-Match` gives `304`) |
| `/reader/api/0/tag/list` | `GET` | `output` | `{"tags":[{"id":"user/-/state/com.google/starred"},{"id":"user/-/state/com.google/reading-list"},{"id":"user/-/label/<folder>","type":"folder"}…]}`, with an `ETag` |
| `/reader/api/0/unread-count` | `GET` | `output` | `{"max":<total>,"unreadcounts":[{"id","count","newestItemTimestampUsec"}]}`: the reading list, then each folder, then each feed with unread items |
| `/reader/api/0/stream/items/ids` | `GET` (or `POST`) | `s`, `n` (default 20, at most 100,000), `r=o`, `c`, `ot`, `nt`, `it`, `xt` | `{"itemRefs":[{"id":"<decimal>"}],"continuation":"<c>"}`; `continuation` only when there is a next page |
| `/reader/api/0/stream/contents[/<stream>]` | `GET` (or `POST`) | the stream in the path or `s`, `n` (default 20, at most 1000), `r=o`, `c`, `ot`, `nt`, `it`, `xt` | `{"id":<stream>,"updated":<seconds>,"items":[…],"continuation"}` |
| `/reader/api/0/stream/items/contents` | `POST` (or `GET`) | `i` (repeated, up to 1000), `r=o` | `{"id":"user/-/state/com.google/reading-list","updated","items":[…]}` |
| `/reader/api/0/edit-tag` | `POST` | `i` (repeated, up to 10,000), `a`, `r` (repeated) | `OK` |
| `/reader/api/0/mark-all-as-read` | `POST` | `s`, `ts` | `OK` |
| `/reader/api/0/subscription/quickadd` | `POST` | `quickadd` (a feed URL, `feed/` prefix optional) | `{"numResults":1,"query","streamId":"feed/<n>","streamName"}`, or `{"numResults":0,"query","error"}` |
| `/reader/api/0/subscription/edit` | `POST` | `ac` (`subscribe`, `edit`, `unsubscribe`), `s` (repeated), `t` (one per `s`), `a`, `r` | `OK` |
| `/reader/api/0/rename-tag` | `POST` | `s`, `dest` | `OK` |
| `/reader/api/0/disable-tag` | `POST` | `s` (repeated) | `OK` |
| `/reader/api/0/subscription/import` | `POST` | the OPML document as the body (header authentication only) | `OK`, or `400 Bad OPML` |
| `/reader/api/0/subscription/export` | `GET` | | the OPML file |
| `/icon/<id>-<hash>` | `GET` | | a feed's icon, the `iconUrl` of `subscription/list`; no token needed |
| any other `/reader/api/0/…` | any | | `200` `[]` |

`ck`, `client`, `includeAllDirectStreamIds`, `merge`, `likes`, `comments`, `mediaRss` and `types` are accepted and
ignored everywhere, and so is `output` except on `ClientLogin`.

**Items** in `stream/contents` and `stream/items/contents` carry `id`, `crawlTimeMsec` and `timestampUsec` (strings),
`published` and `updated` (seconds), `title`, `author`, `canonical` and `alternate` (`[{"href"}]`), `summary.content`
(the article HTML), `categories` (`reading-list`, the feed's folder label, and `read` or `starred` when they apply),
`origin` (`streamId`, `title`, `htmlUrl`) and `enclosure` when the item has any.

### Stream ids

| Stream | Items |
|---|---|
| `user/-/state/com.google/reading-list` (or no stream) | every item |
| `user/-/state/com.google/starred` | starred |
| `user/-/state/com.google/read` | read |
| `user/-/state/com.google/unread`, `…/kept-unread` | unread |
| `user/-/state/com.google/broadcast`, `…/like` | none |
| `user/-/label/<folder>` | the folder's own feeds (a nested folder is the label `Parent/Child`) |
| `feed/<n>` or `feed/<feed URL>` | one feed |

`user/<any user id>/…` is the same as `user/-/…`. `it` keeps only items in a state stream and `xt` drops them (`read`,
`unread`, `kept-unread`, `starred`); both repeat and combine. `ot` and `nt` are Unix seconds: `ot` returns items crawled
from two minutes before it (so a client clock that is slightly ahead loses nothing), `nt` items crawled at or before
it. `c`
is the `continuation` of the previous page. `r=o` lists oldest first.

**Item ids** may be sent in any of these forms, mixed in one request: `tag:google.com,2005:reader/item/<hex>` (padded
to 16 digits or not), 16 bare hex digits, decimal, or `0x<hex>`. An id that cannot be parsed is skipped.

**edit-tag** understands `read`, `kept-unread` and `starred` (`a=…/read` and `r=…/kept-unread` mark read, `r=…/read`
and `a=…/kept-unread` mark unread). **mark-all-as-read** marks the unread items of `s` that were crawled at or before
`ts` (Unix seconds, milliseconds, microseconds or nanoseconds, told apart by length), or all of them without `ts`.
`s` may be the reading list, `unread`, `kept-unread`, `starred`, a folder label or a feed; `read` and unknown streams
change nothing.

### Endpoint notes

Where the Google Reader API, FreshRSS and Miniflux differ, or where Kipple does something on purpose that a client
might not expect:

- **ClientLogin failure** is `401`, as on FreshRSS and Miniflux. The original Google service answered `403`.
- **The token** is the same value from `ClientLogin` and `token`, and it is not 57 characters long. `T` is compared
  after trimming surrounding whitespace, so the `token` body may be sent back as it came.
- **`subscription/list`** has no `firstitemmsec` or `sortid`, and categories carry `id` and `label` but no `type`
  (the FreshRSS shape). Each feed has exactly one category, its folder; there are no feeds outside a folder (they are
  in the default folder).
- **`tag/list`** lists folders only (`type` `folder`); Kipple has no per-item tags.
- **`edit-tag`** ignores `user/-/label/…` values: a folder is a property of a feed, never of an item.
  `broadcast`, `like`, `tracking-*` and unknown tags are ignored too.
- **Items** carry the article in `summary.content` only, not also in `content.content` (the FreshRSS shape). In the
  original API a client reads `content` or `summary`, whichever is present.
- **`stream/items/ids`** reads one `s` (the first), and returns `itemRefs` with `id` only: no `timestampUsec` and no
  `directStreamIds`, even with `includeAllDirectStreamIds=true`.
- **JSON only.** `output=atom` or `xml` still returns JSON.
- **`subscription/quickadd` and `subscription/edit ac=subscribe`** take the feed's own URL. They do no network request
  and no feed discovery: the feed is fetched within the next scheduler tick (about 30 seconds), and a web page URL
  stays a feed that fails to fetch. The web app's add-feed does discovery.
- **Unsubscribing** a feed that has starred items keeps those items in a hidden archive feed; its items still appear
  in the reading list and starred streams with an `origin.streamId` that is not in `subscription/list`.
- **New items of a feed with full-text extraction on** are held back from every listing for up to 60 seconds after
  they arrive (30 seconds by default), until the extracted text is ready, so a client never stores the short feed
  version.
- **No CORS headers.** A browser page on another origin cannot call the API, and an `OPTIONS` request is a `401`.

### Running the conformance suite

The suite drives the real Reader API handler over HTTP as a client does (sign in, keep the token, list, read, write)
against a temporary database, and checks every endpoint, parameter and answer above. Each assertion names the
reference it follows: the Google Reader API, FreshRSS, Miniflux, or Kipple's design document. From a checkout:

```
go test ./internal/greader -run Conformance -v
```

It needs Go and nothing else (no network, no Docker). The tests are in `internal/greader/conformance_*_test.go`.

## What is not covered

These may change in any release, including a patch release:

- Internal Go packages. Kipple is an application, not a library.
- The database schema. Read or write the database only through Kipple, its backups and its CLI.
- The HTTP API that the web app uses (everything under `/api/` except `/api/greader.php`). It belongs to the web app
  that is shipped in the same image.
- The user interface: layout, wording, themes, fonts, keyboard shortcuts and screens.
- Log lines and their fields. Do not build alerts on their exact text.
- The contents of `/data/imgcache`, the nightly snapshot's file name, and other files marked transient in
  [deploy.md](deploy.md#where-things-live).
- Behavior that no document or test specifies, and a bug that something depends on.

Security fixes are exempt from everything on this page. A fix that has to change a covered surface does so, and the
release notes say what and why.

## Deprecation

Something covered is removed or changed incompatibly only after this notice:

1. A release announces the deprecation under **Deprecated** in [CHANGELOG.md](../CHANGELOG.md), with what to use
   instead.
2. The removal happens only in a major release (2.0.0 or later), after the release that announced the deprecation.
3. The removal is listed under **Removed** in the changelog of the release that makes it.

The changelog is the only place a deprecation is announced, so read the notes of each release you skip. Kipple never
contacts any server to check for updates.

## Upgrading and downgrading

- An upgrade within 1.x migrates the database on the first start. Before any schema migration Kipple writes a
  pre-migration snapshot, checks the free disk space first, and refuses to start rather than migrate with too little
  room. See [deploy.md](deploy.md#disk-space-during-an-upgrade).
- Migrations apply in order from whichever schema your database has. Read the release notes of every release you skip.
- **Downgrade means restoring the pre-migration snapshot.** There are no down migrations, and an older Kipple refuses a
  database that a newer one has migrated. See [deploy.md](deploy.md#roll-back-an-upgrade-that-migrated-the-schema).

<!-- TODO(owner): state which earlier versions may upgrade directly to 1.x. The code applies every migration in order
     from any schema and enforces no minimum, but no test or recorded run proves a direct upgrade from versions older
     than 0.7. If confirmed, say: "from 0.7.0 and later; an older install upgrades through 0.7.x first". -->
