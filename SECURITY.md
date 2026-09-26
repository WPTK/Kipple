# Security policy

Kipple is a private, single-user project and is not offered as a supported public service.

## Reporting a vulnerability

Do not open a public issue. Report privately to the repository owner (WPTK) by email or
GitHub private security advisory. Include affected version or commit, reproduction steps,
and impact.

## Supported versions

Only the latest tag deployed to Host-A (currently a `phase-2` prerelease) or the active development branch receives fixes.

## Automated checks

CI runs govulncheck, staticcheck, gosec (gates on high severity and high confidence), gitleaks, `npm audit --omit=dev` (high) and a Trivy image scan; Dependabot proposes
weekly dependency updates.
