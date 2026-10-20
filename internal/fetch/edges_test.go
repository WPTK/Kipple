package fetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/feedurl"
)

// The tests in this file pin limits and edges one step inside and one step outside, found by mutation testing: each
// one fails if its check moves by one or flips.

// A page offers at most maxFeedLinks feeds, whether its <link> tags are written open or self-closed.
func TestFeedLinksLimitAndTagForms(t *testing.T) {
	var b strings.Builder
	b.WriteString("<html><head>")
	for i := range maxFeedLinks + 1 {
		close := ">"
		if i%2 == 1 {
			close = "/>"
		}
		fmt.Fprintf(&b, `<link rel="alternate" type="application/rss+xml" href="/feed/%d"%s`, i, close)
	}
	b.WriteString("</head><body></body></html>")
	links := FeedLinks([]byte(b.String()), "https://example.com/")
	require.Len(t, links, maxFeedLinks)
	require.Equal(t, "https://example.com/feed/0", links[0].URL)
	require.Equal(t, "https://example.com/feed/1", links[1].URL, "a self-closed tag counts")

	one := FeedLinks([]byte(`<head><link rel="alternate" type="application/atom+xml" href="/a"/></head>`), "https://example.com/")
	require.Equal(t, []FeedLink{{URL: "https://example.com/a", Type: "atom"}}, one)
}

// A comment or XML declaration that never closes is not a page; closed ones are skipped.
func TestLooksHTMLUnclosedPreamble(t *testing.T) {
	require.False(t, LooksHTML([]byte("<!-- never closed <html>")))
	require.False(t, LooksHTML([]byte(`<?xml version="1.0" <html>`)))
	require.True(t, LooksHTML([]byte("<!---->\n<?xml version=\"1.0\"?><html>")))
	require.True(t, LooksHTML([]byte("<!-- a --><!-- b --><!doctype html>")))
}

// The validators keep every printable ASCII character up to '~', and the bounds are inclusive.
func TestCleanValidatorsEdges(t *testing.T) {
	for _, c := range []struct {
		etag, lm         string
		wantETag, wantLM bool
	}{
		{`!"~`, " ~", true, true},
		{"a b", "\x1f", false, false},
		{"a\x7f", "a\x7f", false, false},
		{strings.Repeat("e", maxETagLen), strings.Repeat("l", maxLastModifiedLen), true, true},
		{strings.Repeat("e", maxETagLen+1), strings.Repeat("l", maxLastModifiedLen+1), false, false},
	} {
		e, l := CleanValidators(c.etag, c.lm)
		require.Equal(t, c.wantETag, e == c.etag && e != "", "etag %q", c.etag)
		require.Equal(t, c.wantLM, l == c.lm && l != "", "last-modified %q", c.lm)
	}
}

// A redirect is permanent only when every hop says so, and with no hops at all it is not; a permanent one seen for
// the third time migrates and counts three.
func TestDecideRedirectEdges(t *testing.T) {
	const from, to = "https://a.example/feed", "https://b.example/feed"
	require.Equal(t, RedirectDecision{Action: RedirectSet, To: to, Kind: "temporary"}, DecideRedirect(from, RedirectState{}, to, nil))
	perm := []Hop{{Status: 301}}
	require.Equal(t, RedirectDecision{Action: RedirectSet, To: to, Kind: "permanent", Count: 2},
		DecideRedirect(from, RedirectState{To: to, Kind: "permanent", Count: 1}, to, perm))
	require.Equal(t, RedirectDecision{Action: RedirectMigrate, To: to, Kind: "permanent", Count: 3},
		DecideRedirect(from, RedirectState{To: to, Kind: "permanent", Count: 2}, to, perm))
}

// One guid and one link repeated: each later copy takes the next free counter, and the uids are what the store
// already holds for such a feed, so they must not change.
func TestAssignUIDsRepeatedGUIDAndLink(t *testing.T) {
	items := []Item{{GUID: "g", RawLink: "https://x.example/a"}, {GUID: "g", RawLink: "https://x.example/a"}, {GUID: "g", RawLink: "https://x.example/a"}}
	kept, _ := AssignUIDs(items, DedupAuto)
	require.Len(t, kept, 3)
	require.Equal(t, "g:"+H("g|https://x.example/a"), kept[0].UID)
	require.Equal(t, "g:"+H("g|https://x.example/a|2"), kept[1].UID)
	require.Equal(t, "g:"+H("g|https://x.example/a|3"), kept[2].UID)

	// A link that spells the next counter's key makes the counter step on again.
	items = []Item{{GUID: "g", RawLink: "L"}, {GUID: "g", RawLink: "L|3"}, {GUID: "g", RawLink: "L"}}
	kept, _ = AssignUIDs(items, DedupAuto)
	require.Equal(t, "g:"+H("g|L|3"), kept[1].UID)
	require.Equal(t, "g:"+H("g|L|4"), kept[2].UID)
}

// The duplicate-guid note needs more than 5% of the guided items to repeat a guid: one in twenty is not enough.
func TestAssignUIDsDuplicateNoteThreshold(t *testing.T) {
	items := func(n int) []Item {
		out := make([]Item, n)
		for i := range out {
			out[i] = Item{GUID: fmt.Sprintf("g%d", i), RawLink: fmt.Sprintf("https://x.example/%d", i)}
		}
		out[n-1].GUID = "g0"
		return out
	}
	_, notes := AssignUIDs(items(20), DedupAuto)
	require.Empty(t, notes, "1 of 20 is 5%")
	_, notes = AssignUIDs(items(19), DedupAuto)
	require.Equal(t, []string{"guid_duplicates: 1/19"}, notes)
}

// A body labelled single-byte that is really UTF-8 is read as UTF-8, down to the lowest UTF-8 lead byte (0xC2, as in
// a no-break space); a UTF-8 label on bytes that are not UTF-8 falls back to the HTTP charset, else windows-1252.
func TestDecodeBodyRepairEdges(t *testing.T) {
	nbsp := []byte("<?xml version=\"1.0\" encoding=\"iso-8859-1\"?><rss>a\xc2\xa0b</rss>")
	d := DecodeBody(nbsp, "")
	require.True(t, d.Repaired)
	require.Equal(t, "utf-8", d.Source)
	require.Contains(t, string(d.Body), "a\u00A0b")

	latin2 := []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?><rss>\xb1</rss>") // ą in iso-8859-2
	d = DecodeBody(latin2, "iso-8859-2")
	require.True(t, d.Repaired)
	require.Equal(t, "iso-8859-2", d.Source)
	require.Contains(t, string(d.Body), "ą")
	d = DecodeBody(latin2, "utf-8")
	require.Equal(t, "windows-1252", d.Source)
	require.Contains(t, string(d.Body), "±")
}

// The phase key is the id plus the salt. It places every feed's fetch slots, so a change would move them all at once.
func TestPhaseKeyIsIDPlusSalt(t *testing.T) {
	require.Equal(t, uint64(8), PhaseKey(5, 3))
	require.Equal(t, uint64(0), PhaseKey(-3, 3))
}

// A zero option takes the design value and a set one is kept. Of the defaults only the 10 MiB body cap shows in a
// quick fetch; the default timeouts are seconds long, so for them a zero is checked only as "not no time at all".
func TestNewClientDefaults(t *testing.T) {
	srv, _ := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cap":
			_, _ = w.Write([]byte(strings.Repeat("x", maxBodyBytes)))
		case "/over":
			_, _ = w.Write([]byte(strings.Repeat("x", maxBodyBytes+1)))
		default:
			serveRSS(w, r)
		}
	})
	def := NewClient(ClientOptions{})
	res := doFetch(t, def, snapFor(srv.URL))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.NotEqual(t, ClassTooLarge, doFetch(t, def, snapFor(srv.URL+"/cap")).ErrClass, "10 MiB is within the cap")
	require.Equal(t, ClassTooLarge, doFetch(t, def, snapFor(srv.URL+"/over")).ErrClass, "one byte more is not")

	// A listener that accepts and never says anything: every wait on it lasts until a timeout ends it.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = silent.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	mute := "http://" + silent.Addr().String() + "/"
	for name, c := range map[string]struct {
		opt  ClientOptions
		url  string
		want string
	}{
		"dial timeout":   {ClientOptions{DialTimeout: 1}, "http://192.0.2.1/", ClassTimeout}, // never answers in time
		"TLS timeout":    {ClientOptions{TLSTimeout: 1}, "https://" + silent.Addr().String() + "/", ClassTimeout},
		"header timeout": {ClientOptions{HeaderTimeout: 1}, mute, ClassTimeout},
		"client timeout": {ClientOptions{ClientTimeout: 1}, mute, ClassTimeout},
		"body cap":       {ClientOptions{MaxResponseBody: 5}, srv.URL, ClassTooLarge},
	} {
		// A timeout that was not kept leaves the fetch to the caller, who cancels it after 5 s: cancelled, not timed out.
		ctx, cancel := context.WithCancel(t.Context())
		stop := time.AfterFunc(5*time.Second, cancel)
		res := NewClient(c.opt).Fetch(ctx, snapFor(c.url), t0)
		stop.Stop()
		cancel()
		require.False(t, res.Cancelled, name)
		require.Equal(t, c.want, res.ErrClass, "%s: %s", name, res.ErrMsg)
	}
}

// A failure is scheduled as the next one in the run: a feed with one failure behind it waits twice its interval.
func TestScheduleCountsThisFailure(t *testing.T) {
	r := &Result{Outcome: OutcomeError, Snap: Snapshot{IntervalS: 1800, ConsecutiveFailures: 1}}
	r.Schedule(t0, func() float64 { return 0.5 })
	require.EqualValues(t, 3600, r.CurrentDelayS)
}

// The browser User-Agent "worked" when its answer is a 304 or any 2xx, and not otherwise.
func TestUserAgentFallbackWorkedEdges(t *testing.T) {
	for code, want := range map[int]bool{304: true, 299: true, 300: false} {
		srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.UserAgent() != BrowserUserAgent {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(code)
			if code == 299 {
				_, _ = w.Write([]byte(rssBody))
			}
		})
		s := snapFor(srv.URL + "/feed")
		s.RetryUserAgent = BrowserUserAgent
		require.Equal(t, want, doFetch(t, c, s).UAFallbackWorked, code)
	}
}

// The final URL is recorded normalized: a redirect target's fragment is not part of it.
func TestFinalURLIsNormalized(t *testing.T) {
	var target string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", serveRSS)
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/feed#top", http.StatusFound)
	})
	srv, c := feedServer(t, mux.ServeHTTP)
	target = srv.URL
	res := doFetch(t, c, snapFor(srv.URL+"/old"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, srv.URL+"/feed", res.FinalURL)
}

// A fetch cancelled while its body is still arriving is cancelled, not a failure.
func TestCancelledDuringBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan struct{})
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssBody[:40]))
		w.(http.Flusher).Flush()
		close(sent) // the headers and part of the body are out; the rest never comes
		<-r.Context().Done()
	})
	go func() {
		<-sent
		cancel()
	}()
	res := c.Fetch(ctx, snapFor(srv.URL), t0)
	require.True(t, res.Cancelled)
	require.Empty(t, res.ErrClass)
}

// The HTTP charset decides when the document claims UTF-8 but its bytes are not.
func TestHTTPCharsetRepairsAWrongDeclaration(t *testing.T) {
	body := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?><rss version=\"2.0\"><channel><title>\xb1</title><item><guid>1</guid><title>x</title></item></channel></rss>")
	srv, c := feedServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=iso-8859-2")
		_, _ = w.Write(body)
	})
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, "ą", res.Feed.Title)
}

// A plain ASCII body labelled single-byte is not a "repair": nothing in it is multi-byte.
func TestDecodeBodyASCIIUnderASingleByteLabel(t *testing.T) {
	d := DecodeBody([]byte(`<?xml version="1.0" encoding="iso-8859-1"?><rss>abc</rss>`), "")
	require.False(t, d.Repaired)
	require.Equal(t, "windows-1252", d.Source)
}

// A stored feed address of exactly the length cap is within it: its redirect is recorded as any other.
func TestStoredFeedURLAtTheCapRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/short", serveRSS)
	srv, c := feedServer(t, mux.ServeHTTP)
	prefix := srv.URL + "/feed/"
	path := "/feed/" + strings.Repeat("a", feedurl.MaxLen-len(prefix))
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/short", http.StatusFound) })
	require.Len(t, srv.URL+path, feedurl.MaxLen)

	res := doFetch(t, c, snapFor(srv.URL+path))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Empty(t, res.Notes)
	require.Equal(t, RedirectSet, res.Redirect.Action)
}
