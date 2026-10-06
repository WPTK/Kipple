# Docs index

A map of everything under `docs/`, for a newcomer (self-hoster or future contributor) who doesn't already know
the project's shape. Each links to more detail; nothing here duplicates it. Records such as the UAT results in `docs/uat-plan.md` use the owner's own host labels (Host-A, Host-B); they are history and say nothing about how you should run Kipple.

| Doc | What it's for |
|---|---|
| [design.md](design.md) | The source of truth for how Kipple actually works today: data model, item ids, scheduler, retention, the Reader API contract, the web API (including optional Cloudflare Access sign-in, §7.0), stats. |
| [ui-decisions.md](ui-decisions.md) | The record of every UI/UX and project-scope decision made in planning meetings, in the order they were decided. |
| [uat-plan.md](uat-plan.md) | The user-acceptance-testing plan: roles, entry/exit criteria, test suites, defect severity, sign-off. |
| [sqa-plan.md](sqa-plan.md) | The software-quality-assurance plan, mapped to IEEE 730: what already ensures quality (CI, reviews, release discipline) and open candidate additions. |
| [risk-register.md](risk-register.md) | Currently open risks and their mitigations, kept live rather than scattered across old phase notes. |
| [RELEASING.md](RELEASING.md) | The release checklist for every version, including alphas, and the alpha/beta/rc/1.0 promotion criteria. |
| [performance.md](performance.md) | How Kipple behaves with a large library (500 feeds, 150,000 items): start, upgrade, search, backup and restore, refresh, memory, database size, with sizing advice and how to repeat the measurement (`scripts/scalegen`). |
| [troubleshooting.md](troubleshooting.md) | Symptom, cause and fix: unreachable page, port mapping, sign-in busy, clients that will not connect, feeds not updating, restore, password reset, and which logs and version output to attach to an issue. |
| [reverse-proxy.md](reverse-proxy.md) | Caddy, nginx and Traefik setups, and exactly which headers Kipple trusts from a proxy. |
| [compatibility.md](compatibility.md) | What stays the same across 1.x (the Reader API, backup format, settings keys, environment variables, CLI, image tags, volume layout), what does not, and how a removal is announced. |
| [deploy.md](deploy.md) | Backups, recovery, migrations, optional Cloudflare Access setup, and how a deploy and a rollback actually work. Also the first-run account form and setup mode, ports (1919 by default), open mode (no password), the time zone rules, the published image, and the disk space an upgrade needs. |
| [../README.md](../README.md) | What Kipple is, a screenshot, the one-command install, the Reader API address, and what is left out on purpose. |
| [../docker-compose.pull.example.yml](../docker-compose.pull.example.yml), [../docker-compose.example.yml](../docker-compose.example.yml) | The ready-to-run compose file for the published image, and the build-from-source one with resource limits and full hardening. |
| [../.env.example](../.env.example) | The optional advanced overrides (listen address, host names, proxies, time zone, Cloudflare Access, scheduler, logging). Nothing needs a `.env` to get started. |

`CLAUDE.md` at the repo root holds the fixed project decisions used when working on the code; `CHANGELOG.md`
is the authoritative history of what shipped, when. A few working documents (the master plan, the parking lot,
and per-phase handoff notes) are kept local rather than in this public repo — internal planning notes, not
required reading for using or contributing to Kipple.
