# Kipple

> *When nobody's around, kipple reproduces itself.*
>
> Philip K. Dick, *Do Androids Dream of Electric Sheep?* (1968)

A self-hosted RSS reader. That I want to use.

[![CI](https://github.com/WPTK/Kipple/actions/workflows/ci.yml/badge.svg)](https://github.com/WPTK/Kipple/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/WPTK/Kipple?include_prereleases&label=release)](https://github.com/WPTK/Kipple/releases)
[![License: Blue Oak 1.0.0](https://img.shields.io/badge/license-Blue%20Oak%201.0.0-blueviolet)](LICENSE)
[![Container image](https://img.shields.io/badge/image-ghcr.io%2Fwptk%2Fkipple-blue?logo=docker&logoColor=white)](https://github.com/WPTK/Kipple/pkgs/container/kipple)

<table>
  <tr>
    <td width="560" align="center" valign="bottom">
      <picture>
        <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/desktop-dark.webp">
        <img src="docs/screenshots/desktop-light.webp" alt="Kipple in a desktop browser: feeds on the left, a list of articles with thumbnails in the middle, and an article open on the right." width="560">
      </picture>
    </td>
    <td width="170" align="center" valign="bottom">
      <picture>
        <source media="(prefers-color-scheme: dark)" srcset="docs/screenshots/phone-dark.webp">
        <img src="docs/screenshots/phone-light.webp" alt="Kipple on a phone: article cards with large photos, one per row, and a tab bar along the bottom." width="170">
      </picture>
    </td>
  </tr>
  <tr>
    <td width="560" align="center"><sub><b>Web app.</b> Feeds on the left, the article list in the middle, the open article on the right.</sub></td>
    <td width="170" align="center"><sub><b>On a phone.</b> Add Kipple to the home screen and it opens full screen, like an app.</sub></td>
  </tr>
</table>

Kipple 1.0 is a release candidate. [CHANGELOG.md](CHANGELOG.md) lists what changed in each release.

## What you get

- Eight layouts: Editorial (large lead images with an excerpt), Cards (a grid of photo cards), Gazette (a newspaper-style
  front page), Inbox (email-style rows), Compact (a plain list without pictures), Headlines (one line per title),
  Digest (a few articles from each folder or feed) and Coffee break (unread articles grouped by how long they take).
  Set it per device, per folder or per feed.
- 20 color themes, with separate picks for day and night, and 11 bundled reading fonts.
- Nested folders for organizing feeds.
- Import feeds from your old reader and export them any time (OPML).
- Search, saved searches, and keyword filters that mute, star, highlight or mark articles read.
- Choose how many articles each feed keeps, from 50 to unlimited. Starred articles are never deleted.
- Runs as a phone app, installed from your browser (needs HTTPS; see the [reverse proxy guide](docs/reverse-proxy.md)). Articles you have opened still read offline.
- Syncs with apps that speak the Google Reader API, on your phone or computer.
- Reading statistics and a yearly Wrapped summary, stored only in your own database.
- Backup and restore are built in: export a backup from Settings, or copy the nightly snapshot off the machine. Keep backups private; they contain credentials (password hashes, the account secret and feed logins).
- One account per instance.

## Install

Replace `<version>` with the exact tag of the newest version on the [releases page](https://github.com/WPTK/Kipple/releases), for example `1.0.0`. The `latest` tag follows the newest stable release; pin an exact version if you want upgrades to be your choice.

```
docker run -d --name kipple --restart unless-stopped -p 127.0.0.1:1919:1919 -v kipple_data:/data ghcr.io/wptk/kipple:<version>
```

This one-line command is the short form: it does not run the container read-only, drop capabilities or cap memory.
The hardened form, and the compose files that include it, are in [the deploy guide](docs/deploy.md).

Open <http://127.0.0.1:1919>. The first screen creates your account, or restores a backup from another Kipple. One more step follows the account:
a few recommended feeds, none ticked until you tick them, and a link to import a feeds file from your old reader.
Your time zone is taken from your browser; theme, address and app password are in Settings.

Kipple runs on another machine? Open an SSH tunnel to it from the computer you browse on, and leave it open:

```
ssh -L 1919:127.0.0.1:1919 you@your-server
```

Then open <http://127.0.0.1:1919> on that computer.

Until you create the account, anyone who can reach the port can. The command above publishes the port on `127.0.0.1`
only, so only this machine can. Widen it after you have an account.

To reach Kipple from other devices, put it behind HTTPS (see the [reverse proxy guide](docs/reverse-proxy.md)) or on a
private network such as Tailscale. Do not publish it on the open internet over plain http. For a safe setup: publish on
`127.0.0.1`, put HTTPS in front, set a password and keep backups private (see [threat-model.md](docs/threat-model.md)).

The [deploy guide](docs/deploy.md) starts with a quick start for the compose file. It also covers reaching Kipple from
your phone or over HTTPS, backups,
upgrades, verifying the image signature and building from source.

## Sync with other apps

Kipple can sync read and starred articles with RSS apps on your phone or computer. In the app, add a Google Reader
compatible account with:

- Server: your Kipple address plus `/api/greader.php`
- User name: the one you chose during setup
- Password: the app password, a second password used only by sync apps, so your web password never leaves the browser.
  Make one in the last setup step or under Settings, Account & Devices, Generate new app password. Kipple shows it once.

## What Kipple leaves out

Kipple leaves these out on purpose.

- Native support for podcasts, audio and video. A media link in an article still opens.
- Webhooks and other integrations with outside services.
- Tags. Feeds live in folders, and articles can be starred.
- AI features.
- Notifications.
- Social features. You can still share an article from the web app: it opens your device's share sheet, or copies the link.
- Monitoring or analytics.\*
- More than one user.

\* _Kipple keeps reading statistics, but they live in your own database, only you see them, and nothing sends them anywhere._

## Documentation

- [Deploy guide](docs/deploy.md): quick start, install options, ports, backups, restore, upgrades, Cloudflare Access.
- [Reverse proxy](docs/reverse-proxy.md): HTTPS with Caddy, nginx or Traefik.
- [Troubleshooting](docs/troubleshooting.md)
- [Threat model](docs/threat-model.md): what Kipple protects and how to run it safely.
- [Compatibility](docs/compatibility.md): what stays the same across 1.x, and the Reader API in detail.
- [Design](docs/design.md): how Kipple works inside.
- [Performance](docs/performance.md): 500 feeds and 150,000 articles, measured.
- [All docs](docs/README.md)

To report a bug, open an [issue](https://github.com/WPTK/Kipple/issues). To send a change, read
[CONTRIBUTING.md](CONTRIBUTING.md). Vulnerabilities go through [SECURITY.md](SECURITY.md).

## Maintenance and governance

Kipple is vibe coded. AI coding agents (Claude) write nearly all of its code, tests and docs. The maintainer is not a
professional programmer; the work is specifying, steering and verifying the agents.

What backs it up is checking, not authorship: test suites, CI scans (govulncheck, staticcheck, gosec, gitleaks, Trivy,
zizmor), fuzzing, signed and attested releases with an SBOM, a written [compatibility promise](docs/compatibility.md) and
a release checklist. [Verify a release yourself](docs/deploy.md#verify-the-signature).

There is one maintainer with one person's time, and Kipple is single-user by design. [SECURITY.md](SECURITY.md) sets a
14-day target for a first response. If the maintainer stops, the code is under a permissive license and a fork is welcome:
the repository's workflow builds and signs each release, so a fork builds its own. Notes and decisions are on the
[maintainer's blog](https://blog.wptk.org) and in the [history repository](https://github.com/WPTK/kipple-history).

## License

[Blue Oak Model License 1.0.0](LICENSE): use, modify and share it, including commercially. Third-party licenses are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
