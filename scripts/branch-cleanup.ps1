#requires -Version 7
<#
.SYNOPSIS
  Deletes local (and optionally remote) branches whose pull request is merged, and their agent worktrees.
.DESCRIPTION
  Run it by hand now and then, after merges. It asks GitHub which pull requests are merged and open, and only
  touches a branch when ALL of these hold:
    - it is not main or master;
    - no open pull request uses it as its head branch;
    - a MERGED pull request used it as its head, and the branch tip equals that pull request's final head commit
      (so a branch with later commits that were never merged is kept);
    - it is not checked out in this checkout, and its worktree (if any, under .claude/worktrees) has no
      uncommitted changes.
  For each such branch, in this order: remove its worktree under .claude/worktrees (`git worktree remove --force`,
  repeated once for a locked worktree), then `git branch -D`, and with -IncludeRemote
  `git push origin --delete <branch>` when origin's copy is at the same commit as the merged pull request's head.

  What it can change: local branches and worktrees, and with -IncludeRemote branches on origin. That is
  destructive, so the script prints exactly what it will do and, unless -WhatIf, asks you to type yes.
  -WhatIf prints the plan and changes nothing. Run it yourself; an automated agent should only use -WhatIf.
.PARAMETER IncludeRemote
  Also delete the merged branches on origin.
.EXAMPLE
  pwsh scripts/branch-cleanup.ps1 -WhatIf
.EXAMPLE
  pwsh scripts/branch-cleanup.ps1 -IncludeRemote -Verbose
.NOTES
  Exit codes: 0 done (or nothing to do, or -WhatIf, or you did not type yes), 1 an action failed,
  2 usage or environment error.
  If this breaks: it depends on `gh pr list --json headRefName,mergedAt,headRefOid` (Get-PullRequestHead),
  `git for-each-ref` and `git worktree list --porcelain` output (Get-WorktreeEntry). The decision logic is
  Get-CleanupPlan and is covered by scripts/branch-cleanup.Tests.ps1.
#>
[CmdletBinding(SupportsShouldProcess)]
param([switch]$IncludeRemote)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lib/Kipple.Tools.ps1')

$script:ProtectedBranches = @('main', 'master')

function ConvertTo-RefMap {
  <#
  .SYNOPSIS
    Turns `git for-each-ref --format='%(refname:short) %(objectname)'` lines into a name-to-sha hashtable.
  .PARAMETER Strip
    A prefix to remove from each name (for example origin/).
  #>
  [CmdletBinding()]
  [OutputType([hashtable])]
  param([string[]]$Lines = @(), [string]$Strip = '')
  $map = @{}
  foreach ($line in $Lines) {
    if ($line -notmatch '^(\S+) ([0-9a-f]{40})$') { continue }
    $name = $Matches[1]
    if ($Strip -and -not $name.StartsWith($Strip)) { continue }
    $name = $name.Substring($Strip.Length)
    if ($name -in @('HEAD', 'origin')) { continue }
    $map[$name] = $Matches[2]
  }
  return $map
}

function ConvertTo-NormalPath {
  <#
  .SYNOPSIS
    A full path with forward slashes and no trailing slash, so paths from git and from PowerShell compare equal.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Path)
  return ([IO.Path]::GetFullPath($Path)).Replace('\', '/').TrimEnd('/')
}

function Get-WorktreeEntry {
  <#
  .SYNOPSIS
    Parses `git worktree list --porcelain` into objects with Path, Branch (short name or $null) and Locked.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([string[]]$Lines = @())
  $entries = [System.Collections.Generic.List[object]]::new()
  $current = $null
  foreach ($line in $Lines) {
    if ($line -like 'worktree *') {
      $current = [pscustomobject]@{ Path = (ConvertTo-NormalPath -Path $line.Substring(9)); Branch = $null; Locked = $false }
      $entries.Add($current)
    } elseif ($null -ne $current -and $line -like 'branch refs/heads/*') {
      $current.Branch = $line.Substring('branch refs/heads/'.Length)
    } elseif ($null -ne $current -and $line -like 'locked*') {
      $current.Locked = $true
    }
  }
  return $entries.ToArray()
}

function Get-PullRequestHead {
  <#
  .SYNOPSIS
    Reads pull request head branches from GitHub for one state (merged or open).
  .OUTPUTS
    Objects with headRefName, headRefOid and (for merged) mergedAt.
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][ValidateSet('merged', 'open')][string]$State, [Parameter(Mandatory)][string]$RepoRoot)
  $r = Invoke-Native -FilePath gh -Arguments 'pr', 'list', '--state', $State, '--limit', '1000', '--json', 'headRefName,headRefOid,mergedAt' `
    -Step "list $State pull requests" -Fix 'gh auth status, and run inside the checkout' -WorkingDirectory $RepoRoot
  $text = ($r.Output -join "`n")
  if ([string]::IsNullOrWhiteSpace($text)) { return @() }
  return @($text | ConvertFrom-Json)
}

function Get-CleanupPlan {
  <#
  .SYNOPSIS
    Decides, for every local (and, with -IncludeRemote, remote) branch, whether it may be deleted and why not.
  .DESCRIPTION
    Pure: takes already-read facts and returns plan rows. The rules are listed in the script help.
  .PARAMETER Local
    Hashtable of local branch name to tip sha.
  .PARAMETER Remote
    Hashtable of origin branch name to tip sha.
  .PARAMETER Merged
    Merged pull requests (headRefName, headRefOid).
  .PARAMETER Open
    Open pull requests (headRefName).
  .PARAMETER Worktrees
    Worktree entries (Path, Branch, Locked).
  .PARAMETER WorktreeRoot
    The .claude/worktrees directory; only worktrees under it are ever removed.
  .PARAMETER CurrentPath
    The worktree this script runs from; its branch is never deleted.
  .PARAMETER Dirty
    Worktree paths with uncommitted changes.
  .OUTPUTS
    Rows with Branch, Delete (bool), Reason, LocalSha, RemoteSha, DeleteRemote (bool), WorktreePath.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param(
    [hashtable]$Local = @{},
    [hashtable]$Remote = @{},
    [object[]]$Merged = @(),
    [object[]]$Open = @(),
    [object[]]$Worktrees = @(),
    [string]$WorktreeRoot = '',
    [string]$CurrentPath = '',
    [string[]]$Dirty = @(),
    [switch]$IncludeRemote
  )
  $names = @($Local.Keys)
  if ($IncludeRemote) { $names += @($Remote.Keys) }
  $rows = [System.Collections.Generic.List[object]]::new()
  foreach ($name in ($names | Sort-Object -Unique)) {
    $localSha = if ($Local.ContainsKey($name)) { $Local[$name] } else { $null }
    $remoteSha = if ($IncludeRemote -and $Remote.ContainsKey($name)) { $Remote[$name] } else { $null }
    $wt = $Worktrees | Where-Object { $_.Branch -eq $name } | Select-Object -First 1
    $mergedForBranch = @($Merged | Where-Object { $_.headRefName -eq $name })
    $reason = $null
    if ($name -in $script:ProtectedBranches) { $reason = 'protected branch' }
    elseif (@($Open | Where-Object { $_.headRefName -eq $name }).Count) { $reason = 'has an open pull request' }
    elseif ($mergedForBranch.Count -eq 0) { $reason = 'no merged pull request' }
    elseif ($wt -and $CurrentPath -and ($wt.Path -eq $CurrentPath)) { $reason = 'checked out in the worktree this script runs from' }
    elseif ($wt -and -not $wt.Path.StartsWith($WorktreeRoot + '/', [StringComparison]::OrdinalIgnoreCase)) {
      $reason = "checked out in a worktree outside .claude/worktrees ($($wt.Path))"
    }
    elseif ($wt -and ($Dirty -contains $wt.Path)) { $reason = 'its worktree has uncommitted changes' }
    else {
      $okLocal = (-not $localSha) -or [bool]($mergedForBranch | Where-Object { $_.headRefOid -eq $localSha })
      $okRemote = (-not $remoteSha) -or [bool]($mergedForBranch | Where-Object { $_.headRefOid -eq $remoteSha })
      if (-not $okLocal) { $reason = 'has commits that are not in the merged pull request (local tip differs)' }
      elseif (-not $okRemote) { $reason = 'origin has commits that are not in the merged pull request' }
    }
    $rows.Add([pscustomobject]@{
      Branch       = $name
      Delete       = ($null -eq $reason)
      Reason       = $(if ($null -eq $reason) { 'merged pull request, tip matches' } else { $reason })
      LocalSha     = $localSha
      RemoteSha    = $remoteSha
      DeleteRemote = ($null -eq $reason) -and [bool]$remoteSha
      WorktreePath = $(if ($wt) { $wt.Path } else { $null })
    })
  }
  return $rows.ToArray()
}

function Get-CleanupAction {
  <#
  .SYNOPSIS
    Expands plan rows into the ordered actions: worktrees first, then local branches, then remote branches.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([object[]]$Plan = @())
  $doomed = @($Plan | Where-Object { $_.Delete })
  $actions = [System.Collections.Generic.List[object]]::new()
  foreach ($row in $doomed | Where-Object { $_.WorktreePath }) {
    $actions.Add([pscustomobject]@{ Kind = 'RemoveWorktree'; Branch = $row.Branch; Target = $row.WorktreePath; Text = "remove worktree $($row.WorktreePath)" })
  }
  foreach ($row in $doomed | Where-Object { $_.LocalSha }) {
    $actions.Add([pscustomobject]@{ Kind = 'DeleteLocal'; Branch = $row.Branch; Target = $row.Branch; Text = "git branch -D $($row.Branch)" })
  }
  foreach ($row in $doomed | Where-Object { $_.DeleteRemote }) {
    $actions.Add([pscustomobject]@{ Kind = 'DeleteRemote'; Branch = $row.Branch; Target = $row.Branch; Text = "git push origin --delete $($row.Branch)" })
  }
  return $actions.ToArray()
}

function Read-TypedYes {
  <#
  .SYNOPSIS
    Asks a question and returns $true only when the answer is exactly "yes".
  #>
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][string]$Prompt)
  Write-KippleInfo $Prompt
  return (($Host.UI.ReadLine()).Trim() -eq 'yes')
}

function Invoke-CleanupAction {
  <#
  .SYNOPSIS
    Runs one action. Returns $true on success; failures are reported, not thrown, so the rest can continue.
  #>
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][pscustomobject]$Action, [Parameter(Mandatory)][string]$RepoRoot)
  switch ($Action.Kind) {
    'RemoveWorktree' {
      $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'worktree', 'remove', '--force', $Action.Target -Step $Action.Text -AllowFailure -Fix 'unlock it: git worktree unlock <path>'
      if ($r.ExitCode -ne 0) {
        # A locked worktree needs --force twice.
        $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'worktree', 'remove', '--force', '--force', $Action.Target -Step "$($Action.Text) (locked)" -AllowFailure -Fix 'close any program using the folder, then retry'
      }
      return ($r.ExitCode -eq 0)
    }
    'DeleteLocal' {
      $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'branch', '-D', $Action.Target -Step $Action.Text -AllowFailure -Fix 'the branch may be checked out somewhere (git worktree list)'
      return ($r.ExitCode -eq 0)
    }
    'DeleteRemote' {
      $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'push', 'origin', '--delete', $Action.Target -Step $Action.Text -AllowFailure -Fix 'check your push access; the branch may already be gone'
      return ($r.ExitCode -eq 0)
    }
  }
  throw "Unknown action kind '$($Action.Kind)'."
}

function Invoke-BranchCleanup {
  <#
  .SYNOPSIS
    The body of the script. Emits the plan rows, then the exit code (0 or 1).
  #>
  [CmdletBinding()]
  param([switch]$IncludeRemote, [switch]$DryRun)
  $root = Get-RepoRoot -Path $PSScriptRoot
  $null = Invoke-Native -FilePath git -Arguments '-C', $root, 'fetch', '--prune', '--quiet', 'origin' -Step 'fetch origin' -Fix 'check the network'

  $local = ConvertTo-RefMap -Lines (Invoke-Native -FilePath git -Arguments '-C', $root, 'for-each-ref', '--format=%(refname:short) %(objectname)', 'refs/heads' -Step 'list local branches').Output
  $remote = ConvertTo-RefMap -Strip 'origin/' -Lines (Invoke-Native -FilePath git -Arguments '-C', $root, 'for-each-ref', '--format=%(refname:short) %(objectname)', 'refs/remotes/origin' -Step 'list origin branches').Output
  $worktrees = Get-WorktreeEntry -Lines (Invoke-Native -FilePath git -Arguments '-C', $root, 'worktree', 'list', '--porcelain' -Step 'list worktrees').Output
  $merged = @(Get-PullRequestHead -State merged -RepoRoot $root)
  $open = @(Get-PullRequestHead -State open -RepoRoot $root)

  $mainRoot = $worktrees[0].Path
  $worktreeRoot = "$mainRoot/.claude/worktrees"
  $agentWorktrees = @($worktrees | Where-Object { $_.Path.StartsWith($worktreeRoot + '/', [StringComparison]::OrdinalIgnoreCase) })
  $dirty = foreach ($w in $agentWorktrees) {
    $s = Invoke-Native -FilePath git -Arguments '-C', $w.Path, 'status', '--porcelain' -Step "check $($w.Path) for changes" -AllowFailure
    if ($s.ExitCode -eq 0 -and ($s.Output -join '')) { $w.Path }
  }

  $plan = Get-CleanupPlan -Local $local -Remote $remote -Merged $merged -Open $open -Worktrees $worktrees `
    -WorktreeRoot $worktreeRoot -CurrentPath (ConvertTo-NormalPath -Path $root) -Dirty @($dirty) -IncludeRemote:$IncludeRemote
  Write-KippleInfo 'Branches:'
  foreach ($p in $plan) { Write-KippleInfo ('  {0,-6} {1}  ({2})' -f $(if ($p.Delete) { 'DELETE' } else { 'keep' }), $p.Branch, $p.Reason) }
  $plan

  $actions = @(Get-CleanupAction -Plan $plan)
  if ($actions.Count -eq 0) { Write-KippleInfo 'Nothing to do.'; return 0 }
  Write-KippleInfo "`nThis will:"
  foreach ($a in $actions) { Write-KippleInfo "  - $($a.Text)" }
  if ($DryRun) { Write-KippleInfo "`nWhatIf: nothing changed."; return 0 }
  if (-not (Read-TypedYes -Prompt "`nType yes to do all of the above (anything else cancels):")) { Write-KippleInfo 'Cancelled; nothing changed.'; return 0 }

  $failed = 0
  foreach ($a in $actions) {
    if (Invoke-CleanupAction -Action $a -RepoRoot $root) { Write-KippleInfo "done:   $($a.Text)" }
    else { Write-KippleInfo "FAILED: $($a.Text)"; $failed++ }
  }
  if ($failed) { Write-KippleInfo "$failed action(s) failed."; return 1 }
  return 0
}

if ($MyInvocation.InvocationName -ne '.') {
  $boundArgs = @{ IncludeRemote = $IncludeRemote; DryRun = [bool]$WhatIfPreference }
  Invoke-ToolMain -Name 'branch-cleanup' -Body { Invoke-BranchCleanup @boundArgs }
}



