// Package favicon finds and stores an icon for each feed's site (design
// §4.11). Lookup fetches the site's home page, ranks its <link rel="icon">,
// "shortcut icon" and "apple-touch-icon" candidates, falls back to
// /favicon.ico, and keeps the first raster image (PNG, JPEG, GIF, WebP or ICO,
// sniffed from the bytes) within the size limit. Every request goes through
// the caller's guarded transport (ScopedTransport: the feed's network
// exceptions cover its own host only), so the feed fetcher's dial-time SSRF
// check applies to each hop. User names and passwords in any URL (site, link,
// redirect) are dropped: never sent as credentials, never stored. Finder runs lookups one at a time in the background,
// off the fetch path, and records them in feed_icon_checks.
package favicon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
)

const (
	maxPageBytes = 512 << 10 // of the home page; the head is near the top, the rest is ignored
	maxIconBytes = 256 << 10
	maxRedirects = 5
	maxTries     = 4 // icon fetches per lookup, /favicon.ico included

	requestTimeout = 10 * time.Second // per request, body included
)

// Icon is a found icon.
type Icon struct {
	Data        []byte
	ContentType string // image/png, image/jpeg, image/gif, image/webp or image/x-icon
	SourceURL   string // where the bytes came from (after redirects), without userinfo
	Hash        string // 16 hex chars of the SHA-256 of Data, used in icon URLs
}

// ErrTooLarge means an icon response was over maxIconBytes.
var ErrTooLarge = fmt.Errorf("the icon is larger than %d KiB", maxIconBytes>>10)

// Request is one lookup.
type Request struct {
	SiteURL   string            // the feed's site_url; "" to use FeedURL's origin
	FeedURL   string            // the feed's own URL, for its origin
	Transport http.RoundTripper // guarded (ScopedTransport); never a bare transport in production
	UserAgent string
	RetryUA   string // tried once after a 403/406, as the feed fetcher does
}

// PageURL is the page a lookup starts from: site_url when it is an absolute
// http(s) URL, else the origin of the feed URL. Userinfo is dropped.
func PageURL(siteURL, feedURL string) (string, error) {
	if u, err := url.Parse(strings.TrimSpace(siteURL)); err == nil && httpURL(u) {
		u.Fragment, u.RawFragment = "", ""
		u.User = nil
		return u.String(), nil
	}
	u, err := url.Parse(feedURL)
	if err != nil || !httpURL(u) {
		return "", errors.New("the feed has no http(s) site or feed URL")
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}).String(), nil
}

// Lookup finds an icon for the request's site. The error of the last attempt
// is returned when no candidate yields a usable icon.
func Lookup(ctx context.Context, r Request) (Icon, error) {
	page, err := PageURL(r.SiteURL, r.FeedURL)
	if err != nil {
		return Icon{}, err
	}
	hc := &http.Client{Transport: r.Transport, Timeout: requestTimeout, CheckRedirect: checkRedirect}

	var cands []candidate
	origin := page
	if body, final, perr := getPage(ctx, hc, r, page); perr == nil {
		cands = iconLinks(body, final)
		origin = final
	} else if ctx.Err() != nil {
		return Icon{}, ctx.Err()
	} else if blocked(perr) {
		return Icon{}, perr // the icon would live on the same refused address
	}
	if fav := faviconURL(origin); fav != "" {
		dup := false
		for _, c := range cands {
			dup = dup || c.URL == fav
		}
		if !dup {
			cands = append(cands, candidate{URL: fav})
		}
	}
	if len(cands) > maxTries {
		// Keep the fallback: it is the most likely to exist.
		cands = append(cands[:maxTries-1], cands[len(cands)-1])
	}
	lastErr := errors.New("the site advertises no icon")
	for _, c := range cands {
		icon, err := getIcon(ctx, hc, r, c.URL)
		if err == nil {
			return icon, nil
		}
		if ctx.Err() != nil {
			return Icon{}, ctx.Err()
		}
		lastErr = fmt.Errorf("%s: %w", c.URL, err)
	}
	return Icon{}, lastErr
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return errors.New("redirect to a non-http(s) URL")
	}
	// http.Client turns a Location's userinfo into an Authorization header when
	// it sends the hop, which happens after this check: drop it here.
	req.URL.User = nil
	return nil
}

func blocked(err error) bool {
	var b *fetch.BlockedError
	return errors.As(err, &b)
}

// faviconURL is /favicon.ico at the origin of page.
func faviconURL(page string) string {
	u, err := url.Parse(page)
	if err != nil || !httpURL(u) {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/favicon.ico"}).String()
}

// get sends a GET with the request's User-Agent and retries once with RetryUA
// when the server refuses it.
func get(ctx context.Context, hc *http.Client, r Request, target, accept string) (*http.Response, error) {
	do := func(ua string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.URL.User = nil // never sent as Basic credentials
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", accept)
		return hc.Do(req)
	}
	resp, err := do(r.UserAgent)
	if err == nil && r.RetryUA != "" && r.RetryUA != r.UserAgent && fetch.UARefused(resp) {
		resp.Body.Close()
		resp, err = do(r.RetryUA)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// getPage fetches the home page, keeping at most maxPageBytes of it, and
// returns the body and the final URL after redirects.
func getPage(ctx context.Context, hc *http.Client, r Request, page string) ([]byte, string, error) {
	resp, err := get(ctx, hc, r, page, "text/html, application/xhtml+xml;q=0.9, */*;q=0.1")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, "", err
	}
	return body, withoutUser(resp.Request.URL), nil
}

// getIcon fetches one candidate and validates it.
func getIcon(ctx context.Context, hc *http.Client, r Request, target string) (Icon, error) {
	resp, err := get(ctx, hc, r, target, "image/png, image/x-icon, image/webp, image/gif, image/jpeg;q=0.9, image/*;q=0.5")
	if err != nil {
		return Icon{}, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxIconBytes {
		return Icon{}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return Icon{}, err
	}
	if len(data) > maxIconBytes {
		return Icon{}, ErrTooLarge
	}
	ct, err := sniff(data)
	if err != nil {
		return Icon{}, err
	}
	sum := sha256.Sum256(data)
	return Icon{Data: data, ContentType: ct, SourceURL: withoutUser(resp.Request.URL), Hash: hex.EncodeToString(sum[:8])}, nil
}

// withoutUser is u as a string with any userinfo removed.
func withoutUser(u *url.URL) string {
	c := *u
	c.User = nil
	return c.String()
}
