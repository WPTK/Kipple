package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
	"github.com/WPTK/kipple/starter"
)

// starterFeed is one recommendation as GET /api/starter-feeds shows it.
type starterFeed struct {
	starter.Feed
	Subscribed bool `json:"subscribed"`
}

type starterCategory struct {
	ID    string        `json:"id"`
	Title string        `json:"title"`
	Feeds []starterFeed `json:"feeds"`
}

// loadStarter is the embedded list, or nil (logged) when it cannot be used.
func (s *Server) loadStarter() *starter.File {
	f, err := starter.Load()
	if err != nil {
		s.log.Error("api: the recommended feeds list cannot be used", "err", err)
		return nil
	}
	return f
}

// starterFeeds is GET /api/starter-feeds: the embedded recommendations with
// subscribed per feed. available is false (and categories empty) when the list
// cannot be used, so the wizard shows "no recommendations available".
func (s *Server) starterFeeds(w http.ResponseWriter, r *http.Request) {
	f := s.loadStarter()
	cats := []starterCategory{}
	if f != nil {
		rd := s.db.Reader()
		for _, c := range f.Categories {
			sc := starterCategory{ID: c.ID, Title: c.Title, Feeds: make([]starterFeed, 0, len(c.Feeds))}
			for _, fd := range c.Feeds {
				_, found, err := store.FindFeedByURL(r.Context(), rd, fd.URL)
				if err != nil {
					s.serverError(w, "starter feeds", err)
					return
				}
				sc.Feeds = append(sc.Feeds, starterFeed{Feed: fd, Subscribed: found})
			}
			cats = append(cats, sc)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": f != nil, "categories": cats})
}

// starterSubscribe is POST /api/starter-feeds {ids, folders}: subscribes by id
// only (the server never takes a URL here), one folder per category when
// folders is true, through the same insert-then-import path as OPML.
func (s *Server) starterSubscribe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs     []string `json:"ids"`
		Folders bool     `json:"folders"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	f := s.loadStarter()
	if f == nil {
		writeErrorMsg(w, http.StatusServiceUnavailable, "unavailable", "no recommendations are available")
		return
	}
	type pick struct {
		feed     starter.Feed
		category string
	}
	byID := map[string]pick{}
	for _, c := range f.Categories {
		for _, fd := range c.Feeds {
			byID[fd.ID] = pick{fd, c.Title}
		}
	}
	if len(body.IDs) > starter.MaxFeeds {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	want := map[string]bool{}
	var unknown []string
	for _, id := range body.IDs {
		if _, ok := byID[id]; !ok {
			unknown = append(unknown, id)
		}
		want[id] = true
	}
	if len(unknown) > 0 {
		if len(unknown) > 5 {
			unknown = unknown[:5]
		}
		writeErrorMsg(w, http.StatusBadRequest, "unknown_id", "not a recommended feed: "+strings.Join(unknown, ", "))
		return
	}
	// The file's order, not the request's, so folders and positions are stable.
	doc := &opml.Doc{}
	seenFolder := map[string]bool{}
	for _, c := range f.Categories {
		for _, fd := range c.Feeds {
			if !want[fd.ID] {
				continue
			}
			folder := ""
			if body.Folders {
				folder = c.Title
				if !seenFolder[folder] {
					seenFolder[folder] = true
					doc.Folders = append(doc.Folders, folder)
				}
			}
			doc.Feeds = append(doc.Feeds, opml.Feed{URL: fd.URL, Title: fd.Title, SiteURL: fd.Site, Folder: folder})
		}
	}
	res, err := opml.Import(r.Context(), s.db, doc, opml.ImportOptions{})
	if err != nil {
		s.serverError(w, "starter feeds", err)
		return
	}
	if res.FoldersCreated > 0 {
		s.publishFolderChanged(0)
	}
	var runID any
	if len(res.NewFeedIDs) > 0 {
		info, err := s.opt.Sched.StartImport(res.NewFeedIDs)
		switch {
		case err == nil:
			runID = fmt.Sprint(info.RunID)
		case errors.Is(err, sched.ErrStopped):
		default:
			s.log.Error("api: starter feeds import run", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": res.FeedsAdded, "existing": len(res.FeedsExisting), "run_id": runID})
}
