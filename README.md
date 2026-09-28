# Kipple

[![CI](https://github.com/WPTK/Kipple/actions/workflows/ci.yml/badge.svg)](https://github.com/WPTK/Kipple/actions/workflows/ci.yml)
[![Latest tag](https://img.shields.io/github/v/tag/WPTK/Kipple?label=release&sort=semver)](https://github.com/WPTK/Kipple/tags)
[![License: Blue Oak 1.0.0](https://img.shields.io/badge/license-Blue%20Oak%201.0.0-blueviolet)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![Last commit](https://img.shields.io/github/last-commit/WPTK/Kipple)](https://github.com/WPTK/Kipple/commits/main)
[![Open issues](https://img.shields.io/github/issues/WPTK/Kipple)](https://github.com/WPTK/Kipple/issues)

A self-hosted RSS reader, built for one reader: yours. No ads, no algorithm, no tracking, nobody else's
data mixed in, just your feeds, kept in a small SQLite database on a server you control, read the way
you like to read. It builds to one Docker container with one port, and it talks the Google Reader sync API,
so apps like Reeder Classic and NetNewsWire can act as a second client on the same account.

> **Status: prerelease (beta).** The fetch and sync core, the reading UI, an installable offline-capable app,
> and reading statistics with a yearly Wrapped summary are all done and in daily use; testing before 1.0 is
> under way (see [docs/RELEASING.md](docs/RELEASING.md)). See [CHANGELOG.md](CHANGELOG.md) for what has
> shipped release by release.

<!-- TODO: drop in a real screenshot or GIF of the reading UI (e.g. docs/screenshots/reading.png).
     None exists in the repo yet; the Editorial or Cards layout would make the strongest first impression. -->

## Why Kipple

FreshRSS and Miniflux are built to look and feel like admin panels: dense lists, unread counts, sidebars.
Kipple is built to look and feel like a reading app instead, closer to Feedly's magazine layouts, while
still being a single Go binary with an embedded SQLite database and no dependency on anyone else's service.
It speaks the same Google Reader sync API those tools do, so existing sync clients like Reeder Classic and
NetNewsWire work against it unmodified.

## Contents

- [What it's like to use](#what-its-like-to-use)
- [Quickstart](#quickstart)
- [Configuration](#configuration)
- [Support](#support)
- [Roadmap](#roadmap)
- [For developers](#for-developers)
- [About the name](#about-the-name)
- [License](#license)

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
- Reeder Classic, NetNewsWire, and other apps that support the Google Reader sync API work against the same
  account, keeping read and starred state in sync with the web app.
- No ads, no tracking, no account anywhere else, no social features (no other people's data, no
  comparisons), no AI.

## Quickstart

```
git clone https://github.com/WPTK/Kipple.git
cd Kipple
cp .env.example .env
cp docker-compose.example.yml docker-compose.yml
```

Edit `.env`: set `KIPPLE_PASSWORD` (5-256 characters) at minimum. Everything else has a working default.

```
docker compose build
docker compose up -d
```

Open `http://127.0.0.1:7080` and sign in with `KIPPLE_USERNAME` / `KIPPLE_PASSWORD` from `.env` (default
username `owner`). That's the whole happy path: one image, one container, one port, no database to set up
separately.

To sync with Reeder Classic, NetNewsWire, or another Google Reader-API client, run
`docker exec -it kipple /kipple api-password` once Kipple is running, to generate a Reader API password.

To put it on your phone, open Kipple's address in Safari (iPhone/iPad) or Chrome (Android), then use "Add
to Home Screen" (Safari's share sheet) or "Install app" (Chrome's menu). It launches full-screen from your
home screen from then on, like any other app, and keeps already-read articles available without a
connection.

For anything past this (backups, restoring, running behind a reverse proxy or tunnel, optional Cloudflare
Access sign-in), see [docs/deploy.md](docs/deploy.md). It's written from the maintainer's own two-machine
setup (one box running Kipple, one for admin/backups over SSH), but says up front how that collapses to a
single machine, which is what most people running this will actually have.

## Configuration

Kipple is configured with environment variables; [.env.example](.env.example) documents every one, and
[docker-compose.example.yml](docker-compose.example.yml) shows a hardened container setup. Everything else,
themes, fonts, layouts, retention, sync behavior, and so on, is set in the app's own Settings screen, per
device or for the account, with no need to touch the container again.

Variables self-hosters actually need to look at, beyond the required password:

| Variable | Purpose |
| --- | --- |
| `KIPPLE_USERNAME` / `KIPPLE_PASSWORD` | Web login, created on first start. |
| `KIPPLE_PUBLIC_URL` | Public URL, used for feed icons in sync clients. |
| `KIPPLE_TRUSTED_PROXY_IPS` | Required if Kipple sits behind a reverse proxy or tunnel. |
| `KIPPLE_ACCESS_TEAM_DOMAIN` / `KIPPLE_ACCESS_AUD` | Optional Cloudflare Access integration. |
| `TZ` | IANA time zone; defaults to `America/New_York`. |

## Support

Something broken or missing? Open an issue on [GitHub Issues](https://github.com/WPTK/Kipple/issues).
There's no chat room or mailing list; issues are the one place to ask.

## Roadmap

Kipple is working toward a 1.0 release; see [docs/RELEASING.md](docs/RELEASING.md) for what that involves
and [CHANGELOG.md](CHANGELOG.md#unreleased) for what's landed since the last tag.

## For developers

Everything above is all a self-hoster needs. This section is for changing Kipple itself.

- `cmd/kipple/` and `internal/` are the Go server (fetching, storage, the sync API, the web API).
- `web/` is the React app, built into the Go binary.
- `docs/design.md` is the source of truth for how it works;
  `docs/ui-decisions.md` records the design decisions; `docs/deploy.md` covers backups, recovery and deploys.
  See [docs/README.md](docs/README.md) for a full index of everything under `docs/`.
- `CLAUDE.md` holds the project rules used when working on the code with Claude.

### Build and test

- Go tests: `go test ./...`
- Web app: `cd web && npm ci && npm test && npm run build`
- Image: `docker build -t kipple:dev .`
- Local development with sample feeds is described in `web/README.md`.

## About the name

"Kipple" is Philip K. Dick's word, from *Do Androids Dream of Electric Sheep?* (the novel *Blade Runner* is
based on): junk that accumulates on its own when nobody's tending to it, gum wrappers, junk mail, yesterday's
newspaper.

> "Kipple drives out nonkipple."
> (Philip K. Dick, *Do Androids Dream of Electric Sheep?*)

An RSS reader is exactly the kind of thing that turns into kipple if you let it: thousands of unread items
piling up, feeds nobody's pruned in years. The name's a reminder not to let that happen here.

## License

Kipple is licensed under the [Blue Oak Model License 1.0.0](LICENSE), a short, plain-English
permissive license: use, modify and share it freely, including commercially, as long as everyone you pass
it on to also gets the license text.
Third-party components and their licenses are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)
(regenerate with `node scripts/gen-notices.mjs` after `cd web && npm ci`).
