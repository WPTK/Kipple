# Kipple: deploy, backups and recovery

Commands here run on the machine that runs Kipple, in the directory of your compose file (the README's
`docker-compose.pull.example.yml`, or `docker-compose.example.yml` to build from source). Anything that is yours to
choose is written `<like this>`. Always name the service (`kipple`) in `docker compose` commands, never a bare `up` or
`down`, in case the file also runs other services. To run them from another machine, wrap the command in
`ssh your-server '...'`.

The container is distroless: no shell, no `ls`, no `rm`. The binary is the entrypoint, so a
subcommand is `docker exec kipple /kipple <command>` on the running container, or
`docker compose run --rm -T --no-deps kipple <command>` on a stopped service (it starts a throwaway
container on the same volume, publishes no port and ignores the restart policy).

**Commands that pipe a file in (`... < file`) need a POSIX shell or cmd, not PowerShell.** PowerShell refuses the line
with `ParserError: The '<' operator is reserved for future use` before anything runs. From a PowerShell prompt, wrap the
whole line: `cmd /c "docker exec -i kipple /kipple import - < feeds.opml"` (cmd passes the bytes through unchanged; do
not use a PowerShell pipe, which can re-encode them).

## Contents

- [System requirements](#system-requirements)
- [Where things live](#where-things-live), [what to back up](#what-to-back-up), [back up the volume](#back-up-the-volume-itself), [back up on a schedule](#back-up-on-a-schedule)
- [Export a backup](#export-a-backup-the-button)
- [OPML import and export](#opml-import-and-export)
- [Health check and container hardening](#health-check-and-container-hardening)
- [First run: create your account](#first-run-create-your-account)
- [Ports](#ports) and [reverse proxies](reverse-proxy.md)
- [Open mode (no password)](#open-mode-no-password)
- [Time zone](#time-zone)
- [About, debug info and versions](#about-debug-info-and-versions)
- [The published image](#the-published-image)
- [Installing the app and offline reading](#installing-the-app-and-offline-reading)
- [Reset the web password](#reset-the-web-password)
- [Cloudflare Access (optional)](#cloudflare-access-optional)
- [Restore a backup](#restore-a-backup), [onto a new volume](#restore-onto-a-new-empty-volume-lost-volume-new-host)
- [Roll back an upgrade](#roll-back-an-upgrade-that-migrated-the-schema) and [disk space during an upgrade](#disk-space-during-an-upgrade)
- [Database size and compacting](#database-size-and-compacting)
- [What stays the same across 1.x](compatibility.md)

Something not working? See [troubleshooting.md](troubleshooting.md). Behind HTTPS with Caddy, nginx or Traefik:
[reverse-proxy.md](reverse-proxy.md).

## System requirements

- A machine that runs Docker and Docker Compose 2.24 or newer (older Compose needs an empty `.env`; see
  `docker-compose.example.yml`), on `linux/amd64` or `linux/arm64`. Building from source needs Docker and Git only.
- Memory: Kipple is designed to stay under 100 MB resident, and the example compose file caps the container at 256 MB
  (`mem_limit: 256m`, with `GOMEMLIMIT=64MiB`).
- Disk, on the volume that holds `/data`: the database, the nightly snapshot (about one more copy of the database), the
  image cache (at most 1 GiB by default, `imgproxy.cache_mb`) and room for upgrades and exports, below. A database
  of about 140 feeds and 5,600 stored articles was 53 MB, so plan on roughly 10 KB per stored article. The default
  keeps the newest 250 per feed (`retention.default`) and never trims starred articles.
- A browser for the web app. Any client that speaks the Google Reader API can sync.

## Where things live

Everything is on the `kipple_data` volume, mounted at `/data`. Compose prefixes the volume with the project name
(`<project>_kipple_data`; `docker volume ls` shows it), and that full name is what the `docker run` commands below use.

| Path in the container | What | Kept |
|---|---|---|
| `/data/kipple.db` (+ `-wal`, `-shm`) | The database. Never copy it while the server runs | live |
| `/data/kipple.lock` | Held by `serve` (an OS lock: it vanishes with the process, no stale lock) | live |
| `/data/backup/kipple-snapshot.db` | Nightly snapshot at 04:10 (`tz` setting), consistent, safe to copy | 1 |
| `/data/backup/pre-migration-<from>-<to>-<ns>.db` | Written before a schema migration (`0600`) | newest 3 |
| `/data/backup/pre-restore-<YYYYMMDD-HHMMSS>Z/` (UTC) | The database that `kipple restore` replaced | newest 3 |
| `/data/backup/export/` | Temporary files of an export in progress. Emptied at startup | transient |
| `/data/imgcache/` | Image cache (`imgproxy.cache_mb`, default 1024 MiB, least recently used evicted; never in backups or snapshots) | capped |
| `/data/restore-tmp.db*`, `/data/restore-upload.tmp` | Only while a `kipple restore` runs | transient |

These are all on the same disk as the database. They protect against a bad migration or a bad
restore, not against losing the machine. An off-box copy is the export (below) or a `docker cp` of the
nightly snapshot.

To look inside the volume (there is no shell in the Kipple image):

    docker run --rm -v <project>_kipple_data:/data:ro alpine ls -la /data /data/backup

### What to back up

Kipple's state is split in two places, and a backup of one does not cover the other.

| Lives in the database (in every export zip and snapshot) | Lives in your compose file or `.env` (in no backup) |
|---|---|
| The account: user name, password hashes, account secret | `KIPPLE_ADDR` and the compose port mapping |
| Every setting, including the time zone (`tz`) and `security.allowed_hosts` | `KIPPLE_PUBLIC_URL`, `KIPPLE_TRUSTED_PROXY_IPS`, `KIPPLE_ALLOWED_HOSTS` |
| Feeds, folders, per-feed options, filters, feed logins | `KIPPLE_ACCESS_TEAM_DOMAIN`, `KIPPLE_ACCESS_AUD` |
| Read and starred state, the statistics history | `TZ`, `KIPPLE_DATA`, and the other tuning and logging variables |
| Device profiles | Image tag, resource limits (`mem_limit`, `GOMEMLIMIT`), the reverse proxy or tunnel setup |

`KIPPLE_USERNAME`, `KIPPLE_PASSWORD` and `KIPPLE_API_PASSWORD` are read only on the first start of an empty database and
ignored once an account exists, so they are not part of the state. If you set them, the plain text sits in `.env`:
remove the lines after setup, and keep `.env` somewhere private either way.

`security.allowed_hosts` has no precedence rule between the two places: the names in `KIPPLE_ALLOWED_HOSTS` and the
names in the setting are added together, so a restore brings back the setting's names and your `.env` must bring back
the rest. `TZ` only seeds the zone: it is stored when no `tz` setting exists yet, so a restored backup's zone wins, and
`TZ` applies only when the backup has none.

Checklist, for a rebuild to be a copy and paste:

1. The data volume, as an export zip (above) or a tarball of the volume (below).
2. The compose file and the `.env`, with the image tag you ran (`docker inspect kipple --format '{{.Config.Image}}'`
   or `docker exec kipple /kipple version`).
3. Your reverse proxy or tunnel configuration, and any Cloudflare Access application settings.
4. For a bind mount, the host directory must be owned by uid 65532 on a new machine, for a fresh start or a restore.

### Back up the volume itself

The live `kipple.db` and its `-wal` must not be copied while the server runs. Either stop the service first, or copy
only the nightly snapshot, which is always consistent. A tarball of the whole volume, taken
while Kipple is stopped:

    docker stop kipple
    docker run --rm -v kipple_data:/data:ro -v "$PWD":/out alpine tar czf /out/kipple-data.tar.gz -C /data .
    docker start kipple

Use your compose project's volume name (`docker volume ls`; `<project>_kipple_data`). Add `--exclude=./imgcache` to
leave out the image cache. Restoring a tarball means extracting it into an empty volume whose files are owned by uid 65532,
which is more work than `kipple restore`, so the export zip or the snapshot is the better routine backup.

### Back up on a schedule

Kipple does not schedule an off-machine backup for you. The nightly snapshot is one file on the same volume, so it is
lost with the volume. Run something on a timer (cron, a systemd timer, Windows Task Scheduler) that copies it away; on
one machine:

    docker cp kipple:/data/backup/kipple-snapshot.db /path/to/backups/kipple-$(date +%F).db

Run it after 04:10 in your Kipple time zone, and keep as many generations as you like; the file is a consistent
database that `kipple restore` accepts as it is. The export zip is the same data plus the OPML and a manifest.

## Export a backup (the button)

Settings > Account > Export backup. The app calls `POST /api/backup`, which answers at once when the build is quick and otherwise with `202 {job_id}`; the app then polls `GET /api/backup/jobs/<id>` until it is `ready` (the build carries on if the browser tab closes or Cloudflare cuts the request at about 100 s; a big database can take minutes). It shows the `warning` text and the size,
then starts the download of `GET /api/backup/<token>`, which saves as
`kipple-backup-YYYYMMDD-HHMMSS.zip`. Save it somewhere other than the server (another machine, an external drive, cloud storage).

- The zip holds `kipple.db` (a consistent snapshot), `feeds.opml` (imports into any reader),
  `settings.json` (readable copy), `manifest.json` (versions, counts, SHA-256 of every file) and
  `RESTORE.txt`. It is roughly a third of the database size.
- **The file is sensitive.** It contains the web and Reader API password hashes, the account secret
  that signs Reader tokens and image links, hashed session ids, and any feed logins (HTTP Basic
  `user:password`, stored in plain text). Keep it private.
- `kipple.db` in the zip is the whole database (everything in the left column of the table above, also the `sys.*`
  flags); `settings.json` leaves the `sys.*` flags out and is only a readable copy.
- The link is single use and expires after 5 minutes. If the transfer breaks, export again.
- "Busy" (409): the nightly snapshot or another export is running. Retry in a few seconds.
  "Not enough free disk space" (507): the volume needs about 2.2 times the database size free.
- Exports never block feed fetching: the snapshot only holds a read view.

The database only grows so much: an export is refused above 4 GiB. Past that, copy the nightly snapshot instead (see
"Back up on a schedule"); that copies the snapshot only, never the live database or WAL.

## OPML import and export

Feeds screen > Export OPML downloads the subscription list (`GET /api/opml`), the same file as `feeds.opml` in a backup
zip. It carries the folder tree (subfolders as nested outlines), feed URLs and titles, and the per-feed options Kipple
adds (interval, retention, full text, and the like, as `kipple:` attributes that other readers ignore). It does **not**
carry read or starred state, filters, settings, the statistics history, the account or feed logins. If OPML is all you
keep, those are lost on a restore from it.

OPML carries your feeds and folders only; starred items and read state are not part of OPML (and most readers do not export them), so they are not imported.

An import keeps the file's folder tree: nested outlines become subfolders, up to 8 levels deep. A feed nested deeper, or
in a folder whose name Kipple cannot store (over 100 characters, control characters), goes into the nearest folder above
it, and the import report lists that folder under `folders_refused`. A feed you already have stays in its folder unless
you ask for "move feeds that already exist into the file's folders" (`kipple import -move-existing`, or
`POST /api/opml?move_existing=true`); folders that this leaves empty are listed under `folders_emptied` and kept.

`kipple import [-mark-read-older-than-days N] [-move-existing] <file.opml | ->` is safe while the server runs and prints JSON on standard output. The flags must come before the file. Pipe the file in, because the container user cannot read a bind-mounted `/import`:

    docker exec -i kipple /kipple import - < feeds.opml

New feeds are fetched on the scheduler's next tick. `docker exec kipple /kipple version` prints the running build.

Health: `curl -s http://127.0.0.1:1919/healthz` answers `ok` (use your published port; see "Ports" below).

## Health check and container hardening

The image carries a `HEALTHCHECK` (every 30 s, 5 s timeout, 40 s start period, 3 retries) that runs
`/kipple healthcheck`. That subcommand does a GET on `http://127.0.0.1:<port>/healthz` (the port comes
from `KIPPLE_ADDR`; a `0.0.0.0`, `::` or empty host becomes `127.0.0.1`; with `KIPPLE_ADDR` unset it probes
1919), makes that one probe, waits at most 3 s and exits 0 only on HTTP 200 `ok`, otherwise printing a line and exiting 1.
`/healthz` needs no login and touches no database: it answers `ok` as long as the HTTP server is serving. So
"healthy" means the process is up and answering, not that feeds are fetching, and not that Kipple has been set up:
a container that is waiting for you to create its account is healthy.

See it:

    docker ps --filter name=kipple                       # STATUS shows (healthy) / (unhealthy) / (health: starting)
    docker inspect -f '{{.State.Health.Status}} {{.State.Health.FailingStreak}}' kipple
    docker inspect -f '{{json .State.Health.Log}}' kipple  # last 5 probe results and messages
    docker exec kipple /kipple healthcheck; echo $?      # run the probe by hand

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
| `cap_drop: [ALL]` | Removes all Linux capabilities; the app needs none (port 1919 is unprivileged). |
| `security_opt: no-new-privileges:true` | Blocks privilege escalation through setuid binaries. |
| logging `json-file` 10m x 3 | Bounded container logs. |

If you run the image with plain `docker run`, the same flags are `--read-only --tmpfs /tmp --cap-drop ALL
--security-opt no-new-privileges:true --pids-limit 200`. The README's pull-and-run file
(`docker-compose.pull.example.yml`) carries the same limits, log rotation and hardening as `docker-compose.example.yml`, the
build-from-source file, which also reads an optional `.env`.

## First run: create your account

A Kipple with no account starts in **setup mode**. It is the normal server, but the browser shows the setup wizard
instead of a sign-in screen, and its first step is the form that creates your account. Open the address and create your
account, then the wizard takes you the rest of the way (time zone, theme, OPML import,
recommended feeds, an optional Reader API password).

- **Who can create the account.** Whoever gets there first. An unclaimed Kipple is simply "no account yet": the one
  request that creates the account succeeds for exactly one caller, and any other that arrives at the same moment is
  told Kipple was just set up (409) and is not signed in; one that arrives after the winner has finished gets 404, because
  the route is gone by then. That is how Jellyfin, Gitea and Home Assistant set
  themselves up too.
- **Keep the port on this machine until you have created your account.** Anyone who can reach an unclaimed Kipple can
  claim it, and whoever owns the account can also change a feed's network settings (which feeds may reach private
  addresses). The examples and the README's one-line command publish the port on `127.0.0.1` for that reason: create
  your account, then widen the port if you want to. A host firewall is not protection here: a Docker-published port
  bypasses `ufw`. For a headless install that has to listen on a network before you can open a browser, create the
  account from the environment instead (below). If you find Kipple already set up when you did not do it, stop the
  container, delete its data volume and start again.
- **What guards the request.** The Host gate answers 421 to a name Kipple does not recognize (so a web page in your
  browser cannot reach the form by DNS rebinding), the request must be same-origin and carry `X-Kipple-Client`, and
  only one account is created at a time. There is no per-address counting, so a noisy neighbour behind the same Docker
  gateway can never keep you out.
- **What answers while there is no account.** Only `GET /api/instance`, `POST /api/setup/account`, `/healthz` and the
  app itself. Every other `/api` route answers 401, sign-in answers 409 `setup_required`, and the Reader API answers
  401, apart from its static probe paths, which return no data (`/api/greader.php` and `/api/greader.php/` answer
  `200 OK`, `/check/compatibility` answers `200 PASS`, and `/icon/...` answers 404). Nothing is fetched and no maintenance runs until the account exists: the scheduler starts at the claim.
- **What signed-out visitors can see.** `GET /api/instance` answers without signing in (from an address the Host gate
  admits). It says whether setup is pending, the sign-in mode (`open`, `access` or `password`) and, while pending,
  whether Cloudflare Access is configured and whether open mode would work from where they are. No version, username
  or feed data.
- **Lifetime.** Once the account row exists the setup route is gone. The process that created the account keeps answering
  404 on it until it restarts; after any restart the route is not registered at all, so a signed-out request to it gets
  the same `401` as any other `/api/` path.
- **Setup is not health.** `/healthz` and the container's health check answer `ok` in setup mode: healthy means serving,
  not configured. `/_status` says "Setup is pending" until an account exists.
- **Env credentials skip it.** With both `KIPPLE_USERNAME` and `KIPPLE_PASSWORD` set on a first start, Kipple creates the
  account from them and starts in normal mode, with no wizard onboarding and no unclaimed window. A lone
  `KIPPLE_USERNAME` is ignored and the wizard asks.
- **Cloudflare Access.** Access proves who may reach the app, not who owns this instance, so it does not replace
  creating the account. The wizard offers "No password, through Cloudflare Access" only on a request that came through
  Access and carries a verified token.

After the account step the wizard continues as an ordinary signed-in session. Each step saves as it goes, so a reload
or "Skip for now" loses nothing. Settings > Account & Devices > Run setup again repeats the steps after the account for
any account (it never touches the account).

## Ports

The default listen address is `:1919`. Set `KIPPLE_ADDR` to choose any other address. If the address is taken, Kipple
exits with an error that names it and `KIPPLE_ADDR`; it never picks another port by itself (a container has its own
network, so this only happens with the bare binary). The port is never stored in the database or a backup.

In a container the simplest way to use another port is to change only the host side of the mapping
(`127.0.0.1:8080:1919`). If you do set `KIPPLE_ADDR`, change the container side of the mapping to the same port: a
mapping to a port Kipple does not listen on answers nothing, while the health check, which probes exactly the address
Kipple listens on (`KIPPLE_ADDR`, or 1919 when unset), still reports healthy.

The image has no shell, so to see the `KIPPLE_ADDR` and `TZ` a container really runs with, read its environment from
the host:

    docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' <container>

The README's compose files publish `127.0.0.1:1919:1919`, this machine only. For the LAN use `1919:1919`, for Tailscale
your `100.x.y.z:1919:1919`. A reverse proxy or tunnel (Cloudflare Tunnel, Caddy, nginx) is what gives Kipple HTTPS; set
`KIPPLE_TRUSTED_PROXY_IPS` to the address it connects from, and `KIPPLE_PUBLIC_URL` to the public address.

`KIPPLE_TRUSTED_PROXY_IPS` is a comma-separated list of single addresses and CIDR ranges (`192.0.2.10`,
`198.51.100.0/24`, `2001:db8::/32`), empty by default. Only a connection from a listed address is believed about who the
client is: for such a peer the client is the rightmost `X-Forwarded-For` hop that is not itself listed (each proxy appends
the address it received from, so the entries to its left are whatever the client chose to send), or `CF-Connecting-IP`
when there is no `X-Forwarded-For`; for any other peer the forwarding headers are ignored and the client is the peer.
List every proxy in the chain and nothing wider: `0.0.0.0/0` would let anyone choose their own address. Trust a Docker
network, or any range that clients can reach directly, only when the published port is reachable by the proxy alone;
otherwise any client in the range can write its own `X-Forwarded-For` or `CF-Connecting-IP` and be believed. A trusted
peer also makes open mode refuse the request as `forwarded` (it fails closed), so a trusted Docker gateway cannot be used
with open mode.

That address keys the per-client budgets of web sign-in and the Reader API. Wrong passwords are slowed, never counted
against anyone else: five are free, then each wait doubles from two seconds to a minute, a correct password clears the
count, and it is forgotten after an hour with no failure. With the list correct every visitor is a separate client and a
stranger cannot slow your key's pacing. All keys do share one password-hashing slot (a 5 s wait, also used by the
public Reader API login), so enough distinct addresses (one IPv6 /48 is 65,536 of them) can keep it full and make any
sign-in answer "busy, try again". If several people truly share one address (a proxy you did not list, Docker Desktop's
gateway without listing it, carrier NAT), they share one budget: even one persistent guesser among them can make your
sign-in answer "busy, try again" (`503`, never a lockout) until it stops, and your own typos are slowed along with
theirs. A client whose own failures ask for a long wait is answered at once with the remaining wait in `Retry-After`.
Fix a shared address by listing the proxy, not by raising a limit.

## Open mode (no password)

The wizard's account step offers **No password at all**. It is for a Kipple that only you can reach: this computer, your
local network and your tailnet. The rule is modeled on Sonarr's and Radarr's "Disabled for Local Addresses" (it is stricter: no
`fec0::/10`, the Tailscale narrowing below, and every forwarding header refused): a request is allowed by where it comes
from, and there is no setting to tune it. The screen shows the warning verbatim in substance: anyone who can reach the address can read and change
everything. It needs a ticked acknowledgement, and it stores the account with no password hash and `auth_mode = open`.
Sign-in then happens by itself when the app opens: it asks the server for a session, and the server grants one only if
the request passes the **open gate**:

1. **The name is expected.** Open mode answers a `Host` header that is an IP address, `localhost`, a `.localhost` or
   `.ts.net` name, the host of `KIPPLE_PUBLIC_URL`, or a name you allowed. Anything else gets
   `421 Misdirected Request`, which says how to allow the name. `http://<ip>:1919` always works.

   To use a local network name such as `nas.local` or `nas` without a password, allow it: add it to
   `KIPPLE_ALLOWED_HOSTS` (comma-separated, for example `KIPPLE_ALLOWED_HOSTS=nas.local`) and restart Kipple.

   This defeats DNS rebinding, where a hostile web page points a name it controls at your computer so your browser
   treats Kipple as part of that page. A public name is the usual tool, but a device on your network can do the same
   with a single-word or `.local`, `.lan`, `.home.arpa` or `.internal` name (mDNS, LLMNR or NetBIOS, a router's DHCP
   names), even when it cannot reach Kipple's port itself, as with the default `127.0.0.1:1919` publish. So open mode
   answers only the names of that kind you allowed, never all of them, and choosing open mode never allows a name for
   you: the setup wizard has no secret, so a hostile page could make that choice too. During setup the check is
   broader (single-word names and those suffixes are answered, so the wizard opens at whatever name you use); with a
   password it only logs, once an hour.
2. **Not forwarded.** A request that came through a proxy or tunnel (a `CF-Connecting-IP`, `Cf-Access-Jwt-Assertion`,
   `Forwarded`, `X-Real-IP` or `X-Forwarded-*` header, a `Tailscale-Funnel-Request`, or a peer listed in
   `KIPPLE_TRUSTED_PROXY_IPS`) is refused, because a tunnel means the port is published to people you did not pick.
   The one exception is Tailscale Serve (tailnet-only HTTPS to a `.ts.net` name), recognised by exactly what
   `tailscaled` sends and nothing a client can choose alone: a loopback peer, a `.ts.net` Host, one `X-Forwarded-For`
   address in Tailscale's range, `X-Forwarded-Host` equal to the Host, `X-Forwarded-Proto` `https` if present, and a
   Tailscale address on this machine. A reverse proxy in front that passes the Host through reports the real client
   address in `X-Forwarded-For` and is refused.
3. **A local peer.** The connection must come from this computer (loopback), a link-local address, a private network
   address (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, or IPv6 `fc00::/7`), or a Tailscale address
   (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`). Anything else, a public address included, is refused. The address is the
   one the connection really came from; Kipple never believes a header for it, which is also why a request with a
   forwarding header is refused in rule 2. A Tailscale-range peer is let in only when it reached this machine on its own
   Tailscale address or on a private address of this machine (the LAN interface or a container's bridge), because
   `100.64.0.0/10` is also carrier-grade NAT, cloud and Kubernetes overlay space: one that reached a CGNAT, public or
   unknown address of this machine is refused. What Kipple cannot check is a device on your own network that routes a
   forged tailnet-range packet at this machine's Tailscale address; on Linux Tailscale's own firewall rule drops those,
   on other systems keep open mode to a network you trust. Likewise, anything that forwards connections from a private
   address without adding a header looks local: a Kubernetes Service with the Cluster traffic policy, a cloud layer-4
   load balancer with IP targets, `socat`, Docker Desktop's port forwarding. An open-mode Kipple must never sit behind
   one of those on a public listener.
4. **The browser says so.** The `Origin` must name the same host the request was sent to.

A signed-in session in open mode keeps passing the network part of the gate on every request, so a session cannot
outlive the position that admitted it. An open live-update stream (`/api/events`) is closed at its next heartbeat when the
device moves, and at once when a security setting such as the allowed host names changes.

**The Docker caveat.** Inside a container every connection arrives from Docker's bridge gateway, a private address:
Docker delivers even a `-p 127.0.0.1:1919:1919` connection that way, and on Docker Desktop, rootless Docker or IPv6
without ip6tables it delivers other machines' connections from that same address too. Kipple therefore cannot tell your
local network from the world there, and the protection is the address you publish the port on, not Kipple. Publish an
open-mode Kipple only on the interface of your local network or your tailnet (the pull-and-run example binds
`127.0.0.1`, this machine only), and never on a public interface; `1919:1919` binds every interface, so use it only on a
machine that is not reachable from the internet. For the same reason Tailscale devices reach a container as ordinary
private peers. A plain binary on the same machine as the browser sees the real address and needs nothing special.

Changing your mind: **Settings > Set web password** (or `kipple password` on the host) gives the account a password and
returns it to normal mode; either signs every other session out. Going from a password account to open mode is not in
the Settings screen (it is `POST /api/account/password` with `{"current": ..., "open": true}`, which needs the current
password and passes the same gate); choose open mode in the wizard on a new install. The Reader API is unaffected in
open mode: sync apps still sign in with the API password, which is then the only credential that exists.

## Time zone

The time zone is one setting, `tz` (Settings > Account & Devices, and wizard step 2). It is the zone for daily reading
statistics, the nightly 04:10 maintenance and the weekly snapshot, backup file names and, from the next start, log
timestamps. It is read live, so a change takes effect at the next statistics write, summary request and nightly tick,
with no restart. The default for a new install is UTC, and the wizard preselects your browser's zone.

The `TZ` environment variable (IANA name) only gives a new install its first value: on a start where no `tz` setting
exists yet, Kipple stores `TZ` as the setting. After that `TZ` is not read for the zone, so changing or removing it does
not move statistics, the nightly job or backup names; choose in Settings. (Go itself still reads `TZ` for the first
start-up log lines and for the CLI subcommands.) An unknown name in `TZ` stops a start that would have stored it.

A change applies to new statistics only: rows already recorded keep the local date and hour of the zone that was in
effect when they were written, so a day never moves. Dates in the web app follow each device's own clock, whatever the
server zone is.

**Known limitation.** Kipple cannot tell an explicit choice of UTC from the untouched default, because both are stored as
`UTC`. So the wizard's time zone step, when it opens with `UTC` saved, treats it as "not chosen yet" and suggests your
browser's zone; that suggestion is only saved if you press Continue. (In a Run setup again session the saved zone is kept
as the choice.) A `TZ` of `UTC` on a new install is stored as `UTC`, so it behaves the same way.

## About, debug info and versions

Settings > About shows the version, commit, build date, Go version, platform, database schema, uptime, whether the data
directory is writable, the time zone, the sign-in mode and whether a public URL is set, and a **Copy debug info** button
that shows the text before copying it. It contains no user name, host names, URLs, paths or secrets, so it is safe to
paste into an issue. Nothing on it contacts GitHub or anything else: Kipple never checks for updates.

On the host: `docker exec kipple /kipple version` prints the version alone; `docker exec kipple /kipple version -v`
adds the commit, build date, Go version, platform, schema and web build id. The image digest is not knowable from
inside (it is computed after the build); read it from the host with
`docker inspect --format '{{index .RepoDigests 0}}' kipple`. The release notes of each version name its digest. After an
upgrade Kipple shows what changed once ("What's new"), and an open browser tab that is running an older build shows
"Kipple was updated. Reload".

## The published image

A tag push publishes a signed, multi-arch (`linux/amd64`, `linux/arm64`) image at
`ghcr.io/wptk/kipple:<version>` (`docker-compose.pull.example.yml` in the repository is the ready file). Stable releases
also move `latest` and the `X.Y` and `X` tags; **a prerelease is tagged only with its exact version**, so until the first
stable release name the version. Verify a pull with cosign (the command is in the README and in each release's notes);
the signature identity is the release workflow of this repository. Upgrade by changing the tag and
`docker compose pull kipple && docker compose up -d kipple` (name the service). It is built from the same source
as a source build (a different build: single-architecture there, no provenance), so `kipple restore`, rollbacks and everything else in this file apply unchanged; for a rollback
across a migration, start the previous tag's image only after restoring the pre-migration snapshot (see below).

To check the build provenance and read the software bill of materials (needs the GitHub CLI and Docker):

    gh attestation verify oci://ghcr.io/wptk/kipple:<version> --repo WPTK/Kipple
    docker buildx imagetools inspect ghcr.io/wptk/kipple:<version> --format '{{ json .SBOM }}'

The first confirms the image digest was built by this repository's release workflow from the tagged commit; the second
prints the SPDX package list BuildKit attached to the image, one per platform. Both are part of the signed image index,
so the digest the signature covers covers them too. The threat model and a checklist for testing an instance yourself are
in [threat-model.md](threat-model.md).

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

    docker exec -it kipple /kipple password

From a script or a pipe (one line on standard input; mind shell history and the process list):

    printf '%s\n' "$NEW" | docker exec -i kipple /kipple password --stdin

Rules: 5 to 256 characters. It signs out every web session **and revokes every Reader API token**
(it rotates the account secret), so afterwards: sign in again in the browser, and re-enter the
Reader API password in your sync apps (that password itself is unchanged; if you have lost
it too, `docker exec kipple /kipple api-password` sets a new one). The failed-login pacing is in
memory: it clears after an hour with no failure or on a restart. If `KIPPLE_PASSWORD` is still in
your `.env`, remove it: it is read only when the account is first created. On an account in open mode
(no password) this sets a password and returns it to normal sign-in ("Open mode" above). If the account does not exist
yet, the command says so and points to the setup wizard.

When you can still sign in, change either password in Settings > Account & Devices instead; the CLI is the recovery path.
It is also how to give the account a password again after it was removed through Cloudflare Access and Access
validation was then turned off (see "Cloudflare Access (optional)" below).

## Cloudflare Access (optional)

If Cloudflare Access sits in front of Kipple, Kipple can verify the `Cf-Access-Jwt-Assertion` header Access adds
to every request it lets through (design §7.0). In your `.env` set both (or neither; one without the
other stops startup):

    KIPPLE_ACCESS_TEAM_DOMAIN=yourteam.cloudflareaccess.com   # Zero Trust > Settings > Custom Pages; https:// optional, no path
    KIPPLE_ACCESS_AUD=<Application Audience (AUD) tag>          # Zero Trust > Access > Applications > your Kipple app > Overview

then `docker compose up -d kipple`. The startup log says
`Cloudflare Access token validation on` with the issuer, and Settings > Account shows the Access email you are
signed in with. Kipple checks the RS256 signature against `https://<team domain>/cdn-cgi/access/certs` (cached for
an hour and refreshed in the background), the issuer, the audience and the expiry; a verified token never replaces
the session cookie. The Reader API keeps its own API password either way, and its path (`/api/greader.php`) is
normally left outside Access (a bypass), since sync apps cannot sign in to Access.

With validation on, the web password becomes optional: Settings > Account > Remove web password (offered only when
you are signed in through a verified Access token, and it asks for the current password). An account without a
password signs in only on requests that carry a verified token, so the LAN or a published port cannot sign in.
Anyone your Access policy admits can, so keep the policy to your own email. The setup wizard offers the same choice
("No password, through Cloudflare Access") on a fresh install, but only when Kipple is opened through Access and the
request carries a verified token. Open mode (see "Open mode" above)
is a different thing: it has no Access involved and refuses requests that came through Access.

**Before you unset the two variables, set a password again**: Settings > Account > Set web password, or
afterwards `kipple password` (see "Reset the web password" above). With Access off and no password, web sign-in
is impossible and the startup log warns about it.

## Restore a backup

`kipple restore` replaces the database with a backup zip (or a bare `.db` such as a snapshot). It
refuses while the server runs (the lock), verifies checksums and integrity, refuses a database
from a newer Kipple than this binary, keeps the current database under
`/data/backup/pre-restore-<timestamp>/`, and signs every web session out. Without `--yes` it only
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

An older schema is migrated on that start, after the usual `pre-migration-*` snapshot. Then sign in
again. A restore never touches a backup `.zip`.

**What a zip restore brings back, and what it does not.** Back: everything in the database column of "What to back up"
(account and account secret, settings, feeds, filters, read and starred state, statistics). Not back: the environment
(compose file, `.env`, proxy or tunnel, Access setup), the image cache (`/data/imgcache`, fetched again on demand), and
web sessions (all signed out). Things kept in each browser or installed app, such as the offline queue and the local
appearance cache, stay on that device and are not part of any backup.

**Same version first.** The restore refuses a database from a newer Kipple but migrates an older one. For a new host,
look up the version in the zip's `manifest.json` (`kipple_version`, `schema_version`), restore with that image tag, check
it, and only then upgrade; an upgrade is then a normal one with its own `pre-migration-*` snapshot.

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
Kipple backup (no `manifest.json`, or an entry with a directory part), and a backup from a newer
Kipple ("the database schema version N is newer than this Kipple binary (M): upgrade Kipple
first"). All exit with status 1.

### Restore onto a new, empty volume (lost volume, new host)

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

The account comes from the backup: sign in with the web password and use the Reader API password
that were current when the backup was taken. `KIPPLE_USERNAME`, `KIPPLE_PASSWORD` and
`KIPPLE_API_PASSWORD` in `.env` are ignored because the account already exists. The restore prints
"There was no previous database to keep." on an empty volume. The Reader API answered ClientLogin
and `unread-count` with the backup's password on the rehearsal; if a Reader API client reports an
authentication error, sign in again in the app with that password. If the backed-up account had no web
password (removed through Cloudflare Access), sign in through Access with the same `KIPPLE_ACCESS_*` values, or set
one first with `kipple password` (it works on the stopped service:
`docker compose run --rm -T --no-deps kipple password --stdin`).

Do not start the server on the empty volume first: it would start in setup mode with a new database, and the restore then replaces that database anyway (it is kept under `pre-restore-*`), so it only adds a step.

## Roll back an upgrade that migrated the schema

There are no down migrations. An older binary refuses a newer schema, so going back means restoring the
`pre-migration-<from>-<to>-<ns>.db` that the upgrade wrote before migrating. Anything read, starred or fetched since the
upgrade is lost.

`<from>` is the schema the database had and `<to>` the schema the upgrade migrated it to. After skipping releases
`<from>` can be several schemas below `<to>` (`pre-migration-8-11-<ns>.db`). Restore the newest snapshot whose `<to>` is
the current schema of the database.

Always go back through `kipple restore`. Never copy a snapshot over `kipple.db` by hand, and never edit the schema by
hand (drop a column, table or index) to make an older binary start.

(This is the "Rolling back" procedure that a refused start points to.) If the old image is started on the migrated
database without these steps, it does not start, the container exits with status 1 (with `restart: unless-stopped` it
keeps restarting) and nothing is changed on the volume. `docker logs kipple` shows the refusal. A binary that records
versions names the Kipple that wrote the database and what to do:

    kipple: store: store: database schema version 11 is newer than this binary (10); refusing to start. This database
    was last opened by Kipple v0.7.0 (schema 11); this is Kipple v0.6.0 (schema 10). Run v0.7.0 or newer, or restore the
    pre-migration snapshot from the backup folder (docs/deploy.md, Rolling back).

(The numbers are examples. A database that never recorded its version gets "It was written by a newer Kipple than this
binary." in place of the second sentence.) A binary that does not record versions prints only
`database schema version N is newer than this binary (M); refusing to start`. Either way, stop it, then:

    # 1. Stop the service and find the newest pre-migration snapshot.
    docker compose stop kipple
    docker run --rm -v <project>_kipple_data:/data:ro alpine ls -la /data/backup
    # 2. Restore it with the NEW image (it is still on the machine; old releases may have no restore command).
    docker compose run --rm -T --no-deps kipple restore /data/backup/pre-migration-<from>-<to>-<ns>.db --yes
    # 3. Start the old version. Do not start the new image in between: it would migrate again.
    #    Published image: put the old tag in the service's image: line, then
    docker compose pull kipple && docker compose up -d kipple
    #    Built from source: check out the old tag, then
    KIPPLE_VERSION=<old tag> KIPPLE_VCS_REF=$(git rev-parse HEAD) docker compose build kipple && docker compose up -d kipple

The restore prints `schema version <from>` for the snapshot and moves the migrated database to
`backup/pre-restore-<ts>/`, so the roll-forward is one more restore away. Sign in again afterwards.

### Disk space during an upgrade

An upgrade that migrates the schema writes the pre-migration snapshot (a full copy of the database) and then runs the
migration, which is held in the WAL until it is committed and checkpointed into the database file. The server does not
answer, health check included, until the migration finishes; a migration that builds an index over a large table can
take a while.

Before the snapshot is written Kipple checks the free space: it refuses to start, with `not enough free disk space to
migrate the database ... (nothing was changed)`, unless there is enough extra free space: on the database's volume, the
size of the database file (without the WAL) plus 64 MB of headroom for the migration's WAL and growth, and on the backup
directory's volume, 1.1 times the database size for the snapshot; when both are on the same volume (the default `/data`
layout) the two add up. That is the minimum. A migration that builds indexes briefly needs about twice the new indexes'
size on top (the WAL holds the build until the checkpoint), so leave more than the minimum free. Nothing has been written
when the check refuses, so free some space (older `backup/` files, exported archives, the image cache) and start again.
If the volume fills up despite the check, the migration transaction fails and is rolled back (the database keeps its
schema and the previous binary keeps working); a full disk can also fail the snapshot itself, which likewise leaves the
database untouched. The check is skipped when the free space cannot be read.

What each migration changes is in that release's notes in `CHANGELOG.md`. Before upgrading, read the top paragraph of
every release you skip in `CHANGELOG.md`.

## Database size and compacting

Kipple keeps the newest N articles per feed (the global `retention.default`, 250 unless you change it, or a per-feed
value) and trims after each fetch and when you lower a value. Starred articles are never trimmed. Trimmed articles leave
small stubs so that read state and Reader API ids stay consistent, and `retention.restore_days` (90 by default) keeps
enough to bring a recently trimmed article back; a nightly job removes the trimmed records once they are at least 180 days old. Statistics events are never
trimmed.

SQLite reuses space freed by trimming for new articles, but it does not shrink the file by itself: Kipple does not run
`VACUUM` or use `auto_vacuum`, so `kipple.db` stays at its high-water mark after you lower retention or delete many
feeds. A size that stops growing is normal, and `kipple.db-wal` next to it is a transient write log that Kipple
checkpoints regularly. If you want the file smaller:

1. Export a backup (Settings > Account > Export backup), or copy the nightly snapshot. Both are written with
   `VACUUM INTO`, so they are compacted copies.
2. Restore that copy with `kipple restore` (see [Restore a backup](#restore-a-backup)).

Space to keep free on the volume:

| When | Free space needed beyond what the database already occupies |
|---|---|
| Steady state | The nightly snapshot lives on the same volume: plan for about 2 times the database in total. |
| Export | About 2.2 times the database, temporarily (the snapshot copy plus the zip). Over 4 GiB an export is refused: copy the nightly snapshot instead. |
| Upgrade that migrates the schema | The database size plus 64 MB, plus 1.1 times the database for the pre-migration snapshot. The newest three pre-migration snapshots are kept, each about one more copy of the database. |
| Restore | The new database, plus the previous one kept under `backup/pre-restore-*` (newest three kept). |
