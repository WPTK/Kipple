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
  # A real repository carrying this repository's own .gitignore files, so the tests feed the blocker what git really
  # prints for the real ignore rules, whatever they become. The machine's global excludes file is neutralised.
  function script:New-TempRepo {
    $dir = Join-Path $TestDrive ([guid]::NewGuid().ToString('N').Substring(0, 8))
    $null = New-Item -ItemType Directory -Path $dir
    $none = Join-Path $TestDrive 'no-global-excludes'
    Set-Content -LiteralPath $none -Value ''
    $repoRoot = Split-Path -Parent $PSScriptRoot
    Copy-Item -LiteralPath (Join-Path $repoRoot '.gitignore') -Destination (Join-Path $dir '.gitignore')
    $null = New-Item -ItemType Directory -Force -Path (Join-Path $dir 'web')
    Copy-Item -LiteralPath (Join-Path $repoRoot 'web/.gitignore') -Destination (Join-Path $dir 'web/.gitignore')
    $null = New-Item -ItemType File -Force -Path (Join-Path $dir 'web/dist/.gitkeep')
    $null = Invoke-Native -FilePath git -Arguments '-C', $dir, 'init', '--quiet' -Step 'git init'
    $null = Invoke-Native -FilePath git -Arguments '-C', $dir, 'config', 'core.excludesFile', $none -Step 'git config'
    $null = Invoke-Native -FilePath git -Arguments '-C', $dir, 'add', '-A' -Step 'git add'
    $null = Invoke-Native -FilePath git -Arguments '-C', $dir, '-c', 'user.name=t', '-c', 'user.email=t@example.com', 'commit', '--quiet', '-m', 'init' -Step 'git commit'
    return $dir
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
  It 'allows the ignored lines of a real built agent worktree, as git prints them' {
    $repo = New-TempRepo
    foreach ($p in 'web/dist/assets/app.js', 'web/dist/apple-touch-icon.png', 'web/node_modules/pkg/index.js', 'web/tsconfig.tsbuildinfo', 'kipple.exe', 'web/coverage/lcov.info', '.claude/settings.local.json') {
      $null = New-Item -ItemType File -Force -Path (Join-Path $repo $p)
    }
    $status = Invoke-Native -FilePath git -Arguments '-C', $repo, 'status', '--porcelain', '--ignored=matching' -Step 'status'
    ($status.Output | Where-Object { $_ -match '^!! ' }).Count | Should -BeGreaterThan 3   # the fixture really produced ignored lines
    Get-WorktreeBlocker -ExitCode 0 -Lines $status.Output | Should -BeNullOrEmpty
  }
  It 'keeps a worktree that holds a backup of the permission file (a backup exists because someone wanted it)' {
    Get-WorktreeBlocker -ExitCode 0 -Lines @('!! .claude/settings.local.json.bak.20261005') | Should -Match 'ignored files'
  }
  It 'blocks on a real ignored file that is not build output, and on a real untracked one' {
    $repo = New-TempRepo
    $null = New-Item -ItemType File -Force -Path (Join-Path $repo 'web/dist/assets/app.js')
    $null = New-Item -ItemType File -Force -Path (Join-Path $repo 'scripts/local/deploy.config.psd1')
    $status = Invoke-Native -FilePath git -Arguments '-C', $repo, 'status', '--porcelain', '--ignored=matching' -Step 'status'
    Get-WorktreeBlocker -ExitCode 0 -Lines $status.Output | Should -Match 'ignored files.*scripts/local'
    $null = New-Item -ItemType File -Force -Path (Join-Path $repo 'notes.txt')
    $status = Invoke-Native -FilePath git -Arguments '-C', $repo, 'status', '--porcelain', '--ignored=matching' -Step 'status'
    Get-WorktreeBlocker -ExitCode 0 -Lines $status.Output | Should -Match 'uncommitted'
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
    $plan = @([pscustomobject]@{ Branch = 'a'; Delete = $true; LocalSha = $sha1; RemoteSha = $sha1; DeleteRemote = $true; WorktreePath = 'C:/repo/.claude/worktrees/a' })
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

Describe 'Invoke-CleanupAction: nothing is deleted on the word of the plan alone' {
  BeforeEach {
    Mock Write-KippleInfo {}
    # git as the worktree and the repository answer it: clean, on feat/a at $sha1, one worktree listed.
    $script:status = @(); $script:statusCode = 0
    $script:symref = 'refs/heads/feat/a'; $script:headSha = $sha1
    $script:listCode = 0; $script:listOut = @('worktree C:/repo', "HEAD $sha1", 'branch refs/heads/main')
    $script:updateCode = 0
    Mock Invoke-Native {
      $out = @(); $code = 0
      if ($Arguments -contains 'status') { $out = $script:status; $code = $script:statusCode }
      elseif ($Arguments -contains 'symbolic-ref') { $out = @($script:symref) }
      elseif ($Arguments -contains 'rev-parse') { $out = @($script:headSha) }
      elseif ($Arguments -contains 'list') { $out = $script:listOut; $code = $script:listCode }
      elseif ($Arguments -contains 'update-ref') { $code = $script:updateCode }
      # Like the real Invoke-Native: a non-zero exit throws unless the caller passed -AllowFailure.
      if ($code -ne 0 -and -not $AllowFailure) { throw "git failed ($code)" }
      [pscustomobject]@{ ExitCode = $code; Output = $out; CommandLine = 'git' }
    }
    $script:wt = [pscustomobject]@{ Kind = 'RemoveWorktree'; Branch = 'feat/a'; Target = 'C:/x'; Sha = $sha1; Text = 'remove worktree C:/x' }
    $script:del = [pscustomobject]@{ Kind = 'DeleteLocal'; Branch = 'feat/a'; Target = 'feat/a'; Sha = $sha1; Text = 'delete local branch feat/a' }
    $script:rem = [pscustomobject]@{ Kind = 'DeleteRemote'; Branch = 'feat/a'; Target = 'feat/a'; Sha = $sha1; Text = 'delete origin/feat/a' }
  }
  It 'removes a worktree without --force after a fresh clean status, still on its branch at the planned commit' {
    Invoke-CleanupAction -Action $wt -RepoRoot 'C:/repo' | Should -BeTrue
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $Arguments -contains 'remove' -and $Arguments -notcontains '--force' }
  }
  It 'keeps a worktree that became dirty after the plan and removes nothing' {
    $script:status = @('?? new-work.txt')
    Invoke-CleanupAction -Action $wt -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains 'remove' }
  }
  It 'keeps a worktree whose status can no longer be read' {
    $script:statusCode = 128
    Invoke-CleanupAction -Action $wt -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains 'remove' }
  }
  It 'keeps a worktree whose HEAD was detached after the plan (a commit there would be lost with it)' {
    $script:symref = ''
    Invoke-CleanupAction -Action $wt -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains 'remove' }
  }
  It 'keeps a worktree whose HEAD moved to another commit after the plan' {
    $script:headSha = $sha2
    Invoke-CleanupAction -Action $wt -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains 'remove' }
  }
  It 'deletes a local branch only at the planned sha (update-ref with the old value) and removes its branch config' {
    Invoke-CleanupAction -Action $del -RepoRoot 'C:/repo' | Should -BeTrue
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $Arguments -contains 'update-ref' -and $Arguments -contains '-d' -and $Arguments -contains 'refs/heads/feat/a' -and $Arguments[-1] -eq $sha1 }
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $Arguments -contains 'config' -and $Arguments -contains '--remove-section' -and $Arguments -contains 'branch.feat/a' }
  }
  It 'reports a local branch that gained a commit since the plan as not deleted, and leaves its config alone' {
    $script:updateCode = 1
    Invoke-CleanupAction -Action $del -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains '--remove-section' }
  }
  It 'does not delete a local branch that is checked out in a worktree' {
    $script:listOut = @('worktree C:/repo', "HEAD $sha1", 'branch refs/heads/feat/a')
    Invoke-CleanupAction -Action $del -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains 'update-ref' }
  }
  It 'returns false instead of throwing when the worktrees cannot be listed' {
    $script:listCode = 128
    { Invoke-CleanupAction -Action $del -RepoRoot 'C:/repo' } | Should -Not -Throw
    Invoke-CleanupAction -Action $del -RepoRoot 'C:/repo' | Should -BeFalse
    Should -Invoke Invoke-Native -Times 0 -ParameterFilter { $Arguments -contains 'update-ref' }
  }
  It 'deletes a remote branch with a lease on the planned sha' {
    Invoke-CleanupAction -Action $rem -RepoRoot 'C:/repo' | Should -BeTrue
    Should -Invoke Invoke-Native -Times 1 -ParameterFilter { $Arguments -contains "--force-with-lease=refs/heads/feat/a:$sha1" -and $Arguments -contains ':refs/heads/feat/a' -and $Arguments -notcontains '--delete' }
  }
  It 'reports a remote branch that moved since the plan as not deleted' {
    Mock Invoke-Native { [pscustomobject]@{ ExitCode = 1; Output = @('stale info'); CommandLine = 'git' } }
    Invoke-CleanupAction -Action $rem -RepoRoot 'C:/repo' | Should -BeFalse
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
  Context 'with an agent worktree on the merged branch' {
    BeforeEach {
      Mock Invoke-Native {
        $joined = $Arguments -join ' '
        $out = @(); $code = 0
        switch -Wildcard ($joined) {
          '*for-each-ref*refs/heads*' { $out = @("feat/a $script:sha1", "main $script:sha1") }
          '*worktree list*' { $out = @('worktree C:/repo', "HEAD $script:sha1", 'branch refs/heads/main', '', 'worktree C:/repo/.claude/worktrees/old', "HEAD $script:sha1", 'branch refs/heads/feat/a', '', 'worktree C:/repo/.claude/worktrees/me', "HEAD $script:sha1", 'branch refs/heads/me') }
          '*status*' { $out = $script:statusLines; $code = $script:statusCode }
        }
        [pscustomobject]@{ ExitCode = $code; Output = $out; CommandLine = 'git' }
      }
      Mock Read-TypedYes { $true }
    }
    It 'plans nothing when git status fails in that worktree (fail closed at the call site)' {
      $script:statusLines = @(); $script:statusCode = 128   # no output: only the exit code can block
      $null = Invoke-BranchCleanup
      Should -Invoke Invoke-CleanupAction -Times 0
    }
    It 'plans nothing when that worktree holds an ignored file that is not build output' {
      $script:statusLines = @('!! .env'); $script:statusCode = 0
      $null = Invoke-BranchCleanup
      Should -Invoke Invoke-CleanupAction -Times 0
    }
    It 'asks git for ignored files too (so the check can see them)' {
      $script:statusLines = @(); $script:statusCode = 0
      $null = Invoke-BranchCleanup
      Should -Invoke Invoke-Native -ParameterFilter { $Arguments -contains 'status' -and $Arguments -contains '--ignored=matching' }
      Should -Invoke Invoke-CleanupAction -Times 1 -ParameterFilter { $Action.Kind -eq 'RemoveWorktree' }
    }
  }
}
