package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/store"
)

// ---- folders ----

func parseFolderName(raw json.RawMessage) (string, bool) {
	n, ok := rawString(raw)
	n = strings.TrimSpace(n)
	return n, ok && n != "" && utf8.RuneCountInString(n) <= maxFolderName && !hasControl(n)
}

// parseParentID reads a folder's parent_id: null is the top level (0).
func parseParentID(raw json.RawMessage) (int64, bool) {
	if isNull(raw) {
		return 0, true
	}
	return parseID(raw)
}

// writeFolderError answers the folder writer's refusals; it reports false for any other error.
func writeFolderError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrParentNotFound): // the request's fault, unlike the folder itself missing (404)
		writeErrorMsg(w, http.StatusBadRequest, "folder_not_found", "no such parent folder")
	case errors.Is(err, store.ErrFolderExists):
		writeErrorMsg(w, http.StatusConflict, "folder_exists", "a folder with that name exists there")
	case errors.Is(err, store.ErrFolderDepth):
		writeErrorMsg(w, http.StatusConflict, "folder_too_deep", "folders nest at most 8 levels deep")
	case errors.Is(err, store.ErrFolderCycle):
		writeErrorMsg(w, http.StatusConflict, "folder_cycle", "a folder cannot move inside itself or one of its subfolders")
	case errors.Is(err, store.ErrFolderParent):
		writeErrorMsg(w, http.StatusConflict, "default_folder", "the default folder stays at the top level and holds no subfolders")
	case errors.Is(err, store.ErrBadFolderName): // a stored name the rules now refuse, met on a move
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "name must be 1 to 100 characters")
	default:
		return false
	}
	return true
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	m, ok := readObject(w, r, "name", "position", "parent_id")
	if !ok {
		return
	}
	name, ok := parseFolderName(m["name"])
	if !ok {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "name must be 1 to 100 characters")
		return
	}
	pos := int64(-1)
	if raw, present := m["position"]; present && !isNull(raw) {
		if pos, ok = rawInt(raw); !ok || pos < 0 || pos > 1_000_000 {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "position must be an integer from 0 to 1000000")
			return
		}
	}
	var parent int64
	if raw, present := m["parent_id"]; present {
		if parent, ok = parseParentID(raw); !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "parent_id must be a folder id or null")
			return
		}
	}
	f, err := s.db.CreateFolder(r.Context(), name, parent, pos)
	switch {
	case writeFolderError(w, err):
		return
	case err != nil:
		s.serverError(w, "create folder", err)
		return
	}
	s.publishFolderChanged(f.ID)
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) patchFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	m, ok := readObject(w, r, "name", "position", "parent_id")
	if !ok {
		return
	}
	var p store.FolderPatch
	if raw, present := m["name"]; present {
		n, ok := parseFolderName(raw)
		if !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "name must be 1 to 100 characters")
			return
		}
		p.Name = &n
	}
	if raw, present := m["position"]; present {
		n, ok := rawInt(raw)
		if !ok || isNull(raw) || n < 0 || n > 1_000_000 {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "position must be an integer from 0 to 1000000")
			return
		}
		p.Position = &n
	}
	if raw, present := m["parent_id"]; present {
		parent, ok := parseParentID(raw)
		if !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "parent_id must be a folder id or null")
			return
		}
		p.Parent = &parent
	}
	f, err := s.db.UpdateFolder(r.Context(), id, p)
	switch {
	case errors.Is(err, store.ErrFolderNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case writeFolderError(w, err):
		return
	case err != nil:
		s.serverError(w, "patch folder", err)
		return
	}
	s.publishFolderChanged(id)
	writeJSON(w, http.StatusOK, f)
}
func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	moved, err := s.db.DeleteFolder(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrFolderNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, store.ErrDefaultFolder):
		writeErrorMsg(w, http.StatusConflict, "default_folder", "the default folder cannot be deleted")
		return
	case err != nil:
		s.serverError(w, "delete folder", err)
		return
	}
	for _, fid := range moved {
		s.publishFeedChanged(fid)
	}
	s.publishFolderChanged(id)
	w.WriteHeader(http.StatusNoContent)
}
