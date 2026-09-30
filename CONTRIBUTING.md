# Contributing to Kipple

Kipple is a single-user RSS reader with one maintainer. Bug reports, fixes and small improvements are welcome;
large features are not, because the scope is deliberate (see the non-goals in [README.md](README.md) and the
decisions in [CLAUDE.md](CLAUDE.md)). Nothing here promises a response time: issues and pull requests are
triaged best-effort (see [SECURITY.md](SECURITY.md)).

## Ways to help

- **Report a bug or ask a question:** open an [issue](https://github.com/WPTK/Kipple/issues). Say which version
  you run (`docker exec kipple /kipple version -v`, or Settings > About > Copy debug info), what you did, what
  you expected and what happened.
- **Report a security problem:** not as a public issue. Follow [SECURITY.md](SECURITY.md) (GitHub private
  vulnerability reporting).
- **Propose a change:** open an issue first for anything bigger than a fix, so you don't spend time on
  something that is out of scope. What was decided against is listed in [CLAUDE.md](CLAUDE.md) ("Decisions").
- **Send a pull request:** small and focused, against `main`, as described below.

## Getting set up

- Go (see `go.mod` for the version) and Node (see `web/package.json`). You do not need Docker to develop.
- Sample data: `cd web && npm run seed` starts Kipple on `127.0.0.1:7080` with sample feeds in a temporary
  folder. `web/README.md` has the rest.
- Checks: `go test ./...`, and in `web/`: `npm ci && npm test && npm run build`.
- `pwsh scripts/ci-local.ps1` runs what CI runs (formatting, vet, staticcheck, gosec, govulncheck, tests,
  lint, contrast check, `npm audit`), except the container steps.

## Pull requests

1. **Branch from `main`** and keep the PR to one topic.
2. **Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)**
   (`fix(web): ...`, `feat(sched): ...`, `docs: ...`, `chore: ...`).
3. **Code style:** `gofmt` clean, `go vet` and staticcheck clean; ESLint and `tsc` clean for `web/`. CI checks
   all of it.
4. **Tests are required for new functionality and for bug fixes.** A fix comes with a test that fails without
   it; a feature comes with tests for its behaviour and its error paths. Fuzz targets exist for parsers and
   validators (`scripts/fuzz.ps1`); add one when you add a parser.
5. **Changelog:** a behaviour change adds one small file under `changes/` (see [changes/README.md](changes/README.md)).
   Do not edit `CHANGELOG.md` directly.
6. **No secrets, hostnames, addresses or personal names** in code, docs or test data. Use `example.com`-style
   names.
7. **CI must be green** on the exact commit: the `go`, `web`, `security` and `docker` checks are required before
   anything merges to `main`, which only accepts changes through a pull request.
8. The maintainer merges. Releases are tagged by the maintainer only ([docs/RELEASING.md](docs/RELEASING.md)).

## Design and quality notes

- [docs/design.md](docs/design.md) is the source of truth for how Kipple works; read the relevant section
  before changing behaviour. Passwords are stored with argon2id and there is no custom cryptography; keep it
  that way.
- [docs/sqa-plan.md](docs/sqa-plan.md) and [docs/uat-plan.md](docs/uat-plan.md) describe how changes are tested
  and accepted.

## License

By contributing you agree that your contribution is licensed under the project's license, the
[Blue Oak Model License 1.0.0](LICENSE).
