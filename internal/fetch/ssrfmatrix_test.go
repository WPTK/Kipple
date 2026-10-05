package fetch_test

// The SSRF matrix: every place Kipple makes an outgoing request for a URL that a
// user, a feed or a redirect supplied, driven with the same hostile targets
// through the real guarded transport.
//
// Entry points (found by grepping internal/ and cmd/ for http.Client,
// http.NewRequest and fetch.Client.Transport):
//
//	feed fetch ......... fetch.Client.Fetch          (scheduler; the Reader API subscribe/quickadd,
//	                                                  web "add feed", OPML import and a feed URL edit
//	                                                  all store a URL that this then fetches)
//	feed discovery ..... discover.Find               (web "add feed": the typed URL and the page it returns)
//	full-text .......... extract.Extractor.Extract   (an article link taken from a feed item)
//	favicon ............ favicon.Lookup              (a feed's site_url, feed URL, or an icon link on the page)
//	image proxy ........ imgproxy.Handler            (an image URL taken from feed HTML; signed path)
//
// Not entry points: access (the Cloudflare Access certs URL) and the container
// health check dial addresses the operator configured; no user value reaches them.
// Every entry point above gets its transport from fetch.Client.Transport, whose
// net.Dialer.Control runs on the resolved address of each dial, so the matrix
// asserts one thing for all of them: a refused target is refused by the guard
// (not by an unrelated failure), and the server standing at the loopback address
// the alias spellings point to is never reached.
//
// Names are resolved by fakedns, so no test reaches a real DNS server; nothing
// is dialled except the loopback servers started here. Hostnames that spell an
// address in an alternate notation (decimal, octal, hex, short) are answered with
// the address an inet_aton-style system resolver (getaddrinfo on Windows and
// glibc) would give them: whether the resolver in use understands those spellings
// or not, the dial has to be refused when it does.
//
// Redirect cases start at a "public" origin simulated by the feed's own
// private-network exception on 127.0.0.1 (the exception covers its own site
// only), so every hop after the first is dialled through the guarded transport.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/discover"
	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fakedns"
	"github.com/WPTK/kipple/internal/favicon"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/imgproxy"
)

var matrixSecret = []byte("0123456789abcdef0123456789abcdef")

var matrixZone = map[string][]string{
	"localhost":                     {"127.0.0.1", "::1"},
	"loopback.test":                 {"127.0.0.1"},
	"private.test":                  {"10.0.0.5"},
	"lan.test":                      {"192.168.1.20"},
	"metadata.test":                 {"169.254.169.254"},
	"tailscale.test":                {"100.100.100.100"},
	"cgnat.test":                    {"100.64.0.1"},
	"mapped.test":                   {"::ffff:127.0.0.1"},
	"mapped-meta.test":              {"::ffff:169.254.169.254"},
	"nat64.test":                    {"64:ff9b::7f00:1"},
	"ula.test":                      {"fd00::1"},
	"multi.test":                    {"10.0.0.5", "127.0.0.1", "::1", "fd00::1"},
	"metadata.google.internal.test": {"169.254.169.254"},
}

// inetAton parses the spellings of an IPv4 address that BSD inet_aton accepts and
// Go's parser does not: 1 to 4 dot-separated numbers, each decimal, 0x hex or
// 0-prefixed octal, the last one filling the remaining bytes.
func inetAton(s string) (netip.Addr, bool) {
	parts := strings.Split(s, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	var nums []uint64
	for _, p := range parts {
		n, err := strconv.ParseUint(p, 0, 32) // base 0: 0x hex, leading 0 octal, else decimal
		if err != nil {
			return netip.Addr{}, false
		}
		nums = append(nums, n)
	}
	var v uint64
	for i, n := range nums[:len(nums)-1] {
		if n > 255 {
			return netip.Addr{}, false
		}
		v |= n << (8 * (3 - uint(i)))
	}
	last := nums[len(nums)-1]
	if last >= 1<<(8*(5-uint(len(nums)))) {
		return netip.Addr{}, false
	}
	v |= last
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

func resolveMatrix(name string) []netip.Addr {
	if ss, ok := matrixZone[name]; ok {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	if a, ok := inetAton(name); ok {
		return []netip.Addr{a}
	}
	return nil
}

// spellings returns the alternate notations of ip that a URL host can use, each
// checked to mean ip under inetAton.
func spellings(t *testing.T, ip string) []string {
	t.Helper()
	a := netip.MustParseAddr(ip).As4()
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	out := []string{
		strconv.FormatUint(uint64(v), 10),                                         // decimal
		fmt.Sprintf("0x%x", v),                                                    // hex
		fmt.Sprintf("0%o", v),                                                     // octal
		fmt.Sprintf("0x%x.0x%x.0x%x.0x%x", a[0], a[1], a[2], a[3]),                // hex per byte
		fmt.Sprintf("0%o.0%o.0%o.0%o", a[0], a[1], a[2], a[3]),                    // octal per byte
		fmt.Sprintf("%d.%d.%d", a[0], a[1], uint32(a[2])<<8|uint32(a[3])),         // a.b.c
		fmt.Sprintf("%d.%d", a[0], uint32(a[1])<<16|uint32(a[2])<<8|uint32(a[3])), // a.b
		fmt.Sprintf("0x%x.%d.%d.%d", a[0], a[1], a[2], a[3]),                      // mixed bases
	}
	for _, s := range out {
		got, ok := inetAton(s)
		require.True(t, ok, s)
		require.Equal(t, ip, got.String(), s)
	}
	return out
}

// matrixTargets are hosts (as written in a URL, without the port) that must be
// refused by the guard.
func matrixTargets(t *testing.T) []string {
	t.Helper()
	hosts := []string{
		// loopback
		"127.0.0.1", "127.255.255.254", "[::1]", "localhost", "loopback.test", "LOCALHOST", "localhost.",
		"[::ffff:127.0.0.1]", "[::ffff:7f00:1]", "[0:0:0:0:0:ffff:7f00:1]", "mapped.test", "[::127.0.0.1]",
		// private ranges
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", "[::ffff:10.0.0.1]", "[::ffff:192.168.1.1]",
		"private.test", "lan.test", "[fd00::1]", "[fc00::1]", "ula.test",
		// link-local and cloud metadata (AWS, GCP, Azure, Oracle and DigitalOcean answer 169.254.169.254;
		// AWS also fd00:ec2::254)
		"169.254.169.254", "169.254.0.1", "[fe80::1]", "[::ffff:169.254.169.254]", "[::ffff:a9fe:a9fe]",
		"[fd00:ec2::254]", "metadata.test", "mapped-meta.test", "metadata.google.internal.test",
		// CGNAT, which includes Tailscale's 100.64.0.0/10
		"100.64.0.1", "100.100.100.100", "100.127.255.254", "tailscale.test", "cgnat.test",
		// unspecified, benchmarking, reserved, multicast, broadcast
		"0.0.0.0", "[::]", "198.18.0.1", "240.0.0.1", "224.0.0.1", "255.255.255.255", "[ff02::1]",
		// IPv6 transition forms that embed a private IPv4 address
		"[64:ff9b::7f00:1]", "nat64.test", "[2002:7f00:1::]", "[2002:a00:1::]", "[2001:0:4136:e378:8000:63bf:3fff:fdd2]",
		// every answer private
		"multi.test",
		// userinfo tricks: the host is what follows the @
		"example.com@127.0.0.1", "example.com:80@127.0.0.1", "user:pass@169.254.169.254", "good.example%40evil@10.0.0.1",
	}
	return hosts
}

// numericTargets are alternate spellings of loopback, private, link-local and CGNAT
// addresses. Go's own resolver refuses a name whose last label is a number before
// it dials (fail closed, "no such host"); a system resolver that follows inet_aton
// (getaddrinfo) turns them into the address, which the guard then refuses.
// Either way nothing is dialled; TestGuardRefusesLiteralSpellings in package
// fetch covers the second case on the resolved address.
func numericTargets(t *testing.T) []string {
	t.Helper()
	var hosts []string
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.168.1.1", "100.100.100.100", "0.0.0.0"} {
		hosts = append(hosts, spellings(t, ip)...)
	}
	// Short forms that only make sense for some addresses.
	return append(hosts, "127.1", "127.0.1", "10.1", "0", "0.0", "0x0", "127.0.0.1.", "169.254.169.254.")
}

// matrixSchemes are targets that are not http(s) URLs for a server.
var matrixSchemes = []string{
	"file:///etc/passwd", "file://127.0.0.1/etc/passwd", "ftp://127.0.0.1:{P}/", "gopher://127.0.0.1:{P}/_GET%20/",
	"dict://127.0.0.1:{P}/info", "ldap://127.0.0.1:{P}/", "sftp://127.0.0.1/", "ws://127.0.0.1:{P}/", "wss://127.0.0.1:{P}/",
	"javascript:alert(1)", "data:text/html,<script>alert(1)</script>", "jar:http://127.0.0.1:{P}!/", "view-source:http://127.0.0.1:{P}/",
	"//127.0.0.1:{P}/", "/etc/passwd", "127.0.0.1:{P}/feed", "http:127.0.0.1:{P}/", "http:///feed", "http://", "", " ",
	"HtTp://127.0.0.1:{P}/", // scheme case is not a bypass: the host is still loopback
}

// matrixEnv is the loopback lure the alias spellings point at, and the origin
// that redirects.
type matrixEnv struct {
	fc      *fetch.Client
	lure    *httptest.Server
	lureHit atomic.Int32
	origin  *httptest.Server // 302s to ?to=
	port    string
	// proxyErr is the last RoundTrip error the image proxy's transport saw.
	proxyErr atomic.Value
}

func newMatrixEnv(t *testing.T) *matrixEnv {
	t.Helper()
	fakedns.Install(t, resolveMatrix)
	e := &matrixEnv{fc: fetch.NewClient(fetch.ClientOptions{Version: "test"})}
	e.lure = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		e.lureHit.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>reached</body></html>"))
	}))
	t.Cleanup(e.lure.Close)
	e.origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", r.URL.Query().Get("to"))
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(e.origin.Close)
	u, err := url.Parse(e.lure.URL)
	require.NoError(t, err)
	e.port = u.Port()
	return e
}

// result is what one entry point did with a URL.
type result struct {
	err      error
	blocked  bool // the failure is the SSRF guard's refusal
	rejected bool // the URL was refused as malformed before any dial (image proxy: 400)
}

type entry struct {
	name string
	// run asks the entry point to fetch start. grant is the feed's private-network
	// exception for 127.0.0.1 (the redirect origin).
	run func(t *testing.T, e *matrixEnv, start string, grant bool) result
}

type spyRT struct {
	inner http.RoundTripper
	e     *matrixEnv
}

func (s spyRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := s.inner.RoundTrip(r)
	if err != nil {
		s.e.proxyErr.Store(err)
	}
	return resp, err
}

func isBlocked(err error) bool {
	var be *fetch.BlockedError
	return errors.As(err, &be)
}

func entries() []entry {
	ctx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 20*time.Second)
	}
	return []entry{
		{"feed fetch", func(t *testing.T, e *matrixEnv, start string, grant bool) result {
			c, cancel := ctx()
			defer cancel()
			res := e.fc.Fetch(c, fetch.Snapshot{ID: 1, URL: start, AllowPrivateNet: grant, IntervalS: 1800,
				DedupMode: fetch.DedupAuto, HonorTTL: true, Trigger: fetch.TriggerScheduled}, time.Now())
			if res.Outcome != fetch.OutcomeError {
				return result{}
			}
			return result{err: errors.New(res.ErrMsg), blocked: res.ErrClass == fetch.ClassSSRF}
		}},
		{"discovery", func(t *testing.T, e *matrixEnv, start string, grant bool) result {
			c, cancel := ctx()
			defer cancel()
			guarded := e.fc.Transport(false, false, false)
			rt := guarded
			if grant {
				// A web "add feed" has no exceptions; the origin's grant stands in for a public first hop.
				rt = hopGrant{host: "127.0.0.1", granted: e.fc.Transport(true, false, false), guarded: guarded}
			}
			_, err := discover.Find(c, rt, "ua", "", start, false)
			return result{err: err, blocked: isBlocked(err)}
		}},
		{"full-text extraction", func(t *testing.T, e *matrixEnv, start string, grant bool) result {
			c, cancel := ctx()
			defer cancel()
			ex := extract.New(extract.Options{Transport: e.fc.Transport, Timeout: 15 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			tg := extract.Target{URL: start, AllowPrivate: grant}
			if grant {
				tg.FeedHost = "127.0.0.1"
			}
			_, err := ex.Extract(c, tg)
			return result{err: err, blocked: err != nil && (strings.Contains(err.Error(), "not allowed") || strings.Contains(err.Error(), "exception does not cover"))}
		}},
		{"favicon", func(t *testing.T, e *matrixEnv, start string, grant bool) result {
			c, cancel := ctx()
			defer cancel()
			_, err := favicon.Lookup(c, favicon.Request{
				SiteURL:   start,
				Transport: favicon.ScopedTransport(e.fc.Transport, "127.0.0.1", grant, false, false),
				UserAgent: "ua",
			})
			return result{err: err, blocked: isBlocked(err)}
		}},
		{"image proxy", func(t *testing.T, e *matrixEnv, start string, grant bool) result {
			h := imgproxy.New(imgproxy.Options{
				Secret: matrixSecret,
				Transport: func(allowPrivate, insecure bool) http.RoundTripper {
					return spyRT{inner: e.fc.Transport(allowPrivate, insecure, false), e: e}
				},
			})
			flags := 0
			if grant {
				flags = imgproxy.FlagPrivateNet
			}
			e.proxyErr = atomic.Value{}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, proxyRequest(start, flags))
			if rec.Code == http.StatusOK {
				return result{}
			}
			err, _ := e.proxyErr.Load().(error)
			return result{err: fmt.Errorf("status %d: %v", rec.Code, err), blocked: isBlocked(err), rejected: rec.Code == http.StatusBadRequest}
		}},
	}
}

// hopGrant sends requests for host through granted and the rest through guarded.
type hopGrant struct {
	host             string
	granted, guarded http.RoundTripper
}

func (h hopGrant) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() == h.host {
		return h.granted.RoundTrip(r)
	}
	return h.guarded.RoundTrip(r)
}

func proxyRequest(orig string, flags int) *http.Request {
	r := httptest.NewRequest(http.MethodGet, imgproxy.Path(matrixSecret, flags, orig), nil)
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/img/"), "/")
	if len(parts) == 3 {
		r.SetPathValue("sig", parts[0])
		r.SetPathValue("flags", parts[1])
		r.SetPathValue("u", parts[2])
	}
	return r
}

// requireGuardRefused asserts the request failed and the guard is the reason. For an
// alternate numeric spelling the resolver may refuse the name first (see numericTargets).
func requireGuardRefused(t *testing.T, res result, host string, numeric bool) {
	t.Helper()
	require.Error(t, res.err, "must not succeed")
	if res.rejected && strings.Contains(host, "@") {
		return // the image proxy refuses a URL with credentials in it outright
	}
	if numeric {
		return // the resolver refused the name, or the guard refused its address; the lure counter is checked by the caller
	}
	require.True(t, res.blocked, "must be the guard that refuses, got: %v", res.err)
}

func withPort(e *matrixEnv, tmpl string) string { return strings.ReplaceAll(tmpl, "{P}", e.port) }

// Every entry point refuses every hostile host it is given directly, with the
// guard's refusal, and never reaches the loopback server the aliases point at.
func TestSSRFMatrixDirectTargets(t *testing.T) {
	e := newMatrixEnv(t)
	hosts := matrixTargets(t)
	numeric := numericTargets(t)
	for _, ep := range entries() {
		for _, h := range append(append([]string{}, hosts...), numeric...) {
			start := "http://" + h + ":" + e.port + "/feed"
			t.Run(ep.name+"/"+h, func(t *testing.T) {
				requireGuardRefused(t, ep.run(t, e, start, false), h, slices.Contains(numeric, h))
			})
		}
	}
	require.Zero(t, e.lureHit.Load(), "a request reached the loopback server")
}

// A host name for the guard's own port: https and an explicit default port are
// guarded the same way (the dial is what is checked, not the scheme).
func TestSSRFMatrixHTTPSAndDefaultPorts(t *testing.T) {
	e := newMatrixEnv(t)
	for _, ep := range entries() {
		for _, start := range []string{
			"https://127.0.0.1/feed", "https://169.254.169.254/latest/meta-data/", "http://169.254.169.254/latest/meta-data/",
			"https://[::1]:443/", "http://10.0.0.1:80/", "http://localhost/",
		} {
			t.Run(ep.name+"/"+start, func(t *testing.T) {
				res := ep.run(t, e, start, false)
				require.Error(t, res.err)
				require.True(t, res.blocked, "got: %v", res.err)
			})
		}
	}
}

// URLs that are not http(s) are refused by every entry point and nothing is dialled.
func TestSSRFMatrixNonHTTPSchemes(t *testing.T) {
	e := newMatrixEnv(t)
	for _, ep := range entries() {
		for _, tmpl := range matrixSchemes {
			start := withPort(e, tmpl)
			if strings.EqualFold(start, "HtTp://127.0.0.1:"+e.port+"/") {
				continue // an http URL after all; covered by the direct matrix
			}
			t.Run(ep.name+"/"+tmpl, func(t *testing.T) {
				res := ep.run(t, e, start, false)
				require.Error(t, res.err, "must be refused")
			})
		}
	}
	require.Zero(t, e.lureHit.Load())
}

// A redirect from an allowed origin to any hostile target is refused at that hop
// (each hop is dialled through the guarded transport), including a redirect to a
// non-http scheme, and the second hop never reaches the loopback server. The
// origin's own address is left out: the feed's exception covers its own host.
func TestSSRFMatrixRedirectTargets(t *testing.T) {
	e := newMatrixEnv(t)
	hosts := matrixTargets(t)
	numeric := numericTargets(t)
	for _, ep := range entries() {
		for _, h := range append(append([]string{}, hosts...), numeric...) {
			if _, host, _ := strings.Cut(h, "@"); host == "127.0.0.1" || h == "127.0.0.1" {
				continue // the origin's own host (userinfo does not change it): a hop to it keeps the feed's exception by design
			}
			to := "http://" + h + ":" + e.port + "/feed"
			t.Run(ep.name+"/"+h, func(t *testing.T) {
				requireGuardRefused(t, ep.run(t, e, e.origin.URL+"/r?to="+url.QueryEscape(to), true), h, slices.Contains(numeric, h))
			})
		}
		for _, tmpl := range matrixSchemes {
			to := withPort(e, tmpl)
			if !strings.Contains(to, ":") || strings.HasPrefix(strings.ToLower(to), "http") || strings.HasPrefix(to, "127.") || strings.HasPrefix(to, "//") {
				continue // relative or http forms resolve against the origin or are covered above
			}
			t.Run(ep.name+"/scheme "+tmpl, func(t *testing.T) {
				res := ep.run(t, e, e.origin.URL+"/r?to="+url.QueryEscape(to), true)
				require.Error(t, res.err, "must not follow")
			})
		}
	}
	require.Zero(t, e.lureHit.Load(), "a redirect reached the loopback server")
}

// A redirect chain that bounces between allowed origins and then a private host
// is stopped at the private hop, however deep it sits in the chain.
func TestSSRFMatrixRedirectChainEndsInPrivate(t *testing.T) {
	e := newMatrixEnv(t)
	hop2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(hop2.Close)
	to := hop2.URL + "/x" // same host (127.0.0.1) as the origin, so this hop keeps the exception
	for _, ep := range entries() {
		t.Run(ep.name, func(t *testing.T) {
			res := ep.run(t, e, e.origin.URL+"/r?to="+url.QueryEscape(to), true)
			require.Error(t, res.err)
			require.True(t, res.blocked, "got: %v", res.err)
		})
	}
}
