package extract

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func newExtractor(tune ...func(*Options)) *Extractor {
	fc := fetch.NewClient(fetch.ClientOptions{})
	opt := Options{Transport: fc.Transport}
	for _, f := range tune {
		f(&opt)
	}
	return New(opt)
}

const para = "The quick brown fox jumps over the lazy dog while the reader keeps going through a long and detailed paragraph of article text. "

func article(body string) string {
	return `<!DOCTYPE html><html><head><title>Story</title><meta charset="utf-8"></head><body>
<nav><a href="/home">Home</a> <a href="/about">About</a></nav>
<article><h1>Story</h1>` + body + `</article>
<footer>Copyright junk</footer><script>alert('x')</script></body></html>`
}

func longBody() string {
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString("<p>" + strings.Repeat(para, 3) + "</p>")
	}
	return b.String()
}

func page(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestExtractAbsolutizesAndSanitizes(t *testing.T) {
	srv := page(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(article(`<p><img src="/img/one.png" onerror="alert(1)"> <a href="more/page.html">more</a> <a href="javascript:alert(1)">bad</a></p>` +
			`<script>evil()</script><iframe src="http://evil.example/"></iframe>` + longBody())))
	})
	res, err := newExtractor().Extract(context.Background(), Target{URL: srv.URL + "/posts/story.html", AllowPrivate: true})
	require.NoError(t, err)
	require.Contains(t, res.HTML, `src="`+srv.URL+`/img/one.png"`)
	require.Contains(t, res.HTML, `href="`+srv.URL+`/posts/more/page.html"`)
	require.NotContains(t, res.HTML, "javascript:")
	require.NotContains(t, res.HTML, "onerror")
	require.NotContains(t, res.HTML, "<script")
	require.NotContains(t, res.HTML, "<iframe")
	require.NotContains(t, res.HTML, "Copyright junk")
	require.Contains(t, res.Text, "quick brown fox")
	require.Greater(t, res.WordCount, 200)
	require.Equal(t, srv.URL+"/img/one.png", res.ImageURL)
	require.Equal(t, srv.URL+"/posts/story.html", res.SourceURL)
}

func TestExtractResolvesAgainstFinalURLAfterRedirect(t *testing.T) {
	srv := page(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/short" {
			http.Redirect(w, r, "/deep/er/article.html", http.StatusMovedPermanently)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(article(`<p><img src="pic.jpg"></p>` + longBody())))
	})
	res, err := newExtractor().Extract(context.Background(), Target{URL: srv.URL + "/short", AllowPrivate: true})
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/deep/er/article.html", res.SourceURL)
	// Extraction (readability) resolves against the final page; the pipeline keeps it absolute.
	require.Contains(t, res.HTML, srv.URL+"/deep/er/pic.jpg")
}

func TestExtractCharsetDecoded(t *testing.T) {
	srv := page(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1252")
		body := strings.Replace(article(longBody()+"<p>caf\xe9 cr\xe8me br\xfbl\xe9e "+strings.Repeat(para, 2)+"</p>"), `<meta charset="utf-8">`, "", 1)
		_, _ = w.Write([]byte(body))
	})
	res, err := newExtractor().Extract(context.Background(), Target{URL: srv.URL, AllowPrivate: true})
	require.NoError(t, err)
	require.Contains(t, res.Text, "café crème brûlée")
}

func TestExtractFailures(t *testing.T) {
	ex := newExtractor(func(o *Options) { o.MaxBody = 1 << 20 })
	cases := map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"404": {func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "HTTP 404"},
		"500": {func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, "HTTP 500"},
		"pdf": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF"))
		}, "not HTML"},
		"empty page": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body></body></html>"))
		}, "no readable content"},
		"too large": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body><p>" + strings.Repeat("x", 2<<20) + "</p></body></html>"))
		}, "larger than"},
	}
	for name, c := range cases {
		srv := page(t, c.h)
		_, err := ex.Extract(context.Background(), Target{URL: srv.URL, AllowPrivate: true})
		var ee *Error
		require.ErrorAs(t, err, &ee, name)
		require.Contains(t, ee.Msg, c.want, name)
	}
}

func TestExtractRefusesBadURLsAndPrivateNets(t *testing.T) {
	var hits atomic.Int32
	srv := page(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1) })
	ex := newExtractor()
	for _, u := range []string{"", "ftp://example.com/a", "file:///etc/passwd", "javascript:alert(1)", "/relative", "http://"} {
		_, err := ex.Extract(context.Background(), Target{URL: u, AllowPrivate: true})
		require.Error(t, err, u)
	}
	// Loopback, metadata and RFC 1918 are refused at dial time unless the feed allows private nets.
	for _, u := range []string{srv.URL, "http://169.254.169.254/latest", "http://10.1.2.3/", "http://[::1]:9/"} {
		_, err := ex.Extract(context.Background(), Target{URL: u})
		require.ErrorContains(t, err, "not allowed", u)
	}
	require.Zero(t, hits.Load())
}

func TestExtractTimeout(t *testing.T) {
	srv := page(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) })
	ex := newExtractor(func(o *Options) { o.Timeout = 200 * time.Millisecond })
	start := time.Now()
	_, err := ex.Extract(context.Background(), Target{URL: srv.URL, AllowPrivate: true})
	require.Error(t, err)
	require.Less(t, time.Since(start), time.Second)
}

func TestExtractUserAgent(t *testing.T) {
	var ua atomic.Value
	srv := page(t, func(w http.ResponseWriter, r *http.Request) { ua.Store(r.Header.Get("User-Agent")); w.WriteHeader(404) })
	ex := newExtractor(func(o *Options) { o.UserAgent = "Default/1" })
	_, _ = ex.Extract(context.Background(), Target{URL: srv.URL, AllowPrivate: true})
	require.Equal(t, "Default/1", ua.Load())
	_, _ = ex.Extract(context.Background(), Target{URL: srv.URL, AllowPrivate: true, UserAgent: "Feed/2"})
	require.Equal(t, "Feed/2", ua.Load())
}

func TestFailuresAreClassifiedTransientOrPermanent(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status.Load() == 200 {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body><p>hi</p></body></html>")) // too little to read
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(srv.Close)
	ex := newExtractor(func(o *Options) { o.Transport = fetch.NewClient(fetch.ClientOptions{}).Transport })
	cases := map[int32]bool{500: true, 502: true, 503: true, 429: true, 404: false, 410: false, 403: false, 401: false}
	for code, transient := range cases {
		status.Store(code)
		_, err := ex.Extract(context.Background(), Target{URL: srv.URL + "/a", AllowPrivate: true})
		var ee *Error
		require.ErrorAs(t, err, &ee, code)
		require.Equal(t, transient, ee.Transient, "HTTP %d", code)
	}
	// A refused connection is transient.
	srv.Close()
	_, err := ex.Extract(context.Background(), Target{URL: srv.URL + "/a", AllowPrivate: true})
	var ee *Error
	require.ErrorAs(t, err, &ee)
	require.True(t, ee.Transient)
}

func TestTransportFailuresAreClassedLikeFetch(t *testing.T) {
	// The SSRF guard refuses this every time.
	_, err := newExtractor().Extract(context.Background(), Target{URL: "http://10.1.2.3/"})
	var ee *Error
	require.ErrorAs(t, err, &ee)
	require.False(t, ee.Transient, "blocked address")

	// A certificate that does not verify (httptest's is self-signed).
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(tlsSrv.Close)
	_, err = newExtractor().Extract(context.Background(), Target{URL: tlsSrv.URL, AllowPrivate: true})
	require.ErrorAs(t, err, &ee)
	require.False(t, ee.Transient, "untrusted certificate: %s", ee.Msg)

	// A redirect loop.
	var loop *httptest.Server
	loop = page(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, loop.URL+"/again", http.StatusFound) })
	_, err = newExtractor().Extract(context.Background(), Target{URL: loop.URL, AllowPrivate: true})
	require.ErrorAs(t, err, &ee)
	require.False(t, ee.Transient, "redirect loop: %s", ee.Msg)

	// HTTP 408 is transient, like a timeout.
	slow := page(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusRequestTimeout) })
	_, err = newExtractor().Extract(context.Background(), Target{URL: slow.URL, AllowPrivate: true})
	require.ErrorAs(t, err, &ee)
	require.True(t, ee.Transient, "HTTP 408")
}

func TestTransientTransportDNS(t *testing.T) {
	require.False(t, transientTransport(&net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}))
	require.True(t, transientTransport(&net.DNSError{Err: "server misbehaving", Name: "x.example", IsTemporary: true}))
	require.True(t, transientTransport(&net.DNSError{Err: "i/o timeout", Name: "x.example", IsTimeout: true}))
	require.True(t, transientTransport(context.DeadlineExceeded))
	require.True(t, transientTransport(io.ErrUnexpectedEOF))
}
