package fetch

import (
	"context"
	"fmt"
	"time"

	"github.com/WPTK/kipple/internal/feedurl"
)

// normalizeLink is a linked feed URL as the feed's URL would be stored (feedurl.Normalize: http(s),
// no credentials).
func normalizeLink(u string) (string, error) { return feedurl.Normalize(u) }

// hostOf is the lowercase host of u, "" when it has none.
func hostOf(u string) string {
	h, _ := feedurl.Host(u)
	return h
}

// discoverFeed handles a feed URL that answered with a web page before the feed ever fetched
// successfully: an address stored as typed (a Reader API subscribe, an OPML outline, a site address),
// which never went through the web dialog's discovery. It takes the first feed the page links in its
// <head> (FeedLinks: the page's own order, main feed first) and fetches that instead, in the same
// attempt and through the same guarded client. A success carries Discovered, and the commit makes it
// the feed's URL (or, when another feed already has that URL, removes this one: see
// store.CommitFetch). page is the decoded page body and res the page fetch's result.
//
// A feed that has fetched successfully before never does this: a feed URL that starts answering with
// a page is reported as the parse error it is, not silently replaced.
func (c *Client) discoverFeed(ctx context.Context, res *Result, page []byte, now time.Time) *Result {
	snap := res.Snap
	var link string
	for _, l := range FeedLinks(page, res.FinalURL) {
		if norm, err := normalizeLink(l.URL); err == nil && norm != snap.URL {
			link = norm
			break
		}
	}
	if link == "" {
		return res.fail(ClassParse, "not a feed: this address is a web page, and the page does not link to a feed")
	}
	// The feed's credentials and network exceptions were granted for its own site; a page that
	// points elsewhere is not followed with them (the same rule as a redirect, design §4.7).
	if snap.HTTPAuth != "" || snap.AllowPrivateNet || snap.AllowInsecureTLS {
		if !SameSite(hostOf(snap.URL), hostOf(link)) {
			return res.fail(ClassParse, fmt.Sprintf("not a feed: this address is a web page whose feed is %s, on another site; "+
				"edit the feed address to use it", link))
		}
	}
	sub := snap
	sub.URL, sub.Host, sub.linked = link, hostOf(link), true
	sub.Full, sub.ETag, sub.LastModified, sub.BodyHash = true, "", "", ""
	sub.Redirect = RedirectState{}
	out := c.Fetch(ctx, sub, now)
	if out.Cancelled {
		return out
	}
	out.Snap = snap // the commit checks the feed row against the URL it was fetched as
	out.StartedAt = res.StartedAt
	out.UAFallbackWorked = out.UAFallbackWorked || res.UAFallbackWorked
	if !out.Success() {
		// The page stays the feed's address; the next attempt discovers again.
		out.ErrMsg = fmt.Sprintf("this address is a web page; the feed it links, %s, failed: %s", link, out.ErrMsg)
		out.Gone = false // a 410 from the linked feed says nothing about the page
		out.Redirect = RedirectDecision{Action: RedirectClear}
		return out
	}
	out.Discovered = link
	return out
}
