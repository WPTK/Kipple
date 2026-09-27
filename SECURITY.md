# Security policy

Kipple is a single-user application, developed in the open. Anyone can self-host it, but it is maintained by
one person and is not offered as a supported public service.

## Reporting a vulnerability

Do not open a public issue. Report privately through GitHub's private vulnerability reporting:
on the repository (github.com/WPTK/Kipple), open the **Security** tab and choose **Report a vulnerability**
(or go straight to https://github.com/WPTK/Kipple/security/advisories/new). Include the affected version
or commit, reproduction steps, and impact. If that button is not offered, open an issue that only asks for a
private contact, with no details of the problem.

## Supported versions

Only the latest release tag and the `main` branch receive fixes. Kipple is prerelease software (see CHANGELOG.md for the current version).

## Automated checks

CI runs govulncheck, staticcheck, gosec (gates on high severity and high confidence), gitleaks, `npm audit --omit=dev` (high) and a Trivy image scan; Dependabot proposes
weekly dependency updates.

## Issue triage

Kipple has a single maintainer. Security reports filed as above get a fast look. Other issues and pull
requests are triaged best-effort, in whatever order the maintainer gets to them — there is no guaranteed
response time or SLA. This is stated plainly rather than left unsaid now that the repository is public.
