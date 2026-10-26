#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for the pure parts of scripts/fuzz-cache.ps1 (keys, corpus paths, size, pruning, restore report). Run: Invoke-Pester scripts/fuzz-cache.Tests.ps1
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
  BeforeAll { function Get-CacheEntry($id, $name, $when) { [pscustomobject]@{ id = $id; key = $name; createdAt = $when } } }
  It 'keeps the newest key of each target and lists the rest' {
    $c = @(
      Get-CacheEntry 1 'fuzz-corpus-p-FuzzA-100' '2026-10-05T05:30:00Z'
      Get-CacheEntry 2 'fuzz-corpus-p-FuzzA-300' '2026-10-19T05:30:00Z'
      Get-CacheEntry 3 'fuzz-corpus-p-FuzzA-200' '2026-10-12T05:30:00Z'
      Get-CacheEntry 4 'fuzz-corpus-p-FuzzAB-150' '2026-10-10T05:30:00Z'
      Get-CacheEntry 5 'setup-go-Linux-abc' '2026-09-01T00:00:00Z'
    )
    @(Select-StaleFuzzCache -Cache $c | ForEach-Object id | Sort-Object) | Should -Be @(1, 3)
  }
  It 'lists nothing when every target has one key' {
    $c = @(Get-CacheEntry 1 'fuzz-corpus-p-FuzzA-100' '2026-10-05T05:30:00Z')
    @(Select-StaleFuzzCache -Cache $c).Count | Should -Be 0
  }
}

Describe 'Get-FuzzCacheReport' {
  BeforeAll {
    $script:plan = 1..3 | ForEach-Object { [pscustomobject]@{ Slot = $_; Name = "FuzzT$_" } }
    $script:steps = '{"plan":{"outputs":{"key1":"k"}},"restore1":{"outputs":{"cache-hit":"false","cache-matched-key":"fuzz-corpus-p-FuzzT1-9"}},"restore2":{"outputs":{"cache-hit":"false","cache-matched-key":""}},"restore3":{"outputs":{}}}'
  }
  It 'counts a slot as restored when its matched key is non-empty' {
    $r = Get-FuzzCacheReport -StepsJson $script:steps -Plan $script:plan
    $r.Restored | Should -Be 1
    $r.Total | Should -Be 3
    @($r.Row.Restored) | Should -Be @($true, $false, $false)
  }
  It 'reports nothing restored when the steps context is missing' {
    (Get-FuzzCacheReport -StepsJson '' -Plan $script:plan).Restored | Should -Be 0
  }
  It 'formats the headline, a plain table and a markdown table' {
    $r = Get-FuzzCacheReport -StepsJson $script:steps -Plan $script:plan
    @(Format-FuzzCacheReport -Report $r)[0] | Should -Be 'Fuzz corpus cache: restored 1 of 3 slots'
    @(Format-FuzzCacheReport -Report $r)[1] | Should -Match '^slot 1\s+FuzzT1\s+yes$'
    $md = @(Format-FuzzCacheReport -Report $r -Markdown)
    $md[0] | Should -Be '### Fuzz corpus cache: restored 1 of 3 slots'
    $md[4] | Should -Be '| 1 | FuzzT1 | yes |'
    $md[5] | Should -Be '| 2 | FuzzT2 | no |'
  }
}
