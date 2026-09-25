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

func TestFeedServedAsTextHTML(t *testing.T) {
	res, err := Find(context.Background(), http.DefaultTransport, "ua", serve(t, "text/html; charset=utf-8", atom))
	require.NoError(t, err)
	require.True(t, res.IsFeed)
}

func TestLargeFeedAndOverflow(t *testing.T) {
	pad := "<!-- " + strings.Repeat("x", 5<<20) + " -->"
	res, err := Find(context.Background(), http.DefaultTransport, "ua", serve(t, "application/atom+xml", strings.Replace(atom, "<title>T</title>", "<title>T</title>"+pad, 1)))
	require.NoError(t, err)
	require.True(t, res.IsFeed)

	huge := atom + strings.Repeat(" ", maxBody)
	_, err = Find(context.Background(), http.DefaultTransport, "ua", serve(t, "application/atom+xml", huge))
	require.ErrorIs(t, err, ErrTooLarge)
}
