package fetch

import (
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
}

// A stored feed URL over the cap cannot come from any path that stores one (each goes through
// feedurl, which refuses it), so DecideRedirect clearing is the safe answer for a legacy row.
func TestDecideRedirectOverlongStoredURLClears(t *testing.T) {
	d := DecideRedirect("https://a.test/"+strings.Repeat("a", 5000), RedirectState{}, "https://b.test/f", []Hop{{Status: 301}})
	require.Equal(t, RedirectClear, d.Action)
}
