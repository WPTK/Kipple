# Kipple: backups, recovery and phase 2 deploy

Naming in these docs: **Host-A** is the machine that runs Kipple (the app host) and **Host-B** is the machine you run
admin commands and backups from. Hostnames, addresses and paths are placeholders; substitute your own.

For the owner to merge into the local `deploy-local/RUNBOOK.md` (that file is gitignored, so this one
travels with the repo). Commands run from Host-B as `ssh host-a '...'`, in the compose project
`host-a` at `/home/user/stack`. Never a bare `docker compose up` or `down`: always name `kipple`.

The container is distroless: no shell, no `ls`, no `rm`. The binary is the entrypoint, so a
subcommand is `docker exec kipple /kipple <command>` on the running container, or
`docker compose run --rm -T --no-deps kipple <command>` on a stopped service (it starts a throwaway
container on the same volume, publishes no port and ignores the restart policy).

**Commands that pipe a file in (`... < file`) need Git Bash or cmd, not PowerShell.** PowerShell
refuses the line with `ParserError: The '<' operator is reserved for future use` before anything
runs. From a PowerShell prompt on Host-B, wrap the whole line: `cmd /c "ssh host-a '...' < file"`
(cmd passes the bytes through unchanged; do not use a PowerShell pipe, which can re-encode them).

## Where things live

Everything is on the `kipple_data` volume (`host-a_kipple_data`), mounted at `/data`.

| Path in the container | What | Kept |
|---|---|---|
| `/data/kipple.db` (+ `-wal`, `-shm`) | The database. Never copy it while the server runs | live |
| `/data/kipple.lock` | Held by `serve` (an OS lock: it vanishes with the process, no stale lock) | live |
| `/data/backup/kipple-snapshot.db` | Nightly snapshot at 04:10 (`tz` setting), consistent, safe to copy | 1 |
| `/data/backup/pre-migration-<from>-<to>-<ns>.db` | Written before a schema migration (`0600`; files written by 0.2.0 and earlier are `0644`) | newest 3 |
| `/data/backup/pre-restore-<YYYYMMDD-HHMMSS>Z/` (UTC; older versions wrote local time without the `Z`) | The database that `kipple restore` replaced | newest 3 |
| `/data/backup/export/` | Temporary files of an export in progress. Emptied at startup | transient |
| `/data/imgcache/` | Image cache (`imgproxy.cache_mb`, default 1024 MiB, least recently used evicted; never in backups or snapshots) | capped |
| `/data/restore-tmp.db*`, `/data/restore-upload.tmp` | Only while a `kipple restore` runs | transient |

These are all on the same disk as the database. They protect against a bad migration or a bad
restore, not against losing Host-A. An off-box copy is the export (below) or a `docker cp` of the
nightly snapshot.

To look inside the volume (there is no shell in the Kipple image):

    ssh host-a 'docker run --rm -v host-a_kipple_data:/data:ro alpine ls -la /data /data/backup'

## Export a backup (the button)

Settings, Export backup. The app calls `POST /api/backup`, which answers at once when the build is quick and otherwise with `202 {job_id}`; the app then polls `GET /api/backup/jobs/<id>` until it is `ready` (the build carries on if the browser tab closes or Cloudflare cuts the request at about 100 s; a big database can take minutes). It shows the `warning` text and the size,
then starts the download of `GET /api/backup/<token>`, which saves as
`kipple-backup-YYYYMMDD-HHMMSS.zip`. Save it off Host-A, for example on Host-B in
`<backup-dir>\kipple\`.

- The zip holds `kipple.db` (a consistent snapshot), `feeds.opml` (imports into any reader),
  `settings.json` (readable copy), `manifest.json` (versions, counts, SHA-256 of every file) and
  `RESTORE.txt`. It is roughly a third of the database size.
- **The file is sensitive.** It contains the web and Reader API password hashes, the account secret
  that signs Reader tokens and image links, hashed session ids, and any feed logins (HTTP Basic
  `user:password`, stored in plain text). Keep it private.
- The link is single use and expires after 5 minutes. If the transfer breaks, export again.
- "Busy" (409): the nightly snapshot or another export is running. Retry in a few seconds.
  "Not enough free disk space" (507): the volume needs about 2.2 times the database size free.
- Exports never block feed fetching: the snapshot only holds a read view.

The database only grows so much: an export is refused above 4 GiB. Past that, use the nightly
snapshot with `docker cp`:

    ssh host-a 'docker cp kipple:/data/backup/kipple-snapshot.db /tmp/k.db' && scp host-a:/tmp/k.db '<backup-dir>\kipple\' && ssh host-a 'rm /tmp/k.db'

That copies the snapshot only, never the live database or WAL.

## Import OPML

`kipple import [-mark-read-older-than-days N] <file.opml | ->` is safe while the server runs and prints JSON on standard output. The flag must come before the file. Pipe the file in, because the container user cannot read a bind-mounted `/import`:

    ssh host-a 'docker exec -i kipple /kipple import -' < feeds.opml

New feeds are fetched on the scheduler's next tick. `docker exec kipple /kipple version` prints the running build.

Health: `ssh host-a 'curl -s http://127.0.0.1:7080/healthz'` answers `ok`.

## Health check and container hardening

The image carries a `HEALTHCHECK` (every 30 s, 5 s timeout, 40 s start period, 3 retries) that runs
`/kipple healthcheck`. That subcommand does a GET on `http://127.0.0.1:<port>/healthz` (the port comes
from `KIPPLE_ADDR`; a `0.0.0.0`, `::` or empty host becomes `127.0.0.1`), waits at most 3 s and exits 0 only
on HTTP 200, otherwise printing one line and exiting 1. `/healthz` needs no login and touches no database:
it answers `ok` as long as the HTTP server is serving. So "healthy" means the process is up and answering,
not that feeds are fetching.

See it:

    ssh host-a 'docker ps --filter name=kipple'                       # STATUS shows (healthy) / (unhealthy) / (health: starting)
    ssh host-a "docker inspect -f '{{.State.Health.Status}} {{.State.Health.FailingStreak}}' kipple"
    ssh host-a "docker inspect -f '{{json .State.Health.Log}}' kipple"  # last 5 probe results and messages
    ssh host-a 'docker exec kipple /kipple healthcheck; echo $?'      # run the probe by hand

Docker only reports health; it does not restart an unhealthy container by itself (plain compose ignores
it). Use it for `docker ps`, monitoring and `depends_on: condition: service_healthy`.

`docker-compose.example.yml` also shows these runtime options (all verified by running the image with them):

| Option | What it does |
|---|---|
| `restart: unless-stopped` | Restarts after a crash or reboot, but not after you stopped it on purpose. |
| `stop_grace_period: 30s` | Time Docker waits after SIGTERM before killing. Kipple bounds its whole shutdown (HTTP drain, background work, closing the database) to 25 s, so the process finishes within 25 s and 30 s leaves margin before the kill. |
| no `init: true` | Not needed: Kipple is PID 1 and handles SIGTERM/Ctrl-C itself. |
| `mem_limit: 256m` + `GOMEMLIMIT=64MiB` | Hard cap plus a Go soft limit so the GC works harder before the cap is hit. |
| `pids_limit: 200` | Caps processes and threads. |
| `read_only: true` + `tmpfs: /tmp` | Root filesystem is read-only; the app writes only to `/data` (the volume). `/tmp` is a small RAM disk in case anything needs scratch space. |
| `cap_drop: [ALL]` | Removes all Linux capabilities; the app needs none (port 7080 is unprivileged). |
| `security_opt: no-new-privileges:true` | Blocks privilege escalation through setuid binaries. |
| logging `json-file` 10m x 3 | Bounded container logs. |

If you run the image with plain `docker run`, the same flags are `--read-only --tmpfs /tmp --cap-drop ALL
--security-opt no-new-privileges:true --pids-limit 200`.

### Host-A compose diff to apply at the next deploy

Nothing on Host-A has been changed yet. In the `kipple` service of `/home/user/stack/docker-compose.yml`:

    -    restart: always
    +    restart: unless-stopped
         stop_grace_period: 30s
         mem_limit: 256m
    +    pids_limit: 200
    +    read_only: true
    +    tmpfs:
    +      - /tmp:size=64m,mode=1777
    +    cap_drop:
    +      - ALL
    +    security_opt:
    +      - no-new-privileges:true

Also give the service's `build:` the version arguments from `docker-compose.example.yml` (keep its
existing context path), so the deploy command's `KIPPLE_VERSION`/`KIPPLE_VCS_REF` reach the build; without
them the binary reports version `dev`, because `.git` is not in the build context:

         build:
           context: /home/user/kipple
    +      args:
    +        VERSION: ${KIPPLE_VERSION:-dev}
    +        VCS_REF: ${KIPPLE_VCS_REF:-unknown}

No healthcheck line is needed: it comes from the image, so the rebuild picks it up. Before the deploy,
`docker compose ... config` shows the merged result; afterwards check `docker ps` reaches `(healthy)`
within about a minute. To back out, remove the hardening lines and `up -d kipple` again.

## Installing the app and offline reading

Kipple is an installable web app: open it in a browser and use "Add to Home Screen" (iOS) or "Install" (Chrome). The
service worker (`/sw.js`) needs HTTPS or `127.0.0.1`. If the site sits behind an access proxy, the manifest and the
worker script are fetched with the session cookie, so a signed-in browser is fine; a proxy that answers those two
files with a login page would stop installation and offline support, not reading. Offline, the app opens from its
cache, shows what it kept (the first page of Unread, and anything you opened) and queues stars and read marks until
the connection returns. Signing out clears all of it. A new build is picked up the next time the app is opened or
comes to the front, and a banner offers the reload.

## Reset the web password

Works while the server runs. At a terminal, without echo, asked twice:

    ssh -t host-a 'docker exec -it kipple /kipple password'

From a script or a pipe (one line on standard input; mind shell history and the process list):

    printf '%s\n' "$NEW" | ssh host-a 'docker exec -i kipple /kipple password --stdin'

Rules: 5 to 256 characters. It signs out every web session **and revokes every Reader API token**
(it rotates the account secret), so afterwards: sign in again in the browser, and re-enter the
Reader API password in Reeder and NetNewsWire (that password itself is unchanged; if you have lost
it too, `docker exec kipple /kipple api-password` sets a new one). The failed-login lockout is in
memory: it clears when its 15-minute window ends or on a restart. If `KIPPLE_PASSWORD` is still in
`/home/user/stack/.env`, remove it: it is read only when the account is first created.

When you can still sign in, change either password in Settings > Account instead; the CLI is the recovery path.

## Restore a backup

`kipple restore` replaces the database with a backup zip (or a bare `.db` such as a snapshot). It
refuses while the server runs (the lock), verifies checksums and integrity, refuses a database
from a newer Kipple than this binary, keeps the current database under
`/data/backup/pre-restore-<timestamp>/`, and signs every web session out. Without `--yes` it only
verifies and reports (and then exits with status 1 and "nothing was changed", which is expected).

Runbook, with the backup on Host-B (it is piped in; the container user cannot read `/import`):

    # 1. Stop the server (named service only).
    ssh host-a 'cd /home/user/stack && docker compose stop kipple'

    # 2. Verify without changing anything. Read the counts and the date it prints. It ends with "nothing was changed ..." and exit status 1; that is expected.
    ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore -' < kipple-backup-YYYYMMDD-HHMMSS.zip

    # 3. Restore for real.
    ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore - --yes' < kipple-backup-YYYYMMDD-HHMMSS.zip

    # 4. Start it and check the feed count and last fetch on /_status.
    ssh host-a 'cd /home/user/stack && docker compose up -d kipple && docker logs --tail 20 kipple'

An older schema is migrated on that start, after the usual `pre-migration-*` snapshot. Then sign in
again. A restore never touches a backup `.zip`.

**Undo a restore.** Stop `kipple`, then restore the previous database from the volume (list it with
the `alpine ls` command above):

    ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore /data/backup/pre-restore-<timestamp>/kipple.db --yes'

A bare `.db` restore also applies a `-wal` file sitting beside it (a pre-restore copy taken after an unclean stop keeps its newest transactions there), and says so. Two restores in the same second get `pre-restore-<ts>` and `pre-restore-<ts>-2`, never the same directory.

**Run restore as Kipple's own user.** `docker compose run` does that by default; do not add `-u root`. A restore run as root would leave a root-owned `0600` `kipple.db` that the server (nonroot) cannot open. If it detects root it warns and hands the new database, the pre-restore directory, `kipple.lock` and a `backup/` directory it created to the data directory's owner (the lock even when the restore stops early), but do not rely on that.

If the refusal says "kipple is running": the service is still up (`docker compose stop kipple`), or
a second `run` is open. Do not delete `kipple.lock`; it is not a file marker, the OS drops it.

Other refusals change nothing on the volume (no `pre-restore-*` directory, no temporary files
left): a damaged or truncated zip ("not a readable zip", "checksum mismatch"), a zip that is not a
Kipple backup (no `manifest.json`, or an entry with a directory part), and a backup from a newer
Kipple ("the database schema version N is newer than this Kipple binary (M): upgrade Kipple
first"). All exit with status 1.

### Restore onto a new, empty volume (lost volume, new host)

The same `restore -` command works when the volume does not exist yet: `docker compose run`
creates the named volume, and a new named volume is filled from the image's `/data`, which is
already owned by uid 65532 (Kipple's user), so nothing needs a `chown`. Only a bind mount (a host
directory at `/data`) needs `chown 65532:65532` on that directory first.

    # 1. The compose service and /home/user/stack/.env exist; the service is not running and has never started on this volume.
    # 2. Verify, then restore (the second command creates host-a_kipple_data if it is missing):
    ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore -' < kipple-backup-YYYYMMDD-HHMMSS.zip
    ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore - --yes' < kipple-backup-YYYYMMDD-HHMMSS.zip
    # 3. Start it.
    ssh host-a 'cd /home/user/stack && docker compose up -d kipple && docker logs --tail 20 kipple'

The account comes from the backup: sign in with the web password and use the Reader API password
that were current when the backup was taken. `KIPPLE_USERNAME`, `KIPPLE_PASSWORD` and
`KIPPLE_API_PASSWORD` in `.env` are ignored because the account already exists. The restore prints
"There was no previous database to keep." on an empty volume. The Reader API answered ClientLogin
and `unread-count` with the backup's password on the rehearsal; if Reeder or NetNewsWire reports an
authentication error, sign in again in the app with that password.

Do not start the server on the empty volume first: it would create a new, empty account, and the
restore then replaces that database anyway (it is kept under `pre-restore-*`), so it only adds a
step.

## Roll back an upgrade that migrated the schema

There are no down migrations. An older binary refuses a newer schema, so going back means
restoring the `pre-migration-<old>-<new>-<ns>.db` that the upgrade wrote before migrating.
Anything read, starred or fetched since the upgrade is lost.

If the old image is started on the migrated database without these steps, it does not start:
`docker logs kipple` shows `kipple: store: store: database schema version 5 is newer than this
binary (3); refusing to start` and the container exits with status 1 (with `restart: always`
it keeps restarting). Nothing is changed on the volume. Stop it, then:

    # 1. Stop the service and find the newest pre-migration snapshot.
    ssh host-a 'cd /home/user/stack && docker compose stop kipple'
    ssh host-a 'docker run --rm -v host-a_kipple_data:/data:ro alpine ls -la /data/backup'
    # 2. Restore it with the NEW image (still built; old releases may have no restore command).
    ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-<old>-<new>-<ns>.db --yes'
    # 3. Check out the old tag, rebuild, start. Do not start the new image in between: it would migrate again.
    ssh host-a 'cd /home/user/kipple && git fetch --tags --force && git checkout <old tag>'
    ssh host-a 'cd /home/user/stack && KIPPLE_VERSION=<old tag> KIPPLE_VCS_REF=$(git -C /home/user/kipple rev-parse HEAD) docker compose build kipple && docker compose up -d kipple'
    # 4. Put the checkout back on a branch (the checkout above left a detached HEAD; the running image is not affected).
    ssh host-a 'cd /home/user/kipple && git checkout main'

The restore prints `schema version <old>` for the snapshot and moves the migrated database to
`backup/pre-restore-<ts>/`, so the roll-forward is one more restore away. Sign in again afterwards.
Rehearsed 2026-09-26 with v0.2.0-alpha.2 (schema 3) and 0.2.0 (schema 5): feeds, items, read and
starred state were identical after the upgrade and after the rollback, and the Reader API answered.

### Schema 5 -> 6 (the favicon finder)

The first build with the favicon finder adds migration 0006 (`feed_icon_checks`, a new empty
table; nothing is rewritten, so the first start is quick). Its first start writes
`/data/backup/pre-migration-5-6-<ns>.db`, then migrates. A binary without 0006 (0.3.0-alpha.2, or
an alpha.3 built before the finder was merged) refuses the schema-6 database with `database schema
version 6 is newer than this binary (5); refusing to start`, so a rollback is the procedure above
with that snapshot: stop kipple, `restore /data/backup/pre-migration-5-6-<ns>.db --yes` with the
new image, then check out, rebuild and start the old tag. Never copy the snapshot over `kipple.db`
by hand. Anything read or starred since the upgrade is lost; the icons found are simply looked up
again after the next upgrade.

## Phase 1 to phase 2 (done 2026-09-25, v0.2.0-alpha.1)

Historical: this applies to a schema-1 database. With a build after alpha 2 the snapshot is `pre-migration-1-<latest>-*` (schema 5 is the latest at the time of writing), not `pre-migration-1-3-*`. Phase 1 (`v0.1.0`) has no export button and no restore command, and phase 2 migrates the schema
(0002, 0003) on its first start. So:

1. **Take an off-box copy of the phase 1 data before building phase 2.** Stop the service so the
   WAL is checkpointed and `kipple.db` alone is complete, copy it out, then continue:

       ssh host-a 'cd /home/user/stack && docker compose stop kipple && docker cp kipple:/data/kipple.db /tmp/kipple-phase1.db'
       scp host-a:/tmp/kipple-phase1.db '<backup-dir>\kipple\kipple-phase1-YYYYMMDD.db'
       ssh host-a 'rm /tmp/kipple-phase1.db'

   (`docker cp` works on a stopped container.) The no-downtime alternative is the nightly
   `kipple-snapshot.db` (see above), up to a day old.
2. Pull, build, start (named service): at the time `git pull`, `docker compose build kipple`,
   `docker compose up -d kipple` (deploys now check out the release tag instead; see docs/RELEASING.md, step 9).
   The first start writes
   `/data/backup/pre-migration-1-3-<ns>.db` (on the volume) and migrates.
3. First thing in the new UI: **Export backup**, saved off-box. That is the first backup that
   contains phase 2 data.

### Roll back to phase 1

(Historical, with the numbers of the alpha 1 deploy; substitute `pre-migration-1-<latest>-*` for a newer build.)

There are no down migrations and the phase 1 binary refuses a newer schema, so a rollback restores
a schema-1 database. Everything read or starred since the snapshot is lost, so decide with that in
mind.

1. Stop the service: `docker compose stop kipple`.
2. Find the snapshot: `docker run --rm -v host-a_kipple_data:/data:ro alpine ls -la /data/backup`.
   Use the newest `pre-migration-1-3-*.db`, or the off-box `kipple-phase1-*.db` from step 1.
3. Restore it **with the phase 2 image, before rebuilding** (phase 1 has no restore command):

       ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-1-3-<ns>.db --yes'
       # or the off-box copy:
       ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore - --yes' < kipple-phase1-YYYYMMDD.db

4. Check out phase 1 on Host-A (`cd /home/user/kipple && git fetch --tags --force && git checkout v0.1.0`), then
   from `/home/user/stack`: `KIPPLE_VERSION=v0.1.0 docker compose build kipple && docker compose up -d kipple`.
   Afterwards `git checkout main` in `/home/user/kipple` puts the checkout back on a branch (the image is already built).
5. Sign in again (restore signed every session out).

## Phase 2 alpha 3 deploy notes

- **Schema 3 -> 5: this is a one-way migration.** Alpha 2 runs schema 3. The alpha 3 binary has two
  more migrations. The first start writes `/data/backup/pre-migration-3-5-<ns>.db`, then applies
  0004 (additive: filters, muted items, devices, auto-read) and **0005, which drops and rebuilds the
  whole search index (`items_fts`, porter tokenizer) and re-tokenizes every item**, so the first
  start takes longer in proportion to the size of the library (about 0.6 s for 5,600 items on the
  rehearsal; watch `docker logs kipple` for `store: applied migration`). An alpha 2 binary refuses
  a schema-5 database, so **there is no rollback path back to alpha 2 except restoring the
  pre-migration snapshot**, and anything read or starred since the migration is lost. Before
  deploying, take an Export backup from alpha 2 and save it off-box. Rollback: stop kipple, restore
  the snapshot with the alpha 3 image (before rebuilding), check out the old tag, rebuild, start:

      ssh host-a 'cd /home/user/stack && docker compose stop kipple'
      ssh host-a 'cd /home/user/stack && docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-3-5-<ns>.db --yes'
      ssh host-a 'cd /home/user/kipple && git fetch --tags --force && git checkout v0.2.0-alpha.2'
      ssh host-a 'cd /home/user/stack && KIPPLE_VERSION=v0.2.0-alpha.2 KIPPLE_VCS_REF=$(git -C /home/user/kipple rev-parse HEAD) docker compose build kipple && docker compose up -d kipple'
      ssh host-a 'cd /home/user/kipple && git checkout main'

  Do not start the alpha 3 image on the restored database (it would migrate again). Until the new
  server has started once, `kipple password`, `api-password` and `import` refuse the old schema
  (they never migrate).
- **Images now go through Kipple by default.** The default of the setting `imgproxy.mode` is
  `all` (it was "only insecure images"). A database that has no stored `imgproxy.mode` row, which
  is every existing one, therefore starts proxying and caching **all** article images on the first
  start, into `<data>/imgcache/` with a 1 GiB cap (`imgproxy.cache_mb`, default 1024). The cache is
  never part of a backup or snapshot, so the volume needs the room. To go back to the old
  behavior: Settings > Images > "Load images through Kipple" = "Only insecure (http) images", or
  `PATCH /api/settings {"imgproxy.mode":"http_only"}`. Setting the cache size to 0 keeps proxying but
  turns the disk cache off.
- **Search behavior changed.** An unfinished last word is no longer a prefix unless the UI sends
  `typing=1`; saved-search unread counts and "mark all results read" now count the plain stemmed
  words (they were widened by an accidental `word*`: `apple` counted 851 items where 518 match).
  Expect saved-search counts to drop after this deploy. A search that matches too much (over a
  500 ms budget) answers `422 search_too_broad` instead of stalling.
- **Device profiles are protected from a runaway client.** At most 5 new devices per login session
  per day, and the 50-device cap only evicts devices unseen for 30 days.

## Release checklist: license notices

Before tagging, run `cd web && npm ci` then `node scripts/gen-notices.mjs` from the repo root and commit any change to `THIRD_PARTY_NOTICES.md`. Investigate anything the script prints under FLAGGED. The Dockerfile copies `LICENSE` and `THIRD_PARTY_NOTICES.md` into `/licenses/`.

