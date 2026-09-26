package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/ftrun"
	"github.com/WPTK/kipple/internal/sanitize"
)

// extractBudget is the whole synchronous extraction (design §7.1).
const extractBudget = 15 * time.Second

// transientRetryAfter is how long a stored transient extraction failure is
// reported as is before opening the article tries again (design §7.5).
const transientRetryAfter = time.Hour

type fulltextResponse struct {
	Mode        *int    `json:"mode"`
	Effective   int     `json:"effective"`
	Status      string  `json:"status"` // ok | error | skipped
	ContentHTML *string `json:"content_html"`
	WordCount   int64   `json:"word_count"`
	Error       *string `json:"error"`
}

// itemFulltext is POST /api/items/{id}/fulltext (design §7.1, §7.5).
//
// mode: 1 or 0 sets items.fulltext_mode, null clears it (follow the feed), an
// omitted field keeps it. When the effective mode is 1 and there is no stored
// extraction it extracts synchronously; ?refresh=1 forces a new attempt, which
// is also how a failed extraction is retried (a stored failure is reported as
// is, so opening the article does not refetch a page that already failed).
// The exception is a transient failure (timeout, connection error, 5xx, 429)
// last attempted over an hour ago, which the endpoint retries by itself;
// permanent ones (404, 403, not readable) and failures stored without a class
// stay sticky.
func (s *Server) itemFulltext(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var body struct {
		Mode json.RawMessage `json:"mode"`
	}
	if !decodeBody(w, r, &body, true) {
		return
	}
	setMode := false
	var mode *int
	switch strings.TrimSpace(string(body.Mode)) {
	case "":
	case "null":
		setMode = true
	case "1", "0":
		setMode = true
		m := int(strings.TrimSpace(string(body.Mode))[0] - '0')
		mode = &m
	default:
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	ctx := r.Context()

	if setMode {
		found, err := s.db.SetFulltextMode(ctx, id, mode)
		if err != nil {
			s.serverError(w, "fulltext mode", err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
	}
	it, found, err := s.db.GetFulltextItem(ctx, id)
	if err != nil {
		s.serverError(w, "fulltext", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found") // includes restore stubs: they have no live row to extract for
		return
	}

	resp := fulltextResponse{Mode: it.Mode, Effective: it.Effective, Status: "skipped"}
	hasGood := it.HasRow && it.HTML != ""
	var html string
	switch {
	case it.Effective != 1:
	case hasGood && !refresh:
		resp.Status, html, resp.WordCount = "ok", it.HTML, it.Words
	case it.HasRow && !hasGood && it.Error != "" && !refresh &&
		!(it.ErrorTransient && s.now().Unix()-it.AttemptedAt > int64(transientRetryAfter/time.Second)):
		resp.Status, resp.Error = "error", &it.Error
	default:
		// One run per item across the whole process: the runner extracts, saves
		// and only then publishes the outcome, so an item the ingest pool is
		// extracting right now is joined rather than fetched twice, and the
		// per-host limit covers this fetch too. A joined run whose save was
		// refused (the pool's guard: full text turned off, URL changed) or cut off
		// by shutdown is not usable here, so this request runs its own once.
		var out ftrun.Outcome
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			out, err = s.runner.Run(ctx, ftrun.Request{Item: it, Now: s.now().Unix(), Timeout: extractBudget})
			if out.Joined && (errors.Is(err, ftrun.ErrAborted) || (err == nil && !out.Written)) {
				continue
			}
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return // the caller went away while waiting on a shared run
			}
			s.serverError(w, "fulltext save", err)
			return
		}
		if out.Save.Error == "" {
			resp.Status, html, resp.WordCount = "ok", out.Save.HTML, int64(out.Save.WordCount)
		} else {
			msg := out.Save.Error
			resp.Status, resp.Error = "error", &msg
			if hasGood { // a failed refresh keeps the earlier extraction
				html, resp.WordCount = it.HTML, it.Words
			}
		}
	}
	if html != "" {
		html = sanitize.ServeHTML(html, s.serveOptions(ctx, it.FeedID))
		resp.ContentHTML = &html
	}
	writeJSON(w, http.StatusOK, resp)
}
