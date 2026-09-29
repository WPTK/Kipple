## Summary

<!-- What changed and why. -->

## Checklist

- [ ] Behavior change? Added a `changes/<slug>.<kind>.md` fragment (see `changes/README.md`; don't edit `CHANGELOG.md`)
- [ ] Tests added or updated; `gofmt -l .` is empty, `go vet ./...` and `go test ./...` pass; for web changes `npm run lint && npm test && npm run build && npm run contrast` (in `web/`)
- [ ] `/code-review high` run before deploy
- [ ] Deploy to Host-A needed?
- [ ] Tag needed? (annotated `vX.Y.Z[-pre.N]` on the deployed commit, at deploy time)
