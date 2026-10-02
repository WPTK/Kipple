# Kipple

[![CI](https://github.com/WPTK/Kipple/actions/workflows/ci.yml/badge.svg)](https://github.com/WPTK/Kipple/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/WPTK/Kipple?include_prereleases&label=release)](https://github.com/WPTK/Kipple/releases)
[![SemVer](https://img.shields.io/badge/semver-2.0.0-blue)](https://semver.org/)
[![Release date](https://img.shields.io/github/release-date/WPTK/Kipple?include_prereleases)](https://github.com/WPTK/Kipple/releases)
[![Last commit](https://img.shields.io/github/last-commit/WPTK/Kipple)](https://github.com/WPTK/Kipple/commits/main)
[![Commit activity](https://img.shields.io/github/commit-activity/m/WPTK/Kipple)](https://github.com/WPTK/Kipple/graphs/commit-activity)
[![Open issues](https://img.shields.io/github/issues/WPTK/Kipple)](https://github.com/WPTK/Kipple/issues)

[![Test coverage](https://codecov.io/gh/WPTK/Kipple/graph/badge.svg)](https://codecov.io/gh/WPTK/Kipple)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/15120/badge)](https://www.bestpractices.dev/projects/15120)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/WPTK/Kipple/badge)](https://scorecard.dev/viewer/?uri=github.com/WPTK/Kipple)
[![Signed with cosign](https://img.shields.io/badge/signed-cosign%20(sigstore)-success)](#quickstart)
[![Security policy](https://img.shields.io/badge/security-policy-success)](SECURITY.md)
[![License: Blue Oak 1.0.0](https://img.shields.io/badge/license-Blue%20Oak%201.0.0-blueviolet)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/WPTK/Kipple?logo=go&logoColor=white&color=00ADD8)](go.mod)
[![Repo size](https://img.shields.io/github/repo-size/WPTK/Kipple)](https://github.com/WPTK/Kipple)

[![Container image](https://img.shields.io/badge/image-ghcr.io%2Fwptk%2Fkipple-blue?logo=docker&logoColor=white)](https://github.com/WPTK/Kipple/pkgs/container/kipple)
[![Image tags](https://ghcr-badge.egpl.dev/wptk/kipple/tags?ignore=sha*&n=3)](https://github.com/WPTK/Kipple/pkgs/container/kipple)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-informational)](https://github.com/WPTK/Kipple/pkgs/container/kipple)
[![Sync API](https://img.shields.io/badge/sync-Google%20Reader%20API-orange)](#what-its-like-to-use)
[![Keep a Changelog 1.1.0](https://img.shields.io/badge/changelog-Keep%20a%20Changelog%201.1.0-E05735)](CHANGELOG.md)
[![Conventional Commits 1.0.0](https://img.shields.io/badge/commits-Conventional%201.0.0-FE5196?logo=conventionalcommits&logoColor=white)](https://www.conventionalcommits.org/en/v1.0.0/)
[![Website: kipple.cc](https://img.shields.io/badge/website-kipple.cc-informational)](https://kipple.cc)
[![Views](https://hits.sh/github.com/WPTK/Kipple.svg?style=flat&label=views&color=lightgrey)](https://hits.sh/github.com/WPTK/Kipple/)

> "Kipple drives out nonkipple."
> (Philip K. Dick, *Do Androids Dream of Electric Sheep?*)

A self-hosted RSS reader, built for one reader: yours. No ads, no algorithm, no tracking, nobody else's
data mixed in, just your feeds, kept in a small SQLite database on a server you control, read the way
you like to read. It builds to one Docker container with one port, and it talks the Google Reader sync API,
so a Google Reader-API client can act as a second reader on the same account.

> **Status: prerelease (beta).** The fetch and sync core, the reading UI, an installable offline-capable app,
> and reading statistics with a yearly Wrapped summary are all done and in daily use; testing before 1.0 is
> under way (see [docs/RELEASING.md](docs/RELEASING.md)). See [CHANGELOG.md](CHANGELOG.md) for what has
> shipped release by release.

<!-- TODO: drop in a real screenshot or GIF of the reading UI (e.g. docs/screenshots/reading.png).
     None exists in the repo yet; the Editorial or Cards layout would make the strongest first impression. -->

## Contents

- [Why Kipple](#why-kipple)
- [What it's like to use](#what-its-like-to-use)
- [Quickstart](#quickstart)
- [Configuration](#configuration)
- [Support](#support)
- [Roadmap](#roadmap)
- [For developers](#for-developers)
- [License](#license)

## Why Kipple

Kipple looks and feels like a reading app: magazine-style layouts, real images up front, five ways to view
a feed. It's a single Go binary with an embedded SQLite database, nothing else to run. It speaks the Google
Reader sync API, so existing sync clients work against it unmodified.

## What it's like to use

- Switch anytime, from the layout button in the header, between five ways of browsing a feed: Editorial (a
  magazine layout with big lead images), Cards (a photo grid), Compact and Email - Compact (dense,
  text-first views at two densities), or Inbox (sender, subject, and snippet, like an email inbox). Set per
  device or per feed.
- 20 color themes, "Follow system," and an optional day/night schedule that switches themes on its own
  clock. 11 bundled reading fonts (serif and sans, including a dyslexia-friendly option), your device's own
  system fonts, five text sizes, and five density presets from Dense to Airy.
- Add Kipple to your phone's home screen and it runs as a PWA: full screen, no browser chrome, already-read
  articles available offline, and it updates itself in the background.
- When a feed only publishes a summary, Kipple can fetch and extract the full article automatically, so
  short feeds still read like full ones.
- Reading stats stay on your server, including a yearly Wrapped summary of how much you read and which
  feeds you spent the most time on. Wrapped's share sheet is opt-in; nothing is shared or sent anywhere on
  its own.
- Any app that speaks the Google Reader sync API works against the same account, keeping read and starred
  state in sync with the web app.
- No ads, no tracking, no account anywhere else, no social features (no other people's data, no
  comparisons), no AI.

## Quickstart

Kipple is one container with one port. There are no configuration files to edit: Kipple asks for what it needs in
your browser the first time you open it.

> **Which image?** The published image is `ghcr.io/wptk/kipple`. It is signed and built for `linux/amd64` and
> `linux/arm64`. If you would rather build it yourself, use [Build from source](#build-from-source) below; the steps
> after starting the container are the same either way. Releases before 1.0 are prereleases, and a prerelease is only
> ever tagged with its exact version (`latest` moves only on a stable release), so the examples name a version: use
> the newest one on the [releases page](https://github.com/WPTK/Kipple/releases).

### Run the published image

One command:

```
docker run -d --name kipple --restart unless-stopped -p 127.0.0.1:1919:1919 -v kipple_data:/data --read-only --tmpfs /tmp:size=64m,mode=1777 --cap-drop ALL --security-opt no-new-privileges ghcr.io/wptk/kipple:0.5.0-beta.2
```

Or the same thing as a compose file. Save it as `docker-compose.yml` (it is
[docker-compose.pull.example.yml](docker-compose.pull.example.yml)) and run `docker compose up -d`:

```yaml
services:
  kipple:
    image: ghcr.io/wptk/kipple:0.5.0-beta.2
    container_name: kipple
    restart: unless-stopped
    ports: ["127.0.0.1:1919:1919"]
    volumes: ["kipple_data:/data"]
    read_only: true
    tmpfs: ["/tmp:size=64m,mode=1777"]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
volumes:
  kipple_data:
```

Then:

1. Open **http://127.0.0.1:1919**. A new Kipple has no account, so the first screen is the form that creates it: there is
   no setup code and nothing to look up. (If something looks wrong, `docker logs kipple` shows what Kipple is doing;
   `kipple` is the container name both example files set.)
2. Follow the wizard. It takes about a minute, and every step after the account can be skipped:
   1. **Account**: a user name, then a password (or one of the two ways to go without, below).
   2. **Time zone**, preselected from your browser.
   3. **Theme**: one look for day and one for night.
   4. **Import** an OPML file from your old reader (or skip).
   5. **Recommended feeds**, a few good ones to start with (or skip).
   6. **Done**, with an optional Reader API password for Reeder or NetNewsWire.

**Until you have created your account, anyone who can reach the port can create it.** That is how every
self-hosted app that sets itself up in the browser works, and it is why the examples publish the port on `127.0.0.1`
(this machine only): create your account first, then widen the port if you want to. A headless install that has to
listen on a network before you can open a browser should create the account from the environment instead: set
`KIPPLE_USERNAME` and `KIPPLE_PASSWORD` for the first start and Kipple never shows the form. Whoever creates the
account owns the instance, including its feed network settings, so if you ever find Kipple already set up when you did
not do it, take the container down, delete its data volume and start again.

**Going without a password.** The account step offers "No password at all". Read its notice: anyone who can reach
Kipple's address can then read and change everything, so choose it only when Kipple is reachable from this computer,
your local network and [Tailscale](https://tailscale.com/) and nowhere else. Kipple refuses open sign-in through a reverse
proxy or tunnel and from public addresses. In Docker every connection arrives from Docker's own network, so Kipple cannot
tell your network from the internet: the address you publish the port on (`127.0.0.1:` in the examples above) is what
keeps other machines out, so never publish an open-mode Kipple on a public interface. You can set a password later in Settings, Account & Devices. Details:
[docs/deploy.md](docs/deploy.md), "Open mode".

**Reaching Kipple from your phone or another computer.** The examples publish the port on `127.0.0.1`, which is this
machine only. To use Kipple from your LAN or tailnet, change `127.0.0.1:1919:1919` to `1919:1919` (LAN) or to your
Tailscale address (`100.x.y.z:1919:1919`); with a password that is all it takes. Kipple speaks plain HTTP: for HTTPS
put a reverse proxy or tunnel in front (see [docs/deploy.md](docs/deploy.md)). If port 1919 is taken, change the left-hand
number (`127.0.0.1:8080:1919`); nothing else changes. A named volume (as above) is ready to use. A bind mount
(`-v /srv/kipple:/data`) needs `chown 65532:65532 /srv/kipple` first, because the container runs as that
unprivileged user.

To check the image before you run it (optional; needs [cosign](https://docs.sigstore.dev/cosign/)):

```
cosign verify ghcr.io/wptk/kipple:0.5.0-beta.2 \
  --certificate-identity-regexp '^https://github.com/WPTK/Kipple/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Build from source

Use this to run your own changes, or if you would rather not pull the published image. It needs Docker and Git only (no Go or Node).

```
git clone https://github.com/WPTK/Kipple.git
cd Kipple
cp docker-compose.example.yml docker-compose.yml
docker compose build
docker compose up -d
```

Then open **http://127.0.0.1:1919** and create your account, exactly as above. There is no
`.env` to create; [.env.example](.env.example) lists the optional overrides for people who want them, and the compose
file reads it when it exists (Docker Compose 2.24 or newer).

The image reports its version as `dev` unless you pass it (it shows in `kipple version`, the startup log and backups).
To stamp it with the release you cloned, set `KIPPLE_VERSION=$(git describe --tags --always)` and
`KIPPLE_VCS_REF=$(git rev-parse HEAD)` in the environment of the build step.

### After setup

To sync with a Google Reader-API client (Reeder Classic, NetNewsWire), use the API password from the last wizard
step, or make one any time in Settings, Account & Devices (or `docker exec -it kipple /kipple api-password`). The
server address is your Kipple address plus `/api/greader.php`; the user name is the one you chose.

To put it on your phone, open Kipple's address in Safari (iPhone/iPad) or Chrome (Android), then use "Add
to Home Screen" (Safari's share sheet) or "Install app" (Chrome's menu). It launches full-screen from your
home screen from then on, like any other app, and keeps already-read articles available without a
connection. The phone has to be able to reach Kipple (see above), and installing needs HTTPS or `127.0.0.1`.

**Back up.** Settings > Account > Export backup downloads a zip (database, OPML, readable settings, manifest). It holds
password hashes and feed logins, so keep it private. Kipple also writes a snapshot nightly at 04:10 to the same volume,
which does not survive losing the volume, so copy it off the machine on a schedule:
`docker cp kipple:/data/backup/kipple-snapshot.db ./kipple-snapshot.db`. To restore, stop the container and run
`docker compose run --rm -T --no-deps kipple restore - --yes < kipple-backup-YYYYMMDD-HHMMSS.zip` (without `--yes` it only
verifies). The backup holds your account, settings and feeds but not your compose file or `.env` (port, public URL, proxy
and Access settings, `TZ`): keep those too. The full checklist is in [docs/deploy.md](docs/deploy.md#what-to-back-up).

For anything past this (backups, restoring, upgrading from an older version, running behind a reverse proxy or
tunnel, optional Cloudflare Access sign-in), see [docs/deploy.md](docs/deploy.md).

## Configuration

Almost everything is set in the browser: the setup wizard covers the account, time zone, theme and feeds, and
Settings covers themes, fonts, layouts, retention, sync behavior and the rest, per device or for the account,
without touching the container again. Environment variables are optional advanced overrides;
[.env.example](.env.example) documents every one, and [docker-compose.example.yml](docker-compose.example.yml) shows
a hardened container setup with resource limits.

The ones self-hosters most often want:

| Variable | Purpose |
| --- | --- |
| `KIPPLE_ADDR` | Listen address, default `:1919`. Since 0.6.0 an unset value is always 1919; an install that used the old 7080 must set `KIPPLE_ADDR=:7080` (see [docs/deploy.md](docs/deploy.md)). |
| `KIPPLE_PUBLIC_URL` | Public URL, used for feed icons in sync clients. |
| `KIPPLE_TRUSTED_PROXY_IPS` | Required if Kipple sits behind a reverse proxy or tunnel. |
| `KIPPLE_ALLOWED_HOSTS` | Extra host names Kipple answers to during setup and without a password. |
| `KIPPLE_ACCESS_TEAM_DOMAIN` / `KIPPLE_ACCESS_AUD` | Optional Cloudflare Access integration. |
| `TZ` | IANA time zone for a new install: stored as the time zone setting on the first start only. Choose it in Kipple afterwards. |
| `KIPPLE_USERNAME` / `KIPPLE_PASSWORD` | Create the account from the environment instead of the wizard (scripted deploys). |

## Support

Something broken or missing? Open an issue on [GitHub Issues](https://github.com/WPTK/Kipple/issues).
There's no chat room or mailing list; issues are the one place to ask.

Want to contribute? See [CONTRIBUTING.md](CONTRIBUTING.md).

## Roadmap

Kipple is working toward a 1.0 release; see [docs/RELEASING.md](docs/RELEASING.md) for what that involves
and [CHANGELOG.md](CHANGELOG.md) (plus the pending entries in [changes/](changes/)) for what's landed.

## For developers

Everything above is all a self-hoster needs. This section is for changing Kipple itself.

- `cmd/kipple/` and `internal/` are the Go server (fetching, storage, the sync API, the web API).
- `web/` is the React app, built into the Go binary.
- `docs/design.md` is the source of truth for how it works;
  `docs/ui-decisions.md` records the design decisions; `docs/deploy.md` covers backups, recovery and deploys.
  See [docs/README.md](docs/README.md) for a full index of everything under `docs/`.
- `CLAUDE.md` holds the project rules used when working on the code with Claude.
- [CONTRIBUTING.md](CONTRIBUTING.md) covers how to report a bug, propose a change and send a pull request.

### Build and test

- Go tests: `go test ./...`
- Web app: `cd web && npm ci && npm test && npm run build`
- Image: `docker build -t kipple:dev .`
- Local development with sample feeds is described in `web/README.md`.

## License

Kipple is licensed under the [Blue Oak Model License 1.0.0](LICENSE), a short, plain-English
permissive license: use, modify and share it freely, including commercially, as long as everyone you pass
it on to also gets the license text.
Third-party components and their licenses are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
(regenerate with `node scripts/gen-notices.mjs` after `cd web && npm ci`).
