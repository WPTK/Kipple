# scripts/

Release and maintenance tooling. PowerShell 7 (`pwsh`) and Node 22 or later. Nothing here is needed to run Kipple.

## The PowerShell tools

All of them share `scripts/lib/Kipple.Tools.ps1`: small functions with comment-based help, one place that runs native
commands (`Invoke-Native`) so every failure names the step, the exact command and the likely fix, and one place that
maps the outcome to an exit code (`Invoke-ToolMain`). Add `-Verbose` to any of them to see each step and each command
line. Messages go to the information stream; result rows are returned as objects.

Exit codes: `0` ok, `1` a gate or check failed, `2` usage or environment error (a tool is missing, a command failed,
bad input). PowerShell itself exits `1` when it rejects a parameter before the script starts (a malformed sha or tag).

| Script | What it does | Can change | Dry run |
|---|---|---|---|
| `release-gates.ps1` | Runs the release test gates one at a time on a detached worktree of an exact commit | A temp worktree and logs folder, removed at the end. Nothing in your checkout, nothing remote | `-WhatIf` |
| `release-publish.ps1` | Verifies the image and SBOM signatures of a pushed tag, then creates the GitHub Release | One new GitHub Release (the only change); a temp folder | `-WhatIf` (verifies, prints the create command) |
### release-gates.ps1

    pwsh scripts/release-gates.ps1 [-Ref <full sha>] [-SkipFuzz] [-Only go,fuzz,web,changelog,node] [-KeepWorktree] [-WhatIf]

Prints the full sha it tests, runs `go test` twice, fuzz, the web tests, the changelog check and the Node script tests,
and prints a pass/fail table. A Go package that fails in the full run but passes alone is shown as FLAKY (both
results are in the table) and does not fail the run. The web step refuses to start while another heavy step holds the
lock.

If this breaks: it parses `go test` output (`FAIL <package>` and `--- FAIL: <test>` lines, `Get-GoFailure` in the
library), runs `npm ci` and `npm test` in `web/`, `node scripts/changelog.mjs check` and `scripts/fuzz.ps1`. The step
list is `Get-GatePlan`.

### release-publish.ps1

    pwsh scripts/release-publish.ps1 -Tag vX.Y.Z[-beta.N] (-Prerelease | -Full) [-Repo owner/name] [-WhatIf]

Run after the tag is pushed (docs/RELEASING.md, step 11). It waits for the tag's Release workflow run, downloads the
`image-notes` artifact, runs `cosign verify` and `cosign verify-blob` with the exact identity of that tag's run, builds
the notes (`changelog.mjs notes` plus `image-notes.md`) and runs `gh release create` with the SBOM and its bundle
attached. A failed verification stops it before anything is created (exit 1). Exactly one of `-Prerelease` and `-Full`
is required. It never creates the tag.

If this breaks: it depends on the workflow file name `release.yml`, the artifact name `image-notes` and the three file
names in it (`image-notes.md`, `kipple-<version>.sbom.json`, `kipple-<version>.sbom.json.sigstore.json`; see "Release
notes block" in `.github/workflows/release.yml`), the output of `node scripts/changelog.mjs notes`, cosign 3 or later
and `gh run list/download`. Each has one function in the script.

## Tests for the tools

    pwsh -NoProfile -Command "Invoke-Pester scripts/lib/Kipple.Tools.Tests.ps1, scripts/release-gates.Tests.ps1, scripts/release-publish.Tests.ps1"

Pester 5.5 or later (`Install-PSResource Pester -Scope CurrentUser`). Native commands are mocked: no network, no push.
Lint: `pwsh scripts/ci-local.ps1 -Lint` (PSScriptAnalyzer with `scripts/PSScriptAnalyzerSettings.psd1`; findings of
severity Warning or Error fail it; fix them rather than suppress them).


