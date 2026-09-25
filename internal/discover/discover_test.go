package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	return Find(context.Background(), http.DefaultTransport, "ua", serve(t, ct, body))
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
