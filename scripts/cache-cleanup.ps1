<#
.SYNOPSIS
  Deletes Actions caches that only take up the repository's 10 GB cache quota: the Docker layer caches that pull
  requests left behind, and all but the newest CodeQL overlay database per language. Run weekly by
  .github/workflows/cache-cleanup.yml; run by hand with gh signed in to the repository.
.EXAMPLE
  scripts/cache-cleanup.ps1 -WhatIfOnly   # list what would be deleted
  scripts/cache-cleanup.ps1               # delete it
Both kinds are rebuilt on demand, so a deletion costs at most one slower run. The build, module and browser caches are
not touched. Pull requests only read the Docker layer cache of main (.github/actions/build-image), so the pull request
layers are left over from before that and from other tools writing under a pull request ref.
#>
[CmdletBinding()]
param([switch]$WhatIfOnly)

# Select-DisposableCache: the caches to delete, from `gh cache list --json id,key,ref,createdAt,sizeInBytes`.
function Select-DisposableCache {
  [CmdletBinding()]
  param([object[]]$Cache)
  # Docker layers written under a pull request ref: main's copy is the one every run reads.
  $Cache | Where-Object { $_.key -like 'buildkit-blob-*' -and $_.ref -like 'refs/pull/*' }
  # CodeQL overlay databases: one is saved per main commit and none is pruned. Keep the newest per language.
  $Cache | Where-Object { $_.key -match '^codeql-overlay-base-database-\d+-[0-9a-f]+-[a-z-]+-\d' } |
    Group-Object { $_.key -replace '^codeql-overlay-base-database-\d+-[0-9a-f]+-([a-z-]+?)-\d.*$', '$1' } |
    ForEach-Object { $_.Group | Sort-Object { [datetime]$_.createdAt } -Descending | Select-Object -Skip 1 }
}

# Dot-sourced (the tests do), the script only defines the function above.
if ($MyInvocation.InvocationName -ne '.') {
  $ErrorActionPreference = 'Stop'
  $json = gh cache list --limit 5000 --json 'id,key,ref,createdAt,sizeInBytes'
  if ($LASTEXITCODE -ne 0) { throw 'gh cache list failed' }
  $all = @($json | ConvertFrom-Json)
  $doomed = @(Select-DisposableCache -Cache $all)
  $bytes = ($doomed | Measure-Object -Property sizeInBytes -Sum).Sum
  Write-Information -InformationAction Continue ('{0} of {1} caches ({2:N0} MB) are disposable' -f $doomed.Count, $all.Count, ($bytes / 1MB))
  if ($WhatIfOnly) { return }
  foreach ($c in $doomed) {
    gh cache delete $c.id
    if ($LASTEXITCODE -ne 0) { throw "gh cache delete $($c.id) failed" }
  }
  Write-Information -InformationAction Continue "deleted $($doomed.Count) caches"
}
