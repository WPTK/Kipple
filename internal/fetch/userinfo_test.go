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
