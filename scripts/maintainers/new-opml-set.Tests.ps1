#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/maintainers/new-opml-set.ps1. Run: Invoke-Pester scripts/maintainers/new-opml-set.Tests.ps1
# The script is dot-sourced (it only defines functions then); files go to a temp dir.

BeforeAll {
  $script:script = Join-Path $PSScriptRoot 'new-opml-set.ps1'
  . $script:script
  $script:pwshExe = (Get-Process -Id $PID).Path
  function script:Get-TreeHash($dir) {
    Get-ChildItem $dir -Recurse -File | Sort-Object FullName | ForEach-Object { "$($_.FullName.Substring($dir.Length)) $((Get-FileHash $_.FullName).Hash)" }
  }
  function script:Get-Depth([string]$xml) {
    $d = 0; $max = 0
    foreach ($m in [regex]::Matches($xml, '<outline\b[^>]*?(/?)>|</outline>')) {
      if ($m.Value -eq '</outline>') { $d-- }
      elseif ($m.Groups[1].Value -ne '/') { $d++; if ($d -gt $max) { $max = $d } }
    }
    $max
  }
}

Describe 'new-opml-set.ps1' {
  BeforeAll {
    $script:a = Join-Path ([System.IO.Path]::GetTempPath()) "opml-a-$([guid]::NewGuid())"
    $script:b = Join-Path ([System.IO.Path]::GetTempPath()) "opml-b-$([guid]::NewGuid())"
    $script:c = Join-Path ([System.IO.Path]::GetTempPath()) "opml-c-$([guid]::NewGuid())"
    $null = Write-OpmlSet -Seed 5 -OutDir $script:a
    $null = Write-OpmlSet -Seed 5 -OutDir $script:b
    $null = Write-OpmlSet -Seed 6 -OutDir $script:c
  }
  AfterAll { Remove-Item -Recurse -Force $script:a, $script:b, $script:c -ErrorAction SilentlyContinue }

  It 'writes byte-identical files for the same seed and different nesting for another' {
    Get-TreeHash $script:a | Should -Be (Get-TreeHash $script:b)
    (Get-FileHash (Join-Path $script:a 'huge-nested.opml')).Hash | Should -Not -Be (Get-FileHash (Join-Path $script:c 'huge-nested.opml')).Hash
  }
  It 'writes 10,000 flat feeds and 3,000 in one folder' {
    ([regex]::Matches((Get-Content -Raw (Join-Path $script:a 'huge-flat.opml')), 'xmlUrl=')).Count | Should -Be 10000
    $wide = Get-Content -Raw (Join-Path $script:a 'wide-folder.opml')
    ([regex]::Matches($wide, 'xmlUrl=')).Count | Should -Be 3000
    ([regex]::Matches($wide, '<outline text=')).Count | Should -Be 1
  }
  It 'writes 5,000 feeds in 500 folders nested up to 12 levels, and a 200-level file' {
    $nested = Get-Content -Raw (Join-Path $script:a 'huge-nested.opml')
    ([regex]::Matches($nested, 'xmlUrl=')).Count | Should -Be 5000
    ([regex]::Matches($nested, '<outline text="Folder ')).Count | Should -Be 500
    Get-Depth $nested | Should -Be 12
    Get-Depth (Get-Content -Raw (Join-Path $script:a 'huge-nested-200.opml')) | Should -Be 200
  }
  It 'writes only well-formed-or-deliberately-broken files, all with synthetic hosts' {
    foreach ($file in Get-ChildItem $script:a -Recurse -Filter *.opml | Where-Object { $_.BaseName -notlike 'truncated*' }) {
      $text = [System.Text.Encoding]::Latin1.GetString([System.IO.File]::ReadAllBytes($file.FullName))
      foreach ($m in [regex]::Matches($text, 'https?://([A-Za-z0-9.\-]+)')) {
        $m.Groups[1].Value | Should -Match '(example\.com|example\.com\.|127\.0\.0\.1|localhost|nip\.io|^[0-9.]+$|^h+\.example\.com|^user$|^0x7f\.1$)' -Because $file.Name
      }
    }
  }
  It 'covers every hostile case the plan lists' {
    $names = (Get-ChildItem (Join-Path $script:a 'hostile') -Filter *.opml).BaseName
    foreach ($want in 'dup-near-duplicate-urls', 'empty-titles', 'long-title', 'unicode-titles', 'invalid-entities', 'cdata', 'unknown-attributes',
      'missing-or-relative-xmlurl', 'bad-schemes', 'private-and-local-urls', 'credentials-in-urls', 'overlong-urls', 'huge-category', 'bom-utf8',
      'utf16le-bom', 'latin1-declared', 'xxe-file', 'billion-laughs', 'mixed-opml-versions', 'outline-children-and-feed-url', 'truncated', 'nul-bytes') {
      $names | Should -Contain $want
    }
    (Get-Item (Join-Path $script:a 'hostile/long-title.opml')).Length | Should -BeGreaterThan 10MB
  }
  It '-Sample writes only hostile files, each under 20 KB' {
    $d = Join-Path ([System.IO.Path]::GetTempPath()) "opml-s-$([guid]::NewGuid())"
    try {
      $out = & $script:pwshExe -NoProfile -File $script:script -Sample -OutDir $d
      $out | Should -Match '^OK: wrote \d+ files'
      Test-Path (Join-Path $d 'huge-flat.opml') | Should -BeFalse
      $files = Get-ChildItem $d -Recurse -File
      $files.Count | Should -BeGreaterThan 20
      ($files | Where-Object Length -ge 20KB) | Should -BeNullOrEmpty
    } finally { Remove-Item -Recurse -Force $d -ErrorAction SilentlyContinue }
  }
}
