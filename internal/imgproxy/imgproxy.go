// Package imgproxy is the signed, streaming image proxy (design §7.4).
//
// Reader-facing HTML is rewritten at serve time so an image loads through
// /img/{sig}/{flags}/{b64url}: the browser never contacts the source (no
// tracking pixel, no mixed content). The URL is bound to the account secret by
// an HMAC that also covers the flags, so the proxy cannot be used as an open
// fetcher. The route itself is mounted behind the web session by internal/api.
package imgproxy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Flag bits, taken from the item's feed when the URL is signed.
const (
	FlagPrivateNet  = 1 << 0 // feeds.allow_private_net
	FlagInsecureTLS = 1 << 1 // feeds.allow_insecure_tls
	maxFlags        = FlagPrivateNet | FlagInsecureTLS
)

const (
	sigLen         = 22 // base64url chars of the HMAC kept
	maxURLLen      = 4096
	sniffLen       = 512
	defaultMax     = 15 << 20
	defaultTimeout = 15 * time.Second
	defaultWait    = 10 * time.Second
	defaultConc    = 8
	maxHops        = 5
	cacheControl   = "private, max-age=2592000, immutable"
)

// Sign returns the signature of (flags, originalURL): the first 22 base64url
// characters of HMAC-SHA256(secret, "img-v1|<flags>|<url>").
func Sign(secret []byte, flags int, orig string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("img-v1|" + strconv.Itoa(flags) + "|" + orig))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:sigLen]
}

// Path returns the proxy path for orig.
func Path(secret []byte, flags int, orig string) string {
	return "/img/" + Sign(secret, flags, orig) + "/" + strconv.Itoa(flags) + "/" + base64.RawURLEncoding.EncodeToString([]byte(orig))
}

// Rewriter turns source image URLs into proxy paths for one feed.
type Rewriter struct {
	Secret []byte
	Flags  int
	// All proxies https sources too; the default (http_only) proxies only
	// http:// sources, which a browser would block as mixed content.
	All bool
}

// Rewrite returns the proxy path for u, or u unchanged when the mode leaves it
// alone (https in http_only mode) or it is not an absolute http(s) URL.
func (r Rewriter) Rewrite(u string) string {
	u = strings.TrimSpace(u)
	l := strings.ToLower(u)
	switch {
	case strings.HasPrefix(l, "http://"):
	case strings.HasPrefix(l, "https://") && r.All:
	default:
		return u
	}
	if len(u) > maxURLLen {
		return u
	}
	return Path(r.Secret, r.Flags, u)
}

// Options configures New. Zero values take the design's numbers.
type Options struct {
	Secret []byte
	// Transport returns the SSRF-guarded transport for a flags variant
	// (fetch.Client.Transport).
	Transport func(allowPrivate, insecureTLS bool) http.RoundTripper
	UserAgent string
	Logger    *slog.Logger

	MaxBytes    int64         // 15 MiB
	Timeout     time.Duration // 15 s, the whole upstream exchange
	Concurrency int           // 8 simultaneous upstream fetches
	Wait        time.Duration // 10 s queueing for a slot
}

// Handler serves GET /img/{sig}/{flags}/{u} (path values). The caller enforces
// the web session before it gets here.
type Handler struct {
	opt Options
	sem chan struct{}
	log *slog.Logger
}

// New builds the handler.
func New(opt Options) *Handler {
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = defaultMax
	}
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = defaultConc
	}
	if opt.Wait <= 0 {
		opt.Wait = defaultWait
	}
	if opt.UserAgent == "" {
		opt.UserAgent = "Mozilla/5.0 (compatible; Kipple)"
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handler{opt: opt, sem: make(chan struct{}, opt.Concurrency), log: log}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sig, rawFlags, enc := r.PathValue("sig"), r.PathValue("flags"), r.PathValue("u")
	flags, err := strconv.Atoi(rawFlags)
	if err != nil || flags < 0 || flags > maxFlags || strconv.Itoa(flags) != rawFlags {
		fail(w, http.StatusBadRequest)
		return
	}
	if len(enc) > base64.RawURLEncoding.EncodedLen(maxURLLen) {
		fail(w, http.StatusBadRequest)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		fail(w, http.StatusBadRequest)
		return
	}
	orig := string(raw)
	if !hmac.Equal([]byte(sig), []byte(Sign(h.opt.Secret, flags, orig))) {
		fail(w, http.StatusForbidden)
		return
	}
	u, err := url.Parse(orig)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		fail(w, http.StatusBadRequest)
		return
	}

	// At most Concurrency upstream fetches; the rest queue for up to Wait.
	wait := time.NewTimer(h.opt.Wait)
	defer wait.Stop()
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-wait.C:
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable)
		return
	case <-r.Context().Done():
		return
	}
	h.fetch(w, r, u, flags)
}

func fail(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
}

func (h *Handler) fetch(w http.ResponseWriter, r *http.Request, u *url.URL, flags int) {
	// #nosec G704 -- URL is HMAC-signed by us; the transport dial guard blocks private ranges
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		fail(w, http.StatusBadRequest)
		return
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", h.opt.UserAgent)
	if v := r.Header.Get("If-None-Match"); v != "" {
		req.Header.Set("If-None-Match", v)
	}
	if v := r.Header.Get("If-Modified-Since"); v != "" {
		req.Header.Set("If-Modified-Since", v)
	}
	client := &http.Client{
		Transport: h.opt.Transport(flags&FlagPrivateNet != 0, flags&FlagInsecureTLS != 0),
		Timeout:   h.opt.Timeout, // covers reading the body too
		// No cookie jar. Go adds a Referer on redirects; strip it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return errors.New("imgproxy: too many redirects")
			}
			req.Header.Del("Referer")
			return nil
		},
	}
	// #nosec G704 -- same as above: signed URL, SSRF-guarded transport, capped redirects
	resp, err := client.Do(req)
	if err != nil {
		h.log.Debug("imgproxy: upstream", "host", u.Host, "err", err)
		fail(w, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	hdr := w.Header()
	if resp.StatusCode == http.StatusNotModified {
		passValidators(hdr, resp)
		hdr.Set("Cache-Control", cacheControl)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength > h.opt.MaxBytes {
		fail(w, http.StatusBadGateway) // over the cap: nothing was written
		return
	}

	body := http.MaxBytesReader(nil, resp.Body, h.opt.MaxBytes)
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadGateway)
		return
	}
	head = head[:n]
	if n == 0 {
		fail(w, http.StatusBadGateway)
		return
	}
	ct, ok := detectType(head, resp.Header.Get("Content-Type"))
	if !ok {
		fail(w, http.StatusUnsupportedMediaType)
		return
	}

	passValidators(hdr, resp)
	hdr.Set("Content-Type", ct)
	hdr.Set("Cache-Control", cacheControl)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", "default-src 'none'")
	if resp.ContentLength >= 0 {
		hdr.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(head); err != nil {
		return
	}
	buf := make([]byte, 32<<10)
	for {
		m, rerr := body.Read(buf)
		if m > 0 {
			if _, werr := w.Write(buf[:m]); werr != nil {
				return // the browser went away
			}
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			// Over the cap mid-stream, or upstream died: the status is already
			// sent, so cut the connection and the browser shows a broken image.
			panic(http.ErrAbortHandler)
		}
	}
}

func passValidators(hdr http.Header, resp *http.Response) {
	if v := resp.Header.Get("ETag"); v != "" {
		hdr.Set("ETag", v)
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		hdr.Set("Last-Modified", v)
	}
}

var allowedTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true, "image/avif": true,
}

// detectType decides the type to serve. http.DetectContentType covers jpeg,
// png, gif and webp; AVIF is an ISO-BMFF ftyp box. Only when sniffing is
// inconclusive (plain text or opaque bytes) is the upstream Content-Type
// trusted, and then only if it is on the allowed list. SVG is never allowed,
// and anything the sniffer positively identifies as another type is refused.
func detectType(head []byte, upstream string) (string, bool) {
	if isAVIF(head) {
		return "image/avif", true
	}
	sniffed := http.DetectContentType(head)
	if allowedTypes[sniffed] {
		return sniffed, true
	}
	switch sniffed {
	case "application/octet-stream", "text/plain; charset=utf-8":
		if t := bytes.TrimSpace(head); len(t) > 0 && t[0] == '<' {
			return "", false // markup (SVG, HTML) posing as an image
		}
		mt, _, err := mime.ParseMediaType(upstream)
		if err == nil && allowedTypes[mt] {
			return mt, true
		}
	}
	return "", false
}

// isAVIF reports an ISO-BMFF file-type box whose major or compatible brand is
// avif or avis.
func isAVIF(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	size := int(binary.BigEndian.Uint32(b[0:4]))
	if size < 16 || size > len(b) {
		size = len(b)
	}
	brand := func(p []byte) bool { return string(p) == "avif" || string(p) == "avis" }
	if brand(b[8:12]) {
		return true
	}
	for off := 16; off+4 <= size; off += 4 {
		if brand(b[off : off+4]) {
			return true
		}
	}
	return false
}
