## Summary

<!-- What changed and why. -->

## Checklist

- [ ] `CHANGELOG.md` updated under `[Unreleased]` (behavior changes)
- [ ] Tests added or updated; `gofmt -l .` is empty, `go vet ./...` and `go test ./...` pass; for web changes `npm run lint && npm test && npm run build && npm run contrast` (in `web/`)
- [ ] `/code-review high` run before deploy
- [ ] Deploy to Host-A needed?
- [ ] Tag needed? (annotated `vX.Y.Z[-pre.N]` on the deployed commit, at deploy time)
