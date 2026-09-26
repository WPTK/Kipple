# Kipple

A self-hosted RSS reader for one person. It fetches feeds, keeps them in a small SQLite database, and serves
a web app plus a Google Reader-compatible sync API, so apps like Reeder Classic and NetNewsWire can sync
with it. It builds to one container with one port.

**Status:** work in progress, developed in phases. Phase 1 (fetch, store, sync API) is deployed. Phase 2 (the
reading UI, search, filters, image cache, backups) is built on the `phase-2` branch and being reviewed.
Phase 3 (installable app, offline use) is next. See [CHANGELOG.md](CHANGELOG.md).

## Where things are

- `cmd/kipple/` and `internal/` are the Go server (fetching, storage, the sync API, the web API).
- `web/` is the React app, built into the Go binary.
- `docs/design.md` is the source of truth for how it works; `docs/plan.md` is the plan;
  `docs/ui-decisions.md` records the design decisions; `docs/deploy.md` covers backups, recovery and deploys.
- `docs/HF/` holds human feedback: the owner's testing notes and evidence captured from the live server.
- `CLAUDE.md` holds the project rules used when working on the code with Claude.

## Build and test

- Go tests: `go test ./...`
- Web app: `cd web && npm ci && npm test && npm run build`
- Image: `docker build -t kipple:dev .`
- Local development with sample feeds is described in `web/README.md`.

This file was added as a short orientation; a fuller README can follow when the project is ready to share.
