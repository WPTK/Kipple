# Handover: picking Kipple up after a long gap

For whoever maintains Kipple after months away, or takes it over. It says what keeps running by itself, what stops
without a sound, and what only a person with the right account can do. The release steps are in
[RELEASING.md](RELEASING.md); this page is the order to look at things in and the traps.

## Resuming after months away

1. **Check that the schedules are still on.** Open the repository's Actions tab. Every workflow in the table below
   should have run recently. If the weekly ones show nothing, read "The 60-day rule" before anything else.
2. **Read the audit issue.** The weekly audit keeps one tracking issue: open while a dependency or base image has a
   known advisory, closed by the first clean run. An open one is the first thing to fix.
3. **Look at the Dependabot pull requests.** Version updates wait seven days after a release, so they are safe to read
   and merge one at a time once CI is green. A major bump of Node or the Go toolchain is not proposed by Dependabot;
   check the pinned versions in `Dockerfile` and the workflows against their end-of-life dates by hand.
4. **Check `main` is green** on its latest commit, then run `pwsh scripts/maintainers/release-gates.ps1` on it. If
   nothing in the code has changed since the last release, there is nothing to release.
5. **Read the open issues and `CHANGELOG.md`** before touching code. `CLAUDE.md` holds the decisions that are not
   reopened.

## What runs by itself

| What | When | Where | If it stops |
|---|---|---|---|
| Weekly audit (dependencies and base image advisories) | Mondays | `.github/workflows/audit.yml` | Advisories go unseen until the next push runs CI |
| Fuzzing | Mondays | `fuzz.yml` | Parsers are not fuzzed between releases |
| Link check | Mondays | `links.yml` | Dead links in the docs go unseen |
| OpenSSF Scorecard | Mondays | `scorecard.yml` | The score stops updating |
| Browser UAT | Daily | `browser-uat.yml` | Browser regressions show only on the next pull request |
| Cache cleanup | Daily | `cache-cleanup.yml` | Actions caches fill the quota and CI slows |
| Dependency update pull requests | Weekly | `.github/dependabot.yml` | Updates stop arriving |
| Nightly database snapshot and maintenance (04:10 in the server's time zone) | Nightly | inside Kipple | The health view shows an old snapshot time |

Nothing updates a running Kipple. A person pulls a new image on the server.

## The 60-day rule

GitHub turns off scheduled workflows in a public repository after 60 days without repository activity. A quiet
repository therefore loses every schedule above at once, with no notice, and the audit that is meant to catch
problems is the first thing to go.

- **Spot it:** a scheduled workflow whose last run is older than a week or two, or a notice on the workflow's page in
  the Actions tab saying it was disabled for inactivity.
- **Recover:** open the workflow in the Actions tab, choose **Enable workflow**, then **Run workflow** on the audit to
  get a fresh result. Do the same for each scheduled workflow. A commit to the default branch restarts the clock.
- **Do not** add a third-party "keep alive" action to avoid this: it adds a dependency that runs with repository
  access. Dependabot pull requests are not a reliable substitute for activity.

## Accounts

The roles, not the names. Each needs a person who can sign in.

- **The GitHub account that owns the repository.** Settings, branch rules, secrets, Dependabot and the Actions
  schedules all live there. A second administrator on the repository and on the container package is the only
  protection against losing access; check both after any change of maintainer.
- **The container registry package** (`ghcr.io/wptk/kipple`). Images are pushed by the release workflow with the
  repository's own token; the package must stay linked to the repository.
- **Codecov**, for the coverage badge and report: its token is a repository secret.
- **Cloudflare**, only if Kipple is served through a tunnel or Access: the tunnel token and the Access application
  belong to that account.
- **The server that runs Kipple**: a person with shell access, Docker, and the compose file.

## Restore drill

A backup that has never been restored is a guess. The steps are in [restore.md](../restore.md) and the drill is a
release step in [RELEASING.md](RELEASING.md). Record each drill here with its date and what was restored.

Last restore drill: not recorded here yet.

## The weekly check

The weekly audit (`audit.yml`) is where a quiet Kipple shows it needs attention: the tracking issue it keeps is the
one thing to look at each Monday.

## Routine repository checks

Three checks keep the repository tidy. Run them now and then, after merges:

```bash
gh issue list --state open --search "no:label"
```

```bash
gh api "repos/WPTK/Kipple/milestones?state=open&per_page=100" --jq '.[] | select(.open_issues == 0) | .title'
```

```bash
pwsh scripts/maintainers/branch-cleanup.ps1 -WhatIf
```

The first prints open issues with no label, the second open milestones with nothing left in them, and the third which
merged branches and worktrees are still around (it deletes nothing with `-WhatIf`).
