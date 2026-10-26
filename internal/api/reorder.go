package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/WPTK/kipple/internal/store"
)

// ---- POST /api/reorder ----

func unmarshalMember(m map[string]json.RawMessage, key string, into any) error {
	if raw, ok := m[key]; ok {
		return json.Unmarshal(raw, into)
	}
	return nil
}

const reorderMaxIDs = 20000

// parseFolderOrder reads one folders entry of POST /api/reorder: a folder id (it keeps its parent),
// or {id, parent_id} (it moves inside parent_id first; null is the top level). Both keys are required
// in the object form and no other key is allowed.
func parseFolderOrder(raw json.RawMessage) (store.FolderOrder, bool) {
	if id, ok := parseID(raw); ok {
		return store.FolderOrder{ID: id}, true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || len(m) != 2 {
		return store.FolderOrder{}, false
	}
	rawID, okID := m["id"]
	rawParent, okParent := m["parent_id"]
	if !okID || !okParent {
		return store.FolderOrder{}, false
	}
	id, okID := parseID(rawID)
	parent, okParent := parseParentID(rawParent)
	if !okID || !okParent {
		return store.FolderOrder{}, false
	}
	return store.FolderOrder{ID: id, Parent: &parent}, true
}

// reorder is POST /api/reorder: {folders?:[id or {id, parent_id}, in order], feeds?:[{folder_id, ids}]}.
func (s *Server) reorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Folders []json.RawMessage `json:"folders"`
		Feeds   []struct {
			FolderID json.RawMessage   `json:"folder_id"`
			IDs      []json.RawMessage `json:"ids"`
		} `json:"feeds"`
	}
	m, ok := readObject(w, r, "folders", "feeds")
	if !ok {
		return
	}
	// readObject already parsed the body once; decode each member from its raw
	// bytes instead of marshalling the whole object and parsing it again.
	if err := errors.Join(unmarshalMember(m, "folders", &body.Folders), unmarshalMember(m, "feeds", &body.Feeds)); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "folders is a list of ids and feeds a list of {folder_id, ids}")
		return
	}
	bad := func(msg string) { writeErrorMsg(w, http.StatusBadRequest, "bad_request", msg) }
	if len(body.Folders) == 0 && len(body.Feeds) == 0 {
		bad("give folders and/or feeds")
		return
	}
	total := len(body.Folders)
	var folders []store.FolderOrder
	for _, rw := range body.Folders {
		f, ok := parseFolderOrder(rw)
		if !ok {
			bad("folders must be folder ids or {id, parent_id} objects")
			return
		}
		folders = append(folders, f)
	}
	var feeds []store.FeedOrder
	for _, g := range body.Feeds {
		fid, ok := parseID(g.FolderID)
		if !ok {
			bad("feeds[].folder_id must be a folder id")
			return
		}
		fo := store.FeedOrder{FolderID: fid}
		for _, rw := range g.IDs {
			id, ok := parseID(rw)
			if !ok {
				bad("feeds[].ids must be feed ids")
				return
			}
			fo.IDs = append(fo.IDs, id)
		}
		total += len(fo.IDs)
		feeds = append(feeds, fo)
	}
	if total > reorderMaxIDs {
		bad("too many ids")
		return
	}
	res, err := s.db.Reorder(r.Context(), folders, feeds)
	var re *store.ErrReorder
	switch {
	case writeFolderError(w, err):
		return
	case errors.As(err, &re):
		bad(re.Reason)
		return
	case errors.Is(err, store.ErrArchiveFeed):
		writeErrorMsg(w, http.StatusConflict, "archive_feed", "the archive feed cannot be reordered")
		return
	case err != nil:
		s.serverError(w, "reorder", err)
		return
	}
	for _, id := range res.Feeds {
		s.publishFeedChanged(id)
	}
	if len(res.Folders) > 0 || len(res.Feeds) > 0 {
		s.publishFolderChanged(0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed_feeds": idStrings(res.Feeds), "changed_folders": idStrings(res.Folders)})
}
