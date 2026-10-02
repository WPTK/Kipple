<#
.SYNOPSIS
  Runs every Go fuzz target for a fixed time each. Run by hand before a release; the weekly Fuzz workflow
  (.github/workflows/fuzz.yml) runs this same script on Linux.
.EXAMPLE
  scripts\fuzz.ps1                     # every target, 60 s each
  scripts\fuzz.ps1 -Seconds 10         # quick smoke
  scripts\fuzz.ps1 -Filter Sanitize    # only targets whose name or package matches
  scripts\fuzz.ps1 -List               # list targets and exit
Stops at the first failure and prints the failing input file (testdata\fuzz\<Target>\<hash>
inside the package; commit it as a regression seed once the bug is fixed). New coverage
found while running is cached under "go env GOCACHE"\fuzz and is not committed.
#>
[CmdletBinding()]
param(
  [int]$Seconds = 60,
  [string]$Filter = '',
  [switch]$List
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)

$targets = @()
foreach ($pkg in (go list ./...)) {
  foreach ($line in (go test -list 'Fuzz.*' $pkg 2>$null)) {
    if ($line -match '^Fuzz\w+$') { $targets += [pscustomobject]@{ Package = $pkg; Name = $line } }
  }
}
if ($Filter) { $targets = $targets | Where-Object { "$($_.Package) $($_.Name)" -like "*$Filter*" } }

Write-Host "$($targets.Count) fuzz targets, $Seconds s each:"
$targets | ForEach-Object { Write-Host ("  {0,-28} {1}" -f $_.Name, ($_.Package -replace '^.*/kipple/', '')) }
if ($List) { return }
Write-Host "Fuzz cache: $(go env GOCACHE)\fuzz"

foreach ($t in $targets) {
  Write-Host "`n=== $($t.Name) ($($t.Package))"
  $out = go test -run '^$' -fuzz "^$($t.Name)$" -fuzztime "${Seconds}s" $t.Package 2>&1 | Tee-Object -Variable log
  if ($LASTEXITCODE -ne 0) {
    Write-Host "`nFAILED: $($t.Name)" -ForegroundColor Red
    $log | Select-String 'Failing input written to (.+)$' | ForEach-Object { Write-Host "Failing input: $($_.Matches[0].Groups[1].Value)" -ForegroundColor Red }
    exit 1
  }
}
Write-Host "`nAll $($targets.Count) fuzz targets clean." -ForegroundColor Green
