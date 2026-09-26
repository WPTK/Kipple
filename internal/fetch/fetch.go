package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/sanitize"
)

// Outcomes (fetch_log.outcome).
const (
	OutcomeOK          = "ok"
	OutcomeNotModified = "not_modified"
	OutcomeUnchanged   = "unchanged"
	OutcomeError       = "error"
	OutcomeSkipped     = "skipped"
	OutcomeTrimOnly    = "trim_only"
)

// Triggers (fetch_log.trigger).
const (
	TriggerScheduled  = "scheduled"
	TriggerManual     = "manual"
	TriggerFeedManual = "feed_manual"
	TriggerSubscribe  = "subscribe"
	TriggerImport     = "import"
	TriggerRetention  = "retention"
)

// Snapshot is the slice of a feeds row that one fetch attempt needs, plus the
// values the scheduler resolved from settings. The fetch never touches the DB.
type Snapshot struct {
	ID      int64
	URL     string
	Host    string
	Enabled bool
	Trigger string

	ETag         string
	LastModified string
	BodyHash     string
	UserAgent    string // resolved: feed override, else browser UA per mode ("" = client default)
	// RetryUserAgent, when set, is tried once after a 403/406 (fetch.user_agent_mode
	// browser_on_failure); a success sets Result.UAFallbackWorked.
	RetryUserAgent string
	UAFallback     bool   // feeds.ua_fallback as loaded
	HTTPAuth       string // user:pass

	IgnoreHTTPCache  bool
	DisableHTTP2     bool
	AllowInsecureTLS bool
	AllowPrivateNet  bool

	DedupMode    string
	RekeyPending bool
	Fulltext     bool

	IntervalMinutes int   // feeds.interval_minutes, 0 = inherit
	Retention       int   // feeds.retention, -1 = inherit, 0 = unlimited
	IntervalS       int64 // resolved interval in seconds (feed override or global)
	HonorTTL        bool  // fetch.honor_publisher_ttl

	Redirect            RedirectState
	ConsecutiveFailures int
	InitialReadBefore   int64 // 0 = none
	LastSuccessAt       int64 // 0 = never

	// Full drops validators and the body-hash short circuit for this attempt.
	Full bool
	// HostUntil is the host's live Retry-After deadline at dispatch time.
	HostUntil time.Time
}

// Result is everything one attempt learned, ready for CommitFetch or
// CommitFetchError. Schedule() fills NextFetchAt and CurrentDelayS.
type Result struct {
	Snap      Snapshot
	StartedAt time.Time
	Duration  time.Duration

	Outcome  string // ok | unchanged | not_modified | error
	Status   int    // last HTTP status, 0 when none
	ErrClass string
	ErrMsg   string
	Gone     bool // 410: disable the feed
	// Cancelled means the attempt was aborted by shutdown and must not be written.
	Cancelled bool

	Bytes    int
	FinalURL string
	Hops     []Hop

	// UAFallbackWorked: the RetryUserAgent retry succeeded, so the feed should
	// use the browser UA from now on.
	UAFallbackWorked bool

	Feed  *Feed // parsed feed, Outcome ok only
	Notes []string

	// Validators to store when SetValidators (empty string stores NULL).
	SetValidators bool
	ETag          string
	LastModified  string
	BodyHash      string // decoded-body hash to store (ok and unchanged)

	RetryAfter time.Duration // 429/503
	TTLHintS   int64

	Redirect RedirectDecision

	NextFetchAt   time.Time
	CurrentDelayS int64
}

// Success reports whether the outcome counts as a successful fetch.
func (r *Result) Success() bool { return r.Outcome != OutcomeError }

// Schedule fills NextFetchAt and CurrentDelayS per design §4.6.
func (r *Result) Schedule(now time.Time, rnd Rand) {
	if r.Outcome == OutcomeError {
		r.NextFetchAt, r.CurrentDelayS = NextOnFailure(now, r.Snap.IntervalS, r.Snap.ConsecutiveFailures+1,
			r.RetryAfter, r.Snap.HostUntil, rnd)
		return
	}
	r.NextFetchAt, r.CurrentDelayS = NextOnSuccess(now, r.Snap.IntervalS, r.TTLHintS, rnd)
}

func (r *Result) fail(class, msg string) *Result {
	r.Outcome, r.ErrClass, r.ErrMsg = OutcomeError, class, msg
	return r
}

// UARefused reports whether a response looks like the publisher rejecting the
// User-Agent: 403 or 406, or a Cloudflare challenge served as a 503.
func UARefused(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusNotAcceptable:
		return true
	case http.StatusServiceUnavailable:
		return resp.Header.Get("cf-mitigated") == "challenge"
	}
	return false
}

// get sends one conditional GET with the given User-Agent.
func (c *Client) get(ctx context.Context, hc *http.Client, snap Snapshot, ua string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, snap.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", acceptHeader)
	if snap.HTTPAuth != "" { // the first request goes to the feed's own URL; redirects: httpClient
		user, pass, _ := strings.Cut(snap.HTTPAuth, ":")
		req.SetBasicAuth(user, pass)
	}
	if !snap.Full && !snap.IgnoreHTTPCache {
		if snap.ETag != "" {
			req.Header.Set("If-None-Match", snap.ETag)
		}
		if snap.LastModified != "" {
			req.Header.Set("If-Modified-Since", snap.LastModified)
		}
	}
	return hc.Do(req)
}

// Fetch performs one conditional GET and classifies the outcome (design §4.4,
// §4.5). It never returns an error: everything is a Result. now is the
// scheduler clock, used for Retry-After / Expires arithmetic.
func (c *Client) Fetch(ctx context.Context, snap Snapshot, now time.Time) *Result {
	res := &Result{Snap: snap, StartedAt: now}
	began := time.Now()
	defer func() { res.Duration = time.Since(began) }()

	ua := snap.UserAgent
	if ua == "" {
		ua = c.ua
	}
	feedU, _ := url.Parse(snap.URL) // nil on error: then no hop carries credentials
	hc := c.httpClient(variant{noHTTP2: snap.DisableHTTP2, insecureTLS: snap.AllowInsecureTLS, allowPrivate: snap.AllowPrivateNet}, &res.Hops, feedU)
	resp, err := c.get(ctx, hc, snap, ua)
	if err == nil && snap.RetryUserAgent != "" && snap.RetryUserAgent != ua && UARefused(resp) {
		// The feed refused Kipple's User-Agent: retry once as a browser. The
		// same guarded transport is used, so the SSRF guard is unchanged.
		resp.Body.Close()
		res.Hops = nil
		retry, rerr := c.get(ctx, hc, snap, snap.RetryUserAgent)
		if rerr == nil {
			resp = retry
			if code := resp.StatusCode; code == http.StatusNotModified || (code >= 200 && code <= 299) {
				res.UAFallbackWorked = true
			}
		} else {
			resp, err = nil, rerr
		}
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			res.Cancelled = true
			return res
		}
		class, msg := Classify(err)
		return res.fail(class, msg)
	}
	defer resp.Body.Close()

	res.Status = resp.StatusCode
	if resp.Request != nil && resp.Request.URL != nil {
		fu := *resp.Request.URL
		// A redirect to user:pass@host must not put the credentials in fetch_log
		// or feeds.url; without them the URL is also one feedurl accepts.
		fu.User = nil
		if fin, err := feedurl.Normalize(fu.String()); err == nil {
			res.FinalURL = fin
		} else {
			res.FinalURL = fu.String()
		}
	}
	res.TTLHintS = PublisherHintSeconds(snap.HonorTTL, 0, resp.Header, now)
	res.Redirect = DecideRedirect(snap.URL, snap.Redirect, res.FinalURL, res.Hops)

	switch code := resp.StatusCode; {
	case code == http.StatusNotModified:
		res.Outcome = OutcomeNotModified
		res.SetValidators = true
		res.ETag = snap.ETag // a 304's ETag is ignored; keep ours
		res.LastModified = snap.LastModified
		if lm := resp.Header.Get("Last-Modified"); lm != "" {
			res.LastModified = lm
		}
		return res
	case code == http.StatusGone:
		res.Gone = true
		return res.fail(ClassGone, "410 Gone: the feed was removed")
	case code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable:
		res.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"), now, responseDate(resp.Header))
		res.Notes = append(res.Notes, fmt.Sprintf("retry_after=%ds", int64(res.RetryAfter.Seconds())))
		return res.fail(ClassHTTP, fmt.Sprintf("HTTP %d", code))
	case code == http.StatusForbidden && resp.Header.Get("cf-mitigated") == "challenge" &&
		strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html"):
		return res.fail(ClassCloudflare, "blocked by a Cloudflare challenge (TLS fingerprint); try 'disable HTTP/2' or a browser User-Agent")
	case code < 200 || code > 299:
		return res.fail(ClassHTTP, fmt.Sprintf("HTTP %d", code))
	}

	limit := c.opt.MaxResponseBody
	body, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, limit))
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			res.Cancelled = true
			return res
		}
		class, msg := Classify(err)
		return res.fail(class, msg)
	}
	res.Bytes = len(body)
	if len(bytes.TrimSpace(body)) == 0 {
		return res.fail(ClassEmpty, "empty response body")
	}

	httpCharset := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
		httpCharset = params["charset"]
	}

	// Validators, dropped together when Expires says the response is already stale ("0").
	etag, lm := resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	if exp, ok := resp.Header["Expires"]; ok && len(exp) > 0 {
		if _, err := http.ParseTime(exp[0]); err != nil {
			etag, lm = "", ""
		}
	}

	dec := decodeBody(body, httpCharset)
	res.BodyHash = dec.BodyHash
	if !snap.Full && snap.BodyHash != "" && dec.BodyHash == snap.BodyHash {
		res.Outcome = OutcomeUnchanged
		res.SetValidators, res.ETag, res.LastModified = true, etag, lm
		return res
	}

	feed, err := ParseDecoded(dec, ParseOptions{
		FeedURL:   res.FinalURL,
		DedupMode: snap.DedupMode,
		Content:   sanitize.Content,
	})
	if err != nil {
		res.BodyHash = ""
		return res.fail(ClassParse, "not a feed: "+err.Error())
	}
	res.TTLHintS = PublisherHintSeconds(snap.HonorTTL, feed.TTLMinutes, resp.Header, now)
	res.Feed = feed
	res.Outcome = OutcomeOK
	res.SetValidators, res.ETag, res.LastModified = true, etag, lm
	res.Notes = append(res.Notes, feed.Notes...)
	return res
}

// responseDate is the response's Date header, or the zero time when it is
// missing or not an HTTP date.
func responseDate(h http.Header) time.Time {
	t, err := http.ParseTime(h.Get("Date"))
	if err != nil {
		return time.Time{}
	}
	return t
}
