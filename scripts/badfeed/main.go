// Command badfeed is a local feed server that misbehaves on purpose, so fetcher, retry and backoff tests
// need no internet. It listens on loopback only.
//
//	go run ./scripts/badfeed [-addr 127.0.0.1:1940] [-tls-addr 127.0.0.1:1941]
//
// -tls-addr serves the same routes over HTTPS with a throwaway self-signed certificate. Every feed it returns
// is synthetic. The routes are listed in routes() and summarised in scripts/README.md.
package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const goodFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>badfeed</title><link>https://feeds.example.test/</link><description>synthetic</description>
<item><title>one</title><link>https://feeds.example.test/1</link><guid>bf-1</guid><pubDate>Mon, 01 Jan 2024 00:00:00 +0000</pubDate><description>first</description></item>
<item><title>two</title><link>https://feeds.example.test/2</link><guid>bf-2</guid><pubDate>Tue, 02 Jan 2024 00:00:00 +0000</pubDate><description>second</description></item>
</channel></rss>
`

const feedType = "application/rss+xml"

type server struct {
	mu     sync.Mutex
	counts map[string]int // per-route request counter for the burst routes
}

func newServer() *server { return &server{counts: map[string]int{}} }

// hit returns how many times key was requested before this call.
func (s *server) hit(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.counts[key]
	s.counts[key] = n + 1
	return n
}

func qint(r *http.Request, name string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil && v >= 0 {
		return v
	}
	return def
}

func writeFeed(w http.ResponseWriter, ct string, body []byte) {
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func (s *server) routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { writeFeed(w, feedType, []byte(goodFeed)) })
	m.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.counts = map[string]int{}
		s.mu.Unlock()
		fmt.Fprint(w, "reset")
	})

	// slow-drip: ?chunk bytes per write (default 8), ?delay ms between writes (default 200), ?pad extra bytes.
	m.HandleFunc("/slow-drip", func(w http.ResponseWriter, r *http.Request) {
		body := []byte(goodFeed + strings.Repeat(" ", qint(r, "pad", 0)))
		chunk, delay := max(qint(r, "chunk", 8), 1), time.Duration(qint(r, "delay", 200))*time.Millisecond
		w.Header().Set("Content-Type", feedType)
		fl, _ := w.(http.Flusher)
		for i := 0; i < len(body); i += chunk {
			if _, err := w.Write(body[i:min(i+chunk, len(body))]); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
	})
	// slow-headers: waits ?delay ms (default 5000) before sending any header.
	m.HandleFunc("/slow-headers", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Duration(qint(r, "delay", 5000)) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// truncated: promises more bytes than it sends, then drops the connection.
	m.HandleFunc("/truncated", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", feedType)
		w.Header().Set("Content-Length", strconv.Itoa(len(goodFeed)+5000))
		w.Write([]byte(goodFeed[:len(goodFeed)/2]))
		panic(http.ErrAbortHandler)
	})
	// truncated-chunked: valid chunks, then the connection drops before the final zero chunk.
	m.HandleFunc("/truncated-chunked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", feedType)
		w.Write([]byte(goodFeed[:len(goodFeed)/2]))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	// empty: 200 with no body.
	m.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Type", feedType) })
	// reset-conn: closes the connection before sending anything.
	m.HandleFunc("/reset-conn", func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) })

	// redirect/N: N hops, then /ok. ?code picks 301/302/303/307/308.
	m.HandleFunc("/redirect/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/redirect/"))
		code := qint(r, "code", http.StatusFound)
		to := "/ok"
		if err == nil && n > 0 {
			to = fmt.Sprintf("/redirect/%d?code=%d", n-1, code)
		}
		http.Redirect(w, r, to, code)
	})
	m.HandleFunc("/loop-a", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop-b", http.StatusFound) })
	m.HandleFunc("/loop-b", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop-a", http.StatusFound) })
	m.HandleFunc("/loop-self", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop-self", http.StatusMovedPermanently)
	})
	// redirect-to?to=URL: redirect anywhere (scheme changes, private addresses, relative, empty Location).
	m.HandleFunc("/redirect-to", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", r.URL.Query().Get("to"))
		w.WriteHeader(max(qint(r, "code", http.StatusFound), 300))
	})
	m.HandleFunc("/redirect-no-location", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) })

	// 304 variants.
	// 304-always: 304 even when the client sent no validator.
	m.HandleFunc("/304-always", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) })
	// 304-etag: 304 when If-None-Match matches the ETag it sent.
	m.HandleFunc("/304-etag", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"bf-v1"`)
		if r.Header.Get("If-None-Match") == `"bf-v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 304-lastmod: 304 when If-Modified-Since matches the Last-Modified it sent.
	m.HandleFunc("/304-lastmod", func(w http.ResponseWriter, r *http.Request) {
		const lm = "Mon, 01 Jan 2024 00:00:00 GMT"
		w.Header().Set("Last-Modified", lm)
		if r.Header.Get("If-Modified-Since") == lm {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 304-weak-etag: a weak ETag, matched when the client echoes it back.
	m.HandleFunc("/304-weak-etag", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"bf-weak"`)
		if strings.Contains(r.Header.Get("If-None-Match"), `"bf-weak"`) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 304-new-etag: the 304 carries a different ETag than the client sent.
	m.HandleFunc("/304-new-etag", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("ETag", `"bf-v2"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"bf-v1"`)
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 304-ignores-validators: validators are sent but never honoured (always a full 200).
	m.HandleFunc("/304-ignores-validators", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"bf-ignored"`)
		w.Header().Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 304-bad-validators: an over-long ETag and an unparsable Last-Modified.
	m.HandleFunc("/304-bad-validators", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", strings.Repeat("x", 10000))
		w.Header().Set("Last-Modified", "yesterday-ish")
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 304-then-200: 304 for the first ?n validator-bearing requests (default 2), then changed content.
	m.HandleFunc("/304-then-200", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"bf-seq"`)
		if r.Header.Get("If-None-Match") != "" && s.hit("304-then-200") < qint(r, "n", 2) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeFeed(w, feedType, []byte(strings.Replace(goodFeed, "second", "second (changed)", 1)))
	})

	// huge: streams ?mb megabytes (default 100) of items, no Content-Length.
	m.HandleFunc("/huge", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", feedType)
		io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>huge</title>`)
		row := `<item><title>row</title><guid>g</guid><description>` + strings.Repeat("x", 900) + `</description></item>` + "\n"
		for n, limit := 0, qint(r, "mb", 100)<<20; n < limit; n += len(row) {
			if _, err := io.WriteString(w, row); err != nil {
				return
			}
		}
		io.WriteString(w, `</channel></rss>`)
	})
	// huge-header: a response header far past any sane limit (?kb, default 1024).
	m.HandleFunc("/huge-header", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Pad", strings.Repeat("h", qint(r, "kb", 1024)<<10))
		writeFeed(w, feedType, []byte(goodFeed))
	})

	// Encodings: the body bytes differ from what the headers or the declaration claim.
	m.HandleFunc("/enc/latin1", func(w http.ResponseWriter, r *http.Request) {
		body := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><rss version=\"2.0\"><channel><title>caf\xe9</title><item><title>na\xefve \xe9t\xe9</title><guid>l1</guid></item></channel></rss>"
		writeFeed(w, feedType+"; charset=iso-8859-1", []byte(body))
	})
	m.HandleFunc("/enc/header-lies", func(w http.ResponseWriter, r *http.Request) { // UTF-8 body, header says latin1
		writeFeed(w, feedType+"; charset=iso-8859-1", []byte(goodFeed))
	})
	m.HandleFunc("/enc/declaration-lies", func(w http.ResponseWriter, r *http.Request) { // latin1 bytes, declared UTF-8
		writeFeed(w, feedType, []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?><rss version=\"2.0\"><channel><title>caf\xe9</title><item><title>\xe9</title><guid>d1</guid></item></channel></rss>"))
	})
	m.HandleFunc("/enc/bogus-charset", func(w http.ResponseWriter, r *http.Request) {
		writeFeed(w, feedType+"; charset=x-no-such-charset", []byte(strings.Replace(goodFeed, "UTF-8", "x-no-such-charset", 1)))
	})
	m.HandleFunc("/enc/utf16le", func(w http.ResponseWriter, r *http.Request) {
		b := []byte{0xff, 0xfe}
		for _, c := range utf16.Encode([]rune(strings.Replace(goodFeed, "UTF-8", "UTF-16", 1))) {
			b = append(b, byte(c), byte(c>>8))
		}
		writeFeed(w, feedType, b)
	})
	m.HandleFunc("/enc/utf16be-no-bom", func(w http.ResponseWriter, r *http.Request) {
		var b []byte
		for _, c := range utf16.Encode([]rune(strings.Replace(goodFeed, "UTF-8", "UTF-16BE", 1))) {
			b = append(b, byte(c>>8), byte(c))
		}
		writeFeed(w, feedType, b)
	})
	m.HandleFunc("/enc/shiftjis", func(w http.ResponseWriter, r *http.Request) {
		body := "<?xml version=\"1.0\" encoding=\"Shift_JIS\"?><rss version=\"2.0\"><channel><title>\x93\xfa\x96\x7b</title><item><title>\x83\x65\x83\x58\x83\x67</title><guid>sj</guid></item></channel></rss>"
		writeFeed(w, feedType+"; charset=shift_jis", []byte(body))
	})
	m.HandleFunc("/enc/bom-utf8", func(w http.ResponseWriter, r *http.Request) {
		writeFeed(w, feedType, append([]byte{0xef, 0xbb, 0xbf}, goodFeed...))
	})
	m.HandleFunc("/enc/invalid-utf8", func(w http.ResponseWriter, r *http.Request) {
		writeFeed(w, feedType, []byte(strings.Replace(goodFeed, "first", "bad \xff\xfe\xc3 bytes", 1)))
	})
	m.HandleFunc("/enc/nul-bytes", func(w http.ResponseWriter, r *http.Request) {
		writeFeed(w, feedType, []byte(strings.Replace(goodFeed, "first", "nul \x00 here", 1)))
	})

	// Content-Encoding.
	m.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) { encoded(w, "gzip", goodFeed) })
	m.HandleFunc("/deflate", func(w http.ResponseWriter, r *http.Request) { encoded(w, "deflate", goodFeed) })         // zlib-wrapped, the standard form
	m.HandleFunc("/deflate-raw", func(w http.ResponseWriter, r *http.Request) { encoded(w, "deflate-raw", goodFeed) }) // raw flate labelled "deflate"
	m.HandleFunc("/gzip-corrupt", func(w http.ResponseWriter, r *http.Request) {
		b := gz(goodFeed)
		b[len(b)/2] ^= 0xff
		w.Header().Set("Content-Encoding", "gzip")
		writeFeed(w, feedType, b)
	})
	m.HandleFunc("/gzip-truncated", func(w http.ResponseWriter, r *http.Request) {
		b := gz(goodFeed)
		w.Header().Set("Content-Encoding", "gzip")
		writeFeed(w, feedType, b[:len(b)-12])
	})
	m.HandleFunc("/gzip-lies", func(w http.ResponseWriter, r *http.Request) { // header says gzip, body is plain
		w.Header().Set("Content-Encoding", "gzip")
		writeFeed(w, feedType, []byte(goodFeed))
	})
	m.HandleFunc("/gzip-double", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip, gzip")
		writeFeed(w, feedType, gz(string(gz(goodFeed))))
	})
	m.HandleFunc("/gzip-bomb", func(w http.ResponseWriter, r *http.Request) { // ?mb (default 512) of zeros in a tiny gzip
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		chunk := make([]byte, 1<<20)
		for i := 0; i < qint(r, "mb", 512); i++ {
			zw.Write(chunk)
		}
		zw.Close()
		w.Header().Set("Content-Encoding", "gzip")
		writeFeed(w, feedType, buf.Bytes())
	})
	m.HandleFunc("/unknown-encoding", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "x-no-such")
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// chunked: many small chunks of the good feed.
	m.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", feedType)
		fl := w.(http.Flusher)
		for i := 0; i < len(goodFeed); i += 16 {
			io.WriteString(w, goodFeed[i:min(i+16, len(goodFeed))])
			fl.Flush()
		}
	})

	// Content-Type games.
	m.HandleFunc("/type/html", func(w http.ResponseWriter, r *http.Request) { writeFeed(w, "text/html", []byte(goodFeed)) })
	m.HandleFunc("/type/none", func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		w.Write([]byte(goodFeed))
	})
	m.HandleFunc("/type/json", func(w http.ResponseWriter, r *http.Request) { writeFeed(w, "application/json", []byte(goodFeed)) })
	m.HandleFunc("/type/image", func(w http.ResponseWriter, r *http.Request) { writeFeed(w, "image/png", []byte(goodFeed)) })
	m.HandleFunc("/html-page", func(w http.ResponseWriter, r *http.Request) {
		writeFeed(w, "text/html", []byte(`<!doctype html><html><head><link rel="alternate" type="application/rss+xml" href="/ok"></head><body>not a feed</body></html>`))
	})

	// status/N answers N (200 to 599) with a tiny body.
	m.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/status/"))
		if err != nil || code < 200 || code > 599 {
			code = http.StatusBadRequest
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(code)
		if code != http.StatusNoContent && code != http.StatusNotModified {
			fmt.Fprintf(w, "status %d", code)
		}
	})
	// 5xx-burst: the first ?n requests (default 3) get ?code (default 503), then the feed. /reset starts over.
	m.HandleFunc("/5xx-burst", func(w http.ResponseWriter, r *http.Request) {
		if s.hit("5xx-burst") < qint(r, "n", 3) {
			http.Error(w, "burst", max(qint(r, "code", http.StatusServiceUnavailable), 500))
			return
		}
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 5xx-flap: alternates 503 and the feed.
	m.HandleFunc("/5xx-flap", func(w http.ResponseWriter, r *http.Request) {
		if s.hit("5xx-flap")%2 == 0 {
			http.Error(w, "flap", http.StatusServiceUnavailable)
			return
		}
		writeFeed(w, feedType, []byte(goodFeed))
	})
	// 429: Retry-After in seconds (?after, default 30). 429-date sends an HTTP-date, 429-bad sends ?v verbatim.
	m.HandleFunc("/429", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", strconv.Itoa(qint(r, "after", 30)))
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	m.HandleFunc("/429-date", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", time.Now().Add(time.Duration(qint(r, "after", 30))*time.Second).UTC().Format(http.TimeFormat))
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	m.HandleFunc("/429-bad", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", r.URL.Query().Get("v")) // for example -1, 99999999999, soon, or empty
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	// 503-retry-after: a 503 with Retry-After seconds (?after, default 60).
	m.HandleFunc("/503-retry-after", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", strconv.Itoa(qint(r, "after", 60)))
		http.Error(w, "later", http.StatusServiceUnavailable)
	})
	return m
}

func gz(s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write([]byte(s))
	zw.Close()
	return b.Bytes()
}

func encoded(w http.ResponseWriter, kind, s string) {
	var b bytes.Buffer
	switch kind {
	case "gzip":
		b.Write(gz(s))
	case "deflate":
		zw := zlib.NewWriter(&b)
		zw.Write([]byte(s))
		zw.Close()
	case "deflate-raw":
		zw, _ := flate.NewWriter(&b, flate.DefaultCompression)
		zw.Write([]byte(s))
		zw.Close()
		kind = "deflate"
	}
	w.Header().Set("Content-Encoding", kind)
	writeFeed(w, feedType, b.Bytes())
}

// loopback reports whether addr (host:port) names a loopback IP literal. Names, including localhost, are refused.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// selfSigned returns a throwaway certificate for 127.0.0.1, valid for a day, never written to disk.
func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "badfeed.invalid"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:1940", "HTTP listen address (loopback IP only)")
	tlsAddr := flag.String("tls-addr", "", "optional HTTPS listen address with a self-signed certificate (loopback IP only)")
	flag.Parse()
	for _, a := range []string{*addr, *tlsAddr} {
		if a != "" && !loopback(a) {
			fmt.Fprintf(os.Stderr, "badfeed: %q is not a loopback IP address\n", a)
			os.Exit(2)
		}
	}
	h := newServer().routes()
	errc := make(chan error, 2)
	go func() {
		errc <- (&http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
	}()
	fmt.Printf("badfeed on http://%s\n", *addr)
	if *tlsAddr != "" {
		cert, err := selfSigned()
		if err != nil {
			fmt.Fprintln(os.Stderr, "badfeed:", err)
			os.Exit(1)
		}
		srv := &http.Server{Addr: *tlsAddr, Handler: h, ReadHeaderTimeout: 10 * time.Second, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
		go func() { errc <- srv.ListenAndServeTLS("", "") }()
		fmt.Printf("badfeed on https://%s (self-signed)\n", *tlsAddr)
	}
	fmt.Fprintln(os.Stderr, "badfeed:", <-errc)
	os.Exit(1)
}
