package fetch

import (
	"strings"

	"github.com/WPTK/kipple/internal/feedurl"
)

// Hop is one followed redirect.
type Hop struct {
	Status int
	From   string
	To     string
}

// RedirectState is the redirect_* columns of a feed.
type RedirectState struct {
	To    string // redirect_to, "" when none
	Kind  string // permanent | temporary
	Count int
}

// Redirect actions.
const (
	RedirectClear   = "clear"   // final == feed URL: clear redirect_to
	RedirectSet     = "set"     // record To/Kind/Count
	RedirectMigrate = "migrate" // rewrite feeds.url to To (store checks ownership first)
)

// RedirectDecision is the pure outcome of design §4.7.
type RedirectDecision struct {
	Action string
	To     string
	Kind   string
	Count  int
}

// DecideRedirect applies the redirect policy. feedURL is feeds.url, final the
// normalized URL of the last response, hops the followed redirects in order.
func DecideRedirect(feedURL string, cur RedirectState, final string, hops []Hop) RedirectDecision {
	k1, nf, err1 := feedurl.KeyAndNormalize(feedURL)
	k2, fin, err2 := feedurl.KeyAndNormalize(final)
	if err1 != nil || err2 != nil || nf == fin {
		return RedirectDecision{Action: RedirectClear}
	}

	permanent := len(hops) > 0
	for _, h := range hops {
		if h.Status != 301 && h.Status != 308 {
			permanent = false
		}
	}
	downgrade := strings.HasPrefix(nf, "https://") && strings.HasPrefix(fin, "http://")
	for _, h := range hops {
		if strings.HasPrefix(strings.ToLower(h.From), "https://") && strings.HasPrefix(strings.ToLower(h.To), "http://") {
			downgrade = true
		}
	}
	if !permanent || downgrade {
		return RedirectDecision{Action: RedirectSet, To: fin, Kind: "temporary", Count: 0}
	}

	// Only http -> https on the same host, identical path and query.
	if strings.HasPrefix(nf, "http://") && strings.HasPrefix(fin, "https://") && k1 == k2 {
		return RedirectDecision{Action: RedirectMigrate, To: fin, Kind: "permanent", Count: 3}
	}
	if cur.To == fin && cur.Kind == "permanent" {
		if cur.Count+1 >= 3 {
			return RedirectDecision{Action: RedirectMigrate, To: fin, Kind: "permanent", Count: cur.Count + 1}
		}
		return RedirectDecision{Action: RedirectSet, To: fin, Kind: "permanent", Count: cur.Count + 1}
	}
	return RedirectDecision{Action: RedirectSet, To: fin, Kind: "permanent", Count: 1}
}
