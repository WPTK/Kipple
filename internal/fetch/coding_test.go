package fetch

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnaskedCoding(t *testing.T) {
	for v, want := range map[string]bool{"": false, "none": false, "utf-8": false, "br": true, "BR": true, "gzip, br": true, "deflate": true, "zstd": true} {
		require.Equal(t, want, UnaskedCoding(http.Header{"Content-Encoding": {v}}), v)
	}
}

func TestFetchRefusesABodyInACodingNobodyAskedFor(t *testing.T) {
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		serveRSS(w, r)
	})
	res := doFetch(t, c, snapFor(srv.URL+"/feed"))
	require.Equal(t, OutcomeError, res.Outcome)
	require.Contains(t, res.ErrMsg, "did not ask for")
}
