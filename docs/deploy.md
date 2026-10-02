# Kipple: deploy, backups and recovery

Naming in these docs: **Host-A** is the machine that runs Kipple (the app host) and **Host-B** is the machine you run
admin commands and backups from. Hostnames, addresses and paths are placeholders; substitute your own.

**Most self-hosters run everything on one machine, and that's the simpler and equally supported case.** These
docs are written from a two-host workflow because that's how the maintainer happens to run it (a separate
admin/backup machine, reached over SSH), not because Kipple needs two hosts. On a single machine, Host-A and
Host-B are just the same box: drop the `ssh host-a '...'` wrapper and run the command directly where Docker
lives, and treat "copy it to Host-B" as "copy it somewhere off that machine" — a second disk, an external drive,
cloud storage, whatever you'd use for any other backup. Nothing here requires SSH, a second host, or the
specific paths shown; substitute your own compose project location and adjust for your OS's shell (the
PowerShell-specific notes below don't apply if you're on Linux/macOS with everything on one box).

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
| `/data/setup-token` | Left by a Kipple before 0.7, which asked for a setup code; this one needs none and deletes the file at every start | removed at start |
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

### What to back up

Kipple's state is split in two places, and a backup of one does not cover the other.

| Lives in the database (in every export zip and snapshot) | Lives in your compose file or `.env` (in no backup) |
|---|---|
| The account: user name, password hashes, account secret | `KIPPLE_ADDR` and the compose port mapping |
| Every setting in Settings, including the time zone (`tz`) and `security.allowed_hosts` | `KIPPLE_PUBLIC_URL`, `KIPPLE_TRUSTED_PROXY_IPS`, `KIPPLE_ALLOWED_HOSTS` |
| Feeds, folders, per-feed options, filters, feed logins | `KIPPLE_ACCESS_TEAM_DOMAIN`, `KIPPLE_ACCESS_AUD` |
| Read and starred state, the statistics history | `TZ`, `KIPPLE_DATA`, and the other tuning and logging variables |
| Device profiles | Image tag, resource limits (`mem_limit`, `GOMEMLIMIT`), the reverse proxy or tunnel setup |

`KIPPLE_USERNAME`, `KIPPLE_PASSWORD` and `KIPPLE_API_PASSWORD` are read only on the first start of an empty database and
ignored once an account exists, so they are not part of the state. If you set them, the plain text sits in `.env`:
remove the lines after setup, and keep `.env` somewhere private either way.

`security.allowed_hosts` has no precedence rule between the two places: the names in `KIPPLE_ALLOWED_HOSTS` and the
names in the setting are added together, so a restore brings back the setting's names and your `.env` must bring back
the rest. Where `TZ` is set it wins over the restored `tz`; if you lose `TZ` along with the old host, the zone from the
backup applies without any message.

Checklist, for a rebuild to be a copy and paste:

1. The data volume, as an export zip (above) or a tarball of the volume (below).
2. The compose file and the `.env`, with the image tag you ran (`docker inspect kipple --format '{{.Config.Image}}'`
   or `docker exec kipple /kipple version`).
3. Your reverse proxy or tunnel configuration, and any Cloudflare Access application settings.
4. For a bind mount, the host directory must be owned by uid 65532 on a new machine, for a fresh start or a restore.

There is no setup code, so a restore does not need one.

### Back up the volume itself

The live `kipple.db` and its `-wal` must not be copied while the server runs. Either stop the service first, or copy
only the nightly snapshot, which is always consistent. On a single machine (no SSH), a tarball of the whole volume, taken
while Kipple is stopped:

    docker stop kipple
    docker run --rm -v kipple_data:/data:ro -v "$PWD":/out alpine tar czf /out/kipple-data.tar.gz -C /data .
    docker start kipple

Use your compose project's volume name (`docker volume ls`; `host-a_kipple_data` above). Add `--exclude=./imgcache` to
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
`kipple-backup-YYYYMMDD-HHMMSS.zip`. Save it off Host-A, for example on Host-B in
`<backup-dir>\kipple\`.

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

The database only grows so much: an export is refused above 4 GiB. Past that, use the nightly
snapshot with `docker cp`:

    ssh host-a 'docker cp kipple:/data/backup/kipple-snapshot.db /tmp/k.db' && scp host-a:/tmp/k.db '<backup-dir>\kipple\' && ssh host-a 'rm /tmp/k.db'

That copies the snapshot only, never the live database or WAL.

## OPML import and export

Feeds screen > Export OPML downloads the subscription list (`GET /api/opml`), the same file as `feeds.opml` in a backup
zip. It carries folders, feed URLs and titles, and the per-feed options Kipple adds (interval, retention, full text, and
the like, as `kipple:` attributes that other readers ignore). It does **not** carry read or starred state, filters,
settings, the statistics history, the account or feed logins. If OPML is all you keep, those are lost on a restore from it.

`kipple import [-mark-read-older-than-days N] <file.opml | ->` is safe while the server runs and prints JSON on standard output. The flag must come before the file. Pipe the file in, because the container user cannot read a bind-mounted `/import`:

    ssh host-a 'docker exec -i kipple /kipple import -' < feeds.opml

New feeds are fetched on the scheduler's next tick. `docker exec kipple /kipple version` prints the running build.

Health: `ssh host-a 'curl -s http://127.0.0.1:1919/healthz'` answers `ok` (use your published port, 7080 on an install that
sets `KIPPLE_ADDR=:7080`; see "Ports" below).

## Health check and container hardening

The image carries a `HEALTHCHECK` (every 30 s, 5 s timeout, 40 s start period, 3 retries) that runs
`/kipple healthcheck`. That subcommand does a GET on `http://127.0.0.1:<port>/healthz` (the port comes
from `KIPPLE_ADDR`; a `0.0.0.0`, `::` or empty host becomes `127.0.0.1`; with `KIPPLE_ADDR` unset it probes
1919), makes that one probe, waits at most 3 s and exits 0 only on HTTP 200 `ok`, otherwise printing a line and exiting 1.
`/healthz` needs no login and touches no database: it answers `ok` as long as the HTTP server is serving. So
"healthy" means the process is up and answering, not that feeds are fetching, and not that Kipple has been set up:
a container that is waiting for you to create its account is healthy.

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
| `cap_drop: [ALL]` | Removes all Linux capabilities; the app needs none (port 1919 is unprivileged). |
| `security_opt: no-new-privileges:true` | Blocks privilege escalation through setuid binaries. |
| logging `json-file` 10m x 3 | Bounded container logs. |

If you run the image with plain `docker run`, the same flags are `--read-only --tmpfs /tmp --cap-drop ALL
--security-opt no-new-privileges:true --pids-limit 200`. The README's pull-and-run file
(`docker-compose.pull.example.yml`) keeps the hardening that works without edits and leaves out the resource limits and
log rotation, which stay in `docker-compose.example.yml`.

### Host-A compose options (applied)

Host-A's `kipple` service has carried these since the 0.3.0-alpha.2 deploy. For a compose file from before
0.3.0-alpha.1, the change to the `kipple` service of `/home/user/stack/docker-compose.yml` is:

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
    +        BUILD_DATE: ${KIPPLE_BUILD_DATE:-unknown}

`BUILD_DATE` is optional (it shows on the About screen and in `kipple version -v`); set `KIPPLE_BUILD_DATE` in the
deploy command (for example `$(date -u +%Y-%m-%dT%H:%M:%SZ)`) to fill it in.

No healthcheck line is needed: it comes from the image, so the rebuild picks it up. Before applying such a change,
`docker compose ... config` shows the merged result; afterwards check `docker ps` reaches `(healthy)`
within about a minute. To back out, remove the hardening lines and `up -d kipple` again.

## First run: create your account

A Kipple with no account starts in **setup mode**. It is the normal server, but the browser shows the setup wizard
instead of a sign-in screen, and its first step is the form that creates your account. There is no setup code: open the
address and create your account, then the wizard takes you the rest of the way (time zone, theme, OPML import,
recommended feeds, an optional Reader API password).

- **Who can create the account.** Whoever gets there first. An unclaimed Kipple is simply "no account yet": the one
  request that creates the account succeeds for exactly one caller, and any other that arrives at the same moment is
  told Kipple was just set up (409) and is not signed in. That is how Jellyfin, Gitea and Home Assistant set
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
  401. Nothing is fetched and no maintenance runs until the account exists: the scheduler starts at the claim.
- **What signed-out visitors can see.** `GET /api/instance` answers without signing in (from an address the Host gate
  admits). It says whether setup is pending, the sign-in mode (`open`, `access` or `password`) and, while pending,
  whether Cloudflare Access is configured and whether open mode would work from where they are. No version, username
  or feed data.
- **Lifetime.** Once the account row exists the setup route is gone (it answers 404) for as long as that database is
  used.
- **Setup is not health.** `/healthz` and the container's health check answer `ok` in setup mode: healthy means serving,
  not configured. `/_status` says "Setup is pending" until an account exists.
- **Env credentials skip it.** With both `KIPPLE_USERNAME` and `KIPPLE_PASSWORD` set on a first start, Kipple creates the
  account from them and starts in normal mode, with no wizard onboarding and no unclaimed window. A lone
  `KIPPLE_USERNAME` (the default in old example files) is ignored and the wizard asks.
- **Cloudflare Access.** Access proves who may reach the app, not who owns this instance, so it does not replace
  creating the account. The wizard offers "No password, through Cloudflare Access" only on a request that came through
  Access and carries a verified token.
- **Upgrading from 0.5 or 0.6.** Kipple before 0.7 printed a setup code and asked for it. That is gone, and so is
  `kipple setup-token` (it now only says so, and is removed in 1.0). An instance that was upgraded while still
  unclaimed simply shows the account form; a stale `/data/setup-token` file is deleted at start.

After the account step the wizard continues as an ordinary signed-in session. Each step saves as it goes, so a reload
or "Skip for now" loses nothing. Settings > Account & Devices > Run setup again repeats the steps after the account for
any account (it never touches the account).

## Ports

The default listen address is `:1919`. Set `KIPPLE_ADDR` to choose any other address. If the address is taken, Kipple
exits with an error that names it and `KIPPLE_ADDR`; it never picks another port by itself (a container has its own
network, so this only happens with the bare binary).

**Installs that used 7080 must set it (0.6.0).** In 0.5 a database that already had an account before 0.5 kept
listening on the old default `:7080` while `KIPPLE_ADDR` was unset, with a WARN at every start. That fallback was removed
in 0.6.0: an unset `KIPPLE_ADDR` now always means `:1919` (and exits with an error if it is taken), whatever the database says, and a restore
no longer carries a port with it. To stay on 7080 set `KIPPLE_ADDR=:7080` and keep the `7080:7080` mapping; to move, set
`:1919` and change the published port in the compose file and anything that connects to it: a reverse proxy, a tunnel, a
bookmark, sync clients. Without either, the container listens on 1919 behind a mapping for 7080 and looks dead. The
container's health check probes exactly the address Kipple listens on: `KIPPLE_ADDR`, or 1919 when unset.

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

The wizard's account step offers **No password at all**. It is for a Kipple that only you can reach: this computer, or
your tailnet. The screen shows the warning verbatim in substance: anyone who can reach the address can read and change
everything. It needs a ticked acknowledgement, and it stores the account with no password hash and `auth_mode = open`.
Sign-in then happens by itself when the app opens: it asks the server for a session, and the server grants one only if
the request passes the **open gate**:

1. **The name is expected.** The `Host` header must be an IP address, `localhost`, a `.localhost` or `.ts.net` name,
   the host of `KIPPLE_PUBLIC_URL`, or in `KIPPLE_ALLOWED_HOSTS` / Settings > Allowed host names. Anything else gets
   `421 Misdirected Request` naming those settings. This defeats DNS rebinding, where a hostile web page tries to use
   your browser to reach a private address. `http://<ip>:1919` always works. Setup mode also accepts single-word names
   and `.local`, `.lan`, `.home.arpa` and `.internal` names; open mode does not, because any device on your network can
   answer those (a `.local` name over mDNS, a single word over LLMNR or NetBIOS, a DHCP host name under `.lan` on many
   routers) and so point one at your computer and drive your browser into Kipple. List such a name explicitly
   (`nas`, `*.local`) if you use one and trust every device on the network. The check is enforced in setup mode and
   open mode; with a password it only logs, once an hour.
2. **Not forwarded.** A request that came through a proxy or tunnel (a `CF-Connecting-IP`, `Cf-Access-Jwt-Assertion`,
   `Forwarded`, `X-Real-IP` or `X-Forwarded-*` header, a `Tailscale-Funnel-Request`, or a peer listed in
   `KIPPLE_TRUSTED_PROXY_IPS`) is refused, because a tunnel means the port is published to people you did not pick.
   The one exception is Tailscale Serve (tailnet-only HTTPS to a `.ts.net` name), recognised by exactly what
   `tailscaled` sends and nothing a client can choose alone: a loopback peer, a `.ts.net` Host, one `X-Forwarded-For`
   address in Tailscale's range, `X-Forwarded-Host` equal to the Host, `X-Forwarded-Proto` `https` if present, and a
   Tailscale address on this machine. A reverse proxy in front that passes the Host through reports the real client
   address in `X-Forwarded-For` and is refused.
3. **A near peer.** The connection must come from this computer (loopback) or a Tailscale address (`100.64.0.0/10`,
   `fd7a:115c:a1e0::/48`) that reached this machine on its own Tailscale address. A packet from that range arriving on
   the LAN interface is not the tailnet (`100.64.0.0/10` is also carrier-grade NAT space) and counts as a LAN peer.
   What Kipple cannot check is a LAN device that routes a forged tailnet-range packet at this machine's Tailscale
   address itself; on Linux Tailscale's own firewall rule drops those, on other systems keep open mode to machines on a
   network you trust. Devices on the local network
   are refused unless you turn on **Settings > Account & Devices > Also allow devices on my local network**
   (`security.open_lan`), which lets every private-range address in. It also lets in a peer from the Tailscale range
   that did not arrive on this machine's Tailscale address, but only when the connection reached a private-range
   address of this machine (the LAN interface or a container's bridge). A peer from `100.64.0.0/10` that reached a
   CGNAT, public or unknown local address is refused, since that range is also carrier-grade NAT, cloud and
   Kubernetes overlay space. Inside Docker every connection reaches the container's private bridge address, so open_lan
   cannot tell a CGNAT or overlay peer from a LAN peer there; the protection is the published port's bind address, so
   publish the port only on the LAN or tailnet interface.
4. **The browser says so.** The `Origin` must name the same host the request was sent to.

A signed-in session in open mode keeps passing the network part of the gate on every request, so a session cannot
outlive the position or the setting that admitted it. An open live-update stream (`/api/events`) is closed as soon as
the setting changes, and at its next heartbeat when the device moves.

**The Docker caveat.** Inside a container the peer is never loopback: Docker delivers even a
`-p 127.0.0.1:1919:1919` connection from its own bridge gateway, and on Docker Desktop, rootless Docker or IPv6 without
ip6tables it delivers other machines' connections from that same address too, so Kipple cannot tell them apart. A
container therefore treats that gateway as an ordinary LAN peer, and open mode works there only with "Also allow
devices on my local network" on. The wizard notices this and shows that checkbox in the account step. What keeps other
machines out then is the address you publish the port on, not Kipple: keep `127.0.0.1:` in the port mapping (or a
Tailscale address) whenever you use open mode, and never `1919:1919` on a LAN you do not fully trust. For the same reason
Tailscale devices reaching a container also need that checkbox, since Kipple cannot see your tailnet from inside it.
A non-container Kipple (a plain binary) on the same machine as the browser needs neither.

Changing your mind: **Settings > Set web password** (or `kipple password` on the host) gives the account a password and
returns it to normal mode; either signs every other session out. Going from a password account to open mode is not in
the Settings screen in 0.5 (it is `POST /api/account/password` with `{"current": ..., "open": true}`, which needs the current
password and passes the same gate); choose open mode in the wizard on a new install. The Reader API is unaffected in
open mode: sync apps still sign in with the API password, which is then the only credential that exists.

## Time zone

The time zone is one setting, `tz` (Settings > Account & Devices, and wizard step 3). It is the zone for daily reading
statistics, the nightly 04:10 maintenance and the weekly snapshot, backup file names and, from the next start, log
timestamps. It is read live, so a change takes effect at the next statistics write, summary request and nightly tick,
with no restart. The default for a new install is UTC, and the wizard preselects your browser's zone.

The `TZ` environment variable (IANA name) only gives a new install its first value: on a start where no `tz` setting
exists yet, Kipple stores `TZ` as the setting. After that `TZ` is not read for the zone, so changing or removing it no
longer moves statistics, the nightly job or backup names; choose in Settings. (Go itself still reads `TZ` for the first
start-up log lines and for the CLI subcommands.) An unknown name in `TZ` stops a start that would have stored it.

A change applies to new statistics only: rows already recorded keep the local date and hour of the zone that was in
effect when they were written, so a day never moves. Dates in the web app follow each device's own clock, whatever the
server zone is.

**Known limitation.** Kipple cannot tell an explicit choice of UTC from the untouched default, because both are stored as
`UTC`. So the wizard's time zone step, when it opens with `UTC` saved, treats it as "not chosen yet" and suggests your
browser's zone; that suggestion is only saved if you press Continue. (In a Run setup again session the saved zone is kept
as the choice.) An install upgraded from before 0.5 is not affected by the default at all: the upgrade wrote its zone
(`America/New_York`, or whatever it had already chosen). This was left as it is on purpose. (A `TZ` of `UTC` on a new install is stored as `UTC`, so it behaves the same way.)

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

Since 0.5.0-beta.1 a tag push publishes a signed, multi-arch (`linux/amd64`, `linux/arm64`) image at
`ghcr.io/wptk/kipple:<version>` (`docker-compose.pull.example.yml` in the repository is the ready file). Stable releases
also move `latest` and the `X.Y` and `X` tags; **a prerelease is tagged only with its exact version**, so until the first
stable release name the version. Verify a pull with cosign (the command is in the README and in each release's notes);
the signature identity is the release workflow of this repository. Upgrade by changing the tag and
`docker compose pull kipple && docker compose up -d kipple` (name the service). It is built from the same source
as a source build (a different build: single-architecture there, no provenance), so `kipple restore`, rollbacks and everything else in this file apply unchanged; for a rollback
across a migration, start the previous tag's image only after restoring the pre-migration snapshot (see below).

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
it too, `docker exec kipple /kipple api-password` sets a new one). The failed-login pacing is in
memory: it clears after an hour with no failure or on a restart. If `KIPPLE_PASSWORD` is still in
`/home/user/stack/.env`, remove it: it is read only when the account is first created. On an account in open mode
(no password) this sets a password and returns it to normal sign-in ("Open mode" above). If the account does not exist
yet, the command says so and points to the setup wizard.

When you can still sign in, change either password in Settings > Account & Devices instead; the CLI is the recovery path.
It is also how to give the account a password again after it was removed through Cloudflare Access and Access
validation was then turned off (see "Cloudflare Access (optional)" below).

## Cloudflare Access (optional)

If Cloudflare Access sits in front of Kipple, Kipple can verify the `Cf-Access-Jwt-Assertion` header Access adds
to every request it lets through (design §7.0). In `/home/user/stack/.env` set both (or neither; one without the
other stops startup):

    KIPPLE_ACCESS_TEAM_DOMAIN=yourteam.cloudflareaccess.com   # Zero Trust > Settings > Custom Pages; https:// optional, no path
    KIPPLE_ACCESS_AUD=<Application Audience (AUD) tag>          # Zero Trust > Access > Applications > your Kipple app > Overview

then `ssh host-a 'cd /home/user/stack && docker compose up -d kipple'`. The startup log says
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
the setup screens are gone. A backup from before 0.5 that is
migrated on that start is marked as already set up, so it never shows the wizard's onboarding. Only a backup taken
in setup mode (no account in it) returns the instance to setup mode: the account form again.
The listen port is never part of a backup: it comes from `KIPPLE_ADDR` (1919 when unset), so a restore does not
change it.

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
authentication error, sign in again in the app with that password. If the backed-up account had no web
password (removed through Cloudflare Access), sign in through Access with the same `KIPPLE_ACCESS_*` values, or set
one first with `kipple password` (it works on the stopped service:
`docker compose run --rm -T --no-deps kipple password --stdin`).

Do not start the server on the empty volume first: it would start in setup mode with a new database and a new setup
code, and the restore then replaces that database anyway (it is kept under `pre-restore-*`), so it only adds a step.

## Roll back an upgrade that migrated the schema

There are no down migrations. An older binary refuses a newer schema, so going back means
restoring the `pre-migration-<old>-<new>-<ns>.db` that the upgrade wrote before migrating.
Anything read, starred or fetched since the upgrade is lost.

(This is the "Rolling back" procedure that a refused start points to.) If the old image is started on the migrated
database without these steps, it does not start. A binary from 0.5.0 on names the Kipple that wrote the database and
what to do: `docker logs kipple` shows `kipple: store: store: database schema version 11 is newer than this binary (10); refusing to
start. This database was last opened by Kipple v0.6.0 (schema 11); this is Kipple v0.5.0 (schema 10). Run v0.6.0 or
newer, or restore the pre-migration snapshot from the backup folder (docs/deploy.md, Rolling back).` (the version and
schema numbers here are examples; a database from a build that never recorded its version says "It was written by a
newer Kipple than this binary." instead). Older binaries, which is what a rollback to 0.3.x is, print the shorter
`kipple: store: store: database schema version 10 is newer than this binary (9); refusing to start`. Either way the container exits with status
1 (with `restart: unless-stopped` it keeps restarting) and nothing is changed on the volume. Stop it, then:

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

### Schema 6 -> 7 (state changes for `ot`)

Migration 0007 adds `items.state_changed_at` and its partial index, and backfills the column from
`read_at`/`starred_at` in one `UPDATE` (a quick first start: one pass over the items that were ever
read or starred). It is what `greader.ot_includes_user_changes` reads; with that setting off (the
default) nothing a client sees changes. The first start writes
`/data/backup/pre-migration-6-7-<ns>.db` (or `pre-migration-5-7-<ns>.db` when coming straight from
schema 5), then migrates. A binary without 0007 refuses the schema-7 database with `database schema
version 7 is newer than this binary (6); refusing to start`, so a rollback is the same procedure:
stop kipple, `restore /data/backup/pre-migration-6-7-<ns>.db --yes` with the new image, then check
out, rebuild and start the old tag. Never copy the snapshot over `kipple.db` by hand, and never drop
the column by hand to make an older binary start. Anything read or starred since the upgrade is lost.

### Schema 7 -> 8 (stats event ids)

Migration 0008 adds the nullable column `stats_events.event_id` and the partial unique index
`idx_stats_event(event_id) WHERE event_id IS NOT NULL`, which lets the stats ingest drop a repeated
event (a retried flush or a repeated beacon) of any kind. It is not O(1): the ALTER is instant, but
the index build scans `stats_events` once inside the migration transaction (existing rows keep NULL,
so the index starts empty; the scan is fast at expected sizes). The first start writes
`/data/backup/pre-migration-7-8-<ns>.db` (or `pre-migration-6-8-<ns>.db` / `pre-migration-5-8-<ns>.db`
when coming straight from schema 6 or 5), then migrates. A binary without 0008 refuses the schema-8
database with `database schema version 8 is newer than this binary (7); refusing to start`, so a
rollback is the same procedure: stop kipple, `restore /data/backup/pre-migration-7-8-<ns>.db --yes`
with the new image (use the snapshot name that matches where the database came from), then check
out, rebuild and start the old tag. Never copy the snapshot over `kipple.db` by hand, and never drop
the column by hand to make an older binary start. Everything recorded since the upgrade, reads,
stars and statistics included, is lost on rollback.

### Schema 8 -> 9 (stats summary indexes)

Migration 0009 adds three partial covering indexes on `stats_events` (`idx_stats_open_cov`,
`idx_stats_rt_cov`, `idx_stats_scroll_cov`) for `GET /api/stats/summary`. It is not O(1): each index
is built by one scan of `stats_events` inside the migration transaction, about 2 s per million rows
for the three together (measured; a few milliseconds at typical sizes), and the indexes add about 60 MB per
million events (a 232 MB database grew to 300 MB at one million events). Kipple does not serve
meanwhile. The first start writes `/data/backup/pre-migration-8-9-<ns>.db` (or
`pre-migration-7-9-<ns>.db` and so on when coming from an older schema), then migrates. A binary
without 0009 refuses the schema-9 database with `database schema version 9 is newer than this binary
(8); refusing to start`, so a rollback is the same procedure: stop kipple, `restore
/data/backup/pre-migration-8-9-<ns>.db --yes` with the new image (use the snapshot name that matches
where the database came from), then check out, rebuild and start the old tag. Never copy the
snapshot over `kipple.db` by hand, and never drop the indexes by hand to make an older binary
start. Everything recorded since the upgrade, reads, stars and statistics included, is lost on
rollback.

**Disk space during the upgrade.** The upgrade is transiently much bigger than the indexes it
leaves. The pre-migration snapshot is a full copy of the database, and the whole index build is
held in the WAL until it is committed and checkpointed into the database file, so the peak is
about the database plus the snapshot plus twice the index size (database + database + 2 x index; this is
peak total usage): about 580 MB for a 232 MB database with a million events, with the volume returning to about
database + snapshot + index afterwards. Before the snapshot is written Kipple checks the free
space: it refuses to start, with `not enough free disk space to migrate the database ... (nothing
was changed)`, unless there is enough extra free space (as opposed to the peak total above): on the database's
volume, the size of the database file (without the WAL) plus 64 MB of headroom for the migration's
WAL and growth, and on the backup directory's volume, 1.1 times the database size for the snapshot;
when both are on the same volume (the default `/data` layout) the two add up. Nothing has been written at that point, so
free some space (older `backup/` files, exported archives, the image cache) and start again. If the
volume fills up despite the check, the migration transaction fails and is rolled back (the
database stays at schema 8 and an older binary keeps working); a full disk can also fail the
snapshot itself, which likewise leaves the database untouched. The check is skipped when the free
space cannot be read.

### Schema 9 -> 10 and upgrading from 0.3 to 0.5 (the setup wizard)

Migration 0010 rebuilds the one-row `account` table (adding `auth_mode` and `created_via`, with a check that open mode has
no password hash) and, for a database that already has an account, writes three settings so that nothing changes for it:
`sys.setup_completed_at` (an existing account never sees onboarding), `tz` set to `America/New_York` unless a time
zone was already chosen, and `sys.legacy_port` (a marker of 0.5's 7080 fallback; since 0.6.0 nothing reads it). It is quick (one row), and
the first start writes `/data/backup/pre-migration-9-10-<ns>.db` before migrating. A 0.3.x binary refuses the schema-10 database, so a
rollback is the procedure above with that snapshot, and a database created fresh by 0.5 has no 0.3 snapshot and stays on 0.5.

**Before upgrading an existing install, check two things** (`docker inspect` shows the container's environment, since the
image has no shell):

    ssh host-a "docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' kipple" | grep -E '^(KIPPLE_ADDR|TZ)='

1. **`KIPPLE_ADDR` is set.** Confirm it says `KIPPLE_ADDR=:7080` (or whatever port you publish). 0.5 kept
   7080 for an unset value and logged a WARN at each start; 0.6.0 removed that fallback, so an unset value is 1919 and a
   `7080:7080` mapping would point at nothing: set it explicitly before upgrading to 0.6.0. Your port mapping (for
   example `7080:7080`), reverse proxy, tunnel and sync clients keep working untouched. Moving to 1919 is optional and changes all of those.

What you will notice: nothing else. The setup wizard does not run for an existing account (Settings > Account & Devices >
Run setup again is there if you want the tour), sign-in is unchanged, and `KIPPLE_USERNAME`, `KIPPLE_PASSWORD` and
`KIPPLE_API_PASSWORD` left in `.env` remain harmless. Rehearse it first on a copy of a snapshot as UAT Suite 4 describes.

## Phase 1 to phase 2 (done 2026-09-25, v0.2.0-alpha.1)

Historical: this applies to a schema-1 database. With a build after alpha 2 the snapshot is `pre-migration-1-<latest>-*`, not `pre-migration-1-3-*`. Phase 1 (`v0.1.0`) has no export button and no restore command, and phase 2 migrates the schema
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
  behavior: Settings > Sync & Feeds > Images > "Load images through Kipple" = "Only insecure (http) images", or
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

