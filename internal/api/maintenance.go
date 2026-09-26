package api

import "net/http"

// ftsRebuild is POST /api/maintenance/fts-rebuild: the manual repair action for
// the search index (design §2.4). It runs on the writer behind the commit gate
// with its own store.FTSRebuildTimeout bound (45 s), not the 10 s write deadline,
// so it finishes on a large library and still answers inside the server's 60 s
// WriteTimeout.
func (s *Server) ftsRebuild(w http.ResponseWriter, r *http.Request) {
	if err := s.db.RebuildFTS(r.Context()); err != nil {
		s.serverError(w, "fts rebuild", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
