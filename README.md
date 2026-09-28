# Kipple

A self-hosted RSS reader, built for one reader: yours. No ads, no algorithm, no tracking, nobody else's
data mixed into it — just your feeds, kept in a small SQLite database on a server you control, read the way
you like to read. It builds to one Docker container with one port, and it talks the Google Reader sync API,
so apps like Reeder Classic and NetNewsWire can act as a second client on the same account.

**Status:** prerelease (beta). The fetch and sync core, the reading UI, an installable offline-capable app,
and reading statistics with a yearly Wrapped summary are all done and in daily use; testing before 1.0 is
under way (see [docs/RELEASING.md](docs/RELEASING.md)). See [CHANGELOG.md](CHANGELOG.md) for what has
shipped release by release.

## What it's like to use

- **Five ways to read a list**, per device or per feed: **Editorial** (a magazine layout with big lead
  images), **Cards** (a photo grid), **Compact** and **Email - Compact** (dense, text-first lists at two
  different densities), and **Inbox** (an email-style layout with sender, subject and snippet). Switch any
  time from the layout button in the list header.
- **Looks like it's yours.** 20 built-in color themes, from a warm Paper to a true-black Midnight, plus
  "Follow system" and an optional day/night schedule that switches themes on its own clock. 11 bundled
  reading fonts (serif and sans, including a dyslexia-friendly option) alongside your device's own system
  fonts, five text sizes, and five density presets from Dense to Airy.
- **Installs like a real app.** Add Kipple to your phone's home screen as a Progressive Web App: it opens
  full-screen with no browser chrome, keeps articles you've already read available offline, and updates
  itself in the background.
- **Full article text, when the feed only gives you a summary.** Kipple can fetch and extract the full
  article automatically, so short feeds still read like full ones.
- **Reading stats, including a yearly Wrapped.** See how much you actually read and which feeds you spend
  the most time on, with a shareable end-of-year summary when the year turns over.
- **A second client for free.** Because Kipple speaks the Google Reader sync API, Reeder Classic and
  NetNewsWire (and other apps that support that API) can sync against it too — read on the couch in one
  app, catch up at your desk in the web app, and read/starred state stays in sync everywhere.
- **Nothing leaves your server.** No account anywhere else, no analytics, no social features (no other
  people's data, no comparisons), no AI. It's a reading list, not a platform.

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
username `owner`). That's the whole happy path — one image, one container, one port, no database to set up
separately.

To sync with Reeder Classic, NetNewsWire, or another Google Reader-API client, run
`docker exec -it kipple /kipple api-password` once Kipple is running, to generate a Reader API password.

**Put it on your phone.** Open Kipple's address in Safari (iPhone/iPad) or Chrome (Android), then use
"Add to Home Screen" (Safari's share sheet) or "Install app" (Chrome's menu). It launches full-screen from
your home screen from then on, like any other app, and keeps already-read articles available without a
connection.

For anything past this — backups, restoring, running behind a reverse proxy or tunnel, optional Cloudflare
Access sign-in — see [docs/deploy.md](docs/deploy.md). It's written from the maintainer's own two-machine setup
(one box running Kipple, one for admin/backups over SSH) but says up front how that collapses to a single
machine, which is what most people running this will actually have.

## Configuration

Kipple is configured with environment variables; [.env.example](.env.example) documents every one, and
[docker-compose.example.yml](docker-compose.example.yml) shows a hardened container setup. Everything else —
themes, fonts, layouts, retention, sync behavior, and so on — is set in the app's own Settings screen, per
device or for the account, with no need to touch the container again.

## For developers

Everything above is all a self-hoster needs. This section is for changing Kipple itself.

- `cmd/kipple/` and `internal/` are the Go server (fetching, storage, the sync API, the web API).
- `web/` is the React app, built into the Go binary.
- `docs/design.md` is the source of truth for how it works;
  `docs/ui-decisions.md` records the design decisions; `docs/deploy.md` covers backups, recovery and deploys.
  See [docs/README.md](docs/README.md) for a full index of everything under `docs/`.
- `CLAUDE.md` holds the project rules used when working on the code with Claude.

**Build and test:**

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
