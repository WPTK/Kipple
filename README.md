# Kipple

A self-hosted RSS reader for one person. It fetches feeds, keeps them in a small SQLite database, and serves
a web app plus a Google Reader-compatible sync API, so apps like Reeder Classic and NetNewsWire can sync
with it. It builds to one container with one port.

**Status:** prerelease (alpha), developed in phases. The fetch and sync core (phase 1), the reading UI (phase 2),
the installable app with offline reading (phase 3, `v0.3.0-alpha.2`) and reading statistics with the yearly Wrapped
summary (phase 4, `v0.3.0-alpha.7`) are done and run in daily use. Phase 5, release readiness, is under way: a full
code audit, optional Cloudflare Access sign-in (the web password can then be removed) and a scheduled day/night
theme are merged for the next release, and a beta follows once testing is done (see
[docs/RELEASING.md](docs/RELEASING.md)). See [CHANGELOG.md](CHANGELOG.md). Design notes may link to planning and
research documents that are not part of this repository.

## Configuration

Kipple is configured with environment variables; [.env.example](.env.example) documents every one, and
[docker-compose.example.yml](docker-compose.example.yml) shows a hardened container setup. Everything else is set in
the app's Settings screen.

## Where things are

- `cmd/kipple/` and `internal/` are the Go server (fetching, storage, the sync API, the web API).
- `web/` is the React app, built into the Go binary.
- `docs/design.md` is the source of truth for how it works;
  `docs/ui-decisions.md` records the design decisions; `docs/deploy.md` covers backups, recovery and deploys.
  See [docs/README.md](docs/README.md) for a full index of everything under `docs/`.
- `CLAUDE.md` holds the project rules used when working on the code with Claude.

## Build and test

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
