#requires -Version 7.2
<#
.SYNOPSIS
  Reports whether a pull request is ready to merge, and with -Merge squash-merges it safely.
.DESCRIPTION
  Run it when you think a PR is done. It reads the PR from GitHub and prints one verdict, READY or
  BLOCKED because <reasons>. It checks:
    - the PR is open and not a draft;
    - every check passed: it names failing and pending checks;
    - origin's base branch is an ancestor of the PR head (the PR has the latest base merged in), asked of
      GitHub's compare API so no local fetch is needed and FETCH_HEAD is never touched;
    - GitHub's mergeStateStatus is CLEAN (or HAS_HOOKS);
    - no review is requesting changes or still required.
  With -Merge and a READY verdict it runs
  `gh pr merge <N> --squash --match-head-commit <full head sha>`, so a push that lands after the verdict makes
  GitHub refuse the merge. It then verifies the PR is MERGED and that every issue the PR closes (`Closes #n`)
  is CLOSED, waiting a short while for GitHub to close them.

  What it can change: only -Merge changes anything (it merges one PR; it never deletes the branch: use
  scripts/branch-cleanup.ps1). Without -Merge it is read-only. -WhatIf with -Merge prints the merge command.
.PARAMETER Number
  The pull request number.
.PARAMETER Merge
  Squash-merge when the verdict is READY.
.PARAMETER Repo
  owner/name. Default: the repository `gh repo view` reports.
.EXAMPLE
  pwsh scripts/pr-ready.ps1 -Number 276
.EXAMPLE
  pwsh scripts/pr-ready.ps1 -Number 280 -Merge -WhatIf
.NOTES
  Exit codes: 0 READY (and merged and verified, with -Merge), 1 BLOCKED or the post-merge verification failed,
  2 usage or environment error.
  If this breaks: it depends on `gh pr view --json` fields number,state,isDraft,headRefOid,baseRefName,
  mergeStateStatus,reviewDecision,statusCheckRollup,closingIssuesReferences (Get-PullRequestState), on the compare
  API's `status` field (Get-AncestorStatus), and on `gh pr merge --match-head-commit`. The decision logic is
  Get-PrVerdict. Tests: Invoke-Pester scripts/pr-ready.Tests.ps1
#>
[CmdletBinding(SupportsShouldProcess)]
param(
  [Parameter(Mandatory)][ValidateRange(1, 1000000)][int]$Number,
  [switch]$Merge,
  [ValidatePattern('^[\w.-]+/[\w.-]+$')][string]$Repo
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lib/Kipple.Tools.ps1')

$script:FailingConclusions = @('FAILURE', 'CANCELLED', 'TIMED_OUT', 'ACTION_REQUIRED', 'STARTUP_FAILURE', 'ERROR')
$script:PendingStates = @('PENDING', 'EXPECTED')

function Get-CheckSummary {
  <#
  .SYNOPSIS
    Sorts a statusCheckRollup into failing and pending check names.
  .DESCRIPTION
    Handles both shapes GitHub returns: check runs (name, status, conclusion) and commit statuses (context, state).
  .OUTPUTS
    PSCustomObject with Failing and Pending (string arrays) and Total.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([AllowEmptyCollection()][object[]]$Rollup = @())
  $failing = [System.Collections.Generic.List[string]]::new()
  $pending = [System.Collections.Generic.List[string]]::new()
  foreach ($c in $Rollup) {
    $props = $c.PSObject.Properties.Name
    if ($props -contains 'context') {
      $name = $c.context
      if ($c.state -in $script:FailingConclusions) { $failing.Add($name) }
      elseif ($c.state -in $script:PendingStates) { $pending.Add($name) }
    } else {
      $name = $c.name
      if ($c.status -ne 'COMPLETED') { $pending.Add($name) }
      elseif ($c.conclusion -in $script:FailingConclusions) { $failing.Add($name) }
    }
  }
  return [pscustomobject]@{ Failing = $failing.ToArray(); Pending = $pending.ToArray(); Total = @($Rollup).Count }
}

function Get-PrVerdict {
  <#
  .SYNOPSIS
    Decides READY or BLOCKED from a PR record and the compare status of base...head.
  .PARAMETER Pr
    Object with number, state, isDraft, headRefOid, mergeStateStatus, reviewDecision, statusCheckRollup.
  .PARAMETER AncestorStatus
    The compare API status of base...head: ahead or identical mean the base is an ancestor of the head.
  .OUTPUTS
    PSCustomObject with Number, Ready, HeadSha, Reasons (string array) and Text (the one-line verdict).
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][pscustomobject]$Pr, [Parameter(Mandatory)][string]$AncestorStatus)
  $reasons = [System.Collections.Generic.List[string]]::new()
  if ($Pr.state -ne 'OPEN') { $reasons.Add("the PR is $($Pr.state), not open") }
  if ($Pr.isDraft) { $reasons.Add('it is a draft') }
  $checks = Get-CheckSummary -Rollup @($Pr.statusCheckRollup)
  if ($checks.Failing.Count) { $reasons.Add("failing checks: $($checks.Failing -join ', ')") }
  if ($checks.Pending.Count) { $reasons.Add("pending checks: $($checks.Pending -join ', ')") }
  if ($checks.Total -eq 0) { $reasons.Add('no checks have reported') }
  if ($AncestorStatus -notin @('ahead', 'identical')) { $reasons.Add("the base branch is not merged into the head (compare status: $AncestorStatus)") }
  if ($Pr.mergeStateStatus -notin @('CLEAN', 'HAS_HOOKS')) { $reasons.Add("merge state is $($Pr.mergeStateStatus)") }
  if ($Pr.reviewDecision -eq 'CHANGES_REQUESTED') { $reasons.Add('a review requests changes') }
  if ($Pr.reviewDecision -eq 'REVIEW_REQUIRED') { $reasons.Add('a review is required') }
  $ready = ($reasons.Count -eq 0)
  return [pscustomobject]@{
    Number  = $Pr.number
    Ready   = $ready
    HeadSha = $Pr.headRefOid
    Reasons = $reasons.ToArray()
    Text    = $(if ($ready) { "READY (#$($Pr.number) at $($Pr.headRefOid))" } else { "BLOCKED because $($reasons -join '; ')" })
  }
}

function Get-PullRequestState {
  <#
  .SYNOPSIS
    Reads one pull request from GitHub.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][int]$Number)
  $fields = 'number,state,isDraft,headRefOid,baseRefName,mergeStateStatus,reviewDecision,statusCheckRollup,closingIssuesReferences'
  $r = Invoke-Native -FilePath gh -Arguments 'pr', 'view', "$Number", '-R', $Repo, '--json', $fields `
    -Step "read PR #$Number" -Fix 'gh auth status; check the number and the repository'
  $pr = ($r.Output -join "`n") | ConvertFrom-Json
  if ($pr.headRefOid -notmatch '^[0-9a-f]{40}$') { throw "Step 'read PR #$Number' failed: head sha '$($pr.headRefOid)' is not a full sha. Likely fix: gh pr view $Number --json headRefOid." }
  return $pr
}

function Get-AncestorStatus {
  <#
  .SYNOPSIS
    Asks GitHub how the head compares with the base branch: ahead, identical, behind or diverged.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Base, [Parameter(Mandatory)][string]$HeadSha)
  $r = Invoke-Native -FilePath gh -Arguments 'api', "repos/$Repo/compare/${Base}...${HeadSha}", '--jq', '.status' `
    -Step 'compare the head with the base' -Fix 'gh auth status; the head commit must exist on GitHub'
  return $r.Output[0].Trim()
}

function Get-IssueRepo {
  <#
  .SYNOPSIS
    The owner/name an issue reference lives in; closing references can point at another repository.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][pscustomobject]$Issue, [Parameter(Mandatory)][string]$DefaultRepo)
  $repo = $Issue.PSObject.Properties['repository']
  if ($repo -and $repo.Value -and $repo.Value.PSObject.Properties['owner'] -and $repo.Value.owner -and $repo.Value.PSObject.Properties['name']) {
    return "$($repo.Value.owner.login)/$($repo.Value.name)"
  }
  return $DefaultRepo
}

function Test-IssueClosed {
  <#
  .SYNOPSIS
    Waits up to the timeout for an issue to be CLOSED; returns whether it is.
  #>
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][int]$IssueNumber, [int]$TimeoutSeconds = 60)
  $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
  while ($true) {
    $r = Invoke-Native -FilePath gh -Arguments 'issue', 'view', "$IssueNumber", '-R', $Repo, '--json', 'state', '--jq', '.state' -Step "read issue #$IssueNumber" -Fix 'gh auth status'
    if ($r.Output[0].Trim() -eq 'CLOSED') { return $true }
    if ([DateTime]::UtcNow -ge $deadline) { return $false }
    Start-Sleep -Seconds 5
  }
}

function Invoke-PrReady {
  <#
  .SYNOPSIS
    The body of the script. Emits the verdict object, then the exit code (0 or 1).
  #>
  [CmdletBinding(SupportsShouldProcess)]
  param([int]$Number, [bool]$DoMerge, [string]$Repo)
  if (-not $Repo) { $Repo = Get-RepoSlug -RepoRoot (Get-RepoRoot -Path $PSScriptRoot) }
  $pr = Get-PullRequestState -Repo $Repo -Number $Number
  $ancestor = Get-AncestorStatus -Repo $Repo -Base $pr.baseRefName -HeadSha $pr.headRefOid
  $verdict = Get-PrVerdict -Pr $pr -AncestorStatus $ancestor
  Write-KippleInfo $verdict.Text
  $verdict
  if (-not $verdict.Ready) { return 1 }
  if (-not $DoMerge) { return 0 }

  $mergeArgs = @('pr', 'merge', "$Number", '-R', $Repo, '--squash', '--match-head-commit', $verdict.HeadSha)
  if (-not $PSCmdlet.ShouldProcess("$Repo#$Number", 'squash merge')) {
    Write-KippleInfo ('WhatIf: would run: ' + (Format-CommandLine -FilePath gh -Arguments $mergeArgs))
    return 0
  }
  $null = Invoke-Native -FilePath gh -Arguments $mergeArgs -Step "merge PR #$Number" `
    -Fix 'a push after the verdict changes the head (run this script again); or a required check or review is missing'

  $after = Get-PullRequestState -Repo $Repo -Number $Number
  if ($after.state -ne 'MERGED') {
    Write-KippleInfo "Verification failed: PR #$Number is $($after.state), not MERGED."
    return 1
  }
  Write-KippleInfo "Verified: PR #$Number is MERGED."
  $unclosed = @()
  foreach ($issue in @($pr.closingIssuesReferences)) {
    $issueRepo = Get-IssueRepo -Issue $issue -DefaultRepo $Repo
    # The merge has happened by now: a failed lookup is reported per issue and must not look like a failed merge.
    try {
      if (Test-IssueClosed -Repo $issueRepo -IssueNumber $issue.number) { Write-KippleInfo "Verified: issue $issueRepo#$($issue.number) is CLOSED." }
      else { $unclosed += "$issueRepo#$($issue.number)" }
    } catch {
      Write-KippleInfo "Could not check issue $issueRepo#$($issue.number) (the PR is merged): $($_.Exception.Message)"
      $unclosed += "$issueRepo#$($issue.number)"
    }
  }
  if ($unclosed.Count) {
    Write-KippleInfo "Verification failed: issue(s) $($unclosed -join ', ') not confirmed closed after the merge (the PR itself is MERGED). Close them by hand if the PR text does not close them."
    return 1
  }
  return 0
}

if ($MyInvocation.InvocationName -ne '.') {
  $boundArgs = @{ Number = $Number; DoMerge = [bool]$Merge; Repo = $Repo; WhatIf = $WhatIfPreference }
  Invoke-ToolMain -Name 'pr-ready' -Body { Invoke-PrReady @boundArgs }
}
