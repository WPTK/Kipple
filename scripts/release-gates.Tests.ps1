#requires -Version 7
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/release-gates.ps1. Run: Invoke-Pester scripts/release-gates.Tests.ps1
# The script is dot-sourced (it only defines functions then) and native commands are mocked: nothing real runs.

BeforeAll {
  $script:script = Join-Path $PSScriptRoot 'release-gates.ps1'
  . $script:script
  $script:pwshExe = (Get-Process -Id $PID).Path
}

Describe 'argument validation' {
  It 'rejects an abbreviated sha before doing anything' {
    $out = & $script:pwshExe -NoProfile -File $script:script -Ref abc1234 -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'does not match'
  }
  It 'rejects an unknown step name in -Only' {
    $null = & $script:pwshExe -NoProfile -File $script:script -Only bogus -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
}

Describe 'Get-GatePlan' {
  It 'lists every step in the documented order by default' {
    (Get-GatePlan).Key | Should -Be @('go', 'go', 'fuzz', 'web', 'changelog', 'node')
  }
  It 'leaves out fuzz with -SkipFuzz' {
    (Get-GatePlan -SkipFuzz).Key | Should -Not -Contain 'fuzz'
  }
  It 'keeps only the named steps with -Only' {
    (Get-GatePlan -Only web, node).Key | Should -Be @('web', 'node')
  }
}

Describe 'Get-GateOutcome' {
  It 'calls a package that failed in the full run and passed alone FLAKY' {
    $o = Get-GateOutcome -Packages @('pkg/a') -AlonePassed @{ 'pkg/a' = $true }
    $o.Status | Should -Be 'FLAKY'
    $o.Note | Should -Match 'failed in the full run, passed alone'
  }
  It 'calls a package that also fails alone a real FAIL' {
    $o = Get-GateOutcome -Packages @('pkg/a') -AlonePassed @{ 'pkg/a' = $false }
    $o.Status | Should -Be 'FAIL'
    $o.Note | Should -Match 'and alone'
  }
  It 'fails when one of several packages fails alone' {
    $o = Get-GateOutcome -Packages @('pkg/a', 'pkg/b') -AlonePassed @{ 'pkg/a' = $true; 'pkg/b' = $false }
    $o.Status | Should -Be 'FAIL'
  }
  It 'fails when no failing package could be found' {
    (Get-GateOutcome -Packages @()).Status | Should -Be 'FAIL'
  }
}

Describe 'Invoke-GoGate' {
  BeforeEach { Mock Show-LogTail {} }
  It 'passes when go test exits 0' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @('ok'); CommandLine = 'go' } }
    (Invoke-GoGate -Work 'w' -Logs $TestDrive -Run 1).Status | Should -Be 'PASS'
  }
  It 'reports FLAKY and re-runs only the failing package when it then passes alone' {
    $script:calls = 0
    Mock Invoke-Native {
      $script:calls++
      if ($script:calls -eq 1) { return [pscustomobject]@{ ExitCode = 1; Output = @('--- FAIL: TestSlow (2.4s)', "FAIL`texample.com/p/filter`t3s"); CommandLine = 'go' } }
      return [pscustomobject]@{ ExitCode = 0; Output = @('ok'); CommandLine = 'go' }
    }
    $r = Invoke-GoGate -Work 'w' -Logs $TestDrive -Run 1
    $r.Status | Should -Be 'FLAKY'
    $r.Note | Should -Match 'example.com/p/filter'
    $r.Note | Should -Match 'TestSlow'
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $Arguments -contains 'example.com/p/filter' }
  }
  It 'reports FAIL when the package also fails alone' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 1; Output = @("FAIL`texample.com/p/filter`t3s"); CommandLine = 'go' } }
    (Invoke-GoGate -Work 'w' -Logs $TestDrive -Run 2).Status | Should -Be 'FAIL'
  }
}

Describe 'Invoke-GateStep' {
  It 'refuses to run a step while another heavy step holds the lock' {
    # A Mutex is owned by its thread, so hold it on another thread for the duration of the call.
    $ps = [powershell]::Create()
    $null = $ps.AddScript({
        param($name)
        $m = [System.Threading.Mutex]::new($false, $name)
        $null = $m.WaitOne()
        Start-Sleep -Seconds 3
        $m.ReleaseMutex()
      }).AddArgument((Get-GateLockName))
    $handle = $ps.BeginInvoke()
    Start-Sleep -Milliseconds 700
    try {
      Mock Invoke-WebGate { [pscustomobject]@{ Status = 'PASS'; Note = '' } }
      $row = Invoke-GateStep -Name 'web' -Gate 'Invoke-WebGate' -GateArgs @{}
      $row.Status | Should -Be 'FAIL'
      $row.Note | Should -Match 'refused'
      Should -Invoke Invoke-WebGate -Times 0
    } finally {
      $null = $ps.EndInvoke($handle)
      $ps.Dispose()
    }
  }
  It 'turns an exception in a gate into a FAIL row with the message' {
    function Invoke-BoomGate { throw 'it broke' }
    $row = Invoke-GateStep -Name 'boom' -Gate 'Invoke-BoomGate'
    $row.Status | Should -Be 'FAIL'
    $row.Note | Should -Be 'it broke'
  }
}

