#requires -Version 7.2
<#
.SYNOPSIS
  Generates a seeded, reproducible set of large and hostile OPML files for load, fuzz and import tests.
.DESCRIPTION
  The same -Seed always writes byte-identical files. Every host is synthetic (rss.example.com); nothing is read
  from a real subscription list. Files written to -OutDir:
    huge-flat.opml          10,000 feeds, no folders
    huge-nested.opml        5,000 feeds in 500 folders nested 1 to 12 levels deep
    huge-nested-200.opml    pathological: 200 folders nested inside each other, a feed at every fifth level
    wide-folder.opml        one folder holding 3,000 feeds
    hostile/<name>.opml     one file per hostile case (duplicate URLs, bad schemes, encodings, XXE, ...)
  With -Sample the huge files are skipped and the hostile files are written at a small size (every file under
  20 KB); that is what internal/opml/testdata/hostile holds.
  What it can change: it creates -OutDir and writes the files above into it, overwriting files of the same name.
  It touches nothing else. The default OutDir is .hunt/opml under the repository root, which is gitignored.
  It prints one verdict line: OK ... or FAIL ... (exit 2).
.PARAMETER Seed
  Integer seed for the random generator. Default 1.
.PARAMETER OutDir
  Where to write. Default: <repo root>/.hunt/opml.
.PARAMETER Sample
  Write only the small hostile set (for committing under testdata).
.EXAMPLE
  pwsh scripts/maintainers/new-opml-set.ps1 -Seed 7
.EXAMPLE
  pwsh scripts/maintainers/new-opml-set.ps1 -Sample -OutDir internal/opml/testdata/hostile
#>
[CmdletBinding()]
param(
  [int]$Seed = 1,
  [string]$OutDir,
  [switch]$Sample
)

Set-StrictMode -Version Latest

function ConvertTo-XmlAttr {
  # Escapes a value for use inside a double-quoted XML attribute.
  param([string]$Text)
  $Text.Replace('&', '&amp;').Replace('<', '&lt;').Replace('>', '&gt;').Replace('"', '&quot;')
}

function Get-FeedLine {
  param([int]$N, [string]$Indent = '')
  "$Indent<outline type=`"rss`" text=`"Feed $N`" title=`"Feed $N`" xmlUrl=`"https://rss.example.com/f/$N.xml`" htmlUrl=`"https://rss.example.com/s/$N/`"/>"
}

function Get-OpmlHead {
  param([string]$Title = 'Synthetic', [string]$Version = '2.0')
  "<?xml version=`"1.0`" encoding=`"UTF-8`"?>`n<opml version=`"$Version`"><head><title>$Title</title></head><body>"
}

function New-FlatOpml {
  param([int]$Count)
  $sb = [System.Text.StringBuilder]::new()
  [void]$sb.Append((Get-OpmlHead 'Huge flat')).Append("`n")
  foreach ($i in 1..$Count) { [void]$sb.Append((Get-FeedLine $i)).Append("`n") }
  [void]$sb.Append("</body></opml>`n")
  $sb.ToString()
}

function New-WideFolderOpml {
  param([int]$Count)
  $sb = [System.Text.StringBuilder]::new()
  [void]$sb.Append((Get-OpmlHead 'Wide folder')).Append("`n")
  [void]$sb.Append("<outline text=`"Wide`" title=`"Wide`">`n")
  foreach ($i in 1..$Count) { [void]$sb.Append((Get-FeedLine $i ' ')).Append("`n") }
  [void]$sb.Append("</outline>`n</body></opml>`n")
  $sb.ToString()
}

function New-NestedOpml {
  # A random tree of $Folders folders, no deeper than $MaxDepth (the first MaxDepth form a chain so the
  # deepest level is always reached), with $Feeds feeds spread over them.
  param([System.Random]$Rng, [int]$Folders, [int]$Feeds, [int]$MaxDepth)
  $parent = [int[]]::new($Folders)
  $depth = [int[]]::new($Folders)
  for ($i = 0; $i -lt $Folders; $i++) {
    if ($i -lt $MaxDepth) { $parent[$i] = $i - 1; $depth[$i] = $i + 1; continue }
    $p = $Rng.Next(-1, $i)
    while ($p -ge 0 -and $depth[$p] -ge $MaxDepth) { $p = $Rng.Next(-1, $i) }
    $parent[$i] = $p
    $depth[$i] = if ($p -ge 0) { $depth[$p] + 1 } else { 1 }
  }
  $children = @{}
  $feedsOf = @{}
  for ($i = -1; $i -lt $Folders; $i++) {
    $children[$i] = [System.Collections.Generic.List[int]]::new()
    $feedsOf[$i] = [System.Collections.Generic.List[int]]::new()
  }
  for ($i = 0; $i -lt $Folders; $i++) { $children[$parent[$i]].Add($i) }
  for ($n = 1; $n -le $Feeds; $n++) { $feedsOf[$Rng.Next(0, $Folders)].Add($n) }
  $sb = [System.Text.StringBuilder]::new()
  [void]$sb.Append((Get-OpmlHead 'Huge nested')).Append("`n")
  $emit = $null
  $emit = {
    param([int]$Node, [string]$Indent)
    if ($Node -ge 0) {
      $name = "Folder $($Node + 1) d$($depth[$Node])"
      [void]$sb.Append("$Indent<outline text=`"$name`" title=`"$name`">`n")
    }
    foreach ($f in $feedsOf[$Node]) { [void]$sb.Append((Get-FeedLine $f "$Indent ")).Append("`n") }
    foreach ($c in $children[$Node]) { & $emit $c "$Indent " }
    if ($Node -ge 0) { [void]$sb.Append("$Indent</outline>`n") }
  }
  & $emit -1 ''
  [void]$sb.Append("</body></opml>`n")
  $sb.ToString()
}

function New-Nested200Opml {
  $sb = [System.Text.StringBuilder]::new()
  [void]$sb.Append((Get-OpmlHead 'Nested 200')).Append("`n")
  foreach ($d in 1..200) {
    [void]$sb.Append("<outline text=`"Level $d`">`n")
    if ($d % 5 -eq 0) { [void]$sb.Append((Get-FeedLine $d)).Append("`n") }
  }
  [void]$sb.Append('</outline>' * 200).Append("`n</body></opml>`n")
  $sb.ToString()
}

function Get-HostileSet {
  # Returns an ordered map of file name (no extension) to bytes. -Sample keeps every file under 20 KB.
  param([switch]$Sample)
  $utf8 = [System.Text.UTF8Encoding]::new($false)
  $bigTitle = if ($Sample) { 4000 } else { 10MB }
  $bigUrl = if ($Sample) { 3000 } else { 2MB }
  $bigCat = if ($Sample) { 4000 } else { 1MB }
  $wrap = { param([string]$Body, [string]$Ver = '2.0') "$(Get-OpmlHead 'Hostile' $Ver)`n$Body`n</body></opml>`n" }
  $f = { param([string]$Attrs) "<outline type=`"rss`" $Attrs/>" }
  # One feed outline per URL, "<prefix> <n>" as the title.
  $urls = {
    param([string]$Prefix, [string[]]$Url)
    $n = 0
    ($Url | ForEach-Object { $n++; & $f "text=`"$Prefix $n`" xmlUrl=`"$(ConvertTo-XmlAttr $_)`"" }) -join "`n"
  }
  $cp = { param([int]$C) [char]::ConvertFromUtf32($C) }
  $set = [ordered]@{}

  $set['dup-near-duplicate-urls'] = $utf8.GetBytes((& $wrap (& $urls 'Dup' @(
    'https://rss.example.com/feed.xml', 'https://RSS.EXAMPLE.COM/feed.xml', 'https://rss.example.com/feed.xml/',
    'https://www.rss.example.com/feed.xml', 'http://rss.example.com/feed.xml',
    'https://rss.example.com/feed.xml?utm_source=a&utm_medium=b', 'https://rss.example.com/feed.xml#top',
    'https://rss.example.com:443/feed.xml', 'https://rss.example.com/Feed.xml', 'https://rss.example.com/feed.xml?'))))
  $set['same-title-different-url'] = $utf8.GetBytes((& $wrap ((1..5 | ForEach-Object { & $f "text=`"Same title`" xmlUrl=`"https://rss.example.com/s/$_.xml`"" }) -join "`n")))
  $set['empty-titles'] = $utf8.GetBytes((& $wrap (@(
    (& $f 'text="" xmlUrl="https://rss.example.com/e1.xml"'), (& $f 'xmlUrl="https://rss.example.com/e2.xml"'),
    (& $f 'text="   " title="" xmlUrl="https://rss.example.com/e3.xml"'),
    '<outline text=""><outline xmlUrl="https://rss.example.com/e4.xml"/></outline>') -join "`n")))
  $set['long-title'] = $utf8.GetBytes((& $wrap (& $f "text=`"$('T' * $bigTitle)`" xmlUrl=`"https://rss.example.com/long.xml`"")))
  $set['unicode-titles'] = $utf8.GetBytes((& $wrap ((@(
    "text=`"Emoji $(& $cp 0x1F4F0)$(& $cp 0x1F680) feed`" xmlUrl=`"https://rss.example.com/u1.xml`"",
    "text=`"$(& $cp 0x05E9)$(& $cp 0x05DC)$(& $cp 0x05D5)$(& $cp 0x05DD) $(& $cp 0x0645)$(& $cp 0x0631)$(& $cp 0x062D)`" xmlUrl=`"https://rss.example.com/u2.xml`"",
    "text=`"e$(& $cp 0x0301)$(& $cp 0x0302)$(& $cp 0x0303)$(& $cp 0x0304) combining`" xmlUrl=`"https://rss.example.com/u3.xml`"",
    "text=`"zero$(& $cp 0x200B)width$(& $cp 0x200D)joined$(& $cp 0xFEFF)bom$(& $cp 0x202E)rtl`" xmlUrl=`"https://rss.example.com/u4.xml`"",
    "text=`"$(& $cp 0x1F468)$(& $cp 0x200D)$(& $cp 0x1F469)$(& $cp 0x200D)$(& $cp 0x1F467) family`" xmlUrl=`"https://rss.example.com/u5.xml`""
  ) | ForEach-Object { & $f $_ }) -join "`n")))
  $set['invalid-entities'] = $utf8.GetBytes((& $wrap (@(
    '<outline type="rss" text="Bogus &bogus; entity" xmlUrl="https://rss.example.com/i1.xml"/>',
    '<outline type="rss" text="Bare & ampersand" xmlUrl="https://rss.example.com/i2.xml"/>',
    '<outline type="rss" text="Nul &#0; ref" xmlUrl="https://rss.example.com/i3.xml"/>',
    '<outline type="rss" text="Surrogate &#xD800; ref" xmlUrl="https://rss.example.com/i4.xml"/>',
    '<outline type="rss" text="Ctrl &#1; ref" xmlUrl="https://rss.example.com/i5.xml"/>') -join "`n")))
  $set['cdata'] = $utf8.GetBytes((& $wrap ('<outline type="rss" text="Cdata" xmlUrl="https://rss.example.com/c1.xml"><![CDATA[ <b>not markup</b> ]]></outline>' + "`n" + '<![CDATA[ stray cdata in body ]]>')))
  $set['unknown-attributes'] = $utf8.GetBytes((& $wrap (@(
    (& $f 'text="Odd" xmlUrl="https://rss.example.com/a1.xml" x-custom="1" kipple:refresh="9999999999999" xmlns:kipple="urn:example:kipple"'),
    (& $f 'text="Odd2" xmlUrl="https://rss.example.com/a2.xml" TEXT="upper" XMLURL="https://rss.example.com/upper.xml" category="a,b,,/c"')) -join "`n")))
  $set['missing-or-relative-xmlurl'] = $utf8.GetBytes((& $wrap (@(
    (& $f 'text="None"'), (& $f 'text="Empty" xmlUrl=""'), (& $f 'text="Relative" xmlUrl="/feed.xml"'),
    (& $f 'text="Relative2" xmlUrl="../feed.xml"'), (& $f 'text="Scheme-less" xmlUrl="rss.example.com/feed.xml"'),
    (& $f 'text="Proto-relative" xmlUrl="//rss.example.com/feed.xml"'),
    (& $f 'text="Space" xmlUrl="  https://rss.example.com/sp.xml  "')) -join "`n")))
  $set['bad-schemes'] = $utf8.GetBytes((& $wrap (& $urls 'Scheme' @(
    'file:///etc/passwd', 'javascript:alert(1)', 'data:text/xml,x', 'ftp://rss.example.com/f.xml',
    'gopher://rss.example.com/f', 'view-source:https://rss.example.com/f', 'jar:file:///x.jar!/f'))))
  $set['private-and-local-urls'] = $utf8.GetBytes((& $wrap (& $urls 'Private' @(
    'http://127.0.0.1/f.xml', 'http://localhost/f.xml', 'http://10.0.0.1/f.xml', 'http://192.168.1.1/f.xml',
    'http://169.254.169.254/latest/meta-data/', 'http://[::1]/f.xml', 'http://0x7f.1/f.xml',
    'http://2130706433/f.xml', 'http://127.0.0.1.nip.io/f.xml', 'http://rss.example.com./f.xml'))))
  $set['credentials-in-urls'] = $utf8.GetBytes((& $wrap (& $urls 'Cred' @(
    'https://user:secret@rss.example.com/f.xml', 'https://user@rss.example.com/f.xml',
    'https://rss.example.com/f.xml?token=abc123&apikey=xyz'))))
  $set['overlong-urls'] = $utf8.GetBytes((& $wrap (@(
    (& $f "text=`"Long path`" xmlUrl=`"https://rss.example.com/$('p' * $bigUrl).xml`""),
    (& $f "text=`"Long host`" xmlUrl=`"https://$('h' * 300).example.com/f.xml`""),
    (& $f "text=`"Long query`" xmlUrl=`"https://rss.example.com/f.xml?q=$('q' * $bigUrl)`"")) -join "`n")))
  $set['huge-category'] = $utf8.GetBytes((& $wrap (& $f "text=`"Cat`" xmlUrl=`"https://rss.example.com/cat.xml`" category=`"$('c,' * ($bigCat / 2))`"")))

  $body = & $f 'text="Encoded" xmlUrl="https://rss.example.com/enc.xml"'
  $set['bom-utf8'] = [byte[]](@(0xEF, 0xBB, 0xBF) + $utf8.GetBytes((& $wrap $body)))
  $u16 = (& $wrap $body).Replace('encoding="UTF-8"', 'encoding="UTF-16"')
  $set['utf16le-bom'] = [byte[]](@(0xFF, 0xFE) + [System.Text.Encoding]::Unicode.GetBytes($u16))
  $set['utf16be-bom'] = [byte[]](@(0xFE, 0xFF) + [System.Text.Encoding]::BigEndianUnicode.GetBytes($u16))
  $set['utf16-declared-but-utf8'] = $utf8.GetBytes($u16)
  $latin = (& $wrap (& $f "text=`"Caf$(& $cp 0xE9) cr$(& $cp 0xE8)me`" xmlUrl=`"https://rss.example.com/l1.xml`"")).Replace('encoding="UTF-8"', 'encoding="ISO-8859-1"')
  $set['latin1-declared'] = [System.Text.Encoding]::Latin1.GetBytes($latin)
  $set['latin1-declared-but-utf8'] = $utf8.GetBytes($latin)

  $set['xxe-file'] = $utf8.GetBytes("<?xml version=`"1.0`"?>`n<!DOCTYPE opml [<!ENTITY x SYSTEM `"file:///etc/passwd`">]>`n<opml version=`"2.0`"><body><outline text=`"&x;`" xmlUrl=`"https://rss.example.com/x.xml`"/></body></opml>`n")
  $set['xxe-http-parameter-entity'] = $utf8.GetBytes("<?xml version=`"1.0`"?>`n<!DOCTYPE opml [<!ENTITY % p SYSTEM `"http://127.0.0.1:1/p.dtd`">%p;]>`n<opml version=`"2.0`"><body><outline text=`"x`" xmlUrl=`"https://rss.example.com/x.xml`"/></body></opml>`n")
  $ents = '<!ENTITY l0 "lol">' + ((1..9 | ForEach-Object { $n = $_; "<!ENTITY l$n `"$((1..10 | ForEach-Object { "&l$($n - 1);" }) -join '')`">" }) -join '')
  $set['billion-laughs'] = $utf8.GetBytes("<?xml version=`"1.0`"?>`n<!DOCTYPE opml [$ents]>`n<opml version=`"2.0`"><body><outline text=`"&l9;`" xmlUrl=`"https://rss.example.com/x.xml`"/></body></opml>`n")

  $set['mixed-opml-versions'] = $utf8.GetBytes("<?xml version=`"1.0`"?>`n<opml version=`"1.0`"><head><title>v1</title></head><body>`n" +
    '<outline text="One" type="link" url="https://rss.example.com/one/"/>' + "`n" +
    '<opml version="2.0"><body><outline type="rss" text="Two" xmlUrl="https://rss.example.com/two.xml"/></body></opml>' + "`n" +
    '<outline text="Three" xmlUrl="https://rss.example.com/three.xml"/></body></opml>' + "`n")
  $set['outline-children-and-feed-url'] = $utf8.GetBytes((& $wrap ('<outline type="rss" text="Both" xmlUrl="https://rss.example.com/both.xml">' + "`n" + (& $f 'text="Child" xmlUrl="https://rss.example.com/child.xml"') + "`n</outline>")))
  $full = & $wrap ((1..20 | ForEach-Object { Get-FeedLine $_ }) -join "`n")
  $set['truncated'] = $utf8.GetBytes($full.Substring(0, [int]($full.Length * 0.6)))
  $set['truncated-mid-attribute'] = $utf8.GetBytes($full.Substring(0, $full.IndexOf('xmlUrl="https://rss.example.com/f/3') + 20))
  $plain = $utf8.GetBytes((& $wrap (& $f 'text="a" xmlUrl="https://rss.example.com/n1.xml"')))
  $set['nul-bytes'] = [byte[]](@(0) + $plain[0..30] + @(0, 0) + $plain[31..($plain.Length - 1)] + @(0))
  $set
}

function Write-OpmlSet {
  # Writes the files and returns the counts. Used by the script body and by the tests.
  param([int]$Seed, [string]$OutDir, [switch]$Sample)
  $utf8 = [System.Text.UTF8Encoding]::new($false)
  $null = New-Item -ItemType Directory -Force -Path (Join-Path $OutDir 'hostile')
  $files = 0
  $feeds = 0
  if (-not $Sample) {
    $rng = [System.Random]::new($Seed)
    $big = [ordered]@{
      'huge-flat.opml'       = @{ Text = (New-FlatOpml 10000); Feeds = 10000 }
      'huge-nested.opml'     = @{ Text = (New-NestedOpml -Rng $rng -Folders 500 -Feeds 5000 -MaxDepth 12); Feeds = 5000 }
      'huge-nested-200.opml' = @{ Text = (New-Nested200Opml); Feeds = 40 }
      'wide-folder.opml'     = @{ Text = (New-WideFolderOpml 3000); Feeds = 3000 }
    }
    foreach ($k in $big.Keys) {
      [System.IO.File]::WriteAllBytes((Join-Path $OutDir $k), $utf8.GetBytes($big[$k].Text))
      $files++
      $feeds += $big[$k].Feeds
    }
  }
  $hostile = Get-HostileSet -Sample:$Sample
  foreach ($k in $hostile.Keys) {
    [System.IO.File]::WriteAllBytes((Join-Path $OutDir "hostile/$k.opml"), [byte[]]$hostile[$k])
    $files++
  }
  [pscustomobject]@{ Files = $files; Feeds = $feeds }
}

# Dot-sourcing (the Pester test does) only defines the functions.
if ($MyInvocation.InvocationName -ne '.') {
  try {
    if (-not $OutDir) {
      $root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
      $OutDir = Join-Path $root '.hunt/opml'
    }
    $r = Write-OpmlSet -Seed $Seed -OutDir $OutDir -Sample:$Sample
    Write-Output "OK: wrote $($r.Files) files ($($r.Feeds) feeds in the large files) to $OutDir with seed $Seed"
  } catch {
    Write-Output "FAIL: $($_.Exception.Message)"
    exit 2
  }
}
