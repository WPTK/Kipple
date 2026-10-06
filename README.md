# Kipple

> When nobody's around, kipple reproduces itself.
>
> Philip K. Dick, *Do Androids Dream of Electric Sheep?* (1968)

A self-hosted RSS reader for one person. One Docker container, one port, one SQLite database.

[![CI](https://github.com/WPTK/Kipple/actions/workflows/ci.yml/badge.svg)](https://github.com/WPTK/Kipple/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/WPTK/Kipple?include_prereleases&label=release)](https://github.com/WPTK/Kipple/releases)
[![License: Blue Oak 1.0.0](https://img.shields.io/badge/license-Blue%20Oak%201.0.0-blueviolet)](LICENSE)
[![Container image](https://img.shields.io/badge/image-ghcr.io%2Fwptk%2Fkipple-blue?logo=docker&logoColor=white)](https://github.com/WPTK/Kipple/pkgs/container/kipple)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/desktop-dark.webp">
    <img src="docs/screenshots/desktop-light.webp" alt="Kipple on a desktop in the Cards layout: a folder tree on the left, and a grid of article cards with large photos." width="640">
  </picture>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/phone-dark.webp">
    <img src="docs/screenshots/phone-light.webp" alt="Kipple on a phone in the Inbox layout: one article per row with the feed name, headline, snippet and a thumbnail." width="190">
  </picture>
</p>

Kipple is in beta. It is in daily use, and 1.0 follows more testing. [CHANGELOG.md](CHANGELOG.md) lists what shipped in
each release.

## What you get

- Five layouts: Editorial, Cards, Compact, Email Compact and Inbox. Set one per device or per feed.
- 20 color themes, with separate day and night picks. 11 bundled fonts, five text sizes, five densities.
- Nested folders, and OPML import and export.
- Full-text extraction for feeds that publish only a summary.
- Search, starred articles, saved searches, and rules that mute, star or mark articles as read.
- The newest 50, 100, 250, 500 or 1000 articles per feed, or all of them. Starred articles are never trimmed.
- Add it to a phone's home screen. Articles you have already read open offline.
- Reading statistics and a yearly Wrapped summary, stored in your own database. Nothing is sent anywhere.
- A Google Reader API at `/api/greader.php`, so any app that speaks it syncs with the same account.
- A backup zip you download from the web app, and a command that restores it.
- One account. The password is optional.

## Install

Pull the image and start it. Replace `<version>` with the newest tag on the
[releases page](https://github.com/WPTK/Kipple/releases) (the `latest` tag exists from the first stable release).

```
docker run -d --name kipple --restart unless-stopped -p 127.0.0.1:1919:1919 -v kipple_data:/data ghcr.io/wptk/kipple:<version>
```

Open <http://127.0.0.1:1919>. The first screen creates your account. The setup wizard after it takes about a minute:
time zone, themes, an OPML import from your old reader, a few starter feeds, and the address your other devices use.
Every step after the account can be skipped.

Until you create the account, anyone who can reach the port can. The command above publishes the port on `127.0.0.1`
only, so only this machine can. Widen it after you have an account.

The [deploy guide](docs/deploy.md) covers the compose file, reaching Kipple from your phone or over HTTPS, backups,
upgrades, verifying the image signature and building from source.

## Sync with other apps

In an app that speaks the Google Reader API, set the server to your Kipple address plus `/api/greader.php`, and use
your Kipple user name with the API password. The last wizard step shows the API password, and you can make a new one in
Settings, Account & Devices.

## What Kipple leaves out

Kipple is for one person, and these are left out on purpose. Requests for them are closed.

- Podcasts, and audio or video players.
- Webhooks and other integrations with outside services.
- Tags. Feeds live in folders, and articles can be starred.
- AI features.
- Notifications.
- Social features.
- Monitoring or analytics.\*
- More than one user.

\* Kipple keeps reading statistics, but they live in your own database, only you see them, and nothing sends them anywhere.

## Documentation

- [Deploy guide](docs/deploy.md): install options, ports, backups, restore, upgrades, Cloudflare Access.
- [Reverse proxy](docs/reverse-proxy.md): HTTPS with Caddy, nginx or Traefik.
- [Troubleshooting](docs/troubleshooting.md)
- [Design](docs/design.md) and [UI decisions](docs/ui-decisions.md): how it works and why.
- [Performance](docs/performance.md): 500 feeds and 150,000 articles, measured.
- [All docs](docs/README.md)

To report a bug, open an [issue](https://github.com/WPTK/Kipple/issues). To send a change, read
[CONTRIBUTING.md](CONTRIBUTING.md). Vulnerabilities go through [SECURITY.md](SECURITY.md).

## License

[Blue Oak Model License 1.0.0](LICENSE): use, modify and share it, including commercially. Third-party licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
