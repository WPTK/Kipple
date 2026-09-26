# Kipple

A self-hosted RSS reader for one person. It fetches feeds, keeps them in a small SQLite database, and serves
a web app plus a Google Reader-compatible sync API, so apps like Reeder Classic and NetNewsWire can sync
with it. It builds to one container with one port.

**Status:** work in progress, developed in phases. `v0.3.0-alpha.1` is the first public build: the fetch and sync
core (phase 1) and the reading UI (phase 2) are done and run in daily use; installable-app and offline support
(phase 3) is in progress on the `Unreleased` line of the changelog. See [CHANGELOG.md](CHANGELOG.md). Design notes may link to planning and research documents that
are not part of this repository.

## Where things are

- `cmd/kipple/` and `internal/` are the Go server (fetching, storage, the sync API, the web API).
- `web/` is the React app, built into the Go binary.
- `docs/design.md` is the source of truth for how it works;
  `docs/ui-decisions.md` records the design decisions; `docs/deploy.md` covers backups, recovery and deploys.
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
