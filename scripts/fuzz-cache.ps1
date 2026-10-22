<#
.SYNOPSIS
  Keeps each fuzz target's mutation corpus in the GitHub Actions cache under its own key, for the weekly Fuzz workflow
  (.github/workflows/fuzz.yml). Three steps of one shard job call it:
    -Action Plan   before fuzzing: writes the cache key and corpus directory of each of the shard's targets as step outputs
    -Action Cap    after fuzzing: marks which targets' corpora may be saved (non-empty, at most -MaxMiB)
    -Action Prune  after saving: deletes every fuzz-corpus key of a target except the newest
.EXAMPLE
  scripts/fuzz-cache.ps1 -Action Plan -ShardSpec 2/4     # in the workflow; writes to $GITHUB_OUTPUT
  scripts/fuzz-cache.ps1 -Action Plan -ShardSpec 2/4 -WhatIfOnly   # prints the plan and writes nothing
Why per target: a shard's targets change whenever the target list does (the round-robin split in scripts/fuzz.ps1 moves
later targets to another shard), and a corpus cached per shard stays behind on the old shard. A key per target follows the
target to whichever runner gets it. The workflow has a fixed number of restore and save steps (one per slot, because a step
cannot loop); the plan fails when a shard has more targets than slots.
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory)][ValidateSet('Plan', 'Cap', 'Prune')][string]$Action,
  [ValidatePattern('^[1-9]\d*/[1-9]\d*$')][string]$ShardSpec = '1/1',
  [ValidateRange(1, 64)][int]$Slots = 12,
  # A corpus above this is not saved (the previous, smaller one stays in the cache). Dev-machine corpora average about
  # 1 MiB a target, so 10 MiB leaves room to grow and bounds all 30 targets to 300 MiB of the 10 GB repository quota.
  [ValidateRange(1, 1024)][int]$MaxMiB = 10,
  [string]$PlanFile = $(Join-Path ($env:RUNNER_TEMP ?? [IO.Path]::GetTempPath()) 'fuzz-cache-plan.json'),
  [string]$Module = 'github.com/WPTK/kipple',
  [switch]$WhatIfOnly
)

# Dot-sourcing fuzz.ps1 binds its parameter defaults here too, so none of this script's parameters may share a name with them (hence ShardSpec).
. (Join-Path $PSScriptRoot 'fuzz.ps1')

# Get-FuzzCacheKeyPrefix: the cache key of a target without the run id: "fuzz-corpus-internal_feed-FuzzParse-". The trailing
# dash keeps one target's restore prefix from matching another whose name starts the same.
function Get-FuzzCacheKeyPrefix {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Target, [string]$Module = 'github.com/WPTK/kipple')
  $rel = ($Target.Package -replace ('^' + [regex]::Escape($Module) + '/?'), '') -replace '/', '_'
  if (-not $rel) { $rel = 'root' }
  "fuzz-corpus-$rel-$($Target.Name)-"
}

# Get-FuzzCorpusDirectory: where `go test -fuzz` keeps a target's found inputs: GOCACHE/fuzz/<import path>/<name>.
function Get-FuzzCorpusDirectory {
  [CmdletBinding()]
  param([Parameter(Mandatory)][object]$Target, [Parameter(Mandatory)][string]$CacheRoot)
  Join-Path (Join-Path (Join-Path $CacheRoot 'fuzz') $Target.Package) $Target.Name
}

# Get-DirectoryByte: total size of the files under a directory; 0 when it does not exist.
function Get-DirectoryByte {
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) { return [long]0 }
  $sum = (Get-ChildItem -LiteralPath $Path -Recurse -File -Force | Measure-Object -Property Length -Sum).Sum
  [long]($sum ?? 0)
}

# Select-StaleFuzzCache: the caches to delete. Input is `gh cache list --json id,key,createdAt`; a target's keys are the
# prefix plus a run id, so group by the key minus its last dash segment and keep the newest of each group.
function Select-StaleFuzzCache {
  [CmdletBinding()]
  param([object[]]$Cache)
  $Cache | Where-Object { $_.key -match '^fuzz-corpus-.+-\d+$' } |
    Group-Object { $_.key -replace '-\d+$', '' } |
    ForEach-Object { $_.Group | Sort-Object { [datetime]$_.createdAt } -Descending | Select-Object -Skip 1 }
}

function Write-StepOutput {
  param([string]$Name, [string]$Value)
  if ($WhatIfOnly -or -not $env:GITHUB_OUTPUT) { Write-Information -InformationAction Continue "output $Name=$Value" } else { "$Name=$Value" | Add-Content -Path $env:GITHUB_OUTPUT }
}

# Dot-sourced (the tests do), the script only defines the functions above.
if ($MyInvocation.InvocationName -ne '.') {
  $ErrorActionPreference = 'Stop'
  Set-Location (Split-Path -Parent $PSScriptRoot)

  switch ($Action) {
    'Plan' {
      $cacheRoot = (go env GOCACHE)
      $targets = @(Select-FuzzTarget -Target @(Get-FuzzTargetList) -Shard $ShardSpec | Sort-Object Package, Name)
      if ($targets.Count -eq 0) { throw "no fuzz targets in shard $ShardSpec" }
      if ($targets.Count -gt $Slots) { throw "shard $ShardSpec has $($targets.Count) targets but fuzz.yml has $Slots cache slots; add slots there and raise -Slots" }
      $plan = @(for ($i = 0; $i -lt $targets.Count; $i++) {
          [pscustomobject]@{
            Slot = $i + 1; Name = $targets[$i].Name; Package = $targets[$i].Package
            Key  = Get-FuzzCacheKeyPrefix -Target $targets[$i] -Module $Module
            Path = Get-FuzzCorpusDirectory -Target $targets[$i] -CacheRoot $cacheRoot
          }
        })
      foreach ($p in $plan) {
        Write-StepOutput "key$($p.Slot)" $p.Key
        Write-StepOutput "path$($p.Slot)" $p.Path
        Write-Information -InformationAction Continue ("slot {0,-2} {1,-28} {2}" -f $p.Slot, $p.Name, $p.Key)
      }
      if (-not $WhatIfOnly) { $plan | ConvertTo-Json -AsArray | Set-Content -Path $PlanFile }
    }
    'Cap' {
      $plan = @(Get-Content -Raw -Path $PlanFile | ConvertFrom-Json)
      foreach ($p in $plan) {
        $bytes = Get-DirectoryByte -Path $p.Path
        $save = $bytes -gt 0 -and $bytes -le ($MaxMiB * 1MB)
        $why = if ($bytes -eq 0) { 'empty, not saved' } elseif (-not $save) { "over $MaxMiB MiB, not saved" } else { 'saved' }
        Write-Information -InformationAction Continue ("{0,-28} {1,8:N1} MiB  {2}" -f $p.Name, ($bytes / 1MB), $why)
        Write-StepOutput "save$($p.Slot)" ($save ? 'true' : 'false')
      }
    }
    'Prune' {
      $json = gh cache list --key fuzz-corpus- --limit 1000 --json 'id,key,createdAt'
      if ($LASTEXITCODE -ne 0) { throw 'gh cache list failed' }
      $stale = @(Select-StaleFuzzCache -Cache @($json | ConvertFrom-Json))
      foreach ($c in $stale) {
        Write-Information -InformationAction Continue "delete $($c.key)"
        if (-not $WhatIfOnly) { gh cache delete $c.id; if ($LASTEXITCODE -ne 0) { throw "gh cache delete $($c.id) failed" } }
      }
      Write-Information -InformationAction Continue "$($stale.Count) older fuzz corpus caches deleted"
    }
  }
}
