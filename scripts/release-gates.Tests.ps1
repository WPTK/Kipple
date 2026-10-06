#requires -Version 7.2
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
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Ref abc1234 -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'does not match'
  }
  It 'rejects an unknown step name in -Only' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Only bogus -WhatIf 2>&1
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


Describe 'Invoke-GoGate shuffle seed' {
  BeforeEach { Mock Show-LogTail {} }
  It 're-runs a failing package alone with the same shuffle seed as the full run' {
    $script:n = 0
    Mock Invoke-Native {
      $script:n++
      if ($script:n -eq 1) { return [pscustomobject]@{ ExitCode = 1; Output = @("FAIL`texample.com/p/a`t1s"); CommandLine = 'go' } }
      [pscustomobject]@{ ExitCode = 0; Output = @('ok'); CommandLine = 'go' }
    }
    $r = Invoke-GoGate -Work 'w' -Logs $TestDrive -Run 1 -Seed 4242
    Should -Invoke Invoke-Native -Times 2 -ParameterFilter { $Arguments -contains '-shuffle=4242' }
    $r.Note | Should -Match 'shuffle seed 4242'
  }
  It 'calls an order-dependent failure FAIL when it fails alone under the same seed' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 1; Output = @("FAIL`texample.com/p/a`t1s"); CommandLine = 'go' } }
    (Invoke-GoGate -Work 'w' -Logs $TestDrive -Run 1 -Seed 7).Status | Should -Be 'FAIL'
  }
}

Describe 'Invoke-ReleaseGate cleanup' {
  BeforeEach {
    Mock Write-KippleInfo {}
    Mock Get-RepoRoot { 'C:/repo' }
    Mock Resolve-CommitSha { 'a' * 40 }
    Mock New-DetachedWorktree {}
    Mock Remove-Item {}
  }
  It 'removes the worktree even when a step throws' {
    Mock Invoke-GateStep { throw 'boom' }
    Mock Remove-DetachedWorktree { $true }
    { Invoke-ReleaseGate -Ref ('a' * 40) -Only changelog } | Should -Throw
    Should -Invoke Remove-DetachedWorktree -Times 1
  }
  It 'keeps the logs and says so when the worktree cannot be removed' {
    Mock Invoke-GateStep { [pscustomobject]@{ Step = 'x'; Status = 'PASS'; Seconds = 0; Note = '' } }
    Mock Remove-DetachedWorktree { $false }
    $null = Invoke-ReleaseGate -Ref ('a' * 40) -Only changelog
    Should -Invoke Remove-Item -Times 0
    Should -Invoke Write-KippleInfo -ParameterFilter { $Message -match 'stays at' }
  }
}

Describe 'Invoke-ReleaseGate with a real worktree' {
  # Real git, real worktree: only the gates' own commands fail. The temp repository has no scripts/changelog.mjs, so
  # the changelog step fails in the middle of the run, after the worktree exists.
  BeforeAll {
    $script:repo = Join-Path $TestDrive 'repo'
    $null = New-Item -ItemType Directory -Path $script:repo
    $null = Invoke-Native -FilePath git -Arguments '-C', $script:repo, 'init', '--quiet' -Step 'git init'
    Set-Content -LiteralPath (Join-Path $script:repo 'README.md') -Value 'x'
    $null = Invoke-Native -FilePath git -Arguments '-C', $script:repo, 'add', '-A' -Step 'git add'
    $null = Invoke-Native -FilePath git -Arguments '-C', $script:repo, '-c', 'user.name=t', '-c', 'user.email=t@example.com', 'commit', '--quiet', '-m', 'init' -Step 'git commit'
    $script:sha = (Invoke-Native -FilePath git -Arguments '-C', $script:repo, 'rev-parse', 'HEAD' -Step 'rev-parse').Output[0].Trim()
  }
  BeforeEach {
    Mock Write-KippleInfo {}
    Mock Get-RepoRoot { $script:repo }
  }
  It 'removes the worktree and its logs after a step fails in the middle, and the exit code is 1' {
    $out = @(Invoke-ReleaseGate -Ref $script:sha -Only changelog)
    $out[-1] | Should -Be 1
    $rows = @($out | Where-Object { $_ -isnot [int] })
    $rows.Count | Should -Be 1 -Because ($out | ForEach-Object { $_.GetType().Name } | Out-String)
    $rows[0].Status | Should -Be 'FAIL'
    $left = Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Filter "kipple-gates-$($script:sha.Substring(0, 12))-*" -ErrorAction SilentlyContinue
    @($left).Count | Should -Be 0
    (Invoke-Native -FilePath git -Arguments '-C', $script:repo, 'worktree', 'list' -Step 'list').Output.Count | Should -Be 1
  }
  It 'keeps the worktree when asked to, and reports where' {
    $null = Invoke-ReleaseGate -Ref $script:sha -Only changelog -KeepWorktree
    Should -Invoke Write-KippleInfo -ParameterFilter { $Message -match '^Kept: ' }
    foreach ($d in Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Filter "kipple-gates-$($script:sha.Substring(0, 12))-*") {
      if ($d.Name -like '*-logs') { Microsoft.PowerShell.Management\Remove-Item -LiteralPath $d.FullName -Recurse -Force } else {
        $null = Invoke-Native -FilePath git -Arguments '-C', $script:repo, 'worktree', 'remove', '--force', $d.FullName -Step 'cleanup' -AllowFailure
      }
    }
  }
}
