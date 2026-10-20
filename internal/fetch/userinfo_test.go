package fetch

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A redirect to a URL with credentials in it does not put them in the final URL
// (fetch_log.final_url, a redirect migration's feeds.url).
func TestFinalURLDropsUserinfo(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", serveRSS)
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(base, "http://", "http://bob:secret@", 1)+"/feed", http.StatusMovedPermanently)
	})
	srv, c := feedServer(t, mux.ServeHTTP)
	base = srv.URL
	res := doFetch(t, c, snapFor(srv.URL+"/moved"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, srv.URL+"/feed", res.FinalURL)
	require.Equal(t, RedirectSet, res.Redirect.Action)
	require.NotContains(t, res.Redirect.To, "secret")
}

// A redirect to an address over the length cap still reads the feed, records no redirect, and says
// so in the fetch notes instead of dropping the target silently.
func TestRedirectToOverlongAddressIsNoted(t *testing.T) {
	mux := http.NewServeMux()
	long := "/feed/" + strings.Repeat("a", 5000)
	mux.HandleFunc(long, serveRSS)
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, long, http.StatusMovedPermanently)
	})
	srv, c := feedServer(t, mux.ServeHTTP)
	res := doFetch(t, c, snapFor(srv.URL+"/moved"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, RedirectClear, res.Redirect.Action)
	require.Contains(t, strings.Join(res.Notes, ";"), "redirect_not_recorded")
	require.Equal(t, srv.URL+"/moved", res.FinalURL, "the long address is not kept as the final URL")
}

// A stored feed URL over the cap cannot come from any path that stores one (each goes through
// feedurl, which refuses it), so DecideRedirect clearing is the safe answer for a legacy row.
func TestDecideRedirectOverlongStoredURLClears(t *testing.T) {
	d := DecideRedirect("https://a.test/"+strings.Repeat("a", 5000), RedirectState{}, "https://b.test/f", []Hop{{Status: 301}})
	require.Equal(t, RedirectClear, d.Action)
}

// A feed stored before the length limit existed still fetches. With no redirect there is nothing to
// say; when it redirects, the log says why no redirect is recorded.
func TestOverlongStoredFeedURLIsNotedOnlyOnRedirect(t *testing.T) {
	mux := http.NewServeMux()
	long := "/feed/" + strings.Repeat("a", 5000)
	mux.HandleFunc(long, serveRSS)
	mux.HandleFunc("/feed/"+strings.Repeat("a", 5001), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/short", http.StatusFound)
	})
	mux.HandleFunc("/short", serveRSS)
	srv, c := feedServer(t, mux.ServeHTTP)

	res := doFetch(t, c, snapFor(srv.URL+long))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Empty(t, res.Notes, "no redirect, no note")

	res = doFetch(t, c, snapFor(srv.URL+"/feed/"+strings.Repeat("a", 5001)))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Contains(t, strings.Join(res.Notes, ";"), "redirect_not_recorded")
	require.Equal(t, RedirectClear, res.Redirect.Action)
}

// longHost is a server name over the 253 characters DNS allows, each label within 63.
var longHost = strings.Repeat(strings.Repeat("h", 60)+".", 5) + "example"

// A redirect to a server name longer than DNS allows (only a resolver that answers for it lets the fetch get that
// far) reads the feed, records no redirect and says why, like a redirect to an address over the length cap.
func TestRedirectToOverlongHostIsNoted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", serveRSS)
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+longHost+"/feed", http.StatusMovedPermanently)
	})
	srv, c := feedServer(t, mux.ServeHTTP)
	// Every name dials the test server: the stand-in for a resolver that answers for the long name.
	addr := srv.Listener.Addr().String()
	for _, v := range []variant{{}, {allowPrivate: true}} {
		c.transports[v] = &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
	}
	res := doFetch(t, c, snapFor(srv.URL+"/moved"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, srv.URL+"/moved", res.FinalURL, "the long name is not kept as the final URL")
	require.Equal(t, RedirectClear, res.Redirect.Action)
	require.Contains(t, strings.Join(res.Notes, ";"), "redirect_not_recorded: the server name")
}

// DecideRedirect clears for a target whose server name DNS could not carry, whatever the hops say.
func TestDecideRedirectToOverlongHostClears(t *testing.T) {
	d := DecideRedirect("https://a.test/f", RedirectState{}, "https://"+longHost+"/f", []Hop{{Status: 301}})
	require.Equal(t, RedirectClear, d.Action)
	d = DecideRedirect("https://a.test/f", RedirectState{To: "https://" + longHost + "/f", Kind: "permanent", Count: 2}, "https://"+longHost+"/f", []Hop{{Status: 301}})
	require.Equal(t, RedirectClear, d.Action, "a count carried from before the rule does not migrate it")
}
