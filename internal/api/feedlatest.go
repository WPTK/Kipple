package api

import (
	"net/http"
	"strconv"

	"github.com/WPTK/kipple/internal/store"
)

// defaultLatest is how many headlines per feed GET /api/feeds/latest returns when the client says nothing.
const defaultLatest = 3

// feedsLatest is GET /api/feeds/latest?n=3: each feed's newest n headlines, read or not, for the Feed index. Feeds with
// no article are left out. It only reads, so a prefetch or a repeat changes nothing.
func (s *Server) feedsLatest(w http.ResponseWriter, r *http.Request) {
	n := defaultLatest
	if v := r.URL.Query().Get("n"); v != "" {
		var err error
		n, err = strconv.Atoi(v)
		if err != nil || n < 1 || n > store.LatestMaxPerFeed {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	feeds, err := s.db.LatestHeadlines(r.Context(), n)
	if err != nil {
		s.serverError(w, "feeds latest", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": feeds})
}
