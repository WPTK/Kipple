#requires -Version 7.2
<#
.SYNOPSIS
  Runs the release test gates, one at a time, on a detached worktree of an exact commit.
.DESCRIPTION
  Run it by hand on the commit you are about to tag, for a release in the second or third tier of
  docs/maintainers/RELEASING.md ("Gates scale with what changed"). Docs-only and release-commit tiers need only CI.

  Steps, in this order and never in parallel (timing tests flake under load):
    go (twice)   go test -shuffle=<seed> ./... , two runs, each with its own random seed
    fuzz         scripts/fuzz.ps1 (left out by -SkipFuzz)
    web          npm ci --ignore-scripts, then npm test, in web/
    changelog    node scripts/changelog.mjs check
    node         node --test scripts/*.test.mjs

  If a go test run fails, each failing package is re-run alone with the same shuffle seed. A package that passes
  alone is reported as FLAKY (both results appear in the table) and does not fail the run; one that fails alone
  is a real FAIL. Every step takes a heavy-step lock and refuses to start while another heavy step, in this or
  another run of this script, holds it. No -race: this machine has no C compiler.

  What it can change: it adds a detached git worktree and a logs folder under the temp directory and removes
  both at the end (-KeepWorktree keeps them). It never touches your working tree, the network (beyond
  `git fetch origin` when -Ref is not given, and npm ci), or any remote state.
.PARAMETER Ref
  Full 40-character commit sha to test. Default: the tip of origin/main after a fetch. The sha under test is
  printed first so the table can be matched to the commit you tag.
.PARAMETER SkipFuzz
  Skip the fuzz step (60 s per target).
.PARAMETER Only
  Run only the named steps. Mainly for testing this script.
.PARAMETER KeepWorktree
  Leave the worktree and logs in place, to look at a failure.
.EXAMPLE
  pwsh scripts/maintainers/release-gates.ps1
.EXAMPLE
  pwsh scripts/maintainers/release-gates.ps1 -Ref 0123456789abcdef0123456789abcdef01234567 -SkipFuzz -Verbose
.EXAMPLE
  pwsh scripts/maintainers/release-gates.ps1 -WhatIf    # prints the commit and the plan, changes nothing
.NOTES
  Exit codes: 0 every step passed (FLAKY counts as a pass but is flagged), 1 a step failed,
  2 usage or environment error (bad ref, tool missing).
  If this breaks: it depends on `go test` printing lines that start with "FAIL <package>" and "--- FAIL: <test>"
  (Get-GoFailure in scripts/maintainers/lib/Kipple.Tools.ps1), on `npm ci` and `npm test` in web/, on
  `node scripts/changelog.mjs check` and on scripts/fuzz.ps1. Change the step list in Get-GatePlan.
  Tests: Invoke-Pester scripts/maintainers/release-gates.Tests.ps1
#>
[CmdletBinding(SupportsShouldProcess)]
param(
  [ValidatePattern('^[0-9a-fA-F]{40}$')][string]$Ref,
  [switch]$SkipFuzz,
  [ValidateSet('go', 'fuzz', 'web', 'changelog', 'node')][string[]]$Only = @(),
  [switch]$KeepWorktree
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lib/Kipple.Tools.ps1')

function Get-GatePlan {
  <#
  .SYNOPSIS
    The ordered list of steps to run, after applying -Only and -SkipFuzz.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([string[]]$Only = @(), [switch]$SkipFuzz)
  $all = @(
    [pscustomobject]@{ Key = 'go'; Name = 'go test ./... (run 1)'; Run = 1 }
    [pscustomobject]@{ Key = 'go'; Name = 'go test ./... (run 2)'; Run = 2 }
    [pscustomobject]@{ Key = 'fuzz'; Name = 'fuzz (scripts/fuzz.ps1)'; Run = 0 }
    [pscustomobject]@{ Key = 'web'; Name = 'web: npm ci, npm test'; Run = 0 }
    [pscustomobject]@{ Key = 'changelog'; Name = 'changelog check'; Run = 0 }
    [pscustomobject]@{ Key = 'node'; Name = 'node --test scripts/*.test.mjs'; Run = 0 }
  )
  $selected = $all | Where-Object { $Only.Count -eq 0 -or $Only -contains $_.Key }
  if ($SkipFuzz) { $selected = $selected | Where-Object { $_.Key -ne 'fuzz' } }
  return @($selected)
}

function Get-GateOutcome {
  <#
  .SYNOPSIS
    Decides PASS, FLAKY or FAIL from the full run's failing packages and the alone re-run of each.
  .PARAMETER Packages
    Failing packages from the full run.
  .PARAMETER AlonePassed
    Hashtable of package name to $true when it passed on its own re-run.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([string[]]$Packages = @(), [hashtable]$AlonePassed = @{})
  if ($Packages.Count -eq 0) {
    return [pscustomobject]@{ Status = 'FAIL'; Note = 'failed without a failing package (build error?)' }
  }
  $notes = foreach ($p in $Packages) {
    if ($AlonePassed[$p]) { "$p failed in the full run, passed alone" } else { "$p failed in the full run and alone" }
  }
  $allPassed = -not ($Packages | Where-Object { -not $AlonePassed[$_] })
  return [pscustomobject]@{ Status = $(if ($allPassed) { 'FLAKY' } else { 'FAIL' }); Note = $notes -join '; ' }
}

function Get-GateLockName { 'Local\kipple-release-gates-heavy' }

function Enter-HeavyStep {
  <#
  .SYNOPSIS
    Takes the heavy-step lock without waiting. Returns the mutex, or $null when another heavy step holds it.
  #>
  [CmdletBinding()]
  param()
  $mutex = [System.Threading.Mutex]::new($false, (Get-GateLockName))
  if ($mutex.WaitOne(0)) { return $mutex }
  $mutex.Dispose()
  return $null
}

function Invoke-GateStep {
  <#
  .SYNOPSIS
    Runs one step under the heavy-step lock and returns a result row (Step, Status, Seconds, Note).
  .PARAMETER Gate
    Name of a function (Invoke-GoGate and friends) that returns an object with Status and Note.
  .PARAMETER GateArgs
    Named arguments splatted to that function.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string]$Gate, [hashtable]$GateArgs = @{})
  Write-KippleInfo "`n=== $Name"
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $mutex = Enter-HeavyStep
  if (-not $mutex) {
    $row = [pscustomobject]@{ Step = $Name; Status = 'FAIL'; Seconds = 0; Note = 'refused: another heavy step is running (another release-gates run?)' }
  } else {
    try {
      $r = & $Gate @GateArgs
      $row = [pscustomobject]@{ Step = $Name; Status = $r.Status; Seconds = [int]$sw.Elapsed.TotalSeconds; Note = $r.Note }
    } catch {
      $row = [pscustomobject]@{ Step = $Name; Status = 'FAIL'; Seconds = [int]$sw.Elapsed.TotalSeconds; Note = $_.Exception.Message }
    } finally {
      $mutex.ReleaseMutex()
      $mutex.Dispose()
    }
  }
  Write-KippleInfo ('{0,-6} {1} {2}' -f $row.Status, $row.Step, $row.Note)
  return $row
}

function Show-LogTail {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Path, [int]$Lines = 20)
  if (Test-Path -LiteralPath $Path) {
    Get-Content -LiteralPath $Path -Tail $Lines | ForEach-Object { Write-KippleInfo "    | $_" }
  }
}

function Invoke-GoGate {
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param(
    [Parameter(Mandatory)][string]$Work, [Parameter(Mandatory)][string]$Logs, [Parameter(Mandatory)][int]$Run,
    [int]$Seed = (Get-Random -Minimum 1 -Maximum 2147483647)
  )
  $log = Join-Path $Logs "gotest$Run.log"
  $r = Invoke-Native -FilePath go -Arguments 'test', "-shuffle=$Seed", '-timeout', '15m', './...' -WorkingDirectory $Work `
    -Step "go test run $Run" -Fix 'read the log tail above' -AllowFailure -LogPath $log
  if ($r.ExitCode -eq 0) { return [pscustomobject]@{ Status = 'PASS'; Note = '' } }
  Show-LogTail -Path $log -Lines 25
  $failure = Get-GoFailure -Text ($r.Output -join "`n")
  $alone = @{}
  foreach ($pkg in $failure.Packages) {
    $aloneLog = Join-Path $Logs ("gotest$Run-alone-" + ($pkg -replace '[^\w]', '_') + '.log')
    $a = Invoke-Native -FilePath go -Arguments 'test', '-count=1', "-shuffle=$Seed", '-timeout', '15m', $pkg -WorkingDirectory $Work `
      -Step "re-run $pkg alone" -Fix 'read the log tail above' -AllowFailure -LogPath $aloneLog
    $alone[$pkg] = ($a.ExitCode -eq 0)
    if ($a.ExitCode -ne 0) { Show-LogTail -Path $aloneLog -Lines 25 }
  }
  $outcome = Get-GateOutcome -Packages $failure.Packages -AlonePassed $alone
  $tests = if ($failure.Tests.Count) { " (failed tests: $($failure.Tests -join ', '))" } else { '' }
  # The alone re-run uses the same shuffle seed, so an order-dependent failure fails again and is not called FLAKY.
  return [pscustomobject]@{ Status = $outcome.Status; Note = $outcome.Note + $tests + " (shuffle seed $Seed)" }
}

function Get-StatusRow {
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][int]$ExitCode, [string]$FailNote = '')
  if ($ExitCode -eq 0) { return [pscustomobject]@{ Status = 'PASS'; Note = '' } }
  return [pscustomobject]@{ Status = 'FAIL'; Note = "exit $ExitCode. $FailNote".Trim() }
}

function Invoke-FuzzGate {
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Work, [Parameter(Mandatory)][string]$Logs)
  $log = Join-Path $Logs 'fuzz.log'
  $r = Invoke-Native -FilePath pwsh -Arguments '-NoProfile', '-File', 'scripts/fuzz.ps1' -WorkingDirectory $Work `
    -Step 'fuzz' -Fix 'the failing input is under testdata/fuzz in the package; fix the bug and keep it as a seed' -AllowFailure -LogPath $log
  if ($r.ExitCode -ne 0) { Show-LogTail -Path $log -Lines 25 }
  return Get-StatusRow -ExitCode $r.ExitCode -FailNote 'A failing input is under testdata/fuzz in the package.'
}

function Invoke-WebGate {
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Work, [Parameter(Mandatory)][string]$Logs, [Parameter(Mandatory)][string]$NpmCache)
  $web = Join-Path $Work 'web'
  $null = Invoke-Native -FilePath npm -Arguments 'ci', '--ignore-scripts', '--cache', $NpmCache, '--no-audit', '--no-fund' -WorkingDirectory $web `
    -Step 'npm ci' -Fix 'check the network; delete web/node_modules in the worktree and retry' -LogPath (Join-Path $Logs 'web-ci.log')
  $log = Join-Path $Logs 'web-test.log'
  $t = Invoke-Native -FilePath npm -Arguments 'test' -WorkingDirectory $web -Step 'npm test' -Fix 'read the log tail above' -AllowFailure -LogPath $log
  if ($t.ExitCode -ne 0) { Show-LogTail -Path $log -Lines 25 }
  return Get-StatusRow -ExitCode $t.ExitCode -FailNote 'Re-run a timing test alone before calling it flaky.'
}

function Invoke-ChangelogGate {
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Work, [Parameter(Mandatory)][string]$Logs)
  $log = Join-Path $Logs 'changelog.log'
  $r = Invoke-Native -FilePath node -Arguments 'scripts/changelog.mjs', 'check' -WorkingDirectory $Work -Step 'changelog check' -Fix 'read the output above' -AllowFailure -LogPath $log
  if ($r.ExitCode -ne 0) { Show-LogTail -Path $log -Lines 10 }
  return Get-StatusRow -ExitCode $r.ExitCode
}

function Invoke-NodeGate {
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Work, [Parameter(Mandatory)][string]$Logs)
  $log = Join-Path $Logs 'node-tests.log'
  $files = @(Get-ChildItem -LiteralPath (Join-Path $Work 'scripts') -Filter '*.test.mjs' | ForEach-Object { "scripts/$($_.Name)" })
  $r = Invoke-Native -FilePath node -Arguments (@('--test') + $files) -WorkingDirectory $Work -Step 'node tests' -Fix 'read the log tail above' -AllowFailure -LogPath $log
  if ($r.ExitCode -ne 0) { Show-LogTail -Path $log -Lines 25 }
  return Get-StatusRow -ExitCode $r.ExitCode
}
function Invoke-ReleaseGate {
  <#
  .SYNOPSIS
    The body of the script: resolve the commit, build the worktree, run the plan, print the table.
  .OUTPUTS
    Result rows, then the exit code (0 or 1) as the last element.
  #>
  [CmdletBinding(SupportsShouldProcess)]
  param([string]$Ref, [switch]$SkipFuzz, [string[]]$Only = @(), [switch]$KeepWorktree)
  $repo = Get-RepoRoot -Path $PSScriptRoot
  if ($Ref) {
    $sha = Resolve-CommitSha -Ref $Ref -RepoRoot $repo
  } else {
    $null = Invoke-Native -FilePath git -Arguments '-C', $repo, 'fetch', '--quiet', 'origin' -Step 'fetch origin' -Fix 'check the network and the origin remote'
    $sha = Resolve-CommitSha -Ref 'origin/main' -RepoRoot $repo
  }
  $short = $sha.Substring(0, 12)
  # A random suffix keeps two runs on the same sha (or a kept worktree from an earlier run) from colliding.
  $unique = '{0}-{1}' -f $short, ([guid]::NewGuid().ToString('N').Substring(0, 6))
  $work = Join-Path ([IO.Path]::GetTempPath()) "kipple-gates-$unique"
  $logs = Join-Path ([IO.Path]::GetTempPath()) "kipple-gates-$unique-logs"
  $npmCache = Join-Path ([IO.Path]::GetTempPath()) 'kipple-npm-cache'   # the shared npm cache gives EPERM on the dev machine
  $plan = Get-GatePlan -Only $Only -SkipFuzz:$SkipFuzz

  Write-KippleInfo "Commit under test: $sha"
  Write-KippleInfo 'Plan:'
  foreach ($s in $plan) { Write-KippleInfo "  - $($s.Name)" }
  if ($WhatIfPreference) {
    Write-KippleInfo "WhatIf: would create a detached worktree at $work, run the steps above one at a time, then remove it."
    return 0
  }

  $rows = [System.Collections.Generic.List[object]]::new()
  try {
    $null = New-Item -ItemType Directory -Force -Path $logs
    New-DetachedWorktree -RepoRoot $repo -Path $work -Sha $sha
    Write-KippleInfo "Worktree: $work   logs: $logs"
    $common = @{ Work = $work; Logs = $logs }
    foreach ($s in $plan) {
      $gate = @{ go = 'Invoke-GoGate'; fuzz = 'Invoke-FuzzGate'; web = 'Invoke-WebGate'; changelog = 'Invoke-ChangelogGate'; node = 'Invoke-NodeGate' }[$s.Key]
      $gateArgs = $common.Clone()
      if ($s.Key -eq 'go') { $gateArgs.Run = $s.Run }
      if ($s.Key -eq 'web') { $gateArgs.NpmCache = $npmCache }
      $rows.Add((Invoke-GateStep -Name $s.Name -Gate $gate -GateArgs $gateArgs))
    }
  } finally {
    # Whatever is on disk is cleaned up, also a half-created worktree (git worktree add can fail part way).
    if ($KeepWorktree) {
      if (Test-Path -LiteralPath $work) { Write-KippleInfo "Kept: $work and $logs" }
    } elseif (Remove-DetachedWorktree -RepoRoot $repo -Path $work) {
      Remove-Item -LiteralPath $logs -Recurse -Force -ErrorAction SilentlyContinue
    } else {
      Write-KippleInfo "The worktree stays at $work and the logs at $logs (see the warning above)."
    }
  }

  Write-KippleInfo "`n=== release gates for $sha"
  Write-KippleInfo (($rows | Format-Table Step, Status, Seconds, Note -AutoSize -Wrap | Out-String).TrimEnd())
  $rows
  $failed = @($rows | Where-Object { $_.Status -eq 'FAIL' })
  if ($failed.Count) { Write-KippleInfo "$($failed.Count) step(s) failed"; return 1 }
  if (@($rows | Where-Object { $_.Status -eq 'FLAKY' }).Count) { Write-KippleInfo 'passed, with a flaky step (see the table)' } else { Write-KippleInfo 'all gates passed' }
  return 0
}

if ($MyInvocation.InvocationName -ne '.') {
  $boundArgs = $PSBoundParameters
  Invoke-ToolMain -Name 'release-gates' -Body {
    Invoke-ReleaseGate @boundArgs
  }
}
