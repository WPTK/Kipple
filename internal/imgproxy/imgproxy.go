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
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	slotHoldGrace     = 30 * time.Second // SlotHold = Timeout + this
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

	MaxBytes int64 // 15 MiB
	// Timeout bounds the time spent waiting on the source: the headers, then
	// every body read, together (15 s). Time spent writing to a slow client
	// does not count.
	Timeout     time.Duration
	Concurrency int           // 8 simultaneous upstream fetches
	PerHost     int           // 4 of them to one host
	Wait        time.Duration // 10 s queueing for a slot
	// RevalidateWithin bounds how long a stale cached image may wait for its
	// source to answer a conditional request before the stale copy is served (3 s).
	RevalidateWithin time.Duration
	// SlotHold is the most one upstream exchange may keep its fetch slots
	// once its body is streaming (Timeout + 30 s). A client too slow to take
	// the body in that time (only possible where the body streams straight
	// through: cache off, low disk, a failed cache write) has its response
	// cut, so slow readers cannot starve every other image of a slot.
	SlotHold time.Duration

	// Thumbnails (FlagThumb). Zero values take the defaults.
	ThumbWidth     int           // 800 px
	ThumbWorkers   int           // 2 transcoder goroutines
	ThumbQueue     int           // 16 waiting jobs; beyond that the original is served
	ThumbMaxPixels int           // 24 megapixels; more is served as the original
	ThumbWait      time.Duration // 4 s a request waits for a thumbnail before the original is served
	DecodeCeiling  int64         // 80 MiB: the most one transcode may be estimated to allocate (thumbCost)
	DecodeBudget   int64         // 96 MiB: the most all running transcodes together may be estimated to allocate
}

// Handler serves GET /img/{sig}/{flags}/{u} (path values). The caller enforces
// the web session before it gets here.
type Handler struct {
	opt   Options
	sem   chan struct{}
	hosts *hostLimiter
	hints hintStore
	log   *slog.Logger

	pool     *pool
	lim      thumbLimits
	markWarn atomic.Int64 // unix seconds of the last "cannot record a thumbnail in progress" warning
}

// Close stops the thumbnail workers after the queued jobs finish. The handler
// keeps serving (originals) afterwards.
func (h *Handler) Close() { h.pool.close() }

// Closed reports whether Close has begun (no new thumbnail work is accepted).
func (h *Handler) Closed() bool {
	h.pool.mu.Lock()
	defer h.pool.mu.Unlock()
	return h.pool.shut
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
	if opt.PerHost <= 0 {
		opt.PerHost = defaultPerHost
	}
	if opt.Wait <= 0 {
		opt.Wait = defaultWait
	}
	if opt.RevalidateWithin <= 0 {
		opt.RevalidateWithin = defaultRevalidate
	}
	if opt.SlotHold <= 0 {
		opt.SlotHold = opt.Timeout + slotHoldGrace
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
	slot := sync.OnceFunc(release)
	defer slot()
	cd := cond{inm: r.Header.Get("If-None-Match"), ims: r.Header.Get("If-Modified-Since")}
	resp, done, err := h.fetchUpstream(r.Context(), u, flags, cd, 0)
	if err != nil {
		h.log.Debug("imgproxy: upstream", "host", u.Host, "err", err)
		fail(w, http.StatusBadGateway)
		return
	}
	fin := sync.OnceFunc(func() { _ = resp.Body.Close(); done(); slot() })
	defer fin()
	if resp.StatusCode == http.StatusNotModified {
		passValidators(w.Header(), resp, true)
		w.Header().Set("Cache-Control", cacheControl)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.relay(w, resp, nil, fin)
}

// passValidators copies the source's Last-Modified and, with etag, its ETag.
// A response that fills the cache sends no ETag: every later hit answers with
// the cache's own checksum tag, which the source's tag would never match.
func passValidators(hdr http.Header, resp *http.Response, etag bool) {
	if v := resp.Header.Get("ETag"); v != "" && etag {
		hdr.Set("ETag", v)
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		hdr.Set("Last-Modified", v)
	}
}

// refusal is why a response is not served as an image, decided before
// anything is written to the client.
type refusal struct {
	kind   imgcache.NegKind
	status int    // recorded with the failure
	code   int    // answered to the client
	reason string //
}

// vet checks a response before anything is written: the status, the declared
// length and the sniffed type. It returns the sniffed head, the type to serve
// and the capped body to read the rest from.
func (h *Handler) vet(resp *http.Response) (head []byte, ct string, body io.Reader, ref *refusal) {
	if resp.StatusCode != http.StatusOK {
		return nil, "", nil, &refusal{negKindFor(resp.StatusCode), resp.StatusCode, http.StatusBadGateway, "source answered " + strconv.Itoa(resp.StatusCode)}
	}
	if resp.ContentLength > h.opt.MaxBytes {
		return nil, "", nil, &refusal{imgcache.NegPermanent, http.StatusBadGateway, http.StatusBadGateway, "over the size limit"}
	}
	body = http.MaxBytesReader(nil, resp.Body, h.opt.MaxBytes)
	head = make([]byte, sniffLen)
	n, err := io.ReadFull(body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, "", nil, &refusal{negKindForErr(err), 0, http.StatusBadGateway, "reading the image failed"}
	}
	head = head[:n]
	if n == 0 {
		return nil, "", nil, &refusal{imgcache.NegTransient, 0, http.StatusBadGateway, "empty body"}
	}
	ct, ok := detectType(head, resp.Header.Get("Content-Type"))
	if !ok {
		return nil, "", nil, &refusal{imgcache.NegPermanent, http.StatusUnsupportedMediaType, http.StatusUnsupportedMediaType, "not a supported image type"}
	}
	return head, ct, body, nil
}

// relay checks a response and streams it (stream). Refusals are remembered
// through the sink. fin releases the upstream exchange (body, request, slot).
func (h *Handler) relay(w http.ResponseWriter, resp *http.Response, sk *sink, fin func()) {
	head, ct, body, ref := h.vet(resp)
	if ref != nil {
		sk.refuse(ref)
		fail(w, ref.code)
		return
	}
	h.stream(w, resp, sk, head, ct, body, fin)
}

// stream sends a vetted 200 to the client and, with a sink, fills the cache.
//
// With a cache file the body is read into it at the source's speed by a fill
// goroutine, and the client is served from the file as it grows: a slow client
// never holds up the upstream read (so never times it out), and once the body
// is complete it is committed and the flight and fetch slot are released even
// while the client is still reading. Memory stays at two 32 KiB buffers. Only a
// body that arrives complete (matching any Content-Length) is committed.
//
// Without one (no cache, or the cache refused: low disk) the body streams
// straight through, and the upstream time budget (budgetBody) only counts time
// spent waiting on the source.
//
// Either way the exchange gives its fetch slots back after SlotHold: when the
// body goes straight through, a client that has not taken it by then has its
// response cut (fin closes the source, so the next read fails) instead of
// holding a slot every other image needs. A client reading a committed file
// is not affected (fin has already run).
func (h *Handler) stream(w http.ResponseWriter, resp *http.Response, sk *sink, head []byte, ct string, body io.Reader, fin func()) {
	hold := time.AfterFunc(h.opt.SlotHold, fin)
	defer hold.Stop()
	cw := sk.begin(resp.ContentLength, h.log)
	var rd *os.File
	if cw != nil {
		var err error
		if rd, err = cw.OpenReader(); err != nil {
			h.log.Debug("imgproxy: not caching", "err", err)
			cw.Abort()
			cw = nil
		}
	}
	p, probing := w.(*probe)
	if cw == nil && probing {
		p.forward() // the thumbnail path cannot cache the original: stream it to its client instead of fetching twice
	}
	hdr := w.Header()
	passValidators(hdr, resp, sk == nil)
	hdr.Set("Content-Type", ct)
	hdr.Set("Cache-Control", cacheControl)
	setImageSecurityHeaders(hdr)
	if resp.ContentLength >= 0 {
		hdr.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	if cw == nil {
		if _, err := w.Write(head); err != nil {
			return
		}
		h.pump(w, body, sk)
		return
	}
	defer rd.Close()
	pr := newProgress()
	go h.fill(cw, body, head, resp, ct, sk, pr, fin)
	if probing {
		// Nobody waits on the bytes: let the fill finish. Only when it could not
		// commit (the bytes are complete but not cached) does the original go
		// to the thumbnail's client, from the file, without a second fetch.
		if st := pr.wait(); st == fillFailed || (st == fillDone && pr.committed()) {
			return
		}
		p.forward()
	}
	h.follow(w, rd, pr, body, sk)
}

// pump copies the rest of body to the client. A failure mid-stream (over the
// cap, the source died, the budget ran out) cuts the connection: the status is
// already sent, so the browser shows a broken image.
func (h *Handler) pump(w http.ResponseWriter, body io.Reader, sk *sink) {
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
			var mbe *http.MaxBytesError
			if errors.As(rerr, &mbe) {
				sk.fail(imgcache.NegPermanent, http.StatusBadGateway, "over the size limit")
			}
			panic(http.ErrAbortHandler)
		}
	}
}

// fill reads the upstream body into the cache file at the source's speed.
func (h *Handler) fill(cw *imgcache.Writer, body io.Reader, head []byte, resp *http.Response, ct string, sk *sink, pr *progress, fin func()) {
	var total int64
	write := func(b []byte) bool {
		if _, err := cw.Write(b); err != nil {
			// The disk failed or the file hit the cache's object limit: stop
			// caching and hand the rest of the body to the client loop.
			h.log.Debug("imgproxy: cache write failed; streaming the rest uncached", "err", err)
			cw.Abort()
			pr.handoff(b)
			return false
		}
		total += int64(len(b))
		pr.add(int64(len(b)))
		return true
	}
	if !write(head) {
		return
	}
	buf := make([]byte, 32<<10)
	for {
		m, rerr := body.Read(buf)
		if m > 0 && !write(buf[:m]) {
			return
		}
		switch {
		case rerr == io.EOF:
			if resp.ContentLength >= 0 && total != resp.ContentLength {
				cw.Abort()
				pr.finish(fillFailed, false)
			} else {
				committed := sk.commit(cw, ct, resp, h.log)
				sk.done()
				pr.finish(fillDone, committed)
			}
			fin()
			return
		case rerr != nil:
			cw.Abort()
			var mbe *http.MaxBytesError
			if errors.As(rerr, &mbe) {
				sk.fail(imgcache.NegPermanent, http.StatusBadGateway, "over the size limit")
			}
			sk.done()
			pr.finish(fillFailed, false)
			fin()
			return
		}
	}
}

// follow serves the client from the cache file as the fill writes it.
func (h *Handler) follow(w http.ResponseWriter, rd *os.File, pr *progress, body io.Reader, sk *sink) {
	buf := make([]byte, 32<<10)
	var off int64
	for {
		n, st, pending := pr.next(off)
		if off < n {
			m, err := rd.ReadAt(buf[:min(int64(len(buf)), n-off)], off)
			if m > 0 {
				if _, werr := w.Write(buf[:m]); werr != nil {
					return // the browser went away; the request's end cancels the fill
				}
				off += int64(m)
				continue
			}
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			continue
		}
		switch st {
		case fillDone:
			return
		case fillFailed:
			panic(http.ErrAbortHandler)
		case fillHandoff:
			if len(pending) > 0 {
				if _, err := w.Write(pending); err != nil {
					return
				}
			}
			h.pump(w, body, sk)
			return
		}
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
