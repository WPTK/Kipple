# Security policy

Kipple is a private, single-user project and is not offered as a supported public service.

## Reporting a vulnerability

Do not open a public issue. Report privately to the repository owner (WPTK) by email or
GitHub private security advisory. Include affected version or commit, reproduction steps,
and impact.

## Supported versions

Only the latest tagged release (or the current `main`) receives fixes.

## Automated checks

CI runs govulncheck, staticcheck, gosec, gitleaks and a Trivy image scan; Dependabot proposes
weekly dependency updates.
