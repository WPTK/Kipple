package fetch

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/WPTK/kipple/internal/feedurl"
)

// hostOf is the lowercase host of u, "" when it has none.
func hostOf(u string) string {
	h, _ := feedurl.Host(u)
	return h
}

// Discoverable reports whether a fetch that finds a web page at the feed's URL looks for the feed
// the page links (discoverFeed): only while the URL is still the one the feed was given (the
// commit of a discovery, a redirect migration and a URL edit all record the first one in
// url_original) and has never fetched successfully. So a page is followed once, never a chain, and
// a feed that has worked is never silently replaced.
func (s Snapshot) Discoverable() bool { return s.LastSuccessAt == 0 && !s.URLChanged }

// discoverFeed handles a feed URL that answered with a web page (Discoverable): an address stored
// as typed by a Reader API subscribe or an OPML outline, which make no request when they store it.
// It picks the first feed the page links in its <head> (FeedLinks: the page's own order, main feed
// first) and reports it in Discovered, with outcome ok and no items. It makes no second request:
// the commit (store.CommitDiscovered) makes the link the feed's URL, due at once, and the scheduler
// fetches it like any feed, under that host's own per-host limit and Retry-After hold. page is the
// decoded page body and res the page fetch's result.
//
// A link is refused, with the reason as a parse error, when the feed has HTTP credentials and the
// link is on another host (they are for the host they were entered for), when the feed has a
// network exception and the link is on another site (the redirect rule, design §4.7), and when the
// link is a literal private address the feed may not reach. A name that resolves to a private
// address is stopped by the dial guard when it is fetched.
func discoverFeed(res *Result, page []byte) *Result {
	snap := res.Snap
	var link string
	for _, l := range FeedLinks(page, res.FinalURL) {
		if norm, err := feedurl.Normalize(l.URL); err == nil && norm != snap.URL {
			link = norm
			break
		}
	}
	if link == "" {
		return res.fail(ClassParse, "not a feed: this address is a web page, and the page does not link to a feed")
	}
	from, to := hostOf(snap.URL), hostOf(link)
	switch {
	case snap.HTTPAuth != "" && !strings.EqualFold(from, to):
		return res.fail(ClassParse, fmt.Sprintf("not a feed: this address is a web page whose feed is %s, on another host; "+
			"the feed's login is only sent to %s, so edit the feed address to use it (and enter the login again if it needs one)", link, from))
	case (snap.AllowPrivateNet || snap.AllowInsecureTLS) && !SameSite(from, to):
		return res.fail(ClassParse, fmt.Sprintf("not a feed: this address is a web page whose feed is %s, on another site; "+
			"edit the feed address to use it", link))
	}
	if ip, err := netip.ParseAddr(strings.Trim(to, "[]")); err == nil && !snap.AllowPrivateNet && Blocked(ip.Unmap()) {
		return res.fail(ClassSSRF, fmt.Sprintf("this address is a web page whose feed is %s, a private address this feed may not reach", link))
	}
	res.Outcome, res.Discovered = OutcomeOK, link
	res.SetValidators, res.BodyHash = false, ""
	res.Redirect = RedirectDecision{Action: RedirectClear}
	return res
}
