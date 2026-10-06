#requires -Version 7.2
#requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.5.0' }
# Tests for scripts/branch-cleanup.ps1. Run: Invoke-Pester scripts/branch-cleanup.Tests.ps1
# The script is dot-sourced (it only defines functions then). git and gh are mocked: no branch is ever deleted.

BeforeAll {
  . (Join-Path $PSScriptRoot 'branch-cleanup.ps1')
  $script:sha1 = '1' * 40
  $script:sha2 = '2' * 40
  $script:sha3 = '3' * 40
  $script:wtRoot = 'C:/repo/.claude/worktrees'

  function script:Get-Plan([hashtable]$Local, [object[]]$Merged = @(), [object[]]$Open = @(), [hashtable]$Remote = @{}, [object[]]$Worktrees = @(), [hashtable]$Blocked = @{}, [string]$DefaultBranch = '', [switch]$IncludeRemote) {
    Get-CleanupPlan -Local $Local -Remote $Remote -Merged $Merged -Open $Open -Worktrees $Worktrees `
      -WorktreeRoot $script:wtRoot -CurrentPath 'C:/repo/.claude/worktrees/me' -Blocked $Blocked -DefaultBranch $DefaultBranch -IncludeRemote:$IncludeRemote
  }
  function script:Merged([string]$Name, [string]$Oid) { [pscustomobject]@{ headRefName = $Name; headRefOid = $Oid; mergedAt = '2026-01-01T00:00:00Z' } }
}

Describe 'Get-CleanupPlan: which branches may go' {
  It 'deletes a branch whose tip equals the merged pull request head' {
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1)
    $p[0].Delete | Should -BeTrue
  }
  It 'never touches main or master, even if a pull request used them' {
    $p = Get-Plan -Local @{ main = $sha1; master = $sha1 } -Merged @((Merged 'main' $sha1), (Merged 'master' $sha1))
    $p.Delete | Should -Not -Contain $true
    $p[0].Reason | Should -Be 'protected branch'
  }
  It 'keeps a branch that has an open pull request' {
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Open @([pscustomobject]@{ headRefName = 'feat/a' })
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Be 'has an open pull request'
  }
  It 'keeps a branch with commits that are not in the merged pull request' {
    $p = Get-Plan -Local @{ 'feat/a' = $sha2 } -Merged @(Merged 'feat/a' $sha1)
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'not in the merged pull request'
  }
  It 'keeps a branch that has no pull request at all' {
    (Get-Plan -Local @{ 'scratch' = $sha1 })[0].Reason | Should -Be 'no merged pull request'
  }
  It 'accepts the tip of any of several merged pull requests that used the same branch name' {
    $p = Get-Plan -Local @{ 'feat/a' = $sha2 } -Merged @((Merged 'feat/a' $sha1), (Merged 'feat/a' $sha2))
    $p[0].Delete | Should -BeTrue
  }
}

Describe 'Get-CleanupPlan: worktrees' {
  It 'plans to remove the worktree of a merged branch under .claude/worktrees' {
    $wt = [pscustomobject]@{ Path = "$wtRoot/old"; Branch = 'feat/a'; Locked = $false }
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Worktrees @($wt)
    $p[0].Delete | Should -BeTrue
    $p[0].WorktreePath | Should -Be "$wtRoot/old"
  }
  It 'keeps a branch whose worktree has uncommitted changes' {
    $wt = [pscustomobject]@{ Path = "$wtRoot/old"; Branch = 'feat/a'; Locked = $false }
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Worktrees @($wt) -Blocked @{ "$wtRoot/old" = 'its worktree has uncommitted changes (e.g. M a)' }
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'uncommitted'
  }
  It 'keeps a branch whose worktree is locked and never plans to remove it' {
    $wt = [pscustomobject]@{ Path = "$wtRoot/busy"; Branch = 'feat/a'; Locked = $true }
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Worktrees @($wt)
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'locked'
    @(Get-CleanupAction -Plan $p).Count | Should -Be 0
  }
  It 'keeps a branch whose worktree status could not be read' {
    $wt = [pscustomobject]@{ Path = "$wtRoot/old"; Branch = 'feat/a'; Locked = $false }
    $blocked = @{ "$wtRoot/old" = (Get-WorktreeBlocker -ExitCode 128 -Lines @('fatal: Unable to create index.lock')) }
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Worktrees @($wt) -Blocked $blocked
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'could not read'
  }
  It 'keeps a branch checked out in a worktree outside .claude/worktrees' {
    $wt = [pscustomobject]@{ Path = 'C:/elsewhere/x'; Branch = 'feat/a'; Locked = $false }
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Worktrees @($wt)
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'outside'
  }
  It 'keeps the branch of the worktree the script runs from' {
    $wt = [pscustomobject]@{ Path = "$wtRoot/me"; Branch = 'feat/a'; Locked = $false }
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -Worktrees @($wt)
    $p[0].Delete | Should -BeFalse
  }
}

Describe 'Get-CleanupPlan: protected and stacked branches' {
  It 'keeps a merged branch that is the base of an open stacked pull request' {
    $open = @([pscustomobject]@{ headRefName = 'feat/child'; baseRefName = 'feat/parent' })
    $p = Get-Plan -Local @{ 'feat/parent' = $sha1 } -Merged @(Merged 'feat/parent' $sha1) -Open $open
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'base branch of an open pull request'
  }
  It 'keeps the repository default branch even when it is not called main' {
    $p = Get-Plan -Local @{ trunk = $sha1 } -Merged @(Merged 'trunk' $sha1) -DefaultBranch 'trunk'
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Be 'protected branch'
  }
}

Describe 'Get-WorktreeBlocker' {
  It 'treats a failed git status as a reason to keep the worktree, never as clean' {
    Get-WorktreeBlocker -ExitCode 128 -Lines @('fatal: detected dubious ownership') | Should -Match 'could not read the worktree status'
  }
  It 'returns nothing for a clean worktree' { Get-WorktreeBlocker -ExitCode 0 -Lines @() | Should -BeNullOrEmpty }
  It 'blocks on modified and untracked files' {
    Get-WorktreeBlocker -ExitCode 0 -Lines @(' M a.go') | Should -Match 'uncommitted'
    Get-WorktreeBlocker -ExitCode 0 -Lines @('?? new.txt') | Should -Match 'uncommitted'
  }
  It 'blocks on ignored files that removal would delete' {
    Get-WorktreeBlocker -ExitCode 0 -Lines @('!! scripts/local/') | Should -Match 'ignored files'
    Get-WorktreeBlocker -ExitCode 0 -Lines @('!! .env') | Should -Match 'ignored files'
  }
  It 'allows ignored build output' {
    Get-WorktreeBlocker -ExitCode 0 -Lines @('!! web/node_modules/', '!! web/dist/', '!! coverage/') | Should -BeNullOrEmpty
  }
}

Describe 'Get-CleanupPlan: remote branches' {
  It 'ignores origin branches without -IncludeRemote' {
    $p = Get-Plan -Local @{} -Remote @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1)
    @($p).Count | Should -Be 0
  }
  It 'deletes the origin copy when it is at the merged head' {
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Remote @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -IncludeRemote
    $p[0].DeleteRemote | Should -BeTrue
  }
  It 'keeps the branch when origin has commits beyond the merged head' {
    $p = Get-Plan -Local @{ 'feat/a' = $sha1 } -Remote @{ 'feat/a' = $sha3 } -Merged @(Merged 'feat/a' $sha1) -IncludeRemote
    $p[0].Delete | Should -BeFalse
    $p[0].Reason | Should -Match 'origin has commits'
  }
  It 'plans a remote-only merged branch for remote deletion only' {
    $p = Get-Plan -Local @{} -Remote @{ 'feat/a' = $sha1 } -Merged @(Merged 'feat/a' $sha1) -IncludeRemote
    $actions = Get-CleanupAction -Plan $p
    @($actions).Kind | Should -Be @('DeleteRemote')
  }
}

Describe 'Get-CleanupAction' {
  It 'orders worktrees, then local branches, then remote branches' {
    $plan = @([pscustomobject]@{ Branch = 'a'; Delete = $true; LocalSha = $sha1; DeleteRemote = $true; WorktreePath = 'C:/repo/.claude/worktrees/a' })
    (Get-CleanupAction -Plan $plan).Kind | Should -Be @('RemoveWorktree', 'DeleteLocal', 'DeleteRemote')
  }
  It 'returns nothing when no branch is deletable' {
    @(Get-CleanupAction -Plan @([pscustomobject]@{ Branch = 'a'; Delete = $false; LocalSha = $sha1; DeleteRemote = $false; WorktreePath = $null })).Count | Should -Be 0
  }
}

Describe 'parsers' {
  It 'ConvertTo-RefMap reads name and sha, skipping HEAD and strange lines' {
    $m = ConvertTo-RefMap -Strip 'origin/' -Lines @("origin/feat/a $sha1", "origin/HEAD $sha2", 'garbage', "other/x $sha3")
    $m.Keys | Should -Be @('feat/a')
  }
  It 'Get-WorktreeEntry reads path, branch and lock state' {
    $lines = @('worktree C:/repo', "HEAD $sha1", 'branch refs/heads/main', '', 'worktree C:/repo/.claude/worktrees/x', "HEAD $sha2", 'branch refs/heads/feat/x', 'locked agent', '', 'worktree C:/repo/.claude/worktrees/d', "HEAD $sha3", 'detached')
    $e = Get-WorktreeEntry -Lines $lines
    $e.Count | Should -Be 3
    $e[1].Branch | Should -Be 'feat/x'
    $e[1].Locked | Should -BeTrue
    $e[2].Branch | Should -BeNullOrEmpty
  }
}

Describe 'Invoke-CleanupAction' {
  It 'removes a worktree with a single --force and never retries with a second one' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 128; Output = @(); CommandLine = 'git' } }
    $a = [pscustomobject]@{ Kind = 'RemoveWorktree'; Branch = 'a'; Target = 'C:/x'; Text = 'remove worktree C:/x' }
    Invoke-CleanupAction -Action $a -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 1
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { @($Arguments | Where-Object { $_ -eq '--force' }).Count -gt 1 }
  }
}
Describe 'Invoke-BranchCleanup' {
  BeforeEach {
    Mock Write-KippleInfo {}
    Mock Get-RepoRoot { 'C:/repo/.claude/worktrees/me' }
    Mock Invoke-Native {
      $joined = $Arguments -join ' '
      $out = switch -Wildcard ($joined) {
        '*for-each-ref*refs/heads*' { @("feat/a $script:sha1", "main $script:sha1") }
        '*for-each-ref*refs/remotes*' { @() }
        '*worktree list*' { @('worktree C:/repo', "HEAD $script:sha1", 'branch refs/heads/main', '', 'worktree C:/repo/.claude/worktrees/me', "HEAD $script:sha1", 'branch refs/heads/me') }
        default { @() }
      }
      [pscustomobject]@{ ExitCode = 0; Output = $out; CommandLine = 'git' }
    }
    Mock Get-PullRequestHead {
      if ($State -eq 'merged') { return @([pscustomobject]@{ headRefName = 'feat/a'; headRefOid = $script:sha1; mergedAt = 'x' }) }
      @()
    }
    Mock Invoke-CleanupAction { $true }
    Mock Get-DefaultBranch { 'main' }
  }
  It 'with -DryRun prints the plan and deletes nothing, without asking' {
    Mock Read-TypedYes { throw 'must not ask' }
    $code = Invoke-BranchCleanup -DryRun | Select-Object -Last 1
    $code | Should -Be 0
    Should -Invoke Invoke-CleanupAction -Times 0
  }
  It 'does nothing when the answer is not exactly yes' {
    Mock Read-TypedYes { $false }
    (Invoke-BranchCleanup | Select-Object -Last 1) | Should -Be 0
    Should -Invoke Invoke-CleanupAction -Times 0
  }
  It 'runs every planned action after a yes' {
    Mock Read-TypedYes { $true }
    (Invoke-BranchCleanup | Select-Object -Last 1) | Should -Be 0
    Should -Invoke Invoke-CleanupAction -Times 1 -ParameterFilter { $Action.Kind -eq 'DeleteLocal' -and $Action.Target -eq 'feat/a' }
  }
  It 'returns 1 when an action fails' {
    Mock Read-TypedYes { $true }
    Mock Invoke-CleanupAction { $false }
    (Invoke-BranchCleanup | Select-Object -Last 1) | Should -Be 1
  }
}
