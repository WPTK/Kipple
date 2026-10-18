#requires -Version 7.2
<#
.SYNOPSIS
  One-line verdict on whether the 1.1 bug-hunt tools are installed.
.DESCRIPTION
  Repeats the tool verification for the 1.1 bug-hunt plan: for each tool in $script:HuntTools it checks that the
  command runs and reads its version. It prints one line:
    OK: N tools present
    MISSING: a, b (fix: <hint for a>; <hint for b>)
  -Detailed first prints one version line per tool. Optional tools (NVDA) are reported but never make the verdict
  MISSING. The cgo check (`go test -race -run TestNothing ./internal/...`) runs only with -Race, because it compiles.
  Playwright browsers are looked up under $env:LOCALAPPDATA\ms-playwright (chromium, firefox, webkit).

  What it can change: nothing. It only runs version commands and tests that paths exist (-Race compiles the
  test binaries into the Go build cache). Exit code 0 when OK, 1 when something is missing.
.PARAMETER Detailed
  Print one version line per tool before the verdict.
.PARAMETER Race
  Also run the cgo / -race check (needs gcc on PATH).
.EXAMPLE
  pwsh scripts/maintainers/hunt-tools-check.ps1 -Detailed
.NOTES
  How to repair: the table $script:HuntTools below is the whole definition; each row has the command that proves the
  tool works and a fix hint. To add a tool, add a row. The logic is Invoke-HuntToolCheck and takes an injectable
  runner, so scripts/maintainers/hunt-tools-check.Tests.ps1 never needs the real tools. Nothing else (workflows,
  ci-local.ps1, release-gates.ps1) uses this script.
#>
param([switch]$Detailed, [switch]$Race)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Kind: cmd (run Command, first output line is the version), path (Path must exist), java (java on PATH or JAVA_HOME),
# browsers (Path holds chromium*, firefox*, webkit* folders), nvda (process or install path), race (go test -race, only with -Race).
$script:HuntTools = @(
  @{ Name = 'gcc'; Kind = 'cmd'; Command = @('gcc', '--version'); Fix = 'install MinGW-w64 (winget install BrechtSanders.WinLibs.POSIX.UCRT) for cgo and -race' }
  @{ Name = 'go'; Kind = 'cmd'; Command = @('go', 'version'); Fix = 'winget install GoLang.Go' }
  @{ Name = 'staticcheck'; Kind = 'cmd'; Command = @('staticcheck', '-version'); Fix = 'go install honnef.co/go/tools/cmd/staticcheck@latest' }
  @{ Name = 'gosec'; Kind = 'cmd'; Command = @('gosec', '-version'); Fix = 'go install github.com/securego/gosec/v2/cmd/gosec@latest' }
  @{ Name = 'golangci-lint'; Kind = 'cmd'; Command = @('golangci-lint', '--version'); Fix = 'winget install GolangCI.golangci-lint' }
  @{ Name = 'govulncheck'; Kind = 'cmd'; Command = @('govulncheck', '-version'); Fix = 'go install golang.org/x/vuln/cmd/govulncheck@latest' }
  @{ Name = 'sqlite3'; Kind = 'cmd'; Command = @('sqlite3', '--version'); Fix = 'winget install SQLite.SQLite' }
  @{ Name = 'k6'; Kind = 'cmd'; Command = @('k6', 'version'); Fix = 'winget install k6.k6' }
  @{ Name = 'benchstat'; Kind = 'cmd'; Command = @('benchstat', '-h'); Fix = 'go install golang.org/x/perf/cmd/benchstat@latest' }
  @{ Name = 'nuclei'; Kind = 'cmd'; Command = @('nuclei', '-version'); Fix = 'go install github.com/projectdiscovery/nuclei/v3/cmd/nuclei@latest' }
  @{ Name = 'toxiproxy-server'; Kind = 'cmd'; Command = @('toxiproxy-server', '-version'); Fix = 'download toxiproxy-server from github.com/Shopify/toxiproxy/releases onto PATH' }
  @{ Name = 'vegeta'; Kind = 'cmd'; Command = @('vegeta', '-version'); Fix = 'go install github.com/tsenart/vegeta/v12@latest' }
  @{ Name = 'gremlins'; Kind = 'cmd'; Command = @('gremlins', '--version'); Fix = 'go install github.com/go-gremlins/gremlins/cmd/gremlins@latest' }
  @{ Name = 'actionlint'; Kind = 'cmd'; Command = @('actionlint', '-version'); Fix = 'go install github.com/rhysd/actionlint/cmd/actionlint@latest' }
  @{ Name = 'playwright'; Kind = 'cmd'; Command = @('npx', 'playwright', '--version'); WorkingDirectory = 'web'; Fix = 'cd web; npm ci' }
  @{ Name = 'playwright-browsers'; Kind = 'browsers'; Path = (Join-Path ($env:LOCALAPPDATA ?? '') 'ms-playwright'); Fix = 'cd web; npx playwright install chromium firefox webkit' }
  @{ Name = 'lhci'; Kind = 'cmd'; Command = @('npx', 'lhci', '--version'); WorkingDirectory = 'web'; Fix = 'npm install -g @lhci/cli' }
  @{ Name = 'lighthouse'; Kind = 'cmd'; Command = @('lighthouse', '--version'); Fix = 'npm install -g lighthouse' }
  @{ Name = 'zap'; Kind = 'path'; Path = 'C:\Program Files\ZAP\Zed Attack Proxy\zap.bat'; Fix = 'winget install ZAP.ZAP' }
  @{ Name = 'java'; Kind = 'java'; Fix = 'winget install EclipseAdoptium.Temurin.21.JRE (ZAP needs it)' }
  @{ Name = 'nvda'; Kind = 'nvda'; Optional = $true; Fix = 'winget install NVAccess.NVDA (screen-reader pass only)' }
  @{ Name = 'cgo-race'; Kind = 'race'; Command = @('go', 'test', '-race', '-run', 'TestNothing', './internal/...'); Fix = 'install gcc (see gcc) and set CGO_ENABLED=1' }
)

function Get-FirstLine {
  param([object[]]$Output = @())
  $line = $Output | ForEach-Object { "$_".Trim() } | Where-Object { $_ } | Select-Object -First 1
  if ($line) { return [string]$line } else { return '' }
}

# The real runner. Takes a tool row, returns @{ Ok; Line }. Never throws.
function Invoke-HuntProbe {
  param([Parameter(Mandatory)][hashtable]$Tool, [string]$RepoRoot = (Split-Path (Split-Path $PSScriptRoot)))
  try {
    switch ($Tool.Kind) {
      'path' { return @{ Ok = (Test-Path -LiteralPath $Tool.Path); Line = $Tool.Path } }
      'java' {
        $j = Get-Command java -ErrorAction SilentlyContinue
        if (-not $j -and $env:JAVA_HOME) { $j = Get-Command (Join-Path $env:JAVA_HOME 'bin/java') -ErrorAction SilentlyContinue }
        if (-not $j) { return @{ Ok = $false; Line = '' } }
        $out = & $j.Source -version 2>&1
        return @{ Ok = $true; Line = (Get-FirstLine $out) }
      }
      'nvda' {
        if (Get-Process -Name nvda -ErrorAction SilentlyContinue) { return @{ Ok = $true; Line = 'running' } }
        foreach ($d in @($env:ProgramFiles, ${env:ProgramFiles(x86)}) | Where-Object { $_ }) {
          $p = Join-Path $d 'NVDA\nvda.exe'
          if (Test-Path -LiteralPath $p) { return @{ Ok = $true; Line = $p } }
        }
        return @{ Ok = $false; Line = '' }
      }
      'browsers' {
        $found = @(); $absent = @()
        foreach ($b in 'chromium', 'firefox', 'webkit') {
          $has = $Tool.Path -and (Test-Path -LiteralPath $Tool.Path) -and (Get-ChildItem -LiteralPath $Tool.Path -Directory -Filter "$b*" -ErrorAction SilentlyContinue)
          if ($has) { $found += $b } else { $absent += $b }
        }
        $line = "found: $($found -join ', ')"
        if ($absent) { $line += "; absent: $($absent -join ', ')" }
        return @{ Ok = ($absent.Count -eq 0); Line = $line }
      }
      default {
        $exe = $Tool.Command[0]
        if (-not (Get-Command $exe -ErrorAction SilentlyContinue)) { return @{ Ok = $false; Line = '' } }
        $wd = if ($Tool.ContainsKey('WorkingDirectory')) { Join-Path $RepoRoot $Tool.WorkingDirectory } else { $RepoRoot }
        Push-Location -LiteralPath $wd
        try {
          $out = & $exe @($Tool.Command | Select-Object -Skip 1) 2>&1
          $code = $LASTEXITCODE
        } finally { Pop-Location }
        # Some tools exit non-zero on -h / -version, so only the race check is judged by its exit code.
        $ok = if ($Tool.Kind -eq 'race') { $code -eq 0 } else { $true }
        return @{ Ok = $ok; Line = (Get-FirstLine $out) }
      }
    }
  } catch {
    return @{ Ok = $false; Line = "error: $($_.Exception.Message)" }
  }
}

function Invoke-HuntToolCheck {
  <#
  .SYNOPSIS
    Runs every tool in the table through $Runner and returns the verdict.
  .OUTPUTS
    A hashtable: Verdict (the one line), Lines (one per tool), Missing (names), Ok (bool).
  #>
  param(
    [hashtable[]]$Tools = $script:HuntTools,
    [scriptblock]$Runner = { param($Tool) Invoke-HuntProbe -Tool $Tool },
    [switch]$Race
  )
  $present = 0; $missing = @(); $fixes = @(); $lines = @()
  foreach ($t in $Tools) {
    if ($t.Kind -eq 'race' -and -not $Race) { $lines += ('{0,-20} skipped (use -Race)' -f $t.Name); continue }
    $r = & $Runner $t
    $optional = $t.ContainsKey('Optional') -and $t.Optional
    if ($r.Ok) {
      $present++
      $lines += ('{0,-20} {1}' -f $t.Name, $r.Line)
    } elseif ($optional) {
      $lines += ('{0,-20} not found (optional)' -f $t.Name)
    } else {
      $missing += $t.Name
      $fixes += "$($t.Name): $($t.Fix)"
      $detail = if ($r.Line) { $r.Line } else { 'NOT FOUND' }
      $lines += ('{0,-20} MISSING ({1})' -f $t.Name, $detail)
    }
  }
  $verdict = if ($missing.Count -eq 0) { "OK: $present tools present" } else { "MISSING: $($missing -join ', ') (fix: $($fixes -join '; '))" }
  return @{ Verdict = $verdict; Lines = $lines; Missing = $missing; Ok = ($missing.Count -eq 0) }
}

if ($MyInvocation.InvocationName -ne '.') {
  $result = Invoke-HuntToolCheck -Race:$Race
  if ($Detailed) { $result.Lines | ForEach-Object { Write-Host $_ } }
  Write-Host $result.Verdict
  exit ($result.Ok ? 0 : 1)
}
