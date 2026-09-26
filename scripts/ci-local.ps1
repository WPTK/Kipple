# Local stand-in for .github/workflows/ci.yml, for when GitHub Actions minutes run out.
#
#   pwsh scripts/ci-local.ps1               everything except the Docker build and Trivy
#   pwsh scripts/ci-local.ps1 -Docker       also build the image and scan it with Trivy
#   pwsh scripts/ci-local.ps1 -Skip web,security     skip a group (go, security, web, docker)
#
# It runs the same commands and pinned tool versions as the workflow. Differences: no `-race` (this
# machine has no C compiler), gofmt is checked on LF-normalized copies (CRLF working copies hide
# formatting failures), and gitleaks/Trivy run through Docker images. A green run here is what
# "CI green" means for a push while Actions is unavailable. Exit code is non-zero on any failure.
param([switch]$Docker, [string[]]$Skip = @())

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

# Keep these in step with the env block in .github/workflows/ci.yml.
$GovulncheckVersion = 'v1.8.0'
$StaticcheckVersion = 'v0.8.1'
$GosecVersion       = 'v2.29.0'
$GitleaksVersion    = '8.30.1'
$npmCache           = Join-Path $env:TEMP 'kipple-npm-cache'   # the shared npm cache gives EPERM on this box

$results = [System.Collections.Generic.List[object]]::new()
function Step([string]$Group, [string]$Name, [scriptblock]$Body) {
  if ($Skip -contains $Group) { return }
  Write-Host "`n=== [$Group] $Name" -ForegroundColor Cyan
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $global:LASTEXITCODE = 0
  & $Body
  $code = $LASTEXITCODE
  $results.Add([pscustomobject]@{ Group = $Group; Step = $Name; Ok = ($code -eq 0); Seconds = [int]$sw.Elapsed.TotalSeconds })
  if ($code -ne 0) { Write-Host "FAILED ($code)" -ForegroundColor Red }
}

# ---- go ----
Step 'go' 'gofmt (LF-normalized)' {
  # CRLF working copies hide gofmt failures, so check LF copies. Bytes, not text, so nothing else changes.
  $tmp = Join-Path $env:TEMP 'kipple-gofmt-check'
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  foreach ($f in (git ls-files '*.go')) {
    $dest = Join-Path $tmp $f
    New-Item -ItemType Directory -Force -Path (Split-Path $dest) | Out-Null
    $text = [System.IO.File]::ReadAllText((Join-Path $root $f)).Replace("`r`n", "`n")
    [System.IO.File]::WriteAllText($dest, $text, (New-Object System.Text.UTF8Encoding($false)))
  }
  $bad = gofmt -l $tmp
  if ($bad) { Write-Host "gofmt needed on:`n$($bad -join "`n")"; $global:LASTEXITCODE = 1 } else { $global:LASTEXITCODE = 0 }
}
Step 'go' 'go vet' { go vet ./... }
Step 'go' 'go test (shuffled, no -race)' { go test -shuffle=on -timeout 15m ./... }

# ---- security ----
Step 'security' 'govulncheck' { go run "golang.org/x/vuln/cmd/govulncheck@$GovulncheckVersion" ./... }
Step 'security' 'staticcheck' { go run "honnef.co/go/tools/cmd/staticcheck@$StaticcheckVersion" ./... }
Step 'security' 'gosec (gate: high severity, high confidence)' { go run "github.com/securego/gosec/v2/cmd/gosec@$GosecVersion" -quiet -severity high -confidence high ./... }
Step 'security' 'gitleaks (git history)' {
  # The workflow runs the release binary on a checkout. Here the official image scans a fresh local clone of the
  # whole history (a git worktree's .git is a pointer file the container cannot follow, which scans nothing).
  $clone = Join-Path $env:TEMP 'kipple-gitleaks-clone'
  Remove-Item -Recurse -Force $clone -ErrorAction SilentlyContinue
  git clone --quiet --no-hardlinks --mirror $root $clone
  docker run --rm -v "${clone}:/repo:ro" "zricethezav/gitleaks:v$GitleaksVersion" git /repo --no-banner --redact
  $code = $LASTEXITCODE
  Remove-Item -Recurse -Force $clone -ErrorAction SilentlyContinue
  $global:LASTEXITCODE = $code
}

# ---- web ----
Step 'web' 'npm ci' { Push-Location web; npm ci --cache $npmCache --no-audit --no-fund; Pop-Location }
Step 'web' 'lint' { Push-Location web; npm run lint; Pop-Location }
Step 'web' 'test' { Push-Location web; npm test; Pop-Location }
Step 'web' 'build' { Push-Location web; npm run build; Pop-Location }
Step 'web' 'theme contrast' { Push-Location web; npm run contrast; Pop-Location }
Step 'web' 'npm audit (prod, high)' { Push-Location web; npm audit --omit=dev --audit-level=high; Pop-Location }

# ---- docker (opt-in: slow) ----
if ($Docker) {
  Step 'docker' 'build image' { docker build -t kipple:ci . }
  Step 'docker' 'trivy (HIGH,CRITICAL, unfixed ignored)' {
    docker run --rm -v //var/run/docker.sock:/var/run/docker.sock aquasec/trivy:latest image --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 kipple:ci
  }
}

Write-Host "`n=== summary ===" -ForegroundColor Cyan
$results | Format-Table Group, Step, Ok, Seconds -AutoSize | Out-String | Write-Host
$failed = @($results | Where-Object { -not $_.Ok })
if ($failed.Count) { Write-Host "$($failed.Count) step(s) failed" -ForegroundColor Red; exit 1 }
Write-Host 'all steps passed' -ForegroundColor Green
