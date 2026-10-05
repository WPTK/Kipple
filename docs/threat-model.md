# Threat model

One page, for the person who runs Kipple and for anyone who wants to test it. Kipple is a single-user, self-hosted
reader: one account, one container, one SQLite database. Design detail is in `docs/design.md`; accepted risks in
`docs/risk-register.md`; how to report a problem in `SECURITY.md`.

## Assets

| Asset | Why it matters |
|---|---|
| The web session and the account password hash (argon2id) | Control of the reader and everything it can reach. |
| The Reader API password and tokens | A second way in for sync apps. |
| The database (`/data`): subscriptions, read and star state, reading statistics, saved filters | Private reading history. Also holds feed HTTP credentials set per feed. |
| Backups (the export zip, scheduled snapshots) | A copy of the database. Mode 0600, in `/data`. |
| The host's network position | Kipple fetches arbitrary URLs from inside your network. A fetcher is a way to reach what only your server can. |

## Trust boundaries and attackers

| Boundary | Attacker | What they control |
|---|---|---|
| Internet to the public URL (reverse proxy or tunnel, optionally behind an access proxy) | Anyone | Requests to the login page, the Reader API prefix and anything the proxy lets through. |
| Browser to Kipple | A hostile web page the owner visits | Cross-site requests with the owner's cookies. |
| Feed publishers and the sites their links lead to | A malicious or compromised feed | Feed XML, HTML content, image and icon URLs, article pages, redirects, response sizes and timing. |
| LAN or tailnet to the port (open mode or a wider bind) | Another device on the network | Requests that arrive without a password if the owner turned open mode on. |
| Reader API clients | A stolen app token | The Reader API surface only. |
| Backup and restore files | Someone who hands the owner a zip, or reads a copy | Archive structure and size. |

Out of scope: the host operating system, Docker, the proxy and the access proxy themselves; an attacker who already has
the owner's password or a shell on the host; denial of service by someone who can already sign in; multi-user isolation
(there is one user).

## Mitigations

| Threat | Mitigation | Where |
|---|---|---|
| SSRF through any user, feed or redirect URL | One dial-time guard on the resolved address of every connection (loopback, private, link-local and metadata, CGNAT and Tailscale, mapped and transition IPv6); no proxy; per-feed private-network exception covers that feed's own host only, redirects included | `internal/fetch/ssrf.go`, `client.go`; tests `internal/fetch/ssrfmatrix_test.go`, `ssrfdial_test.go` |
| URL syntax tricks | One check for every way a feed URL is stored: read as typed (a missing scheme becomes https, a `feed:` wrapper is removed) and then http(s) only, no credentials in the URL, literal blocked addresses refused; a page address resolved to its feed on the first fetch goes through the same guarded client and keeps a feed's exceptions on its own site | `internal/store/subs.go` (`ValidateFeedURL`), `internal/feedurl`, `internal/fetch/discover.go` |
| Hostile feed HTML (XSS, mXSS) | Allowlist sanitizer on ingest, serve-time rewrite, strict CSP (no inline script, `frame-ancestors 'none'`), iframes only for two embed hosts with a fixed sandbox | `internal/sanitize`, `internal/httpx/headers.go`; corpus `internal/sanitize/corpus_test.go`, fuzz targets |
| Cross-site request forgery | Fetch Metadata and Origin check plus a custom client header on state-changing requests; SameSite=Lax cookie | `internal/api/api.go` (`sameOrigin`) |
| Cross-origin reads of the Reader API | The Reader API answers CORS with `Access-Control-Allow-Origin: *` and no credentials, which gives another origin nothing: it authenticates only by a token the caller must already hold (header or `T`), never by a cookie. The web app's cookie-authenticated `/api` routes send no CORS headers and keep `Cross-Origin-Resource-Policy: same-origin` | `internal/greader/api.go` (`serve`), `internal/httpx/headers.go`; tests `internal/greader/conformance_cors_test.go` |
| Compression length oracles (BREACH) | gzip only for compressible bodies; no compressed response puts a secret beside request-reflected input (the token and backup-download responses are their own small bodies) | `internal/httpx/compress.go` |
| Session theft and fixation | 256-bit random cookie value, stored only as its hash; HttpOnly, SameSite=Lax, Secure when the effective scheme is https; logout deletes the row; a password change signs out other sessions; `kipple password` signs out all | `internal/api/api.go`, `login.go`, `account.go`, `internal/store/sessions.go`; tests `internal/api/session_security_test.go` |
| Password guessing | argon2id, per-client escalating wait, one hashing slot, same for Reader API login | `internal/auth`, `internal/api/login.go`, design section 6.3 |
| Spoofed client address or Access header | Forwarded headers are honoured only from `KIPPLE_TRUSTED_PROXY_IPS`; Access tokens are verified against the team's keys | `internal/auth/clientip.go`, `internal/access` |
| DNS rebinding against the app | Host header gate in setup and open mode | `internal/setup/hosts.go`, `internal/api/hostgate.go` |
| Image proxy abuse (bombs, huge files) | Signed URLs, size and time caps, strict JPEG walk and decode-cost budget before transcoding | `internal/imgproxy` |
| Hostile OPML or backup archive | Go's XML decoder has no external entities; imported feeds never get the private-network or insecure-TLS exceptions; archive entry names are flat and bounded | `internal/opml`, `internal/backup/archive.go` |
| Container compromise | Non-root, read-only root, no capabilities, no-new-privileges, memory and process limits | `docker-compose.example.yml`, `docs/deploy.md` |
| Tampered image or dependency | Signed image, build provenance, SBOM, SHA-pinned actions and base image, Trivy, govulncheck, gitleaks | `.github/workflows/`, `docs/RELEASING.md` |

## Residual risks (accepted)

- **No absolute session lifetime.** A session slides 90 days on use and never expires while it is used at least that
  often. A stolen cookie is valid until sign-out or a password change. Tracked in #223.
- **Cookie is not `__Host-` prefixed**, because Secure must stay conditional so plain-HTTP LAN use works.
- **`style-src 'unsafe-inline'`** in the page CSP (component library inline styles). Scripts stay `'self'` only.
- **Open mode** means anything that reaches the port is signed in. It is limited to loopback, tailnet and private
  addresses by the open gate, but the LAN is trusted when you choose it.
- **The SSRF guard is application-level.** A bug in it is a reach into your network. If your network has sensitive
  internal services, add an egress firewall or put Kipple on its own Docker network: the guard should never be the only
  barrier.
- **The private-network exception is per feed and trusted**: a feed you allow to reach your LAN can serve content for
  anything on that host.
- Feed HTTP credentials are stored in the database unencrypted (they must be sent to the feed).
- **Regulation.** An unmonetised self-hosted project is outside the EU Cyber Resilience Act's manufacturer duties as
  the Commission describes them; reassess if that changes. This is not legal advice.

## Pentest checklist

Run against your own instance, from outside the network and from inside it. Replace `K` with your base URL.

1. **Auth bypass.** Request every `/api/*` path with no cookie: all answer 401 except the listed public ones. Replay a
   cookie after logout and after a password change.
2. **Access and proxy headers.** With the access proxy on, send `Cf-Access-Jwt-Assertion` with a forged, expired and
   other-team token. Send `X-Forwarded-For` and `X-Forwarded-Proto` from an address not in `KIPPLE_TRUSTED_PROXY_IPS`;
   the client address and scheme must not change.
3. **Login pacing.** Ten wrong passwords from one address: waits grow. From a second address the first one's delay is
   not shared.
4. **CSRF.** From another origin, POST to a state-changing route with the owner's cookie (a form post, then `fetch` with
   `credentials: 'include'`): refused.
5. **Reader API.** `POST /api/greader.php/accounts/ClientLogin` with a wrong password; use a token after the API
   password changes (revoked); try web routes with a Reader token (refused).
6. **SSRF.** Add feeds, run discovery, open full-text and load images for URLs such as `http://169.254.169.254/`,
   `http://2130706433/`, `http://[::ffff:127.0.0.1]/`, a name you control that resolves to `127.0.0.1`, and a public URL
   that redirects to each of them. Every one must fail with an "address not allowed" style error and no request must
   reach the target (watch it).
7. **Feed content.** Serve a feed whose items carry the payloads in `internal/sanitize/corpus_test.go`; open each item in
   the web app and in a Reader client; no script runs and the browser console reports no CSP violation from inline
   script.
8. **Image proxy.** Request `/img/...` with a bad signature, a valid one for a 1 GB file, a 30000x30000 JPEG and a
   truncated one: refused or capped, memory stays bounded.
9. **OPML import.** Import a billion-laughs file, a file with `file:`, `ftp:` and private-address feeds, and one with
   `kipple:allow_private_net="1"`: nothing is fetched from the private addresses and no exception is applied.
10. **Backup and restore.** Restore a zip with `../` and absolute names, a very large entry and a mismatched database
    (`kipple restore`): refused.
11. **Storage.** In the volume: backups and the database are not world-readable; the cookie value does not appear in the
    `sessions` table.
12. **Container.** `docker inspect` shows non-root, read-only root, no capabilities; the port is bound to the address you
    chose.
13. **Supply chain.** Verify the image signature, the provenance and the SBOM as in `docs/deploy.md`.
