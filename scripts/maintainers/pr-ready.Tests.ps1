#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/maintainers/pr-ready.ps1. Run: Invoke-Pester scripts/maintainers/pr-ready.Tests.ps1
# The script is dot-sourced (it only defines functions then). gh is mocked: nothing is merged.

BeforeAll {
  $script:script = Join-Path $PSScriptRoot 'pr-ready.ps1'
  . $script:script -Number 1
  $script:pwshExe = (Get-Process -Id $PID).Path
  $script:head = 'a' * 40

  function script:New-Pr([hashtable]$Override = @{}) {
    $pr = @{
      number = 7; state = 'OPEN'; isDraft = $false; headRefOid = $script:head; baseRefName = 'main'
      mergeStateStatus = 'CLEAN'; reviewDecision = ''; closingIssuesReferences = @()
      statusCheckRollup = @([pscustomobject]@{ name = 'go'; status = 'COMPLETED'; conclusion = 'SUCCESS' })
    }
    foreach ($k in $Override.Keys) { $pr[$k] = $Override[$k] }
    [pscustomobject]$pr
  }
}

Describe 'argument validation' {
  It 'requires a PR number' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -WhatIf 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
  It 'rejects a malformed repository name' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Number 3 -Repo 'not a repo' 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
  It 'rejects a PR number below 1' {
    $null = & $script:pwshExe -NoProfile -NonInteractive -File $script:script -Number 0 2>&1
    $LASTEXITCODE | Should -Not -Be 0
  }
}

Describe 'Get-CheckSummary' {
  It 'separates failing and pending check runs and ignores skipped and neutral ones' {
    $rollup = @(
      [pscustomobject]@{ name = 'go'; status = 'COMPLETED'; conclusion = 'SUCCESS' }
      [pscustomobject]@{ name = 'web'; status = 'COMPLETED'; conclusion = 'FAILURE' }
      [pscustomobject]@{ name = 'trivy'; status = 'IN_PROGRESS'; conclusion = '' }
      [pscustomobject]@{ name = 'docs'; status = 'COMPLETED'; conclusion = 'SKIPPED' }
    )
    $s = Get-CheckSummary -Rollup $rollup
    $s.Failing | Should -Be @('web')
    $s.Pending | Should -Be @('trivy')
    $s.Total | Should -Be 4
  }
  It 'understands commit statuses (context and state)' {
    $s = Get-CheckSummary -Rollup @([pscustomobject]@{ context = 'ci/x'; state = 'FAILURE' }, [pscustomobject]@{ context = 'ci/y'; state = 'PENDING' })
    $s.Failing | Should -Be @('ci/x')
    $s.Pending | Should -Be @('ci/y')
  }
}

Describe 'Get-PrVerdict' {
  It 'is READY for an open, clean, up-to-date PR with passing checks' {
    $v = Get-PrVerdict -Pr (New-Pr) -AncestorStatus 'ahead'
    $v.Ready | Should -BeTrue
    $v.Text | Should -Match '^READY'
    $v.HeadSha | Should -Be $head
  }
  It 'prints an advisory check that fails or is pending and does not block on it' {
    $roll = @(
      [pscustomobject]@{ name = 'web'; status = 'COMPLETED'; conclusion = 'SUCCESS' },
      [pscustomobject]@{ name = 'Browser UAT'; status = 'COMPLETED'; conclusion = 'FAILURE' }
    )
    $v = Get-PrVerdict -Pr (New-Pr @{ statusCheckRollup = $roll }) -AncestorStatus 'ahead'
    $v.Ready | Should -BeTrue
    $v.Text | Should -Match 'advisory, not blocking: Browser UAT: failing'
    $roll[1].status = 'IN_PROGRESS'
    (Get-PrVerdict -Pr (New-Pr @{ statusCheckRollup = $roll }) -AncestorStatus 'ahead').Text | Should -Match 'Browser UAT: pending'
  }
  It 'is BLOCKED, naming the check, when a check fails' {
    $pr = New-Pr @{ statusCheckRollup = @([pscustomobject]@{ name = 'web'; status = 'COMPLETED'; conclusion = 'FAILURE' }) }
    $v = Get-PrVerdict -Pr $pr -AncestorStatus 'ahead'
    $v.Ready | Should -BeFalse
    $v.Text | Should -Match 'BLOCKED because.*failing checks: web'
  }
  It 'is BLOCKED while a check is pending' {
    $pr = New-Pr @{ statusCheckRollup = @([pscustomobject]@{ name = 'go'; status = 'QUEUED'; conclusion = '' }) }
    (Get-PrVerdict -Pr $pr -AncestorStatus 'ahead').Text | Should -Match 'pending checks: go'
  }
  It 'is BLOCKED when the base branch is not merged into the head' {
    (Get-PrVerdict -Pr (New-Pr) -AncestorStatus 'diverged').Text | Should -Match 'not merged into the head'
  }
  It 'treats identical as up to date' {
    (Get-PrVerdict -Pr (New-Pr) -AncestorStatus 'identical').Ready | Should -BeTrue
  }
  It 'is BLOCKED for a draft, a merged PR, a dirty merge state and requested changes, all listed' {
    $pr = New-Pr @{ isDraft = $true; state = 'MERGED'; mergeStateStatus = 'DIRTY'; reviewDecision = 'CHANGES_REQUESTED' }
    $v = Get-PrVerdict -Pr $pr -AncestorStatus 'ahead'
    $v.Reasons.Count | Should -Be 4
  }
  It 'is BLOCKED when no checks have reported' {
    (Get-PrVerdict -Pr (New-Pr @{ statusCheckRollup = @() }) -AncestorStatus 'ahead').Text | Should -Match 'no checks'
  }
}

Describe 'Invoke-PrReady' {
  BeforeEach {
    Mock Write-KippleInfo {}
    Mock Get-AncestorStatus { 'ahead' }
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 0; Output = @(); CommandLine = 'gh' } }
  }
  It 'does not merge without -Merge' {
    Mock Get-PullRequestState { New-Pr }
    (Invoke-PrReady -Number 7 -DoMerge $false -Repo 'o/n' | Select-Object -Last 1) | Should -Be 0
    Should -Invoke Invoke-Native -Times 0
  }
  It 'does not merge a BLOCKED PR even with -Merge' {
    Mock Get-PullRequestState { New-Pr @{ mergeStateStatus = 'BEHIND' } }
    (Invoke-PrReady -Number 7 -DoMerge $true -Repo 'o/n' | Select-Object -Last 1) | Should -Be 1
    Should -Invoke Invoke-Native -Times 0
  }
  It 'merges with the exact head sha and verifies the PR and its issues' {
    $script:reads = 0
    Mock Get-PullRequestState {
      $script:reads++
      if ($script:reads -eq 1) { return New-Pr @{ closingIssuesReferences = @([pscustomobject]@{ number = 12 }) } }
      New-Pr @{ state = 'MERGED' }
    }
    Mock Test-IssueClosed { $true }
    (Invoke-PrReady -Number 7 -DoMerge $true -Repo 'o/n' | Select-Object -Last 1) | Should -Be 0
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $Arguments -contains '--squash' -and $Arguments -contains '--match-head-commit' -and $Arguments -contains $script:head }
    Should -Invoke Test-IssueClosed -Times 1 -ParameterFilter { $IssueNumber -eq 12 }
  }
  It 'returns 1 when a closing issue is still open after the merge' {
    $script:reads = 0
    Mock Get-PullRequestState {
      $script:reads++
      if ($script:reads -eq 1) { return New-Pr @{ closingIssuesReferences = @([pscustomobject]@{ number = 12 }) } }
      New-Pr @{ state = 'MERGED' }
    }
    Mock Test-IssueClosed { $false }
    (Invoke-PrReady -Number 7 -DoMerge $true -Repo 'o/n' | Select-Object -Last 1) | Should -Be 1
  }
  It 'looks a closing issue up in its own repository and reports a failed lookup without throwing' {
    $script:reads = 0
    Mock Get-PullRequestState {
      $script:reads++
      $other = [pscustomobject]@{ name = 'other'; owner = [pscustomobject]@{ login = 'them' } }
      if ($script:reads -eq 1) { return New-Pr @{ closingIssuesReferences = @([pscustomobject]@{ number = 5; repository = $other }) } }
      New-Pr @{ state = 'MERGED' }
    }
    Mock Test-IssueClosed { throw 'not found' }
    (Invoke-PrReady -Number 7 -DoMerge $true -Repo 'o/n' | Select-Object -Last 1) | Should -Be 1
    Should -Invoke Test-IssueClosed -Times 1 -ParameterFilter { $Repo -eq 'them/other' }
  }
  It 'returns 1 when the PR is not MERGED after the merge command' {
    Mock Get-PullRequestState { New-Pr }
    (Invoke-PrReady -Number 7 -DoMerge $true -Repo 'o/n' | Select-Object -Last 1) | Should -Be 1
  }
  It 'with -WhatIf prints the merge command and runs nothing' {
    Mock Get-PullRequestState { New-Pr }
    (Invoke-PrReady -Number 7 -DoMerge $true -Repo 'o/n' -WhatIf | Select-Object -Last 1) | Should -Be 0
    Should -Invoke Invoke-Native -Times 0
  }
}

Describe 'advisory checks' {
  It 'matches a matrix leg of an advisory workflow but not a look-alike' {
    Test-AdvisoryCheck -Name 'Browser UAT' | Should -BeTrue
    Test-AdvisoryCheck -Name 'Browser UAT (webkit)' | Should -BeTrue
    Test-AdvisoryCheck -Name 'Browser UATX' | Should -BeFalse
    Test-AdvisoryCheck -Name 'go test (api)' | Should -BeFalse
  }
  It 'does not block on a failing leg' {
    $roll = @([pscustomobject]@{ name = 'Browser UAT (firefox)'; status = 'COMPLETED'; conclusion = 'FAILURE' })
    $v = Get-PrVerdict -Pr (New-Pr @{ statusCheckRollup = $roll }) -AncestorStatus 'ahead'
    $v.Ready | Should -BeTrue
    $v.Text | Should -Match 'Browser UAT \(firefox\): failing'
  }
}

Describe 'failure detail' {
  It 'reads the job id from an Actions details URL and nothing else' {
    Get-JobIdFromUrl -Url 'https://github.com/o/r/actions/runs/123/job/456' | Should -Be '456'
    Get-JobIdFromUrl -Url 'https://codecov.io/gh/o/r' | Should -BeNullOrEmpty
    Get-JobIdFromUrl -Url '' | Should -BeNullOrEmpty
  }
  It 'names the first failed step' {
    $job = [pscustomobject]@{ name = 'web'; steps = @(
        [pscustomobject]@{ name = 'Checkout'; conclusion = 'success' },
        [pscustomobject]@{ name = 'Unit tests'; conclusion = 'failure' },
        [pscustomobject]@{ name = 'Upload'; conclusion = 'skipped' }) }
    Get-FailedStepName -Job $job | Should -Be 'Unit tests'
    Get-FailedStepName -Job ([pscustomobject]@{ name = 'x'; steps = @() }) | Should -BeNullOrEmpty
  }
  It 'keeps the last N non-empty log lines without timestamps or escapes' {
    $log = (1..50 | ForEach-Object { "2026-10-25T10:00:00.0000000Z line $_" }) -join "`n"
    $log += "`n`n2026-10-25T10:00:01.0000000Z `e[31mred`e[0m"
    $tail = Get-LogTail -Text $log -Lines 30
    $tail.Count | Should -Be 30
    $tail[-1] | Should -Be 'red'
    $tail[0] | Should -Be 'line 22'
  }
  It 'prints the job, step and tail of a failed required check, and skips advisory and passing ones' {
    Mock Invoke-Native {
      if ($Arguments -contains 'repos/o/r/actions/jobs/9') { [pscustomobject]@{ Output = @('{"name":"web","steps":[{"name":"Unit tests","conclusion":"failure"}]}') } }
      else { [pscustomobject]@{ Output = @('2026-10-25T10:00:00.0000000Z boom') } }
    }
    $roll = @(
      [pscustomobject]@{ name = 'web'; status = 'COMPLETED'; conclusion = 'FAILURE'; detailsUrl = 'https://github.com/o/r/actions/runs/1/job/9' },
      [pscustomobject]@{ name = 'Browser UAT (webkit)'; status = 'COMPLETED'; conclusion = 'FAILURE'; detailsUrl = 'https://github.com/o/r/actions/runs/1/job/10' },
      [pscustomobject]@{ name = 'go'; status = 'COMPLETED'; conclusion = 'SUCCESS'; detailsUrl = 'https://github.com/o/r/actions/runs/1/job/11' })
    $out = @(Get-FailureDetail -Repo 'o/r' -Rollup $roll)
    $out.Count | Should -Be 1
    $out[0] | Should -Match 'FAILED: web / step: Unit tests'
    $out[0] | Should -Match 'boom'
  }
}
