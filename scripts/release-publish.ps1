#requires -Version 7.2
<#
.SYNOPSIS
  Creates the GitHub Release for a tag that is already pushed, after verifying the image and SBOM signatures.
.DESCRIPTION
  Run it after you pushed the release tag (docs/RELEASING.md, step 11). The tag itself is never created here.

  Steps:
    1. Checks the tag kind matches -Prerelease/-Full, the tag exists in the repository and no GitHub Release exists for it yet.
    2. Waits (polling, not busy-looping) for the Release workflow run of that tag to finish, and requires success.
    3. Downloads the run's `image-notes` artifact (image-notes.md, the SBOM and its signature bundle).
    4. Runs `cosign verify` on the image and `cosign verify-blob` on the SBOM, both with the exact identity
       https://github.com/<repo>/.github/workflows/release.yml@refs/tags/<tag> and the GitHub Actions OIDC issuer.
       Any verification failure stops the script before anything is created (exit 1).
    5. Builds the notes: `node scripts/changelog.mjs notes <version>`, a blank line, then image-notes.md.
    6. Runs `gh release create <tag> --notes-file ... <sbom> <bundle>` with --prerelease unless -Full.

  What it can change: the only state it changes is step 6, one new GitHub Release. Everything else is read-only
  or in a temp folder that is removed at the end. With -WhatIf it does steps 1 to 5 (reads and verifies) and
  prints the step 6 command instead of running it.
.PARAMETER Tag
  The release tag, vX.Y.Z or vX.Y.Z-(alpha|beta|rc).N, already pushed to origin.
.PARAMETER Prerelease
  Mark the release as a pre-release. You must give exactly one of -Prerelease and -Full; there is no default.
.PARAMETER Full
  Create a normal (not pre-release) release.
.PARAMETER Repo
  owner/name. Default: the repository `gh repo view` reports.
.PARAMETER TimeoutMinutes
  How long to wait for the Release workflow run. Default 60.
.EXAMPLE
  pwsh scripts/release-publish.ps1 -Tag v1.2.3-beta.1 -Prerelease -WhatIf
.EXAMPLE
  pwsh scripts/release-publish.ps1 -Tag v1.2.3 -Full -Verbose
.NOTES
  Exit codes: 0 release created (or -WhatIf finished), 1 a signature check failed or the workflow run failed,
  2 usage or environment error (including neither or both of -Prerelease and -Full). PowerShell exits 1 when it rejects a malformed tag.
  If this breaks: it depends on (a) the workflow file name release.yml and its tag-push run, (b) the artifact name
  `image-notes` holding image-notes.md, kipple-<version>.sbom.json and kipple-<version>.sbom.json.sigstore.json
  (all three names are in .github/workflows/release.yml, "Release notes block"), (c) the output format of
  `node scripts/changelog.mjs notes`, (d) cosign 3 or later and its flags, (e) `gh run list/view/download`.
  Each is wrapped in one function below. Tests: Invoke-Pester scripts/release-publish.Tests.ps1
#>
[CmdletBinding(SupportsShouldProcess)]
param(
  [Parameter(Mandatory)]
  [ValidatePattern('^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.[1-9][0-9]*)?$')]
  [string]$Tag,
  [switch]$Prerelease,
  [switch]$Full,
  [ValidatePattern('^[\w.-]+/[\w.-]+$')][string]$Repo,
  [ValidateRange(1, 720)][int]$TimeoutMinutes = 60
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lib/Kipple.Tools.ps1')

$script:WorkflowFile = 'release.yml'
$script:ArtifactName = 'image-notes'
$script:OidcIssuer = 'https://token.actions.githubusercontent.com'

function Get-ReleaseVersion {
  <#
  .SYNOPSIS
    The version part of a release tag: v1.2.3-beta.1 gives 1.2.3-beta.1.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Tag)
  return $Tag.TrimStart('v')
}

function Get-CosignIdentity {
  <#
  .SYNOPSIS
    The exact certificate identity the Release workflow signs with for one tag.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Tag)
  return "https://github.com/$Repo/.github/workflows/$($script:WorkflowFile)@refs/tags/$Tag"
}

function Get-ImageReference {
  <#
  .SYNOPSIS
    The registry reference of the image for a tag. Registry names are lower case.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Version)
  return ('ghcr.io/{0}:{1}' -f $Repo.ToLowerInvariant(), $Version)
}

function Select-ReleaseRun {
  <#
  .SYNOPSIS
    Picks the newest push-triggered run from `gh run list --json databaseId,status,conclusion,event` output.
  .OUTPUTS
    The run object, or $null when there is none yet.
  #>
  [CmdletBinding()]
  param([AllowEmptyCollection()][object[]]$Runs = @())
  $pushed = @($Runs | Where-Object { $_.event -eq 'push' })
  if ($pushed.Count -eq 0) { return $null }
  return ($pushed | Sort-Object -Property databaseId -Descending | Select-Object -First 1)
}

function Get-ReleaseRun {
  <#
  .SYNOPSIS
    Reads the Release workflow runs for a tag from GitHub and returns the newest one, or $null.
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Tag)
  $r = Invoke-Native -FilePath gh -Arguments 'run', 'list', '-R', $Repo, '--workflow', $script:WorkflowFile, '--branch', $Tag, '--limit', '10', '--json', 'databaseId,status,conclusion,event' `
    -Step 'list the Release workflow runs' -Fix 'gh auth status; check that the tag push started the Release workflow (Actions > Release)'
  $runs = ($r.Output -join "`n") | ConvertFrom-Json
  return Select-ReleaseRun -Runs @($runs)
}

function Wait-ReleaseRun {
  <#
  .SYNOPSIS
    Polls until the Release run of the tag completes. Returns the final run; throws on timeout.
  #>
  [CmdletBinding()]
  param(
    [Parameter(Mandatory)][string]$Repo,
    [Parameter(Mandatory)][string]$Tag,
    [int]$TimeoutMinutes = 60,
    [int]$PollSeconds = 30
  )
  $deadline = [DateTime]::UtcNow.AddMinutes($TimeoutMinutes)
  while ($true) {
    $run = Get-ReleaseRun -Repo $Repo -Tag $Tag
    if ($null -eq $run) {
      Write-KippleInfo "No Release run for $Tag yet."
    } elseif ($run.status -eq 'completed') {
      return $run
    } else {
      Write-KippleInfo "Release run $($run.databaseId) is $($run.status)."
    }
    if ([DateTime]::UtcNow -ge $deadline) {
      throw "Step 'wait for the Release run' failed: no completed run for $Tag within $TimeoutMinutes minutes. Likely fix: check Actions > Release, then re-run this script."
    }
    Start-Sleep -Seconds $PollSeconds
  }
}

function Save-ImageNotesArtifact {
  <#
  .SYNOPSIS
    Downloads the image-notes artifact of a run into a folder and checks the three expected files are there.
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][long]$RunId, [Parameter(Mandatory)][string]$Version, [Parameter(Mandatory)][string]$Directory)
  $null = Invoke-Native -FilePath gh -Arguments 'run', 'download', "$RunId", '-R', $Repo, '-n', $script:ArtifactName, '-D', $Directory `
    -Step 'download the image-notes artifact' -Fix "check the run has an artifact named $($script:ArtifactName) (the workflow's last job)"
  $names = @('image-notes.md', "kipple-$Version.sbom.json", "kipple-$Version.sbom.json.sigstore.json")
  foreach ($n in $names) {
    if (-not (Test-Path -LiteralPath (Join-Path $Directory $n))) {
      throw "Step 'download the image-notes artifact' failed: $n is missing from the artifact. Likely fix: the artifact file names changed in .github/workflows/release.yml; update `$names in Save-ImageNotesArtifact."
    }
  }
}

function Test-Signature {
  <#
  .SYNOPSIS
    Runs one cosign verification. Returns an object with Ok and Detail; a failed check is an answer, not an exception.
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][string]$Cosign, [Parameter(Mandatory)][string]$Step, [Parameter(Mandatory)][string[]]$Arguments)
  $r = Invoke-Native -FilePath $Cosign -Arguments $Arguments -Step $Step -AllowFailure `
    -Fix 'cosign 3 or later is needed; check the tag is the one the workflow signed'
  $detail = if ($r.ExitCode -eq 0) { 'verified' } else { ($r.Output | Select-Object -Last 5) -join ' | ' }
  return [pscustomobject]@{ Step = $Step; Ok = ($r.ExitCode -eq 0); Detail = $detail }
}

function Get-VerifyArgument {
  <#
  .SYNOPSIS
    The cosign arguments for the image check (by digest) and the SBOM check, with the exact identity.
  .OUTPUTS
    Hashtable with Image and Blob argument arrays.
  #>
  [CmdletBinding()]
  [OutputType([hashtable])]
  param(
    [Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Tag, [Parameter(Mandatory)][string]$Directory,
    [Parameter(Mandatory)][ValidatePattern('^sha256:[0-9a-f]{64}$')][string]$Digest
  )
  $version = Get-ReleaseVersion -Tag $Tag
  $identity = Get-CosignIdentity -Repo $Repo -Tag $Tag
  $common = @('--certificate-identity', $identity, '--certificate-oidc-issuer', $script:OidcIssuer)
  $sbom = Join-Path $Directory "kipple-$version.sbom.json"
  return @{
    # By digest, the one the release notes name, so the verified image is the object the notes describe.
    Image = @('verify', ((Get-ImageReference -Repo $Repo -Version $version).Split(':')[0] + '@' + $Digest)) + $common
    Blob  = @('verify-blob', $sbom, '--bundle', "$sbom.sigstore.json") + $common
  }
}

function Get-ImageDigest {
  <#
  .SYNOPSIS
    The image digest the release notes name (the `<image>@sha256:...` line the Release workflow writes).
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][AllowEmptyString()][string]$ImageNotes)
  $m = [regex]::Match($ImageNotes, '@(sha256:[0-9a-f]{64})')
  if (-not $m.Success) {
    throw "Step 'read the image digest' failed: image-notes.md has no <image>@sha256:<digest> line. Likely fix: the notes format changed in .github/workflows/release.yml (Release notes block); update Get-ImageDigest."
  }
  return $m.Groups[1].Value
}

function Assert-ReleaseKind {
  <#
  .SYNOPSIS
    Refuses -Full for a prerelease tag and -Prerelease for a stable tag.
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Tag, [Parameter(Mandatory)][bool]$IsFull)
  $isPre = $Tag -match '-(alpha|beta|rc)\.'
  if ($isPre -and $IsFull) { throw "Tag $Tag is a prerelease (alpha, beta or rc) but -Full was given. Likely fix: use -Prerelease." }
  if (-not $isPre -and -not $IsFull) { throw "Tag $Tag is a stable version but -Prerelease was given. Likely fix: use -Full." }
}

function Build-ReleaseNote {
  <#
  .SYNOPSIS
    Joins the changelog section and the image notes into the release body.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$ChangelogSection, [Parameter(Mandatory)][string]$ImageNotes)
  if ([string]::IsNullOrWhiteSpace($ChangelogSection)) {
    throw "Step 'build the notes' failed: the changelog section is empty. Likely fix: run the changelog release step first (node scripts/changelog.mjs release X.Y.Z) so CHANGELOG.md has this version."
  }
  return $ChangelogSection.TrimEnd() + "`n`n" + $ImageNotes.Trim() + "`n"
}

function Get-ReleaseCreateArgument {
  <#
  .SYNOPSIS
    The `gh release create` arguments. --prerelease unless -Full.
  #>
  [CmdletBinding()]
  [OutputType([string[]])]
  param([string]$Repo, [string]$Tag, [string]$NotesFile, [string[]]$Assets, [bool]$IsFull)
  $a = @('release', 'create', $Tag, '-R', $Repo, '--verify-tag', '--title', $Tag, '--notes-file', $NotesFile)
  if (-not $IsFull) { $a += '--prerelease' }
  return $a + $Assets
}

function Test-ReleaseAbsent {
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][string]$Repo, [Parameter(Mandatory)][string]$Tag)
  $r = Invoke-Native -FilePath gh -Arguments 'release', 'view', $Tag, '-R', $Repo -Step 'check for an existing release' -AllowFailure -Fix 'gh auth status'
  if ($r.ExitCode -eq 0) { return $false }
  # Only "release not found" means there is none; an expired login or a network error must not look like absence.
  if (($r.Output -join ' ') -match 'release not found') { return $true }
  throw "Step 'check for an existing release' failed: gh could not tell (($r.Output | Select-Object -First 1)). Likely fix: gh auth status, then check the network."
}

function Assert-ChangelogMatchesTag {
  <#
  .SYNOPSIS
    Checks that this checkout's CHANGELOG.md is the one in the tag, because the notes are read from the checkout.
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$Tag)
  $inTag = Invoke-Native -FilePath git -Arguments '-C', $Root, 'rev-parse', '--verify', '--quiet', "refs/tags/${Tag}:CHANGELOG.md" -Step 'read CHANGELOG.md of the tag' -AllowFailure `
    -Fix "git fetch origin tag $Tag, then run this script again"
  if ($inTag.ExitCode -ne 0) { throw "Step 'read CHANGELOG.md of the tag' failed: the tag $Tag is not in this checkout. Likely fix: git fetch origin tag $Tag." }
  $here = Invoke-Native -FilePath git -Arguments '-C', $Root, 'hash-object', 'CHANGELOG.md' -Step 'hash CHANGELOG.md' -Fix 'run from a Kipple checkout'
  if ($here.Output[0].Trim() -ne $inTag.Output[0].Trim()) {
    throw "Step 'check the changelog' failed: this checkout's CHANGELOG.md differs from the one in $Tag, and the notes are read from it. Likely fix: git switch --detach $Tag (or use a clean checkout at the tag), then run this script again."
  }
}

function Invoke-ReleasePublish {
  <#
  .SYNOPSIS
    The body of the script. Emits the exit code (0 or 1) as its last output.
  #>
  [CmdletBinding(SupportsShouldProcess)]
  param([string]$Tag, [bool]$IsFull, [string]$Repo, [int]$TimeoutMinutes)
  $root = Get-RepoRoot -Path $PSScriptRoot
  if (-not $Repo) { $Repo = Get-RepoSlug -RepoRoot $root }
  $version = Get-ReleaseVersion -Tag $Tag
  Assert-ReleaseKind -Tag $Tag -IsFull $IsFull
  Write-KippleInfo "Release $Tag of $Repo ($(if ($IsFull) { 'full release' } else { 'pre-release' }))"

  $remote = Invoke-Native -FilePath git -Arguments '-C', $root, 'ls-remote', '--tags', "https://github.com/$Repo.git", "refs/tags/$Tag" -Step 'check the tag is on the repository' `
    -Fix 'push the tag first (docs/RELEASING.md step 8); this script never creates it'
  if (-not ($remote.Output -join '')) { throw "Step 'check the tag is on the repository' failed: $Tag is not in $Repo. Likely fix: push the tag first." }
  if (-not (Test-ReleaseAbsent -Repo $Repo -Tag $Tag)) {
    throw "Step 'check for an existing release' failed: a GitHub Release for $Tag exists already (the workflow appends its notes to an existing release itself). Likely fix: nothing to do, or edit it by hand."
  }
  $cosign = Find-Cosign

  $run = Wait-ReleaseRun -Repo $Repo -Tag $Tag -TimeoutMinutes $TimeoutMinutes
  if ($run.conclusion -ne 'success') {
    Write-KippleInfo "The Release run $($run.databaseId) ended as '$($run.conclusion)'. Fix it and use 'Re-run failed jobs' (never 're-run all jobs'), then run this script again."
    return 1
  }

  $work = Join-Path ([IO.Path]::GetTempPath()) "kipple-publish-$([guid]::NewGuid().ToString('N').Substring(0, 8))"
  try {
    $null = New-Item -ItemType Directory -Path $work -WhatIf:$false
    Save-ImageNotesArtifact -Repo $Repo -RunId $run.databaseId -Version $version -Directory $work
    $imageNotes = Get-Content -LiteralPath (Join-Path $work 'image-notes.md') -Raw
    $digest = Get-ImageDigest -ImageNotes $imageNotes
    $verify = Get-VerifyArgument -Repo $Repo -Tag $Tag -Directory $work -Digest $digest
    $checks = @(
      Test-Signature -Cosign $cosign -Step 'cosign verify (image)' -Arguments $verify.Image
      Test-Signature -Cosign $cosign -Step 'cosign verify-blob (SBOM)' -Arguments $verify.Blob
    )
    $checks | ForEach-Object { Write-KippleInfo ('{0,-28} {1}' -f $_.Step, $(if ($_.Ok) { 'ok' } else { 'FAILED: ' + $_.Detail })) }
    if ($checks | Where-Object { -not $_.Ok }) {
      Write-KippleInfo 'A signature check failed: no release was created.'
      return 1
    }

    Assert-ChangelogMatchesTag -Root $root -Tag $Tag
    $section = (Invoke-Native -FilePath node -Arguments 'scripts/changelog.mjs', 'notes', $version -WorkingDirectory $root -Step 'read the changelog section' `
        -Fix "CHANGELOG.md needs a section for $version (node scripts/changelog.mjs release $version)").Output -join "`n"
    $body = Build-ReleaseNote -ChangelogSection $section -ImageNotes $imageNotes
    $notesFile = Join-Path $work 'notes.md'
    Set-Content -LiteralPath $notesFile -Value $body -NoNewline -WhatIf:$false
    $assets = @((Join-Path $work "kipple-$version.sbom.json"), (Join-Path $work "kipple-$version.sbom.json.sigstore.json"))
    $createArgs = Get-ReleaseCreateArgument -Repo $Repo -Tag $Tag -NotesFile $notesFile -Assets $assets -IsFull $IsFull

    if ($PSCmdlet.ShouldProcess("$Repo $Tag", 'gh release create')) {
      $null = Invoke-Native -FilePath gh -Arguments $createArgs -WorkingDirectory $root -Step 'create the GitHub Release' -Fix 'gh auth status; the tag must exist on origin'
      Write-KippleInfo "Created the release for $Tag."
    } else {
      Write-KippleInfo ("WhatIf: would run: " + (Format-CommandLine -FilePath gh -Arguments $createArgs))
    }
    return 0
  } finally {
    Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue -WhatIf:$false
  }
}

if ($MyInvocation.InvocationName -ne '.') {
  # Exactly one of -Prerelease and -Full is required and there is no default: a release is never made
  # pre-release or full by accident.
  $choice = @{ Prerelease = [bool]$Prerelease; Full = [bool]$Full }
  $boundArgs = @{ Tag = $Tag; IsFull = [bool]$Full; Repo = $Repo; TimeoutMinutes = $TimeoutMinutes; WhatIf = $WhatIfPreference }
  Invoke-ToolMain -Name 'release-publish' -Body {
    if ($choice.Prerelease -eq $choice.Full) { throw 'Give exactly one of -Prerelease and -Full (there is no default).' }
    Invoke-ReleasePublish @boundArgs
  }
}
