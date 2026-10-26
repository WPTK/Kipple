#requires -Version 7.2
<#
.SYNOPSIS
  Reports whether a pull request is ready to merge, and with -Merge squash-merges it safely.
.DESCRIPTION
  Run it when you think a PR is done. It reads the PR from GitHub and prints one verdict, READY or
  BLOCKED because <reasons>. It checks:
    - the PR is open and not a draft;
    - every check passed: it names failing and pending checks (except the advisory ones below, which are printed and
      never block);
    - origin's base branch is an ancestor of the PR head (the PR has the latest base merged in), asked of
      GitHub's compare API so no local fetch is needed and FETCH_HEAD is never touched;
    - GitHub's mergeStateStatus is CLEAN (or HAS_HOOKS);
    - no review is requesting changes or still required.
  With -Merge and a READY verdict it runs
  `gh pr merge <N> --squash --match-head-commit <full head sha>`, so a push that lands after the verdict makes
  GitHub refuse the merge. It then verifies the PR is MERGED and that every issue the PR closes (`Closes #n`)
  is CLOSED, waiting a short while for GitHub to close them.

  What it can change: only -Merge changes anything (it merges one PR; it never deletes the branch: use
  scripts/maintainers/branch-cleanup.ps1). Without -Merge it is read-only. -WhatIf with -Merge prints the merge command.
.PARAMETER Number
  The pull request number.
.PARAMETER Merge
  Squash-merge when the verdict is READY.
.PARAMETER FailureDetail
  For each failing check that is a GitHub Actions job, also print the job, the failed step and the last 30 log lines
  (read through `gh api`), so nobody has to pull a whole log. Read-only; it changes nothing about the verdict.
.PARAMETER Repo
  owner/name. Default: the repository `gh repo view` reports.
.EXAMPLE
  pwsh scripts/maintainers/pr-ready.ps1 -Number 276
.EXAMPLE
  pwsh scripts/maintainers/pr-ready.ps1 -Number 280 -Merge -WhatIf
.NOTES
  Exit codes: 0 READY (and merged and verified, with -Merge), 1 BLOCKED or the post-merge verification failed,
  2 usage or environment error.
  If this breaks: it depends on `gh pr view --json` fields number,state,isDraft,headRefOid,baseRefName,
  mergeStateStatus,reviewDecision,statusCheckRollup,closingIssuesReferences,files (Get-PullRequestState), on the compare
  API's `status` field (Get-AncestorStatus), and on `gh pr merge --match-head-commit`. The decision logic is
  Get-PrVerdict. Tests: Invoke-Pester scripts/maintainers/pr-ready.Tests.ps1
#>
[CmdletBinding(SupportsShouldProcess)]
param(
  [Parameter(Mandatory)][ValidateRange(1, 1000000)][int]$Number,
  [switch]$Merge,
  [switch]$FailureDetail,
  [ValidatePattern('^[\w.-]+/[\w.-]+$')][string]$Repo
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lib/Kipple.Tools.ps1')

$script:FailingConclusions = @('FAILURE', 'CANCELLED', 'TIMED_OUT', 'ACTION_REQUIRED', 'STARTUP_FAILURE', 'ERROR')
$script:PendingStates = @('PENDING', 'EXPECTED')
# Checks that report but never block: the slow real-browser run (.github/workflows/browser-uat.yml, not a required check
# yet). A name matches a check called exactly that or "<name> (<matrix leg>)". Take a name off this list when its
# check becomes required. A PR that changes the browser flows or the workflow itself is the exception: for it every
# leg blocks (Test-UatTouched), because the change is what the legs test.
$script:AdvisoryChecks = @('Browser UAT')
$script:UatPaths = @('^web/uat/', '^\.github/workflows/browser-uat\.yml$')

function Test-UatTouched {
  [CmdletBinding()]
  [OutputType([bool])]
  param([AllowEmptyCollection()][string[]]$Path = @())
  foreach ($f in $Path) {
    foreach ($re in $script:UatPaths) { if ($f -match $re) { return $true } }
  }
  return $false
}

function Test-AdvisoryCheck {
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][AllowEmptyString()][string]$Name)
  foreach ($a in $script:AdvisoryChecks) {
    if ($Name -eq $a -or $Name.StartsWith("$a (")) { return $true }
  }
  return $false
}

function Get-CheckSummary {
  <#
  .SYNOPSIS
    Sorts a statusCheckRollup into failing and pending check names.
  .DESCRIPTION
    Handles both shapes GitHub returns: check runs (name, status, conclusion) and commit statuses (context, state).
  .OUTPUTS
    PSCustomObject with Failing and Pending (string arrays), Advisory (strings like 'Browser UAT: failing' for the
    advisory checks that are not passing) and Total. Advisory checks are in neither Failing nor Pending.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([AllowEmptyCollection()][object[]]$Rollup = @(), [switch]$UatBlocking)
  $failing = [System.Collections.Generic.List[string]]::new()
  $pending = [System.Collections.Generic.List[string]]::new()
  $advisory = [System.Collections.Generic.List[string]]::new()
  foreach ($c in $Rollup) {
    $props = $c.PSObject.Properties.Name
    if ($props -contains 'context') {
      $name = $c.context
      if (-not $UatBlocking -and (Test-AdvisoryCheck -Name $name)) {
        if ($c.state -in $script:FailingConclusions) { $advisory.Add("${name}: failing") } elseif ($c.state -in $script:PendingStates) { $advisory.Add("${name}: pending") }
      }
      elseif ($c.state -in $script:FailingConclusions) { $failing.Add($name) }
      elseif ($c.state -in $script:PendingStates) { $pending.Add($name) }
    } else {
      $name = $c.name
      if (-not $UatBlocking -and (Test-AdvisoryCheck -Name $name)) {
        if ($c.status -ne 'COMPLETED') { $advisory.Add("${name}: pending") } elseif ($c.conclusion -in $script:FailingConclusions) { $advisory.Add("${name}: failing") }
      }
      elseif ($c.status -ne 'COMPLETED') { $pending.Add($name) }
      elseif ($c.conclusion -in $script:FailingConclusions) { $failing.Add($name) }
    }
  }
  return [pscustomobject]@{ Failing = $failing.ToArray(); Pending = $pending.ToArray(); Advisory = $advisory.ToArray(); Total = @($Rollup).Count }
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
  $files = @()
  if ($Pr.PSObject.Properties['files'] -and $Pr.files) { $files = @($Pr.files | ForEach-Object { $_.path }) }
  # `gh pr view --json files` lists at most 100 files; a PR that large may hide a web/uat change, so it fails closed.
  $uat = ($files.Count -ge 100) -or (Test-UatTouched -Path $files)
  $checks = Get-CheckSummary -Rollup @($Pr.statusCheckRollup) -UatBlocking:$uat
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
    Advisory = $checks.Advisory
    Text    = $(if ($ready) { "READY (#$($Pr.number) at $($Pr.headRefOid))$(if ($checks.Advisory.Count) { " (advisory, not blocking: $($checks.Advisory -join ', '))" })" } else { "BLOCKED because $($reasons -join '; ')" })
  }
}

function Get-JobIdFromUrl {
  <#
  .SYNOPSIS
    The Actions job id in a check's details URL (.../actions/runs/<run>/job/<job>), or $null for any other check.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([AllowNull()][AllowEmptyString()][string]$Url)
  if ($Url -match '/actions/runs/\d+/job/(\d+)') { return $Matches[1] }
  return $null
}

function Get-FailedStepName {
  <#
  .SYNOPSIS
    The name of the first failed step of an Actions job object (the `gh api repos/.../actions/jobs/<id>` shape), or
    $null when no step failed (a cancelled or timed-out job, or a failure before any step).
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][pscustomobject]$Job)
  $steps = $Job.PSObject.Properties['steps']
  if (-not $steps -or -not $steps.Value) { return $null }
  $bad = @($steps.Value | Where-Object { $_.conclusion -in @('failure', 'timed_out', 'cancelled') } | Select-Object -First 1)
  if ($bad.Count) { return $bad[0].name }
  return $null
}

function Get-LogTail {
  <#
  .SYNOPSIS
    The last N non-empty lines of a job log, with terminal escapes and the leading timestamp removed. When the log has
    a ##[error] line the tail ends there, so it shows the failed step and not the cleanup after it.
  #>
  [CmdletBinding()]
  [OutputType([string[]])]
  param([AllowEmptyString()][string]$Text, [int]$Lines = 30)
  $clean = foreach ($l in ($Text -split "`r?`n")) {
    $l = [regex]::Replace($l, "\[[0-9;]*[A-Za-z]", '')
    $l = [regex]::Replace($l, '^\d{4}-\d{2}-\d{2}T[\d:.]+Z\s?', '')
    if ($l.Trim()) { $l }
  }
  $clean = @($clean)
  # The step's own output ends at the runner's ##[error] line; what follows is cleanup noise.
  $last = -1
  for ($i = 0; $i -lt $clean.Count; $i++) { if ($clean[$i] -like '##`[error`]*') { $last = $i } }
  if ($last -ge 0) { $clean = $clean[0..$last] }
  return @($clean | Select-Object -Last $Lines)
}

function Get-FailureDetail {
  <#
  .SYNOPSIS
    For each failing, non-advisory Actions check of a statusCheckRollup, the job, its failed step and the log tail.
  .OUTPUTS
    Strings, one block per failing job, ready to print.
  #>
  [CmdletBinding()]
  [OutputType([string[]])]
  param([Parameter(Mandatory)][string]$Repo, [AllowEmptyCollection()][object[]]$Rollup = @())
  foreach ($c in $Rollup) {
    if ($c.PSObject.Properties.Name -contains 'context') { continue }
    if ($c.status -ne 'COMPLETED' -or $c.conclusion -notin $script:FailingConclusions -or (Test-AdvisoryCheck -Name $c.name)) { continue }
    $id = Get-JobIdFromUrl -Url $c.detailsUrl
    if (-not $id) { "FAILED: $($c.name) (no Actions job to read: $($c.detailsUrl))"; continue }
    try {
      $job = (Invoke-Native -FilePath gh -Arguments 'api', "repos/$Repo/actions/jobs/$id" -Step "read job $id" -Fix 'gh auth status').Output -join "`n" | ConvertFrom-Json
      $logs = (Invoke-Native -FilePath gh -Arguments 'api', '--allow-escape-sequences', "repos/$Repo/actions/jobs/$id/logs" -Step "read the log of job $id" -Fix 'the log may have expired or the job still be running').Output -join "`n"
      $step = Get-FailedStepName -Job $job
      $name = if ($step) { $step } else { '(no step failed: the job was cancelled, timed out or never started)' }
      "FAILED: $($job.name) / step: $name`n" + ((Get-LogTail -Text $logs -Lines 30) -join "`n")
    } catch {
      "FAILED: $($c.name) (could not read its log: $($_.Exception.Message))"
    }
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
  $fields = 'number,state,isDraft,headRefOid,baseRefName,mergeStateStatus,reviewDecision,statusCheckRollup,closingIssuesReferences,files'
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
  param([int]$Number, [bool]$DoMerge, [string]$Repo, [bool]$Detail = $false)
  if (-not $Repo) { $Repo = Get-RepoSlug -RepoRoot (Get-RepoRoot -Path $PSScriptRoot) }
  $pr = Get-PullRequestState -Repo $Repo -Number $Number
  $ancestor = Get-AncestorStatus -Repo $Repo -Base $pr.baseRefName -HeadSha $pr.headRefOid
  $verdict = Get-PrVerdict -Pr $pr -AncestorStatus $ancestor
  Write-KippleInfo $verdict.Text
  $verdict
  if ($Detail) {
    foreach ($block in @(Get-FailureDetail -Repo $Repo -Rollup @($pr.statusCheckRollup))) { Write-KippleInfo $block }
  }
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
  $boundArgs = @{ Number = $Number; DoMerge = [bool]$Merge; Repo = $Repo; Detail = [bool]$FailureDetail; WhatIf = $WhatIfPreference }
  Invoke-ToolMain -Name 'pr-ready' -Body { Invoke-PrReady @boundArgs }
}
