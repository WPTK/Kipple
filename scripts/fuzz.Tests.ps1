#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for the target selection of scripts/fuzz.ps1 (-Filter and -Shard). Run: Invoke-Pester scripts/fuzz.Tests.ps1
# The script is dot-sourced (it only defines Select-FuzzTarget then); no go command runs.

BeforeAll {
  $script:script = Join-Path $PSScriptRoot 'fuzz.ps1'
  . $script:script
  $script:pwshExe = (Get-Process -Id $PID).Path
  # 30 targets over a few packages, deliberately not in sorted order.
  $script:targets = foreach ($i in 30..1) { [pscustomobject]@{ Package = "example/pkg$($i % 7)"; Name = ('FuzzT{0:d2}' -f $i) } }
  function script:Get-NameList($t) { @($t | ForEach-Object { "$($_.Package) $($_.Name)" } | Sort-Object) }
  function script:Merge-Slice($slices) { foreach ($slice in $slices) { foreach ($t in $slice) { $t } } }
}

Describe 'Select-FuzzTarget' {
  It 'splits into disjoint slices that cover every target and differ in size by at most one' {
    foreach ($count in 1, 2, 4, 7) {
      $slices = 1..$count | ForEach-Object { , @(Select-FuzzTarget -Target $script:targets -Shard "$_/$count") }
      $joined = Merge-Slice $slices
      Get-NameList $joined | Should -Be (Get-NameList $script:targets)
      $sizes = $slices | ForEach-Object { $_.Count }
      ($sizes | Measure-Object -Maximum).Maximum - ($sizes | Measure-Object -Minimum).Minimum | Should -BeLessOrEqual 1
    }
  }
  It 'gives a target the same slice whatever order the list arrives in' {
    $a = Get-NameList (Select-FuzzTarget -Target $script:targets -Shard '3/4')
    $b = Get-NameList (Select-FuzzTarget -Target ($script:targets | Sort-Object Name) -Shard '3/4')
    $a | Should -Be $b
  }
  It 'filters before it slices' {
    $filtered = @(Select-FuzzTarget -Target $script:targets -Filter 'pkg3')
    $filtered.Count | Should -BeGreaterThan 1
    $halves = 1..2 | ForEach-Object { , @(Select-FuzzTarget -Target $script:targets -Filter 'pkg3' -Shard "$_/2") }
    Get-NameList (Merge-Slice $halves) | Should -Be (Get-NameList $filtered)
  }
  It 'returns an empty selection, not an error, when a slice has no target' {
    @(Select-FuzzTarget -Target $script:targets[0..1] -Shard '4/4').Count | Should -Be 0
  }
  It 'throws when the index is above the count' {
    { Select-FuzzTarget -Target $script:targets -Shard '5/4' } | Should -Throw '*index is above the count*'
  }
}

Describe 'the script' {
  It 'refuses a malformed -Shard before any work' {
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -List -Shard 'x/4' 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'Shard'
  }
  It 'refuses an index above the count before listing targets' {
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -List -Shard '5/4' 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'index is above the count'
  }
}

Describe 'long mode helpers' {
  It 'defaults -parallel to half the cores, at least one, and caps a request at the cores' {
    Get-FuzzParallel -Cores 16 | Should -Be 8
    Get-FuzzParallel -Cores 1 | Should -Be 1
    Get-FuzzParallel -Cores 3 | Should -Be 1
    Get-FuzzParallel -Requested 4 -Cores 16 | Should -Be 4
    Get-FuzzParallel -Requested 64 -Cores 16 | Should -Be 16
  }
  It 'builds the go test arguments for a target' {
    $t = [pscustomobject]@{ Package = 'example/pkg'; Name = 'FuzzX' }
    (Get-FuzzArgument -Target $t -FuzzTime '5m' -ParallelCount 4) -join ' ' | Should -Be 'test -run ^$ -fuzz ^FuzzX$ -fuzztime 5m -parallel 4 example/pkg'
    (Get-FuzzArgument -Target $t -FuzzTime '60s') -join ' ' | Should -Be 'test -run ^$ -fuzz ^FuzzX$ -fuzztime 60s example/pkg'
  }
  It 'prints one line per verdict with the short package path' {
    $t = [pscustomobject]@{ Package = 'github.com/WPTK/kipple/internal/opml'; Name = 'FuzzParse' }
    $line = Format-FuzzVerdict -Result FAIL -Target $t -Detail 'input: x'
    $line | Should -Match '^FAIL\s+FuzzParse\s+internal/opml\s+input: x$'
    ($line -split "`n").Count | Should -Be 1
  }
  It 'refuses -Minutes together with -Seconds, and -Parallel without -Minutes' {
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -List -Minutes 1 -Seconds 5 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'cannot be combined'
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -List -Parallel 2 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'only applies to long mode'
  }
  # Opt in with KIPPLE_SLOW_TESTS=1: the first run builds a cold Go cache under .hunt/fuzz (minutes).
  It 'runs a real target in long mode, reports PASS, and keeps its cache under .hunt/fuzz' -Tag 'slow' -Skip:(-not $env:KIPPLE_SLOW_TESTS) {
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Minutes 1 -Parallel 1 -Filter FuzzParseStructured 2>&1
    $LASTEXITCODE | Should -Be 0
    ($out -join "`n") | Should -Match '(?m)^PASS\s+FuzzParseStructured'
    Test-Path (Join-Path (Split-Path -Parent $PSScriptRoot) '.hunt/fuzz/gocache/fuzz') | Should -BeTrue
  }
}
