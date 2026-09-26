package api

import "net/http"

// ftsRebuild is POST /api/maintenance/fts-rebuild: the manual repair action for
// the search index (design §2.4). It runs on the writer behind the commit gate
// with its own store.FTSRebuildTimeout bound (45 s), not the 10 s write deadline,
// so it finishes on a large library and still answers inside the server's 60 s
// WriteTimeout. It answers 204 when the index is rebuilt.
//
// While it runs, every other write that does not queue on the commit gate
// (marking read, starring, edit-tag, logins, settings, ...) is refused at once
// with store.ErrMaintenance, which this API and the Reader API answer as
// 503 {"error":"maintenance"} with Retry-After: 15. The web client and Reader
// clients retry; fetch commits and maintenance batches wait on the gate and are
// not refused.
func (s *Server) ftsRebuild(w http.ResponseWriter, r *http.Request) {
	if err := s.db.RebuildFTS(r.Context()); err != nil {
		s.serverError(w, "fts rebuild", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
