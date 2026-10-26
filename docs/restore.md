# Kipple: restore a backup

How to bring a library back from a backup, a snapshot or an empty start. For what a backup holds and how to take one, see [deploy.md](deploy.md#where-things-live). Something not working? See [troubleshooting.md](troubleshooting.md#restore-a-backup).

## Contents

- [Restore in the setup wizard](#restore-in-the-setup-wizard)
- [Reset Kipple and start over](#reset-kipple-and-start-over)
- [A restore or reset that is waiting](#a-restore-or-reset-that-is-waiting)
- [Restore from the command line](#restore-from-the-command-line)
- [Restore onto a new, empty volume (lost volume, new host)](#restore-onto-a-new-empty-volume-lost-volume-new-host)

## Overview

There are two ways. On a new server with no account yet, the setup wizard restores from the browser. Over an existing
library, `kipple restore` does it from the command line (to empty an existing library from the browser instead, see
[Reset Kipple and start over](#reset-kipple-and-start-over)). Both keep the database they replace under
`/data/backup/pre-restore-<timestamp>/`, sign every web session out, and bring back everything in the database column
of [What to back up](deploy.md#what-to-back-up).

## Restore in the setup wizard

Start Kipple on an empty data volume and open it. The first screen offers "Create an account" or "Restore from a
backup". Pick the export zip (Settings, Account & Devices, Export backup on the old server); the upload shows a progress bar.
Then Kipple checks the file before anything changes, while the page waits: the manifest and every checksum, the
integrity checks, and that the database holds exactly what a Kipple of its version creates, nothing more or less. It
then shows the Kipple version that made the backup, its date, the feed, item and starred counts, and the user name you
will sign in with. Confirm, and Kipple restores everything: the account, settings, feeds, read and starred state and
statistics. It keeps the checked database next to the empty one and stops; your restart policy
(`restart: unless-stopped` in the examples) starts it again, and the restore is applied before the database opens. The
page waits, shows an estimate for the backup's size, and tells you when to sign in. Without a restart policy, start the
container again yourself: the restore finishes on that start.

Only a backup zip is accepted. To bring in just the feeds, or a feeds file from another reader, create an account and use
"Import a feeds file" in the feeds step.

Things to know:

- **Sign-in.** You sign in as the backup's account with its password. If that account had no password (Cloudflare
  Access or open mode) and that sign-in would not work from where you are (this server's own Cloudflare Access does not
  verify the request, or the open-mode gate refuses it), the wizard asks for a new password first.
  You can set a new password in any case.
- **The address is not restored.** The public URL, allowed host names and trusted proxies describe the old server, so
  a wizard restore drops the backup's and keeps this server's own (from `KIPPLE_PUBLIC_URL`, `KIPPLE_ALLOWED_HOSTS` and
  `KIPPLE_TRUSTED_PROXY_IPS`, or set in Settings, Advanced, Address and access, before the restore, as after a
  reset). The same goes for the Cloudflare Access sign-in settings: the backup's are dropped, which is why a backup whose
  account has no password asks for a new one here.
- **Disk space.** The data volume needs room for the zip while it arrives, then for the database inside it (its size is
  read from the backup's manifest before anything is extracted), and for a backup from an older Kipple about 3.1 times
  the database for the upgrade on the next start. The zip is deleted as soon as the database is out of it. A shortfall
  says how much room is needed.
- **Images and icons** are not in a backup; they are downloaded again when first shown.
- **A backup from a newer Kipple** is refused: update Kipple first. One from an older Kipple is upgraded on the start
  that applies it, after the usual `pre-migration-*` snapshot.
- **One restore at a time, and it belongs to your browser tab.** Only the page that started an upload can see it,
  confirm it or cancel it; another browser that can reach setup sees nothing of it. While an upload is arriving or
  being checked, an upload from another browser is refused with a 409 `restore_elsewhere`. Once it has been checked,
  an upload from another browser replaces it as soon as it starts, so a tab left open on a checked backup never keeps
  you out. The first page then says its backup was replaced and asks for the file again; that also happens if the
  other browser sent a wrong file. A checked upload you do not confirm is deleted after an hour, or at the next
  start. Once you confirm, creating an account is refused until the restore is applied; if someone creates the
  account first, the restore is refused and the upload deleted.
- **Slow or stalled uploads are stopped** so they cannot hold the one restore slot: nothing received for two minutes,
  an average under 32 KB/s after the first two minutes, or a transfer that runs past its allowance (its size at
  128 KB/s, at least two hours, never more than four). The page says why, and a refused upload expires after an hour
  like an unconfirmed one. If you are refused, try again over a faster connection or by Kipple's local address, or use
  `kipple restore`; if a stuck upload still blocks you, restarting Kipple clears it. Too little free disk space is
  refused with 507 `no_space` and says how much room is needed.
- **Large files.** The wizard takes backups up to the 4 GiB database limit. A proxy or tunnel in front of Kipple may
  refuse a large file before it arrives; open Kipple by its local address (`http://127.0.0.1:1919` on the server) and
  try again, or use `kipple restore`.
- The nightly snapshot (`kipple-snapshot.db`) is not accepted here; restore it with `kipple restore`.

## Reset Kipple and start over

Settings, Account & Devices, Reset Kipple returns Kipple to setup mode, where you create a new account or restore a
backup in the wizard. It is the browser twin of `kipple restore` over an existing library, and it is a restore of an
empty database: the dialog says what is erased (all feeds, folders, history, settings and the account), asks for your web
password (an account without one needs the sign-in it uses for changing its password instead) and for you to type
`reset kipple`. Kipple then answers, stops cleanly and your restart policy starts it again; that start moves your library
to `/data/backup/pre-restore-<timestamp>/` (the same folder, naming and retention as a restore) and begins empty.
Kipple keeps every safety copy from the last 30 days (at most ten, since each is a whole library), and the newest
three of any age, so a few resets or restores in a row cannot push out a recent one; export a backup first if you want one you can keep elsewhere. (A database that is provably empty, with no account, feed or item, is
never kept as a safety copy.) The page waits and offers setup when Kipple is back. Without a restart policy, start the container
again yourself: the reset finishes on that start. `kipple restore` brings the kept library back.

The address and access settings (public address, allowed host names, trusted proxies, Cloudflare Access) describe your
server and not the library, so a reset keeps them and Kipple answers at the same address afterwards. That also means
that, after a reset, Kipple has no owner: an unclaimed instance is open to first-time setup for anyone who can reach it
(anyone who can open the address Kipple listens on, including through a public address or tunnel if you have one), and there is no secret to claim it with. The dialog and
the page that waits for the restart both say so. Finish setup right away, and if you cannot, stop Kipple or close its
port until you can.

If `KIPPLE_USERNAME` and `KIPPLE_PASSWORD` are set, Kipple ignores them until a new account exists (it writes
`/data/no-env-account` and removes the file once an account exists), because a restart reuses the same environment and
would otherwise create that account again and skip setup. You can delete the variables from your compose file or `.env`
whenever convenient.

## A restore or reset that is waiting

Confirming a restore in the setup wizard or a reset in Settings does not change the database at once. It leaves two
files in the data folder, `restore-staged.db` (the database to install) and `restore-pending.json` (the confirmation),
and stops Kipple; the next start installs it before the database opens. Until then
nothing has changed, so you can cancel: stop Kipple, delete both files, and start it again. If installing fails (the
`backup` folder is not writable, say), the reason is in the log at every start and the files stay, so fix the cause or
delete the files. A confirmation older than seven days is not applied: the next start deletes it and says so in the log,
so an old one cannot surprise you after a downgrade and a later upgrade.

## Restore from the command line

`kipple restore` replaces the database with a backup zip (or a bare `.db` such as a snapshot). It
refuses while the server runs (the lock), verifies checksums and integrity, refuses a database
from a newer Kipple than this binary, keeps the current database under
`/data/backup/pre-restore-<timestamp>/` (unless it is provably empty: no account, feed or item), and signs every web session out. Without `--yes` it only
verifies and reports (and then exits with status 1 and "nothing was changed", which is expected).

Runbook, with the backup zip in the current directory (it is piped in; the container user cannot read `/import`):

    # 1. Stop the server (named service only).
    docker compose stop kipple

    # 2. Verify without changing anything. Read the counts and the date it prints. It ends with "nothing was changed ..." and exit status 1; that is expected.
    docker compose run --rm -T --no-deps kipple restore - < kipple-backup-YYYYMMDD-HHMMSS.zip

    # 3. Restore for real.
    docker compose run --rm -T --no-deps kipple restore - --yes < kipple-backup-YYYYMMDD-HHMMSS.zip

    # 4. Start it and check the feed count and last fetch on /_status.
    docker compose up -d kipple && docker logs --tail 20 kipple

A backup from an older Kipple is upgraded on that start, after the usual `pre-migration-*` snapshot. Then sign in
again. A restore never touches a backup `.zip`.

**What a zip restore brings back, and what it does not.** Back: everything in the database column of "What to back up"
(account and account secret, settings, feeds, filters, read and starred state, statistics). Not back: the environment
(compose file, `.env`, proxy or tunnel, Access setup), the image cache (`/data/imgcache`, fetched again on demand), and
web sessions (all signed out). Things kept in each browser or installed app, such as the offline queue and the local
appearance cache, stay on that device and are not part of any backup.

**Same version first.** The restore refuses a database from a newer Kipple but upgrades an older one. For a careful
move to a new host, look up the version in the zip's `manifest.json` (`kipple_version`), restore with that image tag,
check it, and only then upgrade; an upgrade is then a normal one with its own `pre-migration-*` snapshot.

**Test your backup.** The verify run (`restore -` without `--yes`, step 2 above) checks checksums and integrity and changes
nothing. It needs the service stopped and the volume it names; to test without touching your real one, run it against a
throwaway volume (`docker compose -p test run ...` with a different project name, or a plain `docker run` with a scratch
volume) and compare its feed count with `/_status`. Do this now and then: a backup nobody has verified is a hope.

**Restore leaves setup mode.** A backup that contains the account puts the instance in normal mode at the next start:
the setup screens are gone. Only a backup taken
in setup mode (no account in it) returns the instance to setup mode: the account form again.
The listen port is never part of a backup: it comes from `KIPPLE_ADDR` (1919 when unset), so a restore does not
change it.

**Undo a restore.** Stop `kipple`, then restore the previous database from the volume (list it with
the `alpine ls` command above):

    docker compose run --rm -T --no-deps kipple restore /data/backup/pre-restore-<timestamp>/kipple.db --yes

A bare `.db` restore also applies a `-wal` file sitting beside it (a pre-restore copy taken after an unclean stop keeps its newest transactions there), and says so. Two restores in the same second get `pre-restore-<ts>` and `pre-restore-<ts>-2`, never the same directory.

**Run restore as Kipple's own user.** `docker compose run` does that by default; do not add `-u root`. A restore run as root would leave a root-owned `0600` `kipple.db` that the server (nonroot) cannot open. If it detects root it warns and hands the new database, the pre-restore directory, `kipple.lock` and a `backup/` directory it created to the data directory's owner (the lock even when the restore stops early), but do not rely on that.

If the refusal says "kipple is running": the service is still up (`docker compose stop kipple`), or
a second `run` is open. Do not delete `kipple.lock`; it is not a file marker, the OS drops it.

Other refusals change nothing on the volume (no `pre-restore-*` directory, no temporary files
left): a damaged or truncated zip ("not a readable zip", "checksum mismatch"), a zip that is not a
Kipple backup (no `manifest.json`, more than ten files, or an entry with a directory part), and a backup from a newer
Kipple ("This backup was made by a newer Kipple (x.y). Update Kipple first."). All exit with status 1.

A restore from the command line also drops a wizard restore that was confirmed but not applied yet (Kipple was stopped
before it started again), and says so: the command is the newer decision.

## Restore onto a new, empty volume (lost volume, new host)

The same `restore -` command works when the volume does not exist yet: `docker compose run`
creates the named volume, and a new named volume is filled from the image's `/data`, which is
already owned by uid 65532 (Kipple's user), so nothing needs a `chown`. Only a bind mount (a host
directory at `/data`) needs `chown 65532:65532` on that directory first.

    # 1. The compose service and your .env exist; the service is not running and has never started on this volume.
    # 2. Verify, then restore (the second command creates <project>_kipple_data if it is missing):
    docker compose run --rm -T --no-deps kipple restore - < kipple-backup-YYYYMMDD-HHMMSS.zip
    docker compose run --rm -T --no-deps kipple restore - --yes < kipple-backup-YYYYMMDD-HHMMSS.zip
    # 3. Start it.
    docker compose up -d kipple && docker logs --tail 20 kipple

The account comes from the backup: sign in with the web password and use the app password
that were current when the backup was taken. `KIPPLE_USERNAME`, `KIPPLE_PASSWORD` and
`KIPPLE_API_PASSWORD` in `.env` are ignored because the account already exists. The restore prints
"There was no previous database to keep." on an empty volume. If a Reader API client reports an
authentication error, sign in again in the app with that password. If the backed-up account had no web
password (removed through Cloudflare Access), sign in through Access (the backup keeps its Access setting), or set
one first with `kipple password` (it works on the stopped service:
`docker compose run --rm -T --no-deps kipple password --stdin`).

Starting the server on the empty volume first is the other route: it starts in setup mode, and the wizard restores the same zip from the browser ([Restore in the setup wizard](#restore-in-the-setup-wizard)).

