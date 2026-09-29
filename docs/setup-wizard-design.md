# Setup wizard and pull-and-run image: design

Roadmap issue #33. Ships as **0.5.0-beta.1**, built on feature branches, merged to `main` only after the
0.3.0-beta.2 soak (rc.1 not before 2026-10-06). This is a design, not a spec of shipped behavior: once a PR lands,
`docs/design.md` gets the authoritative text and this file is trimmed to history.

Decisions already made by the owner are stated as facts below and are not reopened. Section 12 lists what still
needs a decision.

## 1. Where things stand today (verified against `main` at c4124c0)

| Area | Today | Where |
|---|---|---|
| Account creation | Only from env on `serve` start: `KIPPLE_USERNAME` + `KIPPLE_PASSWORD` (+ optional `KIPPLE_API_PASSWORD`). No env, no account: web login and Reader API stay disabled, a WARN is logged, and the server runs anyway. An empty `KIPPLE_PASSWORD` never means "no password". | `cmd/kipple/account.go` `ensureAccount` (called at `cmd/kipple/main.go:148`) |
| Account row | Single row, `id = 1`, `password_hash TEXT NOT NULL` (empty = no web password), `api_password_hash` NULL = Reader API off, `secret` 64 hex. | `internal/store/migrations/0001_init.sql:24`, `internal/store/account.go` |
| Race safety of creation | `INSERT ... ON CONFLICT (id) DO NOTHING`, reports `created`. Already the right primitive for a claim race. | `internal/store/account.go:37` |
| Passwordless | Empty hash signs in **only** with a verified Cloudflare Access JWT on that request; without Access configured it cannot sign in at all. Getting there requires Settings > Remove web password with a verified token. | `internal/api/login.go:51`, `internal/api/access.go`, `docs/design.md` §7.0 |
| Sessions | `kipple_session` cookie, 32 random bytes, stored as sha256, 90-day sliding, HttpOnly, SameSite=Lax, Secure when the effective scheme is https. | `internal/api/api.go:36-40, 383-429` |
| CSRF | `authed` wraps every UI route: session first, then for non-GET `sameOrigin`: `Sec-Fetch-Site: same-origin` (or `Origin == scheme://Host`) **and** `X-Kipple-Client: web|pwa`. No CORS headers anywhere. | `internal/api/api.go:317-379` |
| Host header | Never validated. Only used to build the expected `Origin`. | `internal/api/api.go:368` |
| Lockout | Per IP (IPv6 by /64), 10 failures / 15 min, reserve-before-verify. | `internal/auth/auth.go:499-616` |
| Route table | Explicit list; the `/api/` catch-all is `authed` and answers 401/404, never the SPA. New public routes must be registered explicitly. | `internal/api/api.go:233-306` |
| Reader API | Claimed ahead of the mux; disabled while there is no account or no API password (5 s cached snapshot, `InvalidateAccount` to drop it). | `internal/greader/api.go:211, 347-385` |
| Port | Default `:7080` in two places that must agree: `config.defaultAddr` and `healthURL`'s own fallback. `.env.example` sets `KIPPLE_ADDR=:7080` explicitly; Dockerfile `EXPOSE 7080`; compose example `7080:7080`; 18 tracked files mention 7080. | `internal/config/config.go:43`, `cmd/kipple/healthcheck.go:19`, `.env.example:7` |
| Healthcheck | `/healthz` is unauthenticated, 200 `ok`; `kipple healthcheck` probes loopback on the `KIPPLE_ADDR` port. | `internal/api/api.go:308`, `cmd/kipple/healthcheck.go` |
| Version | `main.version` via `-ldflags -X`; `kipple version` prints it alone (RELEASING step 10 greps that). Dockerfile sets OCI `title/description/source/version/revision/licenses`. No commit, date or Go version in the binary. | `cmd/kipple/main.go:43, 87`, `Dockerfile:41-46` |
| Downgrade guard | **Already exists**: `user_version > latest` refuses to start. The message does not say which Kipple version wrote the database. | `internal/store/migrate.go:107` |
| Migrations | Embedded, dense, `0001`..`0009`; pre-migration `VACUUM INTO` snapshot, newest 3 kept. Next is `0010`. | `internal/store/migrate.go`, `internal/store/migrations/` |
| Restore | CLI only, refuses under a running server (data-dir lock), revokes sessions in the copy. No web restore. | `cmd/kipple/restore.go` |
| Themes | Device-scoped keys `ui.theme` (default `system`), `ui.theme_day` (`paper`), `ui.theme_night` (`midnight`); the global row is the default for devices without an override. | `internal/api/settingsmeta.go:296-300, 423`, `internal/store/uibootstrap.go:52` |
| OPML import | `POST /api/opml`, 8 MiB cap, imported feeds always get `allow_private_net` off; fetches go through the SSRF-guarded transport (checked per dialed address, so DNS rebinding of feed hosts is covered). | `internal/api/opml.go`, `internal/opml/import.go:51`, `internal/fetch/ssrf.go` |
| Service worker | Build id `<stamp>-<hash>` baked into `dist/sw.js` at `closeBundle`; the bundle itself does not know the server version. | `web/vite.config.ts` `kippleSw` |
| CI | `ci.yml` on push/PR: Go (vet, race tests), security (govulncheck, staticcheck, gosec gate, gitleaks), web, then a single-arch `docker` build + Trivy (HIGH/CRITICAL, ignore-unfixed). No release workflow, nothing is pushed anywhere. | `.github/workflows/ci.yml` |
| Build context | `.dockerignore` excludes `*.md` (so `CHANGELOG.md`) and `docker-compose.example.yml`. | `.dockerignore` |
| Parking lot | `docs/parking-lot.md` is gitignored and kept outside the repo; env-only settings moving in-app are tracked there. | `.gitignore:40` |

## 2. Goals and non-goals

**Goals**

1. `docker run` or a ~10-line compose file pulls a signed multi-arch image from GHCR and reaches a working,
   claimed instance in the browser without editing an `.env`.
2. The first person to reach the port cannot silently claim the instance: claiming needs a one-time token that
   only someone with container-log (or `docker exec`) access can read.
3. Account creation moves into the wizard. The env path keeps working unchanged for scripted deploys and the
   owner's live instance.
4. A password is optional. Passwordless without Access ("open mode") is new, is the user's choice, and is fenced
   against DNS rebinding, cross-site requests and accidental exposure through a proxy or tunnel.
5. Build and runtime facts are visible (richer `kipple version`, About screen with copyable debug info, version
   mismatch banner, better downgrade message, what's-new), with **no** update check and nothing phoned home.

**Non-goals (0.5.0)**

- Multi-user, invitations, or a second account. Setup creates the one row; it never replaces it.
- Web-based restore. Restore stays `kipple restore` with the server stopped (a hook is left, section 11).
- Moving other env-only configuration in-app (`KIPPLE_PUBLIC_URL`, trusted proxies, scheduler tuning, log level,
  custom domain, Access). Hooks only (section 11).
- An env switch for open mode. `KIPPLE_PASSWORD` empty keeps meaning "no account from env", never "no password".
- Registries other than GHCR (parked until 1.0). Update checks of any kind.

## 3. Architecture and state machine

### 3.1 Two modes, one process

Setup mode is **an authorization state, not a different server**. Every handler, the scheduler, maintenance and
the SPA are built and started exactly as today. What changes is which routes answer and what the SPA renders.
No restart is needed when setup completes.

```
            serve start
                 |
     open store, migrate (0010)
                 |
     ensureAccount(env)          <- unchanged: env creds + no row => create row (created_via='env')
                 |
       account row exists? ----yes----> NORMAL (setup routes never registered; stale setup-token file removed)
                 |no
                 v
              SETUP
   token generated (memory + data/setup-token 0600),
   banner printed to stderr, setup routes registered
                 |
   POST /api/setup/account inserts the row (ON CONFLICT DO NOTHING => exactly one winner)
                 |
                 v
              NORMAL   (atomic flag flips once; token wiped; file removed; setup routes answer 404)
                 |
   onboarding steps 3-6 run as an ordinary signed-in session until sys.setup_completed_at is set
```

- The flag is one-way inside a process. Nothing in the API or CLI deletes the account row, so normal mode never
  returns to setup mode while running. A restart re-derives the mode from the database.
- Precedence: env credentials win. If `KIPPLE_USERNAME` and `KIPPLE_PASSWORD` are set and the row is missing,
  `ensureAccount` creates it exactly as today and the process starts in normal mode. If only `KIPPLE_USERNAME` is set
  (the old example file's default is `owner`), today's WARN is replaced by "no account: finish setup in the browser"
  and the process enters setup mode; the env username is ignored (the wizard asks).
- An env-created account gets `sys.setup_completed_at` at creation, so scripted deploys never see onboarding.

### 3.2 What the SPA shows

The SPA already calls `/api/bootstrap` first and renders `LoginScreen` on 401 (`web/src/App.tsx:110`). The only
new pre-auth call happens on that 401 path, so signed-in launches cost nothing extra:

| `GET /api/instance` says | SPA renders |
|---|---|
| `setup: true` | Wizard step 1 (token), then step 2 (account). |
| `setup: false, auth: "open"` | Calls `POST /api/auth/open` silently, then reloads bootstrap. On refusal (wrong host, forwarded request) shows why and how to reach Kipple locally. |
| `setup: false, auth: "password"` or `"access"` | `LoginScreen`, as today. |

After sign-in, `bootstrap.user.setup_pending: true` (no `sys.setup_completed_at`) routes to `/welcome`, which is
wizard steps 3-6. "Skip for now" on any step and "Finish" both call `POST /api/onboarding/complete`. Steps are
resumable after a reload because each one writes through ordinary endpoints as it goes.

`/_status` (the embedded status page) gets one line in setup mode: "Setup is pending: open Kipple to finish it."

### 3.3 Wizard steps

| Step | Mode | Writes through | Skippable |
|---|---|---|---|
| 1 Token | setup | `POST /api/setup/claim` | no |
| 2 Account | setup | `POST /api/setup/account` (signs the browser in) | no |
| 3 Theme | normal | `PATCH /api/settings` `{ui.theme, ui.theme_day, ui.theme_night}` (global row = default for every future device); live preview by applying the scheme client-side before saving | yes |
| 4 OPML | normal | `POST /api/opml` unchanged | yes |
| 5 Recommended feeds | normal | `GET/POST /api/starter-feeds` | yes |
| 6 Finish | normal | Reader API: `POST /api/account/api-password {generate:true}` (copy button, shown once); then `POST /api/onboarding/complete` | generating is optional |

Step 2 content: username (the existing rule: 1-64 of `A-Za-z0-9._-`), then a choice:

- **Password** (5-256 bytes, the example value `change-me` refused, same rules as `checkEnvPassword`).
- **No password, Cloudflare Access**: offered only when `GET /api/setup/state` reports Access configured **and**
  this request carries a verified token. Same semantics as today's §7.0 passwordless account.
- **No password (open)**: shows the owner's notice verbatim in substance: "Anyone who can reach this address can
  read and change everything. Only choose this if Kipple is reachable only from this computer (localhost) or over
  Tailscale." A checkbox acknowledgement is required and sent as `acknowledge_open: true`.

For step 6 with a password account, the wizard keeps the password typed in step 2 in component memory only and
sends it as `current`; after a reload it asks for it again (the normal Settings flow). In open mode no `current` is
needed (section 4.3).

## 4. Endpoints

All JSON errors keep the existing `{error, message?}` shape. "Same-origin" means today's `sameOrigin` (including
`X-Kipple-Client`). "Host gate" and "open gate" are defined in section 5.

### 4.1 New: public, setup mode only

These are registered only when the process starts in setup mode, and every handler also checks the atomic flag,
so after the row exists they answer `404 not_found` forever (same body as an unknown `/api/` route).

| Route | Auth | Request | Response and errors |
|---|---|---|---|
| `GET /api/setup/state` | Host gate | — | `{claimed: bool (this browser holds a valid setup cookie), access: {enabled, verified}, token_hint: "printed in the container log at <start time>"}`. `no-store`. |
| `POST /api/setup/claim` | Host gate, same-origin, setup lockout | `{token}` (spaces, dashes and case ignored) | `204` + `kipple_setup` cookie (32 random bytes, HttpOnly, `SameSite=Strict`, `Path=/api/setup`, `Max-Age=3600`, Secure by effective scheme). A new claim replaces the previous setup session (the token holder is the authority). `403 bad_token` (counted), `429 locked` + `Retry-After`, `403 host`, `403 origin`. |
| `POST /api/setup/account` | Host gate, same-origin, `kipple_setup` cookie | `{username, password?, passwordless?: "access"\|"open", acknowledge_open?: bool}`; exactly one of `password` and `passwordless` | `201 {username, auth_mode}` + `kipple_session` cookie (a normal 90-day session), `kipple_setup` cleared. `409 already_set_up` (lost the race; the SPA reloads into the login screen), `400 bad_username`, `400 bad_new_password`, `400 ack_required`, `403 access_required` / `503 access_unavailable` (for `"access"`, via the existing `accessProof`), `403 open_refused` (for `"open"`, the request fails the open gate: choosing open mode must happen from a place where open mode would work), `401 setup_session`. |

Account creation reuses one shared function with `ensureAccount` (moved to a new `internal/setup` package with the
username/password checks and `newAccountSecret`, which today lives in `cmd/kipple/password.go`). After a successful
insert: flip the flag, wipe the token, remove `data/setup-token`, `verifier.SetSecret`, `readerAPI.InvalidateAccount`,
log `account created` with `username`, `created_via=wizard` and `auth_mode` (the same line `ensureAccount` logs
today, plus the two new fields).

### 4.2 New: always present

| Route | Auth | Response |
|---|---|---|
| `GET /api/instance` | none (Host gate in setup and open modes) | `{setup: bool, auth: null\|"password"\|"access"\|"open"}`. No version, no username. `no-store`; the service worker never caches it. |
| `POST /api/auth/open` | open gate, same-origin | Open mode only: `204` + session cookie, exactly like a login. `404` when the account is not in open mode, `403 open_refused {reason: "host"\|"peer"\|"forwarded"}`. Not counted against the lockout (there is nothing to guess). |
| `GET /api/about` | session | `{version, commit, build_date, go_version, os_arch, schema_version, schema_latest, sqlite_version, started_at, uptime_s, data_dir_writable, tz, auth_mode, access_enabled, public_url_set, web_build}`. No username, no hostnames, no paths beyond the data dir name. |
| `GET /api/starter-feeds` | session | The embedded list (section 7) with `subscribed: bool` per feed (via `FindFeedByURL`). |
| `POST /api/starter-feeds` | session, same-origin | `{ids:[...], folders: bool}`: subscribes **by id only** (the server never takes a URL here), one folder per category when `folders`. Same insert-then-`StartImport` path as OPML. `{added, existing, run_id}`. `400 unknown_id`. |
| `POST /api/onboarding/complete` | session, same-origin | Sets `sys.setup_completed_at`. `204`. Idempotent. |

### 4.3 Changed

| Route | Change |
|---|---|
| `GET /api/auth/me`, bootstrap `user` | Add `auth_mode: "password"\|"access"\|"open"` and `setup_pending: bool`. |
| `GET /api/bootstrap` | Add `web_build` (the build id the server's embedded `index.html` carries, section 9). |
| `POST /api/account/password` | New `{current, open: true}`: switch to open mode. Needs the current password **and** the request must pass the open gate, so it cannot be turned on from outside. In open mode `{new}` without `current` sets a password and returns to `standard` (the session plus same-origin are the proof; there is no credential to prove). Both sign out every other session (existing `SetPasswordHash` behavior). |
| `POST /api/account/api-password` | In open mode `current` is not required (`checkCurrent` gains an open-mode branch that requires the open gate instead). |
| `POST /api/auth/login` | In open mode answers `409 open_mode` so a stale login form cannot confuse; the SPA calls `/api/auth/open` instead. |
| `kipple password` | Setting a password also sets `auth_mode='standard'` (the 0010 CHECK enforces it). Its "no account yet" message points at the wizard or the env variables. |
| `kipple api-password` | Same message change. |
| `kipple setup-token` (new) | Prints the current token from `data/setup-token`, or "no setup pending". For logs that rotated away. |
| `kipple version` | Unchanged first line (RELEASING step 10 depends on it). `kipple version -v` prints the full build info (section 9). |
| `/healthz` | Unchanged: 200 in setup mode too. "Healthy" means serving, not configured; the docs say so. |

## 5. Security design

### 5.1 Setup token

- **Format:** 24 characters of Crockford base32 (120 bits) shown as `XXXX-XXXX-XXXX-XXXX-XXXX-XXXX`. Case, spaces
  and dashes are ignored on input; `0/O` and `1/I/L` fold as Crockford specifies, so a hand-typed token works.
- **Lifetime:** the life of the process, until claimed. A restart generates a new one. Once the account row exists it
  is gone for good. No wall-clock expiry: a user who pulls the image and returns the next day should not be forced to
  restart, and the token only matters while the row is empty.
- **Storage:** in memory as `sha256(token)`, compared with `subtle.ConstantTimeCompare`. Also written to
  `<data>/setup-token` (0600, created with `O_EXCL` after removing a stale one) so `kipple setup-token` can show it.
  The data directory already holds the database and its account secret, so this adds no new trust boundary. Backups
  never include it (the zip carries `kipple.db` and readable copies only, `internal/backup/archive.go`).
- **Printing:** once at startup to **stderr with `fmt`, not `slog`**, so `KIPPLE_LOG_LEVEL=error` cannot hide it and
  structured log shippers get one plain line rather than a searchable field. The banner gives the code, the path
  form `http://<host>:1919/#setup=<code>` (a fragment: never sent to the server, never in a Referer or proxy log;
  the SPA reads it, prefills step 1 and clears it with `history.replaceState`), and "`docker exec <container>
  /kipple setup-token` shows it again". A structured `setup pending` INFO line without the token is logged too.
- **Brute force:** its own `auth.Lockout` instance (so setup failures never lock the later login and vice versa):
  10 failures per IP (/64) per 15 minutes. Plus a global counter: after 100 failures in one process the token is
  rotated, the new one printed with a WARN "setup token rotated after repeated failures". At 120 bits the lockout is
  about noise and log volume, not feasibility.

### 5.2 Host gate (DNS rebinding)

An open localhost app is the textbook DNS-rebinding target: `evil.example` resolves to `127.0.0.1`, the browser
treats it as same-origin with the attacker's page, and every same-origin check passes. The only thing the attacker
cannot control is the `Host` header, which is `evil.example:1919`. So:

- **Enforced in setup mode and open mode**, on every request (a middleware ahead of the mux, beside
  `httpx.Secure`). A refused request gets `421 Misdirected Request` with a plain-text explanation naming the
  setting. In password/Access mode the gate only logs (once an hour, like the untrusted-proxy WARN): the session
  cookie is bound to the real origin, so rebinding reads nothing there, and enforcing would break existing
  deployments whose hostname was never configured.
- **Allowed by default:** IP literals (a rebinding attack always carries a name), `localhost` and `*.localhost`,
  single-label names (`nas`), `*.local`, `*.lan`, `*.home.arpa`, `*.internal`, `*.ts.net` (Tailscale MagicDNS),
  the host of `KIPPLE_PUBLIC_URL` when set.
- **Configurable:** `KIPPLE_ALLOWED_HOSTS` (comma list, for setup mode, before any UI exists) plus a global setting
  `security.allowed_hosts` (JSON array, editable in Settings after setup). Entries are exact hosts or `*.suffix`.

### 5.3 CSRF

- Every new state-changing route uses the existing `sameOrigin` rule, including `X-Kipple-Client`, which a
  cross-site form cannot send and a cross-site `fetch` cannot send without a CORS preflight Kipple never answers.
- `kipple_setup` is `SameSite=Strict` and scoped to `/api/setup`. `POST /api/auth/open` is same-origin-checked so a
  cross-site page cannot mint a session cookie into the victim's browser (login CSRF).
- No endpoint adds CORS headers. A test asserts `Access-Control-Allow-Origin` is absent on every route.

### 5.4 Open mode (passwordless without Access)

Stored as `account.auth_mode = 'open'` with an empty `password_hash` (migration 0010). The Reader API is unchanged:
it still needs the generated API password, which in open mode is the only credential that exists.

The **open gate**, checked by `POST /api/auth/open`, by switching to open mode, and by `checkCurrent` in open mode:

1. Host gate passes (5.2).
2. **Not forwarded.** Refused when the TCP peer is in `KIPPLE_TRUSTED_PROXY_IPS`, or the request carries
   `CF-Connecting-IP`, `Cf-Access-Jwt-Assertion`, `Forwarded`, `X-Forwarded-For` or `Tailscale-Funnel-Request`.
   A tunnel or reverse proxy in front means the port is published to people the owner did not pick, which is exactly
   what the notice rules out. One exception: a loopback peer with a `*.ts.net` Host and no `Tailscale-Funnel-Request`
   is Tailscale Serve (tailnet-only HTTPS) and is allowed. (Verify the headers Tailscale Serve and Funnel actually
   send during PR B; the rule is written against their documented behavior.)
3. **Peer class.** The TCP peer must be loopback, Tailscale (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`), or the
   container's own default gateway (Docker's userland proxy makes every `-p 127.0.0.1:...` connection arrive from the
   bridge gateway, read once from `/proc/net/route`). Other private-range peers (the LAN) are refused unless the
   owner opts in; see open question 3.

Existing sessions keep working after the gate fails (a session is a session), but they are revoked whenever the mode
changes, as password changes already do.

### 5.5 Threat model

| Threat | Mitigation | Residual |
|---|---|---|
| **Claim race**: someone on the network reaches the fresh port first | Token required before the account step; the account insert is `ON CONFLICT DO NOTHING`, so two token holders racing get one `201` and one `409` | Whoever can read container logs can claim; that person already controls the host |
| **Token leakage in logs** (log shippers, pasted `docker logs` in an issue) | Single use, dies at claim, rotates on restart; printed once, outside slog; never in debug info, request logs or backups; the fragment form keeps it out of proxy logs and Referers | A shipped log line is readable until the instance is claimed |
| **Brute force** of the token or of logins | 120-bit token, separate per-IP lockout, global rotation; login lockout unchanged | None worth noting |
| **DNS rebinding** against setup or open mode | Host gate (5.2) enforced in both; password mode unaffected (cookie is origin-bound) | A user who allowlists a public name they do not control |
| **CSRF / login CSRF** | `sameOrigin` + `X-Kipple-Client` on every write; Strict setup cookie; no CORS | None beyond today |
| **Passwordless on the LAN or the internet** | Explicit acknowledgement; open gate refuses forwarded requests and non-local peers; turning open mode on requires the password and the gate | A user who opts the LAN in (question 3) trusts every device on it, by choice |
| **Setup endpoints reopening** | Not registered when the row exists at start; flag checked per request; no API deletes the row | Direct SQLite surgery (out of scope) |
| **SSRF via OPML or starter feeds** | Unchanged guarded transport, checked per dialed address; imported feeds have `allow_private_net` off; the starter list is validated at build time to public https hosts (7.3) and subscribed by id, never by client URL | Same as adding a feed today |
| **Session fixation** | Setup cookie cleared and a fresh session minted at account creation | None |
| **Debug info leakage** | About payload excludes username, hostnames, URLs and secrets; the copy text is shown before copying | Version and platform are disclosed when a user pastes it, which is the point |
| **Supply chain** | Keyless cosign signature on the digest, SLSA provenance and SBOM attestations, Trivy gate before any tag is published (section 8) | Trust in GitHub's OIDC issuer and Sigstore |

## 6. Schema: migration 0010

`internal/store/migrations/0010_setup.sql`. The account table is rebuilt (one row, so it costs nothing) because the
cross-column invariant needs a table-level CHECK, which `ALTER TABLE ADD COLUMN` cannot add. No foreign key points
at `account`, so the `foreign-keys-off` marker is not needed.

```sql
CREATE TABLE account_new (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  username          TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64
                                         AND username NOT GLOB '*[^A-Za-z0-9._-]*'),
  password_hash     TEXT NOT NULL,
  api_password_hash TEXT,
  secret            TEXT NOT NULL CHECK (length(secret) = 64),
  auth_mode         TEXT NOT NULL DEFAULT 'standard' CHECK (auth_mode IN ('standard', 'open')),
  created_via       TEXT NOT NULL DEFAULT 'env' CHECK (created_via IN ('env', 'wizard')),
  created_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  updated_at        INTEGER NOT NULL DEFAULT (unixepoch()),
  CHECK (auth_mode = 'standard' OR password_hash = '')
) STRICT;
INSERT INTO account_new (id, username, password_hash, api_password_hash, secret, created_at, updated_at)
  SELECT id, username, password_hash, api_password_hash, secret, created_at, updated_at FROM account;
DROP TABLE account;
ALTER TABLE account_new RENAME TO account;
-- An existing account has been "set up" already: it must never see onboarding.
INSERT INTO settings (key, value)
  SELECT 'sys.setup_completed_at', CAST(unixepoch() AS TEXT) FROM account WHERE id = 1
  ON CONFLICT (key) DO NOTHING;
```

- `auth_mode = 'standard'` covers both a password account and today's Access-only passwordless account (empty hash,
  Access configured). The API maps it to `password` or `access` for display.
- New non-migration keys (Go defaults, as usual): `sys.setup_completed_at`, `sys.last_version` (written at every
  start, section 9), `security.allowed_hosts` (global, JSON array, default `[]`), `security.open_lan` (question 3),
  `ui.whats_new_seen` (hidden).
- `migrate0010_test.go`: a schema-9 database with and without an account migrates; the row and hashes are intact;
  the CHECK rejects `open` with a non-empty hash; `sys.setup_completed_at` exists only when the row does.
- Downgrade: a 0.3.x binary meets `user_version = 10` and refuses to start (existing guard). Rolling back means
  restoring the pre-migration snapshot (RELEASING, Rollback), as for every schema change.

## 7. Recommended feeds file

### 7.1 Location and how it reaches the binary

`starter/feeds.json` at the repository root, with `starter/embed.go` (`package starter`, `//go:embed feeds.json`),
the same pattern as `web/embed.go`. It is a top-level directory so the owner edits one obvious file without touching
Go code. The Dockerfile's Go stage adds `COPY starter/ ./starter/`. The server parses it once at startup; the SPA
gets it from `GET /api/starter-feeds`, so there is one source of truth and the server subscribes only ids it knows.

### 7.2 Schema

```json
{
  "version": 1,
  "categories": [
    {
      "id": "tech",
      "title": "Technology",
      "feeds": [
        {
          "id": "example-tech-news",
          "title": "Example Tech News",
          "url": "https://news.example.com/feed.xml",
          "site": "https://news.example.com/",
          "description": "One sentence, shown under the title.",
          "checked": false
        }
      ]
    }
  ]
}
```

`checked` pre-ticks a feed. Optional per-feed `lang` (BCP 47) is allowed for later filtering; nothing else is.

### 7.3 Validation

A Go test in `starter` (so the existing CI `go test` job gates it; nothing new to wire):

- Strict decode (`DisallowUnknownFields`), `version == 1`, at least one category, at most 300 feeds.
- Ids: `^[a-z0-9][a-z0-9-]{0,63}$`, unique across the file; category ids unique.
- `url` and `site`: absolute `https`, no userinfo, no fragment, host is a DNS name (not an IP literal, not
  `localhost`, not a `.local/.lan/.internal/.home.arpa` suffix), port absent or 443; no two feeds with the same
  `feedurl` key.
- `title` 1-100 characters, `description` at most 200, no control characters; category `title` 1-50.

Liveness is **not** checked in CI (network flakiness would gate unrelated PRs). `scripts/check-starter-feeds.mjs`
fetches each URL through a running local Kipple (`POST /api/feeds` against a seed instance) and reports dead or
redirected feeds; it becomes a line in RELEASING "Before the tag". At runtime a file that somehow fails to parse
logs an error and the step shows "no recommendations available" rather than failing startup.

## 8. Distribution: GHCR release workflow

### 8.1 Workflow

`.github/workflows/release.yml`, `on: push: tags: ['v*']`, plus `workflow_dispatch` with a `repoint_latest` input for
rollback (8.4). Actions pinned by SHA like `ci.yml`; Dependabot bumps them.

1. **Gate.** Tag matches `^v\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?$`, is annotated, and its commit is on `main`.
   `prerelease = contains(tag, '-')`.
2. **Tests.** `ci.yml` gains `on: workflow_call`; the release job `needs:` it, so the exact tagged commit is tested
   again rather than trusting a nearby run.
3. **Build.** `docker/setup-buildx-action`, `platforms: linux/amd64,linux/arm64`. The Dockerfile cross-compiles
   instead of emulating: the `web` and `build` stages use `FROM --platform=$BUILDPLATFORM`, the Go build uses
   `GOOS=$TARGETOS GOARCH=$TARGETARCH` (safe: `CGO_ENABLED=0` and the SQLite driver is pure Go, `modernc.org/sqlite`),
   and only the distroless runtime stage is per-platform. `provenance: mode=max`, `sbom: true`. `SOURCE_DATE_EPOCH`
   = commit time, so the build date is reproducible. Pushed **by digest only** to `ghcr.io/wptk/kipple` under a
   `sha-<commit>` tag nobody should use.
4. **Scan.** Trivy against that digest, once per platform (`--platform`), same policy as CI (HIGH/CRITICAL,
   ignore-unfixed, exit 1). A failure stops here; no user-facing tag was created.
5. **Smoke.** `docker run --platform linux/arm64` (QEMU) and amd64 of the digest: `kipple version -v` prints the tag,
   and a `serve` on an empty volume answers `/healthz` and prints a setup banner. Catches an arm64 binary that builds
   but does not run.
6. **Tag.** `docker buildx imagetools create` adds `X.Y.Z[-pre.N]` always; for a stable release also `X.Y`, `X` and
   `latest`. A prerelease never touches `latest` or the floating tags; a step asserts that before running.
7. **Sign and attest.** `permissions: id-token: write, packages: write, attestations: write, contents: write`.
   `cosign sign --yes ghcr.io/wptk/kipple@<digest>` (keyless, the workflow identity); `actions/attest-build-provenance`
   for GitHub's attestation store in addition to the BuildKit attestations.
8. **Release notes.** The GitHub Release (RELEASING step 11) gets the digest and the verify command appended, so a
   version maps to exactly one digest.

Users verify with:

```
cosign verify ghcr.io/wptk/kipple:0.5.0 \
  --certificate-identity-regexp '^https://github.com/WPTK/Kipple/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**One-time owner steps:** after the first push, make the GHCR package public and confirm it is linked to the
repository (the `org.opencontainers.image.source` label does the linking; visibility is a manual switch).

### 8.2 Pull-and-run

`docker-compose.yml` for the README (the hardening from `docker-compose.example.yml` that works without edits;
`mem_limit`, `pids_limit`, `GOMEMLIMIT` and log rotation stay in the full example):

```yaml
services:
  kipple:
    image: ghcr.io/wptk/kipple:latest
    restart: unless-stopped
    ports: ["127.0.0.1:1919:1919"]
    volumes: ["kipple_data:/data"]
    read_only: true
    tmpfs: ["/tmp:size=64m,mode=1777"]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
volumes:
  kipple_data:
```

```
docker run -d --name kipple --restart unless-stopped -p 127.0.0.1:1919:1919 -v kipple_data:/data --read-only --tmpfs /tmp:size=64m,mode=1777 --cap-drop ALL --security-opt no-new-privileges ghcr.io/wptk/kipple:latest
```

Then `docker logs kipple` for the code. Binding `127.0.0.1` is the default on purpose (it matches the open-mode
notice); the README shows the one-word change for LAN or Tailscale access. A named volume inherits `/data`'s
ownership (65532) from the image; a bind mount needs `chown 65532:65532` first, which the README states.

### 8.3 Port 1919

- `config.defaultAddr` becomes `:1919`; `healthURL` stops carrying its own `":7080"` fallback and uses the config
  constant (today they are two literals that must agree: a real drift risk). `EXPOSE 1919`.
- **Fallback 1138:** only when `KIPPLE_ADDR` is unset and binding `:1919` fails with `EADDRINUSE` (a bare binary on a
  machine where 1919 is taken). Logged as a WARN with the port chosen. `kipple healthcheck` with `KIPPLE_ADDR` unset
  probes 1919, then 1138. Inside a container this never triggers.
- **Owner's live instance:** keeps 7080 by override. Before deploying 0.5.0-beta.1, confirm Host-A's env sets
  `KIPPLE_ADDR=:7080`; if it relies on the default, the container would come up healthy on 1919 behind a
  `7080:7080` mapping and be unreachable. This is a checklist line in the deploy notes.
- **Breaking change for others:** anyone who copied `.env.example` has `KIPPLE_ADDR=:7080` set explicitly
  (`.env.example:7`), so the README Quickstart path is unaffected. The break is limited to deployments with no
  `KIPPLE_ADDR` and `-p 7080:7080`: after upgrading, the healthcheck passes (it probes inside) but the mapped port
  answers nothing. Changelog `changed` entry marked **Breaking**, an "Upgrading to 0.5.0" note in `docs/deploy.md`,
  and question 1 proposes a shim that removes the break entirely.

### 8.4 Rollback of a release

Tags are never moved or reused (unchanged policy). A bad stable image is superseded by the next patch version;
until it exists, `workflow_dispatch` with `repoint_latest: vX.Y.Z` re-points `latest` (and `X.Y`, `X`) at the
previous good digest with `imagetools create`, no rebuild. Signed digests are never deleted.

## 9. Build info

- **Binary:** ldflags `-X main.version -X main.commit -X main.buildDate` (commit and date come from the build args,
  because `.git` is not in the context). `debug.ReadBuildInfo` supplies the Go version, `GOOS/GOARCH`, and the module
  versions for `-v`. `kipple version` prints the version alone (unchanged); `kipple version -v` prints version,
  commit, build date, Go version, OS/arch, embedded schema version and web build id.
- **OCI labels:** keep today's six, add `org.opencontainers.image.created`, `url`, `documentation`, `base.name` and
  `base.digest`. The release workflow's `docker/metadata-action` supplies the dynamic ones.
- **Image digest:** a process cannot know its own image digest (it is computed after the image is built). The About
  screen shows version and commit, which the release notes map to exactly one digest (8.1 step 8); the docs give
  `docker inspect --format '{{index .RepoDigests 0}}' kipple` for the host side. No env variable pretends otherwise.
- **About screen** (Settings > About): `GET /api/about` plus client facts (browser UA, service worker build id,
  bundle version, display mode standalone or not). "Copy debug info" builds a plain-text block, shows it, then
  copies. `data_dir_writable` is a create-and-remove of a temp file in the data directory, done per request.
- **Mismatch banner:** the Vite build defines `__KIPPLE_VERSION__` (the Dockerfile passes `VERSION` to the web stage
  too) and `__KIPPLE_BUILD__`, and writes the build id into `index.html` as `<meta name="kipple-build">`. The server
  reads that meta once from the embedded `index.html` and returns it as `bootstrap.web_build`. When a **network**
  bootstrap (not the worker's stored copy) disagrees with the running bundle, the app shows "Kipple was updated.
  Reload" and the button activates the waiting worker, then reloads.
- **Downgrade guard:** keep the existing refusal; improve the message. `sys.last_version` is written at each
  successful start; on refusal the store reads it best-effort (the settings table exists at every schema) and says
  "this database was last opened by Kipple vX, schema N; this is vY, schema M. Run vX or newer, or restore the
  pre-migration snapshot (docs/deploy.md)".
- **What's new:** the web stage copies `CHANGELOG.md` (`.dockerignore` gains `!CHANGELOG.md`); a Vite plugin emits
  the newest ten version sections as a hashed JSON asset, loaded lazily. After an upgrade (bundle version newer than
  `ui.whats_new_seen`, compared by SemVer, prereleases included, `dev` skipped) the app shows those sections once and
  writes the setting. Global, not per device: one reader should not dismiss it three times.
- **No update check.** Nothing contacts GitHub, GHCR or anything else. The fetch User-Agent already carries the
  version to feed hosts; that is unchanged.

## 10. Interactions with existing systems

- **Cloudflare Access JWT:** `accessVerifier` and `accessProof` are unchanged. Setup mode behind Access still needs
  the setup token (Access proves an identity admitted to the app, not ownership of this instance). The wizard offers
  "No password, Cloudflare Access" only with a verified token on the request, which preserves §7.0's rule that a
  passwordless Access account is only ever created by someone for whom Access sign-in demonstrably works. Open mode
  ignores Access entirely and the open gate refuses Access-forwarded requests.
- **Sessions:** setup completes by minting an ordinary session. Mode changes (standard to open and back) revoke other
  sessions through the existing `SetPasswordHash` transaction. `kipple restore` already revokes all sessions.
- **Lockout:** the login `Lockout` is untouched; the setup claim has its own instance; open-mode session minting is not
  counted.
- **Backup and restore:** backups need a session, so none can be made in setup mode (the nightly snapshot still
  runs; harmless). Restoring any backup that contains the account puts the instance in normal mode on the next start,
  so **restore leaves setup mode**. A pre-0.5 backup migrates through 0010, which stamps `sys.setup_completed_at`,
  so it never shows onboarding. Restoring a backup taken in setup mode (no row) returns to setup mode with a fresh
  token. `setup-token` is never in a backup.
- **Reader API:** disabled until the account exists and has an API password, as today. `InvalidateAccount` runs after
  creation so the 5 s snapshot cannot lag.
- **Healthcheck:** 200 in setup mode; port logic per 8.3.
- **Dev tooling:** `npm run seed` sets `KIPPLE_ADDR` explicitly (7080) and creates the account from env, so it is
  unaffected; a new `KIPPLE_SEED_SET=fresh` starts without credentials and captures the banner from stderr for the
  wizard test.

## 11. Hooks for later steps

- The wizard is a step registry (`web/src/setup/steps.ts`): id, title, `when(state)` guard, component. Later steps
  register without touching the flow: **custom domain** (sets `KIPPLE_PUBLIC_URL`'s in-app successor and adds it to
  `security.allowed_hosts`), **Cloudflare Access / OTP** (team domain and AUD in-app, with a live token check),
  **network settings** (trusted proxies), **scheduler and log level**, per `docs/parking-lot.md`.
- Server side, env-only configuration moves in-app by the same pattern `security.allowed_hosts` sets: a settings key
  whose effective value is `env if set, else setting`, with the Settings UI showing "set by environment" when the env
  wins. This keeps scripted deploys authoritative.
- **Restore from a backup during setup:** in setup mode there is nothing to lose, which makes a web restore far safer
  than in normal mode. The claim session is the natural gate. Parked; the step 1 screen links to the CLI procedure.
- **Time zone:** the wizard reads the browser's `Intl` time zone and, if it differs from the server's `TZ`, shows the
  line to add (see question 5).

## 12. Testing

**Go unit and integration (PR B, E):**
- Token: format, Crockford folding, constant-time compare, rotation after 100 failures, file created 0600 and
  removed on claim and on a normal-mode start, `kipple setup-token` output.
- Mode derivation: env creds create the row (normal); `KIPPLE_USERNAME` alone gives setup mode; existing row gives
  normal with setup routes answering 404; the flag never flips back.
- Every new route through `rootHandler`: auth, same-origin, Host gate (`421`), open gate reasons, lockout counting,
  `no-store`, no CORS header anywhere (a table test over the full route list).
- `checkCurrent` and `accountPassword` in open mode; `kipple password` resets `auth_mode`.
- **Claim race:** N goroutines with the valid token and M with wrong ones hammer `claim` + `account` against one
  httptest server and one store; exactly one `201`, every other account call `409` or `401`, one row, one session
  for the winner. Run under `-race` in CI (not locally on Host-B, which has no gcc).
- Migration 0010 test (section 6). Port: default, fallback on `EADDRINUSE`, healthcheck probing order.
- Build info: ldflags wiring, `version` first line unchanged (guards RELEASING step 10), downgrade message with and
  without `sys.last_version`.

**Fuzz targets (added to `scripts/fuzz.ps1`):** `FuzzSetupToken` (normalize and compare), `FuzzHostGate` (Host
header parsing: ports, brackets, trailing dots, IDNA, case), `FuzzStarterFeeds` (the validator never panics and never
accepts a non-https or IP-literal URL). `FuzzParse` for OPML already exists.

**Web:** Vitest for each wizard step against `web/src/test/mockApi.ts`, the step registry guards, the fragment
prefill and clearing, the mismatch banner (stored bootstrap vs network bootstrap), and axe on every step.

**Playwright (UAT Suite 1 addition):** `KIPPLE_SEED_SET=fresh`: read the token from the seed's captured stderr,
walk steps 1-6 with a password, then again with open mode on a second data dir; assert a second browser context
without the token cannot claim, and that after completion `/api/setup/*` answers 404. Run at the **mobile preset**
in the browser pane as well (CLAUDE.md: UI phases are verified at the mobile preset before being called done).

**UAT Suite 5 rewrite:** the literal fresh-machine walkthrough becomes: install Docker, paste the README compose
file (or the one-line `docker run`), `docker compose up -d`, `docker logs` for the code, open the address, finish the
wizard, add a feed, connect NetNewsWire with the generated API password, take a backup. Run once on amd64 and once
on an arm64 machine or VM, as a single-host user. A second pass uses `cosign verify` exactly as the README shows.
The build-from-source path moves to a shorter "For developers" check.

## 13. PR breakdown

| PR | Scope | Depends on | Changelog fragment |
|---|---|---|---|
| **A** release workflow | `release.yml`, `ci.yml` `workflow_call`, Dockerfile cross-compile (`$BUILDPLATFORM`, `TARGETARCH`), metadata labels, `RELEASING.md` steps for GHCR, cosign verify text | none | `added` (signed multi-arch images on GHCR) |
| **B** setup-mode backend | `internal/setup` (token, gates, shared account creation), migration 0010, new and changed routes, open mode, Host gate, port 1919 and fallback, healthcheck constant, CLI changes (`setup-token`, `password`, messages), starter-feeds endpoints (list embedded by C; B ships a two-feed placeholder file so it is testable alone) | none | `added` wizard backend, `changed` **Breaking** default port, `security` Host gate |
| **E** build info | ldflags, `version -v`, `/api/about`, `sys.last_version` and downgrade message, `web_build`, mismatch banner, what's-new, About screen | A for the Dockerfile ldflags and web-stage `VERSION` (rebase on A) | `added` |
| **C** wizard UI + feeds file | `web/src/setup/*`, `/welcome` route, `LoginScreen` open-mode branch, `starter/feeds.json` real content, `scripts/check-starter-feeds.mjs`, Playwright addition | B's API contract (this document); merges after B | `added` |
| **D** docs | README Quickstart (pull-and-run first, build-from-source second), `docker-compose.example.yml` (port, image line), `.env.example` (port default, `KIPPLE_ALLOWED_HOSTS`, account variables now optional), `docs/deploy.md` upgrade notes, `docs/design.md` §7.0/§7.1, UAT Suite 5 rewrite | A, B, C, E merged | none (docs) |

**Parallelism:** A, B and E start together. C starts as soon as this document's section 4 is accepted, building
against `mockApi.ts`, and merges after B. E rebases on A because both edit the Dockerfile's build stages. D is last.
One writer per PR; none touches Host-A. Nothing merges before the beta.2 soak ends; all five land, then
0.5.0-beta.1 is cut and deployed per RELEASING (tag first, which also produces the first GHCR image).

## 14. Risks and rollback

| Risk | Likelihood | Mitigation / rollback |
|---|---|---|
| Host-A relies on the default port and goes dark behind `7080:7080` | Low, but costly | Deploy checklist line (8.3); question 1's shim makes it impossible |
| Migration 0010 surprises the live database | Low (one-row rebuild) | Suite 4 rehearsal on a copy of the live snapshot before deploy; pre-migration snapshot; rollback per RELEASING |
| Open mode ends up behind a tunnel | Medium for other users | Open gate refuses forwarded requests; Settings shows the mode prominently |
| Host gate blocks a legitimate setup host | Medium | `421` body names `KIPPLE_ALLOWED_HOSTS`; IP literals always work, so `http://<ip>:1919` is always a way in |
| First GHCR package is private, pulls fail | High on first release | One-time owner step (8.1), part of the beta.1 release checklist |
| `latest` moved by a prerelease | Low | Workflow assertion; `repoint_latest` dispatch to undo |
| arm64 image builds but crashes | Low | QEMU smoke step blocks tagging |
| Tailscale header behavior differs from the docs | Medium | Verified in PR B against a real tailnet before merge; the rule fails closed |

Whole-feature rollback: 0.5.0-beta.1 is a normal release. Returning to 0.3.x means restoring the pre-migration
snapshot the 0010 upgrade wrote and redeploying the previous tag (RELEASING, Rollback). Anything changed after the
upgrade (read state, new items) is lost with it, as with every schema rollback. A database created fresh by 0.5.0
has no 0.3 snapshot and stays on 0.5.

## 15. Open questions for the owner

1. **Port shim for existing installs.** Should an existing database with `KIPPLE_ADDR` unset keep listening on 7080
   (with a WARN pointing at the upgrade note) while fresh databases get 1919? *Recommendation: yes, through 0.x,
   removed at 1.0.* It turns the only breaking change into a warning; the cost is a default that depends on state,
   and the healthcheck probing 7080 as well.
2. **Host-A deploy source.** Keep building from the tag on Host-A, or pull the signed GHCR digest? *Recommendation:
   keep building for 0.5.0-beta.1; switch to pulling by digest from 0.5.0 onward*, so the owner runs the artifact
   everyone else runs and the build-on-host path remains as the fallback.
3. **Open mode and the LAN.** The notice says localhost or Tailscale only. Enforce that (default refuse other
   private-range peers, with a Settings opt-in "Also allow devices on my local network", `security.open_lan`), or
   allow private ranges by default? *Recommendation: refuse by default with the opt-in*: the notice then matches
   what the code does, and a LAN user makes a second, explicit choice.
4. **Onboarding for env-created accounts.** Scripted and existing accounts skip steps 3-6. Also offer "Run setup
   again" in Settings? *Recommendation: yes, cheap* (it clears `sys.setup_completed_at`; steps 3-6 only).
5. **Default time zone of the public image.** `TZ` defaults to `America/New_York` (`internal/config/config.go:45`),
   which is the owner's zone, not a sensible public default; it affects stats day boundaries and schedules.
   *Recommendation: default to `UTC` for new installs in 0.5.0, with the wizard showing the browser's zone and the
   `TZ=` line to add; the owner's instance sets `TZ` explicitly before the upgrade.* Moving TZ in-app is a later
   parking-lot item.
