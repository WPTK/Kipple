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
  scripts\fuzz.ps1 -ContinueOnFailure  # run every target and report all failures at the end
  scripts\fuzz.ps1 -Minutes 10         # long mode for the dev machine: every target 10 min, half the cores, corpus kept under .hunt\fuzz
  scripts\fuzz.ps1 -Minutes 10 -Parallel 2 -Filter OPML
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
  [ValidateRange(0, 1024)][int]$Parallel = 0
)

# Select-FuzzTarget: the targets a run covers. -Filter first, then -Shard ("2/4" = every fourth target of the sorted list,
# starting at the second), so the two combine the way they read. The slices are disjoint and differ in size by at most one;
# a target moves to another slice when targets before it are added or removed, and its cached corpus then starts again once.
function Select-FuzzTarget {
  [CmdletBinding()]
  param([object[]]$Target, [string]$Filter = '', [string]$Shard = '')
  $picked = @($Target)
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

# Get-FuzzParallel: the -parallel value of a long run. 0 (not given) means half the cores, at least 1; a given value is
# capped at the core count.
function Get-FuzzParallel {
  [CmdletBinding()]
  param([int]$Requested = 0, [int]$Cores = [Environment]::ProcessorCount)
  if ($Requested -le 0) { return [Math]::Max(1, [int][Math]::Floor($Cores / 2)) }
  [Math]::Min($Requested, [Math]::Max(1, $Cores))
}

# Get-FuzzArgument: the arguments of one `go test` fuzz invocation for a target.
function Get-FuzzArgument {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Target, [Parameter(Mandatory)][string]$FuzzTime, [int]$ParallelCount = 0)
  $a = @('test', '-run', '^$', '-fuzz', "^$($Target.Name)$", '-fuzztime', $FuzzTime)
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

  $all = @(Get-FuzzTargetList)
  $targets = @(Select-FuzzTarget -Target $all -Filter $Filter -Shard $Shard)

  $each = if ($Minutes -gt 0) { "$Minutes min" } else { "$Seconds s" }
  Write-Host "$($targets.Count) fuzz targets, $each each:"
  $targets | ForEach-Object { Write-Host ("  {0,-28} {1}" -f $_.Name, ($_.Package -replace '^.*/kipple/', '')) }
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

  $failed = @()
  foreach ($t in $targets) {
    Write-Host "`n=== $($t.Name) ($($t.Package))"
    # Out-Host keeps go's per-target progress (execs, "new interesting") in the log; it shows a target that has stopped finding coverage.
    go test -run '^$' -fuzz "^$($t.Name)$" -fuzztime "${Seconds}s" $t.Package 2>&1 | Tee-Object -Variable log | Out-Host
    if ($LASTEXITCODE -ne 0) {
      Write-Host "`nFAILED: $($t.Name)" -ForegroundColor Red
      $log | Select-String 'Failing input written to (.+)$' | ForEach-Object { Write-Host "Failing input: $($_.Matches[0].Groups[1].Value)" -ForegroundColor Red }
      if (-not $ContinueOnFailure) { exit 1 }
      $failed += $t.Name
    }
  }
  if ($failed) {
    Write-Host "`n$($failed.Count) of $($targets.Count) fuzz targets failed: $($failed -join ', ')" -ForegroundColor Red
    exit 1
  }
  Write-Host "`nAll $($targets.Count) fuzz targets clean." -ForegroundColor Green
}
