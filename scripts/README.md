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
| `branch-cleanup.ps1` | Deletes branches and agent worktrees whose pull request is merged | Local branches and worktrees; with `-IncludeRemote` also branches on origin. Asks for a typed yes | `-WhatIf` (prints the plan only) |
| `pr-ready.ps1` | Prints READY or BLOCKED because ... for a PR; with `-Merge` squash-merges a READY one and verifies | Read-only, except `-Merge` merges that one PR (never deletes the branch) | `-WhatIf` with `-Merge` prints the merge command |

### release-gates.ps1

    pwsh scripts/release-gates.ps1 [-Ref <full sha>] [-SkipFuzz] [-Only go,fuzz,web,changelog,node] [-KeepWorktree] [-WhatIf]

Prints the full sha it tests, runs `go test` twice, fuzz, the web tests, the changelog check and the Node script tests,
and prints a pass/fail table. A Go package that fails in the full run but passes alone is shown as FLAKY (both
results are in the table) and does not fail the run. Every step refuses to start while another heavy step holds the
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

### branch-cleanup.ps1

    pwsh scripts/branch-cleanup.ps1 [-IncludeRemote] [-WhatIf]

Asks GitHub (`gh pr list --state merged` and `--state open`) which branches are merged. It touches a branch only when a
merged PR used it as its head and the branch tip equals that PR's final head commit, no open PR uses it, it is not
`main`, it is not checked out here and its worktree under `.claude/worktrees` has no uncommitted changes. It prints
exactly what it will do (worktree removals, then `git branch -D`, then with `-IncludeRemote` `git push origin
--delete`) and, without `-WhatIf`, waits for you to type `yes`. Run it yourself; an agent should only use `-WhatIf`.
It runs `git fetch --prune origin` first (this updates remote-tracking refs only).

If this breaks: it depends on `gh pr list --json headRefName,headRefOid,mergedAt`, `git for-each-ref` and `git worktree
list --porcelain` output. The decision logic is `Get-CleanupPlan`; every reason a branch is kept is a line in its tests.

### pr-ready.ps1

    pwsh scripts/pr-ready.ps1 -Number N [-Merge] [-Repo owner/name] [-WhatIf]

One verdict: `READY` or `BLOCKED because ...` (open, not a draft, no failing or pending checks and at least one
reported, base branch merged into the head, merge state CLEAN, no review requesting changes). The base-in-head check asks
GitHub's compare API, so no local fetch happens and `FETCH_HEAD` is untouched. With `-Merge` and READY it runs `gh pr merge N
--squash --match-head-commit <full head sha>` (a push after the verdict makes GitHub refuse), then checks the PR is
MERGED and every `Closes #n` issue is CLOSED.

If this breaks: it depends on the `gh pr view --json` fields listed in the script help, the compare API's `status` field
and `gh pr merge --match-head-commit`. The decision logic is `Get-PrVerdict`.

## Node tools

`changelog.mjs`, `audit-report.mjs`, `check-links.mjs` and `uat-labels.mjs` each have a `*.test.mjs` beside them
(`node --test scripts/<name>.test.mjs`). `uat-labels.mjs [<git range>]` is read-only: it fails (exit 1) when an
`aria-label` removed or changed in `web/src` is still used by `web/uat/*.mjs` (see docs/uat-plan.md). If it breaks: it
reads `git diff -U0 <range> -- web/src` and only sees labels written on the same line as the attribute
(`extractFragments`).

## CI

The label guard (`uat-labels`) runs on every pull request (a 5 second text check, so it has no gate). The `tooling`
job (Windows, Pester 5.7.1 and PSScriptAnalyzer 1.25.0, pinned) runs on pull requests only, and only when the
`changes` job reports that `scripts/` or `.github/workflows/ci.yml` changed (`scripts/ci-changes.sh --scripts`,
run from the base commit like the prose filter; it skips only on an explicit `false`, so a failed gate runs it).
Both are separate jobs at the end of `.github/workflows/ci.yml`.

## Tests for the tools

    pwsh -NoProfile -Command "Invoke-Pester scripts/lib/Kipple.Tools.Tests.ps1, scripts/release-gates.Tests.ps1, scripts/release-publish.Tests.ps1, scripts/branch-cleanup.Tests.ps1, scripts/pr-ready.Tests.ps1"

Pester 5.5 or later (`Install-PSResource Pester -Scope CurrentUser`). Native commands are mocked: no network, no push.
Lint: `pwsh scripts/ci-local.ps1 -Lint` (PSScriptAnalyzer with `scripts/PSScriptAnalyzerSettings.psd1`; findings of
severity Warning or Error fail it; fix them rather than suppress them).
