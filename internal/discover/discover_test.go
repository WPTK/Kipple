package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const atom = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>T</title><id>x</id><updated>2026-01-01T00:00:00Z</updated>` +
	`<entry><title>a</title><id>1</id><updated>2026-01-01T00:00:00Z</updated></entry></feed>`

func serve(t *testing.T, ct, body string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func find(t *testing.T, ct, body string) (Result, error) {
	t.Helper()
	return Find(context.Background(), http.DefaultTransport, "ua", "", serve(t, ct, body), false)
}

// sized returns a valid feed padded (inside a comment) to exactly n bytes.
func sized(n int) string {
	const open, closer = "<!--", "-->"
	pad := n - len(atom) - len(open) - len(closer)
	return strings.Replace(atom, "<title>T</title>", "<title>T</title>"+open+strings.Repeat("x", pad)+closer, 1)
}

func TestFeedServedAsTextHTML(t *testing.T) {
	res, err := find(t, "text/html; charset=utf-8", atom)
	require.NoError(t, err)
	require.True(t, res.IsFeed)
}

// The label is ignored, but a real page is still recognised as a page whatever
// it is called, and its advertised feeds are still found.
func TestHTMLPageStillListsCandidatesWhateverItsContentType(t *testing.T) {
	page := `<!doctype html><html><head><link rel="alternate" type="application/atom+xml" href="https://blog.example.com/feed.xml"></head><body>hi</body></html>`
	for _, ct := range []string{"text/html", "text/plain", "application/octet-stream"} {
		res, err := find(t, ct, page)
		require.NoError(t, err, ct)
		require.False(t, res.IsFeed, ct)
		require.NotEmpty(t, res.Candidates, ct)
	}
}

// Each way an address fails to be a feed has its own error, so the add dialog can say which.
func TestNotAFeedErrors(t *testing.T) {
	_, err := find(t, "text/html", `<!doctype html><html><head><title>x</title></head><body>no feed</body></html>`)
	require.ErrorIs(t, err, ErrNoFeed)
	_, err = find(t, "text/plain", "just some text")
	require.ErrorIs(t, err, ErrNotFeed)
	_, err = find(t, "image/png", "\x89PNG\r\n\x1a\n")
	require.ErrorIs(t, err, ErrNotFeed)

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	_, err = Find(context.Background(), http.DefaultTransport, "ua", "", srv.URL+"/x", false)
	var se *StatusError
	require.ErrorAs(t, err, &se)
	require.Equal(t, 404, se.Code)
	require.ErrorContains(t, err, "HTTP 404 Not Found")
}

// A page with several feeds lists them all, in the page's order, for the dialog to offer.
func TestSeveralFeedsInPageOrder(t *testing.T) {
	page := `<!doctype html><html><head>
<link rel="alternate" type="application/rss+xml" title="Posts" href="https://blog.example.com/feed/">
<link rel="alternate" type="application/rss+xml" title="Comments" href="https://blog.example.com/comments/feed/">
<link rel="alternate" type="application/atom+xml" title="Posts (Atom)" href="https://blog.example.com/atom.xml">
</head><body></body></html>`
	res, err := find(t, "text/html", page)
	require.NoError(t, err)
	require.Len(t, res.Candidates, 3)
	require.Equal(t, "Posts", res.Candidates[0].Title)
	require.Equal(t, "Comments", res.Candidates[1].Title)
	require.Equal(t, "https://blog.example.com/atom.xml", res.Candidates[2].URL)
}

func TestFeedBeyondOldTwoMiBLimit(t *testing.T) {
	res, err := find(t, "application/atom+xml", sized(5<<20))
	require.NoError(t, err)
	require.True(t, res.IsFeed)
}

func TestFeedExactlyAtLimitIsAccepted(t *testing.T) {
	res, err := find(t, "application/atom+xml", sized(maxBody))
	require.NoError(t, err)
	require.True(t, res.IsFeed)
}

func TestOneByteOverLimitIsTooLarge(t *testing.T) {
	_, err := find(t, "application/atom+xml", sized(maxBody+1))
	require.ErrorIs(t, err, ErrTooLarge)
	_, err = find(t, "text/html", sized(maxBody+1000))
	require.ErrorIs(t, err, ErrTooLarge, "the label does not matter")
}

func TestRetriesOnceWithTheRetryUserAgent(t *testing.T) {
	var uas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas = append(uas, r.UserAgent())
		switch {
		case r.UserAgent() == "browser":
			w.Header().Set("Content-Type", "application/atom+xml")
			_, _ = w.Write([]byte(atom))
		case r.URL.Path == "/cf":
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/406":
			w.WriteHeader(http.StatusNotAcceptable)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	for _, path := range []string{"/403", "/406", "/cf"} {
		uas = nil
		res, err := Find(context.Background(), http.DefaultTransport, "kipple", "browser", srv.URL+path, false)
		require.NoError(t, err, path)
		require.True(t, res.IsFeed, path)
		require.Equal(t, []string{"kipple", "browser"}, uas, path)
	}
	uas = nil
	_, err := Find(context.Background(), http.DefaultTransport, "kipple", "", srv.URL+"/403", false)
	require.ErrorContains(t, err, "HTTP 403")
	require.Len(t, uas, 1, "no retry UA, no retry")
}

// A source that stalls, before the headers or in the middle of the body, ends
// Find at its own deadline even when the caller set none.
func TestFindEndsAtItsOwnDeadline(t *testing.T) {
	old := findTimeout
	findTimeout = 300 * time.Millisecond
	t.Cleanup(func() { findTimeout = old })
	for name, serve := range map[string]http.HandlerFunc{
		"headers never come": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(30 * time.Second):
			}
		},
		"body trickles": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			for {
				if _, err := w.Write([]byte(" ")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(serve)
			defer srv.Close()
			begin := time.Now()
			_, err := Find(context.Background(), http.DefaultTransport, "ua", "", srv.URL, false)
			require.Error(t, err)
			require.Less(t, time.Since(begin), 5*findTimeout)
		})
	}
}

func TestAPageInACodingNobodyAskedForIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<link rel="alternate" type="application/atom+xml" href="/f">`))
	}))
	t.Cleanup(srv.Close)
	_, err := Find(context.Background(), http.DefaultTransport, "ua", "", srv.URL, false)
	require.ErrorIs(t, err, ErrUnaskedCoding)
}
