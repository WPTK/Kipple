# Security policy

Kipple is a single-user application, developed in the open. Anyone can self-host it, but it is maintained by
one person and is not offered as a hosted service.

## Reporting a vulnerability

Do not open a public issue. Report privately through GitHub's private vulnerability reporting:
on the repository (github.com/WPTK/Kipple), open the **Security** tab and choose **Report a vulnerability**
(or go straight to https://github.com/WPTK/Kipple/security/advisories/new). Include the affected version
or commit, reproduction steps, and impact. If that button is not offered, open an issue that only asks for a
private contact, with no details of the problem.

## Supported versions

Only the latest 1.x release receives security fixes. Upgrade to it to get a fix: within 1.x an upgrade is a normal one
(see [docs/compatibility.md](docs/compatibility.md) and [docs/deploy.md](docs/deploy.md)). Older minor releases, and
the `main` branch between releases, are not patched separately.

## How a fix ships

A fix is released as a new version of the latest 1.x line, built and signed by the release workflow like any other
release, and published as the image `ghcr.io/wptk/kipple`. The release notes in [CHANGELOG.md](CHANGELOG.md) list it under
Security. For a vulnerability that was reported, a GitHub Security Advisory on this repository describes the
affected versions, the fixed version and the impact. Kipple never checks for updates by itself, so watch the
repository's releases or its advisories. A security fix is exempt from the compatibility promise when it has to be.

## Scope

In scope: the Kipple server and web app as shipped in the published Docker image, including its Reader API
(`/api/greader.php`), sign-in, backup and restore, and feed fetching.

Out of scope:

- Anything that needs access you already have: the Docker host, the data volume, a backup zip you did not keep private
  (it holds password hashes and feed logins), or your own `.env`.
- A Kipple you chose to expose in a way its documentation warns against, such as open mode (no password) on a public
  interface.
- Your reverse proxy, tunnel, Cloudflare Access setup or operating system.
- Builds from source and other ways of running Kipple, which are best effort.
- Multi-user concerns. Kipple has one account by design, and it has no Fever API.

## Automated checks

CI runs govulncheck, staticcheck, gosec (gates on high severity and high confidence), gitleaks, `npm audit --omit=dev` (high) and a Trivy image scan; Dependabot proposes
weekly dependency updates.

## Issue triage

Kipple has a single maintainer. Security reports filed as above get a fast look. Other issues and pull
requests are triaged best-effort, in whatever order the maintainer gets to them. There is no guaranteed
response time or SLA.
