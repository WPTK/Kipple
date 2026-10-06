#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/release-publish.ps1. Run: Invoke-Pester scripts/release-publish.Tests.ps1
# The script is dot-sourced (it only defines functions then). gh, cosign, node and git are mocked: nothing real runs.

BeforeAll {
  $script:script = Join-Path $PSScriptRoot 'release-publish.ps1'
  . $script:script -Tag v1.2.3-beta.1 -Prerelease
  $script:pwshExe = (Get-Process -Id $PID).Path
}

Describe 'argument validation' {
  It 'refuses a malformed tag' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Tag 1.2.3 -Prerelease -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
  It 'refuses a tag with a leading zero' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Tag v01.2.3 -Full -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
  It 'requires -Prerelease or -Full: there is no default' {
    $out = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Tag v1.2.3 -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
    ($out -join ' ') | Should -Match 'Prerelease|Full|parameter'
  }
  It 'refuses -Prerelease and -Full together' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Tag v1.2.3 -Prerelease -Full -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
}

Describe 'Get-CosignIdentity' {
  It 'is the exact workflow path at the exact tag' {
    Get-CosignIdentity -Repo 'owner/name' -Tag 'v1.2.3-beta.1' |
      Should -Be 'https://github.com/owner/name/.github/workflows/release.yml@refs/tags/v1.2.3-beta.1'
  }
}

Describe 'Get-ReleaseVersion and Get-ImageReference' {
  It 'drops the leading v' { Get-ReleaseVersion -Tag 'v1.2.3-rc.2' | Should -Be '1.2.3-rc.2' }
  It 'lower-cases the repository for the registry' {
    Get-ImageReference -Repo 'Owner/Name' -Version '1.2.3' | Should -Be 'ghcr.io/owner/name:1.2.3'
  }
}

Describe 'Select-ReleaseRun' {
  It 'returns $null when there is no run yet' { Select-ReleaseRun -Runs @() | Should -BeNullOrEmpty }
  It 'ignores runs that were not started by a push' {
    Select-ReleaseRun -Runs @([pscustomobject]@{ databaseId = 5; event = 'workflow_dispatch'; status = 'completed' }) | Should -BeNullOrEmpty
  }
  It 'picks the newest push run' {
    $runs = @(
      [pscustomobject]@{ databaseId = 5; event = 'push'; status = 'completed' }
      [pscustomobject]@{ databaseId = 9; event = 'push'; status = 'in_progress' }
    )
    (Select-ReleaseRun -Runs $runs).databaseId | Should -Be 9
  }
}

Describe 'Wait-ReleaseRun' {
  BeforeEach { Mock Start-Sleep {}; Mock Write-KippleInfo {} }
  It 'polls until the run completes, sleeping between polls rather than spinning' {
    $script:n = 0
    Mock Get-ReleaseRun {
      $script:n++
      if ($script:n -lt 3) { return [pscustomobject]@{ databaseId = 7; status = 'in_progress'; conclusion = $null } }
      [pscustomobject]@{ databaseId = 7; status = 'completed'; conclusion = 'success' }
    }
    (Wait-ReleaseRun -Repo 'o/n' -Tag 'v1.0.0' -PollSeconds 1).conclusion | Should -Be 'success'
    Should -Invoke Start-Sleep -Times 2
  }
  It 'throws when the timeout passes with no completed run' {
    Mock Get-ReleaseRun { $null }
    { Wait-ReleaseRun -Repo 'o/n' -Tag 'v1.0.0' -TimeoutMinutes 0 -PollSeconds 1 } | Should -Throw -ExpectedMessage "*wait for the Release run*"
  }
}

Describe 'Get-VerifyArgument' {
  It 'uses the exact identity and the Actions issuer for both the image and the SBOM' {
    $a = Get-VerifyArgument -Repo 'owner/name' -Tag 'v1.2.3' -Directory 'd' -Digest ('sha256:' + ('c' * 64))
    $identity = 'https://github.com/owner/name/.github/workflows/release.yml@refs/tags/v1.2.3'
    foreach ($set in @($a.Image, $a.Blob)) {
      $set | Should -Contain '--certificate-identity'
      $set | Should -Contain $identity
      $set | Should -Contain 'https://token.actions.githubusercontent.com'
      $set | Should -Not -Contain '--certificate-identity-regexp'
    }
    $a.Image[0] | Should -Be 'verify'
    $a.Image[1] | Should -Be ('ghcr.io/owner/name@sha256:' + ('c' * 64))
    $a.Blob[0] | Should -Be 'verify-blob'
    $a.Blob | Should -Contain '--bundle'
  }
}

Describe 'Build-ReleaseNote' {
  It 'puts the changelog section first and the image notes after it' {
    $body = Build-ReleaseNote -ChangelogSection "## [1.2.3]`n- a change`n" -ImageNotes "digest: sha256:abc`n"
    $body | Should -Be "## [1.2.3]`n- a change`n`ndigest: sha256:abc`n"
  }
  It 'refuses an empty changelog section' {
    { Build-ReleaseNote -ChangelogSection '  ' -ImageNotes 'x' } | Should -Throw -ExpectedMessage '*changelog section is empty*'
  }
}

Describe 'Get-ReleaseCreateArgument' {
  It 'marks a pre-release unless -Full' {
    $a = Get-ReleaseCreateArgument -Repo 'o/n' -Tag 'v1.0.0-rc.1' -NotesFile 'n.md' -Assets @('a', 'b') -IsFull $false
    $a | Should -Contain '--prerelease'
    $a[-2..-1] | Should -Be @('a', 'b')
  }
  It 'omits --prerelease for a full release' {
    (Get-ReleaseCreateArgument -Repo 'o/n' -Tag 'v1.0.0' -NotesFile 'n.md' -Assets @() -IsFull $true) | Should -Not -Contain '--prerelease'
  }
  It 'asks gh to verify the tag exists' {
    (Get-ReleaseCreateArgument -Repo 'o/n' -Tag 'v1.0.0' -NotesFile 'n.md' -Assets @() -IsFull $true) | Should -Contain '--verify-tag'
  }
}

Describe 'Get-ImageDigest' {
  It 'reads the digest from the image line of the notes' {
    Get-ImageDigest -ImageNotes ("ghcr.io/o/n:1.2.3`nghcr.io/o/n@sha256:" + ('e' * 64)) | Should -Be ('sha256:' + ('e' * 64))
  }
  It 'throws when the notes name no digest' { { Get-ImageDigest -ImageNotes 'nothing' } | Should -Throw '*no <image>@sha256*' }
}

Describe 'Assert-ReleaseKind' {
  It 'refuses -Full for a prerelease tag' { { Assert-ReleaseKind -Tag 'v1.0.0-rc.1' -IsFull $true } | Should -Throw '*is a prerelease*' }
  It 'refuses -Prerelease for a stable tag' { { Assert-ReleaseKind -Tag 'v1.0.0' -IsFull $false } | Should -Throw '*is a stable version*' }
  It 'accepts matching pairs' {
    { Assert-ReleaseKind -Tag 'v1.0.0-beta.2' -IsFull $false } | Should -Not -Throw
    { Assert-ReleaseKind -Tag 'v1.0.0' -IsFull $true } | Should -Not -Throw
  }
}

Describe 'Test-ReleaseAbsent' {
  It 'is true only when gh says the release was not found' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 1; Output = @('release not found'); CommandLine = 'gh' } }
    Test-ReleaseAbsent -Repo 'o/n' -Tag 'v1.0.0' | Should -BeTrue
  }
  It 'throws on another gh failure instead of calling it absent' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 4; Output = @('HTTP 401: Bad credentials'); CommandLine = 'gh' } }
    { Test-ReleaseAbsent -Repo 'o/n' -Tag 'v1.0.0' } | Should -Throw '*could not tell*'
  }
  It 'is false when the release exists' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @('v1.0.0'); CommandLine = 'gh' } }
    Test-ReleaseAbsent -Repo 'o/n' -Tag 'v1.0.0' | Should -BeFalse
  }
}

Describe 'Assert-ChangelogMatchesTag' {
  It 'throws when the checkout CHANGELOG.md differs from the tag' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @($(if ($Arguments -contains 'hash-object') { 'aaa' } else { 'bbb' })); CommandLine = 'git' } }
    { Assert-ChangelogMatchesTag -Root 'r' -Tag 'v1.0.0' } | Should -Throw '*differs from the one in v1.0.0*'
  }
  It 'passes when they are the same blob' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @('same'); CommandLine = 'git' } }
    { Assert-ChangelogMatchesTag -Root 'r' -Tag 'v1.0.0' } | Should -Not -Throw
  }
}

Describe 'Test-Signature' {
  It 'reports a failed verification as a result, with the cosign output' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 1; Output = @('no matching signatures'); CommandLine = 'cosign' } }
    $r = Test-Signature -Cosign 'cosign' -Step 'verify' -Arguments @('verify', 'x')
    $r.Ok | Should -BeFalse
    $r.Detail | Should -Match 'no matching signatures'
  }
}

Describe 'Invoke-ReleasePublish' {
  BeforeEach {
    Mock Write-KippleInfo {}
    Mock Get-RepoRoot { $TestDrive }
    Mock Find-Cosign { 'cosign' }
    Mock Test-ReleaseAbsent { $true }
    Mock Assert-ChangelogMatchesTag {}
    Mock Wait-ReleaseRun { [pscustomobject]@{ databaseId = 42; status = 'completed'; conclusion = 'success' } }
    Mock Save-ImageNotesArtifact {
      Set-Content -LiteralPath (Join-Path $Directory 'image-notes.md') -Value ('image notes ghcr.io/o/n@sha256:' + ('d' * 64)) -WhatIf:$false
    }
    Mock Invoke-Native {
      if ($FilePath -eq 'git') { return [pscustomobject]@{ ExitCode = 0; Output = @('abc refs/tags/v1.2.3-beta.1'); CommandLine = 'git' } }
      if ($FilePath -eq 'node') { return [pscustomobject]@{ ExitCode = 0; Output = @('## [1.2.3-beta.1]', '- a change'); CommandLine = 'node' } }
      [pscustomobject]@{ ExitCode = 0; Output = @(); CommandLine = $FilePath }
    }
  }
  It 'creates the release when both signatures verify' {
    Mock Test-Signature { [pscustomobject]@{ Step = $Step; Ok = $true; Detail = 'verified' } }
    $code = Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $false -Repo 'o/n' -TimeoutMinutes 1 | Select-Object -Last 1
    $code | Should -Be 0
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $FilePath -eq 'gh' -and $Arguments -contains 'create' -and $Arguments -contains '--prerelease' }
  }
  It 'stops before creating anything when a signature check fails' {
    Mock Test-Signature { [pscustomobject]@{ Step = $Step; Ok = $false; Detail = 'bad identity' } }
    $code = Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $false -Repo 'o/n' -TimeoutMinutes 1 | Select-Object -Last 1
    $code | Should -Be 1
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $FilePath -eq 'gh' -and $Arguments -contains 'create' }
  }
  It 'with -WhatIf verifies but does not create the release' {
    Mock Test-Signature { [pscustomobject]@{ Step = $Step; Ok = $true; Detail = 'verified' } }
    $code = Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $false -Repo 'o/n' -TimeoutMinutes 1 -WhatIf | Select-Object -Last 1
    $code | Should -Be 0
    Should -Invoke Test-Signature -Times 2
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $FilePath -eq 'gh' -and $Arguments -contains 'create' }
  }
  It 'returns 1 without verifying when the workflow run failed' {
    Mock Wait-ReleaseRun { [pscustomobject]@{ databaseId = 42; status = 'completed'; conclusion = 'failure' } }
    Mock Test-Signature { throw 'must not be called' }
    (Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $false -Repo 'o/n' -TimeoutMinutes 1 | Select-Object -Last 1) | Should -Be 1
  }
  It 'refuses -Full on a prerelease tag before doing anything' {
    { Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $true -Repo 'o/n' -TimeoutMinutes 1 } | Should -Throw '*is a prerelease*'
    Should -Invoke Wait-ReleaseRun -Times 0
  }
  It 'refuses when a release for the tag already exists' {
    Mock Test-ReleaseAbsent { $false }
    { Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $false -Repo 'o/n' -TimeoutMinutes 1 } | Should -Throw -ExpectedMessage '*exists already*'
  }
  It 'refuses when the tag is not on origin' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @(); CommandLine = 'git' } }
    { Invoke-ReleasePublish -Tag 'v1.2.3-beta.1' -IsFull $false -Repo 'o/n' -TimeoutMinutes 1 } | Should -Throw -ExpectedMessage '*is not in o/n*'
  }
}
