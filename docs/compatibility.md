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
| **Environment variables** | The variables in [.env.example](../.env.example) (`KIPPLE_ADDR`, `KIPPLE_DATA`, `KIPPLE_PUBLIC_URL`, `KIPPLE_TRUSTED_PROXY_IPS`, and the rest) and `TZ`, with the behavior documented there, including which of them only seed a setting. |
| **CLI subcommands** | `serve`, `healthcheck`, `import`, `restore`, `password`, `api-password` and `version`, with the flags and exit statuses documented in [deploy.md](deploy.md). The text printed by `version -v` is for people to read and may gain lines. |
| **Image tags** | `ghcr.io/wptk/kipple:X.Y.Z` is immutable once published. `X.Y` and `X` move to the newest release of that line, and `latest` moves only to the newest stable release. A prerelease is tagged only with its exact version and never moves another tag. |
| **Volume layout** | Your data is the volume mounted at `/data`: `kipple.db`, the `backup/` folder, `imgcache/` and the other paths in [deploy.md](deploy.md#where-things-live). The container runs as uid 65532 and listens on 1919 unless `KIPPLE_ADDR` says otherwise. |

## Reader API

Kipple follows the Google Reader API as the two most widely used server implementations of it do (the "reference servers" in these docs, the ones sync clients
are built against); where they differ, the [endpoint notes](#endpoint-notes) say which way Kipple goes. Nothing depends
on which client is calling.

### Conventions

- **Base URL.** Point a client at `https://your-server/api/greader.php`. The root forms `/accounts/ClientLogin` and
  `/reader/api/0/…` (without the prefix) answer too, for clients that only take a server address. Doubled slashes and a
  repeated prefix are tolerated; nothing on these paths ever redirects. Because every `//` in a path is read as `/`, a
  feed URL given in the path (`stream/contents/feed/<url>`) is restored only after its scheme: a URL with an
  unencoded `?` or another `//` cannot be recovered there. Send such a stream as an encoded `s=` value, which always
  works.
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
  `items[].id` they are `tag:google.com,2005:reader/item/` plus 16 hex digits. A response of 1400 bytes or more is
  gzip-compressed when the request sends `Accept-Encoding: gzip` (with `Vary: Accept-Encoding` and a weak `ETag`).
- **Browser clients (CORS).** Every path answers `Access-Control-Allow-Origin: *` and exposes `ETag`, `Retry-After`
  and the bad-token headers. An `OPTIONS` preflight answers `204` without authentication, allowing `GET`, `POST` and
  the `Authorization`, `Content-Type` and `If-None-Match` headers. Credentials (cookies) are never involved; send the
  token in `Authorization` or `T`.
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
| `/reader/api/0/subscription/quickadd` | `POST` | `quickadd` (a feed URL or a site or page address, `feed/` prefix optional) | `{"numResults":1,"query","streamId":"feed/<n>","streamName"}`, or `{"numResults":0,"query","error"}` |
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
and `content.content` (both the article HTML), `categories` (`reading-list`, the feed's folder label, and `read` or `starred` when they apply),
`origin` (`streamId`, `title`, `htmlUrl`) and `enclosure` when the item has any.

### Stream ids

| Stream | Items |
|---|---|
| `user/-/state/com.google/reading-list` (or no stream) | every item |
| `user/-/state/com.google/starred` | starred |
| `user/-/state/com.google/read` | read |
| `user/-/state/com.google/unread`, `…/kept-unread` | unread |
| `user/-/state/com.google/broadcast`, `…/like` | none |
| `user/-/label/<folder>` | the folder's own feeds, not its subfolders' (see [Folders and flat-folder clients](#folders-and-flat-folder-clients)) |
| `feed/<n>` or `feed/<feed URL>` | one feed |

`user/<any user id>/…` is the same as `user/-/…`. `it` keeps only items in a state stream and `xt` drops them (`read`,
`unread`, `kept-unread`, `starred`); both repeat and combine. `ot` and `nt` are Unix seconds: `ot` returns items crawled
from two minutes before it (so a client clock that is slightly ahead loses nothing), `nt` items crawled at or before
it. `c` is the `continuation` of the previous page. `r=o` lists oldest first.

**Item ids** may be sent in any of these forms, mixed in one request: `tag:google.com,2005:reader/item/<hex>` (padded
to 16 digits or not), 16 bare hex digits, decimal, or `0x<hex>`. An id that cannot be parsed is skipped.

**edit-tag** understands `read`, `kept-unread` and `starred` (`a=…/read` and `r=…/kept-unread` mark read, `r=…/read`
and `a=…/kept-unread` mark unread). **mark-all-as-read** marks the unread items of `s` that were crawled at or before
`ts` (Unix seconds, milliseconds, microseconds or nanoseconds, told apart by length), or all of them without `ts`.
`s` may be the reading list, `unread`, `kept-unread`, `starred`, a folder label or a feed; `read` and unknown streams
change nothing.

### Folders and flat-folder clients

Folders nest in Kipple (at most 8 levels), but Reader API labels are flat. A Reader API client sees a nested folder as one
label named by its full path, so `Local` inside `News` is the label `News/Local`, and a flat-folder client shows it as a
single folder with that name. Four consequences follow, and each is the same on every Reader API client.

- **A label with a slash nests.** When a client subscribes a feed with, or moves a feed to, a label such as `News/Local`,
  Kipple resolves it against the tree: the longest prefix that is an existing folder's full path becomes the parent, and the
  rest becomes folders below it. If a top-level `News` exists, the feed lands in `Local` inside `News`, not in a new
  top-level folder named `News/Local`. A folder's filters (mute, mark as read and the rest) apply to the feeds of its
  subfolders, so the filters of `News` also apply to the feed in `News/Local`, although the client shows `News/Local` as a
  separate folder. If no folder matches a prefix, the whole label becomes folders at the top level, split at each `/`.
  A top-level folder whose name itself contains a slash (`AC/DC`) keeps being found by its full path.
- **A label the folder rules refuse is ignored.** Kipple refuses a label that would create a folder when it has an empty
  level (`News/`, `/News`, `A//B`, or a level of only spaces), more than 8 levels, a level longer than 100 characters or
  holding a control character, or a place below the Uncategorized folder. A label that already names a folder is used
  as it is. Otherwise the request still answers OK, so the client shows no error, and the server log records the reason.
  Kipple creates no folder, not even the upper levels it had started to walk. A feed that is new goes to Uncategorized, and
  a feed that already exists stays in its folder. For `subscription/edit` with `ac=edit`, a refused label rolls back the
  whole edit: a title change in the same request is dropped, and in a batch no feed moves. For `ac=subscribe` on a
  feed that already exists, the title is still applied. The client shows the folder it asked for until its next sync, and
  then the old folders come back.
- **Deleting a folder deletes its subfolders.** `disable-tag` on `News` deletes `News/Local` and every other folder below
  it, moves all their feeds to Uncategorized, and deletes the filters scoped to those folders. A flat-folder client does not
  show that `News` held other folders, so it cannot warn you. The web app's delete does the same, with a warning.
- **A rename that would merge folders is refused when the folder has subfolders.** `rename-tag` onto the label of an
  existing folder merges the two folders. That is allowed for a folder without subfolders, but not for one that has them,
  because two trees would have to be merged. A refused merge is answered OK, logged, and changes nothing, so the client
  shows the merge until its next sync, and then the old folders come back.

A folder's filters always cover the feeds of its subfolders, and deleting a parent from a flat-folder client always deletes
its subfolders. Create and rename nested folders in the web app, delete folders there too (it warns first), and keep
slashes out of the labels you type in a flat-folder client.

### Endpoint notes

Where the Google Reader API and its server implementations differ, or where Kipple does something on purpose that a client
might not expect:

- **ClientLogin failure** is `401`, as on both reference servers. The original Google service answered `403`.
- **`mark-all-as-read` on the `unread` or `kept-unread` stream** marks every unread item in it read, the same as the
  reading list. One reference server does this for `unread`; the other treats both as a no-op. Kipple follows the first. Only unread items are ever marked,
  so these streams hold exactly the items the request is about, and doing nothing would drop the user's action.
- **The token** is the same value from `ClientLogin` and `token`, and it is not 57 characters long. `T` is compared
  after trimming surrounding whitespace, so the `token` body may be sent back as it came.
- **`subscription/list`** has no `firstitemmsec` or `sortid`, and categories carry `id` and `label` but no `type`
  (the shape the reference servers use). Each feed has exactly one category, its folder; there are no feeds outside a folder (they are
  in Uncategorized, the default folder).
- **`tag/list`** lists folders only (`type` `folder`); Kipple has no per-item tags.
- **`edit-tag`** ignores `user/-/label/…` values: a folder is a property of a feed, never of an item.
  `broadcast`, `like`, `tracking-*` and unknown tags are ignored too.
- **`stream/items/ids`** reads one `s` (the first), and returns `itemRefs` with `id` only: no `timestampUsec` and no
  `directStreamIds`, even with `includeAllDirectStreamIds=true`.
- **JSON only.** `output=atom` or `xml` still returns JSON.
- **`subscription/quickadd` and `subscription/edit ac=subscribe`** answer without any network request, with the
  address stored as given (a bare `example.com`, `feed://…` and surrounding spaces are read as the URL they mean). The
  feed is fetched within the next scheduler tick (about 30 seconds). When the address is a web page, that first fetch
  takes the first feed the page links and makes it the subscription's `url`; when that feed is already subscribed, the
  new subscription is removed instead of becoming a duplicate (the next `subscription/list` does not list it).
- **Unsubscribing** a feed that has starred items keeps those items in a hidden archive feed; its items still appear
  in the reading list and starred streams with an `origin.streamId` that is not in `subscription/list`.
- **New items of a feed with full-text extraction on** are held back from every listing for up to 60 seconds after
  they arrive (30 seconds by default), until the extracted text is ready, so a client never stores the short feed
  version.

### Running the conformance suite

The suite drives the real Reader API handler over HTTP as a client does (sign in, keep the token, list, read, write)
against a temporary database, and checks every endpoint, parameter and answer above. Each assertion names the
reference it follows: the Google Reader API, the reference servers, or Kipple's design document. From a checkout:

```
go test ./internal/greader -run Conformance -v
```

It needs Go and nothing else (no network, no Docker). The tests are in `internal/greader/conformance_*_test.go`.

## Browsers, phones and clients checked

What Kipple has been checked against, how, and what is left to a person. The test runs are described in
[uat-plan.md](maintainers/uat-plan.md) (Suite 1).

### Web app

Checked by `npm run uat -- --browsers all` and `npm run uat:keyboard -- --browser <engine>` (in `web/`) against a seeded
instance, headless, on Windows. Each run covers every screen in a light and a dark theme at desktop (1280 px), tablet
(768 px) and phone (390 px) widths: no console errors, no failed API calls, no WCAG 2.2 AA violations (axe-core), no
sideways scroll, no stray `undefined` or `NaN`, and the font choice reachable. The keyboard run checks Tab order, focus
indicators, Escape and focus return in menus and dialogs, and the shortcuts in the `?` overlay.

| Engine | Version tested | Stands for | Screens (Suite 1) | Keyboard run | Notes |
|---|---|---|---|---|---|
| Chromium | 153.0.8010.12 | Chrome, Edge, Brave, Opera | clean | clean | The default run. |
| Firefox | 155.0 | Firefox | clean | clean | No mobile emulation in Firefox, so its tablet and phone runs are touch devices at that width. |
| WebKit | 26.6 | Safari on macOS and iOS | clean | clean | Playwright's build of the engine, not Safari itself. Links are not in the Tab order by default, see below. |

Known engine behavior (not Kipple defects, nothing to fix in the page):

- **Safari and Tab.** Safari leaves links out of the Tab order unless "Press Tab to highlight each item on a webpage"
  is on in Safari's Advanced settings (or you use Option+Tab). Buttons and fields are reached either way. The sidebar
  and list rows are links, so with the default Safari setting use the shortcuts (`j`, `k`, `Enter`, `g` then `i`, `a`,
  `s`, `f`, `,`) or turn that setting on.
- **Firefox and the system color scheme.** Playwright cannot hold an emulated dark mode in Firefox on a page sent with
  `Cross-Origin-Opener-Policy`, which Kipple always sets, so the Firefox runs flip the browser's own preference
  instead. A real Firefox follows the system setting as any browser does.

### Phones

| Device | How it was checked | Result |
|---|---|---|
| iPhone, installed from Safari (Add to Home Screen) | By hand by the maintainer on a beta build: install, relaunch and stay signed in, swipe to mark read and to star or open More, the share sheet, and whether reading time is recorded in the installed app. | Passed. Not repeated by a script: a script cannot swipe or install. |
| Phone widths in the desktop browsers above | Suite 1 at 390 px as a touch device, and the keyboard run's gesture check. | Clean. |

Every swipe or long-press action also has a button or menu, so none is gesture-only: the row's Star button and its
More actions menu (mark read or unread, mark above or below), Back to list on an open article, Refresh all feeds for
pull to refresh, and Move to folder for reordering feeds.

### Sync clients

Kipple speaks the Google Reader API at `/api/greader.php`. Add a Google Reader compatible account and enter the full server URL, `/api/greader.php` included; a client that drops the path and uses only the host cannot sign in through a tunnel that protects the rest of the site. Sign in with the API password from Settings > Account & Devices. The request sequences the protocol uses are replayed against each release by the contract tests in CI; no client is promised beyond what those tests cover.

### Still checked by a person

- Installing the web app on a phone and using it there (touch swipes, the share sheet, safe-area spacing, staying
  signed in).
- A real Safari with its default keyboard setting, and any other browser or version not listed above.
- Connecting a real Reader API client: add, read, star, and mark all as read from it.

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

<!-- TODO(owner): state the oldest release that may upgrade directly to 1.x. The code applies every migration in order
     from any schema and enforces no minimum, but no test or recorded run proves a direct upgrade from the earliest
     releases. If confirmed, say which release an older install must upgrade through first. -->
