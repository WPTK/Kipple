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
Stops at the first failure (unless -ContinueOnFailure) and prints the failing input file (testdata\fuzz\<Target>\<hash>
inside the package; commit it as a regression seed once the bug is fixed). New coverage
found while running is cached under "go env GOCACHE"\fuzz and is not committed.
#>
[CmdletBinding()]
param(
  [int]$Seconds = 60,
  [string]$Filter = '',
  [switch]$List,
  [ValidatePattern('^$|^[1-9]\d*/[1-9]\d*$')][string]$Shard = '',
  [switch]$ContinueOnFailure
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

# Dot-sourced (the tests do), the script only defines the function above.
if ($MyInvocation.InvocationName -ne '.') {
  $ErrorActionPreference = 'Stop'
  Set-Location (Split-Path -Parent $PSScriptRoot)

  # A bad -Shard is refused before the (slow) target list is built.
  if ($Shard) { $null = @(Select-FuzzTarget -Target @() -Shard $Shard) }

  $all = @()
  foreach ($pkg in (go list ./...)) {
    foreach ($line in (go test -list 'Fuzz.*' $pkg 2>$null)) {
      if ($line -match '^Fuzz\w+$') { $all += [pscustomobject]@{ Package = $pkg; Name = $line } }
    }
  }
  $targets = @(Select-FuzzTarget -Target $all -Filter $Filter -Shard $Shard)

  Write-Host "$($targets.Count) fuzz targets, $Seconds s each:"
  $targets | ForEach-Object { Write-Host ("  {0,-28} {1}" -f $_.Name, ($_.Package -replace '^.*/kipple/', '')) }
  if ($List) { return }
  # A run that selects nothing must not look like a clean one.
  if ($targets.Count -eq 0) { throw "no fuzz targets selected (filter '$Filter', shard '$Shard')" }
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
