#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for the -Shard argument of scripts/fuzz.ps1. Run: Invoke-Pester scripts/fuzz.Tests.ps1
# Only the argument checks, which fail before the target list is built; the list itself (go list, about a minute) is not run here.

BeforeAll {
  $script:script = Join-Path $PSScriptRoot 'fuzz.ps1'
  $script:pwshExe = (Get-Process -Id $PID).Path
}

Describe 'shard argument' {
  It 'rejects a malformed value before any work' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -List -Shard 'x/4' 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
  It 'rejects an index above the count' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -List -Shard '5/4' 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
}
