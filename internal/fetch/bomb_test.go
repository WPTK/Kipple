package fetch_test

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/discover"
	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/favicon"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/imgproxy"
)

// Every client caps what a body decodes to, not what it weighs on the wire: a
// few kilobytes of gzip that inflate to far more than the limit are refused.

const inflatedBytes = 96 << 20

// bombServer answers every request with inflatedBytes of zeros under gzip, and
// reports the bytes of it that were actually written to the connection.
func bombServer(t *testing.T, contentType string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		chunk := make([]byte, 64<<10)
		for sent := 0; sent < inflatedBytes; sent += len(chunk) {
			if _, err := zw.Write(chunk); err != nil {
				return
			}
		}
		_ = zw.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFeedFetchRefusesAGzipBomb(t *testing.T) {
	srv := bombServer(t, "application/rss+xml")
	c := fetch.NewClient(fetch.ClientOptions{})
	res := c.Fetch(context.Background(), fetch.Snapshot{ID: 1, URL: srv.URL + "/f", AllowPrivateNet: true, IntervalS: 1800, DedupMode: fetch.DedupAuto, HonorTTL: true}, time.Now())
	require.Equal(t, fetch.OutcomeError, res.Outcome)
	require.Equal(t, fetch.ClassTooLarge, res.ErrClass)
}

func TestDiscoveryRefusesAGzipBomb(t *testing.T) {
	srv := bombServer(t, "text/html")
	c := fetch.NewClient(fetch.ClientOptions{})
	_, err := discover.Find(context.Background(), c.Transport(true, false, false), "ua", "", srv.URL, true)
	require.ErrorIs(t, err, discover.ErrTooLarge)
}

func TestExtractionRefusesAGzipBomb(t *testing.T) {
	srv := bombServer(t, "text/html")
	c := fetch.NewClient(fetch.ClientOptions{})
	_, err := extract.New(extract.Options{Transport: c.Transport}).Extract(context.Background(), extract.Target{URL: srv.URL, AllowPrivate: true, FeedHost: "127.0.0.1"})
	var ee *extract.Error
	require.ErrorAs(t, err, &ee)
	require.Contains(t, ee.Msg, "larger than")
}

func TestIconLookupRefusesAGzipBomb(t *testing.T) {
	srv := bombServer(t, "image/png")
	c := fetch.NewClient(fetch.ClientOptions{})
	_, err := favicon.Lookup(context.Background(), favicon.Request{SiteURL: srv.URL, Transport: c.Transport(true, false, false), UserAgent: "ua"})
	require.Error(t, err)
}

func TestImageProxyRefusesAGzipBomb(t *testing.T) {
	srv := bombServer(t, "image/png")
	c := fetch.NewClient(fetch.ClientOptions{})
	h := imgproxy.New(imgproxy.Options{Secret: bombSecret, Transport: func(allowPrivate, insecure bool) http.RoundTripper {
		return c.Transport(allowPrivate, insecure, false)
	}})
	defer h.Close()
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", h)
	front := httptest.NewServer(mux)
	defer front.Close()
	resp, err := http.Get(front.URL + imgproxy.Path(bombSecret, imgproxy.FlagPrivateNet, srv.URL+"/b.png"))
	require.NoError(t, err)
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	require.LessOrEqual(t, n, int64(15<<20), "never more than the image limit")
	require.True(t, err != nil || resp.StatusCode != http.StatusOK, "the transfer is cut off or refused")
}

var bombSecret = []byte("bomb-secret-0123456789abcdefghijk")
