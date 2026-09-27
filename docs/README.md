# Docs index

A map of everything under `docs/`, for a newcomer (self-hoster or future contributor) who doesn't already know
the project's shape. Each links to more detail; nothing here duplicates it.

| Doc | What it's for |
|---|---|
| [design.md](design.md) | The source of truth for how Kipple actually works today: data model, item ids, scheduler, retention, the Reader API contract, the web API (including optional Cloudflare Access sign-in, §7.0), stats. |
| [ui-decisions.md](ui-decisions.md) | The record of every UI/UX and project-scope decision made in planning meetings, in the order they were decided. |
| [uat-plan.md](uat-plan.md) | The user-acceptance-testing plan: roles, entry/exit criteria, test suites, defect severity, sign-off. |
| [sqa-plan.md](sqa-plan.md) | The software-quality-assurance plan, mapped to IEEE 730: what already ensures quality (CI, reviews, release discipline) and open candidate additions. |
| [risk-register.md](risk-register.md) | Currently open risks and their mitigations, kept live rather than scattered across old phase notes. |
| [RELEASING.md](RELEASING.md) | The release checklist for every version, including alphas, and the alpha/beta/rc/1.0 promotion criteria. |
| [deploy.md](deploy.md) | Backups, recovery, migrations, optional Cloudflare Access setup, and how a deploy and a rollback actually work. |

`CLAUDE.md` at the repo root holds the fixed project decisions used when working on the code; `CHANGELOG.md`
is the authoritative history of what shipped, when. A few working documents (the master plan, the parking lot,
and per-phase handoff notes) are kept local rather than in this public repo — internal planning notes, not
required reading for using or contributing to Kipple.
