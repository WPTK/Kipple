# Compatibility and deprecation promise

This page says what stays the same across Kipple 1.x, what may change, and how a removal is announced. It follows the
shape of Go's compatibility promise: a named surface is promised, everything else is explicitly not.

Kipple uses [Semantic Versioning](https://semver.org/). Within 1.x, a release changes the covered surface below only
by adding to it, unless the deprecation rule below has run its course. A change that breaks the covered surface
without that notice is a bug, and the fix is a new release that restores it.

## What is covered

If you only use Kipple through these, you can upgrade within 1.x without changing anything:

| Surface | What is promised |
|---|---|
| **Reader API** at `/api/greader.php` | The endpoints, parameters and response shapes in the Reader API section of [design.md](design.md#6-google-reader-api-mapping), as Reeder Classic and NetNewsWire use them. Releases may add endpoints, parameters and response fields; they do not remove or reinterpret existing ones. |
| **Backup zip** | The layout of an export (`kipple.db`, `feeds.opml`, `settings.json`, `manifest.json`, `RESTORE.txt`) and the manifest `format` number. A newer 1.x restores any export written by an earlier release of the line. An older Kipple refuses a database from a newer one. |
| **Settings keys** | The keys that appear in a backup's `settings.json`, with their meaning and value format. A key may gain new accepted values; it is not renamed or repurposed without notice. The `sys.*` entries are internal and not covered. |
| **Environment variables** | The variables in [.env.example](../.env.example) (`KIPPLE_ADDR`, `KIPPLE_DATA`, `KIPPLE_PUBLIC_URL`, `KIPPLE_TRUSTED_PROXY_IPS`, and the rest) and `TZ`, with the behavior documented there. |
| **CLI subcommands** | `serve`, `healthcheck`, `import`, `restore`, `password`, `api-password` and `version`, with the flags and exit statuses documented in [deploy.md](deploy.md). The text printed by `version -v` is for people to read and may gain lines. |
| **Image tags** | `ghcr.io/wptk/kipple:X.Y.Z` is immutable once published. `X.Y` and `X` move to the newest release of that line, and `latest` moves only to the newest stable release. A prerelease is tagged only with its exact version and never moves another tag. |
| **Volume layout** | Your data is the volume mounted at `/data`: `kipple.db`, the `backup/` folder, `imgcache/` and the other paths in [deploy.md](deploy.md#where-things-live). The container runs as uid 65532 and listens on 1919 unless `KIPPLE_ADDR` says otherwise. |

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
- Third-party clients other than Reeder Classic and NetNewsWire. Other Google Reader API clients may work, and a
  fix for one will not be held back by this promise.

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
