# Troubleshooting

Symptom first, then the cause and the fix. Commands run on the machine that runs Kipple, in the directory of your
compose file; `kipple` is the container name both example compose files set. For anything that changes data
(restore, password reset) the full procedure is in [deploy.md](deploy.md).

- [First, collect the facts](#first-collect-the-facts)
- [The container is healthy but the page does not load](#the-container-is-healthy-but-the-page-does-not-load)
- [It works on this machine but not from another device](#it-works-on-this-machine-but-not-from-another-device)
- ["Kipple refused this request because of the address it was sent to" (421)](#kipple-refused-this-request-because-of-the-address-it-was-sent-to-421)
- [Sign-in says "busy"](#sign-in-says-busy)
- [Sign-in works but actions fail behind a proxy](#sign-in-works-but-actions-fail-behind-a-proxy)
- [Feeds are not updating](#feeds-are-not-updating)
- [A Reader API client will not connect](#a-reader-api-client-will-not-connect)
- [Kipple will not start after an upgrade](#kipple-will-not-start-after-an-upgrade)
- [Restore a backup](#restore-a-backup)
- [Reset a forgotten password](#reset-a-forgotten-password)

## First, collect the facts

    docker logs --tail 100 kipple
    docker exec kipple /kipple version -v
    docker ps --filter name=kipple

`version -v` prints the version, commit, build date, Go version, platform and database schema. The logs are JSON, one
object per line, with a `level` field; `WARN` and `ERROR` lines are the ones to read first. Set `KIPPLE_LOG_LEVEL=debug`
in your `.env` for more detail, and put it back afterwards. Settings > About shows the same facts in the app, with a
**Copy debug info** button whose text holds no user name, host name, URL, path or secret, so it is safe to paste into an
issue. When you report a problem, include the `version -v` output, how you run Kipple, whether a reverse proxy or tunnel
is in front, and the relevant log lines.

`http://127.0.0.1:1919/_status` (use your published port) is a plain status page that needs no sign-in and says whether
setup is pending.

## The container is healthy but the page does not load

"Healthy" means the process answers on its own address inside the container (`/healthz`), nothing more. It says
nothing about whether your browser can reach it.

1. **The port mapping does not match the port Kipple listens on.** Kipple listens on 1919 unless `KIPPLE_ADDR` says
   otherwise, and the container side of the mapping must be that port: `127.0.0.1:8080:1919` is right for the default,
   `127.0.0.1:8080:8080` is wrong unless `KIPPLE_ADDR=:8080`. A wrong mapping answers nothing while the health check
   still passes. See what the container really runs with:

       docker port kipple
       docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' kipple

   If you set `KIPPLE_ADDR`, change the container side of the mapping to the same port. If you only want a different
   port on your machine, change only the left-hand number and leave `KIPPLE_ADDR` unset.
2. **The address is not the one published.** The example files publish on `127.0.0.1`, which is this machine only.
   See the next section.
3. **The container is restarting.** `docker ps` shows `Restarting` or an uptime of seconds. `docker logs kipple` names
   the reason: a port already in use (change the host side of the mapping), a bind mount at `/data` that the container
   user cannot write (`chown 65532:65532` the directory), or a refused start after an upgrade (see below).
4. **Setup is pending.** A new Kipple answers with the setup form, not a sign-in screen. That is normal.

## It works on this machine but not from another device

The example compose files publish `"127.0.0.1:1919:1919"`, which accepts connections from this machine only. To reach
Kipple from your LAN use `"1919:1919"`, or your Tailscale address (`"100.x.y.z:1919:1919"`). Create your account first,
because until then anyone who can reach the port can create it, and read "Open mode" in [deploy.md](deploy.md#open-mode-no-password)
before choosing no password together with a wider bind.

Then check, in order:

- The address you type: `http://<server address>:1919`, plain HTTP. Kipple speaks no TLS itself.
- The host firewall. A Docker-published port bypasses `ufw` on Linux, but other firewalls and Windows still apply.
- The installable app (Add to Home Screen) and the service worker need HTTPS or `127.0.0.1`; reading in a browser tab over
  plain HTTP works. For HTTPS put a reverse proxy or tunnel in front: [reverse-proxy.md](reverse-proxy.md).

## "Kipple refused this request because of the address it was sent to" (421)

This is the Host check. While Kipple is in setup mode, or runs without a password (open mode), it answers only
requests addressed to an IP address, `localhost`, a `.localhost` name, a `.ts.net` name or the host of
`KIPPLE_PUBLIC_URL`; setup also accepts single-word names and `.local`, `.lan`, `.home.arpa` and `.internal` names. It
protects against DNS rebinding. Open Kipple by its IP address, or add the name you use to `KIPPLE_ALLOWED_HOSTS`
(comma-separated, for example `rss.example.com`) and restart, or add it to the `security.allowed_hosts` setting once you
can sign in. With a password the check only logs.

## Sign-in says "busy"

Sign-in answers `503 busy` (with a `Retry-After`) when it cannot take your attempt yet; it never locks you out. Two
causes:

- **Many wrong passwords from one address.** Five are free, then each wait doubles from two seconds to a minute; a
  correct password clears the count, and it is forgotten after an hour with no failure. Wait and try again.
- **A shared address.** Behind a reverse proxy or tunnel that Kipple does not know about, every visitor looks like the
  proxy and shares one budget, so one guesser (or your own typos) slows everyone. The log says so with a `WARN`: `proxy
  headers from an untrusted peer are ignored ... add its address to KIPPLE_TRUSTED_PROXY_IPS`. Set
  `KIPPLE_TRUSTED_PROXY_IPS` to the address your proxy connects from, as Kipple sees it (see
  [reverse-proxy.md](reverse-proxy.md)), and restart. Docker Desktop's gateway counts as such a shared address too.

The pacing is in memory; a restart clears it.

## Sign-in works but actions fail behind a proxy

Kipple refuses a state-changing request whose `Origin` does not match the address it was sent to, and for a browser
that sends no `Sec-Fetch-Site` it compares the scheme too. A proxy that rewrites `Host`, or that terminates HTTPS without
a trusted `X-Forwarded-Proto: https`, makes every action fail with 403 while the pages still load. Pass the original
`Host` through, send `X-Forwarded-Proto`, and list the proxy in `KIPPLE_TRUSTED_PROXY_IPS`
([reverse-proxy.md](reverse-proxy.md)).

## Feeds are not updating

- **Kipple fetches on its own schedule.** It polls in the background (every 30 minutes by default, or the per-feed
  interval) with conditional requests, and backs off feeds that keep failing. The web app's refresh button fetches all
  feeds now. A sync client's refresh does not fetch existing feeds, so a sync app showing nothing new right after you pulled
  to refresh is expected; new items appear when Kipple has fetched them.
- **See why one feed is stuck.** Feed Health in the web app lists feeds with errors, the last error and the next
  attempt. Common causes: the site blocks the fetch (HTTP 403 or 429), a certificate problem, a feed that moved, or a feed
  that needs a login. A feed on a private address needs "Allow addresses on my own network" in its own options (the
  add dialog offers it when it refuses a private address).
- **A site address instead of a feed.** A web page or site address added from a sync client or an OPML file is
  replaced by the feed the page links, on its first fetch. If the page links none, Feed Health says so ("this address
  is a web page, and the page does not link to a feed"): find the feed's address on the site and edit the feed's URL.
- **Nothing runs until the account exists.** A Kipple still in setup mode fetches nothing.
- **The clock or time zone looks wrong.** The nightly work and the statistics use the `tz` setting; fetching does not.
- **Disk full.** A full volume stops writes. See the disk-space notes in [deploy.md](deploy.md#disk-space-during-an-upgrade)
  and [Database size](deploy.md#database-size-and-compacting).

## A Reader API client will not connect

- The server address is your Kipple address plus `/api/greader.php`, for example `https://rss.example.com/api/greader.php`.
  The user name is the account's. The password is the **Reader API password**, not the web password. Make or replace it in
  Settings > Account & Devices, or with `docker exec -it kipple /kipple api-password`.
- Open `<address>/api/greader.php/check/compatibility` in a browser: `PASS` means the path reaches Kipple. If you get a
  login page from another service, a proxy or Cloudflare Access is intercepting it. Only `/api/greader.php` needs to
  skip an access layer, since sync apps cannot sign in to one.
- An authentication error after a password reset or a restore: resetting the web password revokes every Reader API
  token, so re-enter the Reader API password in the app.
- Use `https://` when the app connects through a proxy or tunnel, and `http://` plus the port on a LAN.
- To see what a client sends, set `KIPPLE_LOG_LEVEL=debug` and `KIPPLE_LOG_GREADER_FORMS=1` for a while (passwords and
  tokens are always redacted).

## Kipple will not start after an upgrade

`docker logs kipple` shows the refusal. Two common ones:

- `not enough free disk space to migrate the database`: nothing was changed. Free space and start again; the amounts are
  in [deploy.md](deploy.md#disk-space-during-an-upgrade).
- `database schema version N is newer than this binary`: you started an older image on a database a newer one migrated.
  Do not edit the database. Follow [Roll back an upgrade](deploy.md#roll-back-an-upgrade-that-migrated-the-schema), or
  run the newer image again.

## Restore a backup

With the zip in the current directory and the service stopped (this verifies first, then replaces the database):

    docker compose stop kipple
    docker compose run --rm -T --no-deps kipple restore - < kipple-backup-YYYYMMDD-HHMMSS.zip
    docker compose run --rm -T --no-deps kipple restore - --yes < kipple-backup-YYYYMMDD-HHMMSS.zip
    docker compose up -d kipple

On PowerShell wrap the lines with `cmd /c "..."`, because PowerShell has no `<`. "kipple is running" means the service is
still up. Everything else about restoring, including a new empty volume and undoing a restore, is in
[deploy.md](deploy.md#restore-a-backup).

## Reset a forgotten password

    docker exec -it kipple /kipple password

It works while the server runs, signs out every web session and revokes every Reader API token, so enter the Reader API
password again in your sync apps afterwards. A lost Reader API password has its own command,
`docker exec kipple /kipple api-password`. Details: [deploy.md](deploy.md#reset-the-web-password).
