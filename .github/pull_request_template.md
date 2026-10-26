## Summary

<!-- What changed and why. -->

Closes #N <!-- One keyword per issue ("Closes #1, closes #2"); "(#N)" in a title does not close anything. -->

## Checklist

- [ ] Behavior change? Added a `changes/<slug>.<kind>.md` fragment (see `changes/README.md`; don't edit `CHANGELOG.md`)
- [ ] Tests added or updated; `gofmt -l .` is empty, `go vet ./...` and `go test ./...` pass; for web changes `npm run lint && npm test && npm run build && npm run contrast` (in `web/`)
- [ ] `/code-review high` run before deploy
- [ ] Does this change need a server deploy?
- [ ] Tag needed? (annotated `vX.Y.Z[-pre.N]` on the deployed commit, at deploy time)
