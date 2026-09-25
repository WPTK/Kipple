package fetch

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const rssBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>T</title><link>https://example.com/</link><ttl>90</ttl>
<item><guid>a</guid><title>A</title><link>https://example.com/a</link><description>&lt;p&gt;hello &lt;img src="/i.png"&gt;&lt;/p&gt;</description></item>
</channel></rss>`

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func snapFor(url string) Snapshot {
	return Snapshot{ID: 1, URL: url, AllowPrivateNet: true, IntervalS: 1800, DedupMode: DedupAuto, HonorTTL: true, Trigger: TriggerScheduled}
}

func doFetch(t *testing.T, c *Client, s Snapshot) *Result {
	t.Helper()
	return c.Fetch(context.Background(), s, t0)
}

func feedServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, NewClient(ClientOptions{Version: "test", PublicURL: "https://rss.example"})
}

func serveRSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(rssBody))
}

func TestStatus200ParsedAndHeaders(t *testing.T) {
	var gotUA, gotAccept, gotAuth string
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotAccept = r.UserAgent(), r.Header.Get("Accept")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `W/"abc"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2026 00:00:00 GMT")
		serveRSS(w, r)
	})
	s := snapFor(srv.URL + "/feed")
	s.HTTPAuth = "bob:secret"
	res := doFetch(t, c, s)
	require.Equal(t, OutcomeOK, res.Outcome)
	require.Equal(t, 200, res.Status)
	require.Len(t, res.Feed.Items, 1)
	require.Equal(t, "Mozilla/5.0 (compatible; Kipple/test; +https://rss.example)", gotUA)
	require.Contains(t, gotAccept, "application/rss+xml")
	require.Equal(t, "Basic Ym9iOnNlY3JldA==", gotAuth)
	require.True(t, res.SetValidators)
	require.Equal(t, `W/"abc"`, res.ETag, "ETag stored verbatim, weak prefix and all")
	require.Equal(t, "Wed, 01 Jan 2026 00:00:00 GMT", res.LastModified)
	require.NotEmpty(t, res.BodyHash)
	require.EqualValues(t, 90*60, res.TTLHintS, "RSS ttl is honoured")
	require.Contains(t, res.Feed.Items[0].ContentHTML, `src="https://example.com/i.png"`, "content is absolutized")
	require.Equal(t, RedirectClear, res.Redirect.Action)

	res.Schedule(t0, func() float64 { return 0.5 })
	require.Equal(t, t0.Add(90*60*time.Second), res.NextFetchAt)
}

func TestConditionalRequests(t *testing.T) {
	var inm, ims atomic.Value
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		inm.Store(r.Header.Get("If-None-Match"))
		ims.Store(r.Header.Get("If-Modified-Since"))
		if r.Header.Get("If-None-Match") == `W/"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		serveRSS(w, r)
	})
	s := snapFor(srv.URL)
	s.ETag, s.LastModified = `W/"abc"`, "Wed, 01 Jan 2026 00:00:00 GMT"

	res := doFetch(t, c, s)
	require.Equal(t, OutcomeNotModified, res.Outcome)
	require.Equal(t, `W/"abc"`, inm.Load())
	require.Equal(t, "Wed, 01 Jan 2026 00:00:00 GMT", ims.Load())
	require.True(t, res.SetValidators)
	require.Equal(t, `W/"abc"`, res.ETag)
	require.Equal(t, "Wed, 01 Jan 2026 00:00:00 GMT", res.LastModified, "no Last-Modified on the 304 keeps ours")

	// Full and ignore_http_cache both drop the validators.
	full := s
	full.Full = true
	require.Equal(t, OutcomeOK, doFetch(t, c, full).Outcome)
	require.Equal(t, "", inm.Load())
	ign := s
	ign.IgnoreHTTPCache = true
	require.Equal(t, OutcomeOK, doFetch(t, c, ign).Outcome)
	require.Equal(t, "", ims.Load())
}

func Test304OverwritesLastModified(t *testing.T) {
	srv, c := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Thu, 02 Jan 2026 00:00:00 GMT")
		w.Header().Set("ETag", `"different"`)
		w.WriteHeader(http.StatusNotModified)
	})
	s := snapFor(srv.URL)
	s.ETag, s.LastModified = `"abc"`, "Wed, 01 Jan 2026 00:00:00 GMT"
	res := doFetch(t, c, s)
	require.Equal(t, OutcomeNotModified, res.Outcome)
	require.Equal(t, `"abc"`, res.ETag, "the ETag is kept")
	require.Equal(t, "Thu, 02 Jan 2026 00:00:00 GMT", res.LastModified)
}

func TestUnchangedBodyHash(t *testing.T) {
	srv, c := feedServer(t, serveRSS)
	first := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, OutcomeOK, first.Outcome)

	s := snapFor(srv.URL)
	s.BodyHash = first.BodyHash
	res := doFetch(t, c, s)
	require.Equal(t, OutcomeUnchanged, res.Outcome)
	require.Nil(t, res.Feed, "no parse on an unchanged body")
	require.True(t, res.SetValidators)

	s.Full = true
	require.Equal(t, OutcomeOK, doFetch(t, c, s).Outcome, "full bypasses the body-hash short circuit")
}

func TestExpiresZeroDropsValidators(t *testing.T) {
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Expires", "0")
		w.Header().Set("ETag", `"x"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2026 00:00:00 GMT")
		serveRSS(w, r)
	})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, OutcomeOK, res.Outcome)
	require.True(t, res.SetValidators)
	require.Empty(t, res.ETag)
	require.Empty(t, res.LastModified)
}

func TestErrorStatusRows(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		class   string
		gone    bool
		retry   time.Duration
		note    string
	}{
		{"404", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, ClassHTTP, false, 0, ""},
		{"418", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(418) }, ClassHTTP, false, 0, ""},
		{"500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, ClassHTTP, false, 0, ""},
		{"502", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }, ClassHTTP, false, 0, ""},
		{"504", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(504) }, ClassHTTP, false, 0, ""},
		{"401", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }, ClassHTTP, false, 0, ""},
		{"403 plain", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }, ClassHTTP, false, 0, ""},
		{"403 cloudflare", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("cf-mitigated", "challenge")
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.WriteHeader(403)
			_, _ = w.Write([]byte("<html>Just a moment</html>"))
		}, ClassCloudflare, false, 0, ""},
		{"410", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(410) }, ClassGone, true, 0, ""},
		{"429 seconds", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(429)
		}, ClassHTTP, false, 600 * time.Second, "retry_after=600s"},
		{"429 date", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", t0.Add(2*time.Hour).Format(http.TimeFormat))
			w.WriteHeader(429)
		}, ClassHTTP, false, 2 * time.Hour, "retry_after=7200s"},
		{"429 clamped low", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		}, ClassHTTP, false, 60 * time.Second, "retry_after=60s"},
		{"429 clamped high", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "999999")
			w.WriteHeader(429)
		}, ClassHTTP, false, 24 * time.Hour, "retry_after=86400s"},
		{"503 no header", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, ClassHTTP, false, 1500 * time.Second, "retry_after=1500s"},
		{"empty", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("ETag", `"e"`)
		}, ClassEmpty, false, 0, ""},
		{"html not a feed", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("ETag", `"e"`)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html><html><body><p>hi</p></body></html>"))
		}, ClassParse, false, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := feedServer(t, tc.handler)
			res := doFetch(t, c, snapFor(srv.URL))
			require.Equal(t, OutcomeError, res.Outcome)
			require.Equal(t, tc.class, res.ErrClass, res.ErrMsg)
			require.Equal(t, tc.gone, res.Gone)
			require.Equal(t, tc.retry, res.RetryAfter)
			if tc.note != "" {
				require.Contains(t, res.Notes, tc.note)
			}
			require.False(t, res.SetValidators, "validators are not stored on an error")
			require.Nil(t, res.Feed)
		})
	}
}

func TestErrorScheduleHonoursRetryAfter(t *testing.T) {
	srv, c := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(429)
	})
	res := doFetch(t, c, snapFor(srv.URL))
	res.Schedule(t0, func() float64 { return 0.5 })
	require.Equal(t, t0.Add(2*time.Hour), res.NextFetchAt, "max(now+backoff, now+retry_after)")
	require.EqualValues(t, 1800, res.CurrentDelayS)
}

func TestOversizeBody(t *testing.T) {
	srv, _ := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	})
	c := NewClient(ClientOptions{MaxResponseBody: 1024})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, ClassTooLarge, res.ErrClass, res.ErrMsg)
}

func TestOversizeIsMeasuredAfterGunzip(t *testing.T) {
	srv, _ := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte(strings.Repeat("a", 64<<10)))
		_ = zw.Close()
	})
	c := NewClient(ClientOptions{MaxResponseBody: 8 << 10})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, ClassTooLarge, res.ErrClass, "a zip bomb is capped on the decompressed stream")
}

func TestGzipBody(t *testing.T) {
	srv, c := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write([]byte(rssBody))
		_ = zw.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(b.Bytes())
	})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
}

func TestCharsetAndBOMOverHTTP(t *testing.T) {
	// windows-1252 bytes under a UTF-8 declaration
	body := append([]byte(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Caf`), 0xE9)
	body = append(body, []byte(`</title><item><guid>1</guid><title>x</title></item></channel></rss>`)...)
	srv, c := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=windows-1252")
		_, _ = w.Write(body)
	})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, "Café", res.Feed.Title)

	srv2, c2 := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(append([]byte{0xEF, 0xBB, 0xBF}, []byte(rssBody)...))
	})
	require.Equal(t, OutcomeOK, doFetch(t, c2, snapFor(srv2.URL)).Outcome)
}

func TestRedirectRows(t *testing.T) {
	var target string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", serveRSS)
	mux.HandleFunc("/perm", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/feed", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/perm308", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/feed", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("/temp", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target+"/feed", http.StatusFound) })
	mux.HandleFunc("/mixed", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target+"/perm", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/loop", http.StatusMovedPermanently)
	})
	srv, c := feedServer(t, mux.ServeHTTP)
	target = srv.URL

	res := doFetch(t, c, snapFor(srv.URL+"/perm"))
	require.Equal(t, OutcomeOK, res.Outcome)
	require.Equal(t, srv.URL+"/feed", res.FinalURL)
	require.Equal(t, RedirectDecision{Action: RedirectSet, To: srv.URL + "/feed", Kind: "permanent", Count: 1}, res.Redirect)

	s := snapFor(srv.URL + "/perm308")
	s.Redirect = RedirectState{To: srv.URL + "/feed", Kind: "permanent", Count: 2}
	res = doFetch(t, c, s)
	require.Equal(t, RedirectMigrate, res.Redirect.Action, "third consecutive permanent chain to the same target")

	res = doFetch(t, c, snapFor(srv.URL+"/temp"))
	require.Equal(t, OutcomeOK, res.Outcome)
	require.Equal(t, "temporary", res.Redirect.Kind)
	require.Equal(t, RedirectSet, res.Redirect.Action)

	res = doFetch(t, c, snapFor(srv.URL+"/mixed"))
	require.Equal(t, "temporary", res.Redirect.Kind, "a mixed 302+301 chain is temporary")
	require.Len(t, res.Hops, 2)

	res = doFetch(t, c, snapFor(srv.URL+"/loop"))
	require.Equal(t, ClassRedirectLoop, res.ErrClass, res.ErrMsg)
	require.Len(t, res.Hops, 5)

	res = doFetch(t, c, snapFor(srv.URL+"/feed"))
	require.Equal(t, RedirectClear, res.Redirect.Action)
}

func TestTimeout(t *testing.T) {
	srv, _ := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	c := NewClient(ClientOptions{ClientTimeout: 150 * time.Millisecond})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, ClassTimeout, res.ErrClass, res.ErrMsg)
}

func TestHeaderTimeout(t *testing.T) {
	srv, _ := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	c := NewClient(ClientOptions{HeaderTimeout: 100 * time.Millisecond})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, ClassTimeout, res.ErrClass, res.ErrMsg)
}

func TestDNSAndConnectAndTLS(t *testing.T) {
	c := NewClient(ClientOptions{})
	res := doFetch(t, c, snapFor("http://no-such-host.invalid/feed"))
	require.Equal(t, ClassDNS, res.ErrClass, res.ErrMsg)

	srv := httptest.NewServer(http.HandlerFunc(serveRSS))
	url := srv.URL
	srv.Close()
	res = doFetch(t, c, snapFor(url))
	require.Equal(t, ClassConnect, res.ErrClass, res.ErrMsg)

	tsrv := httptest.NewTLSServer(http.HandlerFunc(serveRSS))
	defer tsrv.Close()
	res = doFetch(t, c, snapFor(tsrv.URL))
	require.Equal(t, ClassTLS, res.ErrClass, res.ErrMsg)

	s := snapFor(tsrv.URL)
	s.AllowInsecureTLS = true
	require.Equal(t, OutcomeOK, doFetch(t, c, s).Outcome, "allow_insecure_tls accepts the self-signed cert")

	s.DisableHTTP2 = true
	require.Equal(t, OutcomeOK, doFetch(t, c, s).Outcome, "disable_http2 variant works")
}

func TestSSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(serveRSS))
	defer srv.Close()
	c := NewClient(ClientOptions{})

	s := snapFor(srv.URL)
	s.AllowPrivateNet = false
	res := doFetch(t, c, s)
	require.Equal(t, ClassSSRF, res.ErrClass, res.ErrMsg)
	require.Contains(t, res.ErrMsg, "127.0.0.1", "the message names the resolved IP")

	s = snapFor("http://10.1.2.3/feed")
	s.AllowPrivateNet = false
	res = doFetch(t, c, s)
	require.Equal(t, ClassSSRF, res.ErrClass, res.ErrMsg)
	require.Contains(t, res.ErrMsg, "10.1.2.3")
}

func TestBlockedRanges(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.5.5", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "224.0.0.1", "fc00::1", "fe80::1", "64:ff9b::1.2.3.4", "2002:c000:204::1", "::ffff:10.0.0.1", "255.255.255.255"} {
		require.True(t, Blocked(mustAddr(ip)), ip)
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		require.False(t, Blocked(mustAddr(ip)), ip)
	}
}

func TestCancelledWritesNothing(t *testing.T) {
	started := make(chan struct{})
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	res := c.Fetch(ctx, snapFor(srv.URL), t0)
	require.True(t, res.Cancelled)
}
