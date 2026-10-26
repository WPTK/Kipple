#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for the target selection, budgets and summary of scripts/fuzz.ps1. Run: Invoke-Pester scripts/fuzz.Tests.ps1
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

Describe 'budgets and release-only targets' {
  BeforeAll {
    $script:withApi = @($script:targets) + [pscustomobject]@{ Package = 'example/api'; Name = 'FuzzAPISequence' }
  }
  It 'leaves the release-only target out unless -Release is given' {
    @(Select-FuzzTarget -Target $script:withApi | Where-Object Name -eq 'FuzzAPISequence').Count | Should -Be 0
    @(Select-FuzzTarget -Target $script:withApi -Release | Where-Object Name -eq 'FuzzAPISequence').Count | Should -Be 1
  }
  It 'drops it before sharding, so a normal run has the same slices with or without it in the list' {
    $a = Get-NameList (Select-FuzzTarget -Target $script:withApi -Shard '2/4')
    $b = Get-NameList (Select-FuzzTarget -Target $script:targets -Shard '2/4')
    $a | Should -Be $b
  }
  It 'multiplies -Seconds by a slow target''s weight, gives the release-only target a fixed budget, and leaves others alone' {
    Get-FuzzBudgetSecond -Target ([pscustomobject]@{ Name = 'FuzzFolderOps' }) -Seconds 60 | Should -Be 180
    Get-FuzzBudgetSecond -Target ([pscustomobject]@{ Name = 'FuzzCheckRestoreRaw' }) -Seconds 60 | Should -Be 120
    Get-FuzzBudgetSecond -Target ([pscustomobject]@{ Name = 'FuzzAPISequence' }) -Seconds 60 | Should -Be 300
    Get-FuzzBudgetSecond -Target ([pscustomobject]@{ Name = 'FuzzOther' }) -Seconds 60 | Should -Be 60
  }
}

Describe 'summary' {
  It 'reads the last execs and new-interesting counts from go''s progress lines' {
    $log = @('fuzz: elapsed: 0s, gathering baseline coverage: 0/5 completed', 'fuzz: elapsed: 3s, execs: 1200 (400/sec), new interesting: 2 (total: 90)', 'fuzz: elapsed: 6s, execs: 5000 (600/sec), new interesting: 7 (total: 95)', 'PASS')
    $p = Get-FuzzProgress -Log $log
    $p.Execs | Should -Be 5000
    $p.New | Should -Be 7
  }
  It 'has null counts when go printed no progress' {
    (Get-FuzzProgress -Log @('build failed')).Execs | Should -BeNullOrEmpty
  }
  It 'formats a row and a plain and a markdown table' {
    $t = [pscustomobject]@{ Package = 'example/pkg'; Name = 'FuzzX' }
    $ok = Get-FuzzSummaryRow -Target $t -Seconds 120 -Log @('fuzz: elapsed: 3s, execs: 10 (3/sec), new interesting: 1 (total: 2)') -Result PASS
    $bad = Get-FuzzSummaryRow -Target $t -Seconds 60 -Log @() -Result FAIL
    Format-FuzzSummaryRow -Row $ok | Should -Match '^FuzzX\s+120\s+10\s+1\s+PASS$'
    Format-FuzzSummaryRow -Row $bad | Should -Match '^FuzzX\s+60\s+-\s+-\s+FAIL$'
    @(Format-FuzzSummaryTable -Row $ok, $bad).Count | Should -Be 3
    $md = @(Format-FuzzSummaryTable -Row $ok, $bad -Markdown)
    $md[0] | Should -Be '| Target | Seconds | Execs | New interesting | Result |'
    $md[2] | Should -Be '| FuzzX | 120 | 10 | 1 | PASS |'
    $md[3] | Should -Be '| FuzzX | 60 | - | - | FAIL |'
  }
}

Describe 'Read-FuzzTargetFile' {
  It 'reads package and name from a plan file and refuses a missing or empty one' {
    $f = Join-Path $TestDrive 'plan.json'
    '[{"Slot":1,"Name":"FuzzA","Package":"example/p","Key":"k","Path":"x"},{"Slot":2,"Name":"FuzzB","Package":"example/q","Key":"k","Path":"x"}]' | Set-Content $f
    $t = @(Read-FuzzTargetFile -Path $f)
    $t.Count | Should -Be 2
    "$($t[1].Package) $($t[1].Name)" | Should -Be 'example/q FuzzB'
    { Read-FuzzTargetFile -Path (Join-Path $TestDrive 'nope.json') } | Should -Throw '*no such file*'
    '[]' | Set-Content $f
    { Read-FuzzTargetFile -Path $f } | Should -Throw '*no targets*'
  }
}

Describe 'the script' {
  It 'refuses -TargetFile together with -Shard, -Filter, -List or -Release' {
    foreach ($extra in @('-Shard', '1/2'), @('-Filter', 'x'), @('-List'), @('-Release')) {
      $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -TargetFile x.json @extra 2>&1
      $LASTEXITCODE | Should -Not -Be 0
      ($out -join ' ') | Should -Match 'cannot be combined'
    }
  }
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
    (Get-FuzzArgument -Target $t -FuzzTime '5m' -ParallelCount 4) -join ' ' | Should -Be 'test -run ^$ -fuzz ^FuzzX$ -fuzztime 5m -fuzzminimizetime 10s -parallel 4 example/pkg'
    (Get-FuzzArgument -Target $t -FuzzTime '60s') -join ' ' | Should -Be 'test -run ^$ -fuzz ^FuzzX$ -fuzztime 60s -fuzzminimizetime 10s example/pkg'
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
