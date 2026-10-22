#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for the pure parts of scripts/fuzz-cache.ps1 (keys, corpus paths, size, pruning). Run: Invoke-Pester scripts/fuzz-cache.Tests.ps1
# The script is dot-sourced, so it only defines functions; no go or gh command runs.

BeforeAll {
  . (Join-Path $PSScriptRoot 'fuzz-cache.ps1') -Action Plan
}

Describe 'Get-FuzzCacheKeyPrefix' {
  It 'names the package below the module and ends with a dash' {
    $t = [pscustomobject]@{ Package = 'github.com/WPTK/kipple/internal/opml'; Name = 'FuzzImportExport' }
    Get-FuzzCacheKeyPrefix -Target $t | Should -Be 'fuzz-corpus-internal_opml-FuzzImportExport-'
  }
  It 'handles the module root package' {
    $t = [pscustomobject]@{ Package = 'github.com/WPTK/kipple'; Name = 'FuzzX' }
    Get-FuzzCacheKeyPrefix -Target $t | Should -Be 'fuzz-corpus-root-FuzzX-'
  }
  It 'does not let one target prefix match another whose name starts the same' {
    $a = Get-FuzzCacheKeyPrefix -Target ([pscustomobject]@{ Package = 'github.com/WPTK/kipple/p'; Name = 'FuzzParse' })
    $b = Get-FuzzCacheKeyPrefix -Target ([pscustomobject]@{ Package = 'github.com/WPTK/kipple/p'; Name = 'FuzzParseFeed' })
    "${b}123".StartsWith($a) | Should -BeFalse
  }
  It 'gives the same key whichever shard the target is on' {
    $t = [pscustomobject]@{ Package = 'github.com/WPTK/kipple/p'; Name = 'FuzzA' }
    Get-FuzzCacheKeyPrefix -Target $t | Should -Be (Get-FuzzCacheKeyPrefix -Target $t)
  }
}

Describe 'Get-FuzzCorpusDirectory' {
  It 'follows the layout go test uses under GOCACHE/fuzz' {
    $t = [pscustomobject]@{ Package = 'example/pkg'; Name = 'FuzzX' }
    Get-FuzzCorpusDirectory -Target $t -CacheRoot 'C' | Should -Be (Join-Path (Join-Path (Join-Path 'C' 'fuzz') 'example/pkg') 'FuzzX')
  }
}

Describe 'Get-DirectoryByte' {
  It 'sums files below a directory and is 0 for a missing one' {
    $d = Join-Path $TestDrive 'corpus'
    New-Item -ItemType Directory -Path (Join-Path $d 'sub') | Out-Null
    [IO.File]::WriteAllBytes((Join-Path $d 'a'), (New-Object byte[] 100))
    [IO.File]::WriteAllBytes((Join-Path $d 'sub/b'), (New-Object byte[] 50))
    Get-DirectoryByte -Path $d | Should -Be 150
    Get-DirectoryByte -Path (Join-Path $TestDrive 'missing') | Should -Be 0
  }
}

Describe 'Select-StaleFuzzCache' {
  It 'keeps the newest key of each target and lists the rest' {
    $c = @(
      [pscustomobject]@{ id = 1; key = 'fuzz-corpus-p-FuzzA-100'; createdAt = '2026-10-05T05:30:00Z' }
      [pscustomobject]@{ id = 2; key = 'fuzz-corpus-p-FuzzA-300'; createdAt = '2026-10-19T05:30:00Z' }
      [pscustomobject]@{ id = 3; key = 'fuzz-corpus-p-FuzzA-200'; createdAt = '2026-10-12T05:30:00Z' }
      [pscustomobject]@{ id = 4; key = 'fuzz-corpus-p-FuzzAB-150'; createdAt = '2026-10-10T05:30:00Z' }
      [pscustomobject]@{ id = 5; key = 'setup-go-Linux-abc'; createdAt = '2026-09-01T00:00:00Z' }
    )
    @(Select-StaleFuzzCache -Cache $c | ForEach-Object id | Sort-Object) | Should -Be @(1, 3)
  }
  It 'lists nothing when every target has one key' {
    $c = @([pscustomobject]@{ id = 1; key = 'fuzz-corpus-p-FuzzA-100'; createdAt = '2026-10-05T05:30:00Z' })
    @(Select-StaleFuzzCache -Cache $c).Count | Should -Be 0
  }
}
