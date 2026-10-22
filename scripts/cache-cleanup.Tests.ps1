#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for the selection in scripts/cache-cleanup.ps1. Run: Invoke-Pester scripts/cache-cleanup.Tests.ps1
# The script is dot-sourced, so it only defines a function; no gh command runs.

BeforeAll {
  . (Join-Path $PSScriptRoot 'cache-cleanup.ps1')
  function script:Get-CacheEntry($id, $name, $where, $when) { [pscustomobject]@{ id = $id; key = $name; ref = $where; createdAt = $when } }
}

Describe 'Select-DisposableCache' {
  It 'selects Docker layers under a pull request ref and keeps those on main' {
    $c = @(
      Get-CacheEntry 1 'buildkit-blob-1-sha256:aa' 'refs/pull/9/merge' '2026-10-20T00:00:00Z'
      Get-CacheEntry 2 'buildkit-blob-1-sha256:bb' 'refs/heads/main' '2026-10-20T00:00:00Z'
    )
    @(Select-DisposableCache -Cache $c | ForEach-Object id) | Should -Be @(1)
  }
  It 'keeps the newest CodeQL overlay of each language and selects the older ones' {
    $c = @(
      Get-CacheEntry 1 'codeql-overlay-base-database-1-433570c2-go-2.27.2-aaa-111-1' 'refs/heads/main' '2026-10-01T00:00:00Z'
      Get-CacheEntry 2 'codeql-overlay-base-database-1-433570c2-go-2.27.2-bbb-222-1' 'refs/heads/main' '2026-10-20T00:00:00Z'
      Get-CacheEntry 3 'codeql-overlay-base-database-1-c801913f-javascript-2.27.2-aaa-111-1' 'refs/heads/main' '2026-10-01T00:00:00Z'
      Get-CacheEntry 4 'codeql-overlay-base-database-1-c801913f-javascript-2.27.2-bbb-222-1' 'refs/heads/main' '2026-10-20T00:00:00Z'
    )
    @(Select-DisposableCache -Cache $c | ForEach-Object id | Sort-Object) | Should -Be @(1, 3)
  }
  It 'leaves the build, module and browser caches alone' {
    $c = @(
      Get-CacheEntry 1 'setup-go-Linux-x64-ubuntu24-go-1.27.3-abc' 'refs/heads/main' '2026-10-20T00:00:00Z'
      Get-CacheEntry 2 'playwright-Linux-1.64.0-all' 'refs/heads/main' '2026-10-20T00:00:00Z'
      Get-CacheEntry 3 'fuzz-corpus-p-FuzzA-100' 'refs/heads/main' '2026-10-20T00:00:00Z'
    )
    @(Select-DisposableCache -Cache $c).Count | Should -Be 0
  }
}
