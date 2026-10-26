<#
.SYNOPSIS
  Runs every Go fuzz target for a fixed time each. Run by hand before a release; the weekly Fuzz workflow
  (.github/workflows/fuzz.yml) runs this same script on Linux, one shard per runner.
.EXAMPLE
  scripts\fuzz.ps1                     # every target, 60 s each
  scripts\fuzz.ps1 -Seconds 10         # quick smoke
  scripts\fuzz.ps1 -Filter Sanitize    # only targets whose name or package matches
  scripts\fuzz.ps1 -List               # list targets and exit
  scripts\fuzz.ps1 -Shard 2/4          # the second of four disjoint slices of the targets (the Fuzz workflow runs one per runner)
  scripts\fuzz.ps1 -Release            # also the release-only targets (FuzzAPISequence, a fixed 300 s)
  scripts\fuzz.ps1 -TargetFile plan.json  # the targets listed in a fuzz-cache.ps1 plan file, without enumerating the module again
  scripts\fuzz.ps1 -ContinueOnFailure  # run every target and report all failures at the end
  scripts\fuzz.ps1 -Minutes 10         # long mode for the dev machine: every target 10 min, half the cores, corpus kept under .hunt\fuzz
  scripts\fuzz.ps1 -Minutes 10 -Parallel 2 -Filter OPML
Each target runs for -Seconds times its weight (slow targets get 2 or 3 times as long, see Get-FuzzBudget); the release-only
targets are left out unless -Release is given. go's progress output is not streamed: the run prints one summary row per target
(budgeted seconds, executions, new interesting inputs, PASS or FAIL), the last lines of a failing target's log, and at the end
the table (also appended to $env:GITHUB_STEP_SUMMARY as markdown when that is set).
Stops at the first failure (unless -ContinueOnFailure) and prints the failing input file (testdata\fuzz\<Target>\<hash>
inside the package; commit it as a regression seed once the bug is fixed). New coverage
found while running is cached under "go env GOCACHE"\fuzz (one directory per target) and is not committed;
scripts/fuzz-cache.ps1 keeps those directories in the Actions cache, one key per target, for the Fuzz workflow.

Long mode (-Minutes N, for the dev machine, not CI): first re-runs every selected target against its committed
testdata\fuzz reproducers and the seed corpus (a failure there is a regression and is reported as REPRO), then fuzzes each
target for N minutes with -parallel -Parallel (default half the cores, so the other services keep CPU). It prints one
verdict line per target (PASS, FAIL or REPRO), keeps going after a failure, and exits 1 if any failed. The Go build and fuzz
caches live in .hunt\fuzz\gocache, so found coverage persists between runs (the first run builds cold), and each new
failing input is also copied to .hunt\fuzz\crashers\<Target>\. .hunt\ is gitignored. -Minutes cannot be combined with -Seconds.
#>
[CmdletBinding()]
param(
  [int]$Seconds = 60,
  [string]$Filter = '',
  [switch]$List,
  [ValidatePattern('^$|^[1-9]\d*/[1-9]\d*$')][string]$Shard = '',
  [switch]$ContinueOnFailure,
  [ValidateRange(0, 1440)][int]$Minutes = 0,
  [ValidateRange(0, 1024)][int]$Parallel = 0,
  [switch]$Release,
  [string]$TargetFile = ''
)

# Get-FuzzBudget: the one list of per-target budgets. Weight multiplies -Seconds (default 1); Seconds is a fixed budget that
# ignores -Seconds; ReleaseOnly keeps the target out of every run unless -Release is given. Long mode (-Minutes) ignores the
# weights and Seconds but still honours ReleaseOnly.
function Get-FuzzBudget {
  [CmdletBinding()]
  param()
  @{
    FuzzFolderOps              = @{ Weight = 3 }
    FuzzCheckRestoreStructured = @{ Weight = 3 }
    FuzzCheckRestoreRaw        = @{ Weight = 2 }
    FuzzParseFeedStructured    = @{ Weight = 2 }
    FuzzAPISequence            = @{ Seconds = 300; ReleaseOnly = $true }
  }
}

# Get-FuzzBudgetSecond: how long one target runs in a short run.
function Get-FuzzBudgetSecond {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Target, [Parameter(Mandatory)][int]$Seconds)
  $b = (Get-FuzzBudget)[$Target.Name]
  if ($b -and $b.Seconds) { return [int]$b.Seconds }
  if ($b -and $b.Weight) { return $Seconds * [int]$b.Weight }
  $Seconds
}

# Select-FuzzTarget: the targets a run covers. Release-only targets go first (unless -Release), so they never shift the
# shards of a normal run; then -Filter, then -Shard ("2/4" = every fourth target of the sorted list,
# starting at the second), so the two combine the way they read. The slices are disjoint and differ in size by at most one;
# a target moves to another slice when targets before it are added or removed, and its cached corpus then starts again once.
function Select-FuzzTarget {
  [CmdletBinding()]
  param([object[]]$Target, [string]$Filter = '', [string]$Shard = '', [switch]$Release)
  $picked = @($Target)
  if (-not $Release) {
    $budget = Get-FuzzBudget
    $picked = @($picked | Where-Object { -not $budget[$_.Name].ReleaseOnly })
  }
  if ($Filter) { $picked = @($picked | Where-Object { "$($_.Package) $($_.Name)" -like "*$Filter*" }) }
  if ($Shard) {
    $index, $count = [int[]]($Shard -split '/')
    if ($index -gt $count) { throw "-Shard ${Shard}: the index is above the count" }
    $sorted = @($picked | Sort-Object Package, Name)
    $picked = @(for ($i = $index - 1; $i -lt $sorted.Count; $i += $count) { $sorted[$i] })
  }
  $picked
}

# Get-FuzzTargetList: every fuzz target of the module (package and name), unsorted.
function Get-FuzzTargetList {
  [CmdletBinding()]
  param()
  foreach ($pkg in (go list ./...)) {
    foreach ($line in (go test -list 'Fuzz.*' $pkg 2>$null)) {
      if ($line -match '^Fuzz\w+$') { [pscustomobject]@{ Package = $pkg; Name = $line } }
    }
  }
}

# Read-FuzzTargetFile: the targets of a fuzz-cache.ps1 plan file (package and name), in the file's order.
function Read-FuzzTargetFile {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) { throw "-TargetFile ${Path}: no such file" }
  $rows = @(Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json)
  if ($rows.Count -eq 0) { throw "-TargetFile ${Path}: no targets" }
  $rows | ForEach-Object { [pscustomobject]@{ Package = $_.Package; Name = $_.Name } }
}

# Get-FuzzParallel: the -parallel value of a long run. 0 (not given) means half the cores, at least 1; a given value is
# capped at the core count.
function Get-FuzzParallel {
  [CmdletBinding()]
  param([int]$Requested = 0, [int]$Cores = [Environment]::ProcessorCount)
  if ($Requested -le 0) { return [Math]::Max(1, [int][Math]::Floor($Cores / 2)) }
  [Math]::Min($Requested, [Math]::Max(1, $Cores))
}

# Get-FuzzArgument: the arguments of one `go test` fuzz invocation for a target. A short minimize time keeps a crash from
# spending a minute shrinking its input after the budget is spent.
function Get-FuzzArgument {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Target, [Parameter(Mandatory)][string]$FuzzTime, [int]$ParallelCount = 0)
  $a = @('test', '-run', '^$', '-fuzz', "^$($Target.Name)$", '-fuzztime', $FuzzTime, '-fuzzminimizetime', '10s')
  if ($ParallelCount -gt 0) { $a += @('-parallel', "$ParallelCount") }
  $a + $Target.Package
}

# Format-FuzzVerdict: the one line a long run prints per target.
function Format-FuzzVerdict {
  [CmdletBinding()]
  param([ValidateSet('PASS', 'FAIL', 'REPRO')][string]$Result, [object]$Target, [string]$Detail = '')
  $line = '{0,-5} {1,-34} {2}' -f $Result, $Target.Name, ($Target.Package -replace '^.*/kipple/', '')
  if ($Detail) { $line += "  $Detail" }
  $line
}

# Get-FuzzProgress: the executions and new interesting inputs of a target, from the last progress line go printed
# ("fuzz: elapsed: 3s, execs: 1200 (400/sec), new interesting: 2 (total: 90)"). Null values when go printed none.
function Get-FuzzProgress {
  [CmdletBinding()]
  param([string[]]$Log)
  $line = @($Log | Where-Object { $_ -match 'execs: \d+.*new interesting: \d+' }) | Select-Object -Last 1
  if ($line -match 'execs: (\d+).*new interesting: (\d+)') { return [pscustomobject]@{ Execs = [long]$Matches[1]; New = [long]$Matches[2] } }
  [pscustomobject]@{ Execs = $null; New = $null }
}

# Get-FuzzSummaryRow: one finished target of a short run.
function Get-FuzzSummaryRow {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Target, [int]$Seconds, [string[]]$Log, [Parameter(Mandatory)][ValidateSet('PASS', 'FAIL')][string]$Result)
  $p = Get-FuzzProgress -Log $Log
  [pscustomobject]@{ Target = $Target.Name; Seconds = $Seconds; Execs = $p.Execs; New = $p.New; Result = $Result }
}

# Format-FuzzSummaryRow: the line printed when a target finishes, in the columns of Format-FuzzSummaryTable.
function Format-FuzzSummaryRow {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Row)
  '{0,-34} {1,5} {2,12} {3,6}  {4}' -f $Row.Target, $Row.Seconds, ($Row.Execs ?? '-'), ($Row.New ?? '-'), $Row.Result
}

# Format-FuzzSummaryTable: the table of a whole run, as plain text lines or (-Markdown) a markdown table.
function Format-FuzzSummaryTable {
  [CmdletBinding()]
  param([object[]]$Row, [switch]$Markdown)
  if ($Markdown) {
    '| Target | Seconds | Execs | New interesting | Result |'
    '| --- | ---: | ---: | ---: | --- |'
    foreach ($r in $Row) { '| {0} | {1} | {2} | {3} | {4} |' -f $r.Target, $r.Seconds, ($r.Execs ?? '-'), ($r.New ?? '-'), $r.Result }
    return
  }
  '{0,-34} {1,5} {2,12} {3,6}  {4}' -f 'Target', 'Secs', 'Execs', 'New', 'Result'
  foreach ($r in $Row) { Format-FuzzSummaryRow -Row $r }
}

# Invoke-FuzzLong: the long mode. Returns the number of targets that failed.
function Invoke-FuzzLong {
  [CmdletBinding()]
  param([object[]]$Target, [int]$Minutes, [int]$ParallelCount, [string]$Root)
  $dir = Join-Path $Root '.hunt/fuzz'
  $crashers = Join-Path $dir 'crashers'
  New-Item -ItemType Directory -Force (Join-Path $dir 'gocache') | Out-Null
  $oldCache = $env:GOCACHE
  $env:GOCACHE = (Resolve-Path (Join-Path $dir 'gocache')).Path
  $failed = 0
  try {
    Write-Host "Long mode: $Minutes min per target, -parallel $ParallelCount, cache $env:GOCACHE"
    # 1. The committed reproducers and seeds: plain test mode runs every testdata\fuzz file of the target.
    $broken = @{}
    foreach ($t in $Target) {
      $out = & go test -count=1 -run "^$($t.Name)$" $t.Package 2>&1
      if ($LASTEXITCODE -ne 0) {
        $broken[$t.Name] = $true
        $failed++
        $first = ($out | Where-Object { $_ -match '^\s*(---|panic|FAIL|.*_test\.go)' } | Select-Object -First 1)
        Write-Host (Format-FuzzVerdict -Result REPRO -Target $t -Detail "committed reproducer or seed fails: $first")
      }
    }
    # 2. Fuzz each target for N minutes.
    foreach ($t in $Target) {
      $args2 = Get-FuzzArgument -Target $t -FuzzTime "${Minutes}m" -ParallelCount $ParallelCount
      & go @args2 2>&1 | Tee-Object -Variable teeLog | Out-Null
      $code = $LASTEXITCODE
      $execs = ($teeLog | Where-Object { $_ -match 'execs: (\d+)' } | Select-Object -Last 1)
      $detail = if ($execs -match 'elapsed: (\S+), execs: (\d+).*total: (\d+)') { "$($Matches[1]), $($Matches[2]) execs, $($Matches[3]) corpus" } else { '' }
      if ($code -eq 0) {
        Write-Host (Format-FuzzVerdict -Result PASS -Target $t -Detail $detail)
        continue
      }
      $failed++
      $failLine = ($teeLog | Select-String 'Failing input written to (.+)$' | Select-Object -First 1)
      $where = if ($failLine) { $failLine.Matches[0].Groups[1].Value.Trim() } else { '' }
      if ($where -and (Test-Path $where)) {
        $to = Join-Path $crashers $t.Name
        New-Item -ItemType Directory -Force $to | Out-Null
        Copy-Item $where $to -Force
      }
      Write-Host (Format-FuzzVerdict -Result FAIL -Target $t -Detail "input: $where")
    }
  } finally {
    if ($null -eq $oldCache) { Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue } else { $env:GOCACHE = $oldCache }
  }
  $failed
}

# Dot-sourced (the tests do), the script only defines the functions above.
if ($MyInvocation.InvocationName -ne '.') {
  $ErrorActionPreference = 'Stop'
  Set-Location (Split-Path -Parent $PSScriptRoot)

  if ($Minutes -gt 0 -and $PSBoundParameters.ContainsKey('Seconds')) { throw '-Minutes (long mode) and -Seconds cannot be combined' }
  if ($Parallel -gt 0 -and $Minutes -eq 0) { throw '-Parallel only applies to long mode (-Minutes)' }
  # A bad -Shard is refused before the (slow) target list is built.
  if ($Shard) { $null = @(Select-FuzzTarget -Target @() -Shard $Shard) }

  if ($TargetFile -and ($Shard -or $Filter -or $List -or $Release)) { throw '-TargetFile already names the targets: it cannot be combined with -Shard, -Filter, -List or -Release' }
  $targets = if ($TargetFile) { @(Read-FuzzTargetFile -Path $TargetFile) } else { @(Select-FuzzTarget -Target @(Get-FuzzTargetList) -Filter $Filter -Shard $Shard -Release:$Release) }

  $each = if ($Minutes -gt 0) { "$Minutes min" } else { "$Seconds s (some targets longer)" }
  Write-Host "$($targets.Count) fuzz targets, $each each:"
  $targets | ForEach-Object {
    $own = Get-FuzzBudgetSecond -Target $_ -Seconds $Seconds
    $note = if ($Minutes -eq 0 -and $own -ne $Seconds) { "  $own s" } else { '' }
    Write-Host ("  {0,-28} {1}{2}" -f $_.Name, ($_.Package -replace '^.*/kipple/', ''), $note)
  }
  if ($List) { return }
  # A run that selects nothing must not look like a clean one.
  if ($targets.Count -eq 0) { throw "no fuzz targets selected (filter '$Filter', shard '$Shard')" }
  if ($Minutes -gt 0) {
    $bad = Invoke-FuzzLong -Target $targets -Minutes $Minutes -ParallelCount (Get-FuzzParallel -Requested $Parallel) -Root (Get-Location).Path
    if ($bad -gt 0) { Write-Host "`n$bad of $($targets.Count) fuzz targets failed." -ForegroundColor Red; exit 1 }
    Write-Host "`nAll $($targets.Count) fuzz targets clean." -ForegroundColor Green
    return
  }
  Write-Host "Fuzz cache: $(go env GOCACHE)\fuzz"

  $rows = @()
  $failed = @()
  foreach ($t in $targets) {
    $budget = Get-FuzzBudgetSecond -Target $t -Seconds $Seconds
    Write-Host "`n=== $($t.Name) ($($t.Package)), $budget s"
    # go's progress lines (one every 3 s) stay out of the log; the summary row carries the last execs and new-interesting counts.
    $log = @(& go @(Get-FuzzArgument -Target $t -FuzzTime "${budget}s") 2>&1 | ForEach-Object { "$_" })
    $ok = $LASTEXITCODE -eq 0
    $row = Get-FuzzSummaryRow -Target $t -Seconds $budget -Log $log -Result ($ok ? 'PASS' : 'FAIL')
    $rows += $row
    Write-Host (Format-FuzzSummaryRow -Row $row)
    if (-not $ok) {
      Write-Host "`nFAILED: $($t.Name)" -ForegroundColor Red
      $log | Select-Object -Last 30 | ForEach-Object { Write-Host $_ }
      $log | Select-String 'Failing input written to (.+)$' | ForEach-Object { Write-Host "Failing input: $($_.Matches[0].Groups[1].Value)" -ForegroundColor Red }
      $failed += $t.Name
      if (-not $ContinueOnFailure) { break }
    }
  }
  Write-Host ''
  Format-FuzzSummaryTable -Row $rows | ForEach-Object { Write-Host $_ }
  if ($env:GITHUB_STEP_SUMMARY) {
    $title = if ($Shard) { "### Fuzz results, shard $Shard" } else { '### Fuzz results' }
    (@($title, '') + @(Format-FuzzSummaryTable -Row $rows -Markdown) + @('')) | Add-Content -Path $env:GITHUB_STEP_SUMMARY
  }
  if ($failed) {
    Write-Host "`n$($failed.Count) of $($targets.Count) fuzz targets failed: $($failed -join ', ')" -ForegroundColor Red
    exit 1
  }
  Write-Host "`nAll $($targets.Count) fuzz targets clean." -ForegroundColor Green
}
