#requires -Version 7.2
<#
.SYNOPSIS
  Deletes local (and optionally remote) branches whose pull request is merged, and their agent worktrees.
.DESCRIPTION
  Run it by hand now and then, after merges. It asks GitHub which pull requests are merged and open, and only
  touches a branch when ALL of these hold:
    - it is not main, master or the repository's default branch;
    - no open pull request uses it as its head branch or as its base branch (a stacked PR's base);
    - a MERGED pull request used it as its head, and the branch tip equals that pull request's final head commit
      (so a branch with later commits that were never merged is kept);
    - it is not checked out in the worktree this script file lives in (not necessarily your current directory), and
      its worktree (if any, under .claude/worktrees) is not locked,
      has no uncommitted changes, its status can be read, and it holds no ignored files other than build output
      (node_modules, web/dist, coverage, tsbuildinfo, the built binary, .claude/settings.local.json), because removing a worktree deletes ignored files too.
  For each such branch, in this order: remove its worktree under .claude/worktrees (`git worktree remove`, no --force,
  after a fresh status check; a locked worktree is never removed), then delete the local branch with
  `git update-ref -d` at the planned sha (refused if the tip moved), and with -IncludeRemote `git push` with
  `--force-with-lease` at the planned sha, only when origin's copy is at the same commit as the merged PR's head.

  What it can change: local branches and worktrees, and with -IncludeRemote branches on origin. That is
  destructive, so the script prints exactly what it will do and, unless -WhatIf, asks you to type yes.
  -WhatIf prints the plan and changes nothing, except that it first runs
  `git fetch --prune origin` (remote-tracking refs only). Run it yourself; an automated agent should only use -WhatIf.
.PARAMETER IncludeRemote
  Also delete the merged branches on origin.
.EXAMPLE
  pwsh scripts/branch-cleanup.ps1 -WhatIf
.EXAMPLE
  pwsh scripts/branch-cleanup.ps1 -IncludeRemote -Verbose
.NOTES
  Exit codes: 0 done (or nothing to do, or -WhatIf, or you did not type yes), 1 an action failed,
  2 usage or environment error.
  If this breaks: it depends on `gh pr list --json headRefName,baseRefName,mergedAt,headRefOid` (Get-PullRequestHead),
  `gh repo view --json defaultBranchRef` (Get-DefaultBranch), `git status --porcelain --ignored=matching` (Get-WorktreeBlocker),
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
  $r = Invoke-Native -FilePath gh -Arguments 'pr', 'list', '--state', $State, '--limit', '1000', '--json', 'headRefName,baseRefName,headRefOid,mergedAt' `
    -Step "list $State pull requests" -Fix 'gh auth status, and run inside the checkout' -WorkingDirectory $RepoRoot
  $text = ($r.Output -join "`n")
  if ([string]::IsNullOrWhiteSpace($text)) { return @() }
  return @($text | ConvertFrom-Json)
}

function Get-WorktreeBlocker {
  <#
  .SYNOPSIS
    Decides from `git status --porcelain --ignored=matching` whether a worktree must be kept. Fails closed.
  .DESCRIPTION
    Returns $null when the worktree may be removed, else the reason to keep it. A status that could not be read
    (non-zero exit: lock file, ownership refusal, corrupt index) is a reason, never "clean". Ignored files
    (lines starting "!! ") are unrecoverable once the worktree is removed, so they block too, except build output and tool state (see $buildOutput).
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][int]$ExitCode, [string[]]$Lines = @())
  if ($ExitCode -ne 0) {
    $first = ($Lines | Select-Object -First 1)
    return "could not read the worktree status ($first)"
  }
  $changed = @($Lines | Where-Object { $_ -and $_ -notmatch '^!! ' })
  if ($changed.Count) { return "its worktree has uncommitted changes (e.g. $($changed[0].Trim()))" }
  # Build output and per-worktree tool state that can be regenerated. Patterns are matched against what git really prints:
  # one line per file under web/dist (it holds a tracked placeholder, so the directory is never collapsed), the TypeScript
  # build info, the built binary, and the worktree's Claude permission file.
  $buildOutput = '^!! (web/)?(node_modules|dist|coverage)(/|$)|^!! (web/)?[^/]+\.tsbuildinfo$|^!! kipple(\.exe)?$|^!! \.claude/settings\.local\.json(\.bak[^/]*)?$'
  $ignored = @($Lines | Where-Object { $_ -match '^!! ' -and $_ -notmatch $buildOutput })
  if ($ignored.Count) { return "its worktree has ignored files that removal would delete (e.g. $($ignored[0].Substring(3)))" }
  return $null
}

function Get-DefaultBranch {
  <#
  .SYNOPSIS
    The repository's default branch name from GitHub.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$RepoRoot)
  $r = Invoke-Native -FilePath gh -Arguments 'repo', 'view', '--json', 'defaultBranchRef', '--jq', '.defaultBranchRef.name' `
    -Step 'read the default branch' -Fix 'gh auth status, and run inside the checkout' -WorkingDirectory $RepoRoot
  return $r.Output[0].Trim()
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
  .PARAMETER Blocked
    Hashtable of worktree path to the reason it must be kept (dirty, unreadable status, ignored files).
  .PARAMETER DefaultBranch
    The repository's default branch name, protected like main.
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
    [hashtable]$Blocked = @{},
    [string]$DefaultBranch = '',
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
    if ($name -in $script:ProtectedBranches -or ($DefaultBranch -and $name -eq $DefaultBranch)) { $reason = 'protected branch' }
    elseif (@($Open | Where-Object { $_.headRefName -eq $name }).Count) { $reason = 'has an open pull request' }
    elseif (@($Open | Where-Object { $_.PSObject.Properties.Name -contains 'baseRefName' -and $_.baseRefName -eq $name }).Count) { $reason = 'is the base branch of an open pull request' }
    elseif ($mergedForBranch.Count -eq 0) { $reason = 'no merged pull request' }
    elseif ($wt -and $CurrentPath -and ($wt.Path -eq $CurrentPath)) { $reason = 'checked out in the worktree this script runs from' }
    elseif ($wt -and -not $wt.Path.StartsWith($WorktreeRoot + '/', [StringComparison]::OrdinalIgnoreCase)) {
      $reason = "checked out in a worktree outside .claude/worktrees ($($wt.Path))"
    }
    elseif ($wt -and $wt.Locked) { $reason = 'its worktree is locked (in use by another session or tool)' }
    elseif ($wt -and $Blocked.ContainsKey($wt.Path)) { $reason = $Blocked[$wt.Path] }
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
    $actions.Add([pscustomobject]@{ Kind = 'RemoveWorktree'; Branch = $row.Branch; Target = $row.WorktreePath; Sha = $row.LocalSha; Text = "remove worktree $($row.WorktreePath)" })
  }
  foreach ($row in $doomed | Where-Object { $_.LocalSha }) {
    $actions.Add([pscustomobject]@{ Kind = 'DeleteLocal'; Branch = $row.Branch; Target = $row.Branch; Sha = $row.LocalSha; Text = "delete local branch $($row.Branch) (tip $($row.LocalSha.Substring(0, 9)))" })
  }
  foreach ($row in $doomed | Where-Object { $_.DeleteRemote }) {
    $actions.Add([pscustomobject]@{ Kind = 'DeleteRemote'; Branch = $row.Branch; Target = $row.Branch; Sha = $row.RemoteSha; Text = "delete origin/$($row.Branch) (tip $($row.RemoteSha.Substring(0, 9)))" })
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
    Runs one action, re-checking what the plan assumed. Returns $true on success; failures are reported, not thrown.
  .DESCRIPTION
    Minutes can pass between the printed plan and the typed yes, so nothing is deleted on the plan's word alone:
    a worktree is removed without --force only after a fresh status shows it still holds nothing to lose; a local
    branch is deleted with `git update-ref -d <ref> <planned sha>`, which fails when the tip has moved, and never
    while it is checked out in a worktree; a remote branch with `--force-with-lease=<ref>:<planned sha>`, which
    fails when someone pushed to it since the plan.
  #>
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][pscustomobject]$Action, [Parameter(Mandatory)][string]$RepoRoot)
  switch ($Action.Kind) {
    'RemoveWorktree' {
      $s = Invoke-Native -FilePath git -Arguments '-C', $Action.Target, 'status', '--porcelain', '--ignored=matching' -Step "re-check $($Action.Target)" -AllowFailure
      $reason = Get-WorktreeBlocker -ExitCode $s.ExitCode -Lines $s.Output
      if ($reason) { Write-KippleInfo "  kept: $($Action.Target) changed since the plan: $reason"; return $false }
      # No --force: git itself refuses a worktree with modified or untracked files, and a locked one needs two.
      $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'worktree', 'remove', $Action.Target -Step $Action.Text -AllowFailure -Fix 'close any program using the folder, then retry'
      return ($r.ExitCode -eq 0)
    }
    'DeleteLocal' {
      $w = Get-WorktreeEntry -Lines (Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'worktree', 'list', '--porcelain' -Step 'list worktrees').Output
      if ($w | Where-Object { $_.Branch -eq $Action.Target }) { Write-KippleInfo "  kept: $($Action.Target) is still checked out in a worktree"; return $false }
      $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'update-ref', '-d', "refs/heads/$($Action.Target)", $Action.Sha -Step $Action.Text -AllowFailure -Fix 'the branch tip moved since the plan; run this script again'
      return ($r.ExitCode -eq 0)
    }
    'DeleteRemote' {
      $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'push', "--force-with-lease=refs/heads/$($Action.Target):$($Action.Sha)", 'origin', ":refs/heads/$($Action.Target)" -Step $Action.Text -AllowFailure -Fix 'someone pushed to the branch since the plan, or you lack push access; run this script again'
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
  $blocked = @{}
  foreach ($w in $agentWorktrees) {
    $s = Invoke-Native -FilePath git -Arguments '-C', $w.Path, 'status', '--porcelain', '--ignored=matching' -Step "check $($w.Path) for changes" -AllowFailure
    $reason = Get-WorktreeBlocker -ExitCode $s.ExitCode -Lines $s.Output
    if ($reason) { $blocked[$w.Path] = $reason }
  }
  $defaultBranch = Get-DefaultBranch -RepoRoot $root
  $plan = Get-CleanupPlan -Local $local -Remote $remote -Merged $merged -Open $open -Worktrees $worktrees `
    -WorktreeRoot $worktreeRoot -CurrentPath (ConvertTo-NormalPath -Path $root) -Blocked $blocked -DefaultBranch $defaultBranch -IncludeRemote:$IncludeRemote
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
