# Docs index

A map of everything under `docs/`, for a newcomer (self-hoster or future contributor) who doesn't already know
the project's shape. Each links to more detail; nothing here duplicates it. Older records under `docs/` (plans, meeting notes, handoffs, past test results) use the
owner's own host labels (Host-A, Host-B); they are history and say nothing about how you should run Kipple.

| Doc | What it's for |
|---|---|
| [design.md](design.md) | The source of truth for how Kipple actually works today: data model, item ids, scheduler, retention, the Reader API contract, the web API (including optional Cloudflare Access sign-in, §7.0), stats. |
| [setup-wizard-design.md](setup-wizard-design.md) | The design of the 0.5 setup wizard, open mode, the time zone and port rules and the GHCR image, with the owner's decisions (section 15). Merged by PR #91; `design.md` carries the authoritative text once shipped. |
| [ui-decisions.md](ui-decisions.md) | The record of every UI/UX and project-scope decision made in planning meetings, in the order they were decided. |
| [uat-plan.md](uat-plan.md) | The user-acceptance-testing plan: roles, entry/exit criteria, test suites, defect severity, sign-off. |
| [sqa-plan.md](sqa-plan.md) | The software-quality-assurance plan, mapped to IEEE 730: what already ensures quality (CI, reviews, release discipline) and open candidate additions. |
| [risk-register.md](risk-register.md) | Currently open risks and their mitigations, kept live rather than scattered across old phase notes. |
| [RELEASING.md](RELEASING.md) | The release checklist for every version, including alphas, and the alpha/beta/rc/1.0 promotion criteria. |
| [troubleshooting.md](troubleshooting.md) | Symptom, cause and fix: unreachable page, port mapping, sign-in busy, clients that will not connect, feeds not updating, restore, password reset, and which logs and version output to attach to an issue. |
| [reverse-proxy.md](reverse-proxy.md) | Caddy, nginx and Traefik setups, and exactly which headers Kipple trusts from a proxy. |
| [deploy.md](deploy.md) | Backups, recovery, migrations, optional Cloudflare Access setup, and how a deploy and a rollback actually work. Also the first-run account form and setup mode, ports (1919 by default), open mode (no password), the time zone rules, the published image, and the disk space an upgrade needs. |
| [../README.md](../README.md) | The Quickstart: pull-and-run (`docker run` or the pull compose file) or build from source, then the setup wizard. |
| [../docker-compose.pull.example.yml](../docker-compose.pull.example.yml), [../docker-compose.example.yml](../docker-compose.example.yml) | The ready-to-run compose file for the published image, and the build-from-source one with resource limits and full hardening. |
| [../.env.example](../.env.example) | The optional advanced overrides (listen address, host names, proxies, time zone, Cloudflare Access, scheduler, logging). Nothing needs a `.env` to get started. |

`CLAUDE.md` at the repo root holds the fixed project decisions used when working on the code; `CHANGELOG.md`
is the authoritative history of what shipped, when. A few working documents (the master plan, the parking lot,
and per-phase handoff notes) are kept local rather than in this public repo — internal planning notes, not
required reading for using or contributing to Kipple.
