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
	"context"
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

	"github.com/WPTK/kipple/internal/imgcache"
)

// Flag bits, taken from the item's feed when the URL is signed.
const (
	FlagPrivateNet  = 1 << 0 // feeds.allow_private_net
	FlagInsecureTLS = 1 << 1 // feeds.allow_insecure_tls
	// FlagThumb asks for the list-card thumbnail of the image. It is part of the
	// signed flags, so a thumbnail URL cannot be turned into an original (or the
	// reverse) without the secret, and URLs signed before it existed (flags 0
	// to 3) verify exactly as before.
	FlagThumb = 1 << 2
	maxFlags  = FlagPrivateNet | FlagInsecureTLS | FlagThumb
)

const (
	sigLen            = 22 // base64url chars of the HMAC kept
	maxURLLen         = 4096
	sniffLen          = 512
	defaultMax        = 15 << 20
	defaultTimeout    = 15 * time.Second
	defaultWait       = 10 * time.Second
	defaultThumbWait  = 4 * time.Second
	defaultRevalidate = 3 * time.Second
	defaultConc       = 8
	defaultPerHost    = 4
	maxHops           = 5
	cacheControl      = "private, max-age=2592000, immutable"
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
	// Thumb points at the card thumbnail instead of the original.
	Thumb bool
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
	flags := r.Flags
	if r.Thumb {
		flags |= FlagThumb
	}
	return Path(r.Secret, flags, u)
}

// Options configures New. Zero values take the design's numbers.
type Options struct {
	Secret []byte
	// Transport returns the SSRF-guarded transport for a flags variant
	// (fetch.Client.Transport).
	Transport func(allowPrivate, insecureTLS bool) http.RoundTripper
	UserAgent string
	// BrowserUA is the plain browser User-Agent of the hotlink retries; it never
	// names Kipple. The default is a current desktop Chrome string.
	BrowserUA string
	Logger    *slog.Logger

	// Cache is the on-disk cache under the proxy. Nil, or a cache with a cap of
	// zero, streams every image straight from the source as before.
	Cache *imgcache.Cache

	MaxBytes    int64         // 15 MiB
	Timeout     time.Duration // 15 s, the whole upstream exchange
	Concurrency int           // 8 simultaneous upstream fetches
	PerHost     int           // 4 of them to one host
	Wait        time.Duration // 10 s queueing for a slot
	// RevalidateWithin bounds how long a stale cached image may wait for its
	// source to answer a conditional request before the stale copy is served (3 s).
	RevalidateWithin time.Duration

	// Thumbnails (FlagThumb). Zero values take the defaults.
	ThumbWidth     int           // 800 px
	ThumbWorkers   int           // 2 transcoder goroutines
	ThumbQueue     int           // 16 waiting jobs; beyond that the original is served
	ThumbMaxPixels int           // 24 megapixels; more is served as the original
	ThumbWait      time.Duration // 4 s a request waits for a thumbnail before the original is served
	DecodeCeiling  int64         // 96 MiB: the most one decode may be estimated to need
	DecodeBudget   int64         // 128 MiB: the most all decodes together may be estimated to need
}

// Handler serves GET /img/{sig}/{flags}/{u} (path values). The caller enforces
// the web session before it gets here.
type Handler struct {
	opt   Options
	sem   chan struct{}
	hosts *hostLimiter
	hints hintStore
	log   *slog.Logger

	pool *pool
	lim  thumbLimits
}

// Close stops the thumbnail workers after the queued jobs finish. The handler
// keeps serving (originals) afterwards.
func (h *Handler) Close() { h.pool.close() }

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
	if opt.PerHost <= 0 {
		opt.PerHost = defaultPerHost
	}
	if opt.Wait <= 0 {
		opt.Wait = defaultWait
	}
	if opt.RevalidateWithin <= 0 {
		opt.RevalidateWithin = defaultRevalidate
	}
	if opt.UserAgent == "" {
		opt.UserAgent = "Mozilla/5.0 (compatible; Kipple)"
	}
	if opt.BrowserUA == "" {
		opt.BrowserUA = defaultBrowserUA
	}
	if opt.ThumbWidth <= 0 {
		opt.ThumbWidth = ThumbWidth
	}
	if opt.ThumbWorkers <= 0 {
		opt.ThumbWorkers = defaultThumbWorkers
	}
	if opt.ThumbQueue <= 0 {
		opt.ThumbQueue = defaultThumbQueue
	}
	if opt.ThumbMaxPixels <= 0 {
		opt.ThumbMaxPixels = defaultThumbPixels
	}
	if opt.ThumbWait <= 0 {
		opt.ThumbWait = defaultThumbWait
	}
	if opt.DecodeBudget <= 0 {
		opt.DecodeBudget = defaultDecodeBudget
	}
	if opt.DecodeCeiling <= 0 {
		opt.DecodeCeiling = defaultDecodeCeiling
	}
	opt.DecodeCeiling = min(opt.DecodeCeiling, opt.DecodeBudget)
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		opt: opt, sem: make(chan struct{}, opt.Concurrency), hosts: newHostLimiter(opt.PerHost),
		hints: hintStore{cache: opt.Cache}, log: log,
		pool: newPool(opt.ThumbWorkers, opt.ThumbQueue),
		lim: thumbLimits{
			width: opt.ThumbWidth, maxPixels: opt.ThumbMaxPixels, ceiling: opt.DecodeCeiling, budget: newBudget(opt.DecodeBudget),
		},
	}
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
	base := flags &^ FlagThumb // the source is fetched, and cached, by its fetch flags alone
	switch {
	case !h.opt.Cache.Enabled():
		h.serveDirect(w, r, u, base) // no cache, no thumbnails: the original streams through
	case flags&FlagThumb != 0:
		h.serveThumb(w, r, u, base, orig)
	default:
		h.serveCached(w, r, u, base, orig)
	}
}

func fail(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
}

// slotOutcome is how an attempt to take the upstream slots ended.
type slotOutcome int

const (
	slotOK slotOutcome = iota
	slotBusy
	slotGone // the client went away
)

// acquire takes a per-host slot and then a global one (in that order, so a
// crowded host never holds global slots while it waits), queueing up to Wait
// for both together. release must be called once when the outcome is slotOK.
func (h *Handler) acquire(ctx context.Context, host string) (release func(), out slotOutcome) {
	wait := time.NewTimer(h.opt.Wait)
	defer wait.Stop()
	relHost, ok := h.hosts.acquire(ctx, host, wait.C)
	if !ok {
		if ctx.Err() != nil {
			return nil, slotGone
		}
		return nil, slotBusy
	}
	select {
	case h.sem <- struct{}{}:
		return func() { <-h.sem; relHost() }, slotOK
	case <-wait.C:
		relHost()
		return nil, slotBusy
	case <-ctx.Done():
		relHost()
		return nil, slotGone
	}
}

func busy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	fail(w, http.StatusServiceUnavailable)
}

// serveDirect streams from the source with no cache: the client's own
// conditional headers go upstream and a 304 comes straight back.
func (h *Handler) serveDirect(w http.ResponseWriter, r *http.Request, u *url.URL, flags int) {
	release, out := h.acquire(r.Context(), u.Hostname())
	switch out {
	case slotBusy:
		busy(w)
		return
	case slotGone:
		return
	}
	defer release()
	cd := cond{inm: r.Header.Get("If-None-Match"), ims: r.Header.Get("If-Modified-Since")}
	resp, done, err := h.fetchUpstream(r.Context(), u, flags, cd, 0)
	if err != nil {
		h.log.Debug("imgproxy: upstream", "host", u.Host, "err", err)
		fail(w, http.StatusBadGateway)
		return
	}
	defer done()
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		passValidators(w.Header(), resp)
		w.Header().Set("Cache-Control", cacheControl)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.relay(w, resp, nil)
}

func passValidators(hdr http.Header, resp *http.Response) {
	if v := resp.Header.Get("ETag"); v != "" {
		hdr.Set("ETag", v)
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		hdr.Set("Last-Modified", v)
	}
}

// relay checks a 200 response, streams it to the client and, with a sink,
// tees it into the cache. Failures are remembered through the sink. Only a body
// that arrives complete and passes every check is committed to the cache.
func (h *Handler) relay(w http.ResponseWriter, resp *http.Response, sk *sink) {
	hdr := w.Header()
	if resp.StatusCode != http.StatusOK {
		sk.fail(negKindFor(resp.StatusCode), resp.StatusCode, "source answered "+strconv.Itoa(resp.StatusCode))
		fail(w, http.StatusBadGateway)
		return
	}
	if resp.ContentLength > h.opt.MaxBytes {
		sk.fail(imgcache.NegPermanent, http.StatusBadGateway, "over the size limit")
		fail(w, http.StatusBadGateway) // over the cap: nothing was written
		return
	}

	body := http.MaxBytesReader(nil, resp.Body, h.opt.MaxBytes)
	head := make([]byte, sniffLen)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		sk.fail(negKindForErr(err), 0, "reading the image failed")
		fail(w, http.StatusBadGateway)
		return
	}
	head = head[:n]
	if n == 0 {
		sk.fail(imgcache.NegTransient, 0, "empty body")
		fail(w, http.StatusBadGateway)
		return
	}
	ct, ok := detectType(head, resp.Header.Get("Content-Type"))
	if !ok {
		sk.fail(imgcache.NegPermanent, http.StatusUnsupportedMediaType, "not a supported image type")
		fail(w, http.StatusUnsupportedMediaType)
		return
	}
	cw := sk.begin(resp.ContentLength, h.log)

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
		abort(cw)
		return
	}
	teeErr := func(err error) {
		if err != nil {
			abort(cw)
			cw = nil
		}
	}
	if cw != nil {
		_, err := cw.Write(head)
		teeErr(err)
	}
	total := int64(n)
	buf := make([]byte, 32<<10)
	for {
		m, rerr := body.Read(buf)
		if m > 0 {
			if _, werr := w.Write(buf[:m]); werr != nil {
				abort(cw) // the browser went away: a partial body is never cached
				return
			}
			total += int64(m)
			if cw != nil {
				_, err := cw.Write(buf[:m])
				teeErr(err)
			}
		}
		if rerr == io.EOF {
			if cw != nil {
				if resp.ContentLength >= 0 && total != resp.ContentLength {
					abort(cw)
					return
				}
				sk.commit(cw, ct, resp, h.log)
			}
			return
		}
		if rerr != nil {
			// Over the cap mid-stream, or upstream died: the status is already
			// sent, so cut the connection and the browser shows a broken image.
			abort(cw)
			var mbe *http.MaxBytesError
			if errors.As(rerr, &mbe) {
				sk.fail(imgcache.NegPermanent, http.StatusBadGateway, "over the size limit")
			}
			panic(http.ErrAbortHandler)
		}
	}
}

func abort(cw *imgcache.Writer) {
	if cw != nil {
		cw.Abort()
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
