# Local mirror of .github/workflows/ci.yml: the fast check before pushing.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
#
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
#   pwsh scripts/ci-local.ps1               everything except the Docker build and Trivy
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
#   pwsh scripts/ci-local.ps1 -Docker       also build the image and scan it with Trivy
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
#   pwsh scripts/ci-local.ps1 -Skip web,security     skip a group (go, security, web, links, docker)
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
#   pwsh scripts/ci-local.ps1 -AllLinks     check every link in every *.md, not only the *.md changed against origin/main
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
#
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# It runs the same commands and pinned tool versions as the workflow. Differences: no `-race` (this
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# machine has no C compiler), gofmt is checked on LF-normalized copies (CRLF working copies hide
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# formatting failures), and gitleaks/Trivy run through pinned Docker images. The GitHub Actions run on the exact
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# commit is what "CI green" means; this is the check before pushing. Exit code is non-zero on any failure.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
param([switch]$Docker, [switch]$AllLinks, [string[]]$Skip = @())
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$ErrorActionPreference = 'Continue'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$root = Split-Path -Parent $PSScriptRoot
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Set-Location $root
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# Keep these in step with the env block in .github/workflows/ci.yml.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$GovulncheckVersion = 'v1.8.0'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$StaticcheckVersion = 'v0.8.1'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$GosecVersion       = 'v2.29.0'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$GitleaksVersion    = '8.30.1'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# Images are pinned by tag and digest. Trivy matches the default of the workflow's trivy-action (v0.36.0 -> Trivy v0.70.0).
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$GitleaksImage      = "zricethezav/gitleaks:v$GitleaksVersion@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f"
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$TrivyImage         = 'aquasec/trivy:0.70.0@sha256:be1190afcb28352bfddc4ddeb71470835d16462af68d310f9f4bca710961a41e'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$npmCache           = Join-Path $env:TEMP 'kipple-npm-cache'   # the shared npm cache gives EPERM on this box
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$results = [System.Collections.Generic.List[object]]::new()
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
function Step([string]$Group, [string]$Name, [scriptblock]$Body) {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  if ($Skip -contains $Group) { return }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Write-Host "`n=== [$Group] $Name" -ForegroundColor Cyan
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $sw = [Diagnostics.Stopwatch]::StartNew()
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $global:LASTEXITCODE = 0
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  & $Body
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $code = $LASTEXITCODE
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $results.Add([pscustomobject]@{ Group = $Group; Step = $Name; Ok = ($code -eq 0); Seconds = [int]$sw.Elapsed.TotalSeconds })
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  if ($code -ne 0) { Write-Host "FAILED ($code)" -ForegroundColor Red }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
}
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# ---- go ----
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'go' 'gofmt (LF-normalized)' {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  # CRLF working copies hide gofmt failures, so check LF copies. Bytes, not text, so nothing else changes.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $tmp = Join-Path $env:TEMP 'kipple-gofmt-check'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  foreach ($f in (git ls-files '*.go')) {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    $dest = Join-Path $tmp $f
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    New-Item -ItemType Directory -Force -Path (Split-Path $dest) | Out-Null
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    $text = [System.IO.File]::ReadAllText((Join-Path $root $f)).Replace("`r`n", "`n")
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    [System.IO.File]::WriteAllText($dest, $text, (New-Object System.Text.UTF8Encoding($false)))
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $bad = gofmt -l $tmp
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  if ($bad) { Write-Host "gofmt needed on:`n$($bad -join "`n")"; $global:LASTEXITCODE = 1 } else { $global:LASTEXITCODE = 0 }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
}
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'go' 'go vet' { go vet ./... }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'go' 'go test (shuffled, no -race)' { go test -shuffle=on -timeout 15m -cover ./... }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# ---- security ----
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'security' 'govulncheck' { go run "golang.org/x/vuln/cmd/govulncheck@$GovulncheckVersion" ./... }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'security' 'staticcheck' { go run "honnef.co/go/tools/cmd/staticcheck@$StaticcheckVersion" ./... }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'security' 'gosec (gate: high severity, high confidence)' { go run "github.com/securego/gosec/v2/cmd/gosec@$GosecVersion" -quiet -severity high -confidence high ./... }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'security' 'gitleaks (git history)' {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  # The workflow runs the release binary on a checkout. Here the official image scans a fresh local clone of the
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  # whole history (a git worktree's .git is a pointer file the container cannot follow, which scans nothing).
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $clone = Join-Path $env:TEMP 'kipple-gitleaks-clone'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Remove-Item -Recurse -Force $clone -ErrorAction SilentlyContinue
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  git clone --quiet --no-hardlinks --mirror $root $clone
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  # A mirror has no working tree, so hand the container the repo's allowlist config explicitly.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $cfg = Join-Path $env:TEMP 'kipple-gitleaks-cfg'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  New-Item -ItemType Directory -Force -Path $cfg | Out-Null
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Copy-Item (Join-Path $root '.gitleaks.toml') $cfg -Force
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Copy-Item (Join-Path $root '.gitleaksignore') $cfg -Force
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  docker run --rm -v "${clone}:/repo:ro" -v "${cfg}:/cfg:ro" $GitleaksImage git /repo --no-banner --redact -c /cfg/.gitleaks.toml --gitleaks-ignore-path /cfg/.gitleaksignore
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $code = $LASTEXITCODE
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Remove-Item -Recurse -Force $clone -ErrorAction SilentlyContinue
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $global:LASTEXITCODE = $code
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
}
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# ---- web ----
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'changelog fragments' { node scripts/changelog.mjs check; if ($LASTEXITCODE -eq 0) { node --test scripts/changelog.test.mjs } }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'release tag rules (scripts/release-tags.test.sh)' { bash scripts/release-tags.test.sh }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'toolchain versions (scripts/toolchain.test.mjs)' { node --test scripts/toolchain.test.mjs }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'link checker tests (scripts/check-links.test.mjs)' { node --test scripts/check-links.test.mjs }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'weekly audit report (scripts/audit-report.test.mjs)' { node --test scripts/audit-report.test.mjs }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'npm ci' { Push-Location web; npm ci --ignore-scripts --cache $npmCache --no-audit --no-fund; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'lint' { Push-Location web; npm run lint; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'test and coverage (no threshold)' { Push-Location web; npm run test:coverage; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'build' { Push-Location web; npm run build; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'theme contrast' { Push-Location web; npm run contrast; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'npm audit (prod, high)' { Push-Location web; npm audit --omit=dev --audit-level=high; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'web' 'npm audit signatures' { Push-Location web; npm audit signatures --cache $npmCache; Pop-Location }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# ---- links (network; only the *.md changed against origin/main, since CI runs the full set weekly) ----
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Step 'links' 'markdown links (scripts/check-links.mjs)' {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  if ($AllLinks) { node scripts/check-links.mjs; return }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  # The outer @() keeps a single changed file an array, so it is passed as one name and not splatted into characters.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  $changed = @(@(git diff --name-only --diff-filter=d --merge-base origin/main -- '*.md'; git ls-files --others --exclude-standard -- '*.md') | Sort-Object -Unique)
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  if ($changed.Count -eq 0) { Write-Host 'no changed *.md'; $global:LASTEXITCODE = 0; return }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  node scripts/check-links.mjs @changed
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
}
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
# ---- docker (opt-in: slow) ----
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
if ($Docker) {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Step 'docker' 'build image' {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    # .git is not in the build context, so pass the version in as the workflow does.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    docker build --build-arg "VERSION=$(git describe --tags --always --dirty)" --build-arg "VCS_REF=$(git rev-parse HEAD)" -t kipple:ci .
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  Step 'docker' 'trivy (HIGH,CRITICAL, unfixed ignored)' {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    # Scan a saved tarball rather than mounting the Docker socket into the scanner container.
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    $scan = Join-Path $env:TEMP 'kipple-trivy'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    New-Item -ItemType Directory -Force -Path $scan | Out-Null
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    $tar = Join-Path $scan 'kipple-ci.tar'
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    docker save -o $tar kipple:ci
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    if ($LASTEXITCODE -eq 0) {
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
      docker run --rm -v "${scan}:/scan:ro" $TrivyImage image --input /scan/kipple-ci.tar --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    $code = $LASTEXITCODE
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    Remove-Item -Force $tar -ErrorAction SilentlyContinue
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
    $global:LASTEXITCODE = $code
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
  }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
}
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }

Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Write-Host "`n=== summary ===" -ForegroundColor Cyan
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$results | Format-Table Group, Step, Ok, Seconds -AutoSize | Out-String | Write-Host
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
$failed = @($results | Where-Object { -not $_.Ok })
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
if ($failed.Count) { Write-Host "$($failed.Count) step(s) failed" -ForegroundColor Red; exit 1 }
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
Write-Host 'all steps passed' -ForegroundColor Green
Step 'web' 'prose-only filter (scripts/ci-changes.test.sh, ci-prose.test.mjs)' { bash scripts/ci-changes.test.sh; if ($LASTEXITCODE -eq 0) { node --test scripts/ci-prose.test.mjs } }
