# Security audit (October 2026)

A code audit of Kipple against hostile feeds, hostile servers and a hostile network, with a probe for every suspected
gap. `docs/threat-model.md` stays the map of what is defended and where; this file records what the audit tried, what
it found and what was done about it. Findings are fixed in pull requests that link back here, and each fix ships with
a regression test that fails without it.

Scope: the Go server (feed fetch, parsing, sanitizing, image proxy, auth, Google Reader API, backup and restore) and
the web app. Out of scope is what the threat model already excludes (host, Docker, proxy, an attacker holding the
owner's password).

## Method

1. Mapped every outbound client, parser, sanitizer, route and SQL site.
2. Wrote throwaway tests for each hypothesis, with hostile servers on loopback and hostile documents, and discarded
   them. A hypothesis counts as confirmed only when the probe reproduced it.
3. Ran `govulncheck` and `npm audit`.

## Findings

Severity is for a single-owner server reachable from the internet through a tunnel. IDs match the probe numbers in the
plan so later pull requests can cite them.

| ID | Severity | Finding | Status |
|---|---|---|---|
| V1 | **High** | A feed with deeply nested elements crashes the whole process | open |
| V14 | **High** | Reachable Go standard library and `golang.org/x/net` vulnerabilities | open |
| V3 | Medium | No cap on items per fetch: 10 MiB parses to about 320k items | open |
| V6 | Medium | Discovery follows redirects to private addresses for feeds with the private-network exception | open |
| V4 | Low | Image proxy retry ladder holds a slot for 3 x the timeout; discovery has no deadline of its own | open |
| V6b | Low | Redirect policy is copied in six places and they disagree; https to http is allowed everywhere | open |
| V7 | Low | Access key fetch uses the default HTTP transport | open |
| V8b | Low | Stored HTML is never re-sanitized after a restore or a policy change | open |
| V5b | Low | Full-text extraction accepts a compressed body it did not ask for | open |
| V9 | Low | ETag and Last-Modified are stored without validation | open |
| V10 | Info | Item image URL passes through unproxied when it cannot be rewritten | open |
| V11 | Info | No default `Cache-Control` for responses that set none | open |
| I1 | Info | No explicit `MaxHeaderBytes`; no cap on event-stream subscribers | open |

### V1 High: nested XML crashes the server

`internal/fetch/parse.go` hands the body to gofeed v1.5.0. gofeed's `parseExtensionElement`
(`internal/shared/extparser.go:95` in that module) calls itself once per child element with no depth limit. A 10 MiB
body of unclosed namespaced elements inside an RSS item (about 2 million deep), or an Atom entry (about 3.5 million),
ends in `fatal error: stack overflow` after two seconds. A Go stack overflow is not a panic, so the `recover()` in
`convertItem` (`parse.go:170`, which only wraps item conversion anyway) cannot catch it. One hostile feed kills the
single container, and it dies again on every poll, so the server cannot stay up while the feed is subscribed.
Well-formed deep nesting does not crash but peaks near 2 GiB of memory.

Not affected: OPML (`encoding/xml` stops at 10,000 levels before the walk), unknown non-namespaced elements (iterative
skip), Atom xhtml content, and the HTML link scanner.

Root cause: the parser's recursion depth is attacker-controlled and unbounded, and the size cap bounds bytes, not
depth. Patch: scan the decoded body once with `xml.Decoder.RawToken` before the parser sees it and refuse a document
nested deeper than a fixed limit (a few hundred levels; real feeds use fewer than 20). That makes the recursive path
unreachable whatever the library does. Also report it upstream.

### V14 High: standard library and x/net vulnerabilities

`govulncheck ./...` reports 10 reachable vulnerabilities in `net/http`, `net/http` HTTP/2, `net/textproto` and
`crypto/tls`, all fixed in Go 1.27.2, and one in `golang.org/x/net` v0.59.0, fixed in v0.60.0. CI resolves `1.27` from
`go.mod` and can install the newest patch, so the check can pass while the shipped image, built from a digest-pinned
`golang:1.27-alpine`, still contains the old standard library. Patch: a `toolchain go1.27.2` line, `x/net` v0.60.0, a
refreshed builder digest, and a CI step that fails when the toolchain in the image differs from the one `go.mod` names.
`npm audit --omit=dev` reports nothing.

### V3 Medium: unbounded items per fetch

A 10 MiB feed of minimal items yields about 320,000 items. Parsing and sanitizing all of them takes about 2 seconds
and 860 MiB at peak. The commit then splits into about 1,280 write transactions and inserts every item before
retention trims, and every trimmed id stays in the ledger, so a feed that rotates ids can add about 300,000 ledger rows
per poll. The largest retention tier is 1,000 (or unlimited), so items past a small multiple of that are inserted only
to be deleted. Patch: keep the first N items in document order before conversion and sanitizing, with N a documented
constant above the largest tier. One cap bounds parse cost, commit work and ledger growth together. The unlimited
retention setting needs its own decision.

### V6 Medium: discovery follows redirects without the per-hop guard

Fetch, extract and favicon scope the private-network exception to the feed's own host on every hop. Discovery
(`internal/discover/discover.go:64`, called from `internal/api/feedadmin.go:238` and `:583`) uses the unscoped
transport, so for a feed that has the exception it follows redirects to loopback, IPv6 loopback and the metadata
address. The probe reached loopback, attempted the metadata address, sent a `Referer` carrying the original URL and
query, and turned credentials in a redirect `Location` into an `Authorization` header. Requires a feed with the
exception that the owner added, so it is Medium. Patch: use the same scoped transport as fetch.

### V4 Low: deadlines

The image proxy retry ladder (`internal/imgproxy/upstream.go:113`) shares its time budget across tries only when a
header budget is set, and the two main entry points pass none, so a source that answers slowly with 403 holds one
request and a per-host slot for up to 45 seconds instead of 15. Discovery's client has no timeout; its two callers wrap
it in 10 seconds, so nothing is exploitable today. Patch: one deadline for the whole ladder, and a deadline inside
discovery.

### V6b Low: one redirect policy

Six clients each carry a redirect check. All cap at 5 hops; only favicon checks the scheme itself, only favicon strips
credentials, only the image proxy strips `Referer`, and all of them follow an https to http redirect. Patch: one
shared policy in package `fetch` (http and https only, credentials stripped, `Referer` stripped, no https to http
downgrade), used by every client.

### V7 Low: Access key fetch

`internal/access/access.go:171` builds a bare `http.Client`: environment proxy, default redirects, no address guard.
The host is the team-domain setting, which only a signed-in owner can change, and the accepted syntax includes IP
literals. Patch: use the guarded transport, refuse redirects and reject IP literals.

### V8b Low: stored HTML is trusted forever

Content is sanitized at ingest and the web app sanitizes again when serving, and its CSP blocks inline script. The
Google Reader API returns stored HTML as is. A restore swaps in a whole database without re-sanitizing, and a future
policy change would leave old rows under the old policy. Patch: a sanitize pass over item content and full text as a
restore step and on a stored policy-version change. The serve-time rewriter is a denylist; a hostile `svg` animation
passes through it, so the ingest allowlist stays the real control.

### V5b, V9, V10, V11, I1 Low and informational

- Extraction treats a `br` or `deflate` body (never requested) as text: reject any `Content-Encoding` the client did
  not ask for.
- ETag and Last-Modified are stored raw, up to the 64 KiB header cap. Validate on store: `ETag` as an entity tag of at
  most 1 KiB, `Last-Modified` through `http.ParseTime`, otherwise store nothing.
- `imgproxy.Rewrite` returns the original URL when it cannot proxy it. Return an empty string instead, so a missing
  secret drops images rather than passing the feed's URL through.
- All JSON already sets `private, no-store` through one writer. Add the same default in `internal/httpx` for anything
  that sets none.
- Set `MaxHeaderBytes` to 64 KiB, and cap event-stream subscribers.

## Controls verified (no finding)

| Area | Result | Tests |
|---|---|---|
| Private, loopback, link-local, CGNAT and mapped-IPv6 addresses | Blocked at connect on the resolved address, for every client | `internal/fetch/ssrfmatrix_test.go`, `ssrfdial_test.go` |
| DNS rebinding | Guard runs on each dial, including redirects | `TestGuardFollowsTheAddressOfEachDial` |
| Redirect to `gopher:`, `file:`, `data:`, `ftp:` | Refused by every client before any request | probe; regression test planned |
| XML entities and DTD (feeds, OPML) | Inert: no expansion, no external fetch | probe; regression test planned |
| Gzip bombs | Capped on the decoded stream: 10 MiB feed, 256 KiB icon, 15 MiB image | probe; regression test planned |
| Tarpit (headers or body) | Every real call path ends within 15 to 20 seconds, except V4 above | probe; regression test planned |
| `CR`/`LF` in conditional headers | Go refuses them in both directions | probe |
| HTML sanitizing, `javascript:` and `data:` links | Allowlist at ingest, rewrite and DOMPurify at render | `internal/sanitize/corpus_test.go`, `web/src/lib/safeHtml.test.ts` |
| Outbound links | `target="_blank"` with `rel="noopener noreferrer"` | `serve_test.go`, `safeHtml.test.ts` |
| Titles and authors | Plain text in JSON and escaped in OPML; React text in the app | probe |
| SQL | Parameterized; id lists use one `json_each` parameter or are bounded | probe |
| Restore upload | Length required, capped, free space checked | probe |
| CSRF, sessions, login pacing | Fetch Metadata plus custom header, hashed cookie value, per-client wait | `internal/api/session_security_test.go`, `login_pacing_test.go` |
| Paths | No filesystem path comes from feed data | read |

## Accepted

Unchanged from the threat model: no absolute session lifetime (#223), no `__Host-` cookie prefix, `style-src
'unsafe-inline'`, open mode on a trusted network, setup without a secret. Also accepted: the Google Reader login
accepts credentials in a query string, because the protocol does; reader tokens do not expire, and an API password
change revokes them; HSTS without `includeSubDomains`, so a shared parent domain is not affected; unknown JSON fields
are ignored, to stay compatible with older web clients.
