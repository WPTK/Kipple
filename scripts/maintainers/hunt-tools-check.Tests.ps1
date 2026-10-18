#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/maintainers/hunt-tools-check.ps1. Run: Invoke-Pester scripts/maintainers/hunt-tools-check.Tests.ps1
# The script is dot-sourced (it only defines functions then). A fake runner stands in for every tool: nothing real is run.

BeforeAll {
  . (Join-Path $PSScriptRoot 'hunt-tools-check.ps1')
  $script:tools = @(
    @{ Name = 'alpha'; Kind = 'cmd'; Command = @('alpha', '-v'); Fix = 'install alpha' }
    @{ Name = 'beta'; Kind = 'cmd'; Command = @('beta', '-v'); Fix = 'install beta' }
    @{ Name = 'nvda'; Kind = 'nvda'; Optional = $true; Fix = 'install nvda' }
    @{ Name = 'cgo-race'; Kind = 'race'; Command = @('go', 'test'); Fix = 'install gcc' }
  )
  function script:New-Runner([string[]]$Absent = @()) {
    $a = $Absent
    { param($Tool) if ($a -contains $Tool.Name) { @{ Ok = $false; Line = '' } } else { @{ Ok = $true; Line = "$($Tool.Name) 1.0" } } }.GetNewClosure()
  }
}

Describe 'Invoke-HuntToolCheck' {
  It 'says OK with the count when every required tool is present (race skipped)' {
    $r = Invoke-HuntToolCheck -Tools $script:tools -Runner (New-Runner)
    $r.Verdict | Should -Be 'OK: 3 tools present'
    $r.Ok | Should -BeTrue
    ($r.Lines -join "`n") | Should -Match 'cgo-race\s+skipped'
  }
  It 'lists every missing tool with its fix on one line' {
    $r = Invoke-HuntToolCheck -Tools $script:tools -Runner (New-Runner -Absent 'alpha', 'beta')
    $r.Verdict | Should -Be 'MISSING: alpha, beta (fix: alpha: install alpha; beta: install beta)'
    $r.Ok | Should -BeFalse
    $r.Verdict | Should -Not -Match "`n"
  }
  It 'does not fail on a missing optional tool' {
    $r = Invoke-HuntToolCheck -Tools $script:tools -Runner (New-Runner -Absent 'nvda')
    $r.Verdict | Should -Be 'OK: 2 tools present'
    ($r.Lines -join "`n") | Should -Match 'nvda\s+not found \(optional\)'
  }
  It 'runs the race check only with -Race' {
    (Invoke-HuntToolCheck -Tools $script:tools -Runner (New-Runner -Absent 'cgo-race') -Race).Verdict | Should -Match '^MISSING: cgo-race'
    (Invoke-HuntToolCheck -Tools $script:tools -Runner (New-Runner -Absent 'cgo-race')).Ok | Should -BeTrue
  }
  It 'gives one detail line per tool with the version' {
    $r = Invoke-HuntToolCheck -Tools $script:tools -Runner (New-Runner)
    $r.Lines.Count | Should -Be 4
    $r.Lines[0] | Should -Match 'alpha\s+alpha 1.0'
  }
}

Describe 'the tool table' {
  It 'has a name, a fix hint and a command on every cmd row' {
    foreach ($t in $script:HuntTools) {
      $t.Name | Should -Not -BeNullOrEmpty
      $t.Fix | Should -Not -BeNullOrEmpty
      if ($t.Kind -eq 'cmd') { $t.Command.Count | Should -BeGreaterThan 0 }
    }
  }
  It 'has unique names' {
    ($script:HuntTools.Name | Select-Object -Unique).Count | Should -Be $script:HuntTools.Count
  }
}

Describe 'Invoke-HuntProbe' {
  It 'reports a path that exists and one that does not' {
    (Invoke-HuntProbe -Tool @{ Kind = 'path'; Path = $PSCommandPath }).Ok | Should -BeTrue
    (Invoke-HuntProbe -Tool @{ Kind = 'path'; Path = (Join-Path $TestDrive 'nope.bat') }).Ok | Should -BeFalse
  }
  It 'reports which Playwright browsers exist' {
    $null = New-Item -ItemType Directory (Join-Path $TestDrive 'chromium-1200')
    $r = Invoke-HuntProbe -Tool @{ Kind = 'browsers'; Path = $TestDrive }
    $r.Ok | Should -BeFalse
    $r.Line | Should -Be 'found: chromium; absent: firefox, webkit'
  }
  It 'treats an unknown command as missing without throwing' {
    (Invoke-HuntProbe -Tool @{ Kind = 'cmd'; Command = @('no-such-tool-xyz', '-v') }).Ok | Should -BeFalse
  }
}
