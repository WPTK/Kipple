# Local mirror of .github/workflows/ci.yml: the fast check before pushing.
#
#   pwsh scripts/ci-local.ps1               everything except the Docker build and Trivy
#   pwsh scripts/ci-local.ps1 -Docker       also build the image and scan it with Trivy
#   pwsh scripts/ci-local.ps1 -Skip web,security     skip a group (go, security, web, docker)
#
# It runs the same commands and pinned tool versions as the workflow. Differences: no `-race` (this
# machine has no C compiler), gofmt is checked on LF-normalized copies (CRLF working copies hide
# formatting failures), and gitleaks/Trivy run through pinned Docker images. The GitHub Actions run on the exact
# commit is what "CI green" means; this is the check before pushing. Exit code is non-zero on any failure.
param([switch]$Docker, [string[]]$Skip = @())

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

# Keep these in step with the env block in .github/workflows/ci.yml.
$GovulncheckVersion = 'v1.8.0'
$StaticcheckVersion = 'v0.8.1'
$GosecVersion       = 'v2.29.0'
$GitleaksVersion    = '8.30.1'
# Images are pinned by tag and digest. Trivy matches the default of the workflow's trivy-action (v0.36.0 -> Trivy v0.70.0).
$GitleaksImage      = "zricethezav/gitleaks:v$GitleaksVersion@sha256:c00b6bd0aeb3071cbcb79009cb16a60dd9e0a7c60e2be9ab65d25e6bc8abbb7f"
$TrivyImage         = 'aquasec/trivy:0.70.0@sha256:be1190afcb28352bfddc4ddeb71470835d16462af68d310f9f4bca710961a41e'
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
Step 'go' 'go test (shuffled, no -race)' { go test -shuffle=on -timeout 15m -cover ./... }

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
  # A mirror has no working tree, so hand the container the repo's allowlist config explicitly.
  $cfg = Join-Path $env:TEMP 'kipple-gitleaks-cfg'
  New-Item -ItemType Directory -Force -Path $cfg | Out-Null
  Copy-Item (Join-Path $root '.gitleaks.toml') $cfg -Force
  Copy-Item (Join-Path $root '.gitleaksignore') $cfg -Force
  docker run --rm -v "${clone}:/repo:ro" -v "${cfg}:/cfg:ro" $GitleaksImage git /repo --no-banner --redact -c /cfg/.gitleaks.toml --gitleaks-ignore-path /cfg/.gitleaksignore
  $code = $LASTEXITCODE
  Remove-Item -Recurse -Force $clone -ErrorAction SilentlyContinue
  $global:LASTEXITCODE = $code
}

# ---- web ----
Step 'web' 'changelog fragments' { node scripts/changelog.mjs check; if ($LASTEXITCODE -eq 0) { node --test scripts/changelog.test.mjs } }
Step 'web' 'release tag rules (scripts/release-tags.test.sh)' { bash scripts/release-tags.test.sh }
Step 'web' 'toolchain versions (scripts/toolchain.test.mjs)' { node --test scripts/toolchain.test.mjs }
Step 'web' 'npm ci' { Push-Location web; npm ci --ignore-scripts --cache $npmCache --no-audit --no-fund; Pop-Location }
Step 'web' 'lint' { Push-Location web; npm run lint; Pop-Location }
Step 'web' 'test and coverage (no threshold)' { Push-Location web; npm run test:coverage; Pop-Location }
Step 'web' 'build' { Push-Location web; npm run build; Pop-Location }
Step 'web' 'theme contrast' { Push-Location web; npm run contrast; Pop-Location }
Step 'web' 'npm audit (prod, high)' { Push-Location web; npm audit --omit=dev --audit-level=high; Pop-Location }
Step 'web' 'npm audit signatures' { Push-Location web; npm audit signatures --cache $npmCache; Pop-Location }

# ---- docker (opt-in: slow) ----
if ($Docker) {
  Step 'docker' 'build image' {
    # .git is not in the build context, so pass the version in as the workflow does.
    docker build --build-arg "VERSION=$(git describe --tags --always --dirty)" --build-arg "VCS_REF=$(git rev-parse HEAD)" -t kipple:ci .
  }
  Step 'docker' 'trivy (HIGH,CRITICAL, unfixed ignored)' {
    # Scan a saved tarball rather than mounting the Docker socket into the scanner container.
    $scan = Join-Path $env:TEMP 'kipple-trivy'
    New-Item -ItemType Directory -Force -Path $scan | Out-Null
    $tar = Join-Path $scan 'kipple-ci.tar'
    docker save -o $tar kipple:ci
    if ($LASTEXITCODE -eq 0) {
      docker run --rm -v "${scan}:/scan:ro" $TrivyImage image --input /scan/kipple-ci.tar --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1
    }
    $code = $LASTEXITCODE
    Remove-Item -Force $tar -ErrorAction SilentlyContinue
    $global:LASTEXITCODE = $code
  }
}

Write-Host "`n=== summary ===" -ForegroundColor Cyan
$results | Format-Table Group, Step, Ok, Seconds -AutoSize | Out-String | Write-Host
$failed = @($results | Where-Object { -not $_.Ok })
if ($failed.Count) { Write-Host "$($failed.Count) step(s) failed" -ForegroundColor Red; exit 1 }
Write-Host 'all steps passed' -ForegroundColor Green
