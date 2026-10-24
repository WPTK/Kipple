package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

// maxCatchupGroups bounds the sections one GET /api/catchup asks about.
const maxCatchupGroups = 500

// parseCatchupGroups reads `d<id>,f<id>,...`: d is a folder with its subfolders, f one feed.
func parseCatchupGroups(v string) ([]store.CatchupGroup, bool) {
	if v == "" {
		return nil, true
	}
	parts := strings.Split(v, ",")
	if len(parts) > maxCatchupGroups {
		return nil, false
	}
	out := make([]store.CatchupGroup, 0, len(parts))
	for _, p := range parts {
		if len(p) < 2 {
			return nil, false
		}
		id, err := strconv.ParseInt(p[1:], 10, 64)
		if err != nil || id <= 0 {
			return nil, false
		}
		switch p[0] {
		case 'd':
			out = append(out, store.CatchupGroup{FolderTreeID: id})
		case 'f':
			out = append(out, store.CatchupGroup{FeedID: id})
		default:
			return nil, false
		}
	}
	return out, true
}

// catchup is GET /api/catchup?groups=d1,f2: the New page's mark, the page's as_of (the newest article the server knew
// of, read before the counts so no count includes an article above it) and, for each group in the order asked, how many
// unread articles arrived after the mark. The articles themselves are read as any list is, with /api/items?after=<mark>.
func (s *Server) catchup(w http.ResponseWriter, r *http.Request) {
	groups, ok := parseCatchupGroups(r.URL.Query().Get("groups"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	mark, err := s.db.CatchupMark(r.Context())
	if err != nil {
		s.serverError(w, "catchup", err)
		return
	}
	asOf, err := s.db.MaxCommittedID(r.Context())
	if err != nil {
		s.serverError(w, "catchup", err)
		return
	}
	counts, err := s.db.CatchupCounts(r.Context(), mark, asOf, groups)
	if err != nil {
		s.serverError(w, "catchup", err)
		return
	}
	sections := make([]map[string]any, len(groups))
	for i, g := range groups {
		key := "f" + strconv.FormatInt(g.FeedID, 10)
		if g.FolderTreeID != 0 {
			key = "d" + strconv.FormatInt(g.FolderTreeID, 10)
		}
		sections[i] = map[string]any{"group": key, "count": counts[i]}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mark": strconv.FormatInt(mark, 10), "as_of": strconv.FormatInt(asOf, 10), "sections": sections})
}

// catchupSeen is POST /api/catchup/seen {as_of}: the reader left the New page, so the mark moves to the page's as_of.
// It only moves forward, and never past an article the server has not committed. It answers the mark in force.
func (s *Server) catchupSeen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AsOf string `json:"as_of"`
	}
	if !decodeBody(w, r, &req, false) {
		return
	}
	to, err := strconv.ParseInt(req.AsOf, 10, 64)
	if err != nil || to <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	newest, err := s.db.MaxCommittedID(r.Context())
	if err != nil {
		s.serverError(w, "catchup seen", err)
		return
	}
	if to > newest {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	mark, err := s.db.AdvanceCatchupMark(r.Context(), to)
	if err != nil {
		s.serverError(w, "catchup seen", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mark": strconv.FormatInt(mark, 10)})
}
