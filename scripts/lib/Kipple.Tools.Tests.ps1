#requires -Version 7
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/lib/Kipple.Tools.ps1. Run: Invoke-Pester scripts/lib/Kipple.Tools.Tests.ps1
# Each test is Arrange / Act / Assert and names the behavior it protects, so a repair can be checked against it.

BeforeAll {
  . (Join-Path $PSScriptRoot 'Kipple.Tools.ps1')
  $script:pwshExe = (Get-Process -Id $PID).Path
}

Describe 'Format-CommandLine' {
  It 'quotes an argument that contains a space' {
    $line = Format-CommandLine -FilePath git -Arguments 'worktree', 'add', 'a b\c'
    $line | Should -Be 'git worktree add "a b\c"'
  }
  It 'leaves plain arguments alone' {
    Format-CommandLine -FilePath go -Arguments 'test', './...' | Should -Be 'go test ./...'
  }
}

Describe 'Invoke-Native' {
  It 'returns the output and exit code of a command that succeeds' {
    $r = Invoke-Native -FilePath $script:pwshExe -Arguments '-NoProfile', '-Command', 'Write-Output hello' -Step 'say hello'
    $r.ExitCode | Should -Be 0
    $r.Output | Should -Contain 'hello'
  }
  It 'throws an error naming the step, the command, the exit code and the fix when the command fails' {
    $call = { Invoke-Native -FilePath $script:pwshExe -Arguments '-NoProfile', '-Command', 'exit 3' -Step 'make it fail' -Fix 'do the thing' }
    $call | Should -Throw -ExpectedMessage "*Step 'make it fail' failed*exited 3*Likely fix: do the thing*"
  }
  It 'returns the exit code instead of throwing when failure is allowed' {
    $r = Invoke-Native -FilePath $script:pwshExe -Arguments '-NoProfile', '-Command', 'exit 4' -Step 'allowed' -AllowFailure
    $r.ExitCode | Should -Be 4
  }
  It 'throws a clear error when the executable does not exist' {
    $call = { Invoke-Native -FilePath 'kipple-no-such-tool' -Step 'start a missing tool' }
    $call | Should -Throw -ExpectedMessage "*Step 'start a missing tool' could not start*"
  }
  It 'writes the full output to the log file when asked' {
    $log = Join-Path $TestDrive 'out.log'
    $null = Invoke-Native -FilePath $script:pwshExe -Arguments '-NoProfile', '-Command', 'Write-Output one; Write-Output two' -Step 'log' -LogPath $log
    (Get-Content -LiteralPath $log) | Should -Be @('one', 'two')
  }
  It 'runs in the working directory and restores the caller location' {
    $before = (Get-Location).Path
    $dir = New-Item -ItemType Directory -Path (Join-Path $TestDrive 'a b')
    $r = Invoke-Native -FilePath $script:pwshExe -Arguments '-NoProfile', '-Command', '(Get-Location).Path' -Step 'where' -WorkingDirectory $dir.FullName
    $r.Output[0] | Should -Be $dir.FullName
    (Get-Location).Path | Should -Be $before
  }
}

Describe 'Test-FullSha' {
  It 'accepts 40 hex characters' { Test-FullSha ('a' * 40) | Should -BeTrue }
  It 'rejects an abbreviated sha' { Test-FullSha 'abc1234' | Should -BeFalse }
  It 'rejects 40 characters that are not all hex' { Test-FullSha (('a' * 39) + 'g') | Should -BeFalse }
}

Describe 'Get-GoFailure' {
  It 'finds the failing package and test in go test output' {
    $text = @'
--- FAIL: TestRegexWorstCaseCeiling (2.40s)
    bench_test.go:50: took 2.4s
FAIL
FAIL	example.com/kipple/internal/filter	3.1s
ok  	example.com/kipple/internal/store	1.2s
'@
    $f = Get-GoFailure -Text $text
    $f.Packages | Should -Be @('example.com/kipple/internal/filter')
    $f.Tests | Should -Be @('TestRegexWorstCaseCeiling')
  }
  It 'returns empty lists for a clean run' {
    $f = Get-GoFailure -Text 'ok  	example.com/kipple	0.1s'
    $f.Packages.Count | Should -Be 0
    $f.Tests.Count | Should -Be 0
  }
}

Describe 'Find-Cosign' {
  It 'finds cosign in a WinGet-style package folder when it is not on PATH' {
    Mock Get-Command { $null } -ParameterFilter { $Name -eq 'cosign' }
    $pkg = New-Item -ItemType Directory -Path (Join-Path $TestDrive 'Sigstore.Cosign_x')
    $exe = New-Item -ItemType File -Path (Join-Path $pkg.FullName 'cosign-windows-amd64.exe')
    Find-Cosign -WinGetRoot $TestDrive | Should -Be $exe.FullName
  }
  It 'throws with an install hint when cosign is nowhere' {
    Mock Get-Command { $null } -ParameterFilter { $Name -eq 'cosign' }
    { Find-Cosign -WinGetRoot (Join-Path $TestDrive 'nothing') } | Should -Throw -ExpectedMessage '*winget install Sigstore.Cosign*'
  }
}

Describe 'Resolve-CommitSha' {
  It 'returns a lower-case full sha for a ref git resolves' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @(('A' * 40)); CommandLine = 'git' } }
    Resolve-CommitSha -Ref main -RepoRoot 'x' | Should -Be ('a' * 40)
  }
  It 'throws when git cannot resolve the ref' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 1; Output = @(''); CommandLine = 'git' } }
    { Resolve-CommitSha -Ref nope -RepoRoot 'x' } | Should -Throw -ExpectedMessage "*'nope' is not a commit*"
  }
}

Describe 'Get-RepoSlug' {
  It 'returns owner/name from gh' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @('some-owner/some-repo'); CommandLine = 'gh' } }
    Get-RepoSlug | Should -Be 'some-owner/some-repo'
  }
  It 'throws when gh prints something that is not owner/name' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @('garbage'); CommandLine = 'gh' } }
    { Get-RepoSlug } | Should -Throw -ExpectedMessage '*not owner/name*'
  }
}

Describe 'Invoke-ToolMain' {
  # It calls exit, so each case runs a child pwsh that dot-sources the library.
  BeforeAll {
    $script:lib = Join-Path $PSScriptRoot 'Kipple.Tools.ps1'
    function script:Invoke-Child([string]$Body) {
      $cmd = ". '$script:lib'; Invoke-ToolMain -Name 't' -Body { $Body }"
      $out = & $script:pwshExe -NoProfile -Command $cmd 2>&1
      [pscustomobject]@{ Code = $LASTEXITCODE; Out = ($out -join "`n") }
    }
  }
  It 'exits with the last integer the body emits' {
    (Invoke-Child 'Write-Output "row"; 1').Code | Should -Be 1
  }
  It 'exits 0 when the body emits no integer' {
    (Invoke-Child '"row"').Code | Should -Be 0
  }
  It 'exits 2 and prints the script name and message when the body throws' {
    $r = Invoke-Child 'throw "boom"'
    $r.Code | Should -Be 2
    $r.Out | Should -Match 't: boom'
  }
}

