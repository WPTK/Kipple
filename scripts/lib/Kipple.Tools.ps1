#requires -Version 7
<#
.SYNOPSIS
  Shared helpers for the release tooling scripts (release-gates, release-publish, branch-cleanup, pr-ready).
.DESCRIPTION
  Dot-source this file; it defines functions only and changes nothing when loaded:

      . (Join-Path $PSScriptRoot 'lib/Kipple.Tools.ps1')

  The rules every script follows, and why the helpers exist:
    - Every native command (git, gh, node, npm, go, cosign) goes through Invoke-Native, so a non-zero exit is
      always an error that names the step, the command with its arguments, and the likely fix.
    - Arguments are always an array, never a command line built from a string.
    - Messages go to the information stream (Write-KippleInfo); data is returned as objects.
    - A script's body runs inside Invoke-ToolMain, which maps the outcome to the exit code:
      0 ok, 1 a gate or check failed, 2 usage or environment error (tool missing, bad input, command failed).
  Run with -Verbose to see every step and every command line.
  Tests: scripts/lib/Kipple.Tools.Tests.ps1 (see scripts/README.md).
#>
Set-StrictMode -Version Latest

function Write-KippleInfo {
  <#
  .SYNOPSIS
    Writes a message for the person running the script (information stream, always shown).
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][AllowEmptyString()][string]$Message)
  Write-Information -MessageData $Message -InformationAction Continue
}

function Format-CommandLine {
  <#
  .SYNOPSIS
    Renders a command and its arguments as one string for messages. Display only; never executed.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param(
    [Parameter(Mandatory)][string]$FilePath,
    [string[]]$Arguments = @()
  )
  $parts = foreach ($a in $Arguments) {
    if ($a -match '\s') { '"' + $a + '"' } else { $a }
  }
  return (@($FilePath) + @($parts)) -join ' '
}

function Invoke-Native {
  <#
  .SYNOPSIS
    Runs one native command, captures its output and throws a descriptive error when it exits non-zero.
  .DESCRIPTION
    The only way scripts run git, gh, node, npm, go or cosign. Output (stdout and stderr, merged) is returned in
    the result object; with -LogPath it is also written to that file, so long runs can be summarised instead of
    streamed. Never pass secrets as arguments: the command line is shown in error messages.
  .PARAMETER FilePath
    The executable (a name on PATH or a full path).
  .PARAMETER Arguments
    Arguments, one array element each. No quoting is needed for paths with spaces.
  .PARAMETER Step
    Plain-language name of what the command is for, shown in errors ("fetch origin").
  .PARAMETER Fix
    What to try when it fails, shown in errors.
  .PARAMETER WorkingDirectory
    Directory to run in; restored afterwards.
  .PARAMETER AllowFailure
    Return the non-zero exit code instead of throwing (for commands whose failure is an answer, like go test).
  .PARAMETER LogPath
    Also write the full output to this file.
  .OUTPUTS
    PSCustomObject with ExitCode, Output (string[]) and CommandLine.
  .EXAMPLE
    (Invoke-Native -FilePath git -Arguments 'rev-parse', 'HEAD' -Step 'read HEAD').Output[0]
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param(
    [Parameter(Mandatory)][string]$FilePath,
    [string[]]$Arguments = @(),
    [Parameter(Mandatory)][string]$Step,
    [string]$Fix = 'run the command by hand and read its output',
    [string]$WorkingDirectory,
    [switch]$AllowFailure,
    [string]$LogPath
  )
  $commandLine = Format-CommandLine -FilePath $FilePath -Arguments $Arguments
  Write-Verbose "[$Step] $commandLine"
  $pushed = $false
  try {
    if ($WorkingDirectory) { Push-Location -LiteralPath $WorkingDirectory; $pushed = $true }
    $lines = @(& $FilePath @Arguments 2>&1 | ForEach-Object { "$_" })
    $code = $LASTEXITCODE
  } catch {
    throw "Step '$Step' could not start '$FilePath': $($_.Exception.Message). Likely fix: install it and put it on PATH."
  } finally {
    if ($pushed) { Pop-Location }
  }
  if ($LogPath) { Set-Content -LiteralPath $LogPath -Value $lines }
  if ($code -ne 0 -and -not $AllowFailure) {
    $tail = ($lines | Select-Object -Last 15) -join [Environment]::NewLine
    throw "Step '$Step' failed: '$commandLine' exited $code. Likely fix: $Fix.$([Environment]::NewLine)Last output:$([Environment]::NewLine)$tail"
  }
  return [pscustomobject]@{ ExitCode = $code; Output = $lines; CommandLine = $commandLine }
}

function Get-RepoRoot {
  <#
  .SYNOPSIS
    The top-level directory of the git working tree that contains the given path.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Path)
  $r = Invoke-Native -FilePath git -Arguments '-C', $Path, 'rev-parse', '--show-toplevel' `
    -Step 'find the repository root' -Fix 'run the script from inside a Kipple checkout'
  return $r.Output[0].Trim()
}

function Test-FullSha {
  <#
  .SYNOPSIS
    True when the text is a full 40-character hex commit sha.
  #>
  [CmdletBinding()]
  [OutputType([bool])]
  param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)
  return $Text -match '^[0-9a-fA-F]{40}$'
}

function Resolve-CommitSha {
  <#
  .SYNOPSIS
    Resolves a ref to a full commit sha, failing when it is not a commit in this repository.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([Parameter(Mandatory)][string]$Ref, [Parameter(Mandatory)][string]$RepoRoot)
  $r = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'rev-parse', '--verify', '--quiet', "$Ref^{commit}" `
    -Step "resolve $Ref" -Fix 'git fetch origin, then check the ref or sha' -AllowFailure
  if ($r.ExitCode -ne 0 -or -not (Test-FullSha $r.Output[0].Trim())) {
    throw "Step 'resolve $Ref' failed: '$Ref' is not a commit here. Likely fix: git fetch origin, then check the ref or sha."
  }
  return $r.Output[0].Trim().ToLowerInvariant()
}

function New-DetachedWorktree {
  <#
  .SYNOPSIS
    Creates a detached worktree of a commit. Always pair with Remove-DetachedWorktree in a finally block.
  #>
  [CmdletBinding(SupportsShouldProcess)]
  param(
    [Parameter(Mandatory)][string]$RepoRoot,
    [Parameter(Mandatory)][string]$Path,
    [Parameter(Mandatory)][ValidatePattern('^[0-9a-fA-F]{40}$')][string]$Sha
  )
  if (Test-Path -LiteralPath $Path) {
    throw "Step 'create worktree' failed: $Path already exists. Likely fix: git worktree remove --force '$Path' (a previous run crashed or kept it)."
  }
  if ($PSCmdlet.ShouldProcess($Path, "git worktree add --detach $Sha")) {
    $null = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'worktree', 'add', '--detach', '--quiet', $Path, $Sha `
      -Step 'create worktree' -Fix 'check disk space and that the sha exists (git fetch origin)'
  }
}

function Remove-DetachedWorktree {
  <#
  .SYNOPSIS
    Removes a worktree made by New-DetachedWorktree. Does nothing when it is already gone.
  #>
  [CmdletBinding(SupportsShouldProcess)]
  param([Parameter(Mandatory)][string]$RepoRoot, [Parameter(Mandatory)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) { return }
  if ($PSCmdlet.ShouldProcess($Path, 'git worktree remove --force')) {
    $null = Invoke-Native -FilePath git -Arguments '-C', $RepoRoot, 'worktree', 'remove', '--force', $Path `
      -Step 'remove worktree' -Fix "git worktree remove --force --force '$Path', then git worktree prune" -AllowFailure
  }
}

function Find-Cosign {
  <#
  .SYNOPSIS
    Finds the cosign executable on PATH, or in the WinGet package folder, and returns its full path.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([string]$WinGetRoot = (Join-Path $env:LOCALAPPDATA 'Microsoft\WinGet\Packages'))
  $onPath = Get-Command -Name cosign -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($onPath) { return $onPath.Source }
  if (Test-Path -LiteralPath $WinGetRoot) {
    $packages = Get-ChildItem -LiteralPath $WinGetRoot -Directory -Filter 'Sigstore.Cosign*'
    foreach ($p in $packages) {
      $exe = Get-ChildItem -LiteralPath $p.FullName -File -Filter 'cosign*.exe' | Select-Object -First 1
      if ($exe) { return $exe.FullName }
    }
  }
  throw "Step 'find cosign' failed: cosign is not on PATH or under $WinGetRoot. Likely fix: winget install Sigstore.Cosign (version 3 or later)."
}

function Get-GoFailure {
  <#
  .SYNOPSIS
    Extracts the failing packages and tests from `go test` output.
  .OUTPUTS
    PSCustomObject with Packages and Tests (string arrays, sorted, unique).
  #>
  [CmdletBinding()]
  [OutputType([pscustomobject])]
  param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)
  $pkgs = [regex]::Matches($Text, '(?m)^FAIL[ \t]+(\S+)[ \t]') | ForEach-Object { $_.Groups[1].Value }
  $tests = [regex]::Matches($Text, '(?m)^\s*--- FAIL: (\S+)') | ForEach-Object { $_.Groups[1].Value }
  return [pscustomobject]@{
    Packages = @($pkgs | Sort-Object -Unique)
    Tests    = @($tests | Sort-Object -Unique)
  }
}

function Get-RepoSlug {
  <#
  .SYNOPSIS
    The owner/name of the repository gh is pointed at, from `gh repo view`.
  #>
  [CmdletBinding()]
  [OutputType([string])]
  param([string]$RepoRoot = (Get-Location).Path)
  $r = Invoke-Native -FilePath gh -Arguments 'repo', 'view', '--json', 'nameWithOwner', '--jq', '.nameWithOwner' `
    -Step 'read the repository name' -Fix 'gh auth login, and run inside the checkout' -WorkingDirectory $RepoRoot
  $slug = $r.Output[0].Trim()
  if ($slug -notmatch '^[\w.-]+/[\w.-]+$') {
    throw "Step 'read the repository name' failed: gh printed '$slug', not owner/name. Likely fix: gh auth status."
  }
  return $slug
}

function Invoke-ToolMain {
  <#
  .SYNOPSIS
    Runs a script's body and turns the outcome into the process exit code. Calls exit.
  .DESCRIPTION
    The body returns its exit code as the last [int] it emits (0 ok, 1 a gate or check failed); other output
    passes through as the script's result objects. An exception is printed (name, message) and exits 2.
    Call it as the last line of a script, behind the dot-source guard, so tests can load the functions.
  #>
  [CmdletBinding()]
  param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][scriptblock]$Body)
  $code = 2
  try {
    $emitted = @(& $Body)
    $ints = @($emitted | Where-Object { $_ -is [int] })
    $code = if ($ints.Count) { $ints[-1] } else { 0 }
    $emitted | Where-Object { $_ -isnot [int] }
  } catch {
    $host.UI.WriteErrorLine("${Name}: $($_.Exception.Message)")
    Write-Verbose ($_.ScriptStackTrace)
    $code = 2
  }
  exit $code
}

